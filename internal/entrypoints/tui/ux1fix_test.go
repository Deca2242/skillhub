package tui_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/graph/graphtest"
)

// The user's case (L83): git-guardrails-claude-code sits in OFF/, Codex,
// OpenCode and Pi still see it, and Kiro holds a broken link to where it
// was. The panel explains it in plain words, says who really sees it, and
// offers adopting OFF/ instead of actions that would be refused.
func TestThePanelOfAnOFFSkillExplainsItAndOffersAdoptingIt(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "git-guardrails-claude-code")
	got := screen(m)
	for _, want := range []string{
		"La desactivaste a mano moviéndola a OFF/, pero Codex, OpenCode y Pi la siguen viendo porque buscan en",
		"Disponible en: Codex, OpenCode y Pi",
		text.TUIActionAdoptOff,
		text.TUIActionRemove,
		"junto con las otras 3 skills de OFF/",
		"También borra sus 3 enlaces rotos.",
	} {
		if !strings.Contains(flatSkillPanel(got), flat(want)) {
			t.Errorf("the panel lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{text.TUIActionDisable, text.TUIActionAgents, text.TUIActionFixLink, "Cursor"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the panel offers %q:\n%s", unwanted, got)
		}
	}
	if keys := m.OfferedKeysForTest(); !slices.Equal(keys, []string{"o", "e", "enter"}) {
		t.Errorf("offered keys = %v, want [o e enter]", keys)
	}

	// d is not planned: the status says why, in plain words, and nothing
	// is written.
	before := mc.hash()
	pressRun(m, "d")
	if m.InPlanForTest() {
		t.Fatal("d planned a disable of a skill in OFF/")
	}
	if got, want := m.StatusForTest(), text.TUIOffOnly(app.ActionDisable); got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if strings.Contains(m.StatusForTest(), "skillhub scan") || strings.Contains(m.StatusForTest(), "skillhub doctor") {
		t.Errorf("status names CLI commands: %q", m.StatusForTest())
	}

	// o plans adopting OFF/, with a summary that names its four skills.
	pressRun(m, "o")
	if !m.InPlanForTest() {
		t.Fatalf("o did not plan adopting OFF/; status %q", m.StatusForTest())
	}
	got = screen(m)
	want := "Se desactivan las 4 juntas: " + text.JoinNames(slices.Sorted(slices.Values(graphtest.OffSkills))) + "."
	for _, w := range []string{"¿Desactivar de verdad las 4 skills de OFF/?", want} {
		if !strings.Contains(flat(got), flat(w)) {
			t.Errorf("the summary lacks %q:\n%s", w, got)
		}
	}
	pressRun(m, "n")
	if mc.hash() != before {
		t.Fatal("refusing and planning wrote something")
	}
}

// Every action a row of the Skills screen offers plans without a refusal
// (L83): the walk presses each offered key on every row of graphtest's
// machine and requires a plan waiting for confirmation.
func TestEveryActionThePanelOffersPlans(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := open(t, a)
	before := mc.hash()
	offered := 0
	for i := range m.RowCountForTest() {
		if i > 0 {
			pressRun(m, "down")
		}
		id := m.SelectedIDForTest()
		for _, k := range m.OfferedKeysForTest() {
			if k == "enter" || k == "4" {
				continue
			}
			offered++
			pressRun(m, k)
			if k == "l" {
				// The check boxes: changing the first agent plans its link
				// or its unlink.
				pressRun(m, " ")
				pressRun(m, "enter")
			}
			if !m.InPlanForTest() {
				t.Errorf("row %s: %s offered, but no plan; status %q", id, k, m.StatusForTest())
				continue
			}
			pressRun(m, "n")
		}
	}
	if offered == 0 {
		t.Fatal("no row offered an action")
	}
	if mc.hash() != before {
		t.Fatal("the walk wrote something")
	}
}

// A skill outside the store is refused in plain words, without the CLI's
// commands (L83).
func TestAKeyThatDoesNotApplyIsToldPlainly(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "humanizer")
	pressRun(m, "d")
	if m.InPlanForTest() {
		t.Fatal("d planned disabling a skill outside the store")
	}
	want := "Esta skill no está en el almacén de skillhub, así que no se puede desactivar desde aquí. Mira Salud (4) para ver cómo ordenarla."
	if got := m.StatusForTest(); got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// A broken link of an active skill is fixed from its row with the repair
// Doctor offers for it.
func TestTheRowFixesABrokenLink(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".kiro/skills/agent-browser", "../../.agents/skills/old-agent-browser")
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")
	if !strings.Contains(screen(m), text.TUIActionFixLink) {
		t.Fatalf("the panel does not offer fixing the link:\n%s", screen(m))
	}
	before := mc.hash()
	pressRun(m, "p")
	if !m.InPlanForTest() {
		t.Fatalf("p did not plan the repair; status %q", m.StatusForTest())
	}
	if got := screen(m); !strings.Contains(got, "¿Hacer que el enlace lleve a la skill correcta?") {
		t.Errorf("the summary is not the repoint repair:\n%s", got)
	}
	pressRun(m, "n")
	if mc.hash() != before {
		t.Fatal("planning wrote something")
	}
}

// Several broken links are fixed one by one in Doctor: the row offers going
// there instead of a fix that would take only one.
func TestSeveralBrokenLinksSendToDoctor(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".kiro/skills/agent-browser", "../../.agents/skills/old-agent-browser")
	mc.h.Symlink(".junie/skills/agent-browser", "../../.agents/skills/old-agent-browser")
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")
	got := screen(m)
	if !strings.Contains(got, text.TUIActionDoctor) || strings.Contains(got, text.TUIActionFixLink) {
		t.Errorf("the panel does not send to Doctor:\n%s", got)
	}
	if !strings.Contains(flatSkillPanel(got), flat(text.TUIPanelLinksInDoctor(2))) {
		t.Errorf("the panel does not count the links:\n%s", got)
	}
	pressRun(m, "p")
	if m.InPlanForTest() || m.StatusForTest() != text.TUIFixInDoctor {
		t.Errorf("p: plan = %v, status %q", m.InPlanForTest(), m.StatusForTest())
	}
}

// The panel of a skill in OFF/ at 100 columns.
func TestGoldenScreenOFFSkill100(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), 100, 30)
	selectSkill(t, m, "git-guardrails-claude-code")
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}

// flatSkillPanel ignores the list when the skill panel is beside it.
func flatSkillPanel(s string) string {
	var lines []string
	for line := range strings.SplitSeq(s, "\n") {
		if _, panel, ok := strings.Cut(line, "││"); ok {
			lines = append(lines, panel)
		}
	}
	return flat(strings.Join(lines, "\n"))
}

// flat joins the lines of a wrapped panel, borders and padding out, so a
// sentence is found whatever its wrapping.
func flat(s string) string {
	var words []string
	for _, f := range strings.Fields(s) {
		if w := strings.Trim(f, "│"); w != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// A refusal the planner still returns is told in the TUI's words, without
// the CLI's commands: enabling a skill whose lock neighbour left after it.
func TestARefusalIsToldWithoutCLICommands(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "archify"})
	m := open(t, a)
	selectSkill(t, m, "agent-browser")
	pressRun(m, "a")
	got := m.StatusForTest()
	if m.InPlanForTest() || !strings.Contains(got, "archify") {
		t.Fatalf("enable was not refused for its neighbour; status %q", got)
	}
	if strings.Contains(got, "skillhub skills") || strings.Contains(got, "«skillhub") {
		t.Errorf("the refusal names a CLI command: %q", got)
	}
	if !strings.Contains(got, "Elige esa fila y pulsa [a]") {
		t.Errorf("the refusal does not say what to do in the TUI: %q", got)
	}
}
