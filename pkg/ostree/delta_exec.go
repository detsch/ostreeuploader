//go:build linux

package ostree

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/foundriesio/ostreeuploader/pkg/varint"
)

// deltaState is the interpreter state for one delta part, mirroring libostree's
// StaticDeltaExecutionState (ostree-repo-static-delta-processing.c).
type deltaState struct {
	repo *Repo
	part *deltaPart
	objs []deltaObject

	objIndex int // index into objs; advanced by CLOSE

	// current open object
	outType byte
	outCsum string
	hdr     fileHeader // for content objects (from modes/xattrs)
	content []byte     // accumulated content for the open content object
	size    uint64     // declared content size

	// read source (the "from" object to copy/patch bytes out of)
	readSrc []byte
}

// execDeltaPart runs the operation bytecode of a part against the repo.
func (r *Repo) execDeltaPart(part *deltaPart, objs []deltaObject) error {
	st := &deltaState{repo: r, part: part, objs: objs}
	ops := part.ops
	for len(ops) > 0 {
		op := ops[0]
		ops = ops[1:]
		var err error
		switch op {
		case opOpenSpliceAndClose:
			ops, err = st.openSpliceAndClose(ops)
		case opOpen:
			ops, err = st.open(ops)
		case opWrite:
			ops, err = st.write(ops)
		case opSetReadSource:
			ops, err = st.setReadSource(ops)
		case opUnsetReadSource:
			st.readSrc = nil
		case opClose:
			err = st.close()
		case opBspatch:
			ops, err = st.bspatch(ops)
		default:
			return fmt.Errorf("unknown delta opcode %#x", op)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// readVarint consumes one LEB128 varint from the head of ops.
func readVarint(ops []byte) (uint64, []byte, error) {
	v, n, err := varint.Uvarint(ops)
	if err != nil {
		return 0, ops, err
	}
	return v, ops[n:], nil
}

// nextObject selects the object the current open op produces (objs[objIndex]).
func (st *deltaState) nextObject() error {
	if st.objIndex >= len(st.objs) {
		return fmt.Errorf("delta references more objects than declared (%d)", len(st.objs))
	}
	o := st.objs[st.objIndex]
	st.outType = o.objType
	st.outCsum = o.csum
	st.content = nil
	st.size = 0
	return nil
}

// validateOfs bounds-checks an (offset,length) against the data-source blob.
func (st *deltaState) validateOfs(offset, length uint64) error {
	if offset > uint64(len(st.part.data)) || length > uint64(len(st.part.data))-offset {
		return fmt.Errorf("delta op offset/length [%d,%d] out of data-source bounds %d", offset, length, len(st.part.data))
	}
	return nil
}

// contentOpen reads the mode/xattr offsets shared by content OPEN ops and sets
// the header for the object being produced.
func (st *deltaState) contentOpen(ops []byte) ([]byte, error) {
	modeOff, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	xattrOff, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	if modeOff >= uint64(len(st.part.modes)) {
		return ops, fmt.Errorf("mode offset %d out of range", modeOff)
	}
	m := st.part.modes[modeOff]
	st.hdr = fileHeader{uid: m[0], gid: m[1], mode: m[2]}
	if int(xattrOff) >= len(st.part.xattrs) {
		// An empty xattr dict is common; only error if a non-empty index is bad.
		if !(xattrOff == 0 && len(st.part.xattrs) == 0) {
			return ops, fmt.Errorf("xattr offset %d out of range", xattrOff)
		}
	} else {
		st.hdr.xattrs = parseXattrs(st.part.xattrs[xattrOff])
	}
	return ops, nil
}

// openSpliceAndClose ('S') writes a whole object in one operation.
func (st *deltaState) openSpliceAndClose(ops []byte) ([]byte, error) {
	if err := st.nextObject(); err != nil {
		return ops, err
	}
	if isMetaObjType(st.outType) {
		// metadata: length, offset into data-source -> write verbatim.
		length, ops, err := readVarint(ops)
		if err != nil {
			return ops, err
		}
		offset, ops, err := readVarint(ops)
		if err != nil {
			return ops, err
		}
		if err := st.validateOfs(offset, length); err != nil {
			return ops, err
		}
		raw := st.part.data[offset : offset+length]
		if err := st.repo.writeMetadataObject(st.outCsum, metaExt(st.outType), raw); err != nil {
			return ops, err
		}
		st.objIndex++
		return ops, nil
	}
	// content: mode/xattr offsets, then content_size, content_offset.
	ops, err := st.contentOpen(ops)
	if err != nil {
		return ops, err
	}
	size, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	offset, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	st.size = size
	if st.hdr.isSymlink() {
		// Symlink target lives in the data-source; content is empty.
		if err := st.validateOfs(offset, size); err != nil {
			return ops, err
		}
		st.hdr.symlink = string(st.part.data[offset : offset+size])
	} else {
		if err := st.validateOfs(offset, size); err != nil {
			return ops, err
		}
		st.content = append([]byte(nil), st.part.data[offset:offset+size]...)
	}
	if err := st.commitContent(); err != nil {
		return ops, err
	}
	st.objIndex++
	return ops, nil
}

// open ('o') begins a content object that subsequent WRITE/BSPATCH ops fill.
func (st *deltaState) open(ops []byte) ([]byte, error) {
	if err := st.nextObject(); err != nil {
		return ops, err
	}
	ops, err := st.contentOpen(ops)
	if err != nil {
		return ops, err
	}
	size, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	st.size = size
	st.content = make([]byte, 0, size)
	return ops, nil
}

// write ('w') appends bytes from the read-source object or the data-source blob.
func (st *deltaState) write(ops []byte) ([]byte, error) {
	size, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	offset, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	if st.readSrc != nil {
		if offset > uint64(len(st.readSrc)) || size > uint64(len(st.readSrc))-offset {
			return ops, fmt.Errorf("write: read-source range [%d,%d] out of bounds %d", offset, size, len(st.readSrc))
		}
		st.content = append(st.content, st.readSrc[offset:offset+size]...)
	} else {
		if err := st.validateOfs(offset, size); err != nil {
			return ops, err
		}
		st.content = append(st.content, st.part.data[offset:offset+size]...)
	}
	return ops, nil
}

// setReadSource ('r') loads an existing repo object's raw content as the copy
// source. The 32-byte object checksum sits in the data-source at source_offset.
func (st *deltaState) setReadSource(ops []byte) ([]byte, error) {
	offset, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	if err := st.validateOfs(offset, 32); err != nil {
		return ops, err
	}
	csum := hex.EncodeToString(st.part.data[offset : offset+32])
	content, err := st.repo.readContentObject(csum)
	if err != nil {
		return ops, fmt.Errorf("set-read-source %s: %w", csum, err)
	}
	st.readSrc = content
	return ops, nil
}

// bspatch ('B') applies a bsdiff patch (from the data-source) to the read-source
// content, producing st.size bytes appended to the open object.
func (st *deltaState) bspatch(ops []byte) ([]byte, error) {
	offset, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	length, ops, err := readVarint(ops)
	if err != nil {
		return ops, err
	}
	if err := st.validateOfs(offset, length); err != nil {
		return ops, err
	}
	if st.readSrc == nil {
		return ops, fmt.Errorf("bspatch with no read source")
	}
	out := make([]byte, st.size)
	if err := bspatchApply(st.readSrc, out, st.part.data[offset:offset+length]); err != nil {
		return ops, fmt.Errorf("bspatch: %w", err)
	}
	st.content = append(st.content, out...)
	return ops, nil
}

// close ('c') commits the open content object and advances the object index.
func (st *deltaState) close() error {
	if err := st.commitContent(); err != nil {
		return err
	}
	st.readSrc = nil
	st.objIndex++
	return nil
}

// commitContent writes the currently-open content object via the repo writer,
// which verifies its checksum against st.outCsum.
func (st *deltaState) commitContent() error {
	if st.outType != objFile {
		return fmt.Errorf("commit content: object %s is not a file object", st.outCsum)
	}
	return st.repo.writeContentObject(st.outCsum, st.hdr, st.content)
}

func isMetaObjType(t byte) bool { return t >= 2 && t <= 6 }
func metaExt(t byte) string {
	switch t {
	case objDirTree:
		return "dirtree"
	case objDirMeta:
		return "dirmeta"
	case objCommit:
		return "commit"
	case objCommitMeta:
		return "commitmeta"
	default:
		return fmt.Sprintf("objtype%d", t)
	}
}

// readContentObject reconstructs the raw (uncompressed) content of a content
// object already present in the repo, for use as a delta read-source.
func (r *Repo) readContentObject(csum string) ([]byte, error) {
	path := r.objectPath(csum, "file")
	if r.repoMode() == modeBareUserOnly {
		// The .file body IS the raw content (regular file) or, for a symlink,
		// the link target (no trailing NUL on disk).
		fi, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return nil, err
			}
			return []byte(target), nil
		}
		return os.ReadFile(path)
	}
	// bare-user: the .file body is the raw content (regular) or target+NUL
	// (symlink). Strip the trailing NUL for symlinks.
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return body, nil
}
