package engine_test

import (
	"errors"
	"os"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// journalFaultState wraps faultState to allow failing Journal() reads on demand
// after an AppendJournal fault.
type journalFaultState struct {
	*faultState
	failJournal            bool
	failJournalAfterAppend bool
}

func (s *journalFaultState) AppendJournal(entry domain.HistoryEntry) error {
	err := s.faultState.AppendJournal(entry)
	if err != nil && s.failJournalAfterAppend {
		s.failJournal = true
	}
	return err
}

func (s *journalFaultState) Journal() ([]domain.HistoryEntry, error) {
	if s.failJournal {
		return nil, errInjected
	}
	return s.faultState.Journal()
}

func newJournalFaultEnv(t *testing.T) (*env, fixturePaths, *journalFaultState) {
	t.Helper()
	e, fp := newEnv(t)
	noOwnLevels(t, e)
	st := &journalFaultState{faultState: &faultState{Store: e.state, f: e.files}}
	return e, fp, st
}

func (e *env) engineWithState(st engine.State) *engine.Engine {
	return engine.New(engine.Deps{
		Paths:    e.p,
		Files:    e.files,
		Links:    &faultLinks{LinkOps: fsadapter.NewLinkOps(e.p.Guard()), f: e.files},
		Lock:     &faultLock{Store: e.lock, f: e.files},
		DropLock: &faultDrop{d: e.lock, f: e.files},
		State:    st,
		Dirs:     fsadapter.NewScanner(e.p.Guard()),
		Clock:    e.clock,
	})
}

// appendWriteIndex runs plan on a throwaway env to find the exact write index
// of AppendJournal during Execute.
func appendWriteIndex(t *testing.T, undo bool) int {
	t.Helper()
	e, fp := newEnv(t)
	noOwnLevels(t, e)
	eng := e.engine()
	plan := disableEffectPlan(t, e, eng, fp)
	if undo {
		out, err := eng.Apply(t.Context(), plan)
		must(t, err)
		e.files.arm(-1, failOnce)
		_, err = eng.Undo(t.Context(), out.SnapshotID)
		must(t, err)
	} else {
		e.files.arm(-1, failOnce)
		_, err := eng.Apply(t.Context(), plan)
		must(t, err)
	}
	for i, w := range e.files.writeLog() {
		if w == "AppendJournal applied" || w == "AppendJournal undone" {
			return i
		}
	}
	t.Fatalf("AppendJournal not found in writeLog: %v", e.files.writeLog())
	return -1
}

func TestAppendJournalFailureConsultsJournal(t *testing.T) {
	applyIdx := appendWriteIndex(t, false)
	undoIdx := appendWriteIndex(t, true)

	t.Run("apply absent rolls back", func(t *testing.T) {
		e, fp, st := newJournalFaultEnv(t)
		eng := e.engineWithState(st)
		plan := disableEffectPlan(t, e, eng, fp)
		start := observeWorld(t, e)

		e.files.arm(applyIdx, failOnce)
		out, err := eng.Apply(t.Context(), plan)
		if err == nil {
			t.Fatal("Apply succeeded despite AppendJournal failure")
		}
		if errors.Is(err, engine.ErrClosePending) {
			t.Fatalf("confirmed absent append should roll back, not return ErrClosePending: %v", err)
		}
		if out.SnapshotID != "" {
			t.Errorf("Outcome.SnapshotID = %q, want empty", out.SnapshotID)
		}
		if recordExists(t, e.p) {
			t.Error("operation-v2.json still exists after rollback")
		}
		assertWorld(t, "rolled back to preimages", observeWorld(t, e), start)
		for _, h := range journal(t, e) {
			if h.Action == domain.ActionApplied {
				t.Errorf("unexpected applied row in journal: %+v", h)
			}
		}
	})

	t.Run("apply present closes without duplicate", func(t *testing.T) {
		e, fp, st := newJournalFaultEnv(t)
		eng := e.engineWithState(st)
		plan := disableEffectPlan(t, e, eng, fp)

		e.files.arm(applyIdx, doneButFailed)
		out, err := eng.Apply(t.Context(), plan)
		if err != nil {
			t.Fatalf("Apply with confirmed journal line failed: %v", err)
		}
		if out.SnapshotID == "" {
			t.Fatal("Outcome.SnapshotID is empty after confirmed close")
		}
		if recordExists(t, e.p) {
			t.Error("operation-v2.json still exists after confirmed close")
		}
		if _, err := osStatExists(fp.parked); err != nil {
			t.Errorf("postimage parked folder missing: %v", err)
		}
		var applied int
		for _, h := range journal(t, e) {
			if h.SnapshotID == out.SnapshotID && h.Action == domain.ActionApplied {
				applied++
			}
		}
		if applied != 1 {
			t.Errorf("applied rows for %s = %d, want 1", out.SnapshotID, applied)
		}
	})

	t.Run("apply unreadable journal keeps record and recovers accurately", func(t *testing.T) {
		for _, mode := range []faultMode{failOnce, doneButFailed} {
			name := "absent_on_disk"
			if mode == doneButFailed {
				name = "present_on_disk"
			}
			t.Run(name, func(t *testing.T) {
				e, fp, st := newJournalFaultEnv(t)
				eng := e.engineWithState(st)
				plan := disableEffectPlan(t, e, eng, fp)
				start := observeWorld(t, e)

				st.failJournalAfterAppend = true
				e.files.arm(applyIdx, mode)
				out, err := eng.Apply(t.Context(), plan)
				if !errors.Is(err, engine.ErrClosePending) {
					t.Fatalf("Apply error = %v, want ErrClosePending", err)
				}
				if out.SnapshotID != "" || out.Committed || !out.ClosePending {
					t.Errorf("Outcome = %+v, want SnapshotID='', Committed=false, ClosePending=true", out)
				}
				if !recordExists(t, e.p) {
					t.Fatal("operation-v2.json was removed despite unreadable journal")
				}
				// Must not revert by presumption while journal is unreadable.
				if _, err := osStatExists(fp.parked); err != nil {
					t.Errorf("postimage was reverted by presumption: %v", err)
				}

				// Once journal is readable again, Recover resolves according to truth.
				st.failJournal = false
				e.files.arm(-1, failOnce)
				rec, rerr := e.clean().Recover(t.Context())
				if rerr != nil {
					t.Fatalf("Recover failed: %v", rerr)
				}
				if mode == failOnce {
					if !rec.RolledBack || rec.Committed {
						t.Errorf("Recover = %+v, want RolledBack=true", rec)
					}
					assertWorld(t, "recovered preimages", observeWorld(t, e), start)
				} else {
					if !rec.Committed || rec.RolledBack {
						t.Errorf("Recover = %+v, want Committed=true", rec)
					}
					if _, err := osStatExists(fp.parked); err != nil {
						t.Errorf("postimage lost after Recover: %v", err)
					}
				}
				if recordExists(t, e.p) {
					t.Error("operation-v2.json still exists after Recover")
				}
			})
		}
	})

	t.Run("undo absent rolls back", func(t *testing.T) {
		e, fp, st := newJournalFaultEnv(t)
		eng := e.engineWithState(st)
		plan := disableEffectPlan(t, e, eng, fp)
		out, err := eng.Apply(t.Context(), plan)
		must(t, err)
		disabled := observeWorld(t, e)

		e.files.arm(undoIdx, failOnce)
		uout, err := eng.Undo(t.Context(), out.SnapshotID)
		if err == nil {
			t.Fatal("Undo succeeded despite AppendJournal failure")
		}
		if errors.Is(err, engine.ErrClosePending) {
			t.Fatalf("confirmed absent undo append should roll back, not return ErrClosePending: %v", err)
		}
		if uout.SnapshotID != "" {
			t.Errorf("Undo Outcome.SnapshotID = %q, want empty", uout.SnapshotID)
		}
		if recordExists(t, e.p) {
			t.Error("operation-v2.json still exists after undo rollback")
		}
		assertWorld(t, "reverted undo back to disabled state", observeWorld(t, e), disabled)
		for _, h := range journal(t, e) {
			if h.Action == domain.ActionUndone {
				t.Errorf("unexpected undone row in journal: %+v", h)
			}
		}
	})

	t.Run("undo present closes without duplicate", func(t *testing.T) {
		e, fp, st := newJournalFaultEnv(t)
		eng := e.engineWithState(st)
		plan := disableEffectPlan(t, e, eng, fp)
		start := observeWorld(t, e)
		out, err := eng.Apply(t.Context(), plan)
		must(t, err)

		e.files.arm(undoIdx, doneButFailed)
		uout, err := eng.Undo(t.Context(), out.SnapshotID)
		if err != nil {
			t.Fatalf("Undo with confirmed journal line failed: %v", err)
		}
		if uout.SnapshotID == "" {
			t.Fatal("Undo Outcome.SnapshotID is empty after confirmed close")
		}
		if recordExists(t, e.p) {
			t.Error("operation-v2.json still exists after confirmed undo close")
		}
		assertWorld(t, "undo postimage kept", observeWorld(t, e), start)
		var undone int
		for _, h := range journal(t, e) {
			if h.SnapshotID == uout.SnapshotID && h.Action == domain.ActionUndone {
				undone++
			}
		}
		if undone != 1 {
			t.Errorf("undone rows for %s = %d, want 1", uout.SnapshotID, undone)
		}
	})

	t.Run("undo unreadable journal keeps record and recovers accurately", func(t *testing.T) {
		for _, mode := range []faultMode{failOnce, doneButFailed} {
			name := "absent_on_disk"
			if mode == doneButFailed {
				name = "present_on_disk"
			}
			t.Run(name, func(t *testing.T) {
				e, fp, st := newJournalFaultEnv(t)
				eng := e.engineWithState(st)
				plan := disableEffectPlan(t, e, eng, fp)
				start := observeWorld(t, e)
				out, err := eng.Apply(t.Context(), plan)
				must(t, err)
				disabled := observeWorld(t, e)

				st.failJournalAfterAppend = true
				e.files.arm(undoIdx, mode)
				uout, err := eng.Undo(t.Context(), out.SnapshotID)
				if !errors.Is(err, engine.ErrClosePending) {
					t.Fatalf("Undo error = %v, want ErrClosePending", err)
				}
				if uout.SnapshotID != "" || uout.Committed || !uout.ClosePending {
					t.Errorf("Undo Outcome = %+v, want SnapshotID='', Committed=false, ClosePending=true", uout)
				}
				if !recordExists(t, e.p) {
					t.Fatal("operation-v2.json was removed despite unreadable journal")
				}
				// Must not revert undo by presumption while journal is unreadable.
				if _, err := osStatExists(fp.alpha); err != nil {
					t.Errorf("undo postimage was reverted by presumption: %v", err)
				}

				st.failJournal = false
				e.files.arm(-1, failOnce)
				rec, rerr := e.clean().Recover(t.Context())
				if rerr != nil {
					t.Fatalf("Recover failed: %v", rerr)
				}
				if mode == failOnce {
					if !rec.RolledBack || rec.Committed {
						t.Errorf("Recover = %+v, want RolledBack=true", rec)
					}
					assertWorld(t, "recovered disabled state", observeWorld(t, e), disabled)
				} else {
					if !rec.Committed || rec.RolledBack {
						t.Errorf("Recover = %+v, want Committed=true", rec)
					}
					assertWorld(t, "recovered undo postimage", observeWorld(t, e), start)
				}
				if recordExists(t, e.p) {
					t.Error("operation-v2.json still exists after Recover")
				}
			})
		}
	})

	t.Run("recover closeCommitted failure stays pending with ErrClosePending", func(t *testing.T) {
		e, fp, st := newJournalFaultEnv(t)
		eng := e.engineWithState(st)
		plan := disableEffectPlan(t, e, eng, fp)

		e.files.arm(applyIdx, crashAfter)
		_, err := eng.Apply(t.Context(), plan)
		if !errors.Is(err, errInjected) {
			t.Fatalf("Apply error = %v, want errInjected", err)
		}
		if !recordExists(t, e.p) {
			t.Fatal("operation-v2.json missing after crashAfter AppendJournal")
		}

		// During Recover, fail Save("state") inside closeCommitted.
		e.files.arm(0, failOnce) // first write in closeCommitted after appendOnce is clearRestore or Save
		// Let's target Save specifically by arming after we see which write Save is, or use crashAfter on Save.
		rec, rerr := eng.Recover(t.Context())
		if rerr == nil || !errors.Is(rerr, engine.ErrClosePending) {
			t.Fatalf("Recover error = %v (rec=%+v), want ErrClosePending", rerr, rec)
		}
		if !recordExists(t, e.p) {
			t.Fatal("operation-v2.json removed despite closeCommitted failure")
		}

		// Second Recover without faults finishes closing without duplicating journal row.
		e.files.arm(-1, failOnce)
		rec2, rerr2 := e.clean().Recover(t.Context())
		if rerr2 != nil || !rec2.Committed {
			t.Fatalf("second Recover = %+v, %v; want Committed=true, nil", rec2, rerr2)
		}
		if recordExists(t, e.p) {
			t.Fatal("operation-v2.json still exists after second Recover")
		}
		var applied int
		for _, h := range journal(t, e) {
			if h.SnapshotID == rec2.SnapshotID && h.Action == domain.ActionApplied {
				applied++
			}
		}
		if applied != 1 {
			t.Errorf("applied rows = %d, want 1", applied)
		}
	})

	t.Run("confirmed apply and undo with post-append close failure preserve SnapshotID and Committed", func(t *testing.T) {
		e, fp, st := newJournalFaultEnv(t)
		eng := e.engineWithState(st)
		plan := disableEffectPlan(t, e, eng, fp)

		// Fail Save("state") after AppendJournal("applied") succeeds.
		e.files.arm(applyIdx+2, failOnce)
		out, err := eng.Apply(t.Context(), plan)
		if !errors.Is(err, engine.ErrClosePending) {
			t.Fatalf("Apply error = %v, want ErrClosePending", err)
		}
		if out.SnapshotID == "" || !out.Committed || !out.ClosePending {
			t.Errorf("Apply Outcome = %+v, want SnapshotID!='', Committed=true, ClosePending=true", out)
		}
		if !recordExists(t, e.p) {
			t.Fatal("operation-v2.json missing while close pending")
		}

		e.files.arm(-1, failOnce)
		rec, rerr := e.clean().Recover(t.Context())
		if rerr != nil || !rec.Committed || rec.Undo {
			t.Fatalf("Recover apply = %+v, %v; want Committed=true, Undo=false", rec, rerr)
		}

		// Now undo and fail Save("state") after AppendJournal("undone") succeeds.
		e.files.arm(undoIdx+1, failOnce)
		uout, uerr := eng.Undo(t.Context(), rec.SnapshotID)
		if !errors.Is(uerr, engine.ErrClosePending) {
			t.Fatalf("Undo error = %v, want ErrClosePending", uerr)
		}
		if uout.SnapshotID == "" || !uout.Committed || !uout.ClosePending {
			t.Errorf("Undo Outcome = %+v, want SnapshotID!='', Committed=true, ClosePending=true", uout)
		}
		if !recordExists(t, e.p) {
			t.Fatal("operation-v2.json missing while undo close pending")
		}

		e.files.arm(-1, failOnce)
		urec, urerr := e.clean().Recover(t.Context())
		if urerr != nil || !urec.Committed || !urec.Undo {
			t.Fatalf("Recover undo = %+v, %v; want Committed=true, Undo=true", urec, urerr)
		}
	})
}

func osStatExists(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
