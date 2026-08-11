//go:build linux

package ostree

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundriesio/ostreeuploader/pkg/gvariant"
)

// TestPullBareUserOnly pulls into a bare-user-only destination repo and confirms
// it is valid (ostree fsck) and matches the source listing. The source content
// is root-owned (ostree commit canonicalizes uid/gid to 0), so it is
// representable in bare-user-only.
func TestPullBareUserOnly(t *testing.T) {
	requireOstree(t)
	srcRepo, ref, commit := makeContentRepoOpts(t, true) // root-owned: representable in bare-user-only
	srv := httptest.NewServer(http.FileServer(http.Dir(srcRepo)))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "dest")
	r := OpenRepo(dest).WithMode(modeBareUserOnly)
	res, err := r.Pull(context.Background(), PullOptions{
		Remote: RemoteConfig{BaseURL: srv.URL},
		Ref:    ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != commit {
		t.Fatalf("commit %s want %s", res.Commit, commit)
	}
	if r.repoMode() != modeBareUserOnly {
		t.Fatalf("dest repo mode = %q, want bare-user-only", r.repoMode())
	}

	out := run(t, "ostree", "--repo="+dest, "fsck")
	if !strings.Contains(out, "no errors found") {
		t.Errorf("fsck failed:\n%s", out)
	}
	// The symlink object must be a real symlink in bare-user-only mode.
	srcLs := run(t, "ostree", "--repo="+srcRepo, "ls", "-R", commit)
	dstLs := run(t, "ostree", "--repo="+dest, "ls", "-R", commit)
	if srcLs != dstLs {
		t.Errorf("listings differ:\n--- src ---\n%s\n--- dst ---\n%s", srcLs, dstLs)
	}
}

// TestValidateBareUserOnly checks the mode guard: bare-user-only validates only
// the mode bits (rejecting setuid/setgid/sticky/world-write). uid/gid and xattrs
// are not rejected — the writer drops them, mirroring libostree.
func TestValidateBareUserOnly(t *testing.T) {
	cases := []struct {
		name string
		h    fileHeader
		ok   bool
	}{
		{"root 0644", fileHeader{uid: 0, gid: 0, mode: 0o100644}, true},
		{"root 0755", fileHeader{uid: 0, gid: 0, mode: 0o100755}, true},
		{"group-write 0775", fileHeader{uid: 0, gid: 0, mode: 0o100775}, true},
		{"nonzero uid ok", fileHeader{uid: 1000, gid: 1000, mode: 0o100644}, true},
		{"xattr ok", fileHeader{uid: 0, gid: 0, mode: 0o100644, xattrs: []gvariant.Xattr{{Name: []byte("user.x"), Value: []byte("y")}}}, true},
		{"setuid", fileHeader{uid: 0, gid: 0, mode: 0o104755}, false},
		{"world-write", fileHeader{uid: 0, gid: 0, mode: 0o100666}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateBareUserOnly("deadbeef", c.h)
			if c.ok && err != nil {
				t.Errorf("expected ok, got %v", err)
			}
			if !c.ok && err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}
