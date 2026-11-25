package tui_test

import (
	"image/color"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

// The Skills screen of S13: an icon and a short state per row, and the
// panel of the selected skill.

// Each row shows its state with an icon that keeps its meaning without
// colors: ● active, ↑ with an update, ○ disabled, ⚠ with problems or
// unmanaged.
func TestSkillsListShowsAnIconAndAShortStatePerRow(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	m := openSized(t, a, 100, 40)
	got := screen(m)
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`● archify +sin revisar`),
		regexp.MustCompile(`○ agent-browser +desactivada`),
		regexp.MustCompile(`⚠ humanizer +manual`),
		regexp.MustCompile(`⚠ orca-cli +modificada`),
	} {
		if !want.MatchString(got) {
			t.Errorf("the list lacks %v:\n%s", want, got)
		}
	}

	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	v1 := f.newApp()
	if _, err := v1.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := screen(openSized(t, v1, 100, 30)); !regexp.MustCompile(`↑ alpha +actualizar`).MatchString(got) {
		t.Errorf("a skill with an update lacks ↑:\n%s", got)
	}
}

// The panel tells what the selected skill is, the agents it is available
// in by their readable name, where it comes from, its problems in plain
// words and the actions its row admits, with their keys.
func TestSkillsPanelTellsTheSelectedSkill(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), 100, 40)

	selectSkill(t, m, "agent-browser")
	got := screen(m)
	for _, want := range []string{
		"Disponible en: Claude Code,", "Origen: owner/agent-browser",
		text.TUIActionDisable, text.TUIActionRemove, text.TUIActionAgents,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel of agent-browser lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "claude-code") || regexp.MustCompile(`(?i)harness`).MatchString(got) {
		t.Errorf("the Skills screen names an agent by its id, or says harness:\n%s", got)
	}

	m = openSized(t, mc.newApp(), 100, 40)
	selectSkill(t, m, "humanizer")
	got = screen(m)
	for _, want := range []string{"own copy", "Instalación manual", "Recomendado: [e] Eliminar", "Origen: " + text.TUIPanelNoOrigin, "No tiene un origen registrado", "skillhub no la gestiona"} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel of humanizer lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, text.TUIActionDisable) {
		t.Errorf("an unmanaged skill offers to disable it:\n%s", got)
	}
}

// A disabled skill's row offers to enable or remove it, in the panel and in
// the keys under it.
func TestADisabledRowOffersOnlyToEnable(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	m := openSized(t, a, 100, 40)
	selectSkill(t, m, "agent-browser")
	got := screen(m)
	for _, want := range []string{text.TUIActionEnable, text.TUIKeyEnable, text.TUIActionRemove, text.TUIKeyRemove} {
		if !strings.Contains(got, want) {
			t.Errorf("the disabled row does not offer %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{text.TUIActionDisable, text.TUIKeyDisable, text.TUIKeyAgents} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the disabled row offers %q:\n%s", unwanted, got)
		}
	}
}

// The line at the bottom shows the keys of the screen and of the row.
func TestTheFooterShowsTheKeysOfTheRow(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), 200, 40)
	selectSkill(t, m, "agent-browser")
	want := "d desactivar • e eliminar • l elegir agentes • enter ver todo • x ignorar • / buscar • f filtrar • g ver por agente • ? ayuda • q salir"
	lines := strings.Split(screen(m), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != want {
		t.Errorf("footer = %q, want %q", got, want)
	}
}

// From 100 columns the panel is beside the list; under 100 it goes under
// it. Nothing is wider than the terminal.
func TestThePanelGoesUnderTheListBelow100Columns(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	for _, tt := range []struct {
		width  int
		beside bool
	}{{100, true}, {80, false}} {
		m := openSized(t, a, tt.width, 30)
		selectSkill(t, m, "agent-browser")
		content := m.View().Content
		for i, l := range strings.Split(content, "\n") {
			if w := ansi.StringWidth(l); w > tt.width {
				t.Errorf("width %d: line %d is %d wide: %q", tt.width, i+1, w, ansi.Strip(l))
			}
		}
		var listRow, panelRow int
		for i, l := range strings.Split(ansi.Strip(content), "\n") {
			if strings.Contains(l, "Skills (") {
				listRow = i
			}
			if strings.Contains(l, "Disponible en:") {
				panelRow = i
			}
		}
		if beside := panelRow-listRow < 5; beside != tt.beside {
			t.Errorf("width %d: panel at row %d, list at row %d; want beside=%v:\n%s",
				tt.width, panelRow, listRow, tt.beside, ansi.Strip(content))
		}
	}
}

// The colors follow the terminal's background; the plain text is the same.
func TestTheColorsFollowTheBackground(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), 100, 30)
	dark := m.View().Content
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	light := m.View().Content
	if dark == light {
		t.Error("a light background did not change the colors")
	}
	if ansi.Strip(dark) != ansi.Strip(light) {
		t.Error("a light background changed the text")
	}
}

// Every confirmation starts with its plain summary; v shows and hides the
// technical detail, which still names the plan identifier, and the
// approval runs the plan shown.
func TestTheDetailIsShownOnlyOnRequest(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")
	pressRun(m, "D")
	got := screen(m)
	if !strings.Contains(got, "¿Desactivar agent-browser?") || !strings.Contains(got, text.TUIConfirmKeys) || strings.Contains(got, "Pasos:") {
		t.Fatalf("the confirmation does not start with the summary alone:\n%s", got)
	}
	pressRun(m, "v")
	got = screen(m)
	for _, want := range []string{"¿Desactivar agent-browser?", "Pasos:", "Identificador del plan: ", text.TUIConfirmKeysHide} {
		if !strings.Contains(got, want) {
			t.Errorf("v did not show %q:\n%s", want, got)
		}
	}
	pressRun(m, "v")
	if got = screen(m); strings.Contains(got, "Pasos:") {
		t.Errorf("a second v did not hide the detail:\n%s", got)
	}
	pressRun(m, "s")
	if mc.exists(".agents/skills/agent-browser") {
		t.Fatal("s after v did not run the plan")
	}
	if got = screen(m); !strings.Contains(got, "Listo: agent-browser está desactivada. [z] deshacer") {
		t.Errorf("the result is not told in plain words:\n%s", got)
	}
}

// The version 1 update starts with its summary too; v shows its detail,
// and n leaves it pending.
func TestTheUpdateConfirmationStartsWithItsSummary(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	m := openSized(t, a, 100, 30)
	pressRun(m, "2")
	pressRun(m, "a")
	got := screen(m)
	if !strings.Contains(got, "¿Actualizar alpha?") || strings.Contains(got, "Candidato:") {
		t.Fatalf("the update confirmation does not start with the summary alone:\n%s", got)
	}
	pressRun(m, "v")
	if got = screen(m); !strings.Contains(got, "Candidato:") || !strings.Contains(got, "¿Actualizar alpha?") {
		t.Errorf("v did not show the detail under the summary:\n%s", got)
	}
	if cmd := press(m, "n"); cmd != nil {
		t.Fatal("n ran something")
	}
	if c, err := a.CandidateFor("lock:alpha"); err != nil || c.State != "pending" {
		t.Fatalf("after n the update is not pending: %+v, %v", c, err)
	}
}

// The install review starts with its plain summary, before the files.
func TestTheInstallSummaryComesBeforeTheFiles(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	m := openSized(t, mc.newApp(), 200, 60)
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/tools")
	pressRun(m, "enter")
	got := screen(m)
	q, review := strings.Index(got, "¿Instalar tidy?"), strings.Index(got, "Revisión de tidy")
	if q < 0 || review < 0 || q > review {
		t.Errorf("the summary is not before the review (%d, %d):\n%s", q, review, got)
	}
}

func TestGoldenScreenSkills80(t *testing.T)  { goldenSkills(t, 80) }
func TestGoldenScreenSkills100(t *testing.T) { goldenSkills(t, 100) }

func goldenSkills(t *testing.T, width int) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), width, 30)
	selectSkill(t, m, "agent-browser")
	pressRun(m, "/")
	pressRun(m, "esc")
	for range 1 {
		pressRun(m, "down")
	}
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}

func TestGoldenPlanSummary80(t *testing.T)  { goldenPlanSummary(t, 80) }
func TestGoldenPlanSummary100(t *testing.T) { goldenPlanSummary(t, 100) }

func goldenPlanSummary(t *testing.T, width int) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), width, 30)
	selectSkill(t, m, "agent-browser")
	pressRun(m, "D")
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}
