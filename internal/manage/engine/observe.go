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
	e *Engine

	lockLoaded bool
	lockHash   string // "" when the lock does not exist
	entries    map[string][]byte
	keys       []string // the entries in lock order
}

func (e *Engine) newView() *view { return &view{e: e} }

// path observes one path without following a final symlink.
func (v *view) path(p string) (Image, error) {
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
	if of.Entry != "" {
		return v.entry(of.Entry)
	}
	return v.path(of.Path)
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
	seen := make([]Image, len(s.Pre))
	pre, post := true, true
	for i := range s.Pre {
		img, err := v.image(s.Pre[i])
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
	after := map[string]Image{}
	gone := []string{}       // paths earlier steps moved away or removed
	used := map[string]int{} // path -> the step that touches it
	touches := false
	for i, s := range p.Steps {
		if err := s.shape(); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		if err := s.semantics(); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		touches = touches || s.touchesLock()
		if err := checkPaths(i, s, gone, used, after); err != nil {
			return err
		}
		switch s.Kind {
		case StepMove:
			gone = append(gone, s.Src)
		case StepRemoveTree, StepUnlink:
			gone = append(gone, s.Path)
		}
		for j, img := range s.Pre {
			if prev, ok := after[img.object()]; ok && !prev.same(img) {
				return fmt.Errorf("%w: step %d expects %s as %s, the step before leaves %s",
					ErrInvalidStep, i, img.object(), img.describe(), prev.describe())
			}
			after[img.object()] = s.Post[j]
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
	return p.Effect.validate(p.Steps)
}

// checkPaths refuses a step whose paths nest or repeat: a move into its own
// source, a path inside one an earlier step moved away or removed, or a path
// an earlier step already touches. The one repeat allowed is a replacement
// in place (docs/design-v2.md section 8): a step whose preimage of the path
// is absent may use it again when the earlier step on it left it absent, as
// after holds it. Lock entries may chain freely.
func checkPaths(i int, s Step, gone []string, used map[string]int, after map[string]Image) error {
	if s.Kind == StepMove && within(s.Dst, s.Src) {
		return fmt.Errorf("%w: step %d moves %s into itself", ErrInvalidStep, i, s.Src)
	}
	for _, img := range s.Pre {
		if img.Entry != "" {
			continue
		}
		if j, ok := used[img.Path]; ok && !(img.State == StateAbsent && after[img.object()].State == StateAbsent) {
			return fmt.Errorf("%w: steps %d and %d both touch %s", ErrInvalidStep, j, i, img.Path)
		}
		for _, g := range gone {
			if within(img.Path, g) {
				return fmt.Errorf("%w: step %d touches %s inside %s, which an earlier step moved or removed",
					ErrInvalidStep, i, img.Path, g)
			}
		}
	}
	for _, img := range s.Pre {
		if img.Entry == "" {
			used[img.Path] = i
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
	v := e.newView()
	if p.TouchesLock {
		if err := v.loadLock(); err != nil {
			return err
		}
		if v.lockHash != p.LockSHA {
			return fmt.Errorf("%w: the skills lock is %q, the plan saw %q", ErrPlanChanged, v.lockHash, p.LockSHA)
		}
	}
	seen := map[string]bool{}
	for i, s := range p.Steps {
		for _, want := range s.Pre {
			if seen[want.object()] {
				continue
			}
			seen[want.object()] = true
			got, err := v.image(want)
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
