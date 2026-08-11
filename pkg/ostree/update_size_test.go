//go:build linux

package ostree

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRemoteUpdateSizeDelta: when a from->to static delta is published, the
// unified entry point uses it (Method=delta) and matches RemoteDeltaSize.
func TestRemoteUpdateSizeDelta(t *testing.T) {
	requireOstree(t)
	repo, from, to := makeDeltaRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	us, err := RemoteUpdateSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, from, to, nil)
	if err != nil {
		t.Fatalf("RemoteUpdateSize: %v", err)
	}
	if us.Method != SizeFromDelta {
		t.Fatalf("method = %q, want delta", us.Method)
	}
	want, err := RemoteDeltaSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if us.Compressed != want.Compressed || us.Uncompressed != want.Uncompressed {
		t.Errorf("update-size %+v != delta-size %+v", us, want)
	}
}

// TestRemoteUpdateSizeNoDeltaFallsBackToSizesMeta: with no delta published but a
// commit carrying ostree.sizes, it falls back to the metadata (Method=sizes-meta),
// still yielding an uncompressed figure.
func TestRemoteUpdateSizeNoDeltaFallsBackToSizesMeta(t *testing.T) {
	requireOstree(t)
	repo, _, to := makeSizesRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	const absentFrom = "0000000000000000000000000000000000000000000000000000000000000000"
	us, err := RemoteUpdateSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, absentFrom, to, nil)
	if err != nil {
		t.Fatalf("RemoteUpdateSize: %v", err)
	}
	if us.Method != SizeFromSizesMeta {
		t.Fatalf("method = %q, want sizes-meta", us.Method)
	}
	if us.Compressed == 0 || us.Uncompressed == 0 {
		t.Errorf("expected non-zero compressed and uncompressed, got %+v", us)
	}
}

// TestRemoteUpdateSizeUnavailable: no delta and no ostree.sizes -> the size
// cannot be determined and ErrSizeUnavailable is returned.
func TestRemoteUpdateSizeUnavailable(t *testing.T) {
	requireOstree(t)
	repo, _, to := makeContentRepoOpts(t, true) // no --generate-sizes
	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	_, err := RemoteUpdateSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, "", to, nil)
	if !errors.Is(err, ErrSizeUnavailable) {
		t.Fatalf("expected ErrSizeUnavailable, got %v", err)
	}
}

// TestRemoteUpdateSizeAlreadyUpToDate: when from==to and the local repo has all
// objects, the estimate must be 0 — the device is already on the target commit.
func TestRemoteUpdateSizeAlreadyUpToDate(t *testing.T) {
	requireOstree(t)
	srcRepo, _, commit := makeSizesRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()
	rc := RemoteConfig{BaseURL: srv.URL}

	dest := t.TempDir()
	localRepo := OpenRepo(dest)
	if _, err := localRepo.Pull(context.Background(), PullOptions{Remote: rc, Commit: commit}); err != nil {
		t.Fatalf("pull: %v", err)
	}

	// from == to: no delta will exist, falls to sizes-meta path, all objects local.
	us, err := RemoteUpdateSize(context.Background(), rc, commit, commit, localRepo)
	if err != nil {
		t.Fatalf("RemoteUpdateSize: %v", err)
	}
	if us.Compressed != 0 {
		t.Errorf("already up-to-date: compressed = %d, want 0", us.Compressed)
	}
}
