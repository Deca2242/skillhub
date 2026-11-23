package tui_test

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

func runSyncCmd(m *tui.Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			runSyncCmd(m, sub)
		}
		return
	}
	next, nextCmd := m.Update(msg)
	*m = *next.(*tui.Model)
	runSyncCmd(m, nextCmd)
}

func TestUpdateScreenTransitionsAndGlobalKeys(t *testing.T) {
	f := newFixture(t)
	a := f.newApp()

	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())

	if m.ActiveScreen() != tui.ScreenSkills {
		t.Fatalf("initial screen = %v, want ScreenSkills", m.ActiveScreen())
	}

	// Number keys 1..4 switch directly; h and c open Historial and Ajustes
	// (UX3).
	for _, tc := range []struct {
		key  string
		want tui.Screen
	}{
		{"2", tui.ScreenUpdates},
		{"3", tui.ScreenInstall},
		{"4", tui.ScreenDoctor},
		{"h", tui.ScreenHistory},
		{"c", tui.ScreenSettings},
		{"1", tui.ScreenSkills},
	} {
		next, _ := m.Update(keyPress(tc.key))
		m = next.(*tui.Model)
		if m.ActiveScreen() != tc.want {
			t.Errorf("after key %q: screen = %v, want %v", tc.key, m.ActiveScreen(), tc.want)
		}
	}

	// Tab cycles through the four tabs.
	for _, want := range []tui.Screen{tui.ScreenUpdates, tui.ScreenInstall, tui.ScreenDoctor, tui.ScreenSkills} {
		next, _ := m.Update(keyPress("tab"))
		m = next.(*tui.Model)
		if m.ActiveScreen() != want {
			t.Errorf("after tab: screen = %v, want %v", m.ActiveScreen(), want)
		}
	}

	// ? toggles help.
	next, _ := m.Update(keyPress("?"))
	m = next.(*tui.Model)
	if !strings.Contains(m.View().Content, "Atajos de teclado") {
		t.Errorf("expected help overlay after '?'")
	}
	next, _ = m.Update(keyPress("?"))
	m = next.(*tui.Model)
	if strings.Contains(m.View().Content, "Atajos de teclado") {
		t.Errorf("expected help overlay to close after second '?'")
	}
}

func TestUpdateBlockedCandidateRejectsApplyKey(t *testing.T) {
	f := newFixture(t)
	f.home.WriteFile(".local/state/skillhub/state.json", `{
  "version": 1,
  "candidates": [
    {
      "id": "cand-blocked",
      "target": "git:.agents/skills/myclone",
      "skills": ["git:.agents/skills/myclone"],
      "from_sha": "1111111111",
      "installed_sha_at_stage": "1111111111",
      "to_commit": "2222222222",
      "to_sha": "3333333333",
      "staged_path": "",
      "file_changes": [],
      "frontmatter_delta": {"changed": []},
      "labels": ["text_only"],
      "ruleset_version": 1,
      "first_seen_at": "2026-11-21T10:00:00Z",
      "state": "blocked",
      "block_reason": "dirty_worktree"
    }
  ],
  "skips": [],
  "checks": {"last_run": "2026-11-21T10:00:00Z", "last_success": "2026-11-21T10:00:00Z", "last_failed": false},
  "notified": {"candidate_ids": [], "check_failure": false}
}`)

	a := f.newApp()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())

	next, _ := m.Update(keyPress("2"))
	m = next.(*tui.Model)
	next, cmd := m.Update(keyPress("a"))
	m = next.(*tui.Model)
	if cmd != nil {
		t.Errorf("blocked candidate must not trigger an apply command")
	}
	if !strings.Contains(m.View().Content, "bloqueado") {
		t.Errorf("expected blocked notice in view:\n%s", m.View().Content)
	}
}

func TestUpdateApplyRequiresExplicitConfirmation(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())

	// Switch to Updates and press 'a'.
	next, _ := m.Update(keyPress("2"))
	m = next.(*tui.Model)
	next, cmd := m.Update(keyPress("a"))
	m = next.(*tui.Model)
	if cmd != nil {
		t.Fatalf("pressing 'a' must open confirmation without running apply")
	}
	if !strings.Contains(m.View().Content, "¿Actualizar alpha?") {
		t.Fatalf("expected confirmation prompt, got:\n%s", m.View().Content)
	}
	// Cancel with 'n' must not apply.
	next, cmd = m.Update(keyPress("n"))
	m = next.(*tui.Model)
	if cmd != nil {
		t.Fatalf("pressing 'n' must cancel without running apply")
	}
	cands, err := a.Candidates()
	if err != nil || len(cands) != 1 || cands[0].State != domain.CandidatePending {
		t.Fatalf("candidate should still be pending after cancel: %+v, err=%v", cands, err)
	}
}

// While a check is running its result may replace the candidate on screen, so
// neither 'a' nor 'A' may open a confirmation until it finishes.
func TestUpdateApplyKeysDoNothingWhileACheckRuns(t *testing.T) {
	for _, key := range []string{"a", "A"} {
		t.Run(key, func(t *testing.T) {
			f := newFixture(t)
			f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
			a := f.newApp()
			if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
				t.Fatalf("Check: %v", err)
			}

			m := tui.NewModel(a)
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			runSyncCmd(m, m.Init())

			next, _ := m.Update(keyPress("2"))
			m = next.(*tui.Model)
			// Start a check and leave it running: its command is not executed.
			next, cmd := m.Update(keyPress("r"))
			m = next.(*tui.Model)
			if cmd == nil {
				t.Fatalf("pressing 'r' must start a check")
			}
			next, cmd = m.Update(keyPress(key))
			m = next.(*tui.Model)
			if cmd != nil {
				t.Fatalf("pressing %q during a check must not run anything", key)
			}
			if view := m.View().Content; strings.Contains(view, "¿Actualizar") {
				t.Fatalf("pressing %q during a check opened a confirmation:\n%s", key, view)
			}
		})
	}
}

func TestUpdateInterruptedOperationBlocksOtherActions(t *testing.T) {
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
	a := f.newApp()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())

	if !strings.Contains(m.View().Content, "Operación interrumpida detectada") {
		t.Fatalf("expected interrupted operation view, got:\n%s", m.View().Content)
	}

	// Pressing 2, tab, or u must NOT leave recovery mode or run check; r
	// and enter recover (L79).
	for _, k := range []string{"2", "tab", "u"} {
		next, cmd := m.Update(keyPress(k))
		m = next.(*tui.Model)
		if cmd != nil {
			t.Errorf("key %q in recovery mode returned non-nil cmd", k)
		}
		if !strings.Contains(m.View().Content, "Operación interrumpida detectada") {
			t.Errorf("key %q escaped recovery mode", k)
		}
	}
}

var _ = domain.Skill{}

func TestUpdateSkillsFilterDetailAndIgnore(t *testing.T) {
	f := newFixture(t)
	f.installLock([]string{"alpha", "beta"}, map[string]map[string]string{
		"alpha": {"SKILL.md": "# Alpha\n"},
		"beta":  {"SKILL.md": "# Beta\n"},
	})
	a := f.newApp()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())

	// '/' filters by text.
	next, _ := m.Update(keyPress("/"))
	m = next.(*tui.Model)
	for _, r := range "beta" {
		next, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(*tui.Model)
	}
	next, _ = m.Update(keyPress("enter"))
	m = next.(*tui.Model)
	if !strings.Contains(m.View().Content, "beta") || strings.Contains(m.View().Content, "alpha") {
		t.Fatalf("filtered view should show beta and hide alpha:\n%s", m.View().Content)
	}

	// 'enter' opens skill detail.
	next, _ = m.Update(keyPress("enter"))
	m = next.(*tui.Model)
	if !strings.Contains(m.View().Content, "Skill: beta") || !strings.Contains(m.View().Content, "Ubicaciones") {
		t.Fatalf("expected skill detail view, got:\n%s", m.View().Content)
	}
	next, _ = m.Update(keyPress("esc"))
	m = next.(*tui.Model)

	// 'x' ignores selected skill (beta).
	next, cmd := m.Update(keyPress("x"))
	m = next.(*tui.Model)
	runSyncCmd(m, cmd)
	s, err := a.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Ignore) != 1 || s.Ignore[0] != "beta" {
		t.Errorf("Ignore after 'x' = %v, want [beta]", s.Ignore)
	}
}

func TestUpdateIgnoreRefusesCommaInSkillName(t *testing.T) {
	f := newFixture(t)
	f.installLock([]string{"alpha,one"}, map[string]map[string]string{
		"alpha,one": {"SKILL.md": "# Alpha\n"},
	})
	a := f.newApp()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())

	next, cmd := m.Update(keyPress("x"))
	m = next.(*tui.Model)
	if cmd != nil {
		runSyncCmd(m, cmd)
	}
	s, err := a.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Ignore) != 0 {
		t.Errorf("comma name must not be split into Ignore: %v", s.Ignore)
	}
	if !strings.Contains(m.View().Content, "coma") {
		t.Errorf("expected comma warning in view:\n%s", m.View().Content)
	}
}

func TestUpdateDiffOpenScrollCloseAndSettingsNotificationsToggle(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}

	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())

	// Open diff in Updates.
	next, _ := m.Update(keyPress("2"))
	m = next.(*tui.Model)
	next, cmd := m.Update(keyPress("enter"))
	m = next.(*tui.Model)
	runSyncCmd(m, cmd)
	if !strings.Contains(m.View().Content, "+# Alpha v2") {
		t.Fatalf("expected diff view, got:\n%s", m.View().Content)
	}
	// Close diff with esc.
	next, _ = m.Update(keyPress("esc"))
	m = next.(*tui.Model)
	if strings.Contains(m.View().Content, "+# Alpha v2") {
		t.Fatalf("expected esc to exit diff view")
	}

	// Toggle notifications in Settings.
	next, _ = m.Update(keyPress("c"))
	m = next.(*tui.Model)
	next, _ = m.Update(keyPress("down"))
	m = next.(*tui.Model)
	next, cmd = m.Update(keyPress("enter"))
	m = next.(*tui.Model)
	runSyncCmd(m, cmd)
	s, err := a.Settings()
	if err != nil || s.Notifications {
		t.Errorf("Notifications after first toggle = %v, err=%v; want false", s.Notifications, err)
	}
	next, cmd = m.Update(keyPress("enter"))
	m = next.(*tui.Model)
	runSyncCmd(m, cmd)
	s, err = a.Settings()
	if err != nil || !s.Notifications {
		t.Errorf("Notifications after second toggle = %v, err=%v; want true", s.Notifications, err)
	}
}

// openDiff opens the diff of the first pending update of a fresh model.
func openDiff(t *testing.T, a *app.App) *tui.Model {
	t.Helper()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runSyncCmd(m, m.Init())
	next, _ := m.Update(keyPress("2"))
	m = next.(*tui.Model)
	next, cmd := m.Update(keyPress("enter"))
	m = next.(*tui.Model)
	runSyncCmd(m, cmd)
	return m
}

func TestUpdateDiffTogglesTheUserAndAuthorChanges(t *testing.T) {
	f := newFixture(t)
	f.setupLocallyModified("gamma", "# Gamma v1\n", "# Gamma mine\n", "# Gamma v2\n", true)
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	m := openDiff(t, a)

	user := m.View().Content
	for _, want := range []string{"Vista: Tus cambios (registrada → instalada)", "+# Gamma mine", "v cambiar vista"} {
		if !strings.Contains(user, want) {
			t.Errorf("user view lacks %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "+# Gamma v2") {
		t.Errorf("user view shows the author's change:\n%s", user)
	}

	next, _ := m.Update(keyPress("v"))
	m = next.(*tui.Model)
	author := m.View().Content
	for _, want := range []string{"Vista: Cambios del autor (registrada → upstream)", "+# Gamma v2"} {
		if !strings.Contains(author, want) {
			t.Errorf("author view lacks %q:\n%s", want, author)
		}
	}
	if strings.Contains(author, "+# Gamma mine") {
		t.Errorf("author view shows the user's change:\n%s", author)
	}

	next, _ = m.Update(keyPress("v"))
	m = next.(*tui.Model)
	if got := m.View().Content; !strings.Contains(got, "Vista: Tus cambios (registrada → instalada)") {
		t.Errorf("second v did not return to the user's changes:\n%s", got)
	}

	// The apply key still asks for confirmation from either view.
	next, _ = m.Update(keyPress("a"))
	m = next.(*tui.Model)
	if got := m.View().Content; !strings.Contains(got, "¿Actualizar gamma?") {
		t.Errorf("a from the split diff did not ask for confirmation:\n%s", got)
	}
}

func TestUpdateDiffWithoutTheRegisteredVersionShowsOneDiff(t *testing.T) {
	f := newFixture(t)
	f.setupLocallyModified("gamma", "# Gamma v1\n", "# Gamma mine\n", "# Gamma v2\n", false)
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	m := openDiff(t, a)

	got := m.View().Content
	for _, want := range []string{"Vista: instalada → upstream", "ya no está en upstream", "-# Gamma mine", "+# Gamma v2"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff lacks %q:\n%s", want, got)
		}
	}
	next, _ := m.Update(keyPress("v"))
	m = next.(*tui.Model)
	if after := m.View().Content; after != got {
		t.Errorf("v changed a single diff:\n%s", after)
	}
}

func TestUpdateRejectionReasonsAreSpecific(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantMsg   string
		opensDiff bool
	}{
		{
			name:      "candidate replaced",
			err:       updates.ErrNotPending,
			wantMsg:   "el candidato cambió durante la revisión",
			opensDiff: true,
		},
		{
			name:    "installed changed",
			err:     updates.ErrInstalledChanged,
			wantMsg: "la carpeta instalada cambió",
		},
		{
			name:    "staged changed",
			err:     updates.ErrStagedChanged,
			wantMsg: "la copia preparada cambió",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
			a := f.newApp()
			if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
				t.Fatal(err)
			}
			cand, err := a.CandidateFor("lock:alpha")
			if err != nil {
				t.Fatal(err)
			}

			m := tui.NewModel(a)
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			runSyncCmd(m, m.Init())

			wrapped := errors.Join(updates.ErrRejected, tc.err)
			next, cmd := m.Update(tui.NewApplyFinishedMsgForTest(
				updates.ApplyResult{Target: "lock:alpha", CandidateID: cand.ID, Outcome: updates.OutcomeRejected},
				&cand,
				wrapped,
			))
			m = next.(*tui.Model)
			if !strings.Contains(m.View().Content, tc.wantMsg) {
				t.Errorf("view = %q, want %q", m.View().Content, tc.wantMsg)
			}
			if tc.opensDiff {
				if cmd == nil {
					t.Fatalf("expected cmd to load new candidate diff")
				}
				runSyncCmd(m, cmd)
				if !strings.Contains(m.View().Content, "+# Alpha v2") {
					t.Errorf("expected diff of new candidate to open:\n%s", m.View().Content)
				}
			}
		})
	}
}

func TestUpdateTablesFit80And100Columns(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	cand, _ := a.CandidateFor("lock:alpha")
	_, _ = a.Apply(t.Context(), updates.Approval{CandidateID: cand.ID, ToSHA: cand.ToSHA})
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v3\n"})
	_, _ = a.Check(t.Context(), app.CheckOptions{})

	for _, width := range []int{80, 100} {
		m := tui.NewModel(a)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		runSyncCmd(m, m.Init())

		for _, scrKey := range []string{"1", "2", "3", "4", "h", "c"} {
			next, _ := m.Update(keyPress(scrKey))
			m = next.(*tui.Model)
			content := m.View().Content
			for i, line := range strings.Split(content, "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("width=%d screen=%s line %d exceeds width (%d > %d): %q",
						width, scrKey, i+1, w, width, ansi.Strip(line))
				}
			}
			if scrKey == "1" && !strings.Contains(ansi.Strip(content), "Hay una actualización") {
				t.Errorf("at width %d, expected the panel to tell the update in Skills:\n%s", width, ansi.Strip(content))
			}
		}
	}
}

// Before the initial data arrives, no screen may claim that there is
// nothing: it says that it is loading.
func TestScreensSayLoadingBeforeInitialLoad(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	m := tui.NewModel(f.newApp())
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// Intentionally do NOT run Init() yet.
	for _, screen := range []string{"1", "2", "h"} {
		next, _ := m.Update(keyPress(screen))
		m = next.(*tui.Model)
		view := m.View().Content
		if !strings.Contains(view, "Cargando datos...") {
			t.Errorf("screen %s before load lacks the loading notice:\n%s", screen, view)
		}
		for _, empty := range []string{"No se encontraron skills instaladas.", text.TUIUpdatesNeverChecked, "No hay entradas en el historial."} {
			if strings.Contains(view, empty) {
				t.Errorf("screen %s before load claims %q", screen, empty)
			}
		}
	}
	runSyncCmd(m, m.Init())
	next, _ := m.Update(keyPress("1"))
	m = next.(*tui.Model)
	if view := m.View().Content; strings.Contains(view, "Cargando datos...") || !strings.Contains(view, "alpha") {
		t.Errorf("after load the skills screen should list alpha without the loading notice:\n%s", view)
	}
}

func TestUpdateBlocksMutatingActionsBeforeInitialLoad(t *testing.T) {
	f := newFixture(t)
	a := f.newApp()
	m := tui.NewModel(a)
	// Intentionally do NOT run Init() / dataLoadedMsg yet.
	for _, k := range []string{"r", "x", "a", "A", "s", "u"} {
		_, cmd := m.Update(keyPress(k))
		if cmd != nil {
			t.Errorf("key %q before initial load returned non-nil cmd", k)
		}
	}
	m.Update(keyPress("c"))
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		t.Errorf("editing settings before initial load returned non-nil cmd")
	}
	if strings.Contains(m.View().Content, "Nuevo intervalo") {
		t.Errorf("interval edit opened before initial load")
	}
}
