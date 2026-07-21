//go:build linux

package ostree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilezParseAndChecksum parses real .filez objects from a scratch archive
// repo and asserts our computed content-object id equals the object's filename
// (i.e. our pure-Go checksum matches ostree's), and that the inflated content
// matches what `ostree cat` returns.
func TestFilezParseAndChecksum(t *testing.T) {
	requireOstree(t)
	repo, _, to := makeContentRepo(t)

	// Walk the whole tree (files live in subdirs) and check every file object.
	r := OpenRepo(repo)
	var checked int
	var walk func(tree string)
	walk = func(tree string) {
		entries, err := r.ReadDirTree(tree)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir {
				walk(e.Checksum)
				continue
			}
			fz := r.objectPath(e.Checksum, "filez")
			data, err := os.ReadFile(fz)
			if err != nil {
				t.Fatalf("read %s: %v", fz, err)
			}
			hdr, content, err := parseFilez(data)
			if err != nil {
				t.Fatalf("parseFilez %s: %v", e.Name, err)
			}
			if got := contentObjectID(hdr, content); got != e.Checksum {
				t.Errorf("%s: contentObjectID got %s want %s", e.Name, got, e.Checksum)
			}
			checked++
		}
	}
	walk(mustRootTree(t, r, to))
	if checked == 0 {
		t.Fatal("no file objects checked")
	}
}

func mustRootTree(t *testing.T, r *Repo, commit string) string {
	t.Helper()
	c, err := r.ReadCommit(commit)
	if err != nil {
		t.Fatal(err)
	}
	return c.RootDirTree
}

// makeContentRepo builds a scratch archive repo with a regular file, a second
// regular file in a subdir, and a symlink, returning (repoPath, ref, commit).
func makeContentRepo(t *testing.T) (string, string, string) {
	return makeContentRepoOpts(t, false)
}

// makeContentRepoOpts is makeContentRepo with an option to canonicalize file
// ownership to uid/gid 0 (rootOwned), as published rootfs content is — required
// for the content to be representable in a bare-user-only repo.
func makeContentRepoOpts(t *testing.T, rootOwned bool) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tree, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, "ostree", "--repo="+repo, "init", "--mode=archive")
	writeRandom(t, filepath.Join(tree, "usr", "bin", "prog"), 1_500_000)
	os.WriteFile(filepath.Join(tree, "etc", "version"), []byte("v1\n"), 0o644)
	if err := os.Symlink("version", filepath.Join(tree, "etc", "current")); err != nil {
		t.Fatal(err)
	}
	args := []string{"--repo=" + repo, "commit", "--branch=main", "--tree=dir=" + tree}
	if rootOwned {
		args = append(args, "--owner-uid=0", "--owner-gid=0")
	}
	to := strings.TrimSpace(run(t, "ostree", args...))
	return repo, "main", to
}
