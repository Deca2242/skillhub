package tui_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

func setupActivityFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v1\n"})
	f.publish("beta", map[string]string{"SKILL.md": "# Beta v1\n"})
	f.installLock([]string{"alpha", "beta"}, map[string]map[string]string{
		"alpha": {"SKILL.md": "# Alpha v1\n"},
		"beta":  {"SKILL.md": "# Beta v1\n"},
	})
	return f
}

func TestSkillActivityInPanelAndDetail(t *testing.T) {
	t.Run("v1 apply and undo appear in panel and detail with dates", func(t *testing.T) {
		f := setupActivityFixture(t)
		f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v2\n"})
		a := f.newApp()
		if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
			t.Fatalf("Check: %v", err)
		}
		cands, err := a.Candidates()
		if err != nil || len(cands) != 1 {
			t.Fatalf("Candidates = %v, %v", cands, err)
		}
		res, err := a.Apply(t.Context(), updates.Approval{CandidateID: cands[0].ID, ToSHA: cands[0].ToSHA})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		f.clock.Advance(10 * time.Minute)
		if _, err := a.Undo(t.Context(), updates.UndoRequest{Target: domain.LockID("alpha"), SnapshotID: res.SnapshotID}); err != nil {
			t.Fatalf("Undo: %v", err)
		}
		f.clock.Advance(5 * time.Minute)

		m := openSized(t, f.newApp(), 100, 35)
		selectSkill(t, m, "alpha")

		panel := screen(m)
		for _, want := range []string{
			text.TUIActivityHeader,
			"Actualización deshecha — hace 5 minutos",
			"Actualización aplicada — hace 15 minutos",
		} {
			if !strings.Contains(panel, want) {
				t.Errorf("panel lacks %q:\n%s", want, panel)
			}
		}

		// Beta has no activity and must not show alpha's activity.
		mBeta := openSized(t, f.newApp(), 100, 35)
		selectSkill(t, mBeta, "beta")
		betaPanel := screen(mBeta)
		if !strings.Contains(betaPanel, text.TUIActivityNone) {
			t.Errorf("beta panel lacks %q:\n%s", text.TUIActivityNone, betaPanel)
		}
		if strings.Contains(betaPanel, "Actualización") {
			t.Errorf("beta panel leaked alpha activity:\n%s", betaPanel)
		}

		// Full detail view for alpha shows all entries.
		pressRun(m, "enter")
		detail := screen(m)
		for _, want := range []string{
			text.TUIActivityHeader,
			"Actualización deshecha — hace 5 minutos",
			"Actualización aplicada — hace 15 minutos",
		} {
			if !strings.Contains(detail, want) {
				t.Errorf("detail lacks %q:\n%s", want, detail)
			}
		}
	})

	t.Run("v2 disable and undo appear in panel and detail", func(t *testing.T) {
		mc := newMachine(t)
		a := mc.newApp()
		plan, err := a.PlanDisable(t.Context(), "agent-browser")
		if err != nil {
			t.Fatalf("PlanDisable: %v", err)
		}
		res, err := a.ApplyPlan(t.Context(), plan.Request, plan.PlanID)
		if err != nil {
			t.Fatalf("ApplyPlan disable: %v", err)
		}
		mc.clock.Advance(4 * time.Minute)
		undoPlan, err := a.PlanUndo(t.Context(), res.SnapshotID)
		if err != nil {
			t.Fatalf("PlanUndo: %v", err)
		}
		if _, err := a.ApplyPlan(t.Context(), undoPlan.Request, undoPlan.PlanID); err != nil {
			t.Fatalf("ApplyPlan undo: %v", err)
		}
		mc.clock.Advance(2 * time.Minute)

		m := openSized(t, mc.newApp(), 100, 35)
		selectSkill(t, m, "agent-browser")

		panel := screen(m)
		for _, want := range []string{
			text.TUIActivityHeader,
			"Deshiciste «desactivar» — hace 2 minutos",
			"Desactivada — hace 6 minutos",
		} {
			if !strings.Contains(panel, want) {
				t.Errorf("panel lacks %q:\n%s", want, panel)
			}
		}

		pressRun(m, "enter")
		detail := screen(m)
		for _, want := range []string{
			"Deshiciste «desactivar» — hace 2 minutos",
			"Desactivada — hace 6 minutos",
		} {
			if !strings.Contains(detail, want) {
				t.Errorf("detail lacks %q:\n%s", want, detail)
			}
		}
	})

	t.Run("failed operation appears with date and panel caps at three entries", func(t *testing.T) {
		f := setupActivityFixture(t)
		now := f.clock.Now()
		stStore := statefile.New(f.home.Paths())
		for _, entry := range []domain.HistoryEntry{
			{At: now.Add(-40 * time.Minute), Action: domain.ActionApplied, Target: "lock:alpha", SnapshotID: "s1"},
			{At: now.Add(-30 * time.Minute), Action: domain.ActionUndone, Target: "lock:alpha", SnapshotID: "s1"},
			{At: now.Add(-20 * time.Minute), Action: domain.ActionSkipped, Target: "lock:alpha"},
			{At: now.Add(-10 * time.Minute), Action: domain.ActionFailed, Target: "lock:alpha"},
		} {
			if err := stStore.AppendJournal(entry); err != nil {
				t.Fatalf("AppendJournal: %v", err)
			}
		}

		m := openSized(t, f.newApp(), 100, 35)
		selectSkill(t, m, "alpha")

		panel := screen(m)
		for _, want := range []string{
			"Falló la actualización — hace 10 minutos",
			"Actualización omitida — hace 20 minutos",
			"Actualización deshecha — hace 30 minutos",
		} {
			if !strings.Contains(panel, want) {
				t.Errorf("panel lacks %q:\n%s", want, panel)
			}
		}
		if strings.Contains(panel, "hace 40 minutos") {
			t.Errorf("panel should show at most 3 entries:\n%s", panel)
		}

		pressRun(m, "enter")
		detail := screen(m)
		if !strings.Contains(detail, "Actualización aplicada — hace 40 minutos") {
			t.Errorf("detail should show all entries including 4th:\n%s", detail)
		}
	})
}

func TestGoldenActivity(t *testing.T) {
	f := setupActivityFixture(t)
	now := f.clock.Now()
	stStore := statefile.New(f.home.Paths())
	for _, entry := range []domain.HistoryEntry{
		{At: now.Add(-25 * time.Minute), Action: domain.ActionApplied, Target: "lock:alpha", SnapshotID: "s1"},
		{At: now.Add(-15 * time.Minute), Action: domain.ActionUndone, Target: "lock:alpha", SnapshotID: "s1"},
		{At: now.Add(-5 * time.Minute), Action: domain.ActionFailed, Target: "lock:alpha"},
	} {
		if err := stStore.AppendJournal(entry); err != nil {
			t.Fatalf("AppendJournal: %v", err)
		}
	}

	for _, width := range []int{80, 100} {
		m := openSized(t, f.newApp(), width, 30)
		selectSkill(t, m, "alpha")
		t.Run(fmt.Sprintf("Skills%d", width), func(t *testing.T) {
			teatest.RequireEqualOutput(t, []byte(ansi.Strip(m.View().Content)))
		})
	}

	m := openSized(t, f.newApp(), 160, 30)
	selectSkill(t, m, "alpha")
	pressRun(m, "enter")
	t.Run("Detail", func(t *testing.T) {
		got := strings.ReplaceAll(ansi.Strip(m.View().Content), f.home.Paths().Home(), "~")
		lines := strings.Split(got, "\n")
		for i, l := range lines {
			l = strings.TrimRight(l, " ")
			l = strings.TrimSuffix(l, "│")
			lines[i] = strings.TrimRight(l, " ")
		}
		teatest.RequireEqualOutput(t, []byte(strings.Join(lines, "\n")))
	})
}
