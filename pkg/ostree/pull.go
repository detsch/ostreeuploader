//go:build linux

package ostree

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// PullOptions configures a pull. Exactly one of Commit or Ref identifies what to
// fetch; if both are empty Pull fails. When Ref is given, it is resolved against
// the remote's refs/heads/<ref>.
type PullOptions struct {
	Remote RemoteConfig
	Ref    string // e.g. "main" (resolved via remote refs/heads)
	Commit string // explicit commit checksum (takes precedence over Ref)
	// From is the current (base) commit. When set and UseDelta is true, Pull
	// tries the from->to static delta the remote publishes before falling back
	// to a full object pull.
	From string
	// UseDelta enables the static-delta fast path (default: enabled when From is
	// set). Set NoDelta to force a full object pull.
	NoDelta bool
	// Concurrency bounds parallel content-object downloads (default 4).
	Concurrency int
}

// PullResult reports what a pull did.
type PullResult struct {
	Commit          string
	MetaFetched     int    // metadata objects downloaded
	ContentFetched  int    // content objects downloaded
	ObjectsSkipped  int    // objects already present (resume)
	BytesDownloaded uint64 // total bytes pulled over the wire this run
	UsedDelta       bool   // true if the static-delta fast path was used
}

// fetcher pairs a transport with the destination repo and accumulates stats.
type fetcher struct {
	t    transport
	repo *Repo

	mu    sync.Mutex
	stats PullResult
}

// Pull fetches the commit (and every object it references) from the remote
// archive repo into the local bare-user repo, resuming any prior interrupted
// attempt at both the object and byte level. It is safe to re-run.
func (r *Repo) Pull(ctx context.Context, opts PullOptions) (*PullResult, error) {
	if err := r.ensureRepo(); err != nil {
		return nil, err
	}
	// Resolve the repo mode once now, before any concurrent content writes read
	// it, so the lazy cache is populated single-threaded.
	r.repoMode()

	t, err := newTransport(opts.Remote)
	if err != nil {
		return nil, err
	}
	f := &fetcher{t: t, repo: r}

	commit := opts.Commit
	if commit == "" {
		if opts.Ref == "" {
			return nil, fmt.Errorf("pull: neither Commit nor Ref given")
		}
		commit, err = f.resolveRemoteRef(ctx, opts.Ref)
		if err != nil {
			return nil, err
		}
	}
	if !hexCsumRe.MatchString(commit) {
		return nil, fmt.Errorf("pull: %q is not a commit checksum", commit)
	}

	// Mark the commit partial up front; cleared only once every referenced
	// object is present, so an interrupted pull resumes safely.
	if err := r.markCommitPartial(commit, true); err != nil {
		return nil, err
	}

	// 1. Commit object (+ optional detached commitmeta).
	if err := f.fetchMetadata(ctx, commit, "commit"); err != nil {
		return nil, fmt.Errorf("fetch commit: %w", err)
	}
	if err := f.fetchOptionalMetadata(ctx, commit, "commitmeta"); err != nil {
		return nil, err
	}
	c, err := r.ReadCommit(commit)
	if err != nil {
		return nil, err
	}

	// 2. Prefer the static-delta fast path when a base commit is given and the
	// remote publishes a from->to delta; otherwise fall back to a full pull.
	deltaDone := false
	if opts.From != "" && !opts.NoDelta {
		ok, err := f.tryDeltaPull(ctx, opts.From, commit)
		if err != nil {
			return nil, err
		}
		deltaDone = ok
		f.stats.UsedDelta = ok
	}

	if !deltaDone {
		// Full object pull: walk the tree, fetching metadata depth-first and
		// collecting the set of content objects to download.
		content := newCsumSet()
		if err := f.walkDirTree(ctx, c.RootDirTree, c.RootDirMeta, content); err != nil {
			return nil, err
		}
		// Fetch content objects concurrently (each resumable).
		if err := f.fetchContentObjects(ctx, content.list(), opts.Concurrency); err != nil {
			return nil, err
		}
	}

	// 3. All objects present: clear the partial marker, then write the ref.
	if err := r.markCommitPartial(commit, false); err != nil {
		return nil, err
	}
	if opts.Ref != "" {
		if err := r.writeRef(opts.Ref, commit); err != nil {
			return nil, err
		}
	}

	f.stats.Commit = commit
	res := f.stats
	return &res, nil
}

// resolveRemoteRef fetches refs/heads/<ref> from the remote and returns the
// commit checksum.
func (f *fetcher) resolveRemoteRef(ctx context.Context, ref string) (string, error) {
	data, err := f.get(ctx, filepath.ToSlash(filepath.Join("refs", "heads", ref)))
	if err != nil {
		return "", fmt.Errorf("resolve remote ref %q: %w", ref, err)
	}
	csum := trimRef(data)
	if !hexCsumRe.MatchString(csum) {
		return "", fmt.Errorf("remote ref %q: %q is not a commit checksum", ref, csum)
	}
	return csum, nil
}

func trimRef(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// walkDirTree fetches a dirtree and its dirmeta (skipping any already present),
// records its file content objects, and recurses into subdirectories.
func (f *fetcher) walkDirTree(ctx context.Context, treeCsum, metaCsum string, content *csumSet) error {
	if err := f.fetchMetadata(ctx, metaCsum, "dirmeta"); err != nil {
		return fmt.Errorf("fetch dirmeta %s: %w", metaCsum, err)
	}
	if err := f.fetchMetadata(ctx, treeCsum, "dirtree"); err != nil {
		return fmt.Errorf("fetch dirtree %s: %w", treeCsum, err)
	}
	entries, err := f.repo.ReadDirTree(treeCsum)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir {
			if err := f.walkDirTree(ctx, e.Checksum, e.MetaSum, content); err != nil {
				return err
			}
		} else {
			content.add(e.Checksum)
		}
	}
	return nil
}

// fetchMetadata downloads a metadata object (small; no byte-resume needed) and
// writes it verbatim, unless it is already present.
func (f *fetcher) fetchMetadata(ctx context.Context, csum, ext string) error {
	if f.repo.hasObject(csum, ext) {
		f.addSkipped()
		return nil
	}
	data, err := f.get(ctx, objectRelPath(csum, ext))
	if err != nil {
		return err
	}
	if err := f.repo.writeMetadataObject(csum, ext, data); err != nil {
		return err
	}
	f.addMeta(uint64(len(data)))
	return nil
}

// fetchOptionalMetadata is like fetchMetadata but tolerates a missing object
// (e.g. detached commitmeta, which many commits lack).
func (f *fetcher) fetchOptionalMetadata(ctx context.Context, csum, ext string) error {
	err := f.fetchMetadata(ctx, csum, ext)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

// fetchContentObjects downloads each content object (resumably) and converts it
// to a bare-user .file object, with bounded concurrency.
func (f *fetcher) fetchContentObjects(ctx context.Context, csums []string, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var (
		errMu    sync.Mutex
		firstErr error
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, csum := range csums {
		csum := csum
		if f.repo.hasObject(csum, "file") {
			f.addSkipped()
			continue
		}
		// Acquire a slot, or stop launching work once the context is cancelled
		// (e.g. an earlier fetch failed). Taking the ctx.Done() branch must NOT
		// launch a goroutine: it never acquired a slot, so its release would
		// unbalance the semaphore and deadlock wg.Wait().
		select {
		case <-ctx.Done():
			// fall through to break out of the loop below
		case sem <- struct{}{}:
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := f.fetchOneContent(ctx, csum); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
				}
			}()
			continue
		}
		break
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	// The loop may have stopped early because the parent context was cancelled
	// without any fetch recording an error; surface that rather than a false ok.
	return ctx.Err()
}

// fetchOneContent resumably downloads a single .filez content object into a
// .part sidecar, then parses, verifies and writes the bare-user object.
func (f *fetcher) fetchOneContent(ctx context.Context, csum string) error {
	part := filepath.Join(f.repo.path, "tmp", csum+".filez.part")
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		return err
	}
	n, err := f.downloadResumable(ctx, objectRelPath(csum, "filez"), part)
	if err != nil {
		return fmt.Errorf("download %s: %w", csum, err)
	}

	data, err := os.ReadFile(part)
	if err != nil {
		return err
	}
	hdr, content, err := parseFilez(data)
	if err != nil {
		// A corrupt/incomplete part: drop it so a re-run starts clean.
		os.Remove(part)
		return fmt.Errorf("parse %s: %w", csum, err)
	}
	if err := f.repo.writeContentObject(csum, hdr, content); err != nil {
		os.Remove(part)
		return err
	}
	os.Remove(part)
	f.addContent(uint64(n))
	return nil
}

// downloadResumable downloads relPath into the part file, resuming from its
// current size when the server honors a Range request. It returns the number of
// bytes transferred during THIS call (i.e. excluding bytes already on disk from
// a previous attempt). When the server ignores Range (responds 200), the part
// is truncated and refetched from the start.
func (f *fetcher) downloadResumable(ctx context.Context, relPath, part string) (int64, error) {
	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
	}

	rc, resumed, err := f.t.open(ctx, relPath, offset)
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	flags := os.O_CREATE | os.O_WRONLY
	if resumed {
		flags |= os.O_APPEND // continue after the existing bytes
	} else {
		flags |= os.O_TRUNC // whole object (fresh, or server ignored Range)
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.Copy(out, rc)
	if err != nil {
		return n, err // partial bytes are kept on disk for the next resume
	}
	return n, nil
}

// get downloads a whole small object into memory (used for metadata and refs).
func (f *fetcher) get(ctx context.Context, relPath string) ([]byte, error) {
	rc, _, err := f.t.open(ctx, relPath, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// --- stats helpers (concurrency-safe) ---

func (f *fetcher) addMeta(n uint64) {
	f.mu.Lock()
	f.stats.MetaFetched++
	f.stats.BytesDownloaded += n
	f.mu.Unlock()
}

func (f *fetcher) addContent(n uint64) {
	f.mu.Lock()
	f.stats.ContentFetched++
	f.stats.BytesDownloaded += n
	f.mu.Unlock()
}

func (f *fetcher) addSkipped() {
	f.mu.Lock()
	f.stats.ObjectsSkipped++
	f.mu.Unlock()
}

// csumSet is a small ordered, deduplicated set of checksums.
type csumSet struct {
	seen  map[string]bool
	order []string
}

func newCsumSet() *csumSet { return &csumSet{seen: map[string]bool{}} }

func (s *csumSet) add(c string) {
	if !s.seen[c] {
		s.seen[c] = true
		s.order = append(s.order, c)
	}
}

func (s *csumSet) list() []string { return s.order }
