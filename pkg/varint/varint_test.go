package varint

import "testing"

func TestUvarint(t *testing.T) {
	cases := []struct {
		bytes []byte
		val   uint64
		n     int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x01}, 1, 1},
		{[]byte{0x7f}, 127, 1},
		{[]byte{0x80, 0x01}, 128, 2},
		{[]byte{0xac, 0x02}, 300, 2},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0x0f}, 0xFFFFFFFF, 5},
	}
	for _, c := range cases {
		v, n, err := Uvarint(c.bytes)
		if err != nil {
			t.Errorf("%x: unexpected error %v", c.bytes, err)
			continue
		}
		if v != c.val || n != c.n {
			t.Errorf("%x: got (%d,%d), want (%d,%d)", c.bytes, v, n, c.val, c.n)
		}
	}
}

func TestUvarintErrors(t *testing.T) {
	if _, _, err := Uvarint(nil); err != ErrTruncated {
		t.Errorf("empty: got %v, want ErrTruncated", err)
	}
	if _, _, err := Uvarint([]byte{0x80, 0x80}); err != ErrTruncated {
		t.Errorf("unterminated: got %v, want ErrTruncated", err)
	}
	// 11 continuation bytes -> overflow.
	big := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}
	if _, _, err := Uvarint(big); err != ErrOverflow {
		t.Errorf("overflow: got %v, want ErrOverflow", err)
	}
}
