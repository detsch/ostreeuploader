//go:build linux

package main

import (
	"fmt"
	"os"
	"sync"
	"time"

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

// newLogProgress returns a ProgressFunc that writes periodic, newline-terminated
// progress lines to stderr, for a non-interactive parent (e.g. aktualizr-lite)
// that captures the child's output line by line. Unlike the TTY bar it emits no
// \r, so it is safe to log. Output is throttled to at most one line per second,
// but a phase change or the final done==total snapshot always prints, so the
// last line for each phase reports completion. The callback may be invoked
// concurrently, so emission is serialized under a mutex.
func newLogProgress() ostree.ProgressFunc {
	const minInterval = time.Second
	var (
		mu        sync.Mutex
		lastAt    time.Time
		lastPhase ostree.PullPhase
		started   bool
	)
	return func(p ostree.PullProgress) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		done := p.ObjectsTotal > 0 && p.ObjectsDone >= p.ObjectsTotal
		phaseChanged := !started || p.Phase != lastPhase
		if !phaseChanged && !done && now.Sub(lastAt) < minInterval {
			return
		}
		started = true
		lastPhase = p.Phase
		lastAt = now

		if p.ObjectsTotal > 0 {
			pct := p.ObjectsDone * 100 / p.ObjectsTotal
			fmt.Fprintf(os.Stderr, "fiopull: %s %d/%d (%d%%) %s\n",
				p.Phase, p.ObjectsDone, p.ObjectsTotal, pct, ostree.FormatBytes(p.BytesDownloaded))
		} else {
			fmt.Fprintf(os.Stderr, "fiopull: %s %d objects %s\n",
				p.Phase, p.ObjectsDone, ostree.FormatBytes(p.BytesDownloaded))
		}
	}
}
