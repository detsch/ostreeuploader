//go:build linux

package ostree

import "fmt"

// Estimate is the full result of a pre-flight update size check.
type Estimate struct {
	From  string     `json:"from"`
	To    string     `json:"to"`
	Delta DeltaSize  `json:"delta"`
	Usage *UsageInfo `json:"usage"`
	// Sufficient is true when Usage.Available >= Usage.Required.
	Sufficient bool `json:"sufficient"`
}

// EstimateFromDeltaSize builds an Estimate from an already-obtained DeltaSize by
// querying free space at checkPath against the watermark. When there is not
// enough room, it returns the Estimate alongside an *InsufficientStorageError
// (which wraps ErrInsufficientStorage).
func EstimateFromDeltaSize(from, to, checkPath string, ds DeltaSize, watermark uint64, watermarkInBytes bool) (*Estimate, error) {
	ui, err := GetUsageInfo(checkPath, ds.Uncompressed, watermark, watermarkInBytes)
	if err != nil {
		return nil, fmt.Errorf("usage info: %w", err)
	}
	est := &Estimate{
		From:       from,
		To:         to,
		Delta:      ds,
		Usage:      ui,
		Sufficient: ui.Available >= ui.Required,
	}
	if !est.Sufficient {
		return est, &InsufficientStorageError{Usage: ui}
	}
	return est, nil
}

// EstimateLocalUpdate computes the storage a from->to ostree update will require,
// reading the delta size from the local repository's superblock (pure Go, no
// `ostree` process), and compares it against the available space at checkPath
// (after the reserved watermark).
//
// watermark is a percentage of total size (e.g. 95) when watermarkInBytes is
// false, or an absolute reserved byte count when watermarkInBytes is true.
//
// The returned *Estimate always reflects the computed figures. When there is not
// enough room, the error is an *InsufficientStorageError that also wraps
// ErrInsufficientStorage.
func (r *Repo) EstimateLocalUpdate(from, to, checkPath string, watermark uint64, watermarkInBytes bool) (*Estimate, error) {
	ds, err := r.LocalDeltaSize(from, to)
	if err != nil {
		return nil, fmt.Errorf("delta size: %w", err)
	}
	if checkPath == "" {
		checkPath = r.path
	}
	return EstimateFromDeltaSize(from, to, checkPath, ds, watermark, watermarkInBytes)
}
