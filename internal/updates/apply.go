package updates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// applier is a revalidated apply: everything it may change was checked under
// the state lock against what the user approved. plan applies a lock skill,
// clonePlan a git clone.
type applier interface {
	target() domain.UpdateCandidate
	// snapshot copies what the apply replaces (step 7).
	snapshot(id string, now time.Time) (snapshotRecord, error)
	// change applies and verifies (steps 8 and 9).
	change(now time.Time) error
}

// plan is a revalidated apply of a lock skill.
type plan struct {
	s         *Service
	candidate domain.UpdateCandidate
	name      string
	folder    string
	// lock and entry are the lock as read by the revalidation.
	lock  domain.SkillsLock
	entry domain.LockEntry
}

// Apply applies one approved candidate (docs/design.md section 7, steps 6 to
// 10). It returns a nil error only for OutcomeApplied. A rejection changes
// nothing; a failure is reverted from the snapshot in the same run, and only
// when that revert fails too is the operation left for Recover.
func (s *Service) Apply(ctx context.Context, a Approval) (ApplyResult, error) {
	res := ApplyResult{CandidateID: a.CandidateID}
	finish := func(err error) (ApplyResult, error) {
		res.Err = err
		switch {
		case err == nil:
			res.Outcome = OutcomeApplied
		case errors.Is(err, ErrRejected), errors.Is(err, ErrInterrupted), errors.Is(err, ErrV2OperationPending):
			res.Outcome = OutcomeRejected
		default:
			res.Outcome = OutcomeFailed
		}
		return res, err
	}
	release, err := s.d.State.Lock(ctx)
	if err != nil {
		return finish(fmt.Errorf("take the state lock: %w", err))
	}
	defer release() //nolint:errcheck // releasing an advisory lock; the process exit releases it too.

	if err := s.GuardV2Operation(); err != nil {
		return finish(err)
	}
	if err := s.refuseInterrupted(); err != nil {
		return finish(err)
	}
	st, err := s.d.State.Load()
	if err != nil {
		return finish(fmt.Errorf("load state: %w", err))
	}
	i := slices.IndexFunc(st.Candidates, func(c domain.UpdateCandidate) bool { return c.ID == a.CandidateID })
	if i < 0 {
		return finish(rejected(ErrCandidateNotFound, "%s", a.CandidateID))
	}
	c := st.Candidates[i]
	res.Target = c.Target
	var ap applier
	if isCloneTarget(c.Target) {
		p, err := s.revalidateClone(ctx, c, a)
		if err != nil {
			return finish(err)
		}
		ap = p
	} else {
		p, err := s.revalidate(c, a)
		if err != nil {
			return finish(err)
		}
		ap = p
	}
	res.SnapshotID, err = s.apply(&st, i, ap)
	return finish(err)
}

// checkApproval checks that the approval names the candidate as it is: still
// pending, with the approved to_sha.
func checkApproval(c domain.UpdateCandidate, a Approval) error {
	if c.State != domain.CandidatePending {
		return rejected(ErrNotPending, "%s is %s", c.ID, c.State)
	}
	if c.ToSHA != a.ToSHA {
		return rejected(ErrApprovalMismatch, "%s is %s, approved %s", c.ID, c.ToSHA, a.ToSHA)
	}
	return nil
}

// checkStaged checks that the candidate's staged folder is in the staging
// directory and still hashes to to_sha.
func (s *Service) checkStaged(c domain.UpdateCandidate) error {
	if filepath.Dir(c.StagedPath) != s.d.Paths.StagingDir() {
		return rejected(ErrStagedChanged, "%s is not a staging folder", c.StagedPath)
	}
	if staged, err := s.d.HashDir(c.StagedPath); err != nil || staged.SHA != c.ToSHA {
		return rejected(ErrStagedChanged, "%s does not hash to %s", c.StagedPath, c.ToSHA)
	}
	return nil
}

// refuseInterrupted returns ErrInterrupted while an operation is recorded.
func (s *Service) refuseInterrupted() error {
	op, ok, err := s.d.State.Operation()
	if err != nil {
		return fmt.Errorf("read the operation in progress: %w", err)
	}
	if ok {
		return fmt.Errorf("%w: %s of %s", ErrInterrupted, op.Kind, op.Target)
	}
	return nil
}

// revalidate checks, before anything changes, that the approval still
// describes the truth (docs/design.md section 7, step 6).
func (s *Service) revalidate(c domain.UpdateCandidate, a Approval) (plan, error) {
	if err := checkApproval(c, a); err != nil {
		return plan{}, err
	}
	name, ok := lockName(c.Target)
	if !ok {
		return plan{}, rejected(ErrNotUpdatable, "%s", c.Target)
	}
	folder, err := s.storeFolder(name)
	if errors.Is(err, ErrNotUpdatable) {
		return plan{}, rejected(err, "%s", c.Target)
	}
	if err != nil {
		return plan{}, err
	}
	installed, err := s.d.HashDir(folder)
	if err != nil {
		return plan{}, fmt.Errorf("hash the installed folder: %w", err)
	}
	if installed.SHA != c.InstalledSHAAtStage {
		return plan{}, rejected(ErrInstalledChanged, "%s is %s, staged at %s", folder, installed.SHA, c.InstalledSHAAtStage)
	}
	if err := s.checkStaged(c); err != nil {
		return plan{}, err
	}
	lock, err := s.d.Lock.Read()
	if err != nil {
		return plan{}, fmt.Errorf("read the skills lock: %w", err)
	}
	if lock.Version != supportedLockVersion {
		return plan{}, rejected(ports.ErrUnsupportedLockVersion, "the skills lock is version %d", lock.Version)
	}
	entry, ok := findEntry(lock, name)
	if !ok || entry.SkillFolderHash != c.FromSHA {
		return plan{}, rejected(ErrLockEntryChanged, "%s no longer records %s", name, c.FromSHA)
	}
	meta, err := s.stagedMeta(c)
	if errors.Is(err, errNoStagedRef) {
		return plan{}, rejected(ErrStagedChanged, "%s: %v", c.ID, err)
	}
	if err != nil {
		return plan{}, err
	}
	if entry.Ref != meta.Ref {
		return plan{}, rejected(ErrLockEntryChanged, "%s has ref %q, staged at %q", name, entry.Ref, meta.Ref)
	}
	return plan{s: s, candidate: c, name: name, folder: folder, lock: lock, entry: entry}, nil
}

func (p plan) target() domain.UpdateCandidate { return p.candidate }

// snapshot copies the installed folder and the lock entry's exact JSON. The
// snapshot of a locally modified folder is kept.
func (p plan) snapshot(id string, now time.Time) (snapshotRecord, error) {
	c := p.candidate
	return p.s.takeSnapshot(snapshotRecord{
		Snapshot: domain.Snapshot{
			ID: id, Target: c.Target, TakenAt: now, LockEntryBefore: p.entry.Raw,
			Cause: domain.SnapshotBeforeApply, Keep: c.InstalledSHAAtStage != c.FromSHA,
		},
		LockName: p.name, FolderPath: p.folder, FolderSHA: c.InstalledSHAAtStage,
	})
}

func (p plan) change(now time.Time) error {
	if err := p.s.replace(p, now); err != nil {
		return err
	}
	return p.s.verify(p)
}

// apply runs steps 7 to 10 and returns the snapshot id.
func (s *Service) apply(st *domain.PersistedState, i int, ap applier) (string, error) {
	c := ap.target()
	now := s.d.Clock.Now()
	snapID, err := s.newSnapshotID(now, c.Target)
	if err != nil {
		return "", err
	}
	op := domain.Operation{
		ID: "apply-" + snapID, Kind: domain.OperationApply, Target: c.Target,
		CandidateID: c.ID, RestoreSnapshotID: snapID, StartedAt: now,
	}
	// Step 7: the operation record goes first; until the snapshot is whole,
	// the store does not change.
	if err := s.d.State.RecordOperation(op); err != nil {
		return "", fmt.Errorf("record the operation: %w", err)
	}
	snap, err := ap.snapshot(snapID, now)
	if err != nil {
		return "", s.abandon(op, c, now, err)
	}

	// Steps 8 and 9; any failure, including saving the outcome, reverts.
	// The target is up to date with the candidate it now runs; a revert puts
	// the previous result back.
	prevResults := st.Results
	err = ap.change(now)
	if err == nil {
		st.Candidates[i].State = domain.CandidateApplied
		st.Results = withResult(st.Results, c.Target, &domain.TargetResult{State: domain.StateUpToDate, CheckedAt: now})
		err = s.d.State.Save(*st)
	}
	if err == nil {
		err = s.d.State.AppendJournal(domain.HistoryEntry{
			At: now, Action: domain.ActionApplied, Target: c.Target, FromSHA: c.FromSHA, ToSHA: c.ToSHA,
			SnapshotID: snapID, Labels: c.Labels, Detail: "",
		})
	}
	// Step 10: clearing the record closes the apply.
	if err == nil {
		err = s.d.State.ClearOperation()
	}
	if err != nil {
		st.Candidates[i].State = domain.CandidatePending
		if errors.Is(err, ErrNotFastForward) {
			// The fetch proved that HEAD is not an ancestor of the approved
			// commit: the clone has local commits or upstream rewrote its
			// history. Offering the candidate again would fail again.
			st.Candidates[i].State, st.Candidates[i].BlockReason = domain.CandidateBlocked, domain.BlockLocalCommits
		}
		st.Results = prevResults
		return "", s.revert(op, snap, *st, c, now, err)
	}
	s.discardStaging(c)
	s.prune(c.Target)
	return snapID, nil
}

// replace swaps the staged copy in for the installed folder and rewrites the
// lock entry (step 8).
func (s *Service) replace(p plan, now time.Time) error {
	files := s.d.Files
	c := p.candidate
	tmp := s.tmpRoot()
	next := filepath.Join(tmp, p.name)
	old := filepath.Join(tmp, p.name+".old")
	if err := files.RemoveTree(tmp); err != nil {
		return fmt.Errorf("clear %s: %w", tmp, err)
	}
	if err := files.CopyTree(c.StagedPath, next); err != nil {
		return fmt.Errorf("copy the staged folder: %w", err)
	}
	if err := s.lockStable(p); err != nil {
		return err
	}
	if err := files.Rename(p.folder, old); err != nil {
		return fmt.Errorf("move the installed folder aside: %w", err)
	}
	if err := files.Rename(next, p.folder); err != nil {
		return fmt.Errorf("put the new folder in place: %w", err)
	}
	err := s.d.Lock.UpdateEntry(p.name, ports.LockEntryUpdate{FolderHash: c.ToSHA, UpdatedAt: now, Commit: c.ToCommit})
	if err != nil {
		return fmt.Errorf("update the lock entry: %w", err)
	}
	if err := files.RemoveTree(tmp); err != nil {
		return fmt.Errorf("clear %s: %w", tmp, err)
	}
	return nil
}

// lockStable reads the lock again right before the swap and compares it
// with the revalidation's read. npx skills rewrites the whole lock with no
// lock of its own: a change to another entry is retried once, and a change
// to this entry, or a second change, fails (docs/design.md section 7, "npx
// skills al mismo tiempo").
func (s *Service) lockStable(p plan) error {
	before := p.lock
	for range 2 {
		now, err := s.d.Lock.Read()
		if err != nil {
			return fmt.Errorf("read the skills lock again: %w", err)
		}
		if now.Revision == before.Revision {
			return nil
		}
		entry, ok := findEntry(now, p.name)
		if !ok || !bytes.Equal(entry.Raw, p.entry.Raw) {
			return fmt.Errorf("%w: another tool rewrote %s", ErrLockEntryChanged, p.name)
		}
		before = now
	}
	return fmt.Errorf("%w: %s", ErrLockConcurrent, p.name)
}

// verify checks the installed folder and the lock entry against the
// candidate (step 9).
func (s *Service) verify(p plan) error {
	c := p.candidate
	tree, err := s.d.HashDir(p.folder)
	if err != nil {
		return fmt.Errorf("%w: hash the installed folder: %w", ErrVerifyFailed, err)
	}
	if tree.SHA != c.ToSHA {
		return fmt.Errorf("%w: installed %s, want %s", ErrVerifyFailed, tree.SHA, c.ToSHA)
	}
	lock, err := s.d.Lock.Read()
	if err != nil {
		return fmt.Errorf("%w: read the skills lock: %w", ErrVerifyFailed, err)
	}
	if entry, ok := findEntry(lock, p.name); !ok || entry.SkillFolderHash != c.ToSHA {
		return fmt.Errorf("%w: the lock entry does not record %s", ErrVerifyFailed, c.ToSHA)
	}
	return nil
}

// abandon closes an apply that failed before the store changed: it clears
// the operation record and journals the failure.
func (s *Service) abandon(op domain.Operation, c domain.UpdateCandidate, now time.Time, cause error) error {
	if err := s.d.State.ClearOperation(); err != nil {
		return errors.Join(cause, fmt.Errorf("clear the operation record: %w", err))
	}
	return errors.Join(cause, s.journalFailure(op, c, now, "", cause))
}

// revert puts the snapshot back after a failed apply, then closes the
// operation. When the revert fails, the record stays for Recover.
func (s *Service) revert(op domain.Operation, snap snapshotRecord, st domain.PersistedState, c domain.UpdateCandidate, now time.Time, cause error) error {
	if _, err := s.restore(snap, appliedBy(c)); err != nil {
		return errors.Join(cause, fmt.Errorf("revert from snapshot %s failed; run recover: %w", snap.ID, err))
	}
	// The candidate stays pending; the outcome may already have been saved.
	if err := s.d.State.Save(st); err != nil {
		return errors.Join(cause, fmt.Errorf("save state: %w", err))
	}
	if err := s.d.State.ClearOperation(); err != nil {
		return errors.Join(cause, fmt.Errorf("clear the operation record: %w", err))
	}
	// The store is back; the snapshot of an apply that did not happen is
	// not something to undo.
	_ = s.d.Files.RemoveTree(s.snapshotDir(snap.ID)) //nolint:errcheck // disk space only.
	// The failure names the snapshot, so an "applied" line journaled before
	// the revert is not offered to undo.
	return errors.Join(cause, s.journalFailure(op, c, now, snap.ID, cause))
}

// appliedBy accepts a lock entry an apply of c wrote, so reverting it never
// undoes what another tool wrote meanwhile.
func appliedBy(c domain.UpdateCandidate) func(domain.LockEntry) bool {
	return func(e domain.LockEntry) bool { return e.SkillFolderHash == c.ToSHA }
}

func (s *Service) journalFailure(op domain.Operation, c domain.UpdateCandidate, now time.Time, snapID string, cause error) error {
	err := s.d.State.AppendJournal(domain.HistoryEntry{
		At: now, Action: domain.ActionFailed, Target: op.Target, FromSHA: c.FromSHA, ToSHA: c.ToSHA,
		SnapshotID: snapID, Labels: c.Labels, Detail: cause.Error(),
	})
	if err != nil {
		return fmt.Errorf("journal the failure: %w", err)
	}
	return nil
}

// ApplyAll applies the approved candidates one by one, each with its own
// revalidation, snapshot and verification. A rejected or failed candidate
// neither stops nor reverts the others. A locally modified candidate is left
// out: it is applied one by one (docs/design.md section 7).
func (s *Service) ApplyAll(ctx context.Context, approvals []Approval) []ApplyResult {
	out := make([]ApplyResult, 0, len(approvals))
	for _, a := range approvals {
		if c, ok := s.candidateByID(a.CandidateID); ok && slices.Contains(c.Labels, domain.LabelLocallyModified) {
			out = append(out, ApplyResult{
				Target: c.Target, CandidateID: c.ID, Outcome: OutcomeExcluded, Err: ErrLocallyModifiedExcluded,
			})
			continue
		}
		res, _ := s.Apply(ctx, a) //nolint:errcheck // the error is res.Err, reported per target.
		out = append(out, res)
	}
	return out
}

func (s *Service) candidateByID(id string) (domain.UpdateCandidate, bool) {
	st, err := s.d.State.Load()
	if err != nil {
		return domain.UpdateCandidate{}, false
	}
	i := slices.IndexFunc(st.Candidates, func(c domain.UpdateCandidate) bool { return c.ID == id })
	if i < 0 {
		return domain.UpdateCandidate{}, false
	}
	return st.Candidates[i], true
}
