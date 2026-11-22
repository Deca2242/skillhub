package updates_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// interrupted runs action and reports whether the tripwire killed it. A
// panic that is not an interruption is re-raised.
func interrupted(action func()) (killed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(interruption); !ok {
				panic(r)
			}
			killed = true
		}
	}()
	action()
	return false
}

// assertNothingRepairsItself checks that, after an interruption, every
// action but Recover refuses or leaves the record and the store alone.
func assertNothingRepairsItself(t *testing.T, f *fixture, c domain.UpdateCandidate, step string) {
	t.Helper()
	ctx := context.Background()
	leftover := f.agentsTree()
	op, _ := f.operation()
	if _, err := f.svc.Apply(ctx, approval(c)); !errors.Is(err, updates.ErrInterrupted) {
		t.Fatalf("%s: Apply = %v, want ErrInterrupted", step, err)
	}
	if err := f.svc.Skip(ctx, approval(c)); !errors.Is(err, updates.ErrInterrupted) {
		t.Fatalf("%s: Skip = %v, want ErrInterrupted", step, err)
	}
	if _, err := f.svc.Undo(ctx, updates.UndoRequest{Target: target}); !errors.Is(err, updates.ErrInterrupted) {
		t.Fatalf("%s: Undo = %v, want ErrInterrupted", step, err)
	}
	report := f.check()
	if tc := targetResult(t, report, target); tc.Outcome != updates.CheckNotChecked || !errors.Is(tc.Err, updates.ErrInterrupted) {
		t.Fatalf("%s: check result %+v, want not_checked", step, tc)
	}
	f.assertAgentsTree(leftover, step+": after the refused actions and a check")
	if got, ok := f.operation(); !ok || got != op {
		t.Fatalf("%s: operation = %+v, %v; want %+v kept", step, got, ok, op)
	}
	if got, ok, err := f.svc.Interrupted(); err != nil || !ok || got != op {
		t.Fatalf("%s: Interrupted = %+v, %v, %v", step, got, ok, err)
	}
}

func TestApplyInterruptedAtEveryStepIsRecoveredExactly(t *testing.T) {
	recovered, finished := 0, false
	for at := 1; !finished; at++ {
		f := newFixture(t)
		f.standard()
		c := f.staged()
		before := f.agentsTree()
		lockBefore := f.lockBytes()
		f.trip.arm(at, true)

		killed := interrupted(func() { _, _ = f.svc.Apply(context.Background(), approval(c)) })

		step := "end"
		if killed {
			step = f.trip.ops[at-1]
		}
		f.reopen()
		if !killed {
			finished = true
		}
		op, ok := f.operation()
		if !ok {
			// Killed before the record, or after the apply closed.
			if got := f.agentsTree(); !maps.Equal(got, before) && hashOf(t, f.folder()) != c.ToSHA {
				t.Fatalf("interrupted at %d (%s) with no record and a store that is neither version", at, step)
			}
			continue
		}
		if op.Kind != domain.OperationApply || op.Target != target || op.CandidateID != c.ID {
			t.Fatalf("interrupted at %d (%s): record %+v", at, step, op)
		}
		assertNothingRepairsItself(t, f, c, step)

		res, err := f.svc.Recover(context.Background())
		if err != nil {
			t.Fatalf("interrupted at %d (%s): Recover: %v", at, step, err)
		}
		recovered++
		f.assertAgentsTree(before, "recover after "+step)
		if got := f.lockBytes(); got != lockBefore {
			t.Fatalf("recover after %s: lock differs", step)
		}
		if _, ok := f.operation(); ok {
			t.Fatalf("recover after %s left the record", step)
		}
		if res.Operation != op {
			t.Errorf("recover after %s: result %+v", step, res)
		}
		if got := f.candidate(c.ID).State; got != domain.CandidatePending {
			t.Fatalf("recover after %s: candidate %s, want pending", step, got)
		}
		j := f.journal()
		if last := j[len(j)-1]; last.Action != domain.ActionRecovered || last.Target != target || last.SnapshotID != op.RestoreSnapshotID {
			t.Fatalf("recover after %s: journal %+v", step, last)
		}
		// The candidate applies again after a recovery.
		if _, err := f.svc.Apply(context.Background(), approval(c)); err != nil {
			t.Fatalf("recover after %s: apply again: %v", step, err)
		}
	}
	if recovered == 0 {
		t.Fatal("no interruption left a record to recover")
	}
	t.Logf("%d interruption points left a record and were recovered", recovered)
}

func TestUndoInterruptedAtEveryStepIsRecoveredToTheVersionItWasRestoring(t *testing.T) {
	recovered, finished := 0, false
	for at := 1; !finished; at++ {
		f := newFixture(t)
		c, applied, beforeApply := f.applied()
		afterApply := f.agentsTree()
		f.trip.arm(at, true)

		killed := interrupted(func() { _, _ = f.svc.Undo(context.Background(), updates.UndoRequest{Target: target}) })

		step := "end"
		if killed {
			step = f.trip.ops[at-1]
		}
		f.reopen()
		if !killed {
			finished = true
		}
		op, ok := f.operation()
		if !ok {
			got := f.agentsTree()
			if !maps.Equal(got, afterApply) && !maps.Equal(got, beforeApply) {
				t.Fatalf("undo interrupted at %d (%s) with no record and a store that is neither version", at, step)
			}
			continue
		}
		if op.Kind != domain.OperationUndo || op.RestoreSnapshotID != applied.SnapshotID {
			t.Fatalf("undo interrupted at %d (%s): record %+v", at, step, op)
		}
		assertNothingRepairsItself(t, f, c, step)

		if _, err := f.svc.Recover(context.Background()); err != nil {
			t.Fatalf("undo interrupted at %d (%s): Recover: %v", at, step, err)
		}
		recovered++
		f.assertAgentsTree(beforeApply, "recover of an undo after "+step)
		if _, ok := f.operation(); ok {
			t.Fatalf("recover after %s left the record", step)
		}
	}
	if recovered == 0 {
		t.Fatal("no interruption left a record to recover")
	}
	t.Logf("%d interruption points left a record and were recovered", recovered)
}

func TestRecoverWithoutAnInterruptedOperation(t *testing.T) {
	f := newFixture(t)
	f.standard()
	before := f.agentsTree()

	if _, err := f.svc.Recover(context.Background()); !errors.Is(err, updates.ErrNoOperation) {
		t.Fatalf("Recover = %v, want ErrNoOperation", err)
	}
	f.assertAgentsTree(before, "after a recover with nothing to do")
}
