package engine

import (
	"fmt"
	"slices"

	"skillhub/internal/shared/domain"
)

// Builder computes a plan step by step. Each step reads the objects it
// touches, from the disk the first time and from what the earlier steps
// leave after that, refuses a step whose objects are not in the state it
// needs, and records the step's images. It changes nothing on disk.
//
// The first refused step makes every later call and Build fail with the same
// error, so a caller cannot build a plan that silently lacks a step.
type Builder struct {
	e     *Engine
	meta  Meta
	view  *view
	state map[string]Image
	steps []Step
	err   error

	effect Effect
	undoes string
}

// NewBuilder starts a plan for meta.
func (e *Engine) NewBuilder(meta Meta) *Builder {
	meta.Targets = slices.Clone(meta.Targets)
	if meta.Targets == nil {
		meta.Targets = []string{}
	}
	if meta.Project != nil {
		cp := *meta.Project
		meta.Project = &cp
		var err error
		if vErr := cp.Validate(); vErr != nil {
			err = fmt.Errorf("%w: %w", ErrInvalidStep, vErr)
		} else if gateErr := e.requireProjectScopeGate(cp, "build"); gateErr != nil {
			err = gateErr
		}
		return &Builder{e: e, meta: meta, view: e.newPlanView(&cp), state: map[string]Image{}, err: err}
	}
	return &Builder{e: e, meta: meta, view: e.newView(), state: map[string]Image{}}
}

// current is the object's state as the plan stands.
func (b *Builder) current(of Image, allowClone bool) (Image, error) {
	if img, ok := b.state[of.object()]; ok {
		return img, nil
	}
	return b.view.observe(of, allowClone)
}

// add checks pre against the plan's current state through accept, then
// records the step.
func (b *Builder) add(s Step, accept func(cur []Image) error) error {
	if b.err != nil {
		return b.err
	}
	err := b.addStep(s, accept)
	if err != nil {
		b.err = fmt.Errorf("plan step %d (%s): %w", len(b.steps), s.Kind, err)
		return b.err
	}
	return nil
}

func (b *Builder) addStep(s Step, accept func(cur []Image) error) error {
	if err := s.shape(); err != nil {
		return err
	}
	if b.meta.Project != nil {
		if err := b.validateProjectStep(s); err != nil {
			return err
		}
	}
	allowClone := stepAllowsClone(s)
	cur := make([]Image, len(s.Pre))
	for i := range s.Pre {
		img, err := b.current(s.Pre[i], allowClone)
		if err != nil {
			return err
		}
		cur[i] = img
	}
	if err := accept(cur); err != nil {
		return err
	}
	s.Pre = cur
	for i := range s.Post {
		b.state[s.Post[i].object()] = s.Post[i]
	}
	b.steps = append(b.steps, s)
	return nil
}

func (b *Builder) validateProjectStep(s Step) error {
	wantPath, err := b.meta.Project.SkillPath()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidStep, err)
	}
	if b.undoes == "" {
		if len(b.steps) > 0 && b.steps[len(b.steps)-1].Kind == StepRemoveTree {
			return fmt.Errorf("%w: project-scoped builder cannot add steps after %s", ErrInvalidStep, StepRemoveTree)
		}
		switch s.Kind {
		case StepUnlink:
			if s.Path == wantPath {
				return fmt.Errorf("%w: project-scoped unlink path %q cannot equal skill path", ErrInvalidStep, s.Path)
			}
			if _, _, ok := parseProjectSlot(b.meta.Project.ProjectDir, s.Path); !ok {
				return fmt.Errorf("%w: project-scoped unlink path %q is outside approved project roots", ErrInvalidStep, s.Path)
			}
			for _, prev := range b.steps {
				if prev.Path == s.Path {
					return fmt.Errorf("%w: duplicate project-scoped unlink path %q", ErrInvalidStep, s.Path)
				}
			}
			return nil
		case StepRemoveTree:
			if s.Path != wantPath {
				return fmt.Errorf("%w: project-scoped step path %q does not match %q", ErrInvalidStep, s.Path, wantPath)
			}
			return nil
		default:
			return fmt.Errorf("%w: project-scoped builder expects %s or %s, got %s", ErrInvalidStep, StepUnlink, StepRemoveTree, s.Kind)
		}
	}

	if len(b.steps) == 0 {
		if s.Kind != StepRestoreTree {
			return fmt.Errorf("%w: project-scoped undo builder expects %s first, got %s", ErrInvalidStep, StepRestoreTree, s.Kind)
		}
		if s.Path != wantPath {
			return fmt.Errorf("%w: project-scoped step path %q does not match %q", ErrInvalidStep, s.Path, wantPath)
		}
		return nil
	}
	if s.Kind != StepLink {
		return fmt.Errorf("%w: project-scoped undo builder expects %s, got %s", ErrInvalidStep, StepLink, s.Kind)
	}
	if s.Path == wantPath {
		return fmt.Errorf("%w: project-scoped link path %q cannot equal skill path", ErrInvalidStep, s.Path)
	}
	if _, _, ok := parseProjectSlot(b.meta.Project.ProjectDir, s.Path); !ok {
		return fmt.Errorf("%w: project-scoped link path %q is outside approved project roots", ErrInvalidStep, s.Path)
	}
	for _, prev := range b.steps[1:] {
		if prev.Path == s.Path {
			return fmt.Errorf("%w: duplicate project-scoped link path %q", ErrInvalidStep, s.Path)
		}
	}
	return nil
}

func preconditionf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrPrecondition}, args...)...)
}

// Move moves the real folder src to dst, which must not exist.
func (b *Builder) Move(src, dst string) error {
	s := Step{Kind: StepMove, Src: src, Dst: dst,
		Pre:  []Image{{Path: src}, {Path: dst}},
		Post: []Image{{Path: src, State: StateAbsent}, {Path: dst, State: StateTree}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateTree {
			return preconditionf("%s is %s, not a folder", src, cur[0].describe())
		}
		if cur[1].State != StateAbsent {
			return preconditionf("%s is %s, not absent", dst, cur[1].describe())
		}
		s.Post[1].TreeSHA = cur[0].TreeSHA
		return nil
	})
}

// Link creates path as a symlink whose content is target; path must not
// exist.
func (b *Builder) Link(path, target string) error {
	s := Step{Kind: StepLink, Path: path, Target: target,
		Pre:  []Image{{Path: path}},
		Post: []Image{{Path: path, State: StateLink, Target: target}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateAbsent {
			return preconditionf("%s is %s, not absent", path, cur[0].describe())
		}
		return nil
	})
}

// Unlink removes the symlink path, whose content must be exactly target.
func (b *Builder) Unlink(path, target string) error {
	s := Step{Kind: StepUnlink, Path: path, Target: target,
		Pre:  []Image{{Path: path}},
		Post: []Image{{Path: path, State: StateAbsent}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateLink || cur[0].Target != target {
			return preconditionf("%s is %s, not a link to %q", path, cur[0].describe(), target)
		}
		return nil
	})
}

// RemoveTree removes the real folder path.
func (b *Builder) RemoveTree(path string) error {
	s := Step{Kind: StepRemoveTree, Path: path,
		Pre:  []Image{{Path: path}},
		Post: []Image{{Path: path, State: StateAbsent}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateTree {
			return preconditionf("%s is %s, not a folder", path, cur[0].describe())
		}
		return nil
	})
}

// RemoveClone removes the whole Git clone folder at path, verifying its
// complete contents, .git metadata, empty directories, modes and supported
// symlinks. Ordinary RemoveTree continues to refuse clones.
func (b *Builder) RemoveClone(path string) error {
	s := Step{Kind: StepRemoveClone, Path: path,
		Pre:  []Image{{Path: path, State: StateClone}},
		Post: []Image{{Path: path, State: StateAbsent}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateClone {
			return preconditionf("%s is %s, not a clone", path, cur[0].describe())
		}
		return nil
	})
}

// Require adds a step that changes nothing and needs the path want names to
// be exactly want: a real folder with its tree SHA or a symlink with its
// content.
func (b *Builder) Require(want Image) error {
	want.Raw = slices.Clone(want.Raw)
	s := Step{Kind: StepRequire, Path: want.Path, Pre: []Image{want}, Post: []Image{want}}
	return b.add(s, func(cur []Image) error {
		if want.Entry != "" || (want.State != StateTree && want.State != StateLink) {
			return fmt.Errorf("%w: require needs a folder or a link, not %s", ErrInvalidStep, want.describe())
		}
		if !cur[0].same(want) {
			return preconditionf("%s is %s, not %s", want.Path, cur[0].describe(), want.describe())
		}
		return nil
	})
}

// LockPark takes entry name out of the lock, to keep it parked.
func (b *Builder) LockPark(name string) error {
	s := Step{Kind: StepLockPark, Name: name,
		Pre:  []Image{{Entry: name}},
		Post: []Image{{Entry: name, State: StateAbsent}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateEntry {
			return preconditionf("lock entry %s is %s", name, cur[0].describe())
		}
		return nil
	})
}

// LockUnpark puts a parked entry back. The lock must exist without the entry.
func (b *Builder) LockUnpark(entry domain.ParkedEntry) error {
	entry.Raw = slices.Clone(entry.Raw)
	s := Step{Kind: StepLockUnpark, Entry: &entry,
		Pre:  []Image{{Entry: entry.Name}},
		Post: []Image{{Entry: entry.Name, State: StateEntry}}}
	return b.add(s, func(cur []Image) error {
		raw, err := canonicalEntry(entry.Raw)
		if err != nil {
			return err
		}
		if cur[0].State != StateAbsent {
			return preconditionf("lock entry %s is %s, not absent from an existing lock", entry.Name, cur[0].describe())
		}
		s.Post[0].Raw = raw
		return nil
	})
}

// LockAdd adds entry name with raw. The entry must not exist; the lock may
// not exist either, and then the preimage records that.
func (b *Builder) LockAdd(name string, raw []byte) error {
	s := Step{Kind: StepLockAdd, Name: name, Raw: slices.Clone(raw),
		Pre:  []Image{{Entry: name}},
		Post: []Image{{Entry: name, State: StateEntry}}}
	return b.add(s, func(cur []Image) error {
		canon, err := canonicalEntry(raw)
		if err != nil {
			return err
		}
		if cur[0].State != StateAbsent && cur[0].State != StateNoLock {
			return preconditionf("lock entry %s is %s, not absent", name, cur[0].describe())
		}
		s.Post[0].Raw = canon
		return nil
	})
}

// LockRemove takes entry name out of the lock; its bytes must be
// expectedRaw.
func (b *Builder) LockRemove(name string, expectedRaw []byte) error {
	s := Step{Kind: StepLockRemove, Name: name, Raw: slices.Clone(expectedRaw),
		Pre:  []Image{{Entry: name}},
		Post: []Image{{Entry: name, State: StateAbsent}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateEntry || string(cur[0].Raw) != string(expectedRaw) {
			return preconditionf("lock entry %s is %s, not the entry expected", name, cur[0].describe())
		}
		return nil
	})
}

// LockReplace replaces entry name, whose bytes must be expectedRaw, with raw
// in its place: the lock keeps its order. raw must carry exactly the ref the
// entry has, none included, so a ref is never added, changed or removed;
// Build refuses any other with ErrInvalidStep (the step's semantics).
func (b *Builder) LockReplace(name string, expectedRaw, raw []byte) error {
	s := Step{Kind: StepLockReplace, Name: name, Raw: slices.Clone(raw),
		Pre:  []Image{{Entry: name}},
		Post: []Image{{Entry: name, State: StateEntry}}}
	return b.add(s, func(cur []Image) error {
		canon, err := canonicalEntry(raw)
		if err != nil {
			return err
		}
		if cur[0].State != StateEntry || string(cur[0].Raw) != string(expectedRaw) {
			return preconditionf("lock entry %s is %s, not the entry expected", name, cur[0].describe())
		}
		s.Post[0].Raw = canon
		return nil
	})
}

// LockRemoveLast takes entry name out of the lock and then deletes the
// lock, which must hold no other entry: the inverse of an add that created
// the lock (L35). Its bytes must be expectedRaw and carry no ref.
func (b *Builder) LockRemoveLast(name string, expectedRaw []byte) error {
	s := Step{Kind: StepLockRemove, Name: name, Raw: slices.Clone(expectedRaw),
		Pre:  []Image{{Entry: name}},
		Post: []Image{{Entry: name, State: StateNoLock}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateEntry || string(cur[0].Raw) != string(expectedRaw) {
			return preconditionf("lock entry %s is %s, not the entry expected", name, cur[0].describe())
		}
		if len(b.view.entries) != 1 {
			return preconditionf("the lock holds %d entries, not only %s", len(b.view.entries), name)
		}
		return nil
	})
}

// RestoreTree publishes the snapshot folder copyDir, which must hash to
// treeSHA, at path, which must not exist.
func (b *Builder) RestoreTree(path, copyDir, treeSHA string) error {
	s := Step{Kind: StepRestoreTree, Path: path, Src: copyDir,
		Pre:  []Image{{Path: path}},
		Post: []Image{{Path: path, State: StateTree, TreeSHA: treeSHA}}}
	return b.add(s, func(cur []Image) error {
		if cur[0].State != StateAbsent {
			return preconditionf("%s is %s, not absent", path, cur[0].describe())
		}
		tree, err := b.e.d.HashDir(copyDir)
		if err != nil {
			return fmt.Errorf("hash %s: %w", copyDir, err)
		}
		if tree.SHA != treeSHA {
			return fmt.Errorf("%w: %s is %s, want %s", ErrSnapshotMismatch, copyDir, tree.SHA, treeSHA)
		}
		return nil
	})
}

// RestoreClone publishes the verified whole-clone snapshot folder copyDir,
// which must verify to cloneSHA, at path, which must not exist.
func (b *Builder) RestoreClone(path, copyDir, cloneSHA string) error {
	s := Step{Kind: StepRestoreClone, Path: path, Src: copyDir,
		Pre:  []Image{{Path: path}},
		Post: []Image{{Path: path, State: StateClone, CloneSHA: cloneSHA}}}
	return b.add(s, func(cur []Image) error {
		if !validSHA256(cloneSHA) {
			return fmt.Errorf("%w: clone digest %q is not a SHA-256", ErrInvalidStep, cloneSHA)
		}
		if cur[0].State != StateAbsent {
			return preconditionf("%s is %s, not absent", path, cur[0].describe())
		}
		manifest, err := b.e.inspectClone(copyDir)
		if err != nil {
			return fmt.Errorf("%w: verify clone snapshot %s: %w", ErrSnapshotMismatch, copyDir, err)
		}
		if manifest.SHA != cloneSHA {
			return fmt.Errorf("%w: %s is %s, want %s", ErrSnapshotMismatch, copyDir, manifest.SHA, cloneSHA)
		}
		return nil
	})
}

// SetEffect sets the change to skillhub's state the plan makes when it
// closes. Build checks it against the steps.
func (b *Builder) SetEffect(eff Effect) error {
	if b.err != nil {
		return b.err
	}
	eff.SetManaged = slices.Clone(eff.SetManaged)
	eff.DropManaged = slices.Clone(eff.DropManaged)
	eff.SetParked = slices.Clone(eff.SetParked)
	eff.DropParked = slices.Clone(eff.DropParked)
	b.effect = eff
	return nil
}

// Build returns the plan with its identifier.
func (b *Builder) Build() (Plan, error) {
	if b.err != nil {
		return Plan{}, b.err
	}
	if b.meta.Project != nil {
		if err := b.e.requireProjectScopeGate(*b.meta.Project, "build"); err != nil {
			return Plan{}, err
		}
	}
	p := Plan{Meta: b.meta, Steps: slices.Clone(b.steps), Effect: b.effect, Undoes: b.undoes}
	if p.Steps == nil {
		p.Steps = []Step{}
	}
	for _, s := range p.Steps {
		p.TouchesLock = p.TouchesLock || s.touchesLock()
	}
	if p.TouchesLock {
		if err := b.view.loadLock(); err != nil {
			return Plan{}, err
		}
		p.LockSHA = b.view.lockHash
	}
	if err := p.validate(); err != nil {
		return Plan{}, err
	}
	id, err := p.ComputeID()
	if err != nil {
		return Plan{}, err
	}
	p.ID = id
	return p, nil
}
