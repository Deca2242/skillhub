package app

import (
	"time"

	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/shared/domain"
)

// The common contract of the version 2 operations (docs/design-v2.md section
// 15, point 7): the entry points ask for a Proposal, show it, and hand the
// same Request back to ApplyPlan with the plan identifier the user approved.
// Everything here is typed data; the Spanish texts belong to the entry
// points.

// Action names a version 2 operation.
type Action string

const (
	ActionDisable  Action = "disable"
	ActionEnable   Action = "enable"
	ActionUnlink   Action = "unlink"
	ActionLink     Action = "link"
	ActionAdoptOff Action = "adopt-off"
	ActionRemove   Action = "remove"
	ActionInstall  Action = "install"
	// ActionReinstall installs again, from its source, a skill whose lock
	// entry has no folder: Doctor's reinstall repair (L49).
	ActionReinstall Action = "reinstall"
	ActionRepair    Action = "repair"
	ActionUndo      Action = "undo"
)

// Request is what the user asked for, serializable, so that approving a
// plan can plan it again and compare (docs/design-v2.md section 11, point 8).
type Request struct {
	Action Action `json:"action"`
	// Key is the skill's store key (disable, enable, unlink, link, remove).
	Key string `json:"key,omitempty"`
	// Group requests complete global removal of the grouped installation
	// identified by Key (a scanner-observed graph Skill ID).
	Group bool `json:"group,omitempty"`
	// AllowProvider authorizes planning reversible removal of skills in
	// confirmed provider zones (gentle-ai, claude-plugin, provider).
	AllowProvider bool `json:"allow_provider,omitempty"`
	// Harness is the harness of unlink and link.
	Harness string `json:"harness,omitempty"`
	// Install is the reviewed revision, its targets and the time the lock
	// entry records.
	Install *InstallRequest `json:"install,omitempty"`
	// Finding, Repair and Canonical choose a Doctor repair.
	Finding   string `json:"finding,omitempty"`
	Repair    string `json:"repair,omitempty"`
	Canonical string `json:"canonical,omitempty"`
	// Snapshot is the version 2 operation an undo undoes.
	Snapshot string `json:"snapshot,omitempty"`
	// DryRun plans and compares without writing anything: no state lock,
	// no record, no counter.
	DryRun bool `json:"dry_run,omitempty"`
}

// InstallRequest is an install as the user approves it: the prepared
// revision with its reviewed tree, the targets, and At, the installedAt and
// updatedAt of the new lock entry, fixed when the plan was first made so
// that planning again gives the same plan.
type InstallRequest struct {
	Revision Revision  `json:"revision"`
	Targets  []string  `json:"targets"`
	At       time.Time `json:"at"`
}

// Revision is a skill of GitHub or of a website resolved and prepared for
// review, exactly as an update is (docs/design-v2.md section 7): its files,
// labels and reviewed tree. Prepared is what the install planner takes back.
type Revision struct {
	Input      string `json:"input"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Repo       string `json:"repo"`
	Commit     string `json:"commit"`
	SkillPath  string `json:"skill_path"`
	SourceURL  string `json:"source_url"`
	TreeSHA    string `json:"tree_sha"`
	StagedPath string `json:"staged_path"`
	// FollowsDefaultBranch is true when no ref was pinned.
	FollowsDefaultBranch bool                    `json:"follows_default_branch"`
	FileChanges          []domain.FileChange     `json:"file_changes"`
	FrontmatterDelta     domain.FrontmatterDelta `json:"frontmatter_delta"`
	Labels               []domain.Label          `json:"labels"`
	Prepared             install.Review          `json:"prepared"`
	// Site is the website of a well-known source, as the user typed it;
	// Repo is then its host. "" for GitHub.
	Site string `json:"site,omitempty"`
}

// Proposal is a plan as the entry points show it. PlanID is what the user
// approves; ApplyPlan runs it only if planning Request again gives the same
// identifier. Exactly one of the previews is set, matching Request.Action.
type Proposal struct {
	Request Request
	PlanID  string
	// Operation is the journal target the operation is recorded under, the
	// one undo takes: v2:<action>, or v2:undo for an undo.
	Operation string
	// Steps are the plan's steps in order, as the user reads them.
	Steps []Step
	// Targets are the harnesses the plan reaches.
	Targets []string

	Toggle  *TogglePreview
	Remove  *RemovePreview
	Install *InstallPreview
	Repair  *RepairPreview
	Undo    *UndoPreview

	// Running are the harness processes running now: they may hold the
	// skill while it changes (section 9). It is a warning, never a block.
	Running []Process
	// Reload are the harnesses that read what the plan changes and should
	// reload their skills afterwards.
	Reload []string

	plan engine.Plan
}

// Step is one step of a plan: Kind is move, link, unlink, lockPark,
// lockUnpark, lockAdd, lockRemove, lockRemoveLast, lockReplace, removeTree,
// restoreTree, removeClone or restoreClone; the other fields are set as the
// kind uses them.
type Step struct {
	Kind   string
	Src    string
	Dst    string
	Path   string
	Target string
	Name   string
}

// Process is a running harness process.
type Process struct {
	PID     int
	Harness string
	// Command is the program name the process was recognized by.
	Command string
}

// Sighting is one way a harness, or a diagnostic profile, reaches a skill:
// Kind is direct, link or copy as the graph tells them.
type Sighting struct {
	Harness string
	Kind    string
	Entry   string
}

// LinkChange is a symlink a plan removes or creates.
type LinkChange struct {
	Path      string
	Target    string
	Harnesses []string
	// Broken is true for a removed link that leads nowhere.
	Broken bool
}

// SkillChange is what a toggle plan does to one skill.
type SkillChange struct {
	Key          string
	From, To     string
	RemovedLinks []LinkChange
	CreatedLinks []LinkChange
	LockEntry    bool
	RestoreLinks []domain.LinkRecord
	StillVisible []Sighting
}

// TogglePreview is a disable, enable, unlink, link or adopt-off plan.
type TogglePreview struct {
	Skills        []SkillChange
	Harness       string
	StillVisible  []Sighting
	Visible       []Sighting
	SharesDirWith []string
	Kept          []string
}

// RemovePreview is a remove plan.
type RemovePreview struct {
	Key         string
	Project     bool
	ProjectDir  string
	ProjectRoot string
	SkillName   string
	Parked      bool
	Off         bool
	Folder      string
	TreeSHA     string
	Links       []LinkChange
	LockEntry   *RemovedEntry
	ParkedEntry *domain.ParkedEntry
	// Modified is no, yes or unknown: whether the folder differs from the
	// lock entry's hash.
	Modified          string
	ModifiedExpected  string
	ModifiedActual    string
	Homonyms          []Homonym
	KeptLinks         []KeptLink
	PendingCandidates []string
	Targets           []string
	Group             bool
	SkillID           string
	Folders           []RemovedFolder
	KeptCopies        []KeptCopy
	RetainedTargets   []string
	Clone             bool
	CloneSkills       []string
	// Provider is the confirmed external provider identifier when the removal
	// touches a provider zone (e.g. gentle-ai, claude-plugin, codex).
	Provider string
}

// RemovedFolder is one eligible global folder removed by a group removal.
type RemovedFolder struct {
	Path      string
	TreeSHA   string
	Off       bool
	InStore   bool
	Harnesses []string
}

// KeptCopy is a copy attributed to a group that remains untouched because it
// is protected or unproven, with its exclusion reason.
type KeptCopy struct {
	Path      string
	Name      string
	TreeSHA   string
	Harnesses []string
	Profile   string
	Reason    string
}

// RemovedEntry is the lock entry a remove takes out.
type RemovedEntry struct {
	Name        string
	Raw         []byte
	DeletesLock bool
}

// Homonym is another copy with the skill's name that a remove leaves alone.
type Homonym struct {
	Path      string
	Name      string
	TreeSHA   string
	Identical bool
	Harnesses []string
	Profile   string
}

// KeptLink is a link a remove leaves broken because skillhub never touches
// its zone or profile.
type KeptLink struct {
	Path    string
	Target  string
	Profile string
	Zone    string
}

// InstallPreview is an install plan.
type InstallPreview struct {
	Key                  string
	Name                 string
	Source               string
	SourceURL            string
	Commit               string
	SkillPath            string
	TreeSHA              string
	StagedPath           string
	StorePath            string
	Targets              []string
	Links                []LinkChange
	LockEntry            []byte
	LockCreated          bool
	FollowsDefaultBranch bool
	Labels               []domain.Label
	// Site is the website of a well-known install; Source is then its host.
	// "" for GitHub.
	Site string
	// ReplacedEntry is, for a reinstall, the exact lock entry the plan
	// removes before it adds LockEntry; nil for an install.
	ReplacedEntry []byte
}

// RepairPreview is a Doctor repair plan.
type RepairPreview struct {
	Finding       string
	Repair        string
	Key           string
	Canonical     string
	Moved         []Move
	RemovedLinks  []LinkChange
	CreatedLinks  []LinkChange
	RemovedCopies []CopyChange
	Copies        []CopyDiff
	Kept          []string
	LockEntry     string
	Toggle        *TogglePreview
	// Reinstall is set, with no plan, for the reinstall repair: the entry's
	// origin goes to ResolveInstall.
	Reinstall *Reinstall
}

// Move is a folder a repair moves.
type Move struct{ From, To string }

// CopyChange is a copy a repair removes or replaces by a link.
type CopyChange struct {
	Path      string
	TreeSHA   string
	Harnesses []string
	Replaced  bool
}

// CopyDiff is how a copy differs from the canonical one.
type CopyDiff struct {
	Path    string
	TreeSHA string
	Added   []string
	Removed []string
	Changed []string
}

// Reinstall is the origin of a lock entry whose folder is gone. Input is the
// source to give ResolveInstall; "" when the entry is not a GitHub one.
type Reinstall struct {
	Key        string
	Input      string
	Source     string
	SourceType string
	SourceURL  string
	SkillPath  string
	Ref        string
}

// UndoPreview is the undo of a version 2 operation.
type UndoPreview struct {
	Snapshot string
	// Of is the action undone.
	Of string
}

// Finding is a Doctor finding (docs/design-v2.md section 8).
type Finding struct {
	ID        string
	Kind      string
	Skill     string
	Name      string
	LockKey   string
	Paths     []string
	Target    string
	Related   []string
	Harnesses []string
	Skills    []string
}

// Offer is what Doctor can do with a finding: the repairs offered, or why
// none is.
type Offer struct {
	Repairs []string
	Reason  string
}

// Result is a plan that ran, or that a dry run only checked.
type Result struct {
	Proposal Proposal
	DryRun   bool
	// SnapshotID names the snapshot that undoes the operation.
	SnapshotID string
	// Committed is true when the operation's commit line is confirmed in the
	// journal (even if ClosePending is true).
	Committed bool
	// ClosePending is true when the engine close did not finish, so the record
	// stays pending for Recover.
	ClosePending bool
	// Superseded are the update candidates a remove marked superseded.
	Superseded []string
}
