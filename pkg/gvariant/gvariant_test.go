package gvariant

import (
	"bytes"
	"testing"
)

// Test tuple member-bounds for the meta-entry type "(uayttay)", whose framing is
// the heart of superblock parsing. Construct one entry by hand and read it back.
//
// Layout of (u ay t t ay):
//
//	u  version  @0 (4 bytes, fixed)
//	ay csum     @4 (variable) -> needs a framing offset
//	t  size     @aligned(8)   (fixed)
//	t  usize                  (fixed)
//	ay objs     (variable, LAST) -> end = size - offsets
func TestTupleMetaEntry(t *testing.T) {
	// Build: version=1, csum=[2 bytes 0xAA,0xBB], size=0x1122, usize=0x3344,
	// objs=[0x09].
	var buf bytes.Buffer
	// version u @0
	buf.Write([]byte{1, 0, 0, 0})
	// csum ay (2 bytes) @4
	buf.Write([]byte{0xAA, 0xBB})
	// pad to align 8 for the first t: current len=6 -> pad 2
	buf.Write([]byte{0, 0})
	// size t @8
	buf.Write(le64(0x1122))
	// usize t @16
	buf.Write(le64(0x3344))
	// objs ay (LAST) @24
	buf.Write([]byte{0x09})
	// framing offsets (one, for the first variable member 'csum'): its end = 6.
	// The last member is LAST so it has no stored offset. offset_size for this
	// small container is 1.
	buf.WriteByte(6)

	v, err := New(buf.Bytes(), "(uayttay)")
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Child(0).Uint32(); got != 1 {
		t.Errorf("version: got %d want 1", got)
	}
	if got := v.Child(1).Bytes(); !bytes.Equal(got, []byte{0xAA, 0xBB}) {
		t.Errorf("csum: got %x want aabb", got)
	}
	if got := v.Child(2).Uint64(); got != 0x1122 {
		t.Errorf("size: got %#x want 0x1122", got)
	}
	if got := v.Child(3).Uint64(); got != 0x3344 {
		t.Errorf("usize: got %#x want 0x3344", got)
	}
	if got := v.Child(4).Bytes(); !bytes.Equal(got, []byte{0x09}) {
		t.Errorf("objs: got %x want 09", got)
	}
	if err := v.Err(); err != nil {
		t.Fatal(err)
	}
}

// Test a variable-sized array of strings "as" with two elements.
func TestArrayOfStrings(t *testing.T) {
	// "ab\0" (3) then "cde\0" (4). Offsets: [3, 7], offset_size=1.
	data := []byte{'a', 'b', 0, 'c', 'd', 'e', 0, 3, 7}
	v, err := New(data, "as")
	if err != nil {
		t.Fatal(err)
	}
	if v.Len() != 2 {
		t.Fatalf("len: got %d want 2", v.Len())
	}
	if got := v.At(0).Str(); got != "ab" {
		t.Errorf("elem0: got %q want ab", got)
	}
	if got := v.At(1).Str(); got != "cde" {
		t.Errorf("elem1: got %q want cde", got)
	}
	if err := v.Err(); err != nil {
		t.Fatal(err)
	}
}

// Test an array of fixed-size elements "at" (no framing offsets).
func TestArrayFixed(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(le64(10))
	buf.Write(le64(20))
	buf.Write(le64(30))
	v, err := New(buf.Bytes(), "at")
	if err != nil {
		t.Fatal(err)
	}
	if v.Len() != 3 {
		t.Fatalf("len: got %d want 3", v.Len())
	}
	want := []uint64{10, 20, 30}
	for i, w := range want {
		if got := v.At(i).Uint64(); got != w {
			t.Errorf("elem%d: got %d want %d", i, got, w)
		}
	}
	if err := v.Err(); err != nil {
		t.Fatal(err)
	}
}

func le64(x uint64) []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(x >> (8 * i))
	}
	return b
}
