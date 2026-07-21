// Package varint decodes the unsigned LEB128 varints ostree uses to encode
// static-delta operation operands (libostree _ostree_read_varuint64). It is the
// same wire form as encoding/binary.Uvarint, but exposed here with an explicit
// 10-byte cap and an io-free byte-slice API matching the delta processor.
package varint

import "errors"

// ErrOverflow is returned when a varint does not terminate within 10 bytes.
var ErrOverflow = errors.New("varint: value overflows 64 bits")

// ErrTruncated is returned when the buffer ends mid-varint.
var ErrTruncated = errors.New("varint: truncated")

// Uvarint decodes an unsigned LEB128 varint from buf, returning the value and
// the number of bytes consumed.
func Uvarint(buf []byte) (uint64, int, error) {
	var result uint64
	for i := 0; ; i++ {
		if i == 10 {
			return 0, 0, ErrOverflow
		}
		if i >= len(buf) {
			return 0, 0, ErrTruncated
		}
		b := buf[i]
		result |= uint64(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return result, i + 1, nil
		}
	}
}
