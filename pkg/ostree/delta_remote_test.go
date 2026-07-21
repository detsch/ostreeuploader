//go:build linux

package ostree

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRemoteDeltaSize serves a real delta repo over HTTP and asserts that
// RemoteDeltaSize (which fetches only the superblock) returns the same figures
// as LocalDeltaSize over the same repo on disk — i.e. an online estimate needs
// no local delta and matches `ostree static-delta show`.
func TestRemoteDeltaSize(t *testing.T) {
	requireOstree(t)
	repo, from, to := makeDeltaRepo(t)

	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	local, err := OpenRepo(repo).LocalDeltaSize(from, to)
	if err != nil {
		t.Fatalf("local delta size: %v", err)
	}
	remote, err := RemoteDeltaSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, from, to)
	if err != nil {
		t.Fatalf("remote delta size: %v", err)
	}
	if remote != local {
		t.Errorf("remote %+v != local %+v", remote, local)
	}
	if remote.Uncompressed == 0 {
		t.Errorf("expected non-zero uncompressed size, got %+v", remote)
	}
}

// TestRemoteDeltaSizeNoDelta confirms that when the remote publishes no delta
// for the pair, RemoteDeltaSize reports ErrNoDelta (not a generic error), so the
// caller can fall back rather than treat it as a failure.
func TestRemoteDeltaSizeNoDelta(t *testing.T) {
	requireOstree(t)
	repo, _, to := makeDeltaRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	const absentFrom = "0000000000000000000000000000000000000000000000000000000000000000"
	_, err := RemoteDeltaSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, absentFrom, to)
	if !errors.Is(err, ErrNoDelta) {
		t.Fatalf("expected ErrNoDelta, got %v", err)
	}
}
