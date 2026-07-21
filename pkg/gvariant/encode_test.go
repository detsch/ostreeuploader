package gvariant

import (
	"encoding/hex"
	"testing"
)

// Ground-truth bytes captured from libostree 2024.5 (GLib.Variant) on the box,
// for uid=1001 (0x3e9), gid=1002 (0x3ea), mode=0100664 (0x81b4) / symlink mode
// 0120777 (0xa1ff). These pin the exact serialization (including big-endian
// ints and framing offsets).
func TestEncodeFileHeaderRegfile(t *testing.T) {
	got := EncodeFileHeader(1001, 1002, 0o100664, "", nil)
	want := "000003e9000003ea000081b4000000000011"
	if hex.EncodeToString(got) != want {
		t.Errorf("regfile header:\n got %s\nwant %s", hex.EncodeToString(got), want)
	}
}

func TestEncodeFileHeaderSymlink(t *testing.T) {
	got := EncodeFileHeader(1001, 1002, 0o120777, "regfile.txt", nil)
	want := "000003e9000003ea0000a1ff0000000072656766696c652e747874001c"
	if hex.EncodeToString(got) != want {
		t.Errorf("symlink header:\n got %s\nwant %s", hex.EncodeToString(got), want)
	}
}

func TestEncodeFileMetaRegfile(t *testing.T) {
	got := EncodeFileMeta(1001, 1002, 0o100664, nil)
	want := "000003e9000003ea000081b4"
	if hex.EncodeToString(got) != want {
		t.Errorf("meta:\n got %s\nwant %s", hex.EncodeToString(got), want)
	}
}

func TestEncodeFileMetaWithXattr(t *testing.T) {
	xa := []Xattr{{Name: []byte("user.foo"), Value: []byte("bar")}}
	got := EncodeFileMeta(1001, 1002, 0o100664, xa)
	want := "000003e9000003ea000081b4757365722e666f6f00626172090d"
	if hex.EncodeToString(got) != want {
		t.Errorf("meta w/xattr:\n got %s\nwant %s", hex.EncodeToString(got), want)
	}
}

func TestEncodeFileHeaderWithXattr(t *testing.T) {
	xa := []Xattr{{Name: []byte("user.foo"), Value: []byte("bar")}}
	got := EncodeFileHeader(1001, 1002, 0o100664, "", xa)
	want := "000003e9000003ea000081b40000000000757365722e666f6f00626172090d11"
	if hex.EncodeToString(got) != want {
		t.Errorf("header w/xattr:\n got %s\nwant %s", hex.EncodeToString(got), want)
	}
}

// Round-trip: the encoded meta must decode back to the same fields via the
// decoder in this package.
func TestEncodeMetaRoundTrip(t *testing.T) {
	xa := []Xattr{{Name: []byte("user.foo"), Value: []byte("bar")}}
	raw := EncodeFileMeta(1001, 1002, 0o100664, xa)
	v, err := New(raw, "(uuua(ayay))")
	if err != nil {
		t.Fatal(err)
	}
	// Ints are stored big-endian here, so read the bytes back big-endian.
	if got := beFromBytes(v.Child(0).Raw()); got != 1001 {
		t.Errorf("uid: got %d want 1001", got)
	}
	if got := beFromBytes(v.Child(2).Raw()); got != 0o100664 {
		t.Errorf("mode: got %o want 0100664", got)
	}
	arr := v.Child(3)
	if arr.Len() != 1 {
		t.Fatalf("xattrs len: got %d want 1", arr.Len())
	}
	e := arr.At(0)
	if string(e.Child(0).Bytes()) != "user.foo\x00" {
		t.Errorf("xattr name: got %q", string(e.Child(0).Bytes()))
	}
	if string(e.Child(1).Bytes()) != "bar" {
		t.Errorf("xattr value: got %q", string(e.Child(1).Bytes()))
	}
	if err := v.Err(); err != nil {
		t.Fatal(err)
	}
}

func beFromBytes(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
