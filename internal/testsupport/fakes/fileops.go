package fakes

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// maxLinkHops bounds symlink resolution in the in-memory tree.
const maxLinkHops = 40

// memNode is one entry of the in-memory file system.
type memNode struct {
	dir  bool
	link string // symlink target, as written
	data []byte
	perm fs.FileMode
}

// FileOps is an in-memory ports.FileOps. Paths are absolute and cleaned; the
// root "/" always exists. Like the real adapter, it refuses paths its guard
// forbids.
type FileOps struct {
	Faults

	guard paths.Guard
	mu    sync.Mutex
	nodes map[string]*memNode
}

// NewFileOps builds an empty file system that refuses what guard forbids.
func NewFileOps(guard paths.Guard) *FileOps {
	return &FileOps{guard: guard, nodes: map[string]*memNode{"/": {dir: true, perm: 0o755}}}
}

// AddFile creates a file and its parents, for test setup.
func (f *FileOps) AddFile(path, content string, perm fs.FileMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path = filepath.Clean(path)
	f.mkdirAllLocked(filepath.Dir(path), 0o755)
	f.nodes[path] = &memNode{data: []byte(content), perm: perm.Perm()}
}

// AddSymlink creates a symlink and its parents, for test setup. target is
// stored as given and may be relative or dangling.
func (f *FileOps) AddSymlink(path, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path = filepath.Clean(path)
	f.mkdirAllLocked(filepath.Dir(path), 0o755)
	f.nodes[path] = &memNode{link: target, perm: 0o777}
}

// Tree describes every entry at or below root, for byte-identical
// comparisons: "dir", "file <perm> <content>" or "link <target>".
func (f *FileOps) Tree(root string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	root = filepath.Clean(root)
	out := map[string]string{}
	for path, n := range f.nodes {
		if path == "/" || !within(path, root) {
			continue
		}
		switch {
		case n.dir:
			out[path] = "dir"
		case n.link != "":
			out[path] = "link " + n.link
		default:
			out[path] = fmt.Sprintf("file %04o %s", n.perm, n.data)
		}
	}
	return out
}

func within(path, root string) bool {
	return path == root || root == "/" || strings.HasPrefix(path, root+"/")
}

func (f *FileOps) check(op string, pathList ...string) error {
	if err := f.enter(op, pathList...); err != nil {
		return err
	}
	for _, p := range pathList {
		if err := f.guard.Check(p); err != nil {
			return fmt.Errorf("%s %s: %w", strings.ToLower(op), p, err)
		}
	}
	return nil
}

// resolveLocked follows symlinks in every component of path.
func (f *FileOps) resolveLocked(path string, followLast bool) (string, error) {
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	cur := "/"
	for hops := 0; len(parts) > 0; {
		if parts[0] == "" {
			parts = parts[1:]
			continue
		}
		next := filepath.Join(cur, parts[0])
		parts = parts[1:]
		n, ok := f.nodes[next]
		if ok && n.link != "" && (len(parts) > 0 || followLast) {
			hops++
			if hops > maxLinkHops {
				return "", fmt.Errorf("%s: too many links", path)
			}
			target := n.link
			if !filepath.IsAbs(target) {
				target = filepath.Join(cur, target)
			}
			parts = append(strings.Split(strings.TrimPrefix(filepath.Clean(target), "/"), "/"), parts...)
			cur = "/"
			continue
		}
		cur = next
	}
	return cur, nil
}

func notExist(op, path string) error {
	return &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
}

// ReadFile implements ports.FileOps.
func (f *FileOps) ReadFile(path string) ([]byte, error) {
	if err := f.check("ReadFile", path); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	real, err := f.resolveLocked(path, true)
	if err != nil {
		return nil, err
	}
	n, ok := f.nodes[real]
	if !ok {
		return nil, notExist("read", path)
	}
	if n.dir {
		return nil, &fs.PathError{Op: "read", Path: path, Err: fmt.Errorf("is a directory")}
	}
	return append([]byte(nil), n.data...), nil
}

// WriteFileAtomic implements ports.FileOps.
func (f *FileOps) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if err := f.check("WriteFileAtomic", path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path = filepath.Clean(path)
	if n, ok := f.nodes[path]; ok && n.dir {
		return &fs.PathError{Op: "write", Path: path, Err: fmt.Errorf("is a directory")}
	}
	f.mkdirAllLocked(filepath.Dir(path), 0o755)
	f.nodes[path] = &memNode{data: append([]byte(nil), data...), perm: perm.Perm()}
	return nil
}

// Rename implements ports.FileOps. Like os.Rename, it replaces a file or an
// empty directory at newPath, and moves a whole subtree.
func (f *FileOps) Rename(oldPath, newPath string) error {
	if err := f.check("Rename", oldPath, newPath); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	oldPath, newPath = filepath.Clean(oldPath), filepath.Clean(newPath)
	src, ok := f.nodes[oldPath]
	if !ok {
		return notExist("rename", oldPath)
	}
	if _, ok := f.nodes[filepath.Dir(newPath)]; !ok {
		return notExist("rename", newPath)
	}
	if dst, ok := f.nodes[newPath]; ok && dst.dir {
		if !src.dir || len(f.childrenLocked(newPath)) > 0 {
			return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrExist}
		}
	}
	moved := map[string]*memNode{}
	for path, n := range f.nodes {
		if within(path, oldPath) && path != "/" {
			moved[newPath+strings.TrimPrefix(path, oldPath)] = n
			delete(f.nodes, path)
		}
	}
	for path, n := range moved {
		f.nodes[path] = n
	}
	return nil
}

// CopyTree implements ports.FileOps.
func (f *FileOps) CopyTree(src, dst string) error {
	if err := f.check("CopyTree", src, dst); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	src, dst = filepath.Clean(src), filepath.Clean(dst)
	if n, ok := f.nodes[src]; !ok || !n.dir {
		return notExist("copy", src)
	}
	if _, ok := f.nodes[dst]; ok {
		return &fs.PathError{Op: "copy", Path: dst, Err: fs.ErrExist}
	}
	f.mkdirAllLocked(filepath.Dir(dst), 0o755)
	copies := map[string]*memNode{}
	for path, n := range f.nodes {
		if within(path, src) {
			c := *n
			c.data = append([]byte(nil), n.data...)
			copies[dst+strings.TrimPrefix(path, src)] = &c
		}
	}
	for path, n := range copies {
		f.nodes[path] = n
	}
	return nil
}

// RemoveTree implements ports.FileOps.
func (f *FileOps) RemoveTree(path string) error {
	if err := f.check("RemoveTree", path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path = filepath.Clean(path)
	if path == "/" {
		return fmt.Errorf("remove /: refused")
	}
	for p := range f.nodes {
		if within(p, path) {
			delete(f.nodes, p)
		}
	}
	return nil
}

// MkdirAll implements ports.FileOps.
func (f *FileOps) MkdirAll(path string, perm fs.FileMode) error {
	if err := f.check("MkdirAll", path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.nodes[filepath.Clean(path)]; ok && !n.dir {
		return &fs.PathError{Op: "mkdir", Path: path, Err: fs.ErrExist}
	}
	f.mkdirAllLocked(filepath.Clean(path), perm)
	return nil
}

func (f *FileOps) mkdirAllLocked(path string, perm fs.FileMode) {
	for p := path; ; p = filepath.Dir(p) {
		if _, ok := f.nodes[p]; !ok {
			f.nodes[p] = &memNode{dir: true, perm: perm.Perm()}
		}
		if p == "/" {
			return
		}
	}
}

func (f *FileOps) childrenLocked(dir string) []string {
	var out []string
	for p := range f.nodes {
		if p != dir && filepath.Dir(p) == dir {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Mode implements ports.FileOps; it does not follow a final symlink.
func (f *FileOps) Mode(path string) (fs.FileMode, error) {
	if err := f.check("Mode", path); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	real, err := f.resolveLocked(path, false)
	if err != nil {
		return 0, err
	}
	n, ok := f.nodes[real]
	switch {
	case !ok:
		return 0, notExist("lstat", path)
	case n.dir:
		return fs.ModeDir | n.perm, nil
	case n.link != "":
		return fs.ModeSymlink | n.perm, nil
	default:
		return n.perm, nil
	}
}

// Chmod implements ports.FileOps.
func (f *FileOps) Chmod(path string, perm fs.FileMode) error {
	if err := f.check("Chmod", path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	real, err := f.resolveLocked(path, true)
	if err != nil {
		return err
	}
	n, ok := f.nodes[real]
	if !ok {
		return notExist("chmod", path)
	}
	n.perm = perm.Perm()
	return nil
}

// Exists implements ports.FileOps; it does not follow a final symlink.
func (f *FileOps) Exists(path string) (bool, error) {
	if err := f.check("Exists", path); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	real, err := f.resolveLocked(path, false)
	if err != nil {
		return false, err
	}
	_, ok := f.nodes[real]
	return ok, nil
}

// FailingFileOps wraps another ports.FileOps, such as the real adapter on a
// temporary directory, and injects failures before a call reaches it. A
// failing call never reaches Inner.
type FailingFileOps struct {
	Faults
	Inner ports.FileOps
}

func (w *FailingFileOps) ReadFile(path string) ([]byte, error) {
	if err := w.enter("ReadFile", path); err != nil {
		return nil, err
	}
	return w.Inner.ReadFile(path)
}

func (w *FailingFileOps) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if err := w.enter("WriteFileAtomic", path); err != nil {
		return err
	}
	return w.Inner.WriteFileAtomic(path, data, perm)
}

func (w *FailingFileOps) Rename(oldPath, newPath string) error {
	if err := w.enter("Rename", oldPath, newPath); err != nil {
		return err
	}
	return w.Inner.Rename(oldPath, newPath)
}

func (w *FailingFileOps) CopyTree(src, dst string) error {
	if err := w.enter("CopyTree", src, dst); err != nil {
		return err
	}
	return w.Inner.CopyTree(src, dst)
}

func (w *FailingFileOps) RemoveTree(path string) error {
	if err := w.enter("RemoveTree", path); err != nil {
		return err
	}
	return w.Inner.RemoveTree(path)
}

func (w *FailingFileOps) MkdirAll(path string, perm fs.FileMode) error {
	if err := w.enter("MkdirAll", path); err != nil {
		return err
	}
	return w.Inner.MkdirAll(path, perm)
}

func (w *FailingFileOps) Mode(path string) (fs.FileMode, error) {
	if err := w.enter("Mode", path); err != nil {
		return 0, err
	}
	return w.Inner.Mode(path)
}

func (w *FailingFileOps) Chmod(path string, perm fs.FileMode) error {
	if err := w.enter("Chmod", path); err != nil {
		return err
	}
	return w.Inner.Chmod(path, perm)
}

func (w *FailingFileOps) Exists(path string) (bool, error) {
	if err := w.enter("Exists", path); err != nil {
		return false, err
	}
	return w.Inner.Exists(path)
}
