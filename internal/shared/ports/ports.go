// Package ports defines every port of skillhub (docs/design.md section 5):
// everything outside the process goes through one of these interfaces. The
// adapters under internal/adapters implement them, and
// internal/testsupport/fakes holds an in-memory double of each.
//
// Paths passed to a port are absolute. Every adapter that touches the file
// system or starts a process rejects a path under a forbidden prefix with an
// error matching paths.ErrForbidden.
package ports

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
)

// ---------------------------------------------------------------------------
// SkillDirScanner

// EntryKind is the kind of an entry of a skills directory.
type EntryKind string

const (
	EntryDir        EntryKind = "dir"
	EntrySymlink    EntryKind = "symlink"
	EntryBrokenLink EntryKind = "broken_link"
	EntryFile       EntryKind = "file"
	// EntryOther is anything else: a socket, a device, a named pipe.
	EntryOther EntryKind = "other"
)

// DirEntry is one entry of a skills directory.
type DirEntry struct {
	Name string
	// Path is the entry's absolute path, as listed.
	Path string
	Kind EntryKind
	// ResolvedPath is Path with every symlink resolved; empty for a broken
	// link.
	ResolvedPath string
	// HasSkillMD is true when the resolved entry is a directory holding a
	// SKILL.md file.
	HasSkillMD bool
	// HasGit is true when the resolved entry is a directory holding .git.
	HasGit bool
}

// SkillDirScanner lists the entries of a skills directory.
type SkillDirScanner interface {
	// Scan lists the direct entries of dir, sorted by name. A missing dir
	// returns an error matching fs.ErrNotExist.
	Scan(dir string) ([]DirEntry, error)
}

// ---------------------------------------------------------------------------
// FileOps

// FileOps is every file operation on skill directories, the store, staging
// and snapshots. Nothing else writes there.
type FileOps interface {
	// ReadFile returns the content of a file.
	ReadFile(path string) ([]byte, error)
	// WriteFileAtomic writes data to a temporary file in the same directory
	// and renames it over path, creating missing parents.
	WriteFileAtomic(path string, data []byte, perm fs.FileMode) error
	// Rename renames oldPath to newPath atomically; both are on the same
	// file system.
	Rename(oldPath, newPath string) error
	// CopyTree copies the directory src to dst, which must not exist,
	// keeping file contents, mode bits and symlinks as links.
	CopyTree(src, dst string) error
	// RemoveTree removes path and everything below it. A missing path is
	// not an error.
	RemoveTree(path string) error
	// MkdirAll creates a directory and its missing parents.
	MkdirAll(path string, perm fs.FileMode) error
	// Mode returns the mode bits of path, without following a final
	// symlink.
	Mode(path string) (fs.FileMode, error)
	// Chmod sets the permission bits of path.
	Chmod(path string, perm fs.FileMode) error
	// Exists reports whether path exists, without following a final
	// symlink.
	Exists(path string) (bool, error)
}

// ---------------------------------------------------------------------------
// SkillsLockStore

var (
	// ErrUnsupportedLockVersion reports a lock whose version is not exactly
	// 3. The store refuses to write it (skills-cli-internals.md,
	// consequences 2).
	ErrUnsupportedLockVersion = errors.New("unsupported skills lock version")
	// ErrLockEntryNotFound reports a lock entry that does not exist.
	ErrLockEntryNotFound = errors.New("skills lock entry not found")
	// ErrLockChanged reports a lock that kept changing under the store while
	// it wrote, after one retry.
	ErrLockChanged = errors.New("skills lock changed while writing")
)

// LockEntryUpdate is what an apply writes into a lock entry.
type LockEntryUpdate struct {
	// FolderHash becomes skillFolderHash.
	FolderHash string
	// UpdatedAt becomes updatedAt.
	UpdatedAt time.Time
	// Commit is the applied commit. It is written only when the entry
	// already has a commit key; an entry without one never receives it.
	Commit string
}

// SkillsLockStore reads and writes the global lock of npx skills under its
// compatibility rules: version 3 only, ref never added, changed or removed,
// unknown keys and key order kept, installedAt kept.
type SkillsLockStore interface {
	// Read reads the lock. A missing file is an empty lock of version 3
	// with no entries.
	Read() (domain.SkillsLock, error)
	// UpdateEntry rewrites skillFolderHash, updatedAt and, when the entry
	// already has one, commit of the entry name; nothing else changes.
	UpdateEntry(name string, update LockEntryUpdate) error
	// RestoreEntry puts back the exact JSON of an entry saved in a snapshot
	// (domain.Snapshot.LockEntryBefore), so the entry is byte-identical to
	// what it was.
	RestoreEntry(name string, raw []byte) error
}

// ---------------------------------------------------------------------------
// GitHubPort

var (
	// ErrRepoNotFound reports a repository that does not exist or is not
	// visible.
	ErrRepoNotFound = errors.New("github repository not found")
	// ErrRefNotFound reports a branch, tag, commit or path that does not
	// exist in a repository.
	ErrRefNotFound = errors.New("github ref or path not found")
	// ErrRateLimited reports an exhausted GitHub rate limit.
	ErrRateLimited = errors.New("github rate limit exceeded")
	// ErrNotAuthenticated reports gh without a logged-in session.
	ErrNotAuthenticated = errors.New("gh is not authenticated")
)

// GitHubPort reads repositories through gh api, with conditional requests by
// ETag. repo is owner/name.
type GitHubPort interface {
	// DefaultBranch returns the repository's default branch.
	DefaultBranch(ctx context.Context, repo string) (string, error)
	// ResolveCommit returns the commit SHA a branch, a tag or a commit SHA
	// points at.
	ResolveCommit(ctx context.Context, repo, ref string) (string, error)
	// Tree returns the tree at path in commit, recursively, with every node
	// path relative to path; path "" is the root. A truncated listing is
	// completed tree by tree, so the result is always whole.
	Tree(ctx context.Context, repo, commit, path string) (treehash.Tree, error)
	// TreeBySHA returns the tree whose SHA is treeSHA, recursively, with
	// every node path relative to it, completed tree by tree when the
	// listing is truncated. treeSHA must be 40 hex characters. A tree the
	// repository no longer holds is ErrRefNotFound.
	TreeBySHA(ctx context.Context, repo, treeSHA string) (treehash.Tree, error)
	// Blob returns the content of a blob.
	Blob(ctx context.Context, repo, sha string) ([]byte, error)
}

// ---------------------------------------------------------------------------
// GitPort

// CloneStatus describes a git clone.
type CloneStatus struct {
	// Remote is the URL of origin; empty when there is none.
	Remote string
	// Branch is the checked-out branch; empty when HEAD is detached.
	Branch string
	// Upstream is the branch's remote-tracking branch, such as
	// origin/main; empty when it tracks nothing.
	Upstream string
	// Head is the commit SHA of HEAD.
	Head string
	// Clean is true when git status --porcelain prints nothing.
	Clean bool
}

// GitPort works inside a clone. No operation discards content, and every
// git command runs with hooks disabled.
type GitPort interface {
	// Describe reports the clone's remote, branch, upstream, head and
	// whether it is clean.
	Describe(ctx context.Context, root string) (CloneStatus, error)
	// Fetch fetches one commit from origin.
	Fetch(ctx context.Context, root, commit string) error
	// IsAncestor reports whether ancestor is an ancestor of descendant, or
	// the same commit.
	IsAncestor(ctx context.Context, root, ancestor, descendant string) (bool, error)
	// FastForward moves the checked-out branch to commit with
	// --ff-only --no-overwrite-ignore; it fails rather than overwrite
	// anything.
	FastForward(ctx context.Context, root, commit string) error
	// TreeSHA returns the tree SHA of commit.
	TreeSHA(ctx context.Context, root, commit string) (string, error)
}

// ---------------------------------------------------------------------------
// StateStore

// ErrStateLocked reports that another skillhub process holds the state
// lock and did not release it within the wait.
var ErrStateLocked = errors.New("another skillhub process holds the state lock")

// StateStore keeps skillhub's own state: state.json, the journal and the
// advisory file lock shared by every skillhub process.
type StateStore interface {
	// Lock takes the state lock, waiting briefly for another holder, and
	// returns the function that releases it. It returns an error matching
	// ErrStateLocked when the lock stays held.
	Lock(ctx context.Context) (release func() error, err error)
	// Load reads the candidates, skips and check moments. A missing file is
	// an empty state.
	Load() (domain.PersistedState, error)
	// Save writes the candidates, skips and check moments, keeping the
	// operation in progress as it is.
	Save(state domain.PersistedState) error
	// Operation returns the operation in progress and whether there is one.
	Operation() (domain.Operation, bool, error)
	// RecordOperation persists op as the operation in progress before the
	// first change of an apply or an undo.
	RecordOperation(op domain.Operation) error
	// ClearOperation removes the operation in progress.
	ClearOperation() error
	// AppendJournal appends one line to the journal; lines are never
	// rewritten.
	AppendJournal(entry domain.HistoryEntry) error
	// Journal reads the journal, oldest first.
	Journal() ([]domain.HistoryEntry, error)
}

// ---------------------------------------------------------------------------
// SettingsStore

// SettingsStore loads and saves settings.toml.
type SettingsStore interface {
	// Load reads the settings; a missing file gives domain.DefaultSettings.
	Load() (domain.Settings, error)
	// Save validates and writes the settings; invalid settings are refused
	// with the error of domain.Settings.Validate.
	Save(settings domain.Settings) error
}

// ---------------------------------------------------------------------------
// Notifier

// Notifier sends a desktop notification.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// ---------------------------------------------------------------------------
// BackgroundService

// BackgroundStatus is the state of the background check's user timer.
type BackgroundStatus struct {
	// Installed is true when both units exist.
	Installed bool
	// Enabled is true when the timer starts with the session.
	Enabled bool
	// Active is true when the timer is running now.
	Active bool
}

// BackgroundService manages the systemd user timer of the background check.
// Enabling it is a user action; no test or other command calls Enable on
// the real machine.
type BackgroundService interface {
	Status(ctx context.Context) (BackgroundStatus, error)
	// Enable writes the service and timer units pointing at binary, reloads
	// the user manager and enables the timer.
	Enable(ctx context.Context, binary string) error
	// Disable disables the timer and removes both units.
	Disable(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// ProcessRunner

// ErrInvalidCommand reports a command a ProcessRunner refuses to start: a
// name that is not a bare command name, or an environment entry that sets
// PATH.
var ErrInvalidCommand = errors.New("invalid command")

// Command is one process to run.
type Command struct {
	// Name is a bare command name, looked up on PATH.
	Name string
	Args []string
	// Dir is the working directory: an absolute path, or empty for the
	// process's current directory. Either way it must not be under a
	// forbidden prefix.
	Dir string
	// Env holds KEY=VALUE entries added to the inherited environment; an
	// entry here overrides an inherited one with the same key. PATH is
	// refused: the process and its children always see the inherited PATH.
	Env   []string
	Stdin []byte
}

// Result is what a finished process produced.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ProcessRunner runs a command and waits for it. A process that exited on
// its own, with any exit code, is not an error, even when ctx ended right
// after; its code is in Result.ExitCode. Run returns an error when the
// command is refused, when it cannot start, or when ctx ended before the
// process finished, in which case the process is killed and the error wraps
// ctx.Err().
type ProcessRunner interface {
	Run(ctx context.Context, cmd Command) (Result, error)
}

// ---------------------------------------------------------------------------
// Clock

// Clock reads the wall clock.
type Clock interface {
	Now() time.Time
}
