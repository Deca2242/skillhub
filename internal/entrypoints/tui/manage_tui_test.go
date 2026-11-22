package tui_test

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest/v2"
)

// The whole flow in a running program: disable a skill, confirm, apply,
// and undo it from Historial with its own plan and confirmation. The home
// ends as it began, apart from skillhub's own state.
func TestTeatestDisableConfirmApplyAndUndo(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	tm := newTestTUI(t, a)
	waitForText(t, tm, "agent-browser")

	tm.Type("/agent-browser")
	tm.Send(keyPress("enter"))
	// The panel offers disabling once the app told what the skill admits.
	waitForText(t, tm, "d desactivar")
	tm.Type("D")
	waitForText(t, tm, "¿Desactivar agent-browser?")
	tm.Type("s")
	waitForText(t, tm, "está desactivada")

	if mc.exists(".agents/skills/agent-browser") || mc.exists(".claude/skills/agent-browser") {
		t.Fatal("the disable did not run")
	}

	tm.Type("h")
	// The journal line's detail, not the result's undo hint, which also
	// names v2:disable.
	waitForText(t, tm, "plan=")
	tm.Type("u")
	waitForText(t, tm, "¿Deshacer «desactivar»?")
	tm.Type("s")
	waitForText(t, tm, "v2:undo")

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	if !mc.exists(".agents/skills/agent-browser/SKILL.md") || !mc.exists(".claude/skills/agent-browser") {
		t.Fatal("the undo did not restore the folder and its link")
	}
	if _, ok := mc.persisted().Parked["agent-browser"]; ok {
		t.Fatal("the undo left the lock entry parked")
	}
	lines := mc.v2Lines()
	if len(lines) != 2 || lines[0].Target != "v2:disable" || lines[1].Target != "v2:undo" {
		t.Fatalf("journal = %+v, want v2:disable then v2:undo", lines)
	}
}

func TestGoldenScreenInstall(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), goldenWidth, 40)
	pressRun(m, "3")
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}

func TestGoldenScreenInstallReview(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	m := openSized(t, mc.newApp(), goldenWidth, 40)
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/tools")
	pressRun(m, "enter")
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}

func TestGoldenScreenDoctor(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), goldenWidth, 40)
	pressRun(m, "4")
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}

func TestGoldenScreenDoctorRepairs(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".claude/skills/ghost", "../../.agents/skills/ghost")
	a := mc.newApp()
	m := openSized(t, a, goldenWidth, 40)
	pressRun(m, "4")
	for range findingIndex(t, a, "broken_link:.claude/skills/ghost") {
		pressRun(m, "down")
	}
	pressRun(m, "enter")
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}

func TestGoldenPlanConfirmation(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), goldenWidth, 40)
	selectSkill(t, m, "agent-browser")
	pressRun(m, "D")
	pressRun(m, "v") // the technical detail, under the summary
	teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
}
