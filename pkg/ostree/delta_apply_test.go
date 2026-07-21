//go:build linux

package ostree

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeDeltaContentRepo builds an archive repo with two root-owned commits (so the
// content is representable in bare-user-only) and a static delta between them,
// returning (repoPath, fromCommit, toCommit). The content is structured and the
// v2 change is a small in-place edit of a large file, which makes the delta
// compiler emit rollsum (OPEN/WRITE/SET_READ_SOURCE/CLOSE) and bsdiff (BSPATCH)
// operations in addition to whole-object splices (OPEN_SPLICE_AND_CLOSE) -- so
// applying it exercises the full opcode set.
func makeDeltaContentRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	tree := filepath.Join(dir, "tree")
	mustMkdir(t, filepath.Join(tree, "usr", "bin"))
	mustMkdir(t, filepath.Join(tree, "etc"))
	run(t, "ostree", "--repo="+repo, "init", "--mode=archive")

	// v1: a large, structured (compressible, self-similar) blob so the delta
	// compiler finds rollsum/bsdiff matches against it.
	prog := filepath.Join(tree, "usr", "bin", "prog")
	writeStructured(t, prog, 4_000_000)
	os.WriteFile(filepath.Join(tree, "etc", "version"), []byte("v1\n"), 0o644)
	if err := os.Symlink("version", filepath.Join(tree, "etc", "current")); err != nil {
		t.Fatal(err)
	}
	from := strings.TrimSpace(run(t, "ostree", "--repo="+repo, "commit", "--branch=main",
		"--owner-uid=0", "--owner-gid=0", "--tree=dir="+tree))

	// v2: flip a small middle region of the big file (head and tail stay
	// identical -> rollsum match + bsdiff), and change a small file.
	editMiddle(t, prog)
	os.WriteFile(filepath.Join(tree, "etc", "version"), []byte("v2 updated\n"), 0o644)
	to := strings.TrimSpace(run(t, "ostree", "--repo="+repo, "commit", "--branch=main",
		"--owner-uid=0", "--owner-gid=0", "--tree=dir="+tree))

	run(t, "ostree", "--repo="+repo, "static-delta", "generate", "--from="+from, "--to="+to)
	return repo, from, to
}

// writeStructured writes n bytes of a deterministic, self-similar pattern.
func writeStructured(t *testing.T, path string, n int) {
	t.Helper()
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*131 + 7) % 256)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// editMiddle flips a small region in the middle of the file in place.
func editMiddle(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mid := len(b) / 2
	for i := mid; i < mid+50 && i < len(b); i++ {
		b[i]++
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestApplyStaticDelta pulls the `from` commit fully, then applies the from->to
// static delta with our pure-Go interpreter, and validates the result with
// ostree fsck and a listing comparison against the source `to` commit.
func TestApplyStaticDelta(t *testing.T) {
	requireOstree(t)
	srcRepo, from, to := makeDeltaContentRepo(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "dest")
	r := OpenRepo(dest).WithMode(modeBareUserOnly)

	// 1. Populate the `from` commit (full object pull) so read-source ops resolve.
	if _, err := r.Pull(context.Background(), PullOptions{Remote: RemoteConfig{BaseURL: srv.URL}, Commit: from}); err != nil {
		t.Fatalf("pull from: %v", err)
	}

	// 2. Fetch the commit metadata for `to` (the delta produces content/dirtree/
	// dirmeta but the commit object itself must be present for fsck/deploy).
	f := &fetcher{t: mustTransport(t, srv.URL), repo: r}
	if err := f.fetchMetadata(context.Background(), to, "commit"); err != nil {
		t.Fatalf("fetch to commit: %v", err)
	}

	// 3. Copy the delta dir into the dest repo (apply reads it locally), then
	// apply it.
	copyDeltaDir(t, srcRepo, dest, from, to)
	if err := r.markCommitPartial(to, true); err != nil {
		t.Fatal(err)
	}
	if err := r.applyStaticDelta(from, to); err != nil {
		t.Fatalf("applyStaticDelta: %v", err)
	}
	if err := r.markCommitPartial(to, false); err != nil {
		t.Fatal(err)
	}
	if err := r.writeRef("main", to); err != nil {
		t.Fatal(err)
	}

	// 4. Validate.
	out := run(t, "ostree", "--repo="+dest, "fsck")
	if !strings.Contains(out, "no errors found") {
		t.Errorf("fsck failed:\n%s", out)
	}
	srcLs := run(t, "ostree", "--repo="+srcRepo, "ls", "-R", to)
	dstLs := run(t, "ostree", "--repo="+dest, "ls", "-R", to)
	if srcLs != dstLs {
		t.Errorf("listings differ:\n--- src ---\n%s\n--- dst ---\n%s", srcLs, dstLs)
	}
}

func mustTransport(t *testing.T, url string) transport {
	t.Helper()
	tr, err := newTransport(RemoteConfig{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// copyDeltaDir copies the from->to static delta directory from src to dest repo.
func copyDeltaDir(t *testing.T, srcRepo, destRepo, from, to string) {
	t.Helper()
	rel, err := staticDeltaDir(from, to)
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(srcRepo, filepath.FromSlash(rel))
	dstDir := filepath.Join(destRepo, filepath.FromSlash(rel))
	mustMkdir(t, dstDir)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
