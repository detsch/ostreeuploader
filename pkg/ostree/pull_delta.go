//go:build linux

package ostree

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// tryDeltaPull attempts the static-delta fast path: download the from->to delta
// superblock and parts the remote publishes (resumably), then apply it locally.
// It returns (true, nil) on success, (false, nil) when the remote has no such
// delta (so the caller falls back to a full pull), or (false, err) on a real
// failure.
func (f *fetcher) tryDeltaPull(ctx context.Context, from, to string) (bool, error) {
	rel, err := staticDeltaDir(from, to)
	if err != nil {
		return false, err
	}
	// Fetch the superblock; a 404 means no delta is published for this pair.
	sbRel := path.Join(rel, "superblock")
	sbData, err := f.get(ctx, sbRel)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("fetch delta superblock: %w", err)
	}

	// Determine how many parts the delta has (one per meta entry) and their
	// sizes, by parsing the superblock we just fetched.
	nParts, err := deltaPartCount(sbData)
	if err != nil {
		return false, fmt.Errorf("parse delta superblock: %w", err)
	}

	// Persist the superblock and parts into the local delta directory, so
	// applyStaticDelta can read them (and so a re-run resumes).
	base := filepath.Join(f.repo.path, filepath.FromSlash(rel))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(base, "superblock"), sbData, 0o644); err != nil {
		return false, err
	}
	f.addMeta(uint64(len(sbData)))

	// Downloading the delta parts: report progress as "part N of nParts".
	f.mu.Lock()
	f.curPhase = PhaseDelta
	f.contentTotal = nParts
	f.mu.Unlock()

	for i := 0; i < nParts; i++ {
		partRel := path.Join(rel, fmt.Sprintf("%d", i))
		dst := filepath.Join(base, fmt.Sprintf("%d", i))
		// Resumable download into a .part sidecar, then move into place.
		partFile := dst + ".part"
		_, err := f.downloadResumable(ctx, partRel, partFile)
		if err != nil {
			return false, fmt.Errorf("fetch delta part %d: %w", i, err)
		}
		if err := os.Rename(partFile, dst); err != nil {
			return false, err
		}
		f.addContent()
	}

	// Apply the delta locally (verifies part and produced-object checksums).
	if err := f.repo.applyStaticDelta(from, to); err != nil {
		return false, fmt.Errorf("apply delta: %w", err)
	}
	return true, nil
}

// deltaPartCount returns the number of delta parts (meta entries) in a
// superblock blob.
func deltaPartCount(sbData []byte) (int, error) {
	sb, err := newSuperblock(sbData)
	if err != nil {
		return 0, err
	}
	n := sb.Child(sbMetaEntries).Len()
	if err := sb.Err(); err != nil {
		return 0, err
	}
	return n, nil
}
