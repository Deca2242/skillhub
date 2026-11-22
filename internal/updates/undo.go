package updates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// Skip marks a candidate skipped for its to_sha: the target shows no update
// until upstream publishes a different one. It changes no skills folder and
// not the lock.
func (s *Service) Skip(ctx context.Context, a Approval) error {
	release, err := s.d.State.Lock(ctx)
	if err != nil {
		return fmt.Errorf("take the state lock: %w", err)
	}
	defer release() //nolint:errcheck // releasing an advisory lock; the process exit releases it too.

	if err := s.GuardV2Operation(); err != nil {
		return err
	}
	if err := s.refuseInterrupted(); err != nil {
		return err
	}
	st, err := s.d.State.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	i := slices.IndexFunc(st.Candidates, func(c domain.UpdateCandidate) bool { return c.ID == a.CandidateID })
	if i < 0 {
		return rejected(ErrCandidateNotFound, "%s", a.CandidateID)
	}
	c := st.Candidates[i]
	if !isOpen(c) {
		return rejected(ErrNotPending, "%s is %s", c.ID, c.State)
	}
	if c.ToSHA != a.ToSHA {
		return rejected(ErrApprovalMismatch, "%s is %s, skipped %s", c.ID, c.ToSHA, a.ToSHA)
	}
	now := s.d.Clock.Now()
	st.Candidates[i].State = domain.CandidateSkipped
	st.Skips = slices.DeleteFunc(st.Skips, func(sk domain.Skip) bool { return sk.Target == c.Target })
	st.Skips = append(st.Skips, domain.Skip{Target: c.Target, ToSHA: c.ToSHA, At: now})
	// Nothing is left to do for the target until upstream moves again.
	skipped := domain.TargetResult{State: domain.StateUpToDate, CheckedAt: now, RemovedUpstream: st.Results[c.Target].RemovedUpstream}
	st.Results = withResult(st.Results, c.Target, &skipped)
	if err := s.d.State.Save(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	s.discardStaging(c)
	err = s.d.State.AppendJournal(domain.HistoryEntry{
		At: now, Action: domain.ActionSkipped, Target: c.Target, FromSHA: c.FromSHA, ToSHA: c.ToSHA,
		Labels: c.Labels, Detail: "",
	})
	if err != nil {
		return fmt.Errorf("journal the skip: %w", err)
	}
	return nil
}

// UndoRequest names the apply to undo.
type UndoRequest struct {
	// Target is the id of the skill, lock:<name>, or of a clone, git:<root>.
	Target string
	// SnapshotID picks an earlier apply; empty undoes the latest apply of
	// Target that was not undone yet.
	SnapshotID string
	// Confirm is the separate confirmation an undo needs when the folder
	// changed after the apply.
	Confirm bool
}

// UndoResult is what an undo did.
type UndoResult struct {
	Target string
	// SnapshotID is the snapshot restored.
	SnapshotID string
	// Changed is true when the folder changed after the apply.
	Changed bool
	// SavedSnapshotID is the copy of the changed folder, saved with keep set
	// before anything else happened.
	SavedSnapshotID string
	// Detail says what the lock kept when another tool changed its entry.
	Detail string
}

// Undo puts back the folder and the lock entry an apply replaced, with the
// same operation record as an apply. For a clone it puts back the whole copy,
// .git included, without running git. When the folder changed after the
// apply (for a clone: it is no longer clean, or HEAD's tree is not the
// applied one), it first saves a copy of the folder with keep set and,
// without Confirm, stops with ErrUndoNeedsConfirmation (docs/design.md
// section 7).
func (s *Service) Undo(ctx context.Context, req UndoRequest) (UndoResult, error) {
	res := UndoResult{Target: req.Target}
	release, err := s.d.State.Lock(ctx)
	if err != nil {
		return res, fmt.Errorf("take the state lock: %w", err)
	}
	defer release() //nolint:errcheck // releasing an advisory lock; the process exit releases it too.

	if err := s.GuardV2Operation(); err != nil {
		return res, err
	}
	if err := s.refuseInterrupted(); err != nil {
		return res, err
	}
	journal, err := s.d.State.Journal()
	if err != nil {
		return res, fmt.Errorf("read the journal: %w", err)
	}
	applied, err := undoable(journal, req)
	if err != nil {
		return res, err
	}
	snap, err := s.readSnapshot(applied.SnapshotID)
	if err != nil {
		return res, err
	}
	res.SnapshotID = snap.ID
	cur, err := s.undoFolder(ctx, snap, applied.ToSHA)
	if err != nil {
		return res, err
	}
	res.Changed = cur.changed

	// A copy of what is there now comes first: it is kept when the folder
	// changed after the apply, and it reverts a failed undo otherwise.
	now := s.d.Clock.Now()
	pre, reused, err := s.preUndoCopy(req.Target, snap.LockName, cur, res.Changed, now)
	if err != nil {
		return res, err
	}
	preID := pre.ID
	dropPre := func() {
		if !res.Changed && !reused {
			_ = s.d.Files.RemoveTree(s.snapshotDir(preID)) //nolint:errcheck // disk space only.
		}
	}
	if res.Changed {
		res.SavedSnapshotID = preID
		if !req.Confirm {
			return res, fmt.Errorf("%w: saved the current folder as %s", ErrUndoNeedsConfirmation, preID)
		}
	}

	// The undone target needs a new check. Forgetting its result first, before
	// the record, keeps the undo's own steps as they were; a failed undo only
	// leaves the target unchecked.
	if err := s.forgetResult(req.Target); err != nil {
		dropPre()
		return res, err
	}

	op := domain.Operation{
		ID: "undo-" + preID, Kind: domain.OperationUndo, Target: req.Target,
		RestoreSnapshotID: snap.ID, StartedAt: now,
	}
	if err := s.d.State.RecordOperation(op); err != nil {
		dropPre()
		return res, fmt.Errorf("record the operation: %w", err)
	}
	journaled := false
	note, err := s.restore(snap, nil)
	if err == nil {
		res.Detail = note
		err = s.d.State.AppendJournal(domain.HistoryEntry{
			At: now, Action: domain.ActionUndone, Target: req.Target, FromSHA: cur.tree, ToSHA: snap.contentSHA(),
			SnapshotID: snap.ID, Labels: []domain.Label{}, Detail: note,
		})
		journaled = err == nil
	}
	if err == nil {
		err = s.d.State.ClearOperation()
	}
	if err != nil {
		// Once "undone" is journaled, the failure names the snapshot so the
		// apply is offered to undo again.
		reopen := ""
		if journaled {
			reopen = snap.ID
		}
		return res, s.revertUndo(op, pre, now, reopen, err, dropPre)
	}
	dropPre()
	s.prune(req.Target)
	return res, nil
}

// undoState is what an undo finds installed.
type undoState struct {
	folder string
	// entry is the lock entry's exact JSON; nil for a clone.
	entry []byte
	// sha is the folder's content as a snapshot records it (folderSHA), and
	// tree the tree SHA the journal shows.
	sha, tree string
	// changed is true when the folder changed after the apply.
	changed bool
}

// undoFolder reads what an undo of snap would replace and checks that it is
// still the folder the apply changed.
func (s *Service) undoFolder(ctx context.Context, snap snapshotRecord, appliedSHA string) (undoState, error) {
	if isCloneTarget(snap.Target) {
		if err := s.checkCloneRoot(snap.FolderPath); err != nil {
			return undoState{}, err
		}
		sha, err := s.cloneSHA(snap.FolderPath)
		if err != nil {
			return undoState{}, err
		}
		tree, changed := s.cloneUndoState(ctx, snap.FolderPath, appliedSHA)
		return undoState{folder: snap.FolderPath, sha: sha, tree: tree, changed: changed}, nil
	}
	folder, err := s.storeFolder(snap.LockName)
	if err != nil {
		return undoState{}, err
	}
	if folder != snap.FolderPath {
		return undoState{}, fmt.Errorf("%w: %s now installs to %s, not %s", ErrNotUpdatable, snap.LockName, folder, snap.FolderPath)
	}
	lock, err := s.d.Lock.Read()
	if err != nil {
		return undoState{}, fmt.Errorf("read the skills lock: %w", err)
	}
	entry, ok := findEntry(lock, snap.LockName)
	if !ok || entry.Ref != rawRef(snap.LockEntryBefore) {
		return undoState{}, fmt.Errorf("%w: %s was removed or its ref changed", ErrLockEntryChanged, snap.LockName)
	}
	installed, err := s.d.HashDir(folder)
	if err != nil {
		return undoState{}, fmt.Errorf("hash the installed folder: %w", err)
	}
	return undoState{
		folder: folder, entry: entry.Raw, sha: installed.SHA, tree: installed.SHA, changed: installed.SHA != appliedSHA,
	}, nil
}

// preUndoCopy saves what is installed before an undo changes it. When the
// folder changed after the apply, the copy is kept; a kept copy of the same
// content and lock entry is reused rather than saved twice.
func (s *Service) preUndoCopy(target, name string, cur undoState, keep bool, now time.Time) (snapshotRecord, bool, error) {
	if keep {
		recs, err := s.snapshots(target)
		if err != nil {
			return snapshotRecord{}, false, err
		}
		for _, r := range recs {
			if r.Keep && r.FolderSHA == cur.sha && r.FolderPath == cur.folder && bytes.Equal(r.LockEntryBefore, cur.entry) {
				return r, true, nil
			}
		}
	}
	id, err := s.newSnapshotID(now, target)
	if err != nil {
		return snapshotRecord{}, false, err
	}
	rec := snapshotRecord{
		Snapshot: domain.Snapshot{
			ID: id, Target: target, TakenAt: now, LockEntryBefore: cur.entry,
			Cause: domain.SnapshotBeforeUndo, Keep: keep,
		},
		LockName: name, FolderPath: cur.folder, FolderSHA: cur.sha,
	}
	if isCloneTarget(target) {
		rec.TreeSHA = cur.tree
		if rec.TreeSHA == "" {
			// A clone git cannot describe: its copy still has to be valid.
			rec.TreeSHA = "unknown"
		}
	}
	rec, err = s.takeSnapshot(rec)
	return rec, false, err
}

// revertUndo puts back what was there before a failed undo. When that fails
// too, the record stays and Recover finishes the undo.
func (s *Service) revertUndo(op domain.Operation, pre snapshotRecord, now time.Time, reopen string, cause error, dropPre func()) error {
	if _, err := s.restore(pre, nil); err != nil {
		return errors.Join(cause, fmt.Errorf("revert from snapshot %s failed; run recover: %w", pre.ID, err))
	}
	if err := s.d.State.ClearOperation(); err != nil {
		return errors.Join(cause, fmt.Errorf("clear the operation record: %w", err))
	}
	dropPre()
	err := s.d.State.AppendJournal(domain.HistoryEntry{
		At: now, Action: domain.ActionFailed, Target: op.Target, SnapshotID: reopen,
		Labels: []domain.Label{}, Detail: fmt.Sprintf("undo of %s: %v", op.RestoreSnapshotID, cause),
	})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("journal the failure: %w", err))
	}
	return cause
}

// undoable finds the apply an undo restores: the latest applied entry of the
// target whose snapshot is still open, or the one req names. The journal is
// replayed oldest first: "applied" opens a snapshot; "undone" and
// "recovered" close it; a "failed" line that names a snapshot reopens it
// when it follows "undone" (a reverted undo) and closes it otherwise (a
// reverted apply).
func undoable(journal []domain.HistoryEntry, req UndoRequest) (domain.HistoryEntry, error) {
	open := map[string]bool{}
	undone := map[string]bool{}
	var applies []domain.HistoryEntry
	for _, e := range journal {
		if e.Target != req.Target || e.SnapshotID == "" {
			continue
		}
		id := e.SnapshotID
		switch e.Action {
		case domain.ActionApplied:
			open[id], undone[id] = true, false
			applies = append(applies, e)
		case domain.ActionUndone:
			open[id], undone[id] = false, true
		case domain.ActionRecovered:
			open[id], undone[id] = false, false
		case domain.ActionFailed:
			open[id], undone[id] = undone[id], false
		}
	}
	for i := len(applies) - 1; i >= 0; i-- {
		e := applies[i]
		if open[e.SnapshotID] && (req.SnapshotID == "" || e.SnapshotID == req.SnapshotID) {
			return e, nil
		}
	}
	if req.SnapshotID != "" {
		return domain.HistoryEntry{}, fmt.Errorf("%s of %s: %w", req.SnapshotID, req.Target, ErrSnapshotNotFound)
	}
	return domain.HistoryEntry{}, fmt.Errorf("%s: %w", req.Target, ErrNothingToUndo)
}

// Interrupted returns the operation that did not finish, if any.
func (s *Service) Interrupted() (domain.Operation, bool, error) {
	return s.d.State.Operation()
}

// RecoverResult is what a recover did.
type RecoverResult struct {
	Operation domain.Operation
	// Restored is false when the snapshot never completed: the store did not
	// change, and only leftovers were removed.
	Restored bool
	// Detail says what the lock kept when another tool changed its entry.
	Detail string
	// IncompleteSnapshots is how many incomplete snapshots a recover with
	// no operation recorded removed: the only leftovers it then finds.
	IncompleteSnapshots int
}

// Recover resolves an interrupted apply or undo, only ever as a user action:
// it puts back the folder and the lock entry from the operation's snapshot,
// removes the leftovers, journals recovered and clears the record. After an
// apply, the candidate is pending again (docs/design.md section 7,
// "Interrupciones y recuperación").
func (s *Service) Recover(ctx context.Context) (RecoverResult, error) {
	release, err := s.d.State.Lock(ctx)
	if err != nil {
		return RecoverResult{}, fmt.Errorf("take the state lock: %w", err)
	}
	defer release() //nolint:errcheck // releasing an advisory lock; the process exit releases it too.

	if err := s.GuardV2Operation(); err != nil {
		return RecoverResult{}, err
	}
	op, ok, err := s.d.State.Operation()
	if err != nil {
		return RecoverResult{}, fmt.Errorf("read the operation in progress: %w", err)
	}
	if !ok {
		// An undo interrupted while it copied the folder leaves an incomplete
		// snapshot and no record, since nothing had changed yet.
		removed, err := s.removeIncompleteSnapshots()
		if err != nil {
			return RecoverResult{}, err
		}
		if removed == 0 {
			return RecoverResult{}, ErrNoOperation
		}
		return RecoverResult{IncompleteSnapshots: removed, Detail: fmt.Sprintf("removed %d incomplete snapshots", removed)}, nil
	}
	res := RecoverResult{Operation: op}
	var toSHA string
	snap, err := s.readSnapshot(op.RestoreSnapshotID)
	switch {
	case errors.Is(err, ErrSnapshotNotFound) && op.Kind == domain.OperationApply:
		// The snapshot never completed, so the store never changed.
		err := s.d.Files.RemoveTree(s.snapshotDir(op.RestoreSnapshotID))
		if tmp := s.tmpRootOf(op.Target); tmp != "" {
			err = errors.Join(err, s.d.Files.RemoveTree(tmp))
		}
		if err != nil {
			return res, fmt.Errorf("remove leftovers: %w", err)
		}
	case err != nil:
		return res, err
	default:
		ours, err := s.recoverGuard(op)
		if err != nil {
			return res, err
		}
		if res.Detail, err = s.restore(snap, ours); err != nil {
			return res, err
		}
		res.Restored, toSHA = true, snap.contentSHA()
	}
	if op.Kind == domain.OperationApply {
		if err := s.reopenCandidate(op.CandidateID); err != nil {
			return res, err
		}
	}
	err = s.d.State.AppendJournal(domain.HistoryEntry{
		At: s.d.Clock.Now(), Action: domain.ActionRecovered, Target: op.Target, ToSHA: toSHA,
		SnapshotID: op.RestoreSnapshotID, Labels: []domain.Label{}, Detail: res.Detail,
	})
	if err != nil {
		return res, fmt.Errorf("journal the recovery: %w", err)
	}
	if err := s.d.State.ClearOperation(); err != nil {
		return res, fmt.Errorf("clear the operation record: %w", err)
	}
	if _, err := s.removeIncompleteSnapshots(); err != nil {
		return res, err
	}
	return res, nil
}

// removeIncompleteSnapshots removes every snapshot directory without
// metadata: a copy that was interrupted before it completed. Only Recover
// calls it, under the state lock, so no snapshot is being written.
func (s *Service) removeIncompleteSnapshots() (int, error) {
	entries, err := s.d.Dirs.Scan(s.d.Paths.SnapshotsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list snapshots: %w", err)
	}
	removed := 0
	for _, e := range entries {
		if e.Kind != ports.EntryDir {
			continue
		}
		complete, err := s.d.Files.Exists(filepath.Join(e.Path, snapshotMeta))
		if err != nil {
			return removed, fmt.Errorf("look for %s: %w", e.Path, err)
		}
		if complete {
			continue
		}
		if err := s.d.Files.RemoveTree(e.Path); err != nil {
			return removed, fmt.Errorf("remove the incomplete snapshot %s: %w", e.Name, err)
		}
		removed++
	}
	return removed, nil
}

// recoverGuard accepts the lock entries Recover may put back: after an
// apply, only one the apply wrote; after an undo, any, since the user asked
// for that entry.
func (s *Service) recoverGuard(op domain.Operation) (func(domain.LockEntry) bool, error) {
	if op.Kind != domain.OperationApply {
		return nil, nil
	}
	st, err := s.d.State.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	i := slices.IndexFunc(st.Candidates, func(c domain.UpdateCandidate) bool { return c.ID == op.CandidateID })
	if i < 0 {
		return nil, nil
	}
	return appliedBy(st.Candidates[i]), nil
}

// reopenCandidate makes an interrupted apply's candidate pending again.
func (s *Service) reopenCandidate(id string) error {
	st, err := s.d.State.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	i := slices.IndexFunc(st.Candidates, func(c domain.UpdateCandidate) bool { return c.ID == id })
	if i < 0 || st.Candidates[i].State != domain.CandidateApplied {
		return nil
	}
	st.Candidates[i].State = domain.CandidatePending
	// The folder is back to what it was before the apply, which needs a new
	// check; the pending candidate shows the update meanwhile.
	st.Results = withResult(st.Results, st.Candidates[i].Target, nil)
	if err := s.d.State.Save(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// forgetResult removes the check result of target; it saves the state only
// when there was one.
func (s *Service) forgetResult(target string) error {
	st, err := s.d.State.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if _, ok := st.Results[target]; !ok {
		return nil
	}
	st.Results = withResult(st.Results, target, nil)
	if err := s.d.State.Save(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}
