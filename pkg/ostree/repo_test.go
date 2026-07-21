//go:build linux

package ostree

import (
	"strings"
	"testing"
)

// TestResolveAndReadCommit resolves the test ref to its commit and decodes the
// commit + root dirtree purely in Go, cross-checking against the ostree CLI.
func TestResolveAndReadCommit(t *testing.T) {
	requireOstree(t)
	repo, _, to := makeDeltaRepo(t)
	r := OpenRepo(repo)

	csum, err := r.ResolveRef("test")
	if err != nil {
		t.Fatal(err)
	}
	if csum != to {
		t.Fatalf("ResolveRef(test): got %s want %s", csum, to)
	}

	c, err := r.ReadCommit(csum)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.RootDirTree) != 64 {
		t.Fatalf("root dirtree checksum looks wrong: %q", c.RootDirTree)
	}

	// Walk the root dirtree; it must contain the "usr" and "etc" subdirs we
	// committed.
	entries, err := r.ReadDirTree(c.RootDirTree)
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if e.IsDir {
			dirs[e.Name] = true
		}
	}
	for _, want := range []string{"usr", "etc"} {
		if !dirs[want] {
			t.Errorf("root dirtree missing subdir %q (got %v)", want, dirs)
		}
	}

	// Cross-check against `ostree ls`: the root listing should mention usr/etc.
	ls := run(t, "ostree", "--repo="+repo, "ls", csum)
	if !strings.Contains(ls, "/usr") || !strings.Contains(ls, "/etc") {
		t.Errorf("ostree ls disagrees:\n%s", ls)
	}
}
