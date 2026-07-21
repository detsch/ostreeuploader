package ostree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/foundriesio/ostreeuploader/pkg/gvariant"
)

// ErrNoDelta reports that the remote publishes no static delta for the requested
// from->to pair, so the caller should fall back (to a full pull, or to a
// different size estimate). It is distinct from a transport/parse failure.
var ErrNoDelta = errors.New("no static delta for commit pair")

// superblockFormat is the GVariant type of a static-delta superblock, pinned to
// libostree 2024.5 (OSTREE_STATIC_DELTA_SUPERBLOCK_FORMAT in
// ostree-repo-static-delta-private.h, with OSTREE_COMMIT_GVARIANT_STRING and the
// meta-entry / fallback formats expanded):
//
//	(a{sv} t ay ay <commit> ay a(uayttay) a(yaytt))
//
// Top-level children:
//
//	0 a{sv}        metadata
//	1 t            timestamp (big-endian)
//	2 ay           from checksum
//	3 ay           to checksum
//	4 <commit>     new commit object
//	5 ay           recursion (from,to) digests
//	6 a(uayttay)   meta entries: (u version, ay csum, t size, t usize, ay objs)
//	7 a(yaytt)     fallback: (y objtype, ay csum, t compressed, t uncompressed)
//
// The drift guard is delta_test.go, which cross-checks parsed totals against
// `ostree static-delta show`.
const superblockFormat = "(a{sv}tayay(a{sv}aya(say)sstayay)aya(uayttay)a(yaytt))"

const (
	sbMetaEntries = 6
	sbFallback    = 7
)

// DeltaSize holds the storage figures for a static delta. They match what
// `ostree static-delta show` reports as Total Size / Total Uncompressed Size.
type DeltaSize struct {
	// Compressed is the total download size (delta parts + fallback objects).
	Compressed uint64 `json:"compressed"`
	// Uncompressed is the total on-disk size of the objects the delta produces.
	// This is the figure to compare against free space before applying an update.
	Uncompressed uint64 `json:"uncompressed"`
}

// LocalDeltaSize reads the from->to static delta's size by parsing its
// superblock directly from the local repository. The superblock must be present
// (e.g. an offline update bundle); a missing delta yields an error. No `ostree`
// process is involved.
func (r *Repo) LocalDeltaSize(from, to string) (DeltaSize, error) {
	dir, err := staticDeltaDir(from, to)
	if err != nil {
		return DeltaSize{}, err
	}
	path := filepath.Join(r.path, filepath.FromSlash(dir), "superblock")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DeltaSize{}, fmt.Errorf("static delta %s..%s not found in repo", from, to)
		}
		return DeltaSize{}, fmt.Errorf("read superblock: %w", err)
	}
	return parseSuperblock(data)
}

// RemoteDeltaSize reads the from->to static delta's size from a remote repo by
// fetching only its superblock (not the delta parts) and parsing it. This is the
// online counterpart to LocalDeltaSize: it lets a caller estimate an update's
// required storage before committing to the download, without the delta needing
// to be present locally.
//
// It returns ErrNoDelta (wrapped) when the remote publishes no delta for the
// pair (the superblock 404s / is absent), so callers can distinguish "no delta"
// from a real transport or parse error.
func RemoteDeltaSize(ctx context.Context, rc RemoteConfig, from, to string) (DeltaSize, error) {
	dir, err := staticDeltaDir(from, to)
	if err != nil {
		return DeltaSize{}, err
	}
	t, err := newTransport(rc)
	if err != nil {
		return DeltaSize{}, err
	}
	sbRel := path.Join(dir, "superblock")
	sbReader, _, err := t.open(ctx, sbRel, 0)
	if err != nil {
		if isNotFound(err) {
			return DeltaSize{}, fmt.Errorf("%w: %s..%s", ErrNoDelta, from, to)
		}
		return DeltaSize{}, fmt.Errorf("fetch delta superblock: %w", err)
	}
	defer sbReader.Close()
	data, err := io.ReadAll(sbReader)
	if err != nil {
		return DeltaSize{}, fmt.Errorf("read delta superblock: %w", err)
	}
	return parseSuperblock(data)
}

// newSuperblock wraps a static-delta superblock blob as a GVariant.
func newSuperblock(data []byte) (*gvariant.Value, error) {
	return gvariant.New(data, superblockFormat)
}

// parseSuperblock sums the meta-entry and fallback sizes from a superblock blob,
// the same way `ostree static-delta show` computes its totals.
func parseSuperblock(data []byte) (DeltaSize, error) {
	sb, err := gvariant.New(data, superblockFormat)
	if err != nil {
		return DeltaSize{}, err
	}

	var ds DeltaSize

	// Meta entries: (u version, ay csum, t size, t usize, ay objects).
	meta := sb.Child(sbMetaEntries)
	for i := 0; i < meta.Len(); i++ {
		e := meta.At(i)
		ds.Compressed += e.Child(2).Uint64()
		ds.Uncompressed += e.Child(3).Uint64()
	}

	// Fallback objects: (y objtype, ay csum, t compressed, t uncompressed).
	fb := sb.Child(sbFallback)
	for i := 0; i < fb.Len(); i++ {
		e := fb.At(i)
		ds.Compressed += e.Child(2).Uint64()
		ds.Uncompressed += e.Child(3).Uint64()
	}

	if err := sb.Err(); err != nil {
		return DeltaSize{}, fmt.Errorf("parse superblock: %w", err)
	}
	return ds, nil
}
