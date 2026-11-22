// Package repair plans the repairs of Doctor's findings (docs/design-v2.md
// sections 8 and 11, item 4): deleting or re-pointing a broken link, enabling
// a parked skill a link leads to, adopting copies into the store, parking a
// lock entry whose folder is gone. A planner only reads: it returns the
// engine plan, its identifier and a typed preview, and running it is
// engine.Apply with that same plan. The texts in Spanish belong to the entry
// points.
//
// A repair reuses the planner that already owns a step (section 15, item 3):
// adopting OFF/ is toggle's AdoptOff and enabling is toggle's Enable. Every
// plan is built on the graph as it is when planning, after checking that the
// finding the user chose is still there and unchanged.
package repair

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"reflect"
	"slices"

	"skillhub/internal/data"
	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/treehash"
)

// Observer observes the machine: the graph as it is now.
type Observer interface {
	Observe() (graph.Graph, error)
}

// Plans builds engine plans; *engine.Engine satisfies it. A planner never
// calls Apply.
type Plans interface {
	NewBuilder(meta engine.Meta) *engine.Builder
	// ReadSnapshot is what toggle's Enable reads; the repairs pass it on.
	ReadSnapshot(id string) (engine.Snapshot, error)
}

// Toggler is the part of toggle's planner the repairs reuse;
// *toggle.Planner satisfies it.
type Toggler interface {
	Enable(key string) (toggle.Proposal, error)
	AdoptOff(g graph.Graph) (toggle.Proposal, error)
}

// StateReader reads skillhub's persisted state.
type StateReader interface {
	Load() (domain.PersistedState, error)
}

// LockReader reads the npx skills global lock.
type LockReader interface {
	Read() (domain.SkillsLock, error)
}

// Stat tells what is at a path without following a final symlink; it fails
// with fs.ErrNotExist when nothing is there.
type Stat interface {
	Mode(path string) (iofs.FileMode, error)
}

// Deps are what the planner reads.
type Deps struct {
	Paths     paths.Paths
	Harnesses []data.Harness
	// Profiles are the read-only diagnostic profiles: nothing in their roots
	// is ever touched (S7, decision 6).
	Profiles []data.DiagnosticProfile
	Observe  Observer
	Plans    Plans
	Toggle   Toggler
	State    StateReader
	Lock     LockReader
	Stat     Stat
	// Dirs reads the real path of each link directory (toggle.Resolver);
	// the app passes its SkillDirScanner.
	Dirs toggle.DirLister
	// HashDir hashes a folder for the diff between copies; nil means
	// treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
}

// Planner plans repairs.
type Planner struct {
	d Deps
}

// New builds a Planner.
func New(d Deps) *Planner {
	if d.HashDir == nil {
		d.HashDir = treehash.Dir
	}
	return &Planner{d: d}
}

// Kind names a repair.
type Kind string

const (
	// DeleteLink deletes a broken link.
	DeleteLink Kind = "delete_link"
	// RepointLink points a broken link at the active store skill of its key.
	RepointLink Kind = "repoint_link"
	// EnableAndLink enables the parked skill of the link's key and leaves the
	// link leading to it, in one plan.
	EnableAndLink Kind = "enable_and_link"
	// AdoptOff is toggle's AdoptOff: the broken links towards a skill moved
	// to OFF/ go with the adoption.
	AdoptOff Kind = "adopt_off"
	// Adopt brings a skill's copies to one canonical folder in the store.
	Adopt Kind = "adopt"
	// ParkEntry parks a lock entry whose folder is gone.
	ParkEntry Kind = "park_entry"
	// Reinstall hands a lock entry whose folder is gone to the install flow.
	Reinstall Kind = "reinstall"
)

// Reason tells why a finding gets no repair.
type Reason string

const (
	ReasonNone Reason = ""
	// ReasonManagedByGentleAI: gentle-ai sync would recreate the folder.
	ReasonManagedByGentleAI Reason = "managed_by_gentle_ai"
	// ReasonHomonyms: skills are never merged by name.
	ReasonHomonyms Reason = "homonyms"
	// ReasonLockNameMismatch: the key is npx's; changing it is not a repair.
	ReasonLockNameMismatch Reason = "lock_name_mismatch"
	// ReasonLinkCycle and ReasonLinkOutsideRoots: links skillhub does not
	// follow are only reported.
	ReasonLinkCycle        Reason = "link_cycle"
	ReasonLinkOutsideRoots Reason = "link_outside_roots"
	// ReasonDiagnosticProfile: the path is in a read-only profile.
	ReasonDiagnosticProfile Reason = "diagnostic_profile"
	// ReasonForeignZone: the path is in a zone skillhub never touches.
	ReasonForeignZone Reason = "foreign_zone"
	// ReasonNotInStore: the skill's canonical folder is not a store folder
	// (a clone, or a folder parked in OFF/); it is never adopted.
	ReasonNotInStore Reason = "canonical_not_in_store"
	// ReasonAmbiguous: the folder is shared with an alias or another skill.
	ReasonAmbiguous Reason = "ambiguous"
	// ReasonAlreadyParked: the key is already parked.
	ReasonAlreadyParked Reason = "already_parked"
)

// Offer is what Repairs answers: the repairs that apply, or none and why.
type Offer struct {
	Repairs []Kind
	Reason  Reason
}

// Options are the user's choices for a repair.
type Options struct {
	// Canonical is the folder the user chose as the canonical one, as Doctor
	// listed it (a finding's Paths) or the store folder of the key.
	Canonical string
}

// LinkChange is a symlink the plan removes or creates, with its exact
// content and the harnesses whose directory holds it.
type LinkChange struct {
	Path      string
	Target    string
	Harnesses []string
	// Broken is true for a removed link that leads nowhere.
	Broken bool
}

// Move is a folder the plan moves.
type Move struct {
	From, To string
}

// CopyChange is a real copy the plan removes; the operation's snapshot keeps
// it.
type CopyChange struct {
	Path    string
	TreeSHA string
	// Harnesses are the harnesses that saw the copy.
	Harnesses []string
	// Replaced is true when a link to the canonical folder takes its place.
	Replaced bool
}

// CopyDiff compares a copy with the canonical folder, file by file: the
// paths, slash-separated and relative to the folder, that only the copy has,
// that only the canonical folder has, and that differ.
type CopyDiff struct {
	Path    string
	TreeSHA string
	Added   []string
	Removed []string
	Changed []string
}

// Same reports whether the copy is identical to the canonical folder.
func (c CopyDiff) Same() bool { return len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Changed) == 0 }

// ReinstallRef is the origin of a lock entry, for the install flow: the app
// resolves and prepares that origin as an install, then plans it with
// install.Planner.Reinstall, which replaces the entry in one plan.
type ReinstallRef struct {
	Key        string
	Source     string
	SourceType string
	SourceURL  string
	SkillPath  string
	// Ref is the entry's ref, if it carries one; install refuses those.
	Ref string
}

// Preview is a repair as the user sees it, as typed data.
type Preview struct {
	// Finding is the id of the finding repaired, and Repair the repair.
	Finding string
	Repair  Kind
	PlanID  string
	// Key is the store key of the skill involved, if any.
	Key string
	// Canonical is the canonical folder after the plan.
	Canonical    string
	Moved        []Move
	RemovedLinks []LinkChange
	CreatedLinks []LinkChange
	// RemovedCopies are the copies the plan removes.
	RemovedCopies []CopyChange
	// Copies compare every other copy with the canonical folder.
	Copies []CopyDiff
	// Kept are paths the plan saw and deliberately leaves alone.
	Kept []string
	// LockEntry is the key of the lock entry the plan parks.
	LockEntry string
	// Toggle is the preview of the toggle planner the repair reuses.
	Toggle *toggle.Preview
}

// Proposal is a repair's plan with its preview. Running it is
// engine.Apply(Plan). For Reinstall, Plan is empty and Reinstall tells the
// install flow what to install.
type Proposal struct {
	Plan      engine.Plan
	Preview   Preview
	Reinstall *ReinstallRef
}

var (
	// ErrStale reports a finding that is gone or changed since it was
	// detected; StaleError tells which.
	ErrStale = errors.New("the finding changed since it was detected")
	// ErrNotOffered reports a repair the finding does not get.
	ErrNotOffered = errors.New("the repair does not apply to the finding")
	// ErrNeedsCanonical reports an adoption without the canonical folder,
	// which the user must choose.
	ErrNeedsCanonical = errors.New("the canonical folder must be chosen")
	// ErrBadCanonical reports a canonical folder that is not one of the
	// skill's folders.
	ErrBadCanonical = errors.New("the canonical folder is not one of the skill's folders")
	// ErrLockedSkill reports moving a copy of unknown origin to a key that a
	// lock entry, a parked record or a managed record holds, or over the
	// folder of a lock skill.
	ErrLockedSkill = errors.New("a copy of unknown origin never takes a key a lock entry or skillhub's state holds")
	// ErrLinkedCopy reports a folder the plan would move or remove that links
	// lead to: they would be left broken.
	ErrLinkedCopy = errors.New("links lead to a folder the repair moves or removes")
	// ErrNoPlacement reports a copy a harness that does not read the store
	// sees outside its link directory: removing it would hide the skill.
	ErrNoPlacement = errors.New("a harness that does not read the store would lose the skill")
	// ErrPathTaken reports a link path where something already exists.
	ErrPathTaken = errors.New("something already exists at the link path")
	// ErrKeyInUse reports a store key that another skill holds.
	ErrKeyInUse = errors.New("the key is taken in the store")
	// ErrForeignZone reports a path in a zone skillhub never touches.
	ErrForeignZone = errors.New("the path belongs to a zone skillhub never touches")
)

// StaleError reports a stale finding. It matches ErrStale.
type StaleError struct {
	ID string
	// Now is the finding as Doctor reports it now; nil when it is gone.
	Now *doctor.Finding
}

func (e *StaleError) Error() string {
	if e.Now == nil {
		return fmt.Sprintf("%v: %s is gone", ErrStale, e.ID)
	}
	return fmt.Sprintf("%v: %s changed", ErrStale, e.ID)
}

func (e *StaleError) Unwrap() error { return ErrStale }

// Repairs lists the repairs of f on g, the graph f was detected on, or none
// and the reason.
func (p *Planner) Repairs(g graph.Graph, f doctor.Finding) (Offer, error) {
	st, err := p.loadState()
	if err != nil {
		return Offer{}, err
	}
	return p.offer(g, st, f)
}

// reportOnly are the findings that are only reported.
var reportOnly = map[doctor.Kind]Reason{
	doctor.ManagedByGentleAI: ReasonManagedByGentleAI,
	doctor.Homonyms:          ReasonHomonyms,
	doctor.LockNameMismatch:  ReasonLockNameMismatch,
	doctor.LinkCycle:         ReasonLinkCycle,
	doctor.LinkOutsideRoots:  ReasonLinkOutsideRoots,
}

func (p *Planner) offer(g graph.Graph, st domain.PersistedState, f doctor.Finding) (Offer, error) {
	if r, ok := reportOnly[f.Kind]; ok {
		return Offer{Reason: r}, nil
	}
	switch f.Kind {
	case doctor.BrokenLink:
		return p.linkOffer(g, st, f)
	case doctor.IdenticalCopies, doctor.NoCanonical, doctor.UnmanagedSkill:
		return p.adoptOffer(g, f)
	case doctor.LockEntryWithoutFolder:
		if _, ok := st.Parked[f.LockKey]; ok {
			return Offer{Reason: ReasonAlreadyParked}, nil
		}
		return Offer{Repairs: []Kind{ParkEntry, Reinstall}}, nil
	case doctor.ParkedButVisible:
		// The skill sits in OFF/: adopting OFF/ parks it out of every root.
		return Offer{Repairs: []Kind{AdoptOff}}, nil
	}
	return Offer{}, fmt.Errorf("unknown finding kind %q", f.Kind)
}

// Plan plans repair k of seen, the finding as the user saw it. It observes
// the machine again, detects the findings again and refuses with a
// StaleError when seen is gone or differs; then it refuses, with
// ErrNotOffered, a repair the finding does not get now.
func (p *Planner) Plan(seen doctor.Finding, k Kind, o Options) (Proposal, error) {
	g, err := p.d.Observe.Observe()
	if err != nil {
		return Proposal{}, fmt.Errorf("observe the machine: %w", err)
	}
	findings := doctor.Detect(g)
	i := slices.IndexFunc(findings, func(f doctor.Finding) bool { return f.ID == seen.ID })
	if i < 0 {
		return Proposal{}, &StaleError{ID: seen.ID}
	}
	if now := findings[i]; !reflect.DeepEqual(now, seen) {
		return Proposal{}, &StaleError{ID: seen.ID, Now: &now}
	}
	st, err := p.loadState()
	if err != nil {
		return Proposal{}, err
	}
	offer, err := p.offer(g, st, seen)
	if err != nil {
		return Proposal{}, err
	}
	if !slices.Contains(offer.Repairs, k) {
		return Proposal{}, fmt.Errorf("%w: %s for %s (offered %v, reason %q)", ErrNotOffered, k, seen.ID, offer.Repairs, offer.Reason)
	}
	pv := Preview{Finding: seen.ID, Repair: k}
	switch k {
	case DeleteLink:
		return p.deleteLink(g, seen, pv)
	case RepointLink:
		return p.repoint(g, st, seen, pv)
	case EnableAndLink:
		return p.enableAndLink(g, st, seen, pv)
	case AdoptOff:
		return p.adoptOff(g, pv)
	case Adopt:
		return p.adopt(g, st, seen, o, pv)
	case ParkEntry:
		return p.parkEntry(seen, pv)
	case Reinstall:
		return p.reinstall(seen, pv)
	}
	return Proposal{}, fmt.Errorf("%w: unknown repair %q", ErrNotOffered, k)
}

// build finishes a plan and fills the preview's identifier.
func build(b *engine.Builder, pv Preview) (Proposal, error) {
	plan, err := b.Build()
	if err != nil {
		return Proposal{}, fmt.Errorf("plan %s: %w", pv.Repair, err)
	}
	pv.PlanID = plan.ID
	return Proposal{Plan: plan, Preview: pv}, nil
}

func (p *Planner) loadState() (domain.PersistedState, error) {
	st, err := p.d.State.Load()
	if err != nil {
		return domain.PersistedState{}, fmt.Errorf("load the state: %w", err)
	}
	return st, nil
}
