//go:build linux

package ostree

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/foundriesio/ostreeuploader/pkg/gvariant"
	"github.com/ulikunitz/xz"
)

// Static-delta operation opcodes (libostree OSTREE_STATIC_DELTA_OP_*, ASCII).
const (
	opOpenSpliceAndClose = 'S' // 0x53
	opOpen               = 'o' // 0x6f
	opWrite              = 'w' // 0x77
	opSetReadSource      = 'r' // 0x72
	opUnsetReadSource    = 'R' // 0x52
	opClose              = 'c' // 0x63
	opBspatch            = 'B' // 0x42
)

// ostree object types (ostree-core.h). Meta types are 2..6.
const (
	objFile       = 1
	objDirTree    = 2
	objDirMeta    = 3
	objCommit     = 4
	objCommitMeta = 6
)

const objCsumLen = 33 // 1 byte objtype + 32 byte sha256

// partPayloadFormat is the GVariant type of a (decompressed) static-delta part
// payload (OSTREE_STATIC_DELTA_PART_PAYLOAD_FORMAT_V0):
// (modes a(uuu), xattrs aa(ayay), data-source ay, operations ay).
const partPayloadFormat = "(a(uuu)aa(ayay)ayay)"

// deltaObject is one (objtype, hex-checksum) record from a meta entry's object
// list, identifying an object the part produces (consumed in order).
type deltaObject struct {
	objType byte
	csum    string
}

// deltaPart is a decoded static-delta part payload.
type deltaPart struct {
	modes  [][3]uint32 // (uid, gid, mode) triples, host order (already BE-decoded)
	xattrs []*gvariant.Value
	data   []byte // data-source blob
	ops    []byte // operation bytecode
}

// applyStaticDelta applies the from->to static delta present in the local repo
// (superblock + part files) by interpreting each part's operations, writing the
// produced objects via the repo's bare-user(-only) writer. The commit and all
// objects it references end up present and verified.
func (r *Repo) applyStaticDelta(from, to string) error {
	dir, err := staticDeltaDir(from, to)
	if err != nil {
		return err
	}
	base := filepath.Join(r.path, filepath.FromSlash(dir))
	sbData, err := os.ReadFile(filepath.Join(base, "superblock"))
	if err != nil {
		return fmt.Errorf("read superblock: %w", err)
	}
	sb, err := newSuperblock(sbData)
	if err != nil {
		return err
	}
	meta := sb.Child(sbMetaEntries)
	n := meta.Len()
	for i := 0; i < n; i++ {
		entry := meta.At(i)
		wantCsum := hex.EncodeToString(entry.Child(1).Bytes())
		objs, err := parseObjectList(entry.Child(4).Bytes())
		if err != nil {
			return fmt.Errorf("delta part %d: %w", i, err)
		}
		partRaw, err := os.ReadFile(filepath.Join(base, fmt.Sprintf("%d", i)))
		if err != nil {
			return fmt.Errorf("read delta part %d: %w", i, err)
		}
		// The meta entry's checksum is the SHA-256 of the whole raw part file.
		if got := hex.EncodeToString(sha256Sum(partRaw)); got != wantCsum {
			return fmt.Errorf("delta part %d: checksum mismatch (got %s want %s)", i, got, wantCsum)
		}
		part, err := decodePart(partRaw)
		if err != nil {
			return fmt.Errorf("delta part %d: %w", i, err)
		}
		if err := r.execDeltaPart(part, objs); err != nil {
			return fmt.Errorf("delta part %d: %w", i, err)
		}
	}
	if err := sb.Err(); err != nil {
		return fmt.Errorf("superblock: %w", err)
	}
	return nil
}

// parseObjectList splits a meta entry's trailing flat byte array into
// (objtype, checksum) records.
func parseObjectList(raw []byte) ([]deltaObject, error) {
	if len(raw)%objCsumLen != 0 {
		return nil, fmt.Errorf("object list length %d not a multiple of %d", len(raw), objCsumLen)
	}
	out := make([]deltaObject, 0, len(raw)/objCsumLen)
	for off := 0; off < len(raw); off += objCsumLen {
		out = append(out, deltaObject{
			objType: raw[off],
			csum:    hex.EncodeToString(raw[off+1 : off+objCsumLen]),
		})
	}
	return out, nil
}

// decodePart strips the leading compression byte, decompresses (XZ for 'x'),
// and parses the part payload GVariant.
func decodePart(raw []byte) (*deltaPart, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty part")
	}
	comp := raw[0]
	body := raw[1:]
	switch comp {
	case 0:
		// uncompressed; GVariant follows directly
	case 'x':
		xr, err := xz.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("xz: %w", err)
		}
		body, err = io.ReadAll(xr)
		if err != nil {
			return nil, fmt.Errorf("xz decode: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported part compression %#x", comp)
	}

	pv, err := gvariant.New(body, partPayloadFormat)
	if err != nil {
		return nil, err
	}
	p := &deltaPart{
		data: append([]byte(nil), pv.Child(2).Bytes()...),
		ops:  append([]byte(nil), pv.Child(3).Bytes()...),
	}
	modes := pv.Child(0)
	for i := 0; i < modes.Len(); i++ {
		m := modes.At(i)
		// The (uuu) ints are stored big-endian ("non-canonical"); the gvariant
		// decoder returns them little-endian, so byte-swap.
		p.modes = append(p.modes, [3]uint32{
			swapU32(m.Child(0).Uint32()),
			swapU32(m.Child(1).Uint32()),
			swapU32(m.Child(2).Uint32()),
		})
	}
	xa := pv.Child(1)
	for i := 0; i < xa.Len(); i++ {
		p.xattrs = append(p.xattrs, xa.At(i))
	}
	if err := pv.Err(); err != nil {
		return nil, err
	}
	return p, nil
}

func swapU32(x uint32) uint32 {
	return (x>>24)&0xff | (x>>8)&0xff00 | (x<<8)&0xff0000 | (x<<24)&0xff000000
}
