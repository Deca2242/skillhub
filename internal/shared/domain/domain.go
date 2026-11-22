// Package domain holds the domain types of skillhub (docs/design.md
// section 4). It is pure data: no I/O, no terminal UI, no processes.
//
// JSON keys are the snake_case names of the design, because these records
// are what state.json, the journal and `--json` output carry. Every enum is a
// string type that rejects unknown values when decoded.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ID prefixes. A skill id is stable and is the only thing that identifies
// the target of a write; paths in ids are relative to the home and
// slash-separated.
const (
	lockIDPrefix = "lock:"
	gitIDPrefix  = "git:"
	dirIDPrefix  = "dir:"
)

// LockID is the id of the skill a lock entry names.
func LockID(lockName string) string { return lockIDPrefix + lockName }

// GitID is the id of a skill folder inside a git clone, and of a clone
// itself when given the clone root. homeRel is relative to the home.
func GitID(homeRel string) string { return gitIDPrefix + homeRel }

// DirID is the id of a skill folder without origin. homeRel is relative to
// the home.
func DirID(homeRel string) string { return dirIDPrefix + homeRel }

// StoreAgentID is the AgentID of a location in the universal store,
// ~/.agents/skills, which is not a harness.
const StoreAgentID = "store"

// Skill is one row of the inventory.
type Skill struct {
	ID string `json:"id"`
	// Name is the folder name, or the lock name for a lock entry.
	Name string `json:"name"`
	// Aliases are other names that resolve to the same folder, such as a
	// symlink in the store pointing at a sibling.
	Aliases     []string   `json:"aliases"`
	Description string     `json:"description"`
	Origin      Origin     `json:"origin"`
	Locations   []Location `json:"locations"`
	State       State      `json:"state"`
	Issues      []Issue    `json:"issues"`
}

// Origin is where a skill comes from. Exactly the field that matches Kind is
// set: Lock for OriginLock, GitClone for OriginGitClone, neither for
// OriginNone.
type Origin struct {
	Kind     OriginKind      `json:"kind"`
	Lock     *LockOrigin     `json:"lock,omitempty"`
	GitClone *GitCloneOrigin `json:"git_clone,omitempty"`
}

// LockOrigin is a skill recorded in the global lock of npx skills.
type LockOrigin struct {
	// Source is the repository, owner/repo.
	Source string `json:"source"`
	// SkillPath points at the SKILL.md file inside the repository; the
	// skill folder is its parent directory.
	SkillPath  string `json:"skill_path"`
	FolderHash string `json:"folder_hash"`
	// Ref is set only when the lock entry already carries one. skillhub
	// never adds, changes or removes it.
	Ref string `json:"ref,omitempty"`
}

// GitCloneOrigin is a skill inside a git clone with a GitHub origin.
type GitCloneOrigin struct {
	// Root is the absolute path of the clone's working tree.
	Root string `json:"root"`
	// Remote is the URL of the origin remote.
	Remote string `json:"remote"`
	// Branch is the checked-out branch; empty when HEAD is detached.
	Branch string `json:"branch"`
	// Upstream is the remote-tracking branch, such as origin/main; empty
	// when the branch tracks nothing.
	Upstream string `json:"upstream"`
	// Head is the commit SHA of HEAD.
	Head string `json:"head"`
}

// Location is one place where a skill is present.
type Location struct {
	Path string `json:"path"`
	// AgentID is the harness whose global directory holds Path, or
	// StoreAgentID.
	AgentID string       `json:"agent_id"`
	Kind    LocationKind `json:"kind"`
	// ContentTreeSHA is the git tree SHA of the content; empty for a broken
	// link.
	ContentTreeSHA string `json:"content_tree_sha"`
}

// AgentProfile is a harness of the agents table.
type AgentProfile struct {
	ID string `json:"id"`
	// GlobalDir is the harness's global skills directory, relative to the
	// home.
	GlobalDir string `json:"global_dir"`
	// ReadsUniversal is true when the harness reads ~/.agents/skills
	// directly.
	ReadsUniversal bool `json:"reads_universal"`
}

// UpdateCandidate is a detected change waiting for the user.
type UpdateCandidate struct {
	ID string `json:"id"`
	// Target is the id of a Lock skill, or GitID of a clone root.
	Target string `json:"target"`
	// Skills are the ids of the skills the candidate changes: one for a lock
	// entry, every skill of the clone for a clone.
	Skills  []string `json:"skills"`
	FromSHA string   `json:"from_sha"`
	// InstalledSHAAtStage is the installed folder's tree SHA when the
	// candidate was staged; an apply refuses when it no longer matches.
	InstalledSHAAtStage string           `json:"installed_sha_at_stage"`
	ToCommit            string           `json:"to_commit"`
	ToSHA               string           `json:"to_sha"`
	StagedPath          string           `json:"staged_path"`
	FileChanges         []FileChange     `json:"file_changes"`
	FrontmatterDelta    FrontmatterDelta `json:"frontmatter_delta"`
	Labels              []Label          `json:"labels"`
	RulesetVersion      int              `json:"ruleset_version"`
	FirstSeenAt         time.Time        `json:"first_seen_at"`
	State               CandidateState   `json:"state"`
	// BlockReason is set only when State is CandidateBlocked.
	BlockReason BlockReason `json:"block_reason,omitempty"`
}

// FileChange is one file that differs between the installed folder and the
// staged one. Modes are git modes ("100644", "100755", "120000"); a mode or
// SHA is empty on the side where the file does not exist.
type FileChange struct {
	// Path is slash-separated and relative to the skill folder.
	Path    string         `json:"path"`
	Change  FileChangeKind `json:"change"`
	OldMode string         `json:"old_mode,omitempty"`
	NewMode string         `json:"new_mode,omitempty"`
	OldSHA  string         `json:"old_sha,omitempty"`
	NewSHA  string         `json:"new_sha,omitempty"`
}

// FrontmatterDelta is how the SKILL.md frontmatter changes. Changed lists
// every key whose value was added, removed or changed, with nested keys
// joined by dots (metadata.author), sorted.
type FrontmatterDelta struct {
	Changed []string `json:"changed"`
}

// Skip records that the user skipped a target's update to ToSHA; the target
// shows no update until upstream publishes a different one.
type Skip struct {
	Target string    `json:"target"`
	ToSHA  string    `json:"to_sha"`
	At     time.Time `json:"at"`
}

// Snapshot is what an apply replaced, or the folder an undo was about to
// restore over.
type Snapshot struct {
	ID             string    `json:"id"`
	Target         string    `json:"target"`
	TakenAt        time.Time `json:"taken_at"`
	FolderCopyPath string    `json:"folder_copy_path"`
	// LockEntryBefore is the exact JSON of the lock entry before the change,
	// so restoring it is byte-identical; empty for a clone.
	LockEntryBefore json.RawMessage `json:"lock_entry_before,omitempty"`
	Cause           SnapshotCause   `json:"cause"`
	// Keep protects the snapshot from automatic pruning.
	Keep bool `json:"keep"`
}

// Operation is an apply or an undo in progress; there is at most one. It is
// written before the first change and cleared when the action finishes.
type Operation struct {
	ID          string        `json:"id"`
	Kind        OperationKind `json:"kind"`
	Target      string        `json:"target"`
	CandidateID string        `json:"candidate_id,omitempty"`
	// RestoreSnapshotID is the snapshot that brings the folder back to a
	// known state: the one taken before an apply, or the one an undo was
	// restoring.
	RestoreSnapshotID string    `json:"restore_snapshot_id"`
	StartedAt         time.Time `json:"started_at"`
}

// HistoryEntry is one line of the journal.
type HistoryEntry struct {
	At         time.Time     `json:"at"`
	Action     HistoryAction `json:"action"`
	Target     string        `json:"target"`
	FromSHA    string        `json:"from_sha"`
	ToSHA      string        `json:"to_sha"`
	SnapshotID string        `json:"snapshot_id"`
	Labels     []Label       `json:"labels"`
	Detail     string        `json:"detail"`
}

// CheckTimes are the moments of the last checks.
type CheckTimes struct {
	// LastRun is when the last check finished, successful or not.
	LastRun time.Time `json:"last_run"`
	// LastSuccess is when the last successful check finished; check
	// --if-due measures the interval from it.
	LastSuccess time.Time `json:"last_success"`
	// LastFailed is true when the last check failed.
	LastFailed bool `json:"last_failed"`
}

// NotifiedMarks remember what the background check already announced, so an
// unchanged pending list is not announced again (docs/design.md section 8).
type NotifiedMarks struct {
	// CandidateIDs are the candidates already announced.
	CandidateIDs []string `json:"candidate_ids"`
	// CheckFailure is true when a failing check was announced and no check
	// succeeded since.
	CheckFailure bool `json:"check_failure"`
	// OperationID is the interrupted operation already announced.
	OperationID string `json:"operation_id,omitempty"`
}

// PersistedState is skillhub's own persisted state, the content of
// state.json except the operation in progress, which has its own calls in
// StateStore.
type PersistedState struct {
	Candidates []UpdateCandidate `json:"candidates"`
	Skips      []Skip            `json:"skips"`
	Checks     CheckTimes        `json:"checks"`
	Notified   NotifiedMarks     `json:"notified"`
	// Results holds the outcome of the last check of each target, keyed by
	// skill id. A target missing from it has never been checked.
	Results map[string]TargetResult `json:"results,omitempty"`
	// Managed lists the skills version 2 manages and the harnesses each one
	// is meant to be linked in, keyed by store key (docs/design-v2.md
	// section 14).
	Managed map[string]ManagedSkill `json:"managed,omitempty"`
	// Parked lists the disabled skills, keyed by store key.
	Parked map[string]ParkedSkill `json:"parked,omitempty"`
	// Preferences holds the user's version 2 choices.
	Preferences Preferences `json:"preferences,omitzero"`
}

// ManagedSkill is a skill version 2 manages: its folder in the store and
// the harnesses it should be linked in.
type ManagedSkill struct {
	Key     string   `json:"key"`
	Targets []string `json:"targets"`
}

// LinkRecord is a symlink skillhub created or removed, with its exact
// content.
type LinkRecord struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// ParkedSkill is a disabled skill: where its folder was parked, the links
// it had and its lock entry, if it had one, so enabling it restores all of
// them exactly.
type ParkedSkill struct {
	Key        string       `json:"key"`
	Folder     string       `json:"folder"`
	Links      []LinkRecord `json:"links"`
	Entry      *ParkedEntry `json:"entry,omitempty"`
	At         time.Time    `json:"at"`
	SnapshotID string       `json:"snapshot_id,omitempty"`
}

// Preferences are the user's version 2 choices.
type Preferences struct {
	// Targets are the harness ids proposed as targets by default.
	Targets []string `json:"targets,omitempty"`
}

// TargetResult is the outcome of the last check of one target.
type TargetResult struct {
	// State is StateUpToDate, StateUpdateAvailable or StateCheckFailed.
	State     State     `json:"state"`
	CheckedAt time.Time `json:"checked_at"`
	// RemovedUpstream reports that the skill's folder no longer exists
	// upstream; State is then StateUpToDate and the skill gets
	// IssueRemovedUpstream.
	RemovedUpstream bool `json:"removed_upstream,omitempty"`
	// Detail is the technical reason of a failed check, in English.
	Detail string `json:"detail,omitempty"`
}

// SkillsLock is the global lock of npx skills as skillhub reads it. Only
// the fields skillhub uses are decoded; the lock store keeps every other
// key of the file untouched.
type SkillsLock struct {
	Version int `json:"version"`
	// Entries are in the file's order.
	Entries []LockEntry `json:"entries"`
	// Revision identifies the bytes that were read, so a caller can tell
	// whether the file changed since.
	Revision string `json:"revision"`
}

// LockEntry is one entry of the global lock, under skills.<Name>.
type LockEntry struct {
	Name            string `json:"name"`
	Source          string `json:"source"`
	SourceType      string `json:"source_type"`
	SourceURL       string `json:"source_url"`
	SkillPath       string `json:"skill_path"`
	SkillFolderHash string `json:"skill_folder_hash"`
	// Ref is set only when the entry carries one; it is never added,
	// changed or removed.
	Ref string `json:"ref,omitempty"`
	// Commit is the installed commit SHA, set only when the entry carries
	// one (npx skills 1.9.0 and later, skills-cli-internals.md fact 13).
	Commit      string `json:"commit,omitempty"`
	InstalledAt string `json:"installed_at"`
	UpdatedAt   string `json:"updated_at"`
	// Raw is the exact JSON of the entry, what a snapshot keeps as
	// Snapshot.LockEntryBefore.
	Raw json.RawMessage `json:"raw"`
}

// Settings defaults and bounds (docs/design.md section 4, A14).
const (
	DefaultCheckInterval = time.Hour
	MinCheckInterval     = 15 * time.Minute
)

// ErrIntervalTooShort reports a check interval under MinCheckInterval.
var ErrIntervalTooShort = errors.New("check interval is shorter than the minimum")

// Settings are the user's settings, settings.toml.
type Settings struct {
	CheckInterval time.Duration `json:"check_interval"`
	Notifications bool          `json:"notifications"`
	// Ignore lists what the user excluded from the inventory.
	Ignore []string `json:"ignore"`
}

// DefaultSettings returns the settings used when settings.toml is absent.
func DefaultSettings() Settings {
	return Settings{CheckInterval: DefaultCheckInterval, Notifications: true, Ignore: []string{}}
}

// Validate rejects settings skillhub cannot honor.
func (s Settings) Validate() error {
	if s.CheckInterval < MinCheckInterval {
		return fmt.Errorf("%w: %s < %s", ErrIntervalTooShort, s.CheckInterval, MinCheckInterval)
	}
	return nil
}

// ParkedEntry is a whole entry of the npx skills global lock taken out of
// it (version 2, L32). Raw holds its exact bytes, ref included. Before and
// After name its neighbours in the lock ("" at either end), so it can go
// back to the same position. LockSHA is the hash of the lock before the
// entry left.
type ParkedEntry struct {
	Name    string `json:"name"`
	Raw     []byte `json:"raw"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
	LockSHA string `json:"lock_sha"`
}
