//go:build linux

package ostree

import "fmt"

// bspatchApply applies a bsdiff patch to old, writing exactly len(out) bytes.
// It implements the mendsley/bsdiff control format that libostree bundles: the
// patch is a stream of records [ctrl[0..2] each a 8-byte signed offtin int]
// followed by ctrl[0] diff bytes and ctrl[1] extra bytes. ostree feeds the raw
// patch bytes directly (no secondary bzip2 compression at this layer), matching
// its bspatch_read callback that copies sequentially from the data-source.
func bspatchApply(old, out, patch []byte) error {
	var oldpos, newpos int64
	oldsize := int64(len(old))
	newsize := int64(len(out))
	p := 0

	read := func(n int64) ([]byte, error) {
		if n < 0 || p+int(n) > len(patch) {
			return nil, fmt.Errorf("patch truncated")
		}
		b := patch[p : p+int(n)]
		p += int(n)
		return b, nil
	}

	for newpos < newsize {
		var ctrl [3]int64
		for i := 0; i < 3; i++ {
			b, err := read(8)
			if err != nil {
				return err
			}
			ctrl[i] = offtin(b)
		}
		if ctrl[0] < 0 || ctrl[1] < 0 || newpos+ctrl[0] > newsize {
			return fmt.Errorf("invalid control triple")
		}

		// Diff string: copy ctrl[0] bytes from patch, add overlapping old bytes.
		diff, err := read(ctrl[0])
		if err != nil {
			return err
		}
		copy(out[newpos:newpos+ctrl[0]], diff)
		for i := int64(0); i < ctrl[0]; i++ {
			if oldpos+i >= 0 && oldpos+i < oldsize {
				out[newpos+i] += old[oldpos+i]
			}
		}
		newpos += ctrl[0]
		oldpos += ctrl[0]

		if newpos+ctrl[1] > newsize {
			return fmt.Errorf("extra string exceeds output")
		}
		// Extra string: copy ctrl[1] bytes verbatim from patch.
		extra, err := read(ctrl[1])
		if err != nil {
			return err
		}
		copy(out[newpos:newpos+ctrl[1]], extra)
		newpos += ctrl[1]
		oldpos += ctrl[2]
	}
	return nil
}

// offtin decodes bsdiff's sign-magnitude 8-byte little-endian integer.
func offtin(b []byte) int64 {
	y := int64(b[7] & 0x7f)
	for i := 6; i >= 0; i-- {
		y = y*256 + int64(b[i])
	}
	if b[7]&0x80 != 0 {
		y = -y
	}
	return y
}
