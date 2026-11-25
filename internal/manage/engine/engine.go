// Package engine is the version 2 operation engine (docs/design-v2.md
// sections 4, 11, 14 and 15). It owns the typed plan, the durable operation
// record and the multi-object snapshot. This first half (W2a) builds a plan,
// checks every step's preconditions against the disk, takes and verifies the
// snapshot and writes and reads the record, all under the same state lock as
// version 1. The second half (W2b) runs the steps, verifies the result,
// closes the operation, rolls it back on failure, and adds Recover and Undo.
//
// The engine declares the small interfaces it consumes; the existing adapters
// (fs.FileOps, fs.LinkOps, fs.Scanner, skillslock.Store, statefile.Store and
// clock.System) satisfy them unchanged.
package engine

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// Files is what the engine reads and writes on disk: the record, the
// snapshot and the observation of skill folders. Every method is confined by
// the forbidden-prefix guard of the adapter.
type Files interface {
	ReadFile(path string) ([]byte, error)
	WriteFileAtomic(path string, data []byte, perm fs.FileMode) error
	CopyTree(src, dst string) error
	RemoveTree(path string) error
	Mode(path string) (fs.FileMode, error)
	Exists(path string) (bool, error)
	// MkdirAll and Chmod serve only the engine's own roots under
	// ~/.agents/.skillhub: creating a missing level, and making a folder in
	// the trash deletable.
	MkdirAll(path string, perm fs.FileMode) error
	Chmod(path string, perm fs.FileMode) error
}

// DirReader is an optional capability on Files that lists direct directory
// entries sorted by name without following a final symlink. Whole-clone
// snapshot verification fails closed when Files does not implement it.
type DirReader interface {
	ReadDir(dir string) ([]fs.DirEntry, error)
}

// EntryMetadataReader is an optional capability on Files that inspects an
// entry's hard-link count, extended attributes, and ACLs without following a
// final symlink. Whole-clone snapshot verification fails closed when Files
// does not implement it.
type EntryMetadataReader interface {
	EntryMetadata(path string) (nlink uint64, hasXattrs, hasACLs bool, err error)
}

// Links reads, creates and removes symlinks and moves folders, never
// replacing an existing path (ports.LinkOps).
type Links interface {
	Readlink(path string) (string, error)
	Symlink(target, path string) error
	RenameNoReplace(oldPath, newPath string) error
	RemoveLink(path, target string) error
}

// Lock reads the npx skills global lock and moves whole entries in and out
// of it (ports.LockEntries), under the exception the user approved in L32.
type Lock interface {
	Read() (domain.SkillsLock, error)
	Hash() (string, error)
	Park(name string) (domain.ParkedEntry, error)
	Unpark(entry domain.ParkedEntry) error
	Add(name string, raw []byte) error
	Remove(name string, expectedRaw []byte) (domain.ParkedEntry, error)
	// RestoreEntry replaces entry name in its place with exactly raw, which
	// must carry the same ref as the entry now; nothing else in the lock
	// changes (ports.SkillsLockStore). A lockReplace step uses it.
	RestoreEntry(name string, raw []byte) error
}

// LockDropper deletes the lock file when it holds no entry and is exactly the
// empty lock Lock.Add creates when there is none, and fails with
// ports.ErrLockChanged otherwise. Rolling back or undoing an operation whose
// preimage is "the lock did not exist" needs it (L35).
type LockDropper interface {
	DropEmpty() error
}

// State is the part of the state store the engine needs: the shared state
// lock, the version 1 operation in progress, the journal and the persisted
// state the plan's effect changes.
type State interface {
	Lock(ctx context.Context) (release func() error, err error)
	Operation() (domain.Operation, bool, error)
	Journal() ([]domain.HistoryEntry, error)
	AppendJournal(entry domain.HistoryEntry) error
	Load() (domain.PersistedState, error)
	Save(state domain.PersistedState) error
}

// Dirs lists a directory.
type Dirs interface {
	Scan(dir string) ([]ports.DirEntry, error)
}

// ProjectFS inspects, copies, removes to trash, and restores from staging an
// exact project-scoped skill directory without following symlinks.
type ProjectFS interface {
	ObserveSkill(projectDir, root, name string) (exists bool, foreign bool, tree treehash.Tree, err error)
	SameDevice(projectDir, root, otherDir string) (bool, error)
	CopySkill(projectDir, root, name, dstDir string) (treehash.Tree, error)
	RemoveSkillToTrash(projectDir, root, name, trashDest string) error
	RestoreSkillFromStaging(stagingSrc, projectDir, root, name string) error
}

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// Deps are the engine's dependencies.
type Deps struct {
	Paths paths.Paths
	Files Files
	Links Links
	Lock  Lock
	// DropLock deletes an empty lock; nil makes every step that must leave
	// no lock fail with ErrLockDropUnsupported.
	DropLock LockDropper
	State    State
	Dirs     Dirs
	Clock    Clock
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
	// ProjectDir and ProjectFS enable project-scoped plans when ProjectFS is non-nil.
	ProjectDir string
	ProjectFS  ProjectFS
}

// Engine plans, records and snapshots version 2 operations.
type Engine struct {
	d Deps
}

// New builds an Engine.
func New(d Deps) *Engine {
	if d.HashDir == nil {
		d.HashDir = treehash.Dir
	}
	return &Engine{d: d}
}

var (
	// ErrInvalidStep reports a step whose arguments are not usable: a path
	// that is not absolute and clean, an empty name or target, or an entry
	// that is not the one named.
	ErrInvalidStep = errors.New("invalid plan step")
	// ErrPrecondition reports a step whose objects are not in the state the
	// step needs, or no longer in the state the plan saw.
	ErrPrecondition = errors.New("plan precondition does not hold")
	// ErrPlanChanged reports a plan whose identifier is not the hash of its
	// content, or a lock that changed since the plan was built.
	ErrPlanChanged = errors.New("plan changed since it was shown")
	// ErrUnsupportedTree reports a folder the snapshot cannot keep exactly: it
	// holds a special file, it is a clone or a symlink stands where a folder
	// was expected.
	ErrUnsupportedTree = errors.New("folder cannot be kept in a snapshot")
	// ErrSnapshotMismatch reports a snapshot copy that is not the folder it
	// copies: a different tree hash or different mode bits.
	ErrSnapshotMismatch = errors.New("snapshot copy does not match its folder")
	// ErrSnapshotNotFound reports a snapshot that does not exist or is
	// incomplete.
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrV1OperationPending reports a version 1 operation that was not
	// recovered; the engine refuses to start until it is.
	ErrV1OperationPending = errors.New("a version 1 operation is pending")
	// ErrOperationPending reports a version 2 operation record that was not
	// closed or recovered.
	ErrOperationPending = errors.New("a version 2 operation is pending")
	// ErrUnsupportedRecord reports an operation record or a snapshot whose
	// format or version this binary does not know, or whose content does
	// not match its own identifiers.
	ErrUnsupportedRecord = errors.New("unsupported operation record")
	// ErrProgress reports a progress update that is not the next step.
	ErrProgress = errors.New("operation progress out of order")
	// ErrForeignState reports an object that is neither as a step needs it
	// nor as the step leaves it: somebody else changed it. The engine stops
	// without touching it and keeps the record with a diagnostic.
	ErrForeignState = errors.New("foreign state: an object changed outside the operation")
	// ErrVerify reports an operation whose final state is not the one its
	// plan promised.
	ErrVerify = errors.New("the operation did not leave the expected state")
	// ErrChangedSince reports an operation that cannot be undone because
	// something it left changed afterwards.
	ErrChangedSince = errors.New("changed since the operation")
	// ErrNothingToUndo reports a snapshot that names no version 2 operation
	// that is still undoable.
	ErrNothingToUndo = errors.New("nothing to undo")
	// ErrLockDropUnsupported reports a step that must leave no lock file
	// while the engine has no LockDropper.
	ErrLockDropUnsupported = errors.New("cannot delete the skills lock")
)
