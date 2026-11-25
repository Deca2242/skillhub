package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"skillhub/internal/shared/domain"
)

// rollbackSteps undoes, in reverse order, every step of rec that ran, until
// every object is its preimage again. Only steps up to the one after the
// last recorded one can have run: the engine stops at the first progress
// update that fails.
//
// A first pass only looks: it walks the steps backwards over what the disk
// shows, as if undoing them, and refuses with ForeignStateError if any
// object is neither what a step needs nor what it leaves. Only then does the
// second pass undo, checking each step again right before undoing it. So
// foreign state anywhere leaves everything as it is.
func (e *Engine) rollbackSteps(rec Record, snap Snapshot) error {
	if err := e.requireProjectGate(rec.Plan, "rollback"); err != nil {
		return err
	}
	steps := rec.Plan.Steps
	last := min(rec.LastCompleted+1, len(steps)-1)
	clones := cloneObjects(rec.Plan)
	if err := e.dryRollback(rec.Plan, last, clones); err != nil {
		return err
	}
	for i := last; i >= 0; i-- {
		prog, seen, err := e.observeBack(rec.Plan, i, clones)
		if err != nil {
			return err
		}
		switch prog {
		case NotRun:
			continue
		case Done, halfway:
		default:
			return foreign(rec.Plan, i, seen)
		}
		if err := e.undoStep(rec, snap, i, prog); err != nil {
			return fmt.Errorf("undo step %d (%s): %w", i, steps[i].Kind, err)
		}
		if prog, seen, err = e.observeBack(rec.Plan, i, clones); err != nil {
			return err
		} else if prog != NotRun {
			return foreign(rec.Plan, i, seen)
		}
	}
	return nil
}

// observeBack observes step i of p for a rollback.
func (e *Engine) observeBack(p Plan, i int, clones map[string]bool) (Progress, []Image, error) {
	_, seen, err := e.observe(p, p.Steps[i], clones)
	if err != nil {
		return "", nil, err
	}
	return classifyBack(p, i, seen), seen, nil
}

// classifyBack classifies step i of p for a rollback, which walks the steps
// backwards. A path a plan replaces in place (docs/design-v2.md section 8)
// is touched by two steps, so when the earlier one has not run, or has been
// undone already, the later one sees the state before the earlier one: that
// is "not run" too, not foreign state. Every other object must be the step's
// own preimage for it.
func classifyBack(p Plan, i int, seen []Image) Progress {
	s := p.Steps[i]
	prog := classify(s, seen)
	if prog != Foreign {
		return prog
	}
	for k := range seen {
		if !seen[k].same(s.Pre[k]) && !beforeEarlierStep(p, i, seen[k]) {
			return Foreign
		}
	}
	return NotRun
}

// beforeEarlierStep reports whether img is how its object was before one of
// the steps of p ahead of step i.
func beforeEarlierStep(p Plan, i int, img Image) bool {
	for _, s := range p.Steps[:i] {
		for _, pre := range s.Pre {
			if pre.object() == img.object() && pre.same(img) {
				return true
			}
		}
	}
	return false
}

// dryRollback walks steps last..0 over the disk as the rollback would, and
// reports the first foreign object. It changes nothing.
func (e *Engine) dryRollback(p Plan, last int, clones map[string]bool) error {
	v := e.newPlanView(p.Project)
	cur := map[string]Image{}
	for i := last; i >= 0; i-- {
		s := p.Steps[i]
		seen := make([]Image, len(s.Pre))
		for k, want := range s.Pre {
			img, ok := cur[want.object()]
			if !ok {
				var err error
				if img, err = v.observe(want, clones[want.object()]); err != nil {
					return err
				}
			}
			seen[k] = img
		}
		switch classifyBack(p, i, seen) {
		case NotRun:
		case Done, halfway:
			for _, pre := range s.Pre {
				cur[pre.object()] = pre
			}
		default:
			return foreign(p, i, seen)
		}
	}
	return nil
}

// undoStep brings the objects of step i back from its postimage (or from
// half way) to its preimage.
func (e *Engine) undoStep(rec Record, snap Snapshot, i int, prog Progress) error {
	s := rec.Plan.Steps[i]
	switch s.Kind {
	case StepMove:
		if err := e.ensureParent(s.Src); err != nil {
			return err
		}
		return e.d.Links.RenameNoReplace(s.Dst, s.Src)
	case StepLink:
		return e.d.Links.RemoveLink(s.Path, s.Target)
	case StepUnlink:
		return e.d.Links.Symlink(s.Target, s.Path)
	case StepRemoveTree:
		t, ok := snap.treeCopy(s.Path)
		if !ok {
			return fmt.Errorf("%w: no copy of %s", ErrSnapshotNotFound, s.Path)
		}
		copyDir := filepath.Join(e.SnapshotDir(snap.SnapshotID), filepath.FromSlash(t.Copy))
		if rec.Plan.HasProjectScope() {
			return e.restoreProjectTree(copyDir, *rec.Plan.Project, t.TreeSHA, e.staging(rec, i))
		}
		return e.publish(copyDir, s.Path, t.TreeSHA, e.staging(rec, i))
	case StepRestoreTree:
		if rec.Plan.HasProjectScope() {
			return e.removeProjectTree(*rec.Plan.Project, e.trash(rec, i))
		}
		return e.d.Files.RemoveTree(s.Path)
	case StepRemoveClone:
		t, ok := snap.treeCopy(s.Path)
		if !ok || t.CloneSHA == "" {
			return fmt.Errorf("%w: no clone copy of %s", ErrSnapshotNotFound, s.Path)
		}
		copyDir := filepath.Join(e.SnapshotDir(snap.SnapshotID), filepath.FromSlash(t.Copy))
		return e.publishClone(copyDir, s.Path, t.CloneSHA, e.staging(rec, i))
	case StepRestoreClone:
		return e.removeTree(s.Path, e.trash(rec, i))
	case StepRequire:
		return nil // it changed nothing
	case StepLockPark, StepLockRemove:
		if s.Post[0].State == StateNoLock {
			// The lock held only this entry and is gone (or empty): adding
			// the entry back creates it as it was. Such an entry has no ref.
			return e.d.Lock.Add(s.Name, s.Raw)
		}
		return e.d.Lock.Unpark(takenEntry(rec, snap, i))
	case StepLockUnpark:
		_, err := e.d.Lock.Remove(s.Entry.Name, s.Post[0].Raw)
		return err
	case StepLockReplace:
		return e.d.Lock.RestoreEntry(s.Name, s.Pre[0].Raw)
	case StepLockAdd:
		if prog == Done {
			if _, err := e.d.Lock.Remove(s.Name, s.Post[0].Raw); err != nil {
				return err
			}
		}
		if s.Pre[0].State == StateNoLock {
			return e.dropLock()
		}
		return nil
	}
	return fmt.Errorf("%w: unknown kind %q", ErrInvalidStep, s.Kind)
}

// takenEntry is the entry step i took out of the lock, with the neighbours
// it had: as the lock store reported them when the step's progress was
// recorded, or else as the lock's order in the snapshot gives them after the
// plan's earlier lock steps.
func takenEntry(rec Record, snap Snapshot, i int) domain.ParkedEntry {
	if entry, ok := rec.Parked[i]; ok {
		entry.Raw = slices.Clone(entry.Raw)
		return entry
	}
	return neighbours(rec.Plan, snap, i)
}

// neighbours replays the plan's lock steps before step i over the snapshot's
// lock order, the way the lock store places entries, and returns step i's
// entry between its neighbours at that point.
func neighbours(p Plan, snap Snapshot, i int) domain.ParkedEntry {
	keys := slices.Clone(snap.LockKeys)
	for _, s := range p.Steps[:i] {
		switch s.Kind {
		case StepLockPark, StepLockRemove:
			keys = slices.DeleteFunc(keys, func(k string) bool { return k == s.Name })
		case StepLockAdd:
			keys = append(keys, s.Name)
		case StepLockUnpark:
			keys = insertAfter(keys, *s.Entry)
		}
	}
	s := p.Steps[i]
	entry := domain.ParkedEntry{Name: s.Name, Raw: slices.Clone(s.Pre[0].Raw), LockSHA: snap.LockSHA}
	if at := slices.Index(keys, s.Name); at >= 0 {
		if at > 0 {
			entry.Before = keys[at-1]
		}
		if at < len(keys)-1 {
			entry.After = keys[at+1]
		}
	}
	return entry
}

// insertAfter places entry as the lock store's Unpark does: after Before,
// or before After when it was first.
func insertAfter(keys []string, entry domain.ParkedEntry) []string {
	prev := entry.Before
	if prev == "" && entry.After != "" {
		if at := slices.Index(keys, entry.After); at > 0 {
			prev = keys[at-1]
		}
	}
	at := 0
	if prev != "" {
		at = slices.Index(keys, prev) + 1
	}
	return slices.Insert(keys, at, entry.Name)
}

// RecoverResult tells what Recover found and did.
type RecoverResult struct {
	// Found is true when a version 2 record was pending.
	Found bool
	// Committed is true when the operation had committed (its journal line
	// was written) and Recover finished its close.
	Committed bool
	// RolledBack is true when Recover undid the operation.
	RolledBack bool
	PlanID     string
	SnapshotID string
}

// Recover resolves a pending version 2 operation under the state lock. It
// reads the record and its snapshot and refuses an incoherent pair without
// touching anything. An operation whose journal line was written committed:
// Recover finishes its close. Any other is rolled back, step by step from
// what the disk shows; foreign state anywhere stops it before it changes
// anything, keeps both states and the record, and returns ForeignStateError.
// Running it again, or after a crash inside it, gives the same result.
//
// It only knows version 2 records; the version 1 operation stays with
// internal/updates.
func (e *Engine) Recover(ctx context.Context) (RecoverResult, error) {
	release, err := e.d.State.Lock(ctx)
	if err != nil {
		return RecoverResult{}, fmt.Errorf("take the state lock: %w", err)
	}
	res, err := e.recover()
	return res, errors.Join(err, release())
}

func (e *Engine) recover() (RecoverResult, error) {
	rec, pending, err := e.PendingRecord()
	if err != nil || !pending {
		return RecoverResult{}, err
	}
	if err := e.requireProjectGate(rec.Plan, "recover"); err != nil {
		return RecoverResult{}, err
	}
	res := RecoverResult{Found: true, PlanID: rec.PlanID, SnapshotID: rec.SnapshotID}
	snap, err := e.verifiedSnapshot(rec)
	if err != nil {
		return res, err
	}
	committed, err := e.journaled(rec.SnapshotID, commitAction(rec.Plan))
	if err != nil {
		return res, err
	}
	if committed {
		res.Committed = true
		return res, e.closeCommitted(rec, snap)
	}
	if err := e.rollbackSteps(rec, snap); err != nil {
		var fe *ForeignStateError
		if errors.As(err, &fe) {
			return res, e.stop(rec, fe)
		}
		return res, err
	}
	res.RolledBack = true
	return res, e.closeRolledBack(rec, snap, domain.ActionRecovered)
}
