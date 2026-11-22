package tui_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
)

// The texts the version 2 flows show, as the user reads them.
const (
	// confirmPlan is the keys line of every confirmation, with its details
	// shown or hidden.
	confirmPlan  = "[s] Sí  [n] No"
	notConfirmed = "No se confirmó; no se cambió nada."
	planChanged  = "El plan cambió desde que lo viste"
	done         = "Listo: "
)

func backspace(m *tui.Model, n int) {
	for range n {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		*m = *next.(*tui.Model)
	}
}

// Every Skills action shows its plain summary, and on v the same preview
// the CLI prints, and waits for a confirmation: nothing is written until
// then, and "n" leaves everything as it was.
func TestSkillsActionsShowThePlanAndWaitForConfirmation(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *machine, a *app.App)
		skill string
		key   string
		typed string
		// box is the agent whose check box changes (UX3).
		box  string
		ask  string
		want []string
	}{
		{name: "disable", skill: "agent-browser", key: "D", ask: "¿Desactivar agent-browser?",
			want: []string{"Plan: Desactivar agent-browser en todos sus destinos gestionados", "quitar el enlace", "aparcar la entrada «agent-browser» del lock"}},
		{name: "enable", skill: "archify", key: "A", typed: "agent-browser", ask: "¿Activar agent-browser?",
			setup: func(m *machine, a *app.App) {
				m.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
			},
			// The lockUnpark step carries no name today, so its line reads
			// «» (reported to the orchestrator); the TUI shows what the CLI shows.
			want: []string{"Plan: Activar agent-browser", "devolver la entrada", "al lock, en su posición"}},
		{name: "remove", skill: "grill-me", key: "E", ask: "¿Eliminar grill-me?",
			want: []string{"Plan: Eliminar grill-me", "quitar la entrada «grill-me» del lock"}},
		{name: "unlink", skill: "archify", key: "l", box: "Claude Code", ask: "¿Quitar archify de Claude Code?",
			want: []string{"Plan: Quitar el enlace de archify en claude-code", "quitar el enlace"}},
		{name: "link", skill: "archify", key: "l", box: "Claude Code", ask: "¿Añadir archify a Claude Code?",
			setup: func(m *machine, a *app.App) {
				m.approve(a, app.Request{Action: app.ActionUnlink, Key: "archify", Harness: "claude-code"})
			},
			want: []string{"Plan: Enlazar archify en claude-code", "crear el enlace"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMachine(t)
			a := mc.newApp()
			if tt.setup != nil {
				tt.setup(mc, a)
			}
			m := open(t, a)
			selectSkill(t, m, tt.skill)
			before := mc.hash()

			pressRun(m, tt.key)
			if tt.typed != "" {
				backspace(m, 40)
				typeText(m, tt.typed)
				pressRun(m, "enter")
			}
			if tt.box != "" {
				moveTo(t, m, tt.box)
				pressRun(m, " ")
				pressRun(m, "enter")
			}
			got := screen(m)
			if !strings.Contains(got, tt.ask) || !strings.Contains(got, confirmPlan) || strings.Contains(got, "Identificador del plan: ") {
				t.Errorf("the summary lacks %q or its keys, or shows the detail unasked:\n%s", tt.ask, got)
			}
			pressRun(m, "v")
			got = screen(m)
			for _, want := range append(tt.want, tt.ask, confirmPlan, "Identificador del plan: ") {
				if !strings.Contains(got, want) {
					t.Errorf("the plan lacks %q:\n%s", want, got)
				}
			}
			if mc.hash() != before {
				t.Fatal("showing the plan wrote something")
			}

			pressRun(m, "n")
			got = screen(m)
			if strings.Contains(got, confirmPlan) || !strings.Contains(got, notConfirmed) {
				t.Errorf("after n the plan is still asked, or the refusal is not told:\n%s", got)
			}
			if mc.hash() != before {
				t.Fatal("a refused plan wrote something")
			}
		})
	}
}

// Confirming runs the plan whose identifier was shown, and the result says
// in plain words what changed, how to undo it and which agents to reload.
func TestConfirmingAppliesTheShownPlan(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")

	pressRun(m, "D")
	pressRun(m, "s")

	if mc.exists(".agents/skills/agent-browser") || mc.exists(".claude/skills/agent-browser") {
		t.Fatal("the confirmed disable left the folder or its link")
	}
	if _, ok := mc.persisted().Parked["agent-browser"]; !ok {
		t.Fatal("the confirmed disable did not park the lock entry")
	}
	lines := mc.v2Lines()
	if len(lines) != 1 || lines[0].Target != "v2:disable" {
		t.Fatalf("journal = %+v, want one v2:disable line", lines)
	}
	got := screen(m)
	for _, want := range []string{"Listo: agent-browser está desactivada. [z] deshacer", "Recarga las skills en ", "Claude Code"} {
		if !strings.Contains(got, want) {
			t.Errorf("the result lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, confirmPlan) {
		t.Errorf("the confirmation is still shown after applying:\n%s", got)
	}
}

// A link created between showing the plan and confirming it changes the
// plan: the old one never runs, the new one is shown and needs its own
// confirmation.
func TestAChangedPlanIsShownAgainAndNeverRunsSilently(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")

	pressRun(m, "D")
	pressRun(m, "v") // the detail names every link
	if strings.Contains(screen(m), ".gemini/skills/agent-browser") {
		t.Fatal("the first plan already names the link created later")
	}
	mc.h.Symlink(".gemini/skills/agent-browser", "../../.agents/skills/agent-browser")
	before := mc.hash()

	pressRun(m, "s")
	got := screen(m)
	if mc.hash() != before {
		t.Fatal("the changed plan ran, or the refusal wrote something")
	}
	for _, want := range []string{planChanged, confirmPlan, ".gemini/skills/agent-browser"} {
		if !strings.Contains(got, want) {
			t.Errorf("after the refusal the screen lacks %q:\n%s", want, got)
		}
	}

	pressRun(m, "s")
	if mc.exists(".agents/skills/agent-browser") || mc.exists(".gemini/skills/agent-browser") {
		t.Fatal("confirming the new plan did not run it")
	}
	if !strings.Contains(screen(m), done) {
		t.Errorf("the result is not told:\n%s", screen(m))
	}
}

// While a plan is made or runs, no other write key acts: no command, no
// prompt, no second plan.
func TestWriteKeysDoNothingWhileAnOperationRuns(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	// A version 2 line Historial could undo, and findings Doctor could
	// repair, so that u and enter have something to act on.
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "grill-me"})
	m := open(t, a)
	pressRun(m, "4")
	pressRun(m, "1")
	selectSkill(t, m, "agent-browser")

	writeKeys := []string{"D", "A", "E", "-", "+", "x"}
	planning := press(m, "D")
	if planning == nil {
		t.Fatal("D started nothing")
	}
	for _, k := range writeKeys {
		if cmd := press(m, k); cmd != nil {
			t.Errorf("%q acted while the plan was being made", k)
		}
		if strings.Contains(screen(m), "Nombre del harness") || strings.Contains(screen(m), "Clave de la skill") {
			t.Errorf("%q opened a prompt while the plan was being made", k)
		}
	}
	runSyncCmd(m, planning)

	applying := press(m, "s")
	if applying == nil {
		t.Fatal("s did not apply the plan")
	}
	for _, k := range append(slices.Clone(writeKeys), "s") {
		if cmd := press(m, k); cmd != nil {
			t.Errorf("%q acted while the operation ran", k)
		}
	}
	pressRun(m, "h")
	if cmd := press(m, "u"); cmd != nil {
		t.Error("u acted in Historial while the operation ran")
	}
	pressRun(m, "4")
	if cmd := press(m, "enter"); cmd != nil {
		t.Error("enter acted in Doctor while the operation ran")
	}
	pressRun(m, "1")

	runSyncCmd(m, applying)
	if mc.exists(".agents/skills/agent-browser") || len(mc.v2Lines()) != 2 {
		t.Fatalf("the operation did not run exactly once: journal %+v", mc.v2Lines())
	}
	pressRun(m, "/")
	pressRun(m, "esc") // the disabled skill left the list: clear the filter
	// E acts only on a row that offers removing (L83).
	selectSkill(t, m, "archify")
	if cmd := press(m, "E"); cmd == nil {
		t.Error("write keys stay blocked after the operation finished")
	}
}

// The Install screen: the source, the review of the prepared revision
// (files, labels, diff), the proposed targets, which the user edits, and
// the approval of the plan.
func TestInstallScreenReviewsEditsTargetsAndApproves(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	a := mc.newApp()
	m := openSized(t, a, 200, 60)

	pressRun(m, "3")
	if m.ActiveScreen() != tui.ScreenInstall {
		t.Fatalf("5 opened %v, want ScreenInstall", m.ActiveScreen())
	}
	pressRun(m, "enter")
	typeText(m, "acme/tools")
	pressRun(m, "enter")

	got := screen(m)
	for _, want := range []string{"Revisión de tidy", "permisos nuevos", "SKILL.md", "+second line"} {
		if !strings.Contains(got, want) {
			t.Errorf("the review lacks %q:\n%s", want, got)
		}
	}
	proposed, err := a.ProposeTargets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pressRun(m, "enter")
	got = screen(m)
	for _, id := range proposed {
		if !strings.Contains(got, "[x] "+a.AgentName(id)) {
			t.Errorf("the proposed %s is not marked:\n%s", id, got)
		}
	}
	// The boxes keep only Claude Code (UX3).
	for _, id := range proposed {
		if id != "claude-code" {
			moveTo(t, m, a.AgentName(id))
			pressRun(m, " ")
		}
	}
	pressRun(m, "enter")

	got = screen(m)
	if !strings.Contains(got, "¿Instalar tidy?") || !strings.Contains(got, "Se instala desde acme/tools.") {
		t.Errorf("the install plan does not start with its summary:\n%s", got)
	}
	pressRun(m, "v")
	got = screen(m)
	for _, want := range []string{"Plan: Instalar tidy desde acme/tools", "Destinos: claude-code ", confirmPlan} {
		if !strings.Contains(got, want) {
			t.Errorf("the install plan lacks %q:\n%s", want, got)
		}
	}
	if mc.exists(".agents/skills/tidy") || mc.exists(".claude/skills/tidy") {
		t.Fatal("reviewing and planning an install published the skill")
	}
	if lock, _ := os.ReadFile(mc.p.SkillsLockFile()); strings.Contains(string(lock), `"tidy"`) {
		t.Fatal("reviewing and planning an install wrote the lock")
	}

	pressRun(m, "s")
	if !mc.exists(".agents/skills/tidy/SKILL.md") || !mc.exists(".claude/skills/tidy") {
		t.Fatalf("install did not publish the skill and its link:\n%s", screen(m))
	}
	lock, err := os.ReadFile(mc.p.SkillsLockFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lock), `"tidy"`) || strings.Contains(string(lock), `"ref"`) {
		t.Fatalf("lock after install:\n%s", lock)
	}
	if mc.exists(".agents/.skillhub/staging") {
		t.Error("the install left the staging behind")
	}
	if !strings.Contains(screen(m), done) {
		t.Errorf("the result is not told:\n%s", screen(m))
	}
}

// esc leaves each step of an install without writing anything.
func TestInstallEscCancelsEachStep(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	m := open(t, mc.newApp())
	before := mc.hash()

	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/tools")
	pressRun(m, "enter")
	pressRun(m, "esc")
	if strings.Contains(screen(m), "Revisión de tidy") {
		t.Errorf("esc did not leave the review:\n%s", screen(m))
	}
	if mc.hash() != before {
		t.Fatal("a cancelled install wrote outside the staging")
	}
}

// A source the install refuses is explained in Spanish, and nothing runs.
func TestInstallRefusalIsExplained(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "not a source")
	pressRun(m, "enter")
	got := screen(m)
	if strings.Contains(got, "Revisión de") || !strings.Contains(got, "origen") {
		t.Errorf("the refusal is not explained:\n%s", got)
	}
}

// findingIndex is the row of the finding id on the Doctor screen.
// findingIndex is the row of the finding id in the Doctor list.
func findingIndex(t *testing.T, a *app.App, id string) int {
	t.Helper()
	for i, f := range doctorOrder(t, a) {
		if f.ID == id {
			return i
		}
	}
	t.Fatalf("no finding %s", id)
	return -1
}

// doctorOrder are the findings as the Doctor list offers them: grouped by
// kind in text.TUIFindingOrder, then by kind, each group in Doctor's order,
// and gentle-ai's copies left out (UX2).
func doctorOrder(t *testing.T, a *app.App) []app.Finding {
	t.Helper()
	findings, err := a.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range findings {
		if f.Kind != text.TUIGentleKind && !slices.Contains(kinds, f.Kind) {
			kinds = append(kinds, f.Kind)
		}
	}
	rank := func(k string) int {
		if i := slices.Index(text.TUIFindingOrder, k); i >= 0 {
			return i
		}
		return len(text.TUIFindingOrder)
	}
	slices.SortStableFunc(kinds, func(x, y string) int {
		if d := rank(x) - rank(y); d != 0 {
			return d
		}
		return strings.Compare(x, y)
	})
	var out []app.Finding
	for _, k := range kinds {
		for _, f := range findings {
			if f.Kind == k {
				out = append(out, f)
			}
		}
	}
	return out
}

// The Doctor screen lists the findings; choosing one shows its repairs,
// and the repair chosen runs only after its plan is confirmed.
func TestDoctorShowsRepairsOfTheChosenFindingAndAppliesOne(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".claude/skills/ghost", "../../.agents/skills/ghost")
	const ghost = "broken_link:.claude/skills/ghost"
	a := mc.newApp()
	m := open(t, a)
	before := mc.hash()

	pressRun(m, "4")
	if m.ActiveScreen() != tui.ScreenDoctor {
		t.Fatalf("6 opened %v, want ScreenDoctor", m.ActiveScreen())
	}
	for range findingIndex(t, a, ghost) {
		pressRun(m, "down")
	}
	got := screen(m)
	if !strings.Contains(got, "Enlaces rotos (") || !strings.Contains(got, ghost) {
		t.Fatalf("the findings lack the broken link:\n%s", got)
	}
	pressRun(m, "enter")
	got = screen(m)
	if !strings.Contains(got, text.TUIRepairsQuestion) || !strings.Contains(got, "ghost") ||
		!strings.Contains(got, text.TUIRepairPlain("delete_link")) {
		t.Fatalf("the repairs of the chosen finding are not shown:\n%s", got)
	}
	if mc.hash() != before {
		t.Fatal("listing findings or repairs wrote something")
	}
	pressRun(m, "enter")
	if !strings.Contains(screen(m), confirmPlan) || mc.hash() != before {
		t.Fatalf("the repair did not wait for its confirmation:\n%s", screen(m))
	}
	pressRun(m, "s")
	if mc.exists(".claude/skills/ghost") {
		t.Fatalf("the confirmed repair did not remove the link:\n%s", screen(m))
	}
	if strings.Contains(screen(m), ghost) {
		t.Errorf("the repaired finding is still listed:\n%s", screen(m))
	}
}

// A repair that needs the canonical folder asks for it among the finding's
// paths, and plans with the one chosen.
func TestDoctorAsksForTheCanonicalFolder(t *testing.T) {
	mc := newMachine(t)
	const finding = "no_canonical:dir:.copilot/skills/requirements-elicitation"
	a := mc.newApp()
	m := open(t, a)
	before := mc.hash()
	pressRun(m, "4")
	for range findingIndex(t, a, finding) {
		pressRun(m, "down")
	}
	pressRun(m, "enter") // its repairs: adopt
	pressRun(m, "enter") // adopt needs the canonical folder
	got := screen(m)
	if !strings.Contains(got, text.TUIChooseCanon) || strings.Contains(got, confirmPlan) {
		t.Fatalf("the canonical folder is not asked for:\n%s", got)
	}
	pressRun(m, "down")
	pressRun(m, "enter")
	pressRun(m, "v")
	got = screen(m)
	if !strings.Contains(got, "Plan: Reparar "+finding) || !strings.Contains(got, confirmPlan) {
		t.Fatalf("the chosen canonical folder did not give a plan:\n%s", got)
	}
	if mc.hash() != before {
		t.Fatal("choosing the canonical folder wrote something")
	}
	pressRun(m, "s")
	if !mc.exists(".agents/skills/requirements-elicitation/SKILL.md") {
		t.Fatalf("the confirmed adoption did not reach the store:\n%s", screen(m))
	}
}

// A finding with no repair says why, and nothing is planned.
func TestDoctorExplainsAFindingWithoutRepair(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	findings, err := a.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// gentle-ai's copies are folded in one line; a homonym is listed and
	// has no repair either.
	if !slices.ContainsFunc(findings, func(f app.Finding) bool { return f.Kind == "managed_by_gentle_ai" }) {
		t.Fatal("no managed_by_gentle_ai finding")
	}
	idx := slices.IndexFunc(doctorOrder(t, a), func(f app.Finding) bool { return f.Kind == "homonyms" })
	if idx < 0 {
		t.Fatal("no homonyms finding")
	}
	m := open(t, a)
	pressRun(m, "4")
	if got := screen(m); !strings.Contains(got, "skills de gentle-ai: las gestiona gentle-ai, skillhub no las toca.") {
		t.Errorf("gentle-ai's copies are not told:\n%s", got)
	}
	for range idx {
		pressRun(m, "down")
	}
	pressRun(m, "enter")
	got := screen(m)
	if !strings.Contains(got, "nunca une skills por su nombre") || strings.Contains(got, confirmPlan) {
		t.Errorf("the finding without repair is not explained:\n%s", got)
	}
}

// The help lists the new keys in Spanish.
func TestHelpListsTheVersion2Keys(t *testing.T) {
	m := open(t, newMachine(t).newApp())
	pressRun(m, "?")
	got := screen(m)
	for _, want := range []string{"d desactivar", "a activar", "e eliminar", "l elegir agentes", "3 Instalar", "4 Salud"} {
		if !strings.Contains(flat(got), want) {
			t.Errorf("the help lacks %q:\n%s", want, got)
		}
	}
}
