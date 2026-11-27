package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

type cliT3StateFault struct {
	ports.StateStore
	failAppend          bool
	doneAppend          bool
	failRead            bool
	failReadAfterAppend bool
	failSave            bool
	injected            error
}

func (s *cliT3StateFault) AppendJournal(e domain.HistoryEntry) error {
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

func (s *cliT3StateFault) Journal() ([]domain.HistoryEntry, error) {
	if s.failRead {
		return nil, s.injected
	}
	return s.StateStore.Journal()
}

func (s *cliT3StateFault) Save(st domain.PersistedState) error {
	if s.failSave {
		return s.injected
	}
	return s.StateStore.Save(st)
}

func TestT3UndoSelectsMostRecentEligibleAndRejectsIneligibleWithoutRemovingHistory(t *testing.T) {
	m := newMachine(t)
	m.approve("skills", "disable", "agent-browser")
	_ = m.lastV2().SnapshotID
	m.clock.Advance(time.Minute)
	m.approve("skills", "disable", "archify")
	snap2 := m.lastV2().SnapshotID
	m.clock.Advance(time.Minute)

	// Undo snap2 explicitly.
	m.approve("undo", "v2:disable", "--to", snap2)

	// Add a newer failed v2:disable row in the journal.
	must(t, statefile.New(m.p).AppendJournal(domain.HistoryEntry{
		At: m.clock.Now().UTC(), Action: domain.ActionFailed, Target: "v2:disable",
		SnapshotID: "snap-failed", Labels: []domain.Label{}, Detail: "plan=p",
	}))

	// Without --to, undo v2:disable must pick snap1 (the most recent eligible applied row).
	r := m.ok("undo", "v2:disable", "--dry-run")
	if !strings.Contains(r.stdout, "agent-browser") {
		t.Errorf("undo v2:disable without --to did not select eligible snap1 (agent-browser):\n%s", r.stdout)
	}

	// With --to on an already-undone snapshot, reject without removing it from history.
	r = m.run("", "undo", "v2:disable", "--to", snap2, "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "no hay nada que deshacer") {
		t.Errorf("undo --to consumed snap = exit %d, stderr %q; want exit 2 'no hay nada que deshacer'", r.code, r.stderr)
	}

	// With --to on a failed snapshot, reject without removing it from history.
	r = m.run("", "undo", "v2:disable", "--to", "snap-failed", "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "no hay nada que deshacer") {
		t.Errorf("undo --to failed snap = exit %d, stderr %q; want exit 2 'no hay nada que deshacer'", r.code, r.stderr)
	}

	// With --to '' (explicitly empty), reject as ineligible (exit 2, no stdout, no state change).
	r = m.run("", "undo", "v2:disable", "--to", "")
	if r.code != 2 || r.stdout != "" {
		t.Errorf("undo --to '' = exit %d, stdout %q, stderr %q; want exit 2, empty stdout", r.code, r.stdout, r.stderr)
	}

	hist := m.ok("history", "--json")
	if !strings.Contains(hist.stdout, snap2) || !strings.Contains(hist.stdout, "snap-failed") {
		t.Errorf("history dropped ineligible rows:\n%s", hist.stdout)
	}
}

func TestT3ClosePendingAndRecoverCleanupWarning(t *testing.T) {
	injected := errors.New("injected io failure")

	t.Run("close pending on apply does not report done, repeat approval, or offer undo", func(t *testing.T) {
		m := newMachine(t)
		m.state = func(s ports.StateStore) ports.StateStore {
			return &cliT3StateFault{StateStore: s, failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		}
		p := m.ok("skills", "disable", "agent-browser", "--dry-run")
		id := planID(t, p.stdout)
		r := m.run("", "skills", "disable", "agent-browser", "--yes", "--plan", id)
		if r.code != 2 {
			t.Fatalf("expected exit 2 on unconfirmed close pending, got %d\nstdout: %s", r.code, r.stdout)
		}
		if strings.Contains(r.stdout, "Hecho.") || strings.Contains(r.stdout, "skillhub undo") || strings.Contains(r.stdout, "vuelve a ejecutar el comando con --yes --plan") {
			t.Errorf("close pending reported done, offered undo, or repeated approval:\n%s", r.stdout)
		}
		if !strings.Contains(r.stderr, "no se pudo confirmar") || !strings.Contains(r.stderr, "skillhub recover") {
			t.Errorf("stderr = %q, want unconfirmed + skillhub recover", r.stderr)
		}
		if _, err := os.Stat(engine.RecordFile(m.p)); err != nil {
			t.Errorf("operation-v2.json missing while close pending: %v", err)
		}
	})

	t.Run("confirmed commit with pending close preserves snapshot and forbids repeat approval", func(t *testing.T) {
		m := newMachine(t)
		m.state = func(s ports.StateStore) ports.StateStore {
			return &cliT3StateFault{StateStore: s, failSave: true, injected: injected}
		}
		p := m.ok("skills", "disable", "agent-browser", "--dry-run")
		id := planID(t, p.stdout)
		r := m.run("", "skills", "disable", "agent-browser", "--yes", "--plan", id)
		if r.code != 2 {
			t.Fatalf("expected exit 2 on confirmed close pending, got %d\nstdout: %s", r.code, r.stdout)
		}
		if strings.Contains(r.stdout, "Hecho.") || strings.Contains(r.stdout, "skillhub undo") || strings.Contains(r.stdout, "vuelve a ejecutar el comando con --yes --plan") {
			t.Errorf("confirmed close pending reported done, offered undo, or repeated approval:\n%s", r.stdout)
		}
		if !strings.Contains(r.stderr, "ya quedó confirmada") || !strings.Contains(r.stderr, "skillhub recover") {
			t.Errorf("stderr = %q, want confirmed + skillhub recover", r.stderr)
		}

		// Recover finishes closing and prints undo hint for applied; then undo + recover of undo must NOT print undo hint.
		m.state = nil
		recOut := m.ok("recover")
		if !strings.Contains(recOut.stdout, "Se deshace con su snapshot") {
			t.Errorf("recover of applied missing undo hint:\n%s", recOut.stdout)
		}
		// Now undo with pending close, then recover undo.
		m.state = func(s ports.StateStore) ports.StateStore {
			return &cliT3StateFault{StateStore: s, failSave: true, injected: injected}
		}
		up := m.ok("undo", "v2:disable", "--dry-run")
		uid := planID(t, up.stdout)
		ur := m.run("", "undo", "v2:disable", "--yes", "--plan", uid)
		if ur.code != 2 {
			t.Fatalf("expected exit 2 on undo close pending, got %d", ur.code)
		}
		m.state = nil
		urec := m.ok("recover")
		if strings.Contains(urec.stdout, "Se deshace") {
			t.Errorf("recover of committed undo advertised undo:\n%s", urec.stdout)
		}
	})

	t.Run("recover with pending engine close does not announce completion", func(t *testing.T) {
		m := newMachine(t)
		m.state = func(s ports.StateStore) ports.StateStore {
			return &cliT3StateFault{StateStore: s, failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		}
		p := m.ok("skills", "disable", "agent-browser", "--dry-run")
		id := planID(t, p.stdout)
		_ = m.run("", "skills", "disable", "agent-browser", "--yes", "--plan", id)

		m.state = func(s ports.StateStore) ports.StateStore {
			return &cliT3StateFault{StateStore: s, failSave: true, injected: injected}
		}
		r := m.run("", "recover")
		if r.code == 0 {
			t.Fatalf("expected non-zero exit when recover close fails, got 0\nstdout: %s", r.stdout)
		}
		if strings.Contains(r.stdout, "se terminó de cerrar") || strings.Contains(r.stdout, "Se revirtió") {
			t.Errorf("recover announced completion while close still pending:\n%s", r.stdout)
		}
		if _, err := os.Stat(engine.RecordFile(m.p)); err != nil {
			t.Errorf("operation-v2.json missing while recover close pending: %v", err)
		}
	})

	t.Run("recover resolved with staging cleanup warning reports result and warning", func(t *testing.T) {
		m := newMachine(t)
		m.state = func(s ports.StateStore) ports.StateStore {
			return &cliT3StateFault{StateStore: s, failAppend: true, doneAppend: true, failReadAfterAppend: true, injected: injected}
		}
		p := m.ok("skills", "disable", "agent-browser", "--dry-run")
		id := planID(t, p.stdout)
		_ = m.run("", "skills", "disable", "agent-browser", "--yes", "--plan", id)

		// Create unremovable orphan staging entry.
		orphanDir := filepath.Join(m.p.InstallStagingDir(), "orphan")
		m.h.WriteFile(".agents/.skillhub/staging/orphan/file.txt", "x")
		must(t, os.Chmod(orphanDir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(orphanDir, 0o755) })

		m.state = nil
		r := m.run("", "recover")
		if r.code != 0 {
			t.Fatalf("recover with staging cleanup warning exited %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
		}
		if !strings.Contains(r.stdout, "se terminó de cerrar") {
			t.Errorf("stdout missing recovery result:\n%s", r.stdout)
		}
		if !strings.Contains(r.stdout, "aviso:") || !strings.Contains(r.stdout, "no se pudo limpiar la copia temporal") {
			t.Errorf("stdout missing cleanup warning:\n%s", r.stdout)
		}
		if strings.Contains(r.stderr, "no se pudo resolver la operación interrumpida") {
			t.Errorf("stderr falsely claimed recover failed: %q", r.stderr)
		}
	})
}

func TestT3StalePlanIncludesPlanIDFooter(t *testing.T) {
	m := newMachine(t)
	p := m.ok("skills", "disable", "agent-browser", "--dry-run")
	currentID := planID(t, p.stdout)

	r := m.run("", "skills", "disable", "agent-browser", "--yes", "--plan", "stale-plan-id")
	if r.code != 2 {
		t.Fatalf("expected exit 2 on stale plan, got %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "el plan cambió desde que lo viste") || !strings.Contains(r.stderr, currentID) {
		t.Errorf("stderr = %q, want ErrPlanChanged with current plan %s", r.stderr, currentID)
	}
	if !strings.Contains(r.stdout, "Identificador del plan: "+currentID) {
		t.Errorf("stdout missing current plan identifier %s:\n%s", currentID, r.stdout)
	}
	if !strings.Contains(r.stdout, "Para aprobarlo sin preguntar, vuelve a ejecutar el comando con --yes --plan "+currentID) {
		t.Errorf("stdout missing --yes --plan footer for %s:\n%s", currentID, r.stdout)
	}

	// State must remain unchanged after refusal.
	after := m.ok("skills", "disable", "agent-browser", "--dry-run")
	if got := planID(t, after.stdout); got != currentID {
		t.Errorf("plan changed after stale refusal: got %s, want %s", got, currentID)
	}
}
