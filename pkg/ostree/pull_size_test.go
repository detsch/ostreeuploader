//go:build linux

package ostree

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sumRepoObjectBytes sums the on-disk sizes of every loose object in an archive
// repo, split into content (.filez) and metadata (.commit/.dirtree/.dirmeta).
// For a freshly-created repo holding a single commit, this is the exact set of
// objects a full pull of that commit would download.
func sumRepoObjectBytes(t *testing.T, repo string) (content, meta uint64, nContent, nMeta int) {
	t.Helper()
	objRoot := filepath.Join(repo, "objects")
	err := filepath.Walk(objRoot, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(p, ".filez"):
			content += uint64(fi.Size())
			nContent++
		case strings.HasSuffix(p, ".commit"), strings.HasSuffix(p, ".dirtree"), strings.HasSuffix(p, ".dirmeta"):
			meta += uint64(fi.Size())
			nMeta++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}
	return
}

// TestRemotePullSizeUnavailable confirms that a commit WITHOUT ostree.sizes
// metadata yields ErrSizeUnavailable: there is no cheap source to size it and
// the per-object probe has been removed.
func TestRemotePullSizeUnavailable(t *testing.T) {
	requireOstree(t)
	repo, _, commit := makeContentRepoOpts(t, true) // no --generate-sizes
	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	_, err := RemotePullSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, commit, nil)
	if !errors.Is(err, ErrSizeUnavailable) {
		t.Fatalf("RemotePullSize on a no-ostree.sizes commit: err = %v, want ErrSizeUnavailable", err)
	}
}

// makeSizesRepo builds an archive repo whose single commit was generated with
// `--generate-sizes`, so the commit carries the ostree.sizes metadata table.
func makeSizesRepo(t *testing.T) (repo, ref, commit string) {
	t.Helper()
	dir := t.TempDir()
	repo = filepath.Join(dir, "repo")
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tree, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, "ostree", "--repo="+repo, "init", "--mode=archive")
	writeRandom(t, filepath.Join(tree, "usr", "bin", "prog"), 300_000)
	os.WriteFile(filepath.Join(tree, "etc", "version"), []byte("v1\n"), 0o644)
	commit = strings.TrimSpace(run(t, "ostree", "--repo="+repo, "commit", "--branch=main",
		"--generate-sizes", "--owner-uid=0", "--owner-gid=0", "--tree=dir="+tree))
	return repo, "main", commit
}

// TestRemotePullSizeFromMeta confirms that when the commit carries ostree.sizes
// metadata, RemotePullSize uses it (a single fetch), matches the on-disk object
// totals, and also reports the uncompressed size.
func TestRemotePullSizeFromMeta(t *testing.T) {
	requireOstree(t)
	repo, _, commit := makeSizesRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	wantContent, wantMeta, nContent, nMeta := sumRepoObjectBytes(t, repo)

	ps, err := RemotePullSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, commit, nil)
	if err != nil {
		t.Fatalf("RemotePullSize: %v", err)
	}
	if !ps.FromSizesMeta {
		t.Fatalf("expected FromSizesMeta=true when ostree.sizes present")
	}
	if ps.Compressed != wantContent+wantMeta {
		t.Errorf("compressed = %d, want %d (content %d + meta %d)", ps.Compressed, wantContent+wantMeta, wantContent,
			wantMeta)
	}
	if ps.ContentObjects != nContent {
		t.Errorf("content objects = %d, want %d", ps.ContentObjects, nContent)
	}
	if ps.MetaObjects != nMeta {
		t.Errorf("meta objects = %d, want %d", ps.MetaObjects, nMeta)
	}
	if ps.Uncompressed == 0 {
		t.Errorf("expected a non-zero uncompressed size from ostree.sizes, got 0 (compressed %d)", ps.Compressed)
	}
}

// TestRemotePullSizeMetaMatchesObjects cross-checks the ostree.sizes fast path
// against the actual on-disk object totals for the same repo.
func TestRemotePullSizeMetaMatchesObjects(t *testing.T) {
	requireOstree(t)
	repo, _, commit := makeSizesRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(repo)))
	defer srv.Close()

	fromMeta, err := RemotePullSize(context.Background(), RemoteConfig{BaseURL: srv.URL}, commit, nil)
	if err != nil {
		t.Fatalf("RemotePullSize: %v", err)
	}
	if !fromMeta.FromSizesMeta {
		t.Fatalf("expected FromSizesMeta=true")
	}
	wantContent, wantMeta, _, _ := sumRepoObjectBytes(t, repo)
	if fromMeta.Compressed != wantContent+wantMeta {
		t.Errorf("sizes-meta compressed = %d, want %d", fromMeta.Compressed, wantContent+wantMeta)
	}
}

// TestRemotePullSizeViaRef confirms a ref is resolved to its commit before
// sizing, and file:// transport works too.
func TestRemotePullSizeViaRef(t *testing.T) {
	requireOstree(t)
	repo, ref, commit := makeSizesRepo(t)

	got, err := ResolveRemoteRef(context.Background(), RemoteConfig{BaseURL: "file://" + repo}, ref)
	if err != nil {
		t.Fatalf("ResolveRemoteRef: %v", err)
	}
	if got != commit {
		t.Fatalf("ResolveRemoteRef(%s) = %s, want %s", ref, got, commit)
	}

	ps, err := RemotePullSize(context.Background(), RemoteConfig{BaseURL: "file://" + repo}, got, nil)
	if err != nil {
		t.Fatalf("RemotePullSize (file): %v", err)
	}
	wantContent, wantMeta, _, _ := sumRepoObjectBytes(t, repo)
	if ps.Compressed != wantContent+wantMeta {
		t.Errorf("total size = %d, want %d", ps.Compressed, wantContent+wantMeta)
	}
}

// TestRemotePullSizeLocalRepoSizesMeta verifies that objects already present in
// localRepo are excluded from the ostree.sizes estimate. After a full pull the
// estimate must drop to zero.
func TestRemotePullSizeLocalRepoSizesMeta(t *testing.T) {
	requireOstree(t)
	srcRepo, _, commit := makeSizesRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()
	rc := RemoteConfig{BaseURL: srv.URL}

	// Pull into a local bare-user repo.
	dest := t.TempDir()
	localRepo := OpenRepo(dest)
	if _, err := localRepo.Pull(context.Background(), PullOptions{Remote: rc, Commit: commit}); err != nil {
		t.Fatalf("pull: %v", err)
	}

	// Fast path with all objects local: estimate must be zero.
	ps, err := RemotePullSize(context.Background(), rc, commit, localRepo)
	if err != nil {
		t.Fatalf("RemotePullSize: %v", err)
	}
	if !ps.FromSizesMeta {
		t.Fatalf("expected FromSizesMeta=true")
	}
	if ps.Compressed != 0 {
		t.Errorf("after full pull (sizes-meta path), size = %d, want 0", ps.Compressed)
	}
}
