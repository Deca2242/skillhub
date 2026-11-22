package fs

import (
	"crypto/rand"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

var _ ports.LinkOps = (*LinkOps)(nil)

// errNoReplaceUnsupported reports a folder move on a kernel or file system
// without RENAME_NOREPLACE: link(2) cannot publish a folder, and a plain
// rename could replace an empty folder, so the move is refused.
var errNoReplaceUnsupported = errors.New("the file system cannot move a folder without replacing")

// sysCalls is the seam between LinkOps and the kernel. Tests replace single
// calls to inject EXDEV, force the link(2) fallback or count fsyncs.
type sysCalls struct {
	renameat2 func(oldDirFD int, oldName string, newDirFD int, newName string, flags uint) error
	linkat    func(oldDirFD int, oldName string, newDirFD int, newName string, flags int) error
	fsync     func(fd int) error
	// opened runs after the guard accepted a path and before its parent is
	// opened; nil in production. Tests use it to swap a parent in between.
	opened func(dir string)
}

func realSysCalls() sysCalls {
	return sysCalls{renameat2: unix.Renameat2, linkat: unix.Linkat, fsync: unix.Fsync}
}

// LinkOps is the real ports.LinkOps. Every mutation checks each path and its
// parent against the guard, then pins the parent by opening it, checks the
// opened folder's real location again and works relative to that handle, so
// a parent swapped after the check cannot redirect the change. The final
// component is never followed.
type LinkOps struct {
	guard paths.Guard
	sys   sysCalls
}

// NewLinkOps builds a LinkOps that refuses every path guard refuses. The
// composition root passes Paths.Guard().
func NewLinkOps(guard paths.Guard) *LinkOps {
	return &LinkOps{guard: guard, sys: realSysCalls()}
}

// check runs the guard over each path and its parent. The parent check is
// not redundant: the guard resolves a final symlink, so a link inside a
// forbidden folder, reached through a parent link and pointing outside it,
// passes a check of the path alone.
func (l *LinkOps) check(op string, pathList ...string) error {
	for _, p := range pathList {
		if err := l.guard.Check(p); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		clean := filepath.Clean(p)
		if clean == string(filepath.Separator) {
			return fmt.Errorf("%s: %w: the root has no parent", op, paths.ErrInvalidPath)
		}
		if err := l.guard.Check(filepath.Dir(clean)); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
	}
	return nil
}

// parentDir is an open handle on the folder that holds one path.
type parentDir struct {
	fd   int
	name string // the final component, relative to fd
}

// openParent opens the parent of path and checks where the handle really
// points, so the change happens inside the folder the guard accepted.
func (l *LinkOps) openParent(op, path string) (parentDir, error) {
	clean := filepath.Clean(path)
	dir, name := filepath.Dir(clean), filepath.Base(clean)
	if l.sys.opened != nil {
		l.sys.opened(dir)
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return parentDir{}, &iofs.PathError{Op: op, Path: dir, Err: err}
	}
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err == nil {
		err = l.guard.Check(real)
	}
	if err != nil {
		return parentDir{}, errors.Join(fmt.Errorf("%s: %s: %w", op, dir, err), unix.Close(fd))
	}
	return parentDir{fd: fd, name: name}, nil
}

func (p parentDir) close() error { return unix.Close(p.fd) }

// Symlink implements ports.LinkOps: the link is created under a temporary
// name in the same folder and published under its final name without ever
// replacing an existing entry.
func (l *LinkOps) Symlink(target, path string) (err error) {
	if target == "" {
		return fmt.Errorf("symlink: %w: empty link content", paths.ErrInvalidPath)
	}
	if err := l.check("symlink", path); err != nil {
		return err
	}
	// The content must not lead into a forbidden prefix either.
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(filepath.Clean(path)), target)
	}
	if err := l.guard.Check(resolved); err != nil {
		return fmt.Errorf("symlink content %q: %w", target, err)
	}
	dir, err := l.openParent("symlink", path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.close()) }()

	tmp := ".skillhub-link-" + rand.Text()
	if err := unix.Symlinkat(target, dir.fd, tmp); err != nil {
		return &iofs.PathError{Op: "symlink", Path: path, Err: err}
	}
	if err := l.publish(dir.fd, tmp, dir.fd, dir.name, false); err != nil {
		return errors.Join(&iofs.PathError{Op: "symlink", Path: path, Err: err}, ignoreMissing(unix.Unlinkat(dir.fd, tmp, 0)))
	}
	return l.sync("symlink", path, dir.fd)
}

// publish renames oldName to newName with RENAME_NOREPLACE. Where the kernel
// or file system lacks it, a non-folder is published with link(2), which
// never replaces either, and the old name is then removed.
func (l *LinkOps) publish(oldFD int, oldName string, newFD int, newName string, isDir bool) error {
	err := l.sys.renameat2(oldFD, oldName, newFD, newName, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	if isDir {
		return fmt.Errorf("%w: %w", errNoReplaceUnsupported, err)
	}
	// Without AT_SYMLINK_FOLLOW, linkat links a symlink itself.
	if err := l.sys.linkat(oldFD, oldName, newFD, newName, 0); err != nil {
		return err
	}
	return unix.Unlinkat(oldFD, oldName, 0)
}

func (l *LinkOps) sync(op, path string, fd int) error {
	if err := l.sys.fsync(fd); err != nil {
		return &iofs.PathError{Op: op + " fsync", Path: filepath.Dir(path), Err: err}
	}
	return nil
}

// Readlink implements ports.LinkOps.
func (l *LinkOps) Readlink(path string) (string, error) {
	if err := l.check("readlink", path); err != nil {
		return "", err
	}
	return os.Readlink(path)
}

// RenameNoReplace implements ports.LinkOps. It never copies: a move across
// file systems fails with ports.ErrCrossDevice.
func (l *LinkOps) RenameNoReplace(oldPath, newPath string) (err error) {
	if err := l.check("rename", oldPath, newPath); err != nil {
		return err
	}
	info, err := os.Lstat(oldPath)
	if err != nil {
		return err
	}
	from, err := l.openParent("rename", oldPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, from.close()) }()
	to, err := l.openParent("rename", newPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, to.close()) }()

	if err := l.publish(from.fd, from.name, to.fd, to.name, info.IsDir()); err != nil {
		if errors.Is(err, unix.EXDEV) {
			return fmt.Errorf("rename %s to %s: %w", oldPath, newPath, ports.ErrCrossDevice)
		}
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	return errors.Join(l.sync("rename", oldPath, from.fd), l.sync("rename", newPath, to.fd))
}

// RemoveLink implements ports.LinkOps. It reads the entry itself, never what
// it points to, and removes it only when it is a symlink whose content is
// exactly target.
func (l *LinkOps) RemoveLink(path, target string) (err error) {
	if err := l.check("remove link", path); err != nil {
		return err
	}
	dir, err := l.openParent("remove link", path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.close()) }()

	var st unix.Stat_t
	if err := unix.Fstatat(dir.fd, dir.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("remove link %s: %w: %w", path, ports.ErrLinkMismatch, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFLNK {
		return fmt.Errorf("remove link %s: %w: not a symlink", path, ports.ErrLinkMismatch)
	}
	got, err := readlinkat(dir.fd, dir.name)
	if err != nil {
		return &iofs.PathError{Op: "remove link", Path: path, Err: err}
	}
	if got != target {
		return fmt.Errorf("remove link %s: %w: content is %q, want %q", path, ports.ErrLinkMismatch, got, target)
	}
	if err := unix.Unlinkat(dir.fd, dir.name, 0); err != nil {
		return &iofs.PathError{Op: "remove link", Path: path, Err: err}
	}
	return l.sync("remove link", path, dir.fd)
}

// readlinkat returns the content of the symlink name inside dirFD.
func readlinkat(dirFD int, name string) (string, error) {
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(dirFD, name, buf)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
}

// SameDevice implements ports.LinkOps. It compares the file systems that hold
// the two entries, without following a final symlink.
func (l *LinkOps) SameDevice(a, b string) (bool, error) {
	if err := l.check("same device", a, b); err != nil {
		return false, err
	}
	devA, err := device(a)
	if err != nil {
		return false, err
	}
	devB, err := device(b)
	if err != nil {
		return false, err
	}
	return devA == devB, nil
}

func device(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, &iofs.PathError{Op: "same device", Path: path, Err: errors.New("no device number")}
	}
	return st.Dev, nil
}

// ignoreMissing drops ENOENT, for cleanups of entries that may be gone.
func ignoreMissing(err error) error {
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
