package tui_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

type tuiT3StateFault struct {
	ports.StateStore
	failAppend          bool
	doneAppend          bool
	failRead            bool
	failReadAfterAppend bool
	failSave            bool
	injected            error
}

func (s *tuiT3StateFault) AppendJournal(e domain.HistoryEntry) error {
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

func (s *tuiT3StateFault) Journal() ([]domain.HistoryEntry, error) {
	if s.failRead {
		return nil, s.injected
	}
	return s.StateStore.Journal()
}

func (s *tuiT3StateFault) Save(st domain.PersistedState) error {
	if s.failSave {
		return s.injected
	}
	return s.StateStore.Save(st)
}

func newAppWithState(t *testing.T, mc *machine, st ports.StateStore) *app.App {
	t.Helper()
	files := fsadapter.NewFileOps(mc.p.Guard())
	agents, err := data.LoadAgents(mc.p, files)
	must(t, err)
	git := fakes.NewGit()
	clone, err := filepath.EvalSymlinks(mc.h.Path(graphtest.KarpathyClone))
	must(t, err)
	git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	lock := skillslock.New(mc.p, files)
	a, err := app.New(app.Deps{
		Paths: mc.p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(mc.p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(mc.p.Guard()),
		State: st, Settings: settings.New(mc.p), GitHub: mc.gh, Git: git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: mc.clock,
		ProcDir: mc.proc,
	})
	must(t, err)
	return a
}

func TestT3PresentHistoryHidesUndoOnConsumedRows(t *testing.T) {
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	entries := []domain.HistoryEntry{
		{At: t0, Action: domain.ActionApplied, Target: "lock:a", SnapshotID: "v1-consumed"},
		{At: t0.Add(time.Minute), Action: domain.ActionUndone, Target: "lock:a", SnapshotID: "v1-consumed"},
		{At: t0.Add(2 * time.Minute), Action: domain.ActionApplied, Target: "lock:b", SnapshotID: "v1-open"},
		{At: t0.Add(3 * time.Minute), Action: domain.ActionApplied, Target: "v2:disable", SnapshotID: "v2-consumed", Detail: "plan=p1 snapshot_sha=s1"},
		{At: t0.Add(4 * time.Minute), Action: domain.ActionUndone, Target: "v2:undo", SnapshotID: "v2-undo-1", Detail: "plan=p2 snapshot_sha=s2 undoes=v2-consumed"},
		{At: t0.Add(5 * time.Minute), Action: domain.ActionApplied, Target: "v2:disable", SnapshotID: "v2-open", Detail: "plan=p3 snapshot_sha=s3"},
	}

	rows := tui.PresentHistory(entries)
	if len(rows) != len(entries) {
		t.Fatalf("PresentHistory len = %d, want %d", len(rows), len(entries))
	}
	bySnap := map[string]tui.HistoryRow{}
	for _, r := range rows {
		bySnap[r.SnapshotID] = r
	}
	if !bySnap["v1-consumed"].CanUndo {
		t.Errorf("v1-consumed CanUndo = false, want true (ee22641 v1 behavior)")
	}
	if !bySnap["v1-open"].CanUndo {
		t.Errorf("v1-open CanUndo = false, want true")
	}
	if bySnap["v2-consumed"].CanUndo {
		t.Errorf("v2-consumed CanUndo = true, want false")
	}
	if !bySnap["v2-open"].CanUndo {
		t.Errorf("v2-open CanUndo = false, want true")
	}

	// Verify in ScreenHistory that a consumed v2 row does not offer or trigger [u].
	mc := newMachine(t)
	a := mc.newApp()
	ctx := t.Context()
	p1, err := a.PlanDisable(ctx, "agent-browser")
	must(t, err)
	res1, err := a.ApplyPlan(ctx, p1.Request, p1.PlanID)
	must(t, err)
	pu, err := a.PlanUndo(ctx, res1.SnapshotID)
	must(t, err)
	_, err = a.ApplyPlan(ctx, pu.Request, pu.PlanID)
	must(t, err)

	m := open(t, a)
	pressRun(m, "h")
	// Row 0 is v2:undo (not undoable), Row 1 is v2:disable (consumed!).
	pressRun(m, "down")
	if strings.Contains(screen(m), "u deshacer") {
		t.Errorf("consumed row still shows undo shortcut:\n%s", screen(m))
	}
	pressRun(m, "u")
	if m.InPlanForTest() {
		t.Fatal("pressing u on a consumed history row opened an undo plan")
	}
}

func TestT3RecoverWithCleanupWarningAndClosePending(t *testing.T) {
	injected := errors.New("injected io failure")

	t.Run("recover resolved with staging cleanup warning leaves recover screen and shows warning", func(t *testing.T) {
		mc := newMachine(t)
		st := &tuiT3StateFault{StateStore: statefile.New(mc.p), failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		af := newAppWithState(t, mc, st)
		p, err := af.PlanDisable(t.Context(), "agent-browser")
		must(t, err)
		_, _ = af.ApplyPlan(t.Context(), p.Request, p.PlanID)

		orphanDir := filepath.Join(mc.p.InstallStagingDir(), "orphan")
		mc.h.WriteFile(".agents/.skillhub/staging/orphan/file.txt", "x")
		must(t, os.Chmod(orphanDir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(orphanDir, 0o755) })

		m := open(t, mc.newApp())
		pressRun(m, "R")

		if mc.recordPending() {
			t.Fatal("R did not resolve pending record")
		}
		got := screen(m)
		if strings.Contains(got, interruptedTitle) {
			t.Errorf("recover screen still shown after engine resolved:\n%s", got)
		}
		status := m.StatusForTest()
		if !strings.Contains(status, "se terminó de cerrar") || !strings.Contains(status, "aviso:") {
			t.Errorf("status = %q, want recovery result + cleanup warning", status)
		}
	})

	t.Run("recover with pending engine close stays on recover screen", func(t *testing.T) {
		mc := newMachine(t)
		st := &tuiT3StateFault{StateStore: statefile.New(mc.p), failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		af := newAppWithState(t, mc, st)
		p, err := af.PlanDisable(t.Context(), "agent-browser")
		must(t, err)
		_, _ = af.ApplyPlan(t.Context(), p.Request, p.PlanID)

		st.failAppend = false
		st.failRead = false
		st.failSave = true
		m := open(t, newAppWithState(t, mc, st))
		pressRun(m, "R")

		if !mc.recordPending() {
			t.Fatal("record disappeared despite closeCommitted failure")
		}
		got := screen(m)
		if !strings.Contains(got, interruptedTitle) {
			t.Errorf("recover screen disappeared while close is still pending:\n%s", got)
		}
	})

	t.Run("apply close pending does not offer undo or repeat approval", func(t *testing.T) {
		mc := newMachine(t)
		st := &tuiT3StateFault{StateStore: statefile.New(mc.p), failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		m := open(t, newAppWithState(t, mc, st))

		// Find agent-browser and disable it.
		for i := 0; i < m.RowCountForTest(); i++ {
			if strings.Contains(m.SelectedIDForTest(), "agent-browser") {
				break
			}
			pressRun(m, "down")
		}
		pressRun(m, "d")
		if !m.InPlanForTest() {
			t.Fatal("d did not open plan")
		}
		pressRun(m, "s")
		if m.InPlanForTest() {
			t.Fatal("close pending repeated approval")
		}
		status := m.StatusForTest()
		if strings.Contains(status, "z deshacer") || strings.Contains(status, "Hecho.") {
			t.Errorf("close pending offered undo or reported done: %q", status)
		}
		if !strings.Contains(strings.ToLower(status), "no se pudo confirmar") || !strings.Contains(status, "skillhub recover") {
			t.Errorf("status = %q, want unconfirmed + skillhub recover", status)
		}
		pressRun(m, "z")
		if m.InPlanForTest() {
			t.Fatal("z offered undo after close pending")
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
