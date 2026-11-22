package fakes

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

var _ ports.LinkOps = (*LinkOps)(nil)

// LinkOps is a ports.LinkOps over a real temporary directory. Unlike the
// other doubles it changes the disk, because the engine built on it must see
// real links; it refuses every path outside root as well as every path its
// guard forbids. It checks the destination and then acts, under a mutex, so
// it never replaces anything another goroutine of the same double created;
// it does not defend against other processes or sync to disk.
type LinkOps struct {
	Faults

	// CrossDevice, when set, decides which pairs of paths count as being on
	// different file systems: RenameNoReplace then fails with
	// ports.ErrCrossDevice and SameDevice reports false.
	CrossDevice func(a, b string) bool

	root  string
	guard paths.Guard
	mu    sync.Mutex
}

// NewLinkOps builds a LinkOps confined to the existing directory root, which
// a test takes from t.TempDir(), that also refuses what guard forbids.
func NewLinkOps(root string, guard paths.Guard) *LinkOps {
	return &LinkOps{root: filepath.Clean(root), guard: guard}
}

// check records the call, asks for an injected failure and confines every
// path and its parent.
func (l *LinkOps) check(op string, args []string, pathList ...string) error {
	if err := l.enter(op, args...); err != nil {
		return err
	}
	for _, p := range pathList {
		if err := l.guard.Check(p); err != nil {
			return fmt.Errorf("%s %s: %w", op, p, err)
		}
		clean := filepath.Clean(p)
		if rel, err := filepath.Rel(l.root, clean); err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("%s %s: %w: outside the fake root %s", op, p, paths.ErrForbidden, l.root)
		}
		if err := l.guard.Check(filepath.Dir(clean)); err != nil {
			return fmt.Errorf("%s %s: %w", op, p, err)
		}
	}
	return nil
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, iofs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// Symlink implements ports.LinkOps.
func (l *LinkOps) Symlink(target, path string) error {
	if err := l.check("Symlink", []string{target, path}, path); err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("symlink %s: %w: empty link content", path, paths.ErrInvalidPath)
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(filepath.Clean(path)), target)
	}
	if err := l.guard.Check(resolved); err != nil {
		return fmt.Errorf("symlink content %q: %w", target, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// os.Symlink itself fails with EEXIST and never replaces.
	return os.Symlink(target, path)
}

// Readlink implements ports.LinkOps.
func (l *LinkOps) Readlink(path string) (string, error) {
	if err := l.check("Readlink", []string{path}, path); err != nil {
		return "", err
	}
	return os.Readlink(path)
}

// RenameNoReplace implements ports.LinkOps.
func (l *LinkOps) RenameNoReplace(oldPath, newPath string) error {
	if err := l.check("RenameNoReplace", []string{oldPath, newPath}, oldPath, newPath); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := os.Lstat(oldPath); err != nil {
		return err
	}
	if l.CrossDevice != nil && l.CrossDevice(oldPath, newPath) {
		return fmt.Errorf("rename %s to %s: %w", oldPath, newPath, ports.ErrCrossDevice)
	}
	taken, err := exists(newPath)
	if err != nil {
		return err
	}
	if taken {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: iofs.ErrExist}
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("rename %s to %s: %w", oldPath, newPath, ports.ErrCrossDevice)
		}
		return err
	}
	return nil
}

// RemoveLink implements ports.LinkOps.
func (l *LinkOps) RemoveLink(path, target string) error {
	if err := l.check("RemoveLink", []string{path, target}, path); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("remove link %s: %w: %w", path, ports.ErrLinkMismatch, err)
	}
	if info.Mode()&iofs.ModeSymlink == 0 {
		return fmt.Errorf("remove link %s: %w: not a symlink", path, ports.ErrLinkMismatch)
	}
	got, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if got != target {
		return fmt.Errorf("remove link %s: %w: content is %q, want %q", path, ports.ErrLinkMismatch, got, target)
	}
	return os.Remove(path)
}

// SameDevice implements ports.LinkOps. Without CrossDevice it compares the
// real device numbers.
func (l *LinkOps) SameDevice(a, b string) (bool, error) {
	if err := l.check("SameDevice", []string{a, b}, a, b); err != nil {
		return false, err
	}
	devA, err := deviceOf(a)
	if err != nil {
		return false, err
	}
	devB, err := deviceOf(b)
	if err != nil {
		return false, err
	}
	if l.CrossDevice != nil && l.CrossDevice(a, b) {
		return false, nil
	}
	return devA == devB, nil
}

func deviceOf(path string) (uint64, error) {
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
