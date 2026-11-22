package tui_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/tui"
)

func waitForText(t *testing.T, tm *teatest.TestModel, substr string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte(substr))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
}

func newTestTUI(t *testing.T, a *app.App) *teatest.TestModel {
	t.Helper()
	m := tui.NewModel(a)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() {
		_ = tm.Quit()
	})
	return tm
}

func TestTeatestApplyOneUpdate(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	tm := newTestTUI(t, a)
	waitForText(t, tm, "alpha")

	tm.Type("2")
	waitForText(t, tm, "Actualizaciones (1)")

	tm.Type("a")
	waitForText(t, tm, "¿Actualizar alpha?")

	tm.Type("s")
	waitForText(t, tm, "está actualizada")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	got, err := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Alpha v2\n" {
		t.Errorf("SKILL.md after apply = %q, want v2", string(got))
	}
}

func TestTeatestApplyAllUpdates(t *testing.T) {
	f := newFixture(t)
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v1\n"})
	f.publish("beta", map[string]string{"SKILL.md": "# Beta v1\n"})
	f.installLock([]string{"alpha", "beta"}, map[string]map[string]string{
		"alpha": {"SKILL.md": "# Alpha v1\n"},
		"beta":  {"SKILL.md": "# Beta v1\n"},
	})
	// Modify beta locally so ApplyAll excludes it.
	writeTree(t, filepath.Join(f.home.Paths().StoreDir(), "beta"), map[string]string{"SKILL.md": "# Beta local\n"})
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v2\n"})
	f.publish("beta", map[string]string{"SKILL.md": "# Beta v2\n"})

	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	tm := newTestTUI(t, a)
	waitForText(t, tm, "alpha")

	tm.Type("2")
	waitForText(t, tm, "Actualizaciones (")

	tm.Type("t")
	waitForText(t, tm, "¿Aplicar todas las actualizaciones?")

	tm.Type("s")
	waitForText(t, tm, "lock:alpha: aplicada")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	gotAlpha, _ := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md"))
	gotBeta, _ := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "beta", "SKILL.md"))
	if string(gotAlpha) != "# Alpha v2\n" {
		t.Errorf("alpha = %q, want v2", string(gotAlpha))
	}
	if string(gotBeta) != "# Beta local\n" {
		t.Errorf("beta = %q, want local edits preserved", string(gotBeta))
	}
}

func TestTeatestSkipUpdate(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	tm := newTestTUI(t, a)
	waitForText(t, tm, "alpha")

	tm.Type("2")
	waitForText(t, tm, "Actualizaciones (")

	tm.Type("o")
	waitForText(t, tm, "Actualización omitida")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	cands, err := a.Candidates()
	if err != nil || len(cands) != 0 {
		t.Errorf("pending candidates after skip = %+v, err=%v", cands, err)
	}
}

func TestTeatestUndoFromHistory(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	cand, err := a.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(t.Context(), struct {
		CandidateID string
		ToSHA       string
	}{CandidateID: cand.ID, ToSHA: cand.ToSHA}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	tm := newTestTUI(t, a)
	waitForText(t, tm, "alpha")

	tm.Type("h")
	waitForText(t, tm, "Actualizaste alpha")

	tm.Type("u")
	waitForText(t, tm, "se deshizo la actualización")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	got, _ := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md"))
	if string(got) != "# Alpha v1\n" {
		t.Errorf("SKILL.md after undo = %q, want v1", string(got))
	}
}

func TestTeatestChangeInterval(t *testing.T) {
	f := newFixture(t)
	a := f.newApp()

	tm := newTestTUI(t, a)
	waitForText(t, tm, "1 Skills")

	tm.Type("c")
	waitForText(t, tm, "Intervalo de revisión")

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, tm, "Nuevo intervalo")

	tm.Type("2h")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	// The renderer redraws only the changed cells, so "2h0m0s" may never be
	// written whole; the saved notice is, and the value is read back below.
	waitForText(t, tm, "Ajustes guardados")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	s, err := a.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if s.CheckInterval != 2*time.Hour {
		t.Errorf("CheckInterval = %v, want 2h", s.CheckInterval)
	}
}

func TestTeatestRecoverInterruptedOperation(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	cand, err := a.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	// Apply then simulate interrupted undo or write an interrupted operation pointing to a real snapshot.
	res, err := a.Apply(t.Context(), struct {
		CandidateID string
		ToSHA       string
	}{CandidateID: cand.ID, ToSHA: cand.ToSHA})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Inject an interrupted operation into state.json using the real snapshot.
	stBytes, err := os.ReadFile(f.home.Paths().StateFile())
	if err != nil {
		t.Fatal(err)
	}
	stStr := strings.TrimSuffix(strings.TrimSpace(string(stBytes)), "}")
	stStr += `,
  "operation": {
    "id": "op-int",
    "kind": "undo",
    "target": "lock:alpha",
    "restore_snapshot_id": "` + res.SnapshotID + `",
    "started_at": "2026-11-21T10:00:00Z"
  }
}`
	f.home.WriteFile(".local/state/skillhub/state.json", stStr)

	tm := newTestTUI(t, a)
	waitForText(t, tm, "Operación interrumpida detectada")

	tm.Type("R")
	waitForText(t, tm, "Se recuperó lock:alpha")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	if _, hasOp, err := a.Interrupted(); err != nil || hasOp {
		t.Errorf("Interrupted after recover = %v, err=%v", hasOp, err)
	}
	got, _ := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md"))
	if string(got) != "# Alpha v1\n" {
		t.Errorf("SKILL.md after recover = %q, want v1", string(got))
	}
}

func TestTeatestRejectsReplacedCandidateDuringReview(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	tm := newTestTUI(t, a)
	waitForText(t, tm, "alpha")

	tm.Type("2")
	waitForText(t, tm, "Actualizaciones (")

	// Open diff for v2 candidate.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, tm, "+# Alpha v2")

	// Upstream publishes v3 and another process (or check) supersedes v2 while user is viewing diff.
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v3\n"})
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("second Check: %v", err)
	}

	// User presses 'a' on the old diff, then confirms 's' -> must be rejected and load new candidate!
	tm.Type("a")
	waitForText(t, tm, "¿Actualizar alpha?")
	tm.Type("s")
	waitForText(t, tm, "Aprobación rechazada")

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	// Installed folder must still be v1 (v2 was rejected and v3 was not auto-applied).
	got, _ := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md"))
	if string(got) != "# Alpha v1\n" {
		t.Errorf("SKILL.md = %q, want v1 (rejected apply must not change disk)", string(got))
	}
}

func TestTeatestConfirmNeverSwitchesCandidateOnCheck(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	tm := newTestTUI(t, a)
	waitForText(t, tm, "alpha")

	tm.Type("2")
	waitForText(t, tm, "Actualizaciones (")

	// Open confirmation for v2.
	tm.Type("a")
	waitForText(t, tm, "¿Actualizar alpha?")

	// Publish v3 and complete a check while confirmation is open.
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v3\n"})
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check 2: %v", err)
	}
	cands, err := a.Candidates()
	if err != nil {
		t.Fatal(err)
	}
	tm.Send(tui.NewCheckFinishedMsgForTest(cands))
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("el candidato cambió durante la revisión")) &&
			bytes.Contains(bts, []byte("+# Alpha v3"))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	// Pressing 'y' does nothing in modeDiff (would only confirm in modeConfirmApply).
	tm.Type("y")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	got, _ := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md"))
	if string(got) != "# Alpha v1\n" {
		t.Errorf("SKILL.md = %q, want v1 (neither v2 nor v3 may be applied)", string(got))
	}
}

func TestTeatestUndoWhenFolderChangedNeedsConfirmation(t *testing.T) {
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
	if _, err := a.Apply(t.Context(), struct {
		CandidateID string
		ToSHA       string
	}{CandidateID: cand.ID, ToSHA: cand.ToSHA}); err != nil {
		t.Fatal(err)
	}
	// Modify installed folder after apply.
	writeTree(t, filepath.Join(f.home.Paths().StoreDir(), "alpha"), map[string]string{"SKILL.md": "# Alpha edited\n"})

	tm := newTestTUI(t, a)
	waitForText(t, tm, "alpha")

	tm.Type("h")
	waitForText(t, tm, "Actualizaste alpha")

	tm.Type("u")
	waitForText(t, tm, "¿Deshacer la actualización de alpha?")

	// Confirming 's' restores v1.
	tm.Type("s")
	waitForText(t, tm, "se deshizo la actualización")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	got, _ := os.ReadFile(filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md"))
	if string(got) != "# Alpha v1\n" {
		t.Errorf("SKILL.md after confirmed undo = %q, want v1", string(got))
	}
}

func setupGoldenFixture(t *testing.T) *app.App {
	t.Helper()
	f := newFixture(t)
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v1\n"})
	f.publish("beta", map[string]string{"SKILL.md": "# Beta v1\n"})
	f.publish("gamma", map[string]string{"SKILL.md": "# Gamma v1\n"})
	f.installLock([]string{"alpha", "beta", "gamma"}, map[string]map[string]string{
		"alpha": {"SKILL.md": "# Alpha v1\n"},
		"beta":  {"SKILL.md": "# Beta v1\n"},
		"gamma": {"SKILL.md": "# Gamma v1\n"},
	})
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v2\n"})
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	cand, err := a.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(t.Context(), struct {
		CandidateID string
		ToSHA       string
	}{CandidateID: cand.ID, ToSHA: cand.ToSHA}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Modify gamma locally, then publish v3 for alpha and v2 for gamma.
	writeTree(t, filepath.Join(f.home.Paths().StoreDir(), "gamma"), map[string]string{"SKILL.md": "# Gamma local\n"})
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v3\n"})
	f.publish("gamma", map[string]string{"SKILL.md": "# Gamma v2\n"})
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check 2: %v", err)
	}
	// Also append a blocked candidate in state.json so the Updates golden shows
	// normal, locally_modified, and blocked with its reason.
	stBytes, err := os.ReadFile(f.home.Paths().StateFile())
	if err != nil {
		t.Fatal(err)
	}
	blockedJSON := `{"id":"cand-blocked","target":"git:.agents/skills/myclone","skills":["git:.agents/skills/myclone"],"from_sha":"1111111111","installed_sha_at_stage":"1111111111","to_commit":"2222222222","to_sha":"3333333333","staged_path":"","file_changes":[],"frontmatter_delta":{"changed":[]},"labels":["text_only"],"ruleset_version":1,"first_seen_at":"2026-11-21T10:00:00Z","state":"blocked","block_reason":"dirty_worktree"}`
	stStr := strings.Replace(string(stBytes), `"candidates": [`, `"candidates": [`+"\n    "+blockedJSON+",", 1)
	f.home.WriteFile(".local/state/skillhub/state.json", stStr)
	return a
}

func renderScreenGolden(t *testing.T, a *app.App, screenKey string) []byte {
	t.Helper()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// Run Init synchronously for deterministic golden output.
	cmd := m.Init()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(*tui.Model)
	}
	if screenKey != "1" {
		next, _ := m.Update(tea.KeyPressMsg{Code: []rune(screenKey)[0], Text: screenKey})
		m = next.(*tui.Model)
	}
	return []byte(m.View().Content)
}

// renderDiffGolden opens the diff of a locally modified gamma and presses
// keys in it.
func renderDiffGolden(t *testing.T, publishRegistered bool, keys ...string) []byte {
	t.Helper()
	f := newFixture(t)
	f.setupLocallyModified("gamma", "# Gamma v1\nshared\n", "# Gamma v1\nshared\nmine\n", "# Gamma v2\nshared\n", publishRegistered)
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press := func(msg tea.Msg) {
		next, cmd := m.Update(msg)
		m = next.(*tui.Model)
		for cmd != nil {
			out := cmd()
			if out == nil {
				break
			}
			next, cmd = m.Update(out)
			m = next.(*tui.Model)
		}
	}
	cmd := m.Init()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(*tui.Model)
	}
	press(tea.KeyPressMsg{Code: '2', Text: "2"})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, k := range keys {
		press(tea.KeyPressMsg{Code: []rune(k)[0], Text: k})
	}
	return []byte(m.View().Content)
}

func TestGoldenDiffUserChanges(t *testing.T) {
	teatest.RequireEqualOutput(t, renderDiffGolden(t, true))
}

func TestGoldenDiffAuthorChanges(t *testing.T) {
	teatest.RequireEqualOutput(t, renderDiffGolden(t, true, "v"))
}

func TestGoldenDiffRegisteredVersionGone(t *testing.T) {
	teatest.RequireEqualOutput(t, renderDiffGolden(t, false))
}

func TestGoldenScreenSkills(t *testing.T) {
	a := setupGoldenFixture(t)
	teatest.RequireEqualOutput(t, renderScreenGolden(t, a, "1"))
}

func TestGoldenScreenUpdates(t *testing.T) {
	a := setupGoldenFixture(t)
	teatest.RequireEqualOutput(t, renderScreenGolden(t, a, "2"))
}

func TestGoldenScreenHistory(t *testing.T) {
	a := setupGoldenFixture(t)
	teatest.RequireEqualOutput(t, renderScreenGolden(t, a, "h"))
}

func TestGoldenScreenSettings(t *testing.T) {
	a := setupGoldenFixture(t)
	teatest.RequireEqualOutput(t, renderScreenGolden(t, a, "c"))
}
