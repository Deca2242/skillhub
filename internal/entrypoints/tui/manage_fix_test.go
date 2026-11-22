package tui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/updates"
)

// stale is the start of the warning a result that arrives too late gets.
var stale = strings.SplitN(text.TUIStaleResult, ";", 2)[0]

// B1: a revision resolved after the user left Instalar changes no mode: the
// Skills screen keeps its keys, the user is told, and the prepared copy is
// discarded.
func TestAResolvedInstallArrivingOnAnotherScreenChangesNoMode(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	m := open(t, mc.newApp())
	before := mc.hash()

	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/tools")
	resolving := press(m, "enter")
	pressRun(m, "1")
	runSyncCmd(m, resolving)

	got := screen(m)
	if strings.Contains(got, "Revisión de tidy") || !strings.Contains(got, stale) {
		t.Fatalf("the late review took over, or the user is not told:\n%s", got)
	}
	pressRun(m, "/")
	if !strings.Contains(screen(m), "Filtro: ") {
		t.Fatalf("the Skills screen lost its keys to a hidden review:\n%s", screen(m))
	}
	pressRun(m, "esc")
	pressRun(m, "3")
	if strings.Contains(screen(m), "Revisión de tidy") {
		t.Fatalf("Instalar shows a review nobody is waiting for:\n%s", screen(m))
	}
	if mc.hash() != before {
		t.Fatal("the discarded review left its prepared copy")
	}
}

// B2: a plan that arrives while the user types in the filter is never
// confirmed by the next key.
func TestAPlanArrivingInAnotherModeIsNeverConfirmed(t *testing.T) {
	for _, tt := range []struct {
		name  string
		leave []string
	}{
		{"filter", []string{"/"}},
		{"another screen", []string{"c"}},
		{"help", []string{"?"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMachine(t)
			m := open(t, mc.newApp())
			selectSkill(t, m, "agent-browser")
			before := mc.hash()

			planning := press(m, "D")
			for _, k := range tt.leave {
				pressRun(m, k)
			}
			runSyncCmd(m, planning)
			pressRun(m, "s")
			pressRun(m, "enter")

			if mc.hash() != before || len(mc.v2Lines()) != 0 {
				t.Fatal("a key typed for another mode confirmed the plan")
			}
			got := screen(m)
			if strings.Contains(got, confirmPlan) || !strings.Contains(got, stale) {
				t.Fatalf("the late plan was shown, or the user is not told:\n%s", got)
			}
		})
	}
}

// A changed plan returned after the user left the Skills screen is not
// shown: nothing ran, and the next key confirms nothing.
func TestAChangedPlanArrivingInAnotherModeIsNotShown(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")
	pressRun(m, "D")
	mc.h.Symlink(".gemini/skills/agent-browser", "../../.agents/skills/agent-browser")
	before := mc.hash()

	applying := press(m, "s")
	pressRun(m, "/")
	runSyncCmd(m, applying)
	pressRun(m, "s")
	pressRun(m, "enter")

	if mc.hash() != before {
		t.Fatal("the changed plan ran")
	}
	if got := screen(m); strings.Contains(got, confirmPlan) {
		t.Fatalf("the changed plan was shown in another mode:\n%s", got)
	}
}

// The repairs of a finding read after the user left Doctor open no list.
func TestRepairsArrivingOnAnotherScreenOpenNoList(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".claude/skills/ghost", "../../.agents/skills/ghost")
	a := mc.newApp()
	m := open(t, a)
	pressRun(m, "4")
	for range findingIndex(t, a, "broken_link:.claude/skills/ghost") {
		pressRun(m, "down")
	}
	reading := press(m, "enter")
	pressRun(m, "1")
	runSyncCmd(m, reading)
	before := mc.hash()
	pressRun(m, "enter")
	pressRun(m, "enter")
	got := screen(m)
	if strings.Contains(got, "Reparaciones de") || strings.Contains(got, confirmPlan) || mc.hash() != before {
		t.Fatalf("late repairs opened their list on Skills:\n%s", got)
	}
}

// The version 1 undo that needs a confirmation does not ask it once the
// user left Historial, and the next key undoes nothing.
func TestAnUndoConfirmationArrivingOnAnotherScreenIsNotAsked(t *testing.T) {
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
	if _, err := a.Apply(t.Context(), updates.Approval{CandidateID: cand.ID, ToSHA: cand.ToSHA}); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md")
	writeTree(t, filepath.Dir(skill), map[string]string{"SKILL.md": "# Alpha edited\n"})

	m := open(t, a)
	pressRun(m, "h")
	undoing := press(m, "u")
	pressRun(m, "1")
	runSyncCmd(m, undoing)
	got := screen(m)
	if strings.Contains(got, "¿Deshacer la actualización de") || !strings.Contains(got, stale) {
		t.Fatalf("the late confirmation was asked on another screen:\n%s", got)
	}
	pressRun(m, "s")
	if b, _ := os.ReadFile(skill); string(b) != "# Alpha edited\n" {
		t.Fatalf("a key typed for Skills undid the update: %q", b)
	}
}

// Cancelling an install at any step discards its prepared copy: the home,
// the install staging included, is as it was.
func TestCancellingAnInstallDiscardsThePreparedCopy(t *testing.T) {
	for _, tt := range []struct {
		name string
		keys []string
	}{
		{"esc in the review", []string{"esc"}},
		{"esc in the targets", []string{"enter", "esc"}},
		{"n in the plan", []string{"enter", "enter", "n"}},
		{"esc in the plan", []string{"enter", "enter", "esc"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMachine(t)
			mc.publishTool()
			m := open(t, mc.newApp())
			before := mc.hash()
			pressRun(m, "3")
			pressRun(m, "enter")
			typeText(m, "acme/tools")
			pressRun(m, "enter")
			for _, k := range tt.keys {
				pressRun(m, k)
			}
			if mc.hash() != before {
				t.Fatalf("cancelling left the prepared copy:\n%s", screen(m))
			}
		})
	}
}

// In a terminal smaller than 80x24 the help scrolls to its last line.
func TestTheHelpFitsASmallTerminalAndScrolls(t *testing.T) {
	// At 80x24 the whole help fits (UX3, TestTheHelpIsByTabAndFits80x24);
	// a smaller terminal scrolls it.
	m := openSized(t, newMachine(t).newApp(), 80, 16)
	pressRun(m, "?")
	content := m.View().Content
	lines := strings.Split(content, "\n")
	if len(lines) > 16 {
		t.Fatalf("the help takes %d lines at 80x16:\n%s", len(lines), ansi.Strip(content))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line %d is %d wide", i+1, w)
		}
	}
	if !strings.Contains(ansi.Strip(content), "Atajos de teclado") || strings.Contains(ansi.Strip(content), "Pulsa ? o esc") {
		t.Fatalf("the help does not start at its title:\n%s", ansi.Strip(content))
	}
	for range 30 {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		*m = *next.(*tui.Model)
	}
	if !strings.Contains(screen(m), "Pulsa ? o esc para cerrar la ayuda.") {
		t.Fatalf("scrolling does not reach the end of the help:\n%s", screen(m))
	}
	pressRun(m, "esc")
	if strings.Contains(screen(m), "Atajos de teclado") {
		t.Fatal("esc did not close the help")
	}
}
