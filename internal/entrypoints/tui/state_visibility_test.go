package tui_test

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/domain"
)

func TestPanelSeparatesManagementStateFromVisibility(t *testing.T) {
	t.Run("ignored skill still visible to agents", func(t *testing.T) {
		mc := newMachine(t)
		a := mc.newApp()
		mc.ignore(a, "domain-driven-design")
		m := openSized(t, a, 100, 30)
		pressRun(m, "f") // activas
		pressRun(m, "f") // desactivadas
		pressRun(m, "f") // con problemas
		pressRun(m, "f") // con actualización
		pressRun(m, "f") // ignoradas
		selectSkill(t, m, "domain-driven-design")
		got := flat(screen(m))
		for _, want := range []string{
			"En skillhub: ignorada",
			"Disponible en: Codex, OpenCode y Pi",
		} {
			if !strings.Contains(got, flat(want)) {
				t.Errorf("ignored panel lacks %q:\n%s", want, screen(m))
			}
		}
	})

	t.Run("OFF skill still visible to agents", func(t *testing.T) {
		mc := newMachine(t)
		m := openSized(t, mc.newApp(), 100, 30)
		selectSkill(t, m, "accessibility")
		got := flat(screen(m))
		for _, want := range []string{
			"En skillhub: desactivada (a mano en OFF/)",
			"Disponible en: Codex, OpenCode y Pi",
		} {
			if !strings.Contains(got, flat(want)) {
				t.Errorf("OFF panel lacks %q:\n%s", want, screen(m))
			}
		}
	})

	t.Run("parked skill disabled with skillhub", func(t *testing.T) {
		mc := newMachine(t)
		a := mc.newApp()
		mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
		m := openSized(t, a, 100, 30)
		selectSkill(t, m, "agent-browser")
		got := flat(screen(m))
		for _, want := range []string{
			"En skillhub: desactivada",
			"Disponible en: ningún agente",
		} {
			if !strings.Contains(got, flat(want)) {
				t.Errorf("parked panel lacks %q:\n%s", want, screen(m))
			}
		}
	})

	t.Run("active skill seen by no agent", func(t *testing.T) {
		a := setupGoldenFixture(t)
		m := openSized(t, a, 100, 30)
		selectSkill(t, m, "alpha")
		got := flat(screen(m))
		for _, want := range []string{
			"En skillhub: activa",
			"Disponible en: ningún agente",
		} {
			if !strings.Contains(got, flat(want)) {
				t.Errorf("active hidden panel lacks %q:\n%s", want, screen(m))
			}
		}
		if !strings.Contains(got, "3 activas sin agente") {
			t.Errorf("summary does not distinguish active skill with no agent:\n%s", screen(m))
		}
	})
}

func TestSummarySeparatesManagementFromVisibilityAndNamesExclusions(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), 80, 30)
	got := screen(m)
	flatGot := flat(got)
	for _, want := range []string{
		"Visibles:",
		"desactivadas visibles",
	} {
		if !strings.Contains(flatGot, flat(want)) {
			t.Errorf("Skills screen lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(flatGot, flat(text.TUISkillsExclusions)) {
		t.Errorf("Skills header should not contain exclusions:\n%s", got)
	}
	var visLine string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "Visibles:") {
			visLine = l
			break
		}
	}
	if visLine == "" {
		t.Fatalf("visibility summary line not found:\n%s", got)
	}
	if w := lipgloss.Width(visLine); w > 80 {
		t.Errorf("visibility summary line width = %d, want <= 80: %q", w, visLine)
	}

	// Help (?) names what the inventory leaves out (S1).
	pressRun(m, "?")
	help := flat(screen(m))
	for _, want := range []string{
		"skills de proveedor",
		"plugins de Claude Code",
		"gentle-ai",
		"de proyecto",
	} {
		if !strings.Contains(help, flat(want)) {
			t.Errorf("help lacks exclusion %q:\n%s", want, screen(m))
		}
	}
}

func TestPresentSkillDetailSeparatesStateAndVisibility(t *testing.T) {
	activeNoAgent := domain.Skill{
		ID:        "lock:orphan",
		Name:      "orphan",
		Origin:    domain.Origin{Kind: domain.OriginLock, Lock: &domain.LockOrigin{Source: "owner/orphan"}},
		State:     domain.StateUpToDate,
		Locations: []domain.Location{{AgentID: domain.StoreAgentID, Kind: domain.LocationReal, Path: "/store/orphan"}},
	}
	gotActive := tui.PresentSkillDetail(activeNoAgent)
	for _, want := range []string{"En skillhub: activa", "Disponible en: ningún agente"} {
		if !strings.Contains(gotActive, want) {
			t.Errorf("PresentSkillDetail(activeNoAgent) lacks %q:\n%s", want, gotActive)
		}
	}

	offVisible := domain.Skill{
		ID:     "lock:off-skill",
		Name:   "off-skill",
		Origin: domain.Origin{Kind: domain.OriginLock, Lock: &domain.LockOrigin{Source: "owner/off-skill"}},
		State:  domain.StateDisabled,
		Locations: []domain.Location{
			{AgentID: "codex", Kind: domain.LocationReal, Path: "/codex/off-skill"},
		},
	}
	gotOff := tui.PresentSkillDetail(offVisible)
	for _, want := range []string{"En skillhub: desactivada (a mano en OFF/)", "Disponible en: codex"} {
		if !strings.Contains(gotOff, want) {
			t.Errorf("PresentSkillDetail(offVisible) lacks %q:\n%s", want, gotOff)
		}
	}
}

func TestGoldenVisibility(t *testing.T) {
	for _, width := range []int{80, 100} {
		a := setupGoldenFixture(t)
		m := openSized(t, a, width, 30)
		selectSkill(t, m, "alpha")
		t.Run(fmt.Sprintf("ActiveNoAgent%d", width), func(t *testing.T) {
			teatest.RequireEqualOutput(t, []byte(screen(m)))
		})
	}
}
