package app_test

import (
	"errors"
	"path/filepath"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

type t3StateFault struct {
	ports.StateStore
	failAppend          bool
	doneAppend          bool
	failRead            bool
	failReadAfterAppend bool
	failSave            bool
	injected            error
}

func (s *t3StateFault) AppendJournal(e domain.HistoryEntry) error {
	if s.failAppend {
		s.failAppend = false
		if s.doneAppend {
			_ = s.StateStore.AppendJournal(e)
		}
		if s.failReadAfterAppend {
			s.failRead = true
		}
		return s.injected
	}
	return s.StateStore.AppendJournal(e)
}

func (s *t3StateFault) Journal() ([]domain.HistoryEntry, error) {
	if s.failRead {
		return nil, s.injected
	}
	return s.StateStore.Journal()
}

func (s *t3StateFault) Save(st domain.PersistedState) error {
	if s.failSave {
		return s.injected
	}
	return s.StateStore.Save(st)
}

type t3FileFault struct {
	ports.FileOps
	failRemovePrefix string
	injected         error
}

func (f *t3FileFault) RemoveTree(path string) error {
	if f.failRemovePrefix != "" && filepath.Clean(path) == filepath.Clean(f.failRemovePrefix) {
		return f.injected
	}
	return f.FileOps.RemoveTree(path)
}

func (m *machine) withDeps(files ports.FileOps, st ports.StateStore) *app.App {
	m.t.Helper()
	if files == nil {
		files = fsadapter.NewFileOps(m.p.Guard())
	}
	if st == nil {
		st = m.state
	}
	agents, err := data.LoadAgents(m.p, files)
	must(m.t, err)
	lock := skillslock.New(m.p, files)
	a, err := app.New(app.Deps{
		Paths: m.p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(m.p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(m.p.Guard()),
		State: st, Settings: m.settings, GitHub: m.gh, Git: m.git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: m.clock,
		ProcDir: m.proc,
	})
	must(m.t, err)
	return a
}

func TestApplyPlanDistinguishesRejectedFromClosePending(t *testing.T) {
	injected := errors.New("injected io failure")

	t.Run("rejected before commit leaves ClosePending false and no record", func(t *testing.T) {
		m := newMachine(t)
		st := &t3StateFault{StateStore: m.state, failAppend: true, injected: injected}
		a := m.withDeps(nil, st)

		p, err := a.PlanDisable(bg, "agent-browser")
		must(t, err)

		res, err := a.ApplyPlan(bg, p.Request, p.PlanID)
		if err == nil {
			t.Fatal("ApplyPlan succeeded despite AppendJournal failure")
		}
		if res.SnapshotID != "" {
			t.Errorf("SnapshotID = %q, want empty", res.SnapshotID)
		}
		if res.ClosePending {
			t.Error("ClosePending = true, want false on confirmed absent rollback")
		}
		pending, perr := a.ManagedPending()
		must(t, perr)
		if pending {
			t.Error("ManagedPending = true, want false after rollback")
		}
	})

	t.Run("unreadable journal after append leaves ClosePending true and record pending", func(t *testing.T) {
		m := newMachine(t)
		st := &t3StateFault{StateStore: m.state, failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		a := m.withDeps(nil, st)

		p, err := a.PlanDisable(bg, "agent-browser")
		must(t, err)

		res, err := a.ApplyPlan(bg, p.Request, p.PlanID)
		if err == nil {
			t.Fatal("ApplyPlan succeeded despite unreadable journal")
		}
		if res.SnapshotID != "" || res.Committed || !res.ClosePending {
			t.Errorf("res = %+v, want SnapshotID='', Committed=false, ClosePending=true", res)
		}
		pending, perr := a.ManagedPending()
		must(t, perr)
		if !pending {
			t.Error("ManagedPending = false, want true while close is pending")
		}
	})
}

func TestRecoverOperationDistinguishesResolvedWithCleanupWarningFromPending(t *testing.T) {
	injected := errors.New("injected io failure")

	t.Run("resolved with staging cleanup warning sets Resolved and ErrRecoverCleanup", func(t *testing.T) {
		m := newMachine(t)
		a := m.app

		p, err := a.PlanDisable(bg, "agent-browser")
		must(t, err)
		eng := engine.New(engine.Deps{
			Paths: m.p, Files: fsadapter.NewFileOps(m.p.Guard()),
			Links: fsadapter.NewLinkOps(m.p.Guard()),
			Lock:  skillslock.New(m.p, fsadapter.NewFileOps(m.p.Guard())),
			State: m.state, Dirs: fsadapter.NewScanner(m.p.Guard()), Clock: m.clock,
		})
		// Create a pending record via Begin (not yet committed) and an orphan staging dir.
		// Build a minimal engine plan by running PlanDisable? Wait: Begin via engine directly.
		_ = eng
		_ = p
		// Simpler: use t3StateFault to leave a pending record on ApplyPlan!
		st := &t3StateFault{StateStore: m.state, failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		af := m.withDeps(nil, st)
		_, aerr := af.ApplyPlan(bg, p.Request, p.PlanID)
		if aerr == nil {
			t.Fatal("expected ApplyPlan to stop with pending record")
		}

		// Create an orphan staging directory that fails to be removed.
		stagingDir := m.p.InstallStagingDir()
		m.h.Mkdir(".agents/.skillhub/staging/orphan")
		ff := &t3FileFault{FileOps: fsadapter.NewFileOps(m.p.Guard()), failRemovePrefix: stagingDir, injected: injected}
		st.failAppend = false
		st.failRead = false
		ar := m.withDeps(ff, st)

		rec, rerr := ar.RecoverOperation(bg)
		if !errors.Is(rerr, app.ErrRecoverCleanup) {
			t.Fatalf("RecoverOperation error = %v, want ErrRecoverCleanup", rerr)
		}
		if !rec.V2.Resolved || !rec.V2.Committed {
			t.Errorf("RecoverOperation V2 = %+v, want Resolved=true and Committed=true", rec.V2)
		}
		pending, perr := ar.ManagedPending()
		must(t, perr)
		if pending {
			t.Error("ManagedPending = true after engine resolved record")
		}
	})

	t.Run("engine close still pending leaves Resolved false and record pending", func(t *testing.T) {
		m := newMachine(t)
		st := &t3StateFault{StateStore: m.state, failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		af := m.withDeps(nil, st)

		p, err := af.PlanDisable(bg, "agent-browser")
		must(t, err)
		_, aerr := af.ApplyPlan(bg, p.Request, p.PlanID)
		if aerr == nil {
			t.Fatal("expected ApplyPlan to stop with pending record")
		}

		// Now Recover can read the journal (sees committed), but State.Save fails during closeCommitted.
		st.failAppend = false
		st.failRead = false
		st.failSave = true
		ar := m.withDeps(nil, st)

		rec, rerr := ar.RecoverOperation(bg)
		if rerr == nil {
			t.Fatal("RecoverOperation succeeded despite closeCommitted Save failure")
		}
		if errors.Is(rerr, app.ErrRecoverCleanup) {
			t.Errorf("RecoverOperation error wrapped ErrRecoverCleanup when close is still pending: %v", rerr)
		}
		if rec.V2.Resolved {
			t.Errorf("RecoverOperation V2.Resolved = true, want false when close is still pending")
		}
		pending, perr := ar.ManagedPending()
		must(t, perr)
		if !pending {
			t.Error("ManagedPending = false, want true while close is pending")
		}
	})
}
