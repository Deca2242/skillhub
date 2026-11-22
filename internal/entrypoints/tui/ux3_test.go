package tui_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/domain"
)

// UX3 (S13, L85): four tabs with counters, Historial and Ajustes as
// shortcuts, filters by state and by agent, a summary line, agent check
// boxes, z to undo the last operation, a risk traffic light and a panel
// that starts with the recommended step.

// firstLine is the header of a rendered screen.
func firstLine(m *tui.Model) string { return strings.Split(screen(m), "\n")[0] }

// health is the Salud counter the machine should show: the findings with a
// repair, gentle-ai's left out.
func health(t *testing.T, a *app.App) int {
	t.Helper()
	ov, err := a.Overview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range ov.Findings {
		if f.Kind != text.TUIGentleKind && ov.Repairable[f.ID] {
			n++
		}
	}
	return n
}

// The header names the four tabs; Skills counts its rows, Salud the
// problems it can repair (gentle-ai's left out) and Actualizar the pending
// updates. It fits 80 columns.
func TestTheHeaderHasFourTabsWithCounters(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := openSized(t, a, 80, 30)
	want := health(t, a)
	if want == 0 {
		t.Fatal("the fixture has nothing to repair")
	}
	head := firstLine(m)
	for _, s := range []string{
		fmt.Sprintf("1 Skills (%d)", m.RowCountForTest()), "2 Actualizar", "3 Instalar",
		fmt.Sprintf("4 Salud (%d)", want),
	} {
		if !strings.Contains(head, s) {
			t.Errorf("the header lacks %q:\n%s", s, head)
		}
	}
	if strings.Contains(head, "Actualizar (") {
		t.Errorf("Actualizar counts nothing pending:\n%s", head)
	}
	for _, old := range []string{"Doctor", "Historial", "Ajustes", "5 ", "6 "} {
		if strings.Contains(head, old) {
			t.Errorf("the header still names %q:\n%s", old, head)
		}
	}
	for _, l := range strings.Split(screen(m), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("a line is %d columns wide at 80:\n%s", w, l)
		}
	}

	u := openSized(t, setupGoldenFixture(t), 100, 30)
	if head := firstLine(u); !strings.Contains(head, "2 Actualizar (2)") {
		t.Errorf("two pending updates are not counted:\n%s", head)
	}
}

// tab goes through the four tabs, and 1 to 4 open each one.
func TestTabGoesThroughTheFourTabs(t *testing.T) {
	m := openSized(t, newMachine(t).newApp(), 100, 30)
	order := []tui.Screen{tui.ScreenUpdates, tui.ScreenInstall, tui.ScreenDoctor, tui.ScreenSkills}
	for _, want := range order {
		pressRun(m, "tab")
		if got := m.ActiveScreen(); got != want {
			t.Fatalf("tab went to %v, want %v", got, want)
		}
	}
	for key, want := range map[string]tui.Screen{"1": tui.ScreenSkills, "2": tui.ScreenUpdates, "3": tui.ScreenInstall, "4": tui.ScreenDoctor} {
		pressRun(m, key)
		if got := m.ActiveScreen(); got != want {
			t.Errorf("%s opened %v, want %v", key, got, want)
		}
	}
}

// h opens Historial and c Ajustes from any tab, and esc goes back to it.
func TestHistoryAndSettingsOpenFromAnyTab(t *testing.T) {
	m := openSized(t, newMachine(t).newApp(), 100, 30)
	for _, tab := range []struct {
		key string
		scr tui.Screen
	}{{"1", tui.ScreenSkills}, {"2", tui.ScreenUpdates}, {"3", tui.ScreenInstall}, {"4", tui.ScreenDoctor}} {
		for _, sc := range []struct {
			key string
			scr tui.Screen
		}{{"h", tui.ScreenHistory}, {"c", tui.ScreenSettings}} {
			pressRun(m, tab.key)
			pressRun(m, sc.key)
			if got := m.ActiveScreen(); got != sc.scr {
				t.Fatalf("%s from tab %s opened %v, want %v", sc.key, tab.key, got, sc.scr)
			}
			pressRun(m, "esc")
			if got := m.ActiveScreen(); got != tab.scr {
				t.Errorf("esc from %v went to %v, want tab %s", sc.scr, got, tab.key)
			}
		}
	}
}

// skillTitle is the title of the Skills list, the second line of its
// frame.
func skillTitle(m *tui.Model) string {
	for _, l := range strings.Split(screen(m), "\n") {
		if i := strings.Index(l, "Skills"); i >= 0 && strings.Contains(l, "│") {
			return strings.TrimSpace(strings.Trim(strings.Split(l, "││")[0], "│ "))
		}
		if strings.Contains(l, "Qué ve ") {
			return strings.TrimSpace(strings.Trim(strings.Split(l, "││")[0], "│ "))
		}
	}
	return ""
}

// The summary line counts the skills, and f goes through the filters by
// state: each one keeps only its rows, and the title names it.
func TestTheSummaryAndTheStateFilters(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	m := openSized(t, a, 140, 60)
	total := m.RowCountForTest()
	got := screen(m)
	var c text.SkillCounts
	if _, err := fmt.Sscanf(summaryOf(got), "%d skills · %d activas · %d desactivadas · %d con actualización · %d con problemas",
		&c.Total, &c.Active, &c.Disabled, &c.Updates, &c.Problems); err != nil {
		t.Fatalf("no summary line (%v):\n%s", err, got)
	}
	if c.Total != total || c.Active+c.Disabled != total || c.Disabled == 0 || c.Problems == 0 {
		t.Fatalf("summary %+v does not add up to %d rows", c, total)
	}
	// Every row with a warning is a problem: the problems filter keeps it.
	var warned []string
	for _, l := range strings.Split(got, "\n") {
		if _, rest, ok := strings.Cut(l, "│ ⚠ "); ok {
			warned = append(warned, strings.Fields(rest)[0])
		}
	}
	if len(warned) == 0 || c.Problems < len(warned) {
		t.Fatalf("%d problems, but %d rows warn: %v", c.Problems, len(warned), warned)
	}
	for _, f := range []struct {
		name string
		n    int
	}{{"activas", c.Active}, {"desactivadas", c.Disabled}, {"con problemas", c.Problems}, {"con actualización", c.Updates}} {
		pressRun(m, "f")
		if want := fmt.Sprintf("Skills · %s (%d)", f.name, f.n); !strings.Contains(screen(m), want) {
			t.Errorf("the filter %s lacks its title %q:\n%s", f.name, want, screen(m))
		}
		if m.RowCountForTest() != f.n {
			t.Errorf("the filter %s shows %d rows, want %d", f.name, m.RowCountForTest(), f.n)
		}
	}
	// «ignoradas» (UX4) comes after «con actualización».
	for range 5 {
		pressRun(m, "f")
	}
	for _, name := range warned {
		if !strings.Contains(screen(m), name) {
			t.Errorf("the problems filter lacks %s, which warns:\n%s", name, screen(m))
		}
	}
	pressRun(m, "f")
	pressRun(m, "f")
	pressRun(m, "f")
	if m.RowCountForTest() != total || !strings.Contains(screen(m), fmt.Sprintf("Skills (%d)", total)) {
		t.Errorf("f did not come back to every skill:\n%s", screen(m))
	}
	// The disabled filter keeps agent-browser's row, which a enables.
	pressRun(m, "f")
	pressRun(m, "f")
	if !strings.Contains(screen(m), "agent-browser") {
		t.Errorf("the disabled filter lacks agent-browser:\n%s", screen(m))
	}
}

func summaryOf(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, " skills · ") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// moveTo moves the cursor of a chooser down to the line naming name.
func moveTo(t *testing.T, m *tui.Model, name string) {
	t.Helper()
	for i := range 80 {
		for _, l := range strings.Split(screen(m), "\n") {
			if strings.Contains(l, "> ") && strings.Contains(l, name) {
				return
			}
		}
		if i < 40 {
			pressRun(m, "down")
		} else {
			pressRun(m, "up")
		}
	}
	t.Fatalf("no line with %q under the cursor:\n%s", name, screen(m))
}

// g chooses an agent and the list keeps only the skills it sees; / still
// searches by text within them.
func TestTheAgentFilterShowsWhatAnAgentSees(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	vis, err := a.Visibility(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, ids := range vis {
		if slices.Contains(ids, "claude-code") {
			want++
		}
	}
	m := openSized(t, a, 140, 60)
	all := m.RowCountForTest()
	pressRun(m, "g")
	if got := screen(m); !strings.Contains(got, text.TUIAgentFilterQuestion) || !strings.Contains(got, "Claude Code") {
		t.Fatalf("g did not list the agents:\n%s", got)
	}
	moveTo(t, m, "Claude Code")
	pressRun(m, "enter")
	if m.RowCountForTest() != want || want == 0 || want == all {
		t.Fatalf("Claude Code shows %d rows, want %d of %d", m.RowCountForTest(), want, all)
	}
	if got := screen(m); !strings.Contains(got, fmt.Sprintf("Qué ve Claude Code (%d)", want)) {
		t.Errorf("the title does not name the agent:\n%s", got)
	}
	pressRun(m, "/")
	typeText(m, "agent-browser")
	pressRun(m, "enter")
	if m.RowCountForTest() != 1 {
		t.Errorf("/ within the agent's view shows %d rows, want 1", m.RowCountForTest())
	}
	pressRun(m, "/")
	pressRun(m, "esc")
	pressRun(m, "g")
	moveTo(t, m, text.TUIAgentFilterAll)
	pressRun(m, "enter")
	if m.RowCountForTest() != all {
		t.Errorf("every agent shows %d rows, want %d", m.RowCountForTest(), all)
	}
}

// l opens the agent check boxes of the selected skill: only the agents
// that need a link, with a line on the ones that read the store. Unmarking
// one and marking another plans one change after the other, each with its
// summary and its own confirmation; nothing is written before s.
func TestTheAgentCheckBoxes(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := open(t, a)
	selectSkill(t, m, "agent-browser")
	if !strings.Contains(screen(m), text.TUIActionAgents) {
		t.Fatalf("the panel does not offer the check boxes:\n%s", screen(m))
	}
	before := mc.hash()
	pressRun(m, "l")
	got := screen(m)
	for _, want := range []string{text.TUIChecksQuestion("agent-browser"), "[x] Claude Code", "[ ] Kiro CLI", "Codex", "leen las skills directamente del almacén"} {
		if !strings.Contains(flat(got), want) {
			t.Errorf("the check boxes lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[ ] Codex") || strings.Contains(got, "[x] Codex") {
		t.Errorf("Codex reads the store and has no box:\n%s", got)
	}
	moveTo(t, m, "Claude Code")
	pressRun(m, " ")
	if !strings.Contains(screen(m), "[ ] Claude Code") {
		t.Fatalf("space did not unmark Claude Code:\n%s", screen(m))
	}
	moveTo(t, m, "Kiro")
	pressRun(m, " ")
	pressRun(m, "enter")
	got = screen(m)
	if !m.InPlanForTest() || !strings.Contains(got, "¿Quitar agent-browser de Claude Code?") || !strings.Contains(got, text.TUIChainStep(1, 2)) {
		t.Fatalf("the first change is not shown:\n%s", got)
	}
	if mc.hash() != before {
		t.Fatal("the check boxes wrote before the confirmation")
	}
	pressRun(m, "s")
	got = screen(m)
	if !m.InPlanForTest() || !strings.Contains(got, "¿Añadir agent-browser a Kiro CLI?") || !strings.Contains(got, text.TUIChainStep(2, 2)) {
		t.Fatalf("the second change is not shown after the first:\n%s", got)
	}
	if mc.exists(".claude/skills/agent-browser") {
		t.Error("the first change did not unlink Claude Code")
	}
	pressRun(m, "s")
	if !mc.exists(".kiro/skills/agent-browser") {
		t.Errorf("the second change did not link Kiro:\n%s", screen(m))
	}
	if !strings.Contains(m.StatusForTest(), text.TUIUndoKey) {
		t.Errorf("the result does not offer z: %q", m.StatusForTest())
	}
}

// Cancelling one step of the chain cancels the steps after it.
func TestCancellingAChangeOfAgentsStopsTheOthers(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")
	pressRun(m, "l")
	moveTo(t, m, "Claude Code")
	pressRun(m, " ")
	moveTo(t, m, "Kiro")
	pressRun(m, " ")
	pressRun(m, "enter")
	before := mc.hash()
	pressRun(m, "n")
	if m.InPlanForTest() || mc.hash() != before || !strings.Contains(m.StatusForTest(), text.TUIChainLeft) {
		t.Fatalf("n did not stop the chain: plan=%v status=%q", m.InPlanForTest(), m.StatusForTest())
	}
	pressRun(m, "l")
	pressRun(m, "enter")
	if m.StatusForTest() != text.TUIChecksNone || m.InPlanForTest() {
		t.Errorf("enter with no change: %q", m.StatusForTest())
	}
}

// Instalar chooses its agents with the same check boxes.
func TestInstallChoosesItsAgentsWithCheckBoxes(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	a := mc.newApp()
	m := open(t, a)
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/tools")
	pressRun(m, "enter")
	pressRun(m, "enter")
	got := screen(m)
	for _, want := range []string{text.TUIInstallStep(3), "[x] Claude Code", "[x] Kiro CLI", "leen las skills directamente del almacén"} {
		if !strings.Contains(flat(got), want) {
			t.Fatalf("step 3 lacks %q:\n%s", want, got)
		}
	}
	moveTo(t, m, "Kiro")
	pressRun(m, " ")
	pressRun(m, "enter")
	if !m.InPlanForTest() {
		t.Fatalf("no plan after the boxes:\n%s", screen(m))
	}
	pressRun(m, "s")
	if !mc.exists(".agents/skills/tidy") || !mc.exists(".claude/skills/tidy") || mc.exists(".kiro/skills/tidy") {
		t.Errorf("the install did not follow the boxes:\n%s", screen(m))
	}
}

// z opens the undo of the last operation of the session, with its summary
// and its confirmation, and says why when there is none.
func TestZUndoesTheLastOperation(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := open(t, a)
	pressRun(m, "z")
	if m.StatusForTest() != text.TUIZNothing {
		t.Errorf("z before any operation: %q", m.StatusForTest())
	}
	selectSkill(t, m, "agent-browser")
	pressRun(m, "d")
	pressRun(m, "s")
	if !strings.HasPrefix(m.StatusForTest(), "Listo: agent-browser está desactivada.") || !strings.Contains(m.StatusForTest(), text.TUIUndoKey) {
		t.Fatalf("the result does not offer z: %q", m.StatusForTest())
	}
	if mc.exists(".agents/skills/agent-browser") {
		t.Fatal("the disable did not run")
	}
	for _, tab := range []string{"2", "4"} {
		pressRun(m, tab)
		before := mc.hash()
		pressRun(m, "z")
		if !m.InPlanForTest() || !strings.Contains(screen(m), "¿Deshacer «desactivar»?") {
			t.Fatalf("z on tab %s did not show the undo:\n%s", tab, screen(m))
		}
		pressRun(m, "n")
		if mc.hash() != before {
			t.Fatal("answering no to z wrote something")
		}
	}
	pressRun(m, "z")
	pressRun(m, "s")
	if !mc.exists(".agents/skills/agent-browser") || !mc.exists(".claude/skills/agent-browser") {
		t.Fatalf("z did not undo the disable:\n%s", screen(m))
	}
	pressRun(m, "z")
	if m.StatusForTest() != text.TUIZUndone || m.InPlanForTest() {
		t.Errorf("z after an undo: %q", m.StatusForTest())
	}
}

// z undoes a version 1 update as Historial's u does, after a summary.
func TestZUndoesAnUpdate(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	m := openSized(t, a, 100, 30)
	pressRun(m, "2")
	pressRun(m, "a")
	pressRun(m, "s")
	if !strings.Contains(m.StatusForTest(), text.TUIUndoKey) {
		t.Fatalf("the update does not offer z: %q", m.StatusForTest())
	}
	pressRun(m, "z")
	if got := screen(m); !strings.Contains(got, "¿Deshacer la actualización de alpha?") {
		t.Fatalf("z did not ask:\n%s", got)
	}
	skill := filepath.Join(f.home.Paths().StoreDir(), "alpha", "SKILL.md")
	if b, _ := os.ReadFile(skill); string(b) != "# Alpha v2\n" {
		t.Fatalf("the question already undid: %q", b)
	}
	pressRun(m, "s")
	if b, _ := os.ReadFile(skill); string(b) != "# Alpha v1\n" {
		t.Errorf("z did not undo the update: %q\n%s", b, screen(m))
	}
}

// Each update row has its traffic light, and the panel tells its
// consequence; only text is never told as harmless.
func TestTheRiskTrafficLight(t *testing.T) {
	m := openSized(t, setupGoldenFixture(t), 140, 40)
	pressRun(m, "2")
	got := screen(m)
	for _, want := range []*regexp.Regexp{regexp.MustCompile(`● alpha +bajo`), regexp.MustCompile(`▲ gamma +medio`)} {
		if !want.MatchString(got) {
			t.Errorf("the rows lack %v:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "Riesgo: ● bajo. Cambia las instrucciones que lee el agente.") {
		t.Errorf("the panel does not tell the consequence:\n%s", got)
	}
	pressRun(m, "down")
	pressRun(m, "down")
	if got := screen(m); !strings.Contains(got, "Riesgo: ▲ medio. Reemplaza una edición tuya, que queda guardada en una copia.") {
		t.Errorf("the panel of the modified skill does not tell its risk:\n%s", got)
	}
}

// The traffic light of each label, and the highest one wins.
func TestRiskOfLabels(t *testing.T) {
	tests := []struct {
		labels []domain.Label
		want   text.Risk
	}{
		{nil, text.RiskLow},
		{[]domain.Label{domain.LabelTextOnly}, text.RiskLow},
		{[]domain.Label{domain.LabelTouchesScripts}, text.RiskMedium},
		{[]domain.Label{domain.LabelNonTextFiles}, text.RiskMedium},
		{[]domain.Label{domain.LabelLocallyModified}, text.RiskMedium},
		{[]domain.Label{domain.LabelModeChange}, text.RiskReview},
		{[]domain.Label{domain.LabelNewPermissions}, text.RiskReview},
		{[]domain.Label{domain.LabelSuspiciousText}, text.RiskReview},
		{[]domain.Label{domain.LabelTextOnly, domain.LabelTouchesScripts, domain.LabelSuspiciousText}, text.RiskReview},
	}
	for _, tt := range tests {
		if got, why := text.RiskOf(tt.labels); got != tt.want || why == "" {
			t.Errorf("RiskOf(%v) = %v, %q; want %v", tt.labels, got, why, tt.want)
		}
	}
	if _, why := text.RiskOf([]domain.Label{domain.LabelTouchesScripts}); why != "cambia un script que el agente podría ejecutar" {
		t.Errorf("scripts: %q", why)
	}
	if _, why := text.RiskOf([]domain.Label{domain.LabelTextOnly}); why != "cambia las instrucciones que lee el agente" {
		t.Errorf("only text: %q", why)
	}
}

// The install review tells its traffic light. Every install brings new
// permissions, since the whole skill is new: that label alone is no reason
// to alarm (Gemini, L80).
func TestTheInstallReviewTellsItsRisk(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	m := open(t, mc.newApp())
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/tools")
	pressRun(m, "enter")
	if got := screen(m); !strings.Contains(got, "Riesgo: ● bajo. Cambia las instrucciones que lee el agente.") {
		t.Errorf("the review lacks its traffic light:\n%s", got)
	}
}

// panelLine is the line after the skill's name in its panel.
func guideOf(m *tui.Model) string {
	s := flat(screen(m))
	i := strings.Index(s, text.TUIGuideRecommended)
	if i < 0 {
		return ""
	}
	return s[i:]
}

// The panel starts with the situation and the recommended step; the other
// actions follow, without it.
func TestThePanelStartsWithTheRecommendedStep(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".kiro/skills/agent-browser", "../../.agents/skills/old-agent-browser")
	m := openSized(t, mc.newApp(), 140, 60)
	selectSkill(t, m, "agent-browser")
	got := flat(screen(m))
	if !strings.Contains(got, "Tiene un enlace roto en Kiro CLI. Recomendado: [p] Arreglar el enlace roto") {
		t.Fatalf("the panel does not start with the broken link:\n%s", screen(m))
	}
	if strings.Count(got, "[p] Arreglar el enlace roto") != 1 {
		t.Errorf("the recommended action is repeated:\n%s", screen(m))
	}
	before := mc.hash()
	pressRun(m, "p")
	if !m.InPlanForTest() {
		t.Fatalf("p did not plan the repair:\n%s", screen(m))
	}
	pressRun(m, "n")
	if mc.hash() != before {
		t.Fatal("answering no wrote something")
	}

	pressRun(m, "/")
	pressRun(m, "esc")
	selectSkill(t, m, graphtest.OffSkills[0])
	if g := guideOf(m); !strings.HasPrefix(g, "Recomendado: [o] Desactivarla de verdad") {
		t.Errorf("an OFF skill's panel does not recommend adopting it:\n%s", screen(m))
	}
	if got := flat(screen(m)); !strings.Contains(got, "La desactivaste a mano moviéndola a OFF/, pero") {
		t.Errorf("an OFF skill's panel does not start with its situation:\n%s", screen(m))
	}
}

// The help is told by tab and fits 80x24 without scrolling.
func TestTheHelpIsByTabAndFits80x24(t *testing.T) {
	m := openSized(t, newMachine(t).newApp(), 80, 24)
	pressRun(m, "?")
	got := screen(m)
	lines := strings.Split(got, "\n")
	if len(lines) > 24 {
		t.Errorf("the help takes %d lines:\n%s", len(lines), got)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 80 {
			t.Errorf("a help line is wider than 80:\n%s", l)
		}
	}
	if strings.Contains(got, text.TUIHelpScroll) {
		t.Errorf("the help scrolls at 80x24:\n%s", got)
	}
	for _, want := range []string{"1 Skills:", "2 Actualizar:", "3 Instalar:", "4 Salud:", "Historial (h):", "Ajustes (c):", "z deshacer"} {
		if !strings.Contains(flat(got), want) {
			t.Errorf("the help lacks %q:\n%s", want, got)
		}
	}
}

// The keys of the list views never load anything: what each skill admits
// is read once per load, never per key.
func TestKeysDoNotReadTheMachineAgain(t *testing.T) {
	m := openSized(t, newMachine(t).newApp(), 100, 30)
	for _, k := range []string{"down", "f", "f", "up", "tab", "tab", "1"} {
		if cmd := press(m, k); cmd != nil {
			t.Errorf("%s returned a command", k)
		}
	}
}

// The golden files of the four tabs and of the check boxes, at 80 and 100
// columns. One model per width renders every tab: the machine is read once.
func TestGoldenUX3(t *testing.T) {
	for _, width := range []int{80, 100} {
		mc := newMachine(t)
		a := mc.newApp()
		mc.approve(a, app.Request{Action: app.ActionDisable, Key: "archify"})
		m := openSized(t, a, width, 30)
		shot := func(name string) {
			t.Run(fmt.Sprintf("%s%d", name, width), func(t *testing.T) {
				teatest.RequireEqualOutput(t, stable(mc, screen(m)))
			})
		}
		shot("Skills")
		pressRun(m, "f")
		pressRun(m, "f")
		shot("SkillsDisabled")
		for range 4 {
			pressRun(m, "f")
		}
		selectSkill(t, m, "agent-browser")
		pressRun(m, "l")
		shot("Checks")
		pressRun(m, "esc")
		pressRun(m, "/")
		pressRun(m, "esc")
		pressRun(m, "2")
		shot("Updates")
		pressRun(m, "3")
		shot("Install")
		pressRun(m, "4")
		shot("Health")
		pressRun(m, "1")
		pressRun(m, "g")
		shot("AgentFilter")
	}
}
