package updates_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// applied stages v2, applies it and returns the candidate, the apply result
// and ~/.agents as it was before the apply.
func (f *fixture) applied() (domain.UpdateCandidate, updates.ApplyResult, map[string]string) {
	f.t.Helper()
	f.standard()
	c := f.staged()
	before := f.agentsTree()
	res, err := f.svc.Apply(context.Background(), approval(c))
	if err != nil {
		f.t.Fatalf("Apply: %v", err)
	}
	return c, res, before
}

func TestUndoRestoresExactly(t *testing.T) {
	f := newFixture(t)
	c, applied, before := f.applied()

	res, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}

	f.assertAgentsTree(before, "after undo")
	if res.SnapshotID != applied.SnapshotID || res.Changed || res.SavedSnapshotID != "" {
		t.Errorf("result = %+v", res)
	}
	j := f.journal()
	last := j[len(j)-1]
	if last.Action != domain.ActionUndone || last.Target != target || last.FromSHA != c.ToSHA ||
		last.ToSHA != c.FromSHA || last.SnapshotID != applied.SnapshotID {
		t.Errorf("journal = %+v", last)
	}
	if _, ok := f.operation(); ok {
		t.Error("operation record left after a finished undo")
	}
	snaps, _ := f.svc.Snapshots(target)
	if len(snaps) != 1 || snaps[0].ID != applied.SnapshotID {
		t.Errorf("snapshots = %+v, want only the apply's", snaps)
	}
	if _, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target}); !errors.Is(err, updates.ErrNothingToUndo) {
		t.Errorf("second undo: %v, want ErrNothingToUndo", err)
	}
}

func TestUndoOfAFolderChangedAfterTheApplySavesItFirstAndAsksAgain(t *testing.T) {
	f := newFixture(t)
	_, applied, before := f.applied()
	f.home.WriteFile(".agents/skills/alpha/notes.md", "edited after the apply\n")
	edited := hashOf(t, f.folder())
	afterEdit := f.agentsTree()

	res, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})

	if !errors.Is(err, updates.ErrUndoNeedsConfirmation) || !res.Changed || res.SavedSnapshotID == "" {
		t.Fatalf("result %+v, err %v; want a saved copy and a confirmation request", res, err)
	}
	f.assertAgentsTree(afterEdit, "after an unconfirmed undo")
	saved := findSnapshot(t, f, res.SavedSnapshotID)
	if !saved.Keep || saved.Cause != domain.SnapshotBeforeUndo || hashOf(t, saved.FolderCopyPath) != edited {
		t.Errorf("saved copy = %+v (holds %s, want %s)", saved, hashOf(t, saved.FolderCopyPath), edited)
	}

	res, err = f.svc.Undo(context.Background(), updates.UndoRequest{Target: target, Confirm: true})
	if err != nil {
		t.Fatalf("confirmed Undo: %v", err)
	}
	f.assertAgentsTree(before, "after a confirmed undo")
	if res.SnapshotID != applied.SnapshotID || !res.Changed {
		t.Errorf("result = %+v", res)
	}
	if got := findSnapshot(t, f, res.SavedSnapshotID); !got.Keep || hashOf(t, got.FolderCopyPath) != edited {
		t.Errorf("the edited folder's copy was not kept: %+v", got)
	}
}

func findSnapshot(t *testing.T, f *fixture, id string) domain.Snapshot {
	t.Helper()
	snaps, err := f.svc.Snapshots(target)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(snaps, func(s domain.Snapshot) bool { return s.ID == id })
	if i < 0 {
		t.Fatalf("snapshot %s not in %+v", id, snaps)
	}
	return snaps[i]
}

func TestUndoRefusesWhenTheLockEntryIsGone(t *testing.T) {
	f := newFixture(t)
	f.applied()
	f.writeLock()
	before := f.agentsTree()

	_, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})

	if !errors.Is(err, updates.ErrLockEntryChanged) {
		t.Fatalf("err = %v, want ErrLockEntryChanged", err)
	}
	f.assertAgentsTree(before, "after a refused undo")
}

func TestUndoFailureAtEveryStepLeavesTheStoreAndTheLockIdentical(t *testing.T) {
	succeeded := false
	for at := 1; !succeeded; at++ {
		f := newFixture(t)
		_, _, beforeApply := f.applied()
		before := f.agentsTree()
		f.trip.arm(at, false)

		_, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})

		ops := f.trip.ops
		f.trip.arm(0, false)
		if at > len(ops) {
			if err != nil {
				t.Fatalf("undo with no failure: %v", err)
			}
			succeeded = true
		}
		if err == nil {
			f.assertAgentsTree(beforeApply, "undo succeeded despite a failure")
			continue
		}
		step := ops[at-1]
		if !errors.Is(err, errInjected) {
			t.Fatalf("failure at call %d (%s): err %v", at, step, err)
		}
		f.assertAgentsTree(before, "failure at "+step)
		if op, ok := f.operation(); ok {
			t.Fatalf("failure at call %d (%s) left operation %+v", at, step, op)
		}
	}
}

func TestSkipHidesTheVersionUntilUpstreamMovesAgain(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	before := f.agentsTree()

	if err := f.svc.Skip(context.Background(), approval(c)); err != nil {
		t.Fatalf("Skip: %v", err)
	}

	f.assertAgentsTree(before, "after a skip")
	st := f.persisted()
	if got := f.candidate(c.ID).State; got != domain.CandidateSkipped {
		t.Errorf("candidate = %s, want skipped", got)
	}
	if len(st.Skips) != 1 || st.Skips[0].Target != target || st.Skips[0].ToSHA != c.ToSHA || !st.Skips[0].At.Equal(t0) {
		t.Errorf("skips = %+v", st.Skips)
	}
	if _, err := os.Stat(c.StagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging left behind: %v", err)
	}
	want := domain.HistoryEntry{
		At: t0, Action: domain.ActionSkipped, Target: target, FromSHA: c.FromSHA, ToSHA: c.ToSHA, Labels: c.Labels,
	}
	if j := f.journal(); len(j) != 1 || !reflect.DeepEqual(j[0], want) {
		t.Errorf("journal = %+v, want [%+v]", j, want)
	}
	report := f.check()
	if tc := targetResult(t, report, target); tc.Outcome != updates.CheckSkipped || len(report.NewCandidates) != 0 {
		t.Errorf("check after skip = %+v, new %v", tc, report.NewCandidates)
	}

	_, v3 := f.publish("main", v3Files)
	tc := targetResult(t, f.check(), target)
	if tc.Outcome != updates.CheckUpdateAvailable || f.candidate(tc.CandidateID).ToSHA != v3 {
		t.Errorf("check after upstream moved = %+v", tc)
	}
}

func TestSkipRejectsAnApprovalThatIsNoLongerTrue(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()

	err := f.svc.Skip(context.Background(), updates.Approval{CandidateID: c.ID, ToSHA: c.FromSHA})

	if !errors.Is(err, updates.ErrRejected) || !errors.Is(err, updates.ErrApprovalMismatch) {
		t.Fatalf("err = %v, want a rejected mismatch", err)
	}
	if got := f.candidate(c.ID).State; got != domain.CandidatePending {
		t.Errorf("candidate = %s, want pending", got)
	}
}
