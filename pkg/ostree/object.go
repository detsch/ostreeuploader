package ostree

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/foundriesio/ostreeuploader/pkg/gvariant"
)

// GVariant type strings for ostree objects, pinned to libostree 2024.5
// (ostree-core.h). The drift guard is the integration test that cross-checks
// these against a repo produced by the installed ostree.
const (
	// commitFormat: (metadata, parent-csum, related, subject, body,
	//                timestamp, root-dirtree-csum, root-dirmeta-csum).
	commitFormat = "(a{sv}aya(say)sstayay)"
	// dirTreeFormat: (files=[(name, csum)], dirs=[(name, tree-csum, meta-csum)]).
	dirTreeFormat = "(a(say)a(sayay))"
)

// Commit is the decoded content of an ostree commit object.
type Commit struct {
	Subject     string
	Body        string
	Timestamp   uint64 // seconds since epoch (big-endian in the object)
	RootDirTree string // hex checksum of the root dirtree object
	RootDirMeta string // hex checksum of the root dirmeta object
}

// DirEntry is a child of a directory: a file or a subdirectory.
type DirEntry struct {
	Name     string
	Checksum string // file content / subdir dirtree checksum (hex)
	MetaSum  string // subdir dirmeta checksum (hex); empty for files
	IsDir    bool
}

// readObject reads and wraps the loose object <csum>.<ext> as a GVariant of the
// given format.
func (r *Repo) readObject(csum, ext, format string) (*gvariant.Value, error) {
	data, err := os.ReadFile(r.objectPath(csum, ext))
	if err != nil {
		return nil, fmt.Errorf("read %s object %s: %w", ext, csum, err)
	}
	v, err := gvariant.New(data, format)
	if err != nil {
		return nil, fmt.Errorf("decode %s object %s: %w", ext, csum, err)
	}
	return v, nil
}

// ReadCommit reads and decodes the commit object identified by csum.
func (r *Repo) ReadCommit(csum string) (*Commit, error) {
	v, err := r.readObject(csum, "commit", commitFormat)
	if err != nil {
		return nil, err
	}
	return decodeCommit(csum, v)
}

// decodeCommit decodes a commit object already wrapped as a GVariant.
func decodeCommit(csum string, v *gvariant.Value) (*Commit, error) {
	c := &Commit{
		Subject:     v.Child(3).Str(),
		Body:        v.Child(4).Str(),
		Timestamp:   v.Child(5).Uint64(),
		RootDirTree: hex.EncodeToString(v.Child(6).Bytes()),
		RootDirMeta: hex.EncodeToString(v.Child(7).Bytes()),
	}
	if err := v.Err(); err != nil {
		return nil, fmt.Errorf("commit %s: %w", csum, err)
	}
	return c, nil
}

// ReadDirTree reads and decodes the dirtree object identified by csum, returning
// its file and subdirectory entries.
func (r *Repo) ReadDirTree(csum string) ([]DirEntry, error) {
	v, err := r.readObject(csum, "dirtree", dirTreeFormat)
	if err != nil {
		return nil, err
	}
	return decodeDirTree(csum, v)
}

// decodeDirTree decodes a dirtree object already wrapped as a GVariant.
func decodeDirTree(csum string, v *gvariant.Value) ([]DirEntry, error) {
	var entries []DirEntry
	files := v.Child(0)
	for i := 0; i < files.Len(); i++ {
		e := files.At(i)
		entries = append(entries, DirEntry{
			Name:     e.Child(0).Str(),
			Checksum: hex.EncodeToString(e.Child(1).Bytes()),
		})
	}
	dirs := v.Child(1)
	for i := 0; i < dirs.Len(); i++ {
		e := dirs.At(i)
		entries = append(entries, DirEntry{
			Name:     e.Child(0).Str(),
			Checksum: hex.EncodeToString(e.Child(1).Bytes()),
			MetaSum:  hex.EncodeToString(e.Child(2).Bytes()),
			IsDir:    true,
		})
	}
	if err := v.Err(); err != nil {
		return nil, fmt.Errorf("dirtree %s: %w", csum, err)
	}
	return entries, nil
}

