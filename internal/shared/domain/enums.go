package domain

import (
	"errors"
	"fmt"
	"slices"
)

// ErrUnknownValue reports a text that is not one of an enum's values. Every
// enum rejects it when decoding, so a corrupt state file is noticed instead
// of read as an empty value.
var ErrUnknownValue = errors.New("unknown value")

// parseEnum decodes text into one of values.
func parseEnum[T ~string](kind string, values []T, text []byte) (T, error) {
	v := T(text)
	if !slices.Contains(values, v) {
		var zero T
		return zero, fmt.Errorf("%w for %s: %q", ErrUnknownValue, kind, text)
	}
	return v, nil
}

// State is the single state of a skill row (docs/design.md section 4).
type State string

const (
	StateUpToDate        State = "up_to_date"
	StateUpdateAvailable State = "update_available"
	StateUnchecked       State = "unchecked"
	StateCheckFailed     State = "check_failed"
	StateNoOrigin        State = "no_origin"
	StateDisabled        State = "disabled"
	StateMissing         State = "missing"
)

// States lists every State in the design's order.
func States() []State {
	return []State{
		StateUpToDate, StateUpdateAvailable, StateUnchecked, StateCheckFailed,
		StateNoOrigin, StateDisabled, StateMissing,
	}
}

// UnmarshalText accepts only a known State.
func (s *State) UnmarshalText(text []byte) error {
	v, err := parseEnum("state", States(), text)
	*s = v
	return err
}

// Issue is a problem the inventory reports on a skill. Version 1 only
// reports issues; it never fixes them.
type Issue string

const (
	// IssueLocallyModified: the installed folder differs from what its origin
	// recorded, or a clone has local changes or commits.
	IssueLocallyModified Issue = "locally_modified"
	// IssueDuplicate: another real copy with the same content.
	IssueDuplicate Issue = "duplicate"
	// IssueDivergentCopy: another real copy with different content.
	IssueDivergentCopy Issue = "divergent_copy"
	IssueBrokenLink    Issue = "broken_link"
	// IssueUntrackedBranch: a clone whose HEAD is not on a branch that
	// tracks origin.
	IssueUntrackedBranch Issue = "untracked_branch"
	// IssueInterruptedUpdate: an apply or an undo that did not finish.
	IssueInterruptedUpdate Issue = "interrupted_update"
	// IssueRemovedUpstream: the skill's folder no longer exists upstream at
	// the checked commit. It is not a check failure; the skill cannot be
	// updated until its origin changes.
	IssueRemovedUpstream Issue = "removed_upstream"
)

// Issues lists every Issue in the design's order.
func Issues() []Issue {
	return []Issue{
		IssueLocallyModified, IssueDuplicate, IssueDivergentCopy, IssueBrokenLink,
		IssueUntrackedBranch, IssueInterruptedUpdate, IssueRemovedUpstream,
	}
}

// UnmarshalText accepts only a known Issue.
func (i *Issue) UnmarshalText(text []byte) error {
	v, err := parseEnum("issue", Issues(), text)
	*i = v
	return err
}

// Label is a risk label of an update candidate (docs/design.md section 7).
// Labels inform; they never decide.
type Label string

const (
	LabelNonTextFiles    Label = "non_text_files"
	LabelTouchesScripts  Label = "touches_scripts"
	LabelModeChange      Label = "mode_change"
	LabelNewPermissions  Label = "new_permissions"
	LabelSuspiciousText  Label = "suspicious_text"
	LabelLocallyModified Label = "locally_modified"
	LabelTextOnly        Label = "text_only"
)

// Labels lists every Label in the design's order.
func Labels() []Label {
	return []Label{
		LabelNonTextFiles, LabelTouchesScripts, LabelModeChange, LabelNewPermissions,
		LabelSuspiciousText, LabelLocallyModified, LabelTextOnly,
	}
}

// UnmarshalText accepts only a known Label.
func (l *Label) UnmarshalText(text []byte) error {
	v, err := parseEnum("label", Labels(), text)
	*l = v
	return err
}

// CandidateState is the state of an update candidate.
type CandidateState string

const (
	CandidatePending    CandidateState = "pending"
	CandidateApplied    CandidateState = "applied"
	CandidateSkipped    CandidateState = "skipped"
	CandidateSuperseded CandidateState = "superseded"
	CandidateBlocked    CandidateState = "blocked"
	CandidateFailed     CandidateState = "failed"
)

// CandidateStates lists every CandidateState in the design's order.
func CandidateStates() []CandidateState {
	return []CandidateState{
		CandidatePending, CandidateApplied, CandidateSkipped, CandidateSuperseded,
		CandidateBlocked, CandidateFailed,
	}
}

// UnmarshalText accepts only a known CandidateState.
func (c *CandidateState) UnmarshalText(text []byte) error {
	v, err := parseEnum("candidate state", CandidateStates(), text)
	*c = v
	return err
}

// OriginKind says where a skill comes from (docs/design.md section 2).
type OriginKind string

const (
	OriginLock     OriginKind = "lock"
	OriginGitClone OriginKind = "git_clone"
	OriginNone     OriginKind = "none"
)

// OriginKinds lists every OriginKind in the design's order.
func OriginKinds() []OriginKind { return []OriginKind{OriginLock, OriginGitClone, OriginNone} }

// UnmarshalText accepts only a known OriginKind.
func (o *OriginKind) UnmarshalText(text []byte) error {
	v, err := parseEnum("origin", OriginKinds(), text)
	*o = v
	return err
}

// LocationKind says what kind of directory entry a location is.
type LocationKind string

const (
	LocationReal       LocationKind = "real"
	LocationSymlink    LocationKind = "symlink"
	LocationBrokenLink LocationKind = "broken_link"
)

// LocationKinds lists every LocationKind in the design's order.
func LocationKinds() []LocationKind {
	return []LocationKind{LocationReal, LocationSymlink, LocationBrokenLink}
}

// UnmarshalText accepts only a known LocationKind.
func (l *LocationKind) UnmarshalText(text []byte) error {
	v, err := parseEnum("location kind", LocationKinds(), text)
	*l = v
	return err
}

// OperationKind is the kind of an operation in progress.
type OperationKind string

const (
	OperationApply OperationKind = "apply"
	OperationUndo  OperationKind = "undo"
)

// OperationKinds lists every OperationKind in the design's order.
func OperationKinds() []OperationKind { return []OperationKind{OperationApply, OperationUndo} }

// UnmarshalText accepts only a known OperationKind.
func (o *OperationKind) UnmarshalText(text []byte) error {
	v, err := parseEnum("operation kind", OperationKinds(), text)
	*o = v
	return err
}

// HistoryAction is what a journal line records.
type HistoryAction string

const (
	ActionApplied   HistoryAction = "applied"
	ActionUndone    HistoryAction = "undone"
	ActionSkipped   HistoryAction = "skipped"
	ActionFailed    HistoryAction = "failed"
	ActionRecovered HistoryAction = "recovered"
)

// HistoryActions lists every HistoryAction in the design's order.
func HistoryActions() []HistoryAction {
	return []HistoryAction{ActionApplied, ActionUndone, ActionSkipped, ActionFailed, ActionRecovered}
}

// UnmarshalText accepts only a known HistoryAction.
func (h *HistoryAction) UnmarshalText(text []byte) error {
	v, err := parseEnum("history action", HistoryActions(), text)
	*h = v
	return err
}

// SnapshotCause says which action took a snapshot: an apply, before it
// replaced the folder, or an undo, before it restored over a folder that
// changed after the apply (docs/design.md section 7).
type SnapshotCause string

const (
	SnapshotBeforeApply SnapshotCause = "apply"
	SnapshotBeforeUndo  SnapshotCause = "undo"
)

// SnapshotCauses lists every SnapshotCause.
func SnapshotCauses() []SnapshotCause {
	return []SnapshotCause{SnapshotBeforeApply, SnapshotBeforeUndo}
}

// UnmarshalText accepts only a known SnapshotCause.
func (s *SnapshotCause) UnmarshalText(text []byte) error {
	v, err := parseEnum("snapshot cause", SnapshotCauses(), text)
	*s = v
	return err
}

// BlockReason says why a candidate is blocked. Each value is a situation that
// docs/design.md section 7 says blocks a candidate.
type BlockReason string

const (
	// BlockDirtyWorktree: a clone with modified or untracked files.
	BlockDirtyWorktree BlockReason = "dirty_worktree"
	// BlockLocalCommits: a clone with commits that are not upstream, or a
	// divergent history.
	BlockLocalCommits BlockReason = "local_commits"
	// BlockHashMismatch: the staged folder's tree SHA is not to_sha.
	BlockHashMismatch BlockReason = "hash_mismatch"
	// BlockUnsupportedEntry: the upstream tree holds a symlink or a submodule.
	BlockUnsupportedEntry BlockReason = "unsupported_entry"
	// BlockLimitsExceeded: the upstream tree exceeds the path depth, file
	// count or total size limits of staging.
	BlockLimitsExceeded BlockReason = "limits_exceeded"
	// BlockGitAttributes: a clone update whose target commit holds a
	// .gitattributes that makes git transform files on checkout (filter,
	// eol, text, ident or working-tree-encoding), so the installed files
	// could differ from the reviewed ones or a filter program could run.
	BlockGitAttributes BlockReason = "git_attributes"
)

// BlockReasons lists every BlockReason.
func BlockReasons() []BlockReason {
	return []BlockReason{
		BlockDirtyWorktree, BlockLocalCommits, BlockHashMismatch, BlockUnsupportedEntry, BlockLimitsExceeded,
		BlockGitAttributes,
	}
}

// UnmarshalText accepts only a known BlockReason.
func (b *BlockReason) UnmarshalText(text []byte) error {
	v, err := parseEnum("block reason", BlockReasons(), text)
	*b = v
	return err
}

// FileChangeKind says how a file differs between two trees.
type FileChangeKind string

const (
	ChangeAdded    FileChangeKind = "added"
	ChangeModified FileChangeKind = "modified"
	ChangeRemoved  FileChangeKind = "removed"
)

// FileChangeKinds lists every FileChangeKind.
func FileChangeKinds() []FileChangeKind {
	return []FileChangeKind{ChangeAdded, ChangeModified, ChangeRemoved}
}

// UnmarshalText accepts only a known FileChangeKind.
func (f *FileChangeKind) UnmarshalText(text []byte) error {
	v, err := parseEnum("file change", FileChangeKinds(), text)
	*f = v
	return err
}
