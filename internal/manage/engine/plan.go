package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"unicode/utf8"

	"skillhub/internal/shared/domain"
)

// StepKind names one atomic step of a plan (docs/design-v2.md section 4).
type StepKind string

const (
	StepMove       StepKind = "move"
	StepLink       StepKind = "link"
	StepUnlink     StepKind = "unlink"
	StepLockPark   StepKind = "lockPark"
	StepLockUnpark StepKind = "lockUnpark"
	StepLockAdd    StepKind = "lockAdd"
	StepLockRemove StepKind = "lockRemove"
	// StepLockReplace replaces an entry in its place in the lock: its
	// preimage is the old entry's exact bytes, its postimage the new one's.
	// The ref never changes (a reinstall, L49).
	StepLockReplace StepKind = "lockReplace"
	StepRemoveTree  StepKind = "removeTree"
	// StepRestoreTree publishes a verified copy of a snapshot folder at a
	// path that does not exist; undoing a removeTree uses it.
	StepRestoreTree StepKind = "restoreTree"
	// StepRemoveClone removes a whole Git clone folder, verified by its
	// complete clone digest (file bytes, .git metadata, empty directories,
	// modes and supported symlinks), rather than treehash.Dir.
	StepRemoveClone StepKind = "removeClone"
	// StepRestoreClone publishes a verified whole-clone snapshot copy at a
	// path that does not exist; undoing a removeClone uses it.
	StepRestoreClone StepKind = "restoreClone"
	// StepRequire changes nothing: its object must be exactly its image, a
	// folder with its tree or a link with its content, when the plan is
	// checked, run, verified and rolled back. It pins what a plan relies on
	// without touching, such as the canonical folder a repair keeps.
	StepRequire StepKind = "require"
)

// ImageState is what one object looks like.
type ImageState string

const (
	// StateAbsent is a path that does not exist, or a lock entry that is not
	// in an existing lock.
	StateAbsent ImageState = "absent"
	// StateTree is a real folder (not a symlink) with TreeSHA.
	StateTree ImageState = "tree"
	// StateClone is a real Git clone folder (not a symlink) verified by CloneSHA.
	StateClone ImageState = "clone"
	// StateLink is a symlink whose content is Target, exactly.
	StateLink ImageState = "link"
	// StateOther is a path that is neither a folder nor a symlink. No step
	// accepts it; it only shows up as foreign state.
	StateOther ImageState = "other"
	// StateEntry is a lock entry whose bytes are Raw.
	StateEntry ImageState = "entry"
	// StateNoLock is a lock entry whose lock file does not exist, so undoing
	// a step can leave no lock at all (L35).
	StateNoLock ImageState = "no-lock"
)

// Image is the observable state of one object: a path (Path set) or an
// entry of the npx skills lock (Entry set).
type Image struct {
	Path  string     `json:"path,omitempty"`
	Entry string     `json:"entry,omitempty"`
	State ImageState `json:"state"`
	// TreeSHA is the git tree SHA of a StateTree folder.
	TreeSHA string `json:"tree_sha,omitempty"`
	// CloneSHA is the complete whole-clone digest of a StateClone folder.
	CloneSHA string `json:"clone_sha,omitempty"`
	// Target is the exact content of a StateLink symlink.
	Target string `json:"target,omitempty"`
	// Raw is the exact bytes of a StateEntry entry.
	Raw []byte `json:"raw,omitempty"`

	// emptyLock marks an observed StateAbsent entry of a lock that holds no
	// entry at all: half way between StateNoLock and StateAbsent. It is never
	// serialized or compared by same.
	emptyLock bool
}

// object identifies what an image describes.
func (i Image) object() string {
	if i.Entry != "" {
		return "entry:" + i.Entry
	}
	return "path:" + i.Path
}

// same reports whether two images describe the same state of an object.
func (i Image) same(o Image) bool {
	return i.Path == o.Path && i.Entry == o.Entry && i.State == o.State &&
		i.TreeSHA == o.TreeSHA && i.CloneSHA == o.CloneSHA && i.Target == o.Target && bytes.Equal(i.Raw, o.Raw)
}

// describe is a short account of an image for an error.
func (i Image) describe() string {
	switch i.State {
	case StateTree:
		return fmt.Sprintf("folder %s", i.TreeSHA)
	case StateClone:
		return fmt.Sprintf("clone %s", i.CloneSHA)
	case StateLink:
		return fmt.Sprintf("link to %q", i.Target)
	case StateEntry:
		return fmt.Sprintf("entry of %d bytes", len(i.Raw))
	default:
		return string(i.State)
	}
}

// Step is one atomic change with its preconditions: Pre is every object the
// step touches as it must be before the step, Post as it is after. Pre and
// Post together tell a step not run, a step run and foreign state apart.
type Step struct {
	Kind StepKind `json:"kind"`
	// Src and Dst are the folders of a move; Src is also the snapshot copy a
	// restoreTree publishes.
	Src string `json:"src,omitempty"`
	Dst string `json:"dst,omitempty"`
	// Path is the link of link and unlink, and the folder of removeTree and
	// restoreTree.
	Path string `json:"path,omitempty"`
	// Target is the exact content of the link that link creates and unlink
	// removes.
	Target string `json:"target,omitempty"`
	// Name is the lock entry of lockPark, lockAdd and lockRemove.
	Name string `json:"name,omitempty"`
	// Raw is the entry lockAdd adds and the entry lockRemove expects.
	Raw []byte `json:"raw,omitempty"`
	// Entry is the parked entry lockUnpark puts back.
	Entry *domain.ParkedEntry `json:"entry,omitempty"`

	Pre  []Image `json:"pre"`
	Post []Image `json:"post"`
}

// Meta is what an operation is about, beyond its steps; the plan identifier
// covers it (docs/design-v2.md section 14).
type Meta struct {
	Action string `json:"action"`
	// Targets are the harnesses the operation reaches.
	Targets []string `json:"targets"`
	// Canonical is the copy the user chose as the canonical one.
	Canonical string `json:"canonical,omitempty"`
	// Source and ReviewedTreeSHA are the origin and the reviewed tree of an
	// install.
	Source          string `json:"source,omitempty"`
	ReviewedTreeSHA string `json:"reviewed_tree_sha,omitempty"`
	// Project binds a project-scoped operation to its startup project
	// directory and exact direct skill copy. Nil for global/legacy plans.
	Project *ProjectScope `json:"project,omitempty"`
}

// Plan is an operation as the user approves it. ID is the SHA-256 of the
// canonical serialization of everything else.
type Plan struct {
	ID string `json:"id"`
	Meta
	// LockSHA is the hash of the lock when the plan was built ("" when there
	// was none); set only when a step touches the lock.
	LockSHA string `json:"lock_sha,omitempty"`
	// TouchesLock records whether LockSHA applies, so "no lock" and "the plan
	// does not look at the lock" stay apart.
	TouchesLock bool   `json:"touches_lock"`
	Steps       []Step `json:"steps"`
	// Effect is the change to skillhub's state the operation makes when it
	// closes (docs/design-v2.md section 14: managed and parked).
	Effect Effect `json:"effect,omitzero"`
	// Undoes is the snapshot of the operation this plan undoes, if it is an
	// undo.
	Undoes string `json:"undoes,omitempty"`
}

// planIDDomain separates plan identifiers from any other SHA-256.
const planIDDomain = "skillhub plan v2\n"

// ComputeID returns the identifier p should carry: the SHA-256 of its
// canonical serialization, with ID itself left out. encoding/json writes
// struct fields in declaration order and is deterministic for these types.
func (p Plan) ComputeID() (string, error) {
	p.ID = ""
	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("serialize plan: %w", err)
	}
	sum := sha256.Sum256(append([]byte(planIDDomain), data...))
	return hex.EncodeToString(sum[:]), nil
}

// verifyID checks that p.ID is the hash of its content.
func (p Plan) verifyID() error {
	want, err := p.ComputeID()
	if err != nil {
		return err
	}
	if p.ID != want {
		return fmt.Errorf("%w: its identifier is %s, its content hashes to %s", ErrPlanChanged, p.ID, want)
	}
	return nil
}

// validPath accepts an absolute, clean path that is not the root and is
// valid UTF-8: encoding/json turns invalid bytes into U+FFFD, so two plans
// would share an identifier and a record would read back another path.
func validPath(p string) bool {
	return p != "" && utf8.ValidString(p) && filepath.IsAbs(p) && filepath.Clean(p) == p &&
		p != string(filepath.Separator)
}

// validText accepts a non-empty, valid UTF-8 link target or entry name.
func validText(s string) bool { return s != "" && utf8.ValidString(s) }

// objects lists the step's objects, as its Pre lists them.
func (s Step) objects() []string {
	out := make([]string, 0, len(s.Pre))
	for _, img := range s.Pre {
		out = append(out, img.object())
	}
	return out
}

// shape checks that a step's arguments and its images name the same objects,
// in the order the step's kind defines.
func (s Step) shape() error {
	var want []string
	switch s.Kind {
	case StepMove:
		if !validPath(s.Src) || !validPath(s.Dst) || s.Src == s.Dst {
			return fmt.Errorf("%w: move %q to %q", ErrInvalidStep, s.Src, s.Dst)
		}
		want = []string{"path:" + s.Src, "path:" + s.Dst}
	case StepLink, StepUnlink:
		if !validPath(s.Path) || !validText(s.Target) {
			return fmt.Errorf("%w: %s %q to %q", ErrInvalidStep, s.Kind, s.Path, s.Target)
		}
		want = []string{"path:" + s.Path}
	case StepRemoveTree, StepRemoveClone:
		if !validPath(s.Path) {
			return fmt.Errorf("%w: remove %q", ErrInvalidStep, s.Path)
		}
		want = []string{"path:" + s.Path}
	case StepRequire:
		if !validPath(s.Path) {
			return fmt.Errorf("%w: require %q", ErrInvalidStep, s.Path)
		}
		want = []string{"path:" + s.Path}
	case StepRestoreTree, StepRestoreClone:
		if !validPath(s.Path) || !validPath(s.Src) || s.Src == s.Path {
			return fmt.Errorf("%w: restore %q from %q", ErrInvalidStep, s.Path, s.Src)
		}
		want = []string{"path:" + s.Path}
	case StepLockPark, StepLockAdd, StepLockRemove, StepLockReplace:
		if !validText(s.Name) {
			return fmt.Errorf("%w: %s needs a UTF-8 entry name", ErrInvalidStep, s.Kind)
		}
		want = []string{"entry:" + s.Name}
	case StepLockUnpark:
		if s.Entry == nil || !validText(s.Entry.Name) {
			return fmt.Errorf("%w: lockUnpark needs a parked entry", ErrInvalidStep)
		}
		want = []string{"entry:" + s.Entry.Name}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidStep, s.Kind)
	}
	got := s.objects()
	if len(s.Post) != len(s.Pre) || len(got) != len(want) {
		return fmt.Errorf("%w: %s has images of %v, want %v", ErrInvalidStep, s.Kind, got, want)
	}
	for i := range want {
		if got[i] != want[i] || s.Post[i].object() != want[i] {
			return fmt.Errorf("%w: %s has images of %v, want %v", ErrInvalidStep, s.Kind, got, want)
		}
	}
	return nil
}

// touchesLock reports whether a step changes the lock.
func (s Step) touchesLock() bool {
	switch s.Kind {
	case StepLockPark, StepLockUnpark, StepLockAdd, StepLockRemove, StepLockReplace:
		return true
	}
	return false
}
