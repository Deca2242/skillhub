package tui_test

import (
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/domain"
)

func TestPanelExplainsWhyEachAgentSeesSkill(t *testing.T) {
	t.Run("direct and via link in panel and paths in full detail", func(t *testing.T) {
		mc := newMachine(t)
		m := openSized(t, mc.newApp(), 100, 30)
		selectSkill(t, m, "agent-browser")
		panel := flat(screen(m))
		for _, want := range []string{
			"Claude Code: por un enlace",
			"directamente, sin enlace",
		} {
			if !strings.Contains(panel, flat(want)) {
				t.Errorf("panel lacks %q:\n%s", want, screen(m))
			}
		}
		if strings.Contains(panel, ".claude/skills/agent-browser") {
			t.Errorf("panel should not show paths:\n%s", screen(m))
		}

		// Full detail (enter) shows the sighting paths.
		pressRun(m, "enter")
		detail := flat(screen(m))
		for _, want := range []string{
			"Claude Code: por un enlace",
			".claude/skills/agent-browser",
			".agents/skills/agent-browser",
		} {
			if !strings.Contains(detail, flat(want)) {
				t.Errorf("detail view lacks %q:\n%s", want, screen(m))
			}
		}
	})

	t.Run("own copy in panel", func(t *testing.T) {
		mc := newMachine(t)
		m := openSized(t, mc.newApp(), 100, 30)
		selectSkill(t, m, "humanizer")
		panel := flat(screen(m))
		if !strings.Contains(panel, flat("Claude Code y OpenCode: por una copia propia")) {
			t.Errorf("humanizer panel lacks own copy reason:\n%s", screen(m))
		}
	})

	t.Run("indeterminate in full detail", func(t *testing.T) {
		sk := domain.Skill{
			ID:     "dir:custom",
			Name:   "custom",
			Origin: domain.Origin{Kind: domain.OriginNone},
			State:  domain.StateNoOrigin,
		}
		sgs := []app.Sighting{
			{Harness: "cursor", Kind: app.SightingIndeterminate, Entry: "/home/u/.agents/skills/custom"},
		}
		got := tui.PresentSkillDetailWithSightings(sk, sgs, func(id string) string { return "Cursor" })
		for _, want := range []string{
			"Cursor: indeterminado",
			"/home/u/.agents/skills/custom",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("PresentSkillDetailWithSightings lacks %q:\n%s", want, got)
			}
		}
	})
}
