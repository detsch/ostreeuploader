//go:build linux

package ostree

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path"

	"github.com/foundriesio/ostreeuploader/pkg/gvariant"
	"github.com/foundriesio/ostreeuploader/pkg/varint"
)

// PullSize reports the estimated download size of a full (non-delta) pull of a
// commit: the sum of the compressed on-wire sizes of every object the commit
// references. Metadata is the commit/dirtree/dirmeta objects; Content is the
// .filez file objects.
type PullSize struct {
	// Compressed is the total number of bytes that would be downloaded (the sum
	// of every referenced object's on-wire size). This is the figure to compare
	// against network/transfer budgets.
	Compressed uint64 `json:"compressed"`
	// Uncompressed is the total decompressed size of the objects. It is only
	// populated when the estimate came from the commit's ostree.sizes metadata
	// (FromSizesMeta true); it is 0 otherwise.
	Uncompressed uint64 `json:"uncompressed"`
	// MetaObjects / ContentObjects count the objects of each kind.
	MetaObjects    int `json:"meta_objects"`
	ContentObjects int `json:"content_objects"`
	// FromSizesMeta reports whether the sizes came from the commit's ostree.sizes
	// metadata (a single fetch) rather than probing every object over the network.
	FromSizesMeta bool `json:"from_sizes_meta"`
}

// ResolveRemoteRef fetches refs/heads/<ref> from a remote repo and returns the
// commit checksum it points to.
func ResolveRemoteRef(ctx context.Context, rc RemoteConfig, ref string) (string, error) {
	t, err := newTransport(rc)
	if err != nil {
		return "", err
	}
	rd, _, err := t.open(ctx, path.Join("refs", "heads", ref), 0)
	if err != nil {
		return "", fmt.Errorf("resolve remote ref %q: %w", ref, err)
	}
	defer rd.Close()
	data, err := io.ReadAll(rd)
	if err != nil {
		return "", fmt.Errorf("resolve remote ref %q: %w", ref, err)
	}
	csum := trimRef(data)
	if !hexCsumRe.MatchString(csum) {
		return "", fmt.Errorf("remote ref %q: %q is not a commit checksum", ref, csum)
	}
	return csum, nil
}

// RemotePullSize estimates the download size of a full pull of commit from a
// remote archive repo, WITHOUT downloading object bodies.
//
// It requires the commit to have been built with `--generate-sizes`, so its
// metadata carries an "ostree.sizes" table of every referenced object's
// compressed and uncompressed size. The size is then computed from the commit
// object alone (one fetch), and PullSize.Uncompressed is populated. When the
// commit has no ostree.sizes metadata, ErrSizeUnavailable is returned.
//
// localRepo, if non-nil, is used to skip objects that are already present on
// disk — so the returned size reflects only what would actually be downloaded,
// not a full fresh-pull total.
func RemotePullSize(ctx context.Context, rc RemoteConfig, commit string, localRepo *Repo) (PullSize, error) {
	if !hexCsumRe.MatchString(commit) {
		return PullSize{}, fmt.Errorf("pull-size: %q is not a commit checksum", commit)
	}
	t, err := newTransport(rc)
	if err != nil {
		return PullSize{}, err
	}
	e := &pullSizer{t: t, local: localRepo}

	// Fetch the commit object and measure its on-wire size from the body length
	// (avoids a separate HEAD, which some servers — e.g. Foundries treehub —
	// do not support).
	cv, commitSz, err := e.fetchMetaObject(ctx, commit, "commit", commitFormat)
	if err != nil {
		return PullSize{}, fmt.Errorf("commit %s: %w", commit, err)
	}

	// Use the ostree.sizes metadata table if the commit carries it.
	// Pass 0 for commitSz when the commit is already local so we don't count it.
	localCommitSz := commitSz
	if e.hasLocally(commit, "commit") {
		localCommitSz = 0
	}
	if ps, ok, err := pullSizeFromMeta(cv, localCommitSz, e.local); err != nil {
		return PullSize{}, fmt.Errorf("commit %s ostree.sizes: %w", commit, err)
	} else if ok {
		return ps, nil
	}

	// The commit lacks ostree.sizes; no cheap source is available.
	return PullSize{}, ErrSizeUnavailable
}

// pullSizeFromMeta computes the pull size from a commit's "ostree.sizes"
// metadata, if present. commitSz is the on-wire size of the commit object
// itself (not listed in ostree.sizes), already conditionally counted by the
// caller. local, if non-nil, is used to skip objects already present on disk.
// It returns ok=false when the commit has no ostree.sizes key (the caller then
// falls back to walking).
//
// Each ostree.sizes entry (GVariant "ay") is: 32-byte sha256 checksum, a
// varuint64 compressed ("archived") size, a varuint64 uncompressed ("unpacked")
// size, and an optional trailing object-type byte. Mirrors libostree's
// read_sizes_entry (ostree-core.c).
func pullSizeFromMeta(commitVariant *gvariant.Value, commitSz uint64, local *Repo) (PullSize, bool, error) {
	sizes, ok := lookupMetadata(commitVariant.Child(0), "ostree.sizes")
	if !ok {
		return PullSize{}, false, nil
	}
	// The variant holds an array of byte-arrays ("aay").
	arr := sizes.Variant()
	n := arr.Len()
	// Seed with the commit object size/count, already zeroed by caller when local.
	commitObjCount := 1
	if commitSz == 0 {
		commitObjCount = 0
	}
	ps := PullSize{
		Compressed:    commitSz,
		MetaObjects:   commitObjCount,
		FromSizesMeta: true,
	}
	for i := 0; i < n; i++ {
		entry := arr.At(i).Bytes()
		if len(entry) < objCsumSizeLen+2 {
			return PullSize{}, false, fmt.Errorf("entry %d too short (%d bytes)", i, len(entry))
		}
		csumHex := hex.EncodeToString(entry[:objCsumSizeLen])
		buf := entry[objCsumSizeLen:] // skip the 32-byte checksum
		archived, n1, err := varint.Uvarint(buf)
		if err != nil {
			return PullSize{}, false, fmt.Errorf("entry %d archived size: %w", i, err)
		}
		buf = buf[n1:]
		unpacked, n2, err := varint.Uvarint(buf)
		if err != nil {
			return PullSize{}, false, fmt.Errorf("entry %d unpacked size: %w", i, err)
		}
		buf = buf[n2:]
		// Optional object-type byte; default to a file object when absent.
		objType := byte(objFile)
		if len(buf) > 0 {
			objType = buf[0]
		}
		// Skip objects already present in the local repo.
		if local != nil && local.hasObject(csumHex, objExtForType(objType)) {
			continue
		}
		ps.Compressed += archived
		ps.Uncompressed += unpacked
		if objType == byte(objFile) {
			ps.ContentObjects++
		} else {
			ps.MetaObjects++
		}
	}
	if err := arr.Err(); err != nil {
		return PullSize{}, false, err
	}
	return ps, true, nil
}

// lookupMetadata finds a key in a commit's a{sv} metadata dictionary, returning
// the associated value (still wrapped as a variant) and whether it was found.
func lookupMetadata(meta *gvariant.Value, key string) (*gvariant.Value, bool) {
	for i := 0; i < meta.Len(); i++ {
		ent := meta.At(i)
		if ent.Child(0).Str() == key {
			return ent.Child(1), true
		}
	}
	return nil, false
}

// objCsumSizeLen is the length of the binary sha256 checksum prefixing each
// ostree.sizes entry.
const objCsumSizeLen = 32

// pullSizer holds the transport and optional local repo for RemotePullSize.
type pullSizer struct {
	t     transport
	local *Repo // optional; when set, objects present here are skipped
}

// hasLocally reports whether the object is already present in the local repo.
func (e *pullSizer) hasLocally(csum, ext string) bool {
	return e.local != nil && e.local.hasObject(csum, ext)
}

// objExtForType maps an ostree object-type byte to its loose-object file
// extension in a bare-user or bare-user-only repo.
func objExtForType(t byte) string {
	switch int(t) {
	case objFile:
		return "file"
	case objDirTree:
		return "dirtree"
	case objDirMeta:
		return "dirmeta"
	case objCommit:
		return "commit"
	case objCommitMeta:
		return "commitmeta"
	default:
		return "file"
	}
}

// fetchMetaObject downloads a metadata object, decodes it as the given GVariant
// type, and returns both the value and the on-wire byte count (len of raw body).
// This avoids a separate HEAD request, so it works against servers that only
// support GET (e.g. Foundries treehub).
func (e *pullSizer) fetchMetaObject(ctx context.Context, csum, ext, format string) (*gvariant.Value, uint64, error) {
	rc, _, err := e.t.open(ctx, objectRelPath(csum, ext), 0)
	if err != nil {
		return nil, 0, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, 0, err
	}
	v, err := gvariant.New(data, format)
	if err != nil {
		return nil, 0, fmt.Errorf("decode %s object %s: %w", ext, csum, err)
	}
	return v, uint64(len(data)), nil
}
