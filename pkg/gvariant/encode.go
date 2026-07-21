package gvariant

// This file implements the minimal slice of GVariant *serialization* that
// fiopull needs in order to write bare-user content objects: the two file
// header GVariants ostree computes checksums and xattrs from. It is not a
// general encoder; it exposes just the two concrete shapes, built on small
// generic tuple/array serializers that follow the same framing rules as the
// decoder in type.go (GLib gvariant-serialiser.c).
//
// IMPORTANT endianness note: ostree stores uid/gid/mode/rdev in these headers
// in big-endian (libostree passes GUINT32_TO_BE(...) into g_variant_new), which
// is the opposite of GVariant's normal little-endian integer encoding. The
// encoders below therefore emit those specific uint32 fields big-endian, to
// match the on-disk bytes (verified against libostree 2024.5 output).

// Xattr is a single extended attribute: a NUL-terminated name and a raw value,
// matching ostree's a(ayay) xattr representation (the name's trailing NUL is
// included in Name's bytes by the caller via EncodeFile*; see below).
type Xattr struct {
	Name  []byte // attribute name, WITHOUT trailing NUL (added during encoding)
	Value []byte
}

// EncodeFileHeader serializes the uncompressed ostree file header GVariant
// "(uuuusa(ayay))" = (uid, gid, mode, rdev, symlinkTarget, xattrs). rdev is
// always 0. This is the buffer ostree length-prefixes and hashes to derive a
// content object's checksum.
func EncodeFileHeader(uid, gid, mode uint32, symlinkTarget string, xattrs []Xattr) []byte {
	members := []member{
		{data: beU32(uid), align: 4, fixed: true},
		{data: beU32(gid), align: 4, fixed: true},
		{data: beU32(mode), align: 4, fixed: true},
		{data: beU32(0), align: 4, fixed: true}, // rdev
		{data: encodeString(symlinkTarget), align: 1, fixed: false},
		{data: encodeXattrs(xattrs), align: 1, fixed: false},
	}
	return serTuple(members)
}

// EncodeFileMeta serializes the ostree "(uuua(ayay))" filemeta GVariant =
// (uid, gid, mode, xattrs). This is the value stored in the bare-user
// "user.ostreemeta" xattr.
func EncodeFileMeta(uid, gid, mode uint32, xattrs []Xattr) []byte {
	members := []member{
		{data: beU32(uid), align: 4, fixed: true},
		{data: beU32(gid), align: 4, fixed: true},
		{data: beU32(mode), align: 4, fixed: true},
		{data: encodeXattrs(xattrs), align: 1, fixed: false},
	}
	return serTuple(members)
}

// member is one serialized tuple member: its bytes, alignment, and whether it is
// fixed-size (fixed members never carry a framing offset).
type member struct {
	data  []byte
	align int
	fixed bool
}

// beU32 encodes x as 4 big-endian bytes (see endianness note above).
func beU32(x uint32) []byte {
	return []byte{byte(x >> 24), byte(x >> 16), byte(x >> 8), byte(x)}
}

// encodeString serializes a GVariant string 's': the bytes plus a trailing NUL.
func encodeString(s string) []byte {
	b := make([]byte, 0, len(s)+1)
	b = append(b, s...)
	b = append(b, 0)
	return b
}

// encodeXattrs serializes an a(ayay) array of (name, value) byte-array pairs. An
// empty array serializes to zero bytes. The name's terminating NUL is appended
// here, matching how ostree stores xattr names.
func encodeXattrs(xattrs []Xattr) []byte {
	if len(xattrs) == 0 {
		return nil
	}
	elems := make([][]byte, len(xattrs))
	for i, xa := range xattrs {
		name := make([]byte, 0, len(xa.Name)+1)
		name = append(name, xa.Name...)
		name = append(name, 0)
		// (ayay): two variable byte-array members; the first (name) carries a
		// framing offset, the last (value) does not.
		elems[i] = serTuple([]member{
			{data: name, align: 1, fixed: false},
			{data: xa.Value, align: 1, fixed: false},
		})
	}
	return serArrayVar(elems, 1)
}

// padTo grows b with zero bytes until its length is a multiple of align.
func padTo(b []byte, align int) []byte {
	for align > 1 && len(b)%align != 0 {
		b = append(b, 0)
	}
	return b
}

// putLE writes v as size little-endian bytes into b[:size].
func putLE(b []byte, v uint64, size int) {
	for i := 0; i < size; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

// offsetSizeFor mirrors GLib's gvs_get_offset_size: the width needed to store an
// offset into a container of the given total size.
func offsetSizeFor(size int) int {
	switch {
	case size > 0xFFFFFFFF:
		return 8
	case size > 0xFFFF:
		return 4
	case size > 0xFF:
		return 2
	case size > 0:
		return 1
	default:
		return 0
	}
}

// chooseOffsetSize finds the smallest framing-offset width that is self
// consistent: large enough to address the container once its own offset bytes
// are included.
func chooseOffsetSize(bodyLen, nOff int) int {
	sz := 1
	for {
		total := bodyLen + nOff*sz
		need := offsetSizeFor(total)
		if need <= sz {
			return sz
		}
		sz = need
	}
}

// serTuple serializes tuple members per the GVariant framing rules. Variable
// -size members other than the final member each contribute a framing offset
// (the byte position of their end); those offsets are stored at the tail in
// reverse framing order (framing index 0 rightmost).
func serTuple(members []member) []byte {
	var body []byte
	var offsets []int // end positions, in framing-index order
	n := len(members)
	for i, m := range members {
		body = padTo(body, m.align)
		body = append(body, m.data...)
		if !m.fixed && i != n-1 {
			offsets = append(offsets, len(body))
		}
	}
	nOff := len(offsets)
	if nOff == 0 {
		return body
	}
	offSize := chooseOffsetSize(len(body), nOff)
	out := make([]byte, len(body)+nOff*offSize)
	copy(out, body)
	size := len(out)
	for k := 0; k < nOff; k++ {
		putLE(out[size-offSize*(k+1):], uint64(offsets[k]), offSize)
	}
	return out
}

// serArrayVar serializes an array of variable-size elements. Each element is
// aligned to elemAlign; one framing offset per element (its end position) is
// stored at the tail in forward order. An empty array is zero bytes.
func serArrayVar(elems [][]byte, elemAlign int) []byte {
	var body []byte
	offsets := make([]int, 0, len(elems))
	for _, e := range elems {
		body = padTo(body, elemAlign)
		body = append(body, e...)
		offsets = append(offsets, len(body))
	}
	n := len(offsets)
	if n == 0 {
		return nil
	}
	offSize := chooseOffsetSize(len(body), n)
	out := make([]byte, len(body)+n*offSize)
	copy(out, body)
	base := len(body)
	for k := 0; k < n; k++ {
		putLE(out[base+k*offSize:], uint64(offsets[k]), offSize)
	}
	return out
}
