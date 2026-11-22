package ports

import "errors"

// Version 2 (docs/design-v2.md sections 4 and 11, item 5): the primitives
// that move skill folders and create or remove links. Every method is
// confined by the same forbidden-prefix guard as FileOps, never replaces an
// existing path and never follows a final symlink.

var (
	// ErrCrossDevice reports a move whose source and destination are on
	// different file systems. Version 2 never copies across devices.
	ErrCrossDevice = errors.New("source and destination are on different file systems")
	// ErrLinkMismatch reports a path that is not a symlink whose content is
	// exactly the expected target.
	ErrLinkMismatch = errors.New("path is not a symlink to the expected target")
)

// LinkOps moves folders and manages symlinks without ever replacing an
// existing path.
type LinkOps interface {
	// Symlink creates path as a symlink whose content is target, exactly as
	// given (relative or absolute). It fails with fs.ErrExist when path
	// exists and never replaces anything; a crash leaves either no link or
	// the complete link. It syncs the parent directory.
	Symlink(target, path string) error
	// Readlink returns the content of the symlink at path.
	Readlink(path string) (string, error)
	// RenameNoReplace renames oldPath to newPath. It fails with fs.ErrExist
	// when newPath exists, with ErrCrossDevice when the two are on different
	// file systems, and syncs both parent directories.
	RenameNoReplace(oldPath, newPath string) error
	// RemoveLink removes the symlink at path only when its content is
	// exactly target; otherwise it fails with ErrLinkMismatch and changes
	// nothing. It syncs the parent directory.
	RemoveLink(path, target string) error
	// SameDevice reports whether two existing paths are on the same file
	// system.
	SameDevice(a, b string) (bool, error)
}
