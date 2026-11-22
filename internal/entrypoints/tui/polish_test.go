package tui_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/manage/engine"
)

// The texts of the polish of version 2's TUI, as the user reads them.
const (
	interruptedTitle = "Operación interrumpida detectada"
	rolledBack       = "Se revirtió la operación de gestión interrumpida"
)

// pendingV2 leaves a version 2 operation begun and not finished, as a crash
// would: a record that unlinks .claude/skills/archify.
func (m *machine) pendingV2() {
	m.t.Helper()
	files := fsadapter.NewFileOps(m.p.Guard())
	lock := skillslock.New(m.p, files)
	e := engine.New(engine.Deps{
		Paths: m.p, Files: files, Links: fsadapter.NewLinkOps(m.p.Guard()),
		Lock: lock, DropLock: lock, State: statefile.New(m.p),
		Dirs: fsadapter.NewScanner(m.p.Guard()), Clock: m.clock,
	})
	b := e.NewBuilder(engine.Meta{Action: "unlink", Targets: []string{"claude-code"}})
	if err := b.Unlink(m.h.Path(".claude/skills/archify"), "../../.agents/skills/archify"); err != nil {
		m.t.Fatal(err)
	}
	plan, err := b.Build()
	if err != nil {
		m.t.Fatal(err)
	}
	run, err := e.Begin(m.t.Context(), plan)
	if err != nil {
		m.t.Fatal(err)
	}
	if err := run.Release(); err != nil {
		m.t.Fatal(err)
	}
	if !m.recordPending() {
		m.t.Fatal("no pending record")
	}
}

func (m *machine) recordPending() bool {
	_, err := os.Stat(engine.RecordFile(m.p))
	return err == nil
}

// On opening, a pending version 2 operation is told and recover offered;
// no other screen or write key acts, and nothing resolves it by itself.
func TestOpeningWithAManagedOperationPendingOffersRecover(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.pendingV2()
	before := mc.hash()

	m := open(t, a)
	got := screen(m)
	for _, want := range append(strings.Split(text.TUIManagedPending, "\n"), interruptedTitle, text.TUIManagedRecoverPrompt) {
		if !strings.Contains(got, want) {
			t.Errorf("the opening screen lacks %q:\n%s", want, got)
		}
	}
	// r and enter recover; no other key acts.
	for _, k := range []string{"2", "4", "tab", "u", "d", "a", "D", "z", "h", "c"} {
		if cmd := press(m, k); cmd != nil {
			t.Errorf("key %q with an operation pending returned a command", k)
		}
		if !strings.Contains(screen(m), interruptedTitle) {
			t.Errorf("key %q left the recover screen", k)
		}
	}
	if mc.hash() != before || !mc.recordPending() {
		t.Fatal("the TUI resolved or touched the pending operation by itself")
	}
}

// R resolves the version 2 operation through RecoverOperation: it rolls
// back, empties the install staging the record owned, tells it in the
// CLI's words and goes back to the screens.
func TestRecoverResolvesTheManagedOperation(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.pendingV2()
	staged := filepath.Join(mc.p.InstallStagingDir(), "leftover")
	writeTree(t, staged, map[string]string{"SKILL.md": "x\n"})

	m := open(t, a)
	pressRun(m, "R")

	if mc.recordPending() {
		t.Fatal("R did not resolve the pending record")
	}
	if _, err := os.Lstat(mc.p.InstallStagingDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the install staging is still there after recover: %v", err)
	}
	if !mc.exists(".claude/skills/archify") {
		t.Fatal("the rolled back operation left archify unlinked")
	}
	got := screen(m)
	if strings.Contains(got, interruptedTitle) {
		t.Errorf("the recover screen is still shown:\n%s", got)
	}
	if !strings.Contains(got, rolledBack) {
		t.Errorf("the result is not told:\n%s", got)
	}
}

// The recover of a version 1 operation is told in Spanish, with the CLI's
// texts, and also empties an orphan install staging.
func TestRecoverOfAVersion1OperationSpeaksSpanish(t *testing.T) {
	f := newFixture(t)
	f.home.WriteFile(".local/state/skillhub/state.json", `{
  "version": 1,
  "candidates": [],
  "skips": [],
  "checks": {"last_run": "0001-01-01T00:00:00Z", "last_success": "0001-01-01T00:00:00Z", "last_failed": false},
  "notified": {"candidate_ids": [], "check_failure": false},
  "operation": {
    "id": "op-1",
    "kind": "apply",
    "target": "lock:alpha",
    "candidate_id": "cand-1",
    "restore_snapshot_id": "snap-1",
    "started_at": "2026-11-21T10:00:00Z"
  }
}`)
	f.home.WriteFile(".agents/.skillhub/staging/leftover/SKILL.md", "x\n")
	a := f.newApp()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	runSyncCmd(m, m.Init())

	next, cmd := m.Update(keyPress("R"))
	m = next.(*tui.Model)
	runSyncCmd(m, cmd)

	got := m.View().Content
	for _, want := range []string{
		"La operación sobre lock:alpha no llegó a cambiar nada; se descartaron sus restos.",
		"La actualización sigue pendiente y puedes volver a aprobarla.",
		"Además se eliminó la copia de una instalación sin terminar.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the result lacks %q:\n%s", want, got)
		}
	}
	if _, ok, err := a.Interrupted(); err != nil || ok {
		t.Fatalf("Interrupted after recover = %v, %v", ok, err)
	}
	if _, err := os.Lstat(f.home.Paths().InstallStagingDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the orphan install staging is still there: %v", err)
	}
}

// A disabled skill is listed on the Skills screen as disabled, its panel
// tells since when and the agents it returns to, and A on its row plans
// enabling it with the row's key: no prompt, the summary, the plan and its
// identifier on v, and nothing written until it is confirmed.
func TestADisabledSkillIsEnabledFromItsRow(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	m := open(t, a)
	selectSkill(t, m, "agent-browser")

	got := screen(m)
	for _, want := range []string{"○ agent-browser", "Desactivada desde ", "Al activarla vuelve a estar en Claude Code", text.TUIActionEnable} {
		if !strings.Contains(got, want) {
			t.Errorf("the disabled row lacks %q:\n%s", want, got)
		}
	}
	before := mc.hash()

	pressRun(m, "A")
	got = screen(m)
	if strings.Contains(got, text.TUIPromptEnable) {
		t.Fatalf("A on a disabled row asked for the key:\n%s", got)
	}
	pressRun(m, "v")
	got = screen(m)
	for _, want := range []string{"¿Activar agent-browser?", "Plan: Activar agent-browser", confirmPlan, "Identificador del plan: "} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan lacks %q:\n%s", want, got)
		}
	}
	if mc.hash() != before {
		t.Fatal("showing the plan wrote something")
	}

	pressRun(m, "s")
	if !mc.exists(".agents/skills/agent-browser") || !mc.exists(".claude/skills/agent-browser") {
		t.Fatal("the confirmed enable did not bring the folder and its link back")
	}
	if _, ok := mc.persisted().Parked["agent-browser"]; ok {
		t.Fatal("the confirmed enable left the skill parked")
	}
	if got := screen(m); !strings.Contains(got, done) || strings.Contains(got, "Desactivada desde ") {
		t.Errorf("after enabling, the result is not told or the row is still disabled:\n%s", got)
	}
}

// The plan of A on a disabled row is bound to the screen that asked, as
// every other plan: arriving on another screen, it is dropped unconfirmed.
func TestAPlanFromADisabledRowArrivingOnAnotherScreenIsDropped(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	m := open(t, a)
	selectSkill(t, m, "agent-browser")
	before := mc.hash()

	cmd := press(m, "A")
	pressRun(m, "2")
	runSyncCmd(m, cmd)
	pressRun(m, "s")

	got := screen(m)
	if strings.Contains(got, confirmPlan) || !strings.Contains(got, text.TUIStaleResult[:60]) {
		t.Errorf("the late plan was shown, or its drop not told:\n%s", got)
	}
	if mc.hash() != before {
		t.Fatal("a dropped plan wrote something")
	}
}

// 4 pressed before the data load leaves Salud waiting; the search starts
// by itself once the data arrive.
func TestDoctorOpenedBeforeTheDataLoadSearchesWhenTheyArrive(t *testing.T) {
	mc := newMachine(t)
	m := tui.NewModel(mc.newApp())
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
	load := m.Init()

	if cmd := press(m, "4"); cmd != nil {
		t.Fatal("4 before the data load already searched")
	}
	runSyncCmd(m, load)

	got := screen(m)
	if strings.Contains(got, text.TUISearching) || !strings.Contains(got, text.TUIFindingsHint) {
		t.Errorf("Doctor did not search once the data arrived:\n%s", got)
	}
}
