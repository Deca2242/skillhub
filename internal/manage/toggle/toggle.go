// Package toggle plans disabling, enabling, linking and unlinking a skill and
// adopting the skills parked by hand in ~/.agents/skills/OFF/
// (docs/design-v2.md sections 5 and 11, decisions 3 and 4). A planner only
// reads: it returns the engine plan, its identifier and a typed preview, and
// running it is engine.Apply with that same plan.
//
// The identity of a skill and the links that resolve to its folder come only
// from the graph (internal/graph), never from grouping by name.
package toggle

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// Plans builds engine plans and reads the snapshots of earlier operations;
// *engine.Engine satisfies it. A planner never calls Apply.
type Plans interface {
	NewBuilder(meta engine.Meta) *engine.Builder
	ReadSnapshot(id string) (engine.Snapshot, error)
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

// DirLister lists the direct entries of a directory, each with its real
// path; ports.SkillDirScanner satisfies it. It is how a planner reads the real
// path of a link directory (Resolver).
type DirLister interface {
	Scan(dir string) ([]ports.DirEntry, error)
}

// Deps are what a planner reads.
type Deps struct {
	Paths     paths.Paths
	Harnesses []data.Harness
	// Profiles are the read-only diagnostic profiles: a link in one of their
	// roots is never touched (S7, decision 6).
	Profiles []data.DiagnosticProfile
	Plans    Plans
	State    StateReader
	Lock     LockReader
	Stat     Stat
	// Dirs resolves the real path of a link directory; see Resolver for
	// what happens without it.
	Dirs DirLister
}

// Planner plans the operations of this package.
type Planner struct {
	d Deps
}

// New builds a Planner.
func New(d Deps) *Planner { return &Planner{d: d} }

// Action names an operation of this package; it is the plan's Meta.Action.
type Action string

const (
	ActionDisable  Action = "disable"
	ActionEnable   Action = "enable"
	ActionUnlink   Action = "unlink"
	ActionLink     Action = "link"
	ActionAdoptOff Action = "adopt-off"
)

// LinkChange is a symlink the plan removes or creates, with its exact
// content and the harnesses whose directory holds it.
type LinkChange struct {
	Path      string
	Target    string
	Harnesses []string
	// Broken is true for a removed link that leads nowhere.
	Broken bool
}

// SkillChange is what the plan does to one skill.
type SkillChange struct {
	Key string
	// From and To are the folder's move; both empty when it does not move.
	From, To string
	// RemovedLinks and CreatedLinks are the links the plan removes and
	// creates, in plan order.
	RemovedLinks []LinkChange
	CreatedLinks []LinkChange
	// LockEntry is true when the plan parks or restores the skill's lock
	// entry.
	LockEntry bool
	// RestoreLinks are the links enabling the skill later will create: the
	// links a parked skill keeps.
	RestoreLinks []domain.LinkRecord
	// StillVisible are, for Disable and AdoptOff, the ways a harness still
	// sees the skill after the plan, as the graph observed them: an own copy
	// or any other entry the plan does not remove. The plan never touches
	// them (design-v2 section 11, item 4).
	StillVisible []graph.Sighting
}

// Preview is a plan as the user sees it, as typed data; the texts in Spanish
// belong to the entry points.
type Preview struct {
	Action Action
	PlanID string
	Skills []SkillChange
	// Harness is the harness of Unlink and Link.
	Harness string
	// StillVisible are the ways the harness still sees the skill after
	// Unlink, or, for Disable and AdoptOff, every skill's StillVisible in
	// order; Visible the ways the harness already sees it before Link, as
	// the graph observed them.
	StillVisible []graph.Sighting
	Visible      []graph.Sighting
	// SharesDirWith are the other harnesses that read the directory Unlink
	// or Link changes: the change reaches them too.
	SharesDirWith []string
	// Kept are paths the plan saw and deliberately leaves alone: a loose
	// file such as OFF/README.md, or a link in a read-only profile.
	Kept []string
}

// Proposal is a plan with its preview. Running it is engine.Apply(Plan).
type Proposal struct {
	Plan    engine.Plan
	Preview Preview
}

var (
	// ErrInvalidKey reports a key that is not a single folder name.
	ErrInvalidKey = errors.New("invalid skill key")
	// ErrNotFound reports a key with no active skill folder in the store.
	ErrNotFound = errors.New("no active skill with that key in the store")
	// ErrForeignZone reports a key whose folder belongs to a zone skillhub
	// never touches: a plugin, gentle-ai, _shared or a provider folder.
	ErrForeignZone = errors.New("the folder belongs to a zone skillhub never touches")
	// ErrAmbiguous reports a folder that another skill also claims, or that
	// an alias in the store reaches: acting on it would break the other.
	ErrAmbiguous = errors.New("the skill's folder is shared with an alias or another skill")
	// ErrAlreadyParked reports a key that is already parked.
	ErrAlreadyParked = errors.New("the skill is already disabled")
	// ErrNotParked reports enabling a key that is not parked.
	ErrNotParked = errors.New("the skill is not disabled")
	// ErrKeyReinstalled reports enabling a key that exists again in the store
	// or in the lock; KeyReinstalledError tells which.
	ErrKeyReinstalled = errors.New("another skill with the same key was installed while it was disabled")
	// ErrKeyInUse reports adopting an OFF/ skill whose key is taken in the
	// store.
	ErrKeyInUse = errors.New("the key is taken in the store")
	// ErrUnknownHarness reports a harness id that is not in the table.
	ErrUnknownHarness = errors.New("unknown harness")
	// ErrNotAdministrable reports a harness without a link directory: a
	// universal one reads the store and has no link to remove or create.
	ErrNotAdministrable = errors.New("the harness has no link directory skillhub can manage")
	// ErrHarnessNotDetected reports linking in a harness that is not on this
	// machine.
	ErrHarnessNotDetected = errors.New("the harness is not detected on this machine")
	// ErrNotLinked reports unlinking a skill the harness does not link.
	ErrNotLinked = errors.New("the harness has no link to the skill")
	// ErrAlreadyLinked reports linking a skill the harness already links.
	ErrAlreadyLinked = errors.New("the harness already links the skill")
	// ErrPathTaken reports a link path where something already exists.
	ErrPathTaken = errors.New("something already exists at the link path")
	// ErrParkingVisible reports a parking area inside a directory a harness
	// reads, where a parked skill would stay visible.
	ErrParkingVisible = errors.New("the parking area is inside a directory a harness reads")
	// ErrNothingToAdopt reports an OFF/ folder with no skill to adopt.
	ErrNothingToAdopt = errors.New("no skill to adopt in OFF/")
	// ErrNeighbourParked reports a lock entry whose neighbours are not in
	// the lock; NeighbourParkedError tells which.
	ErrNeighbourParked = errors.New("a neighbour of the lock entry is not in the lock")
	// ErrBadParkedRecord reports a parked record that does not match the
	// parking area.
	ErrBadParkedRecord = errors.New("the parked record does not match the parking area")
)

// NeighbourParkedError reports enabling a skill whose lock entry cannot go
// back yet: the lock store puts an entry back only between the neighbours it
// had (WL), and one of them is not in the lock. It matches ErrNeighbourParked.
type NeighbourParkedError struct {
	Key string
	// Parked are the missing neighbours that are parked: enabling them
	// first lets this one back in.
	Parked []string
	// Gone are the missing neighbours skillhub does not hold.
	Gone []string
}

func (e *NeighbourParkedError) Error() string {
	return fmt.Sprintf("%v: %s (parked %v, gone %v)", ErrNeighbourParked, e.Key, e.Parked, e.Gone)
}

func (e *NeighbourParkedError) Unwrap() error { return ErrNeighbourParked }

// KeyReinstalledError tells what took the key back while the skill was
// disabled. It matches ErrKeyReinstalled. The user chooses what to do.
type KeyReinstalledError struct {
	Key string
	// Folder is the path in the store that exists again; empty if none.
	Folder string
	// LockEntry is true when the lock holds an entry with the key again.
	LockEntry bool
}

func (e *KeyReinstalledError) Error() string {
	var what []string
	if e.Folder != "" {
		what = append(what, "the folder "+e.Folder+" exists")
	}
	if e.LockEntry {
		what = append(what, "the lock has an entry "+e.Key)
	}
	return fmt.Sprintf("%v: %s: %s", ErrKeyReinstalled, e.Key, strings.Join(what, " and "))
}

func (e *KeyReinstalledError) Unwrap() error { return ErrKeyReinstalled }

// validKey accepts a single folder name.
func validKey(key string) error {
	if key == "" || key == "." || key == ".." || strings.ContainsRune(key, filepath.Separator) ||
		strings.ContainsRune(key, '/') || filepath.Base(key) != key {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return nil
}

// within reports whether p is root or below it.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// sortedUnique sorts and compacts a list of ids.
func sortedUnique(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
