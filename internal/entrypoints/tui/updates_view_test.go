package tui_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func setupCheckedNoUpdates(t *testing.T, advance time.Duration) *app.App {
	t.Helper()
	f := newFixture(t)
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v1\n"})
	f.installLock([]string{"alpha"}, map[string]map[string]string{
		"alpha": {"SKILL.md": "# Alpha v1\n"},
	})
	a := f.newApp()
	res, err := a.Check(t.Context(), app.CheckOptions{})
	if err != nil {
		t.Fatalf("a.Check: %v", err)
	}
	if res.Failed {
		t.Fatalf("a.Check failed unexpectedly")
	}
	f.clock.Advance(advance)
	return a
}

func TestUpdatesViewDistinguishesNeverCheckedFromNoChanges(t *testing.T) {
	t.Run("never checked", func(t *testing.T) {
		f := newFixture(t)
		m := openSized(t, f.newApp(), 100, 30)
		pressRun(m, "2")

		got := ansi.Strip(m.View().Content)
		if !strings.Contains(got, text.TUIUpdatesNeverChecked) {
			t.Errorf("want %q in view:\n%s", text.TUIUpdatesNeverChecked, got)
		}
		if strings.Contains(got, "última revisión") {
			t.Errorf("never-checked view should not mention a last check:\n%s", got)
		}
	})

	t.Run("checked with no candidates", func(t *testing.T) {
		a := setupCheckedNoUpdates(t, 2*time.Hour)
		m := openSized(t, a, 100, 30)
		pressRun(m, "2")

		got := ansi.Strip(m.View().Content)
		want := "No hay cambios desde la última revisión (hace 2 horas)."
		if !strings.Contains(got, want) {
			t.Errorf("want %q in view:\n%s", want, got)
		}
		if strings.Contains(got, text.TUIUpdatesNeverChecked) {
			t.Errorf("checked view should not say never checked:\n%s", got)
		}
	})

	t.Run("with candidates list is unchanged", func(t *testing.T) {
		f := newFixture(t)
		f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
		a := f.newApp()
		if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
			t.Fatalf("a.Check: %v", err)
		}
		f.clock.Advance(2 * time.Hour)

		m := openSized(t, a, 100, 30)
		pressRun(m, "2")

		got := ansi.Strip(m.View().Content)
		if !strings.Contains(got, "alpha") {
			t.Errorf("expected candidate alpha in view:\n%s", got)
		}
		if strings.Contains(got, text.TUIUpdatesNeverChecked) || strings.Contains(got, "última revisión") {
			t.Errorf("view with candidates should not show empty-state message:\n%s", got)
		}
	})
}

func TestGoldenUpdatesLastCheck(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("NeverChecked%d", width), func(t *testing.T) {
			mc := newMachine(t)
			m := openSized(t, mc.newApp(), width, 30)
			pressRun(m, "2")
			teatest.RequireEqualOutput(t, stable(mc, screen(m)))
		})
		t.Run(fmt.Sprintf("NoChanges%d", width), func(t *testing.T) {
			a := setupCheckedNoUpdates(t, 2*time.Hour)
			m := openSized(t, a, width, 30)
			pressRun(m, "2")
			teatest.RequireEqualOutput(t, []byte(ansi.Strip(m.View().Content)))
		})
	}
}
