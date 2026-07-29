//go:build linux

package ostree

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestPullProgressReported verifies the Progress callback fires during a full
// pull, ends with all content objects done, and reports monotonic byte totals.
func TestPullProgressReported(t *testing.T) {
	requireOstree(t)
	srcRepo, ref, _ := makeContentRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()

	var (
		mu       sync.Mutex
		events   []PullProgress
		maxBytes uint64
		mono     = true
	)
	dest := filepath.Join(t.TempDir(), "dest")
	res, err := OpenRepo(dest).Pull(context.Background(), PullOptions{
		Remote: RemoteConfig{BaseURL: srv.URL},
		Ref:    ref,
		Progress: func(p PullProgress) {
			mu.Lock()
			defer mu.Unlock()
			if p.BytesDownloaded < maxBytes {
				mono = false
			}
			maxBytes = p.BytesDownloaded
			events = append(events, p)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("progress callback was never invoked")
	}
	if !mono {
		t.Error("BytesDownloaded was not monotonic non-decreasing")
	}
	// Find the final content-phase event; it should report all objects done.
	var lastContent *PullProgress
	for i := range events {
		if events[i].Phase == PhaseContent {
			lastContent = &events[i]
		}
	}
	if lastContent == nil {
		t.Fatal("no content-phase progress events")
	}
	if lastContent.ObjectsTotal == 0 || lastContent.ObjectsDone != lastContent.ObjectsTotal {
		t.Errorf("final content progress = %d/%d, want done==total>0",
			lastContent.ObjectsDone, lastContent.ObjectsTotal)
	}
	if maxBytes == 0 || res.BytesDownloaded == 0 {
		t.Errorf("expected non-zero bytes: progress max=%d result=%d", maxBytes, res.BytesDownloaded)
	}
}

// TestPullProgressNilSafe confirms a pull with no Progress callback works.
func TestPullProgressNilSafe(t *testing.T) {
	requireOstree(t)
	srcRepo, ref, commit := makeContentRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "dest")
	res, err := OpenRepo(dest).Pull(context.Background(), PullOptions{
		Remote: RemoteConfig{BaseURL: srv.URL}, Ref: ref, // Progress nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != commit {
		t.Fatalf("commit %s want %s", res.Commit, commit)
	}
}

// TestPullProgressInObject verifies byte progress advances *within* a single
// large object: pulling a repo whose only sizable object is ~2MB with
// Concurrency 1 must yield multiple content-phase snapshots with increasing
// byte counts before the object completes.
func TestPullProgressInObject(t *testing.T) {
	requireOstree(t)
	dir := t.TempDir()
	srcRepo := filepath.Join(dir, "repo")
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, "ostree", "--repo="+srcRepo, "init", "--mode=archive")
	writeRandom(t, filepath.Join(tree, "d", "big"), 3_000_000) // incompressible
	commit := strings.TrimSpace(run(t, "ostree", "--repo="+srcRepo, "commit", "--branch=main",
		"--owner-uid=0", "--owner-gid=0", "--tree=dir="+tree))

	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()

	var (
		mu     sync.Mutex
		bytes  []uint64
	)
	dest := filepath.Join(t.TempDir(), "dest")
	_, err := OpenRepo(dest).Pull(context.Background(), PullOptions{
		Remote:      RemoteConfig{BaseURL: srv.URL},
		Commit:      commit,
		Concurrency: 1,
		Progress: func(p PullProgress) {
			if p.Phase == PhaseContent {
				mu.Lock()
				bytes = append(bytes, p.BytesDownloaded)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// Expect several increasing byte snapshots (throttled to ~100ms, but a 3MB
	// object over the loopback still yields at least a couple, plus completion).
	distinct := map[uint64]bool{}
	for _, b := range bytes {
		distinct[b] = true
	}
	if len(distinct) < 2 {
		t.Errorf("expected multiple distinct byte snapshots within the object, got %v", bytes)
	}
}
