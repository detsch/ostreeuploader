package ostree

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/foundriesio/ostreeuploader/pkg/gvariant"
)

// zlibFileHeaderFormat is the GVariant type of the header embedded in an archive
// .filez object: (size, uid, gid, mode, rdev, symlink-target, xattrs). The
// leading 't' (uncompressed size) is dropped to form the checksum header. uid/
// gid/mode/rdev are big-endian uint32. (libostree 2024.5,
// _OSTREE_ZLIB_FILE_HEADER_GVARIANT_FORMAT.)
const zlibFileHeaderFormat = "(tuuuusa(ayay))"

// fileHeader is the metadata of a content object, parsed from a .filez header.
type fileHeader struct {
	uid, gid, mode uint32
	symlink        string // symlink target, "" for regular files
	xattrs         []gvariant.Xattr
}

// isSymlink reports whether the header describes a symlink (mode S_IFLNK).
func (h fileHeader) isSymlink() bool { return h.mode&0o170000 == 0o120000 }

// beU32 reads a big-endian uint32 from the first 4 bytes of b.
func beU32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

// parseFilez splits an archive .filez object into its header and uncompressed
// content. Layout: [4B BE header_size][4B zero pad][header GVariant][raw-DEFLATE
// content]. The content of a symlink is empty (its target lives in the header).
func parseFilez(data []byte) (fileHeader, []byte, error) {
	if len(data) < 8 {
		return fileHeader{}, nil, fmt.Errorf("filez too short: %d bytes", len(data))
	}
	hdrSize := binary.BigEndian.Uint32(data[0:4])
	// data[4:8] is 4 zero padding bytes.
	end := 8 + uint64(hdrSize)
	if end > uint64(len(data)) {
		return fileHeader{}, nil, fmt.Errorf("filez header size %d exceeds object", hdrSize)
	}
	hv, err := gvariant.New(data[8:end], zlibFileHeaderFormat)
	if err != nil {
		return fileHeader{}, nil, fmt.Errorf("decode filez header: %w", err)
	}
	h := fileHeader{
		uid:     beU32(hv.Child(1).Raw()),
		gid:     beU32(hv.Child(2).Raw()),
		mode:    beU32(hv.Child(3).Raw()),
		symlink: hv.Child(5).Str(),
		xattrs:  parseXattrs(hv.Child(6)),
	}
	if err := hv.Err(); err != nil {
		return fileHeader{}, nil, fmt.Errorf("decode filez header: %w", err)
	}

	var content []byte
	if !h.isSymlink() {
		zr := flate.NewReader(bytes.NewReader(data[end:]))
		content, err = io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return fileHeader{}, nil, fmt.Errorf("inflate filez content: %w", err)
		}
	}
	return h, content, nil
}

// parseXattrs extracts an a(ayay) xattr array into gvariant.Xattr values. The
// names retain no trailing NUL (it is re-added on encode).
func parseXattrs(arr *gvariant.Value) []gvariant.Xattr {
	n := arr.Len()
	if n == 0 {
		return nil
	}
	out := make([]gvariant.Xattr, 0, n)
	for i := 0; i < n; i++ {
		e := arr.At(i)
		name := e.Child(0).Bytes()
		// Stored names are NUL-terminated; strip it for the in-memory form.
		if len(name) > 0 && name[len(name)-1] == 0 {
			name = name[:len(name)-1]
		}
		out = append(out, gvariant.Xattr{
			Name:  append([]byte(nil), name...),
			Value: append([]byte(nil), e.Child(1).Bytes()...),
		})
	}
	return out
}

// contentObjectID computes the 64-hex checksum (and thus object filename) of a
// content object, the same way ostree does: sha256 over the length-prefixed
// uncompressed header (uuuusa(ayay)) followed by the raw uncompressed content.
// The length prefix is [4B BE variant_size][4B zero pad].
func contentObjectID(h fileHeader, content []byte) string {
	hdr := gvariant.EncodeFileHeader(h.uid, h.gid, h.mode, h.symlink, h.xattrs)

	sum := sha256.New()
	var prefix [8]byte
	binary.BigEndian.PutUint32(prefix[0:4], uint32(len(hdr)))
	sum.Write(prefix[:]) // 4B size + 4B zero padding
	sum.Write(hdr)
	if !h.isSymlink() {
		sum.Write(content)
	}
	return hex.EncodeToString(sum.Sum(nil))
}
