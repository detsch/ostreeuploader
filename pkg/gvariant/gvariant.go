// Package gvariant is a minimal, pure-Go deserializer for the GVariant binary
// serialization format (little-endian), sufficient for reading the GVariant
// objects ostree stores on disk: static-delta superblocks, commits, dirtrees
// and dirmeta.
//
// It implements the framing rules from GLib's gvariant-serialiser.c /
// gvarianttypeinfo.c (the "magic constant" tuple member-bounds algorithm and
// the variable-sized-array framing-offset scheme). No cgo, no GLib.
//
// The API is value-oriented with a deferred error: navigate with Child/At, read
// leaves with Uint64/Bytes/Str, then check Err once at the end. Any access on a
// Value that already carries an error is a no-op returning the zero value, so a
// chain of accesses never panics on malformed input.
package gvariant

import (
	"errors"
	"fmt"
)

// Value is a GVariant value: a type plus the byte slice holding its serialized
// form. The zero Value is not usable; obtain one from New.
type Value struct {
	typ  *Type
	data []byte
	err  error
}

// New parses the type signature typ and wraps data as a Value of that type.
// It does not eagerly validate framing; malformed framing surfaces as an error
// from the accessor that touches it (retrievable via Err).
func New(data []byte, typ string) (*Value, error) {
	t, n, err := parseType(typ, 0)
	if err != nil {
		return nil, err
	}
	if n != len(typ) {
		return nil, fmt.Errorf("gvariant: trailing characters in type %q", typ)
	}
	return &Value{typ: t, data: data}, nil
}

// Err returns the first error encountered while navigating from this Value
// (and its ancestors). Check it once after a chain of accesses.
func (v *Value) Err() error { return v.err }

func (v *Value) fail(format string, a ...any) *Value {
	if v.err == nil {
		v.err = fmt.Errorf("gvariant: "+format, a...)
	}
	return v
}

// Type returns the value's type signature, e.g. "(uayttay)".
func (v *Value) Type() string { return v.typ.String() }

// readLE reads a little-endian unsigned integer from the first size bytes of b.
func readLE(b []byte, size uint64) uint64 {
	var x uint64
	for i := uint64(0); i < size; i++ {
		x |= uint64(b[i]) << (8 * i)
	}
	return x
}

// offsetSize returns the width in bytes of each framing offset for a container
// whose total serialized size is size (GLib's gvs_get_offset_size).
func offsetSize(size uint64) uint64 {
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

// alignUp rounds x up to the next multiple of align (a power of two).
func alignUp(x, align uint64) uint64 {
	return x + ((-x) & (align - 1))
}

// Child returns the i-th member of a tuple (or dict-entry) value.
func (v *Value) Child(i int) *Value {
	if v.err != nil {
		return v
	}
	if v.typ.kind != kindTuple && v.typ.kind != kindDict {
		return (&Value{err: v.err}).fail("Child on non-tuple type %q", v.typ.String())
	}
	members := v.typ.members()
	if i < 0 || i >= len(members) {
		return (&Value{}).fail("tuple child %d out of range (%d members)", i, len(members))
	}
	m := members[i]
	size := uint64(len(v.data))
	os := offsetSize(size)

	// Start of the member: read the framing offset that marks the end of the
	// previous variable-sized member (if any), then apply the precomputed
	// align/add constants. (GLib gvs_tuple_get_member_bounds.)
	var raw uint64
	if m.i+1 != 0 && os*uint64(m.i+1) <= size {
		raw = readLE(v.data[size-os*uint64(m.i+1):], os)
	}
	start := ((raw + m.a) & m.b) | m.c

	var end uint64
	switch m.ending {
	case endingLast:
		if os*uint64(m.i+1) <= size {
			end = size - os*uint64(m.i+1)
		} else {
			return (&Value{}).fail("tuple member %d: bad LAST framing", i)
		}
	case endingFixed:
		end = start + m.fixed
	case endingOffset:
		if os*uint64(m.i+2) <= size {
			end = readLE(v.data[size-os*uint64(m.i+2):], os)
		} else {
			return (&Value{}).fail("tuple member %d: bad OFFSET framing", i)
		}
	}
	if start > end || end > size {
		return (&Value{}).fail("tuple member %d: bounds [%d,%d] exceed size %d", i, start, end, size)
	}
	return &Value{typ: m.typ, data: v.data[start:end]}
}

// Len returns the number of elements in an array value.
func (v *Value) Len() int {
	if v.err != nil {
		return 0
	}
	if v.typ.kind != kindArray {
		v.fail("Len on non-array type %q", v.typ.String())
		return 0
	}
	size := uint64(len(v.data))
	elem := v.typ.elem
	if elem.fixed != 0 {
		// Array of fixed-size elements: no framing offsets.
		if size%elem.fixed != 0 {
			v.fail("fixed array size %d not a multiple of element size %d", size, elem.fixed)
			return 0
		}
		return int(size / elem.fixed)
	}
	if size == 0 {
		return 0
	}
	os := offsetSize(size)
	lastEnd := readLE(v.data[size-os:], os)
	if lastEnd > size {
		v.fail("array framing: last offset %d exceeds size %d", lastEnd, size)
		return 0
	}
	offsetsBytes := size - lastEnd
	if offsetsBytes%os != 0 {
		v.fail("array framing: offset region %d not a multiple of offset size %d", offsetsBytes, os)
		return 0
	}
	return int(offsetsBytes / os)
}

// At returns the i-th element of an array value.
func (v *Value) At(i int) *Value {
	if v.err != nil {
		return v
	}
	if v.typ.kind != kindArray {
		return (&Value{}).fail("At on non-array type %q", v.typ.String())
	}
	size := uint64(len(v.data))
	elem := v.typ.elem
	if elem.fixed != 0 {
		start := uint64(i) * elem.fixed
		end := start + elem.fixed
		if end > size {
			return (&Value{}).fail("fixed array element %d out of range", i)
		}
		return &Value{typ: elem, data: v.data[start:end]}
	}
	// Variable-sized elements: framing offsets live at the tail.
	os := offsetSize(size)
	lastEnd := readLE(v.data[size-os:], os)
	arr := v.data[lastEnd:]
	var start uint64
	if i > 0 {
		start = readLE(arr[os*uint64(i-1):], os)
		start = alignUp(start, elem.align)
	}
	end := readLE(arr[os*uint64(i):], os)
	if start > end || end > size {
		return (&Value{}).fail("array element %d: bounds [%d,%d] exceed size %d", i, start, end, size)
	}
	return &Value{typ: elem, data: v.data[start:end]}
}

// Variant unwraps a variant ('v') value into its contained Value.
func (v *Value) Variant() *Value {
	if v.err != nil {
		return v
	}
	if v.typ.kind != kindVariant {
		return (&Value{}).fail("Variant on non-variant type %q", v.typ.String())
	}
	// Serialized form: <child data> 0x00 <type signature>.
	sep := -1
	for i := len(v.data) - 1; i >= 0; i-- {
		if v.data[i] == 0 {
			sep = i
			break
		}
	}
	if sep < 0 {
		return (&Value{}).fail("variant: no type separator")
	}
	child, err := New(v.data[:sep], string(v.data[sep+1:]))
	if err != nil {
		return (&Value{}).fail("variant: %v", err)
	}
	return child
}

// Uint64 reads a 't' (uint64) leaf.
func (v *Value) Uint64() uint64 {
	if v.err != nil {
		return 0
	}
	if len(v.data) < 8 {
		v.fail("Uint64: need 8 bytes, have %d", len(v.data))
		return 0
	}
	return readLE(v.data, 8)
}

// Uint32 reads a 'u' (uint32) leaf.
func (v *Value) Uint32() uint32 {
	if v.err != nil {
		return 0
	}
	if len(v.data) < 4 {
		v.fail("Uint32: need 4 bytes, have %d", len(v.data))
		return 0
	}
	return uint32(readLE(v.data, 4))
}

// Byte reads a 'y' (uint8) leaf.
func (v *Value) Byte() byte {
	if v.err != nil {
		return 0
	}
	if len(v.data) < 1 {
		v.fail("Byte: empty data")
		return 0
	}
	return v.data[0]
}

// Raw returns the value's underlying serialized bytes. For a fixed-size leaf
// (e.g. 'u'/'t') these are the integer bytes in their stored byte order.
func (v *Value) Raw() []byte {
	if v.err != nil {
		return nil
	}
	return v.data
}

// Bytes returns the raw bytes of an 'ay' (byte array) value.
func (v *Value) Bytes() []byte {
	if v.err != nil {
		return nil
	}
	if v.typ.kind != kindArray || v.typ.elem.kind != kindByte {
		v.fail("Bytes on non-ay type %q", v.typ.String())
		return nil
	}
	return v.data
}

// Str reads a string-like leaf ('s', 'o' or 'g'), stripping the trailing NUL.
func (v *Value) Str() string {
	if v.err != nil {
		return ""
	}
	if k := v.typ.kind; k != kindString && k != kindObjPath && k != kindSignature {
		v.fail("Str on non-string type %q", v.typ.String())
		return ""
	}
	d := v.data
	if len(d) > 0 && d[len(d)-1] == 0 {
		d = d[:len(d)-1]
	}
	return string(d)
}

// ErrType is returned for unsupported or malformed type signatures.
var ErrType = errors.New("gvariant: bad type")
