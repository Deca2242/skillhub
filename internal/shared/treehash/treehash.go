// Package treehash computes git object SHAs the way git does, so skillhub
// can compare a folder on disk with a tree on GitHub or in a clone, and with
// the skillFolderHash of the npx skills lock. It is the only implementation
// of this hash in skillhub: BlobSHA hashes one content, Dir hashes a
// folder.
//
// Dir gives the SHA git would record for the folder after git add -A -f and
// git write-tree: ignore rules do not apply, empty directories and every
// entry named .git are left out, a regular file is 100755 when its owner
// execute bit is set and 100644 otherwise, and a symlink is a 120000 blob
// holding its target.
package treehash

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // git object ids are SHA-1; this is not a security use.
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
)

// Mode is a git tree entry mode.
type Mode string

const (
	ModeFile       Mode = "100644"
	ModeExecutable Mode = "100755"
	ModeSymlink    Mode = "120000"
	ModeTree       Mode = "040000"
	ModeSubmodule  Mode = "160000"
)

// objectMode is the mode as written inside a tree object, where a tree is
// "40000", without the leading zero that ls-tree and the GitHub API show.
func (m Mode) objectMode() string {
	if m == ModeTree {
		return "40000"
	}
	return string(m)
}

// ErrUnsupportedEntry reports something git cannot hash as a folder: a root
// that is not a directory, or an entry that is not a directory, a regular
// file or a symlink.
var ErrUnsupportedEntry = errors.New("unsupported entry")

// Node is one entry of a tree, listed recursively.
type Node struct {
	// Path is slash-separated and relative to the tree's root.
	Path string `json:"path"`
	Mode Mode   `json:"mode"`
	// SHA is the blob, tree or commit SHA, lowercase hex.
	SHA string `json:"sha"`
	// Size is the content size of a blob; 0 for a tree or a submodule.
	Size int64 `json:"size"`
}

// IsTree reports whether the node is a subtree.
func (n Node) IsTree() bool { return n.Mode == ModeTree }

// Tree is a tree's SHA and every node below it.
type Tree struct {
	SHA string `json:"sha"`
	// Nodes lists every blob, subtree and submodule below the root, depth
	// first, each level in git's order, like git ls-tree -r -t.
	Nodes []Node `json:"nodes"`
}

// BlobSHA returns the git blob SHA of content.
func BlobSHA(content []byte) string {
	h := newObjectHash("blob", int64(len(content)))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// Reader provides read-only relative directory traversal and file reading for
// computing a git tree object without symlinks or .git entries.
type Reader interface {
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
}

// Hash hashes the directory exposed through r as git would for a regular-file
// skill tree. Unlike Dir, it rejects .git entries and symlinks with
// ErrUnsupportedEntry so descriptor-bound readers cannot silently skip them.
func Hash(r Reader) (Tree, error) {
	if r == nil {
		return Tree{}, fmt.Errorf("hash: %w: nil reader", ErrUnsupportedEntry)
	}
	info, err := r.Stat(".")
	if err != nil {
		return Tree{}, fmt.Errorf("hash .: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return Tree{}, fmt.Errorf("hash .: %w: not a directory", ErrUnsupportedEntry)
	}
	sha, nodes, err := hashReaderDir(r, ".")
	if err != nil {
		return Tree{}, err
	}
	return Tree{SHA: sha, Nodes: nodes}, nil
}

// Dir hashes the folder root as git would. A symlink as root is followed;
// symlinks below it are hashed as links.
func Dir(root string) (Tree, error) {
	info, err := os.Stat(root)
	if err != nil {
		return Tree{}, fmt.Errorf("hash %s: %w", root, err)
	}
	if !info.IsDir() {
		return Tree{}, fmt.Errorf("hash %s: %w: not a directory", root, ErrUnsupportedEntry)
	}
	sha, nodes, err := hashDir(root, "")
	if err != nil {
		return Tree{}, err
	}
	return Tree{SHA: sha, Nodes: nodes}, nil
}

// item is one entry of the tree object being built.
type item struct {
	node  Node
	name  string
	below []Node
}

// hashDir returns the SHA of the directory abs, whose path in the tree is
// rel, and the nodes below it. A directory with no nodes is an empty tree,
// which the caller leaves out.
func hashDir(abs, rel string) (string, []Node, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", nil, fmt.Errorf("hash %s: %w", abs, err)
	}
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		it, keep, err := hashEntry(filepath.Join(abs, name), path.Join(rel, name), entry)
		if err != nil {
			return "", nil, err
		}
		if keep {
			it.name = name
			items = append(items, it)
		}
	}
	return serializeTree(abs, items)
}

func serializeTree(label string, items []item) (string, []Node, error) {
	// git sorts a tree's entries by name, comparing a subtree as if its
	// name ended in "/".
	slices.SortFunc(items, func(a, b item) int {
		return bytes.Compare(sortKey(a), sortKey(b))
	})

	var object bytes.Buffer
	var nodes []Node
	for _, it := range items {
		raw, err := hex.DecodeString(it.node.SHA)
		if err != nil {
			return "", nil, fmt.Errorf("hash %s: %w", label, err)
		}
		object.WriteString(it.node.Mode.objectMode())
		object.WriteByte(' ')
		object.WriteString(it.name)
		object.WriteByte(0)
		object.Write(raw)
		nodes = append(nodes, it.node)
		nodes = append(nodes, it.below...)
	}
	h := newObjectHash("tree", int64(object.Len()))
	h.Write(object.Bytes())
	return hex.EncodeToString(h.Sum(nil)), nodes, nil
}

func hashReaderDir(r Reader, relDir string) (string, []Node, error) {
	entries, err := r.ReadDir(relDir)
	if err != nil {
		return "", nil, fmt.Errorf("hash %s: %w", relDir, err)
	}
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			return "", nil, fmt.Errorf("hash %s: %w: .git entry %q", relDir, ErrUnsupportedEntry, name)
		}
		childRel := name
		if relDir != "." {
			childRel = path.Join(relDir, name)
		}
		it, keep, err := hashReaderEntry(r, childRel, entry)
		if err != nil {
			return "", nil, err
		}
		if keep {
			it.name = name
			items = append(items, it)
		}
	}
	return serializeTree(relDir, items)
}

func hashReaderEntry(r Reader, rel string, entry fs.DirEntry) (item, bool, error) {
	if entry.Type()&fs.ModeSymlink != 0 {
		return item{}, false, fmt.Errorf("hash %s: %w: symlink", rel, ErrUnsupportedEntry)
	}
	info, err := entry.Info()
	if err != nil {
		return item{}, false, fmt.Errorf("hash %s: %w", rel, err)
	}
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return item{}, false, fmt.Errorf("hash %s: %w: symlink", rel, ErrUnsupportedEntry)
	case mode.IsDir():
		sha, below, err := hashReaderDir(r, rel)
		if err != nil {
			return item{}, false, err
		}
		if len(below) == 0 {
			return item{}, false, nil
		}
		return item{node: Node{Path: rel, Mode: ModeTree, SHA: sha}, below: below}, true, nil
	case mode.IsRegular():
		data, err := r.ReadFile(rel)
		if err != nil {
			return item{}, false, fmt.Errorf("hash %s: %w", rel, err)
		}
		if int64(len(data)) != info.Size() {
			return item{}, false, fmt.Errorf(
				"hash %s: the file changed size while it was read (%d, then %d bytes)",
				rel,
				info.Size(),
				len(data),
			)
		}
		fileMode := ModeFile
		if mode.Perm()&0o100 != 0 {
			fileMode = ModeExecutable
		}
		return item{node: Node{Path: rel, Mode: fileMode, SHA: BlobSHA(data), Size: int64(len(data))}}, true, nil
	default:
		return item{}, false, fmt.Errorf("hash %s: %w: %s", rel, ErrUnsupportedEntry, mode.Type())
	}
}

func sortKey(it item) []byte {
	if it.node.IsTree() {
		return []byte(it.name + "/")
	}
	return []byte(it.name)
}

// hashEntry hashes one directory entry. keep is false for an entry git
// leaves out: an empty directory.
func hashEntry(abs, rel string, entry fs.DirEntry) (item, bool, error) {
	info, err := entry.Info()
	if err != nil {
		return item{}, false, fmt.Errorf("hash %s: %w", abs, err)
	}
	switch mode := info.Mode(); {
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(abs)
		if err != nil {
			return item{}, false, fmt.Errorf("hash %s: %w", abs, err)
		}
		// git stores the target with slashes.
		content := []byte(filepath.ToSlash(target))
		node := Node{Path: rel, Mode: ModeSymlink, SHA: BlobSHA(content), Size: int64(len(content))}
		return item{node: node}, true, nil
	case mode.IsDir():
		sha, below, err := hashDir(abs, rel)
		if err != nil {
			return item{}, false, err
		}
		if len(below) == 0 {
			return item{}, false, nil
		}
		return item{node: Node{Path: rel, Mode: ModeTree, SHA: sha}, below: below}, true, nil
	case mode.IsRegular():
		sha, err := hashFile(abs, info.Size())
		if err != nil {
			return item{}, false, err
		}
		fileMode := ModeFile
		if mode.Perm()&0o100 != 0 {
			fileMode = ModeExecutable
		}
		return item{node: Node{Path: rel, Mode: fileMode, SHA: sha, Size: info.Size()}}, true, nil
	default:
		return item{}, false, fmt.Errorf("hash %s: %w: %s", abs, ErrUnsupportedEntry, mode.Type())
	}
}

// hashFile streams a file into a blob hash. The file must still have the
// size it was listed with, or the header would not match its content.
func hashFile(abs string, size int64) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", abs, err)
	}
	defer f.Close()
	h := newObjectHash("blob", size)
	n, err := io.Copy(h, f)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", abs, err)
	}
	if n != size {
		return "", fmt.Errorf("hash %s: the file changed size while it was read (%d, then %d bytes)", abs, size, n)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// newObjectHash starts the SHA-1 of a git object with its header.
func newObjectHash(kind string, size int64) hash.Hash {
	h := sha1.New() //nolint:gosec // see the import.
	h.Write([]byte(kind + " " + strconv.FormatInt(size, 10) + "\x00"))
	return h
}
