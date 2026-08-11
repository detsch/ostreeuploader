// Package ostree is a pure-Go reader of the ostree on-disk repository format.
//
// It links no libostree and shells out to no `ostree` binary: it reads the
// repository's files directly and decodes their GVariant contents (see
// pkg/gvariant). Its first feature is pre-flight storage estimation for static
// -delta updates (reading the superblock the same way `ostree static-delta
// show` does); the repo handle, ref resolver and commit/dirtree readers it also
// provides are the foundation for later object-fetching work.
package ostree

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Repo is a handle to a local ostree repository identified by its filesystem
// path (the directory containing "config", "objects", "refs", "deltas", ...).
type Repo struct {
	path     string
	mode     string    // cached core.mode from config; resolved once via modeOnce
	modeOnce sync.Once // guards the lazy read of mode in repoMode()
	initMode string    // mode to use when initializing a fresh repo (default bare-user)
}

// OpenRepo returns a handle for the repo at path. It does not validate eagerly;
// errors surface when an operation actually touches the repository.
func OpenRepo(path string) *Repo { return &Repo{path: path} }

// WithMode sets the on-disk mode used when initializing a fresh repo (see
// ensureRepo). It has no effect on an already-initialized repo, whose mode is
// read from its config. Valid values: "bare-user", "bare-user-only".
func (r *Repo) WithMode(mode string) *Repo {
	if mode != "" {
		r.initMode = mode
	}
	return r
}

// Path returns the repository path.
func (r *Repo) Path() string { return r.path }

// Repo modes this package can write into.
const (
	modeBareUser     = "bare-user"
	modeBareUserOnly = "bare-user-only"
)

// reModeLine matches a `mode=<value>` line in the repo config's [core] section.
var reModeLine = regexp.MustCompile(`(?m)^\s*mode\s*=\s*(\S+)\s*$`)

// repoMode returns the repository's core.mode (e.g. "bare-user-only"), read from
// <repo>/config and cached. It defaults to bare-user when the config is missing
// or has no mode line (matching what ensureRepo creates). Safe for concurrent
// use: the read happens exactly once.
func (r *Repo) repoMode() string {
	r.modeOnce.Do(func() {
		r.mode = modeBareUser
		if b, err := os.ReadFile(filepath.Join(r.path, "config")); err == nil {
			if m := reModeLine.FindSubmatch(b); m != nil {
				r.mode = string(m[1])
			}
		}
	})
	return r.mode
}

var hexCsumRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ResolveRef turns a ref into a 64-hex commit checksum. If ref already looks
// like a checksum it is returned unchanged; otherwise the ref file is read from
// refs/heads, then refs/remotes (matching the common ostree lookup order).
func (r *Repo) ResolveRef(ref string) (string, error) {
	if hexCsumRe.MatchString(ref) {
		return ref, nil
	}
	candidates := []string{
		filepath.Join(r.path, "refs", "heads", filepath.FromSlash(ref)),
		filepath.Join(r.path, "refs", "remotes", filepath.FromSlash(ref)),
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err == nil {
			csum := strings.TrimSpace(string(b))
			if !hexCsumRe.MatchString(csum) {
				return "", fmt.Errorf("ref %q: %q is not a commit checksum", ref, csum)
			}
			return csum, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("ref %q: %w", ref, err)
		}
	}
	return "", fmt.Errorf("ref %q not found", ref)
}

// objectPath returns the loose-object path objects/<csum[0:2]>/<csum[2:]>.<ext>.
func (r *Repo) objectPath(csum, ext string) string {
	return filepath.Join(r.path, "objects", csum[:2], csum[2:]+"."+ext)
}
