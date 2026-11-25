package fs

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

var _ ports.FileOps = (*FileOps)(nil)

// FileOps is the real ports.FileOps. Every call checks each path it receives
// against the guard before touching the disk.
type FileOps struct {
	guard paths.Guard
	// fsync makes a file or a directory durable; tests replace it.
	fsync func(f *os.File) error
}

// NewFileOps builds a FileOps that refuses every path guard refuses. The
// composition root passes Paths.Guard().
func NewFileOps(guard paths.Guard) *FileOps {
	return &FileOps{guard: guard, fsync: (*os.File).Sync}
}

func (f *FileOps) check(op string, pathList ...string) error {
	for _, p := range pathList {
		if err := f.guard.Check(p); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
	}
	return nil
}

// ReadFile implements ports.FileOps.
func (f *FileOps) ReadFile(path string) ([]byte, error) {
	if err := f.check("read", path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// WriteFileAtomic implements ports.FileOps: the data goes to a temporary file
// in the target's directory, which is synced and then renamed over the
// target, so a reader sees either the old content or the new one; the
// directory is synced last, so the rename survives a crash.
func (f *FileOps) WriteFileAtomic(path string, data []byte, perm iofs.FileMode) error {
	if err := f.check("write", path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := f.writeAndClose(tmp, data, perm); err != nil {
		return errors.Join(err, os.Remove(tmpPath))
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return errors.Join(err, os.Remove(tmpPath))
	}
	return f.syncDir(dir)
}

func (f *FileOps) writeAndClose(file *os.File, data []byte, perm iofs.FileMode) error {
	_, err := file.Write(data)
	if err == nil {
		err = file.Chmod(perm.Perm())
	}
	if err == nil {
		err = f.fsync(file)
	}
	return errors.Join(err, file.Close())
}

// syncDir makes the entries of dir durable.
func (f *FileOps) syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s to sync it: %w", dir, err)
	}
	err = f.fsync(d)
	if err != nil {
		err = fmt.Errorf("sync %s: %w", dir, err)
	}
	return errors.Join(err, d.Close())
}

// Rename implements ports.FileOps.
func (f *FileOps) Rename(oldPath, newPath string) error {
	if err := f.check("rename", oldPath, newPath); err != nil {
		return err
	}
	return os.Rename(oldPath, newPath)
}

// CopyTree implements ports.FileOps. The copy is built in a temporary sibling
// of dst and renamed into place, so a failed copy leaves no dst behind. Every
// copied file and directory is synced before the rename, and dst's parent
// after it, so a complete copy survives a crash.
func (f *FileOps) CopyTree(src, dst string) error {
	if err := f.check("copy", src, dst); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &iofs.PathError{Op: "copy", Path: src, Err: errors.New("not a directory")}
	}
	if _, err := os.Lstat(dst); err == nil {
		return &iofs.PathError{Op: "copy", Path: dst, Err: iofs.ErrExist}
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dst)+".copy-*")
	if err != nil {
		return err
	}
	err = f.copyDirContent(src, tmp)
	if err == nil {
		err = f.syncDir(tmp)
	}
	if err == nil {
		err = os.Chmod(tmp, info.Mode().Perm())
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		return errors.Join(err, removeTreeWritable(tmp))
	}
	return f.syncDir(parent)
}

func removeTreeWritable(path string) error {
	err := os.RemoveAll(path)
	if !errors.Is(err, iofs.ErrPermission) {
		return err
	}
	_ = filepath.WalkDir(path, func(p string, d iofs.DirEntry, walkErr error) error {
		if walkErr == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// copyDirContent copies the entries of src into the existing directory dst.
func (f *FileOps) copyDirContent(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := f.copyEntry(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (f *FileOps) copyEntry(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch mode := info.Mode(); {
	case mode&iofs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		// A symlink cannot be synced; its directory's sync covers it.
		return os.Symlink(target, dst)
	case mode.IsDir():
		// The directory is writable while it fills and is synced, then gets
		// its own bits.
		if err := os.Mkdir(dst, 0o700); err != nil {
			return err
		}
		if err := f.copyDirContent(src, dst); err != nil {
			return err
		}
		if err := f.syncDir(dst); err != nil {
			return err
		}
		return os.Chmod(dst, mode.Perm())
	case mode.IsRegular():
		return f.copyFile(src, dst, mode.Perm())
	default:
		return &iofs.PathError{Op: "copy", Path: src, Err: fmt.Errorf("unsupported file type %s", mode.Type())}
	}
}

func (f *FileOps) copyFile(src, dst string, perm iofs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // read-only handle; a close error loses nothing.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		// OpenFile applied the umask; set the exact bits.
		err = out.Chmod(perm)
	}
	if err == nil {
		err = f.fsync(out)
	}
	return errors.Join(err, out.Close())
}

// RemoveTree implements ports.FileOps.
func (f *FileOps) RemoveTree(path string) error {
	if err := f.check("remove", path); err != nil {
		return err
	}
	if filepath.Clean(path) == string(filepath.Separator) {
		return &iofs.PathError{Op: "remove", Path: path, Err: errors.New("refusing to remove the root")}
	}
	return os.RemoveAll(path)
}

// MkdirAll implements ports.FileOps.
func (f *FileOps) MkdirAll(path string, perm iofs.FileMode) error {
	if err := f.check("mkdir", path); err != nil {
		return err
	}
	return os.MkdirAll(path, perm)
}

// Mode implements ports.FileOps; it does not follow a final symlink.
func (f *FileOps) Mode(path string) (iofs.FileMode, error) {
	if err := f.check("lstat", path); err != nil {
		return 0, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode(), nil
}

// Chmod implements ports.FileOps.
func (f *FileOps) Chmod(path string, perm iofs.FileMode) error {
	if err := f.check("chmod", path); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

// Exists implements ports.FileOps; it does not follow a final symlink.
func (f *FileOps) Exists(path string) (bool, error) {
	if err := f.check("lstat", path); err != nil {
		return false, err
	}
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

// ReadDir lists the direct entries of dir, sorted by name, without following
// a final symlink. It refuses forbidden paths and non-directories.
func (f *FileOps) ReadDir(dir string) ([]iofs.DirEntry, error) {
	if err := f.check("readdir", dir); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &iofs.PathError{Op: "readdir", Path: dir, Err: errors.New("not a directory")}
	}
	return os.ReadDir(dir)
}

// EntryMetadata inspects path without following a final symlink and reports
// its hard-link count and whether any extended attributes or POSIX ACLs are
// present. Inability to inspect any of these returns an error rather than
// treating it as absence.
func (f *FileOps) EntryMetadata(path string) (nlink uint64, hasXattrs, hasACLs bool, err error) {
	if err := f.check("metadata", path); err != nil {
		return 0, false, false, err
	}
	var st unix.Stat_t
	for {
		err = unix.Lstat(path, &st)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return 0, false, false, &iofs.PathError{Op: "lstat", Path: path, Err: err}
	}
	var sz int
	for {
		sz, err = unix.Llistxattr(path, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return 0, false, false, &iofs.PathError{Op: "llistxattr", Path: path, Err: err}
	}
	if sz > 0 {
		hasXattrs = true
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG, unix.S_IFDIR:
		hasACLs, err = hasPOSIXACL(path, "system.posix_acl_access")
		if err != nil {
			return 0, false, false, err
		}
		if !hasACLs && st.Mode&unix.S_IFMT == unix.S_IFDIR {
			hasACLs, err = hasPOSIXACL(path, "system.posix_acl_default")
			if err != nil {
				return 0, false, false, err
			}
		}
	}
	return uint64(st.Nlink), hasXattrs, hasACLs, nil
}

func hasPOSIXACL(path, attr string) (bool, error) {
	var err error
	for {
		_, err = unix.Lgetxattr(path, attr, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENODATA) {
		return false, nil
	}
	return false, &iofs.PathError{Op: "lgetxattr", Path: path, Err: err}
}
