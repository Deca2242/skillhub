package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// supportedLockVersion is the only lock version the engine plans against
// (skills-cli-internals.md).
const supportedLockVersion = 3

// view observes the disk for one pass: paths are read each time they are
// asked for, the lock once.
type view struct {
	e       *Engine
	project *ProjectScope

	lockLoaded bool
	lockHash   string // "" when the lock does not exist
	entries    map[string][]byte
	keys       []string // the entries in lock order
}

func (e *Engine) newView() *view { return &view{e: e} }

func (e *Engine) newPlanView(proj *ProjectScope) *view {
	if proj == nil {
		return e.newView()
	}
	cp := *proj
	return &view{e: e, project: &cp}
}

// path observes one path without following a final symlink.
func (v *view) path(p string) (Image, error) {
	if v.project != nil {
		return v.projectPath(p)
	}
	mode, err := v.e.d.Files.Mode(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Image{Path: p, State: StateAbsent}, nil
	}
	if err != nil {
		return Image{}, fmt.Errorf("look at %s: %w", p, err)
	}
	switch {
	case mode&fs.ModeSymlink != 0:
		target, err := v.e.d.Links.Readlink(p)
		if err != nil {
			return Image{}, fmt.Errorf("read link %s: %w", p, err)
		}
		return Image{Path: p, State: StateLink, Target: target}, nil
	case mode.IsDir():
		tree, err := v.e.hashTree(p)
		if err != nil {
			return Image{}, err
		}
		return Image{Path: p, State: StateTree, TreeSHA: tree.SHA}, nil
	default:
		return Image{Path: p, State: StateOther}, nil
	}
}

func (v *view) projectPath(p string) (Image, error) {
	wantPath, err := v.project.SkillPath()
	if err != nil {
		return Image{}, fmt.Errorf("%w: %w", ErrInvalidStep, err)
	}
	if p != wantPath {
		return Image{}, fmt.Errorf("%w: project step targets %q, expected %q", ErrInvalidStep, p, wantPath)
	}
	if v.e == nil || v.e.d.ProjectFS == nil {
		return Image{}, fmt.Errorf("%w: observe %s", ErrProjectScopeUnsupported, p)
	}
	exists, foreign, tree, err := v.e.d.ProjectFS.ObserveSkill(v.project.ProjectDir, string(v.project.Root), v.project.SkillName)
	if err != nil {
		return Image{}, fmt.Errorf("%w: %w", ErrUnsupportedTree, err)
	}
	if !exists {
		return Image{Path: p, State: StateAbsent}, nil
	}
	if foreign {
		return Image{Path: p, State: StateOther}, nil
	}
	return Image{Path: p, State: StateTree, TreeSHA: tree.SHA}, nil
}

// hashTree hashes a real folder the snapshot can keep exactly. A folder
// holding a special file, or a clone (its .git is outside the tree hash), is
// ErrUnsupportedTree.
func (e *Engine) hashTree(dir string) (treehash.Tree, error) {
	git, err := e.d.Files.Exists(filepath.Join(dir, ".git"))
	if err != nil {
		return treehash.Tree{}, fmt.Errorf("look for .git in %s: %w", dir, err)
	}
	if git {
		return treehash.Tree{}, fmt.Errorf("%w: %s is a clone", ErrUnsupportedTree, dir)
	}
	tree, err := e.d.HashDir(dir)
	if errors.Is(err, treehash.ErrUnsupportedEntry) {
		return treehash.Tree{}, fmt.Errorf("%w: %s: %w", ErrUnsupportedTree, dir, err)
	}
	if err != nil {
		return treehash.Tree{}, fmt.Errorf("hash %s: %w", dir, err)
	}
	if err := e.refuseSpecialBits(dir, tree); err != nil {
		return treehash.Tree{}, err
	}
	return tree, nil
}

// specialBits are the mode bits a copy does not keep (CopyTree applies the
// permission bits only): set-user-ID, set-group-ID and sticky.
const specialBits = fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// refuseSpecialBits refuses a folder with a special mode bit on its root or on
// any entry the tree lists (docs/design-v2.md section 11, point 6: trees that
// do not meet the snapshot's guarantees are refused).
func (e *Engine) refuseSpecialBits(dir string, tree treehash.Tree) error {
	for _, rel := range treePaths(tree) {
		p := filepath.Join(dir, rel)
		mode, err := e.d.Files.Mode(p)
		if err != nil {
			return fmt.Errorf("look at %s: %w", p, err)
		}
		if mode&specialBits != 0 {
			return fmt.Errorf("%w: %s has mode %v; setuid, setgid and sticky bits are not kept", ErrUnsupportedTree, p, mode)
		}
	}
	return nil
}

// treePaths is the root, ".", and every entry of tree, as file paths.
func treePaths(tree treehash.Tree) []string {
	out := []string{"."}
	for _, n := range tree.Nodes {
		out = append(out, filepath.FromSlash(n.Path))
	}
	return out
}

// loadLock reads the lock once. The hash is read before and after the
// entries, and must agree with the entries' revision, so the view is of one
// version of the file.
func (v *view) loadLock() error {
	if v.lockLoaded {
		return nil
	}
	before, err := v.e.d.Lock.Hash()
	if err != nil {
		return fmt.Errorf("hash the skills lock: %w", err)
	}
	lock, err := v.e.d.Lock.Read()
	if err != nil {
		return fmt.Errorf("read the skills lock: %w", err)
	}
	after, err := v.e.d.Lock.Hash()
	if err != nil {
		return fmt.Errorf("hash the skills lock: %w", err)
	}
	if before != after || (after != "" && lock.Revision != after) {
		return fmt.Errorf("observe the skills lock: %w", ports.ErrLockChanged)
	}
	if after != "" && lock.Version != supportedLockVersion {
		return fmt.Errorf("skills lock version %d: %w", lock.Version, ports.ErrUnsupportedLockVersion)
	}
	v.entries = make(map[string][]byte, len(lock.Entries))
	if after != "" {
		for _, e := range lock.Entries {
			v.entries[e.Name] = e.Raw
			v.keys = append(v.keys, e.Name)
		}
	}
	v.lockHash = after
	v.lockLoaded = true
	return nil
}

// entry observes one lock entry.
func (v *view) entry(name string) (Image, error) {
	if err := v.loadLock(); err != nil {
		return Image{}, err
	}
	if v.lockHash == "" {
		return Image{Entry: name, State: StateNoLock}, nil
	}
	raw, ok := v.entries[name]
	if !ok {
		return Image{Entry: name, State: StateAbsent, emptyLock: len(v.entries) == 0}, nil
	}
	return Image{Entry: name, State: StateEntry, Raw: raw}, nil
}

// image observes the object an image describes.
func (v *view) image(of Image) (Image, error) {
	return v.observe(of, of.State == StateClone)
}

func (v *view) observe(of Image, allowClone bool) (Image, error) {
	if of.Entry != "" {
		return v.entry(of.Entry)
	}
	if allowClone {
		return v.cloneOrPath(of.Path)
	}
	return v.path(of.Path)
}

func (v *view) cloneOrPath(p string) (Image, error) {
	mode, err := v.e.d.Files.Mode(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Image{Path: p, State: StateAbsent}, nil
	}
	if err != nil {
		return Image{}, fmt.Errorf("look at %s: %w", p, err)
	}
	switch {
	case mode&fs.ModeSymlink != 0:
		target, err := v.e.d.Links.Readlink(p)
		if err != nil {
			return Image{}, fmt.Errorf("read link %s: %w", p, err)
		}
		return Image{Path: p, State: StateLink, Target: target}, nil
	case mode.IsDir():
		hasGit, err := v.e.d.Files.Exists(filepath.Join(p, ".git"))
		if err != nil {
			return Image{}, fmt.Errorf("look for .git in %s: %w", p, err)
		}
		if hasGit {
			manifest, err := v.e.inspectClone(p)
			if err != nil {
				return Image{}, err
			}
			return Image{Path: p, State: StateClone, CloneSHA: manifest.SHA}, nil
		}
		tree, err := v.e.hashTree(p)
		if err != nil {
			return Image{}, err
		}
		return Image{Path: p, State: StateTree, TreeSHA: tree.SHA}, nil
	default:
		return Image{Path: p, State: StateOther}, nil
	}
}

func stepAllowsClone(s Step) bool {
	return s.Kind == StepRemoveClone || s.Kind == StepRestoreClone
}

func cloneObjects(p Plan) map[string]bool {
	out := map[string]bool{}
	for _, s := range p.Steps {
		if stepAllowsClone(s) {
			for _, img := range s.Pre {
				out[img.object()] = true
			}
		}
	}
	return out
}

// canonicalEntry is raw as the lock store writes and reads it back
// (orderedjson Value.Bytes), so an entry's post image matches what a read
// of the lock will show.
func canonicalEntry(raw []byte) ([]byte, error) {
	val, err := orderedjson.ParseValue(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: entry: %w", ErrInvalidStep, err)
	}
	if val.Kind() != orderedjson.KindObject {
		return nil, fmt.Errorf("%w: entry is a %s", ErrInvalidStep, val.Kind())
	}
	return val.Bytes(), nil
}

// Progress tells what a step's objects show on disk.
type Progress string

const (
	// NotRun: every object is as the step needs it before running.
	NotRun Progress = "not-run"
	// Done: every object is as the step leaves it.
	Done Progress = "done"
	// Foreign: neither; somebody else changed something.
	Foreign Progress = "foreign"
)

// StepProgress observes a step's objects and tells whether the step has not
// run, has run or meets foreign state. It also returns what it saw.
func (e *Engine) StepProgress(s Step) (Progress, []Image, error) {
	if err := s.shape(); err != nil {
		return "", nil, err
	}
	v := e.newView()
	allowClone := stepAllowsClone(s)
	seen := make([]Image, len(s.Pre))
	pre, post := true, true
	for i := range s.Pre {
		img, err := v.observe(s.Pre[i], allowClone)
		if err != nil {
			return "", nil, err
		}
		seen[i] = img
		pre = pre && img.same(s.Pre[i])
		post = post && img.same(s.Post[i])
	}
	switch {
	case pre:
		return NotRun, seen, nil
	case post:
		return Done, seen, nil
	default:
		return Foreign, seen, nil
	}
}

// validate checks a plan's internal consistency: every step well formed, each
// step's image of an object already touched equal to what the previous step
// left, and TouchesLock set exactly when a step touches the lock.
func (p Plan) validate() error {
	tracker := newPathTracker()
	touches := false
	for i, s := range p.Steps {
		if err := s.shape(); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		if err := s.semantics(); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		touches = touches || s.touchesLock()
		if err := tracker.checkStep(i, s); err != nil {
			return err
		}
		switch s.Kind {
		case StepMove:
			tracker.gone = append(tracker.gone, s.Src)
		case StepRemoveTree, StepRemoveClone, StepUnlink:
			tracker.gone = append(tracker.gone, s.Path)
		}
		for j, img := range s.Pre {
			if prev, ok := tracker.after[img.object()]; ok && !prev.same(img) {
				return fmt.Errorf("%w: step %d expects %s as %s, the step before leaves %s",
					ErrInvalidStep, i, img.object(), img.describe(), prev.describe())
			}
			tracker.after[img.object()] = s.Post[j]
		}
	}
	if touches != p.TouchesLock {
		return fmt.Errorf("%w: touches_lock is %t", ErrInvalidStep, p.TouchesLock)
	}
	if !p.TouchesLock && p.LockSHA != "" {
		return fmt.Errorf("%w: a lock hash on a plan that does not touch the lock", ErrInvalidStep)
	}
	if p.Undoes != "" && !validSnapshotID(p.Undoes) {
		return fmt.Errorf("%w: undoes %q", ErrInvalidStep, p.Undoes)
	}
	if err := p.ValidateProjectScope(); err != nil {
		return err
	}
	return p.Effect.validate(p.Steps)
}

type pathTracker struct {
	gone       []string
	used       map[string]int
	usedOrder  []string
	cloneUsed  map[string]int
	cloneOrder []string
	after      map[string]Image
}

func newPathTracker() *pathTracker {
	return &pathTracker{
		used:      map[string]int{},
		cloneUsed: map[string]int{},
		after:     map[string]Image{},
	}
}

// checkStep refuses a step whose paths nest or repeat: a move into its own
// source, a path inside one an earlier step moved away or removed, a path an
// earlier step already touches, or any mixed or nested use of a clone root.
// The one repeat allowed is a non-clone replacement in place
// (docs/design-v2.md section 8): a step whose preimage of the path is absent
// may use it again when the earlier step on it left it absent, as after holds
// it. Lock entries may chain freely.
func (t *pathTracker) checkStep(i int, s Step) error {
	if s.Kind == StepMove && within(s.Dst, s.Src) {
		return fmt.Errorf("%w: step %d moves %s into itself", ErrInvalidStep, i, s.Src)
	}
	isCloneStep := stepAllowsClone(s)
	for _, img := range s.Pre {
		if img.Entry != "" {
			continue
		}
		if j, ok := t.used[img.Path]; ok {
			if _, wasClone := t.cloneUsed[img.Path]; isCloneStep || wasClone {
				return fmt.Errorf("%w: steps %d and %d cannot mix clone and other steps on %s", ErrInvalidStep, j, i, img.Path)
			}
			if !(img.State == StateAbsent && t.after[img.object()].State == StateAbsent) {
				return fmt.Errorf("%w: steps %d and %d both touch %s", ErrInvalidStep, j, i, img.Path)
			}
		}
		for _, g := range t.gone {
			if within(img.Path, g) {
				return fmt.Errorf("%w: step %d touches %s inside %s, which an earlier step moved or removed",
					ErrInvalidStep, i, img.Path, g)
			}
		}
		if err := t.checkCloneNesting(i, s.Kind, img.Path, isCloneStep); err != nil {
			return err
		}
	}
	for _, img := range s.Pre {
		if img.Entry != "" {
			continue
		}
		if _, ok := t.used[img.Path]; !ok {
			t.usedOrder = append(t.usedOrder, img.Path)
		}
		t.used[img.Path] = i
		if isCloneStep {
			if _, ok := t.cloneUsed[img.Path]; !ok {
				t.cloneOrder = append(t.cloneOrder, img.Path)
			}
			t.cloneUsed[img.Path] = i
		}
	}
	return nil
}

func (t *pathTracker) checkCloneNesting(i int, kind StepKind, p string, isCloneStep bool) error {
	if isCloneStep {
		for _, u := range t.usedOrder {
			if within(u, p) || within(p, u) {
				return fmt.Errorf("%w: step %d (%s) on %s nests with step %d on %s",
					ErrInvalidStep, i, kind, p, t.used[u], u)
			}
		}
		return nil
	}
	for _, c := range t.cloneOrder {
		if within(p, c) || within(c, p) {
			return fmt.Errorf("%w: step %d touches %s, which nests with clone step %d on %s",
				ErrInvalidStep, i, p, t.cloneUsed[c], c)
		}
	}
	return nil
}

// within reports whether path is strictly below dir.
func within(path, dir string) bool {
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// Check revalidates a plan against the disk: its identifier is the hash of
// its content, the lock is the one it was built on, and every object is, the
// first time a step touches it, exactly as the plan saw it. It changes
// nothing.
func (e *Engine) Check(p Plan) error {
	if err := p.verifyID(); err != nil {
		return err
	}
	if err := p.validate(); err != nil {
		return err
	}
	if err := e.requireProjectGate(p, "check"); err != nil {
		return err
	}
	if p.HasProjectScope() {
		sameDev, err := e.d.ProjectFS.SameDevice(p.Project.ProjectDir, string(p.Project.Root), filepath.Dir(e.d.Paths.ParkedDir()))
		if err != nil {
			return fmt.Errorf("check device for %s: %w", p.Canonical, err)
		}
		if !sameDev {
			return fmt.Errorf("move %s to %s: %w", p.Canonical, e.trashRoot(), ports.ErrCrossDevice)
		}
	}
	v := e.newPlanView(p.Project)
	if p.TouchesLock {
		if err := v.loadLock(); err != nil {
			return err
		}
		if v.lockHash != p.LockSHA {
			return fmt.Errorf("%w: the skills lock is %q, the plan saw %q", ErrPlanChanged, v.lockHash, p.LockSHA)
		}
	}
	clones := cloneObjects(p)
	seen := map[string]bool{}
	for i, s := range p.Steps {
		for _, want := range s.Pre {
			if seen[want.object()] {
				continue
			}
			seen[want.object()] = true
			got, err := v.observe(want, clones[want.object()])
			if err != nil {
				return fmt.Errorf("step %d: %w", i, err)
			}
			if !got.same(want) {
				return fmt.Errorf("%w: step %d (%s): %s is %s, the plan expects %s",
					ErrPrecondition, i, s.Kind, want.object(), got.describe(), want.describe())
			}
		}
	}
	return nil
}
