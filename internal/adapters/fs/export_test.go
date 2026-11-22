package fs

import (
	"os"

	"skillhub/internal/shared/paths"
)

// LinkHooks replaces single kernel calls of a LinkOps in tests. A nil field
// keeps the real call.
type LinkHooks struct {
	Renameat2 func(oldDirFD int, oldName string, newDirFD int, newName string, flags uint) error
	Linkat    func(oldDirFD int, oldName string, newDirFD int, newName string, flags int) error
	Fsync     func(fd int) error
	// Opened runs after the guard accepted a path, before its parent opens.
	Opened func(dir string)
}

// NewLinkOpsWithHooks builds a LinkOps whose kernel calls go through hooks.
func NewLinkOpsWithHooks(guard paths.Guard, hooks LinkHooks) *LinkOps {
	l := NewLinkOps(guard)
	if hooks.Renameat2 != nil {
		l.sys.renameat2 = hooks.Renameat2
	}
	if hooks.Linkat != nil {
		l.sys.linkat = hooks.Linkat
	}
	if hooks.Fsync != nil {
		l.sys.fsync = hooks.Fsync
	}
	l.sys.opened = hooks.Opened
	return l
}

// NewFileOpsWithSync builds a FileOps whose every fsync goes through sync.
func NewFileOpsWithSync(guard paths.Guard, sync func(f *os.File) error) *FileOps {
	f := NewFileOps(guard)
	f.fsync = sync
	return f
}
