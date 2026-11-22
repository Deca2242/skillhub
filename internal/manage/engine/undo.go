package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/shared/domain"
)

// UndoPlan builds the plan that undoes the version 2 operation of snapshot
// id, so it can be shown before Undo runs it. The operation must be applied
// and not undone yet. Everything it left must still be exactly its
// postimage, on disk and in skillhub's state; otherwise UndoPlan refuses with
// ErrChangedSince and never reverts what somebody changed afterwards
// (docs/design-v2.md section 11, point 8). The plan is a new plan: every step
// of the operation inverted, in reverse order, with the inverse effect.
func (e *Engine) UndoPlan(id string) (Plan, error) {
	want, err := e.undoable(id)
	if err != nil {
		return Plan{}, err
	}
	snap, sum, err := e.verifySnapshot(id)
	if err != nil {
		return Plan{}, err
	}
	if sum != want {
		return Plan{}, fmt.Errorf("%w: snapshot %s changed since the operation closed", ErrUnsupportedRecord, id)
	}
	orig := snap.Plan
	v := e.newView()
	for _, want := range finalImages(orig) {
		if onlyRequired(orig, want.object()) {
			continue // not the operation's to undo; a later change is fine
		}
		got, err := v.image(want)
		if err != nil {
			return Plan{}, err
		}
		if !got.same(want) {
			return Plan{}, fmt.Errorf("%w: %s is %s, the operation left %s",
				ErrChangedSince, strings.SplitN(want.object(), ":", 2)[1], got.describe(), want.describe())
		}
	}
	if !orig.Effect.IsZero() {
		st, err := e.d.State.Load()
		if err != nil {
			return Plan{}, fmt.Errorf("read the state: %w", err)
		}
		if err := orig.Effect.still(st, orig, snap); err != nil {
			return Plan{}, err
		}
	}
	b := e.NewBuilder(Meta{Action: "undo", Targets: orig.Targets, Canonical: orig.Canonical,
		Source: orig.Source, ReviewedTreeSHA: orig.ReviewedTreeSHA})
	for i := len(orig.Steps) - 1; i >= 0; i-- {
		if err := b.inverse(orig, snap, i); err != nil {
			return Plan{}, err
		}
	}
	if err := b.SetEffect(orig.Effect.inverse(snap.State)); err != nil {
		return Plan{}, err
	}
	b.undoes = id
	return b.Build()
}

// Undo builds the undo plan of snapshot id and applies it.
func (e *Engine) Undo(ctx context.Context, id string) (Outcome, error) {
	p, err := e.UndoPlan(id)
	if err != nil {
		return Outcome{}, err
	}
	return e.Apply(ctx, p)
}

// undoable checks the journal: id is a version 2 operation that was applied
// and not undone since. It returns the hash of snapshot.json the applied
// line recorded.
func (e *Engine) undoable(id string) (string, error) {
	journal, err := e.d.State.Journal()
	if err != nil {
		return "", fmt.Errorf("read the journal: %w", err)
	}
	applied, sum := false, ""
	for _, h := range journal {
		switch {
		case h.SnapshotID == id && h.Action == domain.ActionApplied && strings.HasPrefix(h.Target, "v2:"):
			applied, sum = true, detailField(h, detailSnapshotSHA)
		case h.Action == domain.ActionUndone && undoesOf(h) == id:
			applied = false
		}
	}
	if !applied {
		return "", fmt.Errorf("%s: %w", id, ErrNothingToUndo)
	}
	return sum, nil
}

// inverse adds the step that undoes step i of orig.
func (b *Builder) inverse(orig Plan, snap Snapshot, i int) error {
	s := orig.Steps[i]
	switch s.Kind {
	case StepMove:
		return b.Move(s.Dst, s.Src)
	case StepLink:
		return b.Unlink(s.Path, s.Target)
	case StepUnlink:
		return b.Link(s.Path, s.Target)
	case StepRemoveTree:
		t, ok := snap.treeCopy(s.Path)
		if !ok {
			return fmt.Errorf("%w: no copy of %s", ErrSnapshotNotFound, s.Path)
		}
		return b.RestoreTree(s.Path, filepath.Join(b.e.SnapshotDir(snap.SnapshotID), filepath.FromSlash(t.Copy)), t.TreeSHA)
	case StepRestoreTree:
		return b.RemoveTree(s.Path)
	case StepRequire:
		return nil // it changed nothing, so there is nothing to undo
	case StepLockPark, StepLockRemove:
		if s.Post[0].State == StateNoLock {
			return b.LockAdd(s.Name, s.Raw)
		}
		return b.LockUnpark(neighbours(orig, snap, i))
	case StepLockUnpark:
		return b.LockRemove(s.Entry.Name, s.Post[0].Raw)
	case StepLockReplace:
		return b.LockReplace(s.Name, s.Post[0].Raw, s.Pre[0].Raw)
	case StepLockAdd:
		if s.Pre[0].State == StateNoLock {
			return b.LockRemoveLast(s.Name, s.Post[0].Raw)
		}
		return b.LockRemove(s.Name, s.Post[0].Raw)
	}
	return fmt.Errorf("%w: unknown kind %q", ErrInvalidStep, s.Kind)
}

// onlyRequired reports whether every step of p that touches object is a
// require step: the operation relied on the object and never changed it.
func onlyRequired(p Plan, object string) bool {
	for _, s := range p.Steps {
		if s.Kind != StepRequire && slices.Contains(s.objects(), object) {
			return false
		}
	}
	return true
}
