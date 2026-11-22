package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
)

// foreignCase changes the object of one step of disablePlan into something
// that is neither the step's preimage nor its postimage, and puts it back.
type foreignCase struct {
	step         int
	plant, unset func(t *testing.T, e *env, fp fixturePaths)
}

func foreignCases() map[string]foreignCase {
	return map[string]foreignCase{
		"unlink: the link points elsewhere": {step: 0,
			plant: func(t *testing.T, e *env, fp fixturePaths) {
				must(t, os.Remove(fp.link))
				must(t, os.Symlink("../../elsewhere", fp.link))
			},
			unset: func(t *testing.T, e *env, fp fixturePaths) {
				must(t, os.Remove(fp.link))
				must(t, os.Symlink(fp.linkTarget, fp.link))
			}},
		"move: the parking spot is taken": {step: 1,
			plant: func(t *testing.T, e *env, fp fixturePaths) {
				must(t, os.MkdirAll(fp.parked, 0o755))
				must(t, os.WriteFile(filepath.Join(fp.parked, "theirs.txt"), []byte("theirs\n"), 0o644))
			},
			unset: func(t *testing.T, e *env, fp fixturePaths) { must(t, os.RemoveAll(fp.parked)) }},
		"lockPark: npx rewrote the entry": {step: 2,
			plant: func(t *testing.T, e *env, fp fixturePaths) {
				writeLock(t, e.p, []byte(strings.Replace(fixtureLock, "owner/alpha", "owner/alpha2", 1)))
			},
			unset: func(t *testing.T, e *env, fp fixturePaths) { writeLock(t, e.p, []byte(fixtureLock)) }},
		"removeTree: beta gained a file": {step: 3,
			plant: func(t *testing.T, e *env, fp fixturePaths) {
				must(t, os.WriteFile(filepath.Join(fp.beta, "new.txt"), []byte("new\n"), 0o644))
			},
			unset: func(t *testing.T, e *env, fp fixturePaths) { must(t, os.Remove(filepath.Join(fp.beta, "new.txt"))) }},
	}
}

// TestForeignStateStopsTheOperationAndRecoverKeepsBothStates plants foreign
// state on the object of each step right before the step: the step stops
// without touching it and leaves the record with a diagnostic; Recover
// refuses while the foreign state is there and changes nothing; once the
// user puts it back, Recover rolls everything back byte for byte.
func TestForeignStateStopsTheOperationAndRecoverKeepsBothStates(t *testing.T) {
	for name, fc := range foreignCases() {
		t.Run(name, func(t *testing.T) {
			e, fp := newEnv(t)
			before := observeWorld(t, e)
			eng := e.engine()
			plan := disablePlan(t, eng, fp)
			recordWrites := 0
			var planted world
			e.files.afterWrite = func(_ int, line string) {
				if strings.HasSuffix(line, "operation-v2.json") && strings.HasPrefix(line, "WriteFileAtomic") {
					recordWrites++
					if recordWrites == fc.step+1 {
						fc.plant(t, e, fp)
						planted = observeWorld(t, e)
					}
				}
			}

			_, err := eng.Apply(t.Context(), plan)
			var fe *engine.ForeignStateError
			if !errors.Is(err, engine.ErrForeignState) || !errors.As(err, &fe) || fe.Step != fc.step {
				t.Fatalf("error = %v, want foreign state at step %d", err, fc.step)
			}
			e.files.afterWrite = nil
			stopped := observeWorld(t, e)
			assertWorld(t, "the step that met foreign state", stopped, planted)
			rec := pendingRecord(t, e)
			if rec.Diagnostic == "" || rec.LastCompleted != fc.step-1 {
				t.Errorf("record: diagnostic %q, last completed %d", rec.Diagnostic, rec.LastCompleted)
			}

			_, err = e.clean().Recover(t.Context())
			if !errors.Is(err, engine.ErrForeignState) {
				t.Fatalf("recover over foreign state: error = %v, want ErrForeignState", err)
			}
			assertWorld(t, "recover over foreign state", observeWorld(t, e), stopped)
			if !recordExists(t, e.p) {
				t.Error("recover dropped the record over foreign state")
			}

			fc.unset(t, e, fp)
			res, err := e.clean().Recover(t.Context())
			must(t, err)
			if !res.Found || !res.RolledBack {
				t.Errorf("result = %+v", res)
			}
			assertWorld(t, "after the foreign state went away", observeWorld(t, e), before)
		})
	}
}

// TestForeignStateOnAStepAlreadyRunKeepsEverything crashes after the move and
// then changes the moved folder: Recover must not undo the move, nor the
// unlink before it, and must report the diagnostic.
func TestForeignStateOnAStepAlreadyRunKeepsEverything(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	e.files.arm(scenario{}.firstStepWrite(failingWritesOf(t, e, plan))+3, crashBefore)
	if _, err := eng.Apply(t.Context(), plan); !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	must(t, os.WriteFile(filepath.Join(fp.parked, "mine.txt"), []byte("mine\n"), 0o644))
	stopped := observeWorld(t, e)

	_, err := e.clean().Recover(t.Context())
	var fe *engine.ForeignStateError
	if !errors.As(err, &fe) || fe.Step != 1 {
		t.Fatalf("error = %v, want foreign state at step 1", err)
	}
	assertWorld(t, "recover", observeWorld(t, e), stopped)
	if rec := pendingRecord(t, e); !strings.Contains(rec.Diagnostic, fp.parked) {
		t.Errorf("the diagnostic does not name the folder: %q", rec.Diagnostic)
	}
}

// failingWritesOf is the write log of applying plan in a copy of the world.
func failingWritesOf(t *testing.T, _ *env, _ engine.Plan) []string {
	t.Helper()
	return failingWrites(t, scenarios()["disable"].setup)
}

// TestForeignStateOnAnEarlyStepStopsBeforeUndoingLaterOnes crashes after the
// park and then points the removed link's path elsewhere: the rollback would
// have to undo the park and the move before it meets that, so a rollback
// that does not look first would change them. Recover must change nothing.
func TestForeignStateOnAnEarlyStepStopsBeforeUndoingLaterOnes(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	writes := failingWrites(t, scenarios()["disable"].setup)
	e.files.arm(scenario{}.firstStepWrite(writes)+5, crashBefore) // the record write after the park
	if _, err := eng.Apply(t.Context(), plan); !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	must(t, os.Symlink("../../elsewhere", fp.link))
	stopped := observeWorld(t, e)

	_, err := e.clean().Recover(t.Context())
	var fe *engine.ForeignStateError
	if !errors.As(err, &fe) || fe.Step != 0 {
		t.Fatalf("error = %v, want foreign state at step 0", err)
	}
	assertWorld(t, "recover", observeWorld(t, e), stopped)
}

// TestAVerificationThatFailsIsNotCommitted changes the moved folder after the
// last step: the verification fails, nothing is journaled as applied, and
// the changed folder, now foreign to the rollback, is kept with the record.
func TestAVerificationThatFailsIsNotCommitted(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	records := 0
	e.files.afterWrite = func(_ int, line string) {
		if strings.HasPrefix(line, "WriteFileAtomic") && strings.HasSuffix(line, "operation-v2.json") {
			records++
			if records == len(plan.Steps)+1 {
				must(t, os.WriteFile(filepath.Join(fp.parked, "late.txt"), []byte("late\n"), 0o644))
			}
		}
	}
	_, err := eng.Apply(t.Context(), plan)
	if !errors.Is(err, engine.ErrVerify) || !errors.Is(err, engine.ErrForeignState) {
		t.Fatalf("error = %v, want ErrVerify and ErrForeignState", err)
	}
	for _, l := range journal(t, e) {
		if l.Action == "applied" {
			t.Errorf("a failed verification was journaled as applied: %+v", l)
		}
	}
	if data, err := os.ReadFile(filepath.Join(fp.parked, "late.txt")); err != nil || string(data) != "late\n" {
		t.Errorf("the late file is gone: %q, %v", data, err)
	}
	if !recordExists(t, e.p) {
		t.Error("the record went away")
	}
}
