//go:build linux

package ostree

import (
	"fmt"
	"syscall"
)

// UsageInfo mirrors the shape used by composeapp (pkg/compose/statfs.go) so the
// figures line up with the rest of the Foundries tooling.
type UsageInfo struct {
	Path       string  `json:"path"`
	SizeB      uint64  `json:"size_b"`
	Free       uint64  `json:"free"`
	FreeP      float32 `json:"free_p"`
	Reserved   uint64  `json:"reserved"`
	ReservedP  float32 `json:"reserved_p"`
	Available  uint64  `json:"available"`
	AvailableP float32 `json:"available_p"`
	Required   uint64  `json:"required"`
	RequiredP  float32 `json:"required_p"`
}

var binaryAbbrs = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}

// FormatBytes renders a byte count using binary (1024) units, e.g. "3.001 MiB".
func FormatBytes(size uint64) string {
	v := float64(size)
	i := 0
	for v >= 1024 && i < len(binaryAbbrs)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", size, binaryAbbrs[i])
	}
	return fmt.Sprintf("%.4g %s", v, binaryAbbrs[i])
}

// GetUsageInfo reports filesystem usage for path against a watermark that bounds
// how much storage may be consumed. The watermark is a percentage of total size
// when watermarkInBytes is false, or an absolute amount of free space to keep
// reserved (in bytes) when watermarkInBytes is true. This matches composeapp's
// GetUsageInfo semantics.
func GetUsageInfo(path string, required uint64, watermark uint64, watermarkInBytes bool) (*UsageInfo, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, err
	}
	bsize := uint64(st.Bsize)
	ui := UsageInfo{
		Path:     path,
		SizeB:    bsize * st.Blocks,
		Free:     st.Bfree * bsize,
		Required: required,
	}
	if ui.SizeB > 0 {
		ui.FreeP = (float32(ui.Free) / float32(ui.SizeB)) * 100.0
		ui.RequiredP = (float32(ui.Required) / float32(ui.SizeB)) * 100.0
	}
	if watermarkInBytes {
		ui.Reserved = watermark
		if ui.SizeB > 0 {
			ui.ReservedP = (float32(ui.Reserved) / float32(ui.SizeB)) * 100.0
		}
	} else {
		ui.Reserved = uint64((float64(100-watermark) / 100.0) * float64(ui.SizeB))
		ui.ReservedP = float32(100 - watermark)
	}
	if ui.Free > ui.Reserved {
		ui.Available = ui.Free - ui.Reserved
		ui.AvailableP = ui.FreeP - ui.ReservedP
	}
	return &ui, nil
}
