//go:build linux

package ostree

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestPullWithDelta pulls the `from` commit fully, then pulls `to` with From set
// so the static-delta fast path runs over HTTP. It asserts the delta path was
// used, far fewer/els different objects were transferred than a full pull, and
// the result fscks clean and matches the source.
func TestPullWithDelta(t *testing.T) {
	requireOstree(t)
	srcRepo, from, to := makeDeltaContentRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "dest")
	r := OpenRepo(dest).WithMode(modeBareUserOnly)
	ctx := context.Background()

	// Base commit (full pull).
	if _, err := r.Pull(ctx, PullOptions{Remote: RemoteConfig{BaseURL: srv.URL}, Commit: from}); err != nil {
		t.Fatalf("pull from: %v", err)
	}

	// Update via delta.
	res, err := r.Pull(ctx, PullOptions{
		Remote: RemoteConfig{BaseURL: srv.URL},
		Ref:    "main",
		Commit: to,
		From:   from,
	})
	if err != nil {
		t.Fatalf("delta pull: %v", err)
	}
	if !res.UsedDelta {
		t.Fatalf("expected delta path to be used, got full pull: %+v", res)
	}

	out := run(t, "ostree", "--repo="+dest, "fsck")
	if !strings.Contains(out, "no errors found") {
		t.Errorf("fsck failed:\n%s", out)
	}
	got, err := r.ResolveRef("main")
	if err != nil || got != to {
		t.Fatalf("ResolveRef(main)=%q,%v want %s", got, err, to)
	}
	srcLs := run(t, "ostree", "--repo="+srcRepo, "ls", "-R", to)
	dstLs := run(t, "ostree", "--repo="+dest, "ls", "-R", to)
	if srcLs != dstLs {
		t.Errorf("listings differ:\n--- src ---\n%s\n--- dst ---\n%s", srcLs, dstLs)
	}
}

// TestPullDeltaFallback confirms that when no delta is published for the pair,
// Pull falls back to a full object pull and still succeeds.
func TestPullDeltaFallback(t *testing.T) {
	requireOstree(t)
	// A repo with two commits but NO generated static delta.
	srcRepo, _, to := makeContentRepoOpts(t, true)
	// Fabricate a plausible but absent `from` so the delta superblock 404s.
	const absentFrom = "0000000000000000000000000000000000000000000000000000000000000000"
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "dest")
	r := OpenRepo(dest).WithMode(modeBareUserOnly)
	res, err := r.Pull(context.Background(), PullOptions{
		Remote: RemoteConfig{BaseURL: srv.URL},
		Commit: to,
		From:   absentFrom, // no delta exists -> fall back to full pull
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if res.UsedDelta {
		t.Fatalf("expected full-pull fallback, but delta was used")
	}
	out := run(t, "ostree", "--repo="+dest, "fsck")
	if !strings.Contains(out, "no errors found") {
		t.Errorf("fsck failed:\n%s", out)
	}
}
