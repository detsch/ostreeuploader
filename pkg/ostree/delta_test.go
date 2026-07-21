//go:build linux

package ostree

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// requireOstree skips the test if the ostree CLI isn't available. ostree is used
// ONLY as the test oracle here, never by the library.
func requireOstree(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ostree"); err != nil {
		t.Skip("ostree CLI not found; skipping integration test")
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// makeDeltaRepo builds a scratch archive repo with two commits and a static
// delta between them, returning (repoPath, fromCommit, toCommit).
func makeDeltaRepo(t *testing.T) (string, string, string) {
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

	// v1
	writeRandom(t, filepath.Join(tree, "usr", "bin", "blob1"), 2_000_000)
	os.WriteFile(filepath.Join(tree, "etc", "version"), []byte("v1\n"), 0o644)
	from := strings.TrimSpace(run(t, "ostree", "--repo="+repo, "commit", "--branch=test", "--tree=dir="+tree))

	// v2
	writeRandom(t, filepath.Join(tree, "usr", "bin", "blob2"), 3_000_000)
	os.WriteFile(filepath.Join(tree, "etc", "version"), []byte("v2 updated\n"), 0o644)
	to := strings.TrimSpace(run(t, "ostree", "--repo="+repo, "commit", "--branch=test", "--tree=dir="+tree))

	run(t, "ostree", "--repo="+repo, "static-delta", "generate", "--from="+from, "--to="+to)
	return repo, from, to
}

func writeRandom(t *testing.T, path string, n int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, n)
	src, err := os.Open("/dev/urandom")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(buf); err != nil {
		t.Fatal(err)
	}
}

// TestDeltaSizeMatchesOstreeShow is the ground-truth guard: our superblock-parsed
// totals must equal the numbers `ostree static-delta show` prints. This both
// validates the pure-Go GVariant parsing and pins the on-disk format contract.
func TestDeltaSizeMatchesOstreeShow(t *testing.T) {
	requireOstree(t)
	repo, from, to := makeDeltaRepo(t)

	show := run(t, "ostree", "--repo="+repo, "static-delta", "show", from+"-"+to)
	wantSize := mustMatch(t, regexp.MustCompile(`(?m)^Total Size:\s+(\d+)`), show)
	wantUSize := mustMatch(t, regexp.MustCompile(`(?m)^Total Uncompressed Size:\s+(\d+)`), show)

	r := OpenRepo(repo)
	ds, err := r.LocalDeltaSize(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Compressed != wantSize {
		t.Errorf("compressed size: got %d, ostree says %d", ds.Compressed, wantSize)
	}
	if ds.Uncompressed != wantUSize {
		t.Errorf("uncompressed size: got %d, ostree says %d", ds.Uncompressed, wantUSize)
	}
}

func mustMatch(t *testing.T, re *regexp.Regexp, s string) uint64 {
	t.Helper()
	m := re.FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("pattern %q not found in:\n%s", re, s)
	}
	v, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestMissingDeltaErrors confirms a clean error when the delta is absent.
func TestMissingDeltaErrors(t *testing.T) {
	requireOstree(t)
	repo, _, _ := makeDeltaRepo(t)
	r := OpenRepo(repo)
	const a = "b7e6ee540547970b5fa75133f2348587929e74f03eb1132486759d4e473bdc8d"
	const b = "d284210256995ad1d1ac1cb9fbba95968b64c80b508c4bb32881a3d2c83525d5"
	if _, err := r.LocalDeltaSize(a, b); err == nil {
		t.Fatal("expected error for missing delta")
	}
}

// TestEstimateInsufficient checks the watermark gate fires when reserved space
// exceeds free space.
func TestEstimateInsufficient(t *testing.T) {
	requireOstree(t)
	repo, from, to := makeDeltaRepo(t)
	r := OpenRepo(repo)

	// Reserve an absurd amount so Available collapses to 0.
	_, err := r.EstimateLocalUpdate(from, to, repo, 1<<62, true)
	if err == nil {
		t.Fatal("expected insufficient storage error")
	}
	var ise *InsufficientStorageError
	if !asInsufficient(err, &ise) {
		t.Fatalf("expected InsufficientStorageError, got %T: %v", err, err)
	}
}

func asInsufficient(err error, target **InsufficientStorageError) bool {
	for err != nil {
		if e, ok := err.(*InsufficientStorageError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
