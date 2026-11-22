// Package updates is the update flow of the skills recorded in the global
// lock of npx skills and of the git clones in the skills directories
// (docs/design.md section 7): detect, stage, label, enqueue, revalidate,
// snapshot, apply, verify, undo, recover and skip.
//
// A clone is updated only by a fast-forward of a clean clone to the exact
// commit the user approved: git fetch origin <commit>, then git merge
// --ff-only --no-overwrite-ignore. A dirty clone, one with local commits or
// one whose history diverged is blocked; a clone whose HEAD is not on a
// branch that tracks origin is not checked. Its snapshot is a full copy of
// the clone, .git included, and undo restores that copy without running git.
//
// Safety rules this package keeps:
//
//   - Nothing is applied without a user action. Check only detects, stages
//     and labels: it never touches a skills folder or the lock, and it never
//     recovers an interrupted operation. Only Apply, ApplyAll, Undo and
//     Recover change a skills folder or the lock.
//   - An approval names an exact candidate, by id and to_sha, and is
//     revalidated under the state lock before anything changes.
//   - The operation record is written before the first change. While it
//     exists, Apply, Skip and Undo refuse; only Recover resolves it.
//   - While a version 2 operation record exists (engine.RecordFile), every
//     flow that writes under the state lock (Check, Apply, ApplyAll, Skip,
//     Undo and Recover) refuses with ErrV2OperationPending before writing.
//   - Every write goes through the shared ports: folders through
//     ports.FileOps, the lock through ports.SkillsLockStore, skillhub's state
//     through ports.StateStore, GitHub through ports.GitHubPort, clones
//     through ports.GitPort. Tree hashes
//     come from internal/shared/treehash and labels from internal/updates/risk.
package updates

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

var (
	// ErrMissingDependency reports Deps without a port the flow needs.
	ErrMissingDependency = errors.New("updates: missing dependency")

	// ErrInterrupted reports an apply or an undo that did not finish. Apply,
	// Skip and Undo refuse until the user runs Recover.
	ErrInterrupted = errors.New("an interrupted operation must be recovered first")
	// ErrV2OperationPending reports a version 2 operation record
	// (engine.RecordFile): every flow that writes under the state lock
	// refuses, without writing anything, until recover resolves it
	// (docs/design-v2.md section 15, point 5).
	ErrV2OperationPending = errors.New("a version 2 operation must be recovered first")
	// ErrNoOperation reports a Recover with no interrupted operation.
	ErrNoOperation = errors.New("there is no interrupted operation")

	// ErrRejected is wrapped by every refusal of the revalidation: the user
	// would be approving a diff that is no longer true, so nothing changed and
	// a new approval is needed.
	ErrRejected = errors.New("approval rejected")
	// ErrCandidateNotFound reports an approval naming an unknown candidate.
	ErrCandidateNotFound = errors.New("candidate not found")
	// ErrNotPending reports an approval of a candidate that is no longer
	// pending: superseded by a newer check, applied, skipped or blocked.
	ErrNotPending = errors.New("candidate is not pending")
	// ErrApprovalMismatch reports an approval whose to_sha is not the
	// candidate's.
	ErrApprovalMismatch = errors.New("approved to_sha is not the candidate's")
	// ErrInstalledChanged reports an installed folder that changed after
	// staging.
	ErrInstalledChanged = errors.New("installed folder changed after staging")
	// ErrStagedChanged reports a staged folder that changed or is gone.
	ErrStagedChanged = errors.New("staged folder changed after staging")
	// ErrLockEntryChanged reports a lock entry that another tool changed or
	// removed after staging, or while skillhub was about to write it.
	ErrLockEntryChanged = errors.New("skills lock entry changed")
	// ErrNotUpdatable reports a target whose folder skillhub may not
	// replace: missing, disabled, a link resolving outside the store, a git
	// clone named by the lock, or a clone root that is not a real folder
	// holding a .git directory inside the home.
	ErrNotUpdatable = errors.New("target folder cannot be updated")
	// ErrUntrackedBranch reports a clone whose HEAD is not on a branch that
	// tracks origin; it is not checked (docs/design.md section 7).
	ErrUntrackedBranch = errors.New("the clone is not on a branch that tracks origin")
	// ErrGitAttributes reports a clone whose git attributes would make git
	// write files other than the reviewed ones: a filter, an end-of-line or
	// encoding conversion, or ident.
	ErrGitAttributes = errors.New("the clone's git attributes would transform the checked-out files")
	// ErrNotFastForward reports a clone that cannot move to the approved
	// commit with a fast-forward: its HEAD is not an ancestor of it.
	ErrNotFastForward = errors.New("the clone cannot fast-forward to the approved commit")
	// ErrLocallyModifiedExcluded reports a locally modified candidate in
	// ApplyAll, which applies those one by one only.
	ErrLocallyModifiedExcluded = errors.New("locally modified candidates are applied one by one")

	// ErrLockConcurrent reports a skills lock that kept changing right before
	// the replacement, after one retry: npx skills is writing it.
	ErrLockConcurrent = errors.New("skills lock kept changing while applying")
	// ErrVerifyFailed reports an applied folder or lock entry that does not
	// match the candidate.
	ErrVerifyFailed = errors.New("verification after the change failed")

	// ErrNothingToUndo reports an undo with no applied update to undo.
	ErrNothingToUndo = errors.New("there is no applied update to undo")
	// ErrSnapshotNotFound reports a snapshot that does not exist.
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrUndoNeedsConfirmation reports an undo whose folder changed after
	// the apply. A copy of the current folder was saved first, with keep set;
	// the undo runs only with Confirm.
	ErrUndoNeedsConfirmation = errors.New("folder changed after the apply; undo needs a separate confirmation")
)

// supportedLockVersion is the only lock version skillhub writes
// (skills-cli-internals.md, consequences 2).
const supportedLockVersion = 3

// rejected wraps cause as a revalidation refusal.
func rejected(cause error, format string, args ...any) error {
	return fmt.Errorf("%w: %w: %s", ErrRejected, cause, fmt.Sprintf(format, args...))
}

// Deps are the ports the flow goes through.
type Deps struct {
	Paths paths.Paths
	// Files is every file operation on the store, staging and snapshots.
	Files ports.FileOps
	// Dirs lists the store and the snapshots directory.
	Dirs   ports.SkillDirScanner
	Lock   ports.SkillsLockStore
	State  ports.StateStore
	GitHub ports.GitHubPort
	Clock  ports.Clock
	// Git works inside the clones; required with Clones.
	Git ports.GitPort
	// Clones lists the git clones to check, such as the inventory finds them;
	// nil checks no clone.
	Clones func(ctx context.Context) ([]Clone, error)
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
}

// Clone is a git clone found in a skills directory.
type Clone struct {
	// Root is the absolute, resolved path of the clone's working tree. Its
	// target id is domain.GitID of Root relative to the home.
	Root string
	// Skills are the ids of the skills inside the clone; an update of the
	// clone changes all of them.
	Skills []string
}

func (d Deps) validate() error {
	var missing []string
	if d.Paths.Home() == "" {
		missing = append(missing, "Paths")
	}
	if d.Clones != nil && d.Git == nil {
		missing = append(missing, "Git")
	}
	for name, port := range map[string]any{
		"Files": d.Files, "Dirs": d.Dirs, "Lock": d.Lock, "State": d.State, "GitHub": d.GitHub, "Clock": d.Clock,
	} {
		if port == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingDependency, strings.Join(missing, ", "))
	}
	return nil
}

// Service runs the update flow.
type Service struct {
	d Deps
}

// New builds a Service.
func New(d Deps) (*Service, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	if d.HashDir == nil {
		d.HashDir = treehash.Dir
	}
	return &Service{d: d}, nil
}

// Approval names one candidate exactly as it was shown to the user.
type Approval struct {
	CandidateID string
	ToSHA       string
}

// Outcome is what happened to one target of an apply.
type Outcome string

const (
	// OutcomeApplied: the candidate is installed and verified.
	OutcomeApplied Outcome = "applied"
	// OutcomeRejected: the revalidation refused; nothing changed and the
	// candidate must be reviewed again.
	OutcomeRejected Outcome = "rejected"
	// OutcomeFailed: the apply failed and was reverted from the snapshot, or
	// it left an operation to recover (see ApplyResult.Err).
	OutcomeFailed Outcome = "failed"
	// OutcomeExcluded: ApplyAll left the candidate out; it is applied one by
	// one.
	OutcomeExcluded Outcome = "excluded"
)

// ApplyResult is the result of applying one candidate.
type ApplyResult struct {
	Target      string
	CandidateID string
	Outcome     Outcome
	// SnapshotID is the snapshot taken before the change, when the apply
	// succeeded.
	SnapshotID string
	// Err is nil only for OutcomeApplied.
	Err error
}

// lockName returns the lock entry name a Lock target id names.
func lockName(target string) (string, bool) {
	name, ok := strings.CutPrefix(target, domain.LockID(""))
	return name, ok && validName(name)
}

// validName accepts a lock entry name that is a single path element.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// GuardV2Operation returns ErrV2OperationPending while a version 2
// operation record exists. A caller holding the state lock calls it before
// writing anything.
func (s *Service) GuardV2Operation() error {
	pending, err := s.d.Files.Exists(engine.RecordFile(s.d.Paths))
	if err != nil {
		return fmt.Errorf("look for a version 2 operation: %w", err)
	}
	if pending {
		return ErrV2OperationPending
	}
	return nil
}
