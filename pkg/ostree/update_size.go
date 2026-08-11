//go:build linux

package ostree

import (
	"context"
	"errors"
)

// SizeMethod identifies which source produced an UpdateSize, in increasing order
// of cost.
type SizeMethod string

const (
	// SizeFromDelta: read from the remote static-delta superblock (one fetch).
	SizeFromDelta SizeMethod = "delta"
	// SizeFromSizesMeta: read from the commit's ostree.sizes metadata (one fetch).
	SizeFromSizesMeta SizeMethod = "sizes-meta"
)

// ErrSizeUnavailable reports that the update size could not be determined from a
// cheap source: neither a published static delta nor the commit's ostree.sizes
// metadata is available. It lets a caller distinguish "no efficient estimate
// exists" from a real error, and decide for itself whether to proceed without
// one.
var ErrSizeUnavailable = errors.New("update size unavailable: no static delta and no ostree.sizes metadata")

// UpdateSize is the estimated storage an update to `to` needs.
type UpdateSize struct {
	// Compressed is the download size in bytes (what crosses the network).
	Compressed uint64 `json:"compressed"`
	// Uncompressed is the on-disk size in bytes.
	Uncompressed uint64 `json:"uncompressed"`
	// Method reports which source produced the figures.
	Method SizeMethod `json:"method"`
}

// RemoteUpdateSize estimates the storage a from->to update needs, trying the
// cheapest accurate source first:
//
//  1. If from is set and the remote publishes a from->to static delta, read the
//     sizes from its superblock (one fetch). Both compressed and uncompressed.
//  2. Else, if the `to` commit carries ostree.sizes metadata, read the sizes
//     from the commit object (one fetch). Both compressed and uncompressed.
//
// When neither source is available, ErrSizeUnavailable is returned.
//
// localRepo, if non-nil, is used to subtract objects already present on disk so
// the returned sizes reflect only what would actually be downloaded/written, not
// a full fresh-pull total.
//
// This is the single entry point a caller should use to size an update; it makes
// the delta/metadata decision internally.
func RemoteUpdateSize(ctx context.Context, rc RemoteConfig, from, to string, localRepo *Repo) (UpdateSize, error) {
	// 1. Static-delta superblock (cheapest) when a base commit is known.
	if from != "" {
		ds, err := RemoteDeltaSize(ctx, rc, from, to)
		if err == nil {
			return UpdateSize{Compressed: ds.Compressed, Uncompressed: ds.Uncompressed, Method: SizeFromDelta}, nil
		}
		if !errors.Is(err, ErrNoDelta) {
			return UpdateSize{}, err
		}
		// No delta published for this pair: fall through to the ostree.sizes path.
	}

	// 2. Full-pull sizing from the commit's ostree.sizes metadata.
	ps, err := RemotePullSize(ctx, rc, to, localRepo)
	if err != nil {
		return UpdateSize{}, err
	}
	return UpdateSize{Compressed: ps.Compressed, Uncompressed: ps.Uncompressed, Method: SizeFromSizesMeta}, nil
}
