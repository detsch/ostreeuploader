//go:build linux

package main

import (
	"fmt"
	"os"

	"github.com/foundriesio/ostreeuploader/pkg/ostree"
)

// newTTYProgress returns a ProgressFunc that renders a single-line, carriage
// -return-updated progress bar to stderr. It is intended for interactive use
// only; callers should install it solely when stderr is a terminal.
func newTTYProgress() ostree.ProgressFunc {
	const barWidth = 30
	return func(p ostree.PullProgress) {
		var bar string
		if p.ObjectsTotal > 0 {
			frac := float64(p.ObjectsDone) / float64(p.ObjectsTotal)
			if frac > 1 {
				frac = 1
			}
			filled := int(frac * float64(barWidth))
			b := make([]byte, barWidth)
			for i := range b {
				if i < filled {
					b[i] = '='
				} else {
					b[i] = ' '
				}
			}
			bar = fmt.Sprintf("[%s] %d/%d", string(b), p.ObjectsDone, p.ObjectsTotal)
		} else {
			bar = fmt.Sprintf("%d objects", p.ObjectsDone)
		}
		// \r returns to the start of the line; trailing spaces clear any leftover
		// characters from a longer previous line.
		fmt.Fprintf(os.Stderr, "\rpulling %-8s %s  %s          ",
			p.Phase, bar, ostree.FormatBytes(p.BytesDownloaded))
	}
}

// finishTTYProgress closes the progress line so subsequent output starts fresh.
func finishTTYProgress() {
	fmt.Fprintln(os.Stderr)
}
