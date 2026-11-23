package tui_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
)

func TestCompareTwoAgentsShowsThreeListsAndIndeterminateWithOracle(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()

	// Independent oracle from app.Sightings and app.Skills.
	sgs, err := a.Sightings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	skills, err := a.Skills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nameByID := make(map[string]string, len(skills))
	for _, sk := range skills {
		nameByID[sk.ID] = sk.Name
	}

	const firstID, secondID = "claude-code", "codex"
	var wantCommon, wantFirst, wantSecond []string
	for id, list := range sgs {
		var hasA, hasB, indet bool
		for _, sg := range list {
			if sg.Harness == firstID {
				hasA = true
				if sg.Kind == app.SightingIndeterminate {
					indet = true
				}
			}
			if sg.Harness == secondID {
				hasB = true
				if sg.Kind == app.SightingIndeterminate {
					indet = true
				}
			}
		}
		if indet {
			continue
		}
		name := nameByID[id]
		if name == "" {
			name = id
		}
		switch {
		case hasA && hasB:
			wantCommon = append(wantCommon, name)
		case hasA:
			wantFirst = append(wantFirst, name)
		case hasB:
			wantSecond = append(wantSecond, name)
		}
	}

	m := openSized(t, a, 100, 40)
	pressRun(m, "g")

	moveTo(t, m, "Claude Code")
	pressRun(m, "m")
	if got := screen(m); !strings.Contains(got, "Claude Code") {
		t.Fatalf("after marking first agent, screen lacks Claude Code:\n%s", got)
	}

	moveTo(t, m, "Codex")
	pressRun(m, "m")

	// Collect all lines across scroll so every item is checked.
	got := collectScrolledScreen(m)

	if !strings.Contains(got, text.TUICompareTitle("Claude Code", "Codex")) {
		t.Errorf("compare view lacks title:\n%s", got)
	}
	if !strings.Contains(got, text.TUICompareSection(text.TUICompareCommon, len(wantCommon))) {
		t.Errorf("compare view lacks common count %d:\n%s", len(wantCommon), got)
	}
	if !strings.Contains(got, text.TUICompareSection(text.TUICompareOnly("Claude Code"), len(wantFirst))) {
		t.Errorf("compare view lacks first-only count %d:\n%s", len(wantFirst), got)
	}
	if !strings.Contains(got, text.TUICompareSection(text.TUICompareOnly("Codex"), len(wantSecond))) {
		t.Errorf("compare view lacks second-only count %d:\n%s", len(wantSecond), got)
	}
	for _, name := range append(append(wantCommon, wantFirst...), wantSecond...) {
		if !strings.Contains(got, name) {
			t.Errorf("compare view lacks skill %q:\n%s", name, got)
		}
	}

	// esc returns to the agent filter, and enter still filters Skills as before (INV8).
	pressRun(m, "esc")
	if got := screen(m); !strings.Contains(got, text.TUIAgentFilterQuestion) {
		t.Fatalf("esc from compare did not return to agent filter:\n%s", got)
	}
	moveTo(t, m, "Claude Code")
	pressRun(m, "enter")
	if got := screen(m); !strings.Contains(got, "Qué ve Claude Code") {
		t.Errorf("enter in agent filter no longer filters Skills:\n%s", got)
	}
}

func TestCompareSameAgentTwiceAndSingleDetectedAgent(t *testing.T) {
	t.Run("same agent twice", func(t *testing.T) {
		mc := newMachine(t)
		a := mc.newApp()
		m := openSized(t, a, 100, 35)
		pressRun(m, "g")
		moveTo(t, m, "Claude Code")
		pressRun(m, "m")
		pressRun(m, "m")
		got := screen(m)
		if !strings.Contains(got, text.TUICompareSameAgent) {
			t.Errorf("same-agent compare lacks notice %q:\n%s", text.TUICompareSameAgent, got)
		}
		if !strings.Contains(got, text.TUICompareEmptyList) {
			t.Errorf("same-agent compare lacks empty-list marker %q:\n%s", text.TUICompareEmptyList, got)
		}
	})

	t.Run("single detected agent", func(t *testing.T) {
		f := newFixture(t)
		// Only claude-code is configured and detected.
		if err := os.MkdirAll(f.home.Path(".claude/skills/alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTree(t, f.home.Path(".claude/skills/alpha"), map[string]string{
			"SKILL.md": "---\nname: alpha\ndescription: Alpha skill\n---\n",
		})
		a := f.newApp()
		m := openSized(t, a, 100, 35)
		pressRun(m, "g")
		moveTo(t, m, "Claude Code")
		pressRun(m, "m")
		got := screen(m)
		if !strings.Contains(got, text.TUICompareSingleAgent) {
			t.Errorf("single-agent compare lacks notice %q:\n%s", text.TUICompareSingleAgent, got)
		}
	})
}

func TestCompareSanitizesEscapeSequencesInSkillNames(t *testing.T) {
	comp := app.AgentComparison{
		First:      "claude-code",
		Second:     "codex",
		Common:     []string{"evil-id"},
		OnlyFirst:  []string{},
		OnlySecond: []string{},
	}
	got := tui.RenderCompareBodyForTest(
		comp,
		func(id string) string { return id },
		func(id string) string { return "evil\x1b[31mred\r\n" },
		100,
		false,
	)
	if strings.Contains(got, "\x1b[31m") {
		t.Errorf("compare view contains raw escape sequence:\n%q", got)
	}
	if !strings.Contains(got, `\x1b[31mred\r\n`) {
		t.Errorf("compare view did not escape ANSI/control sequence visibly:\n%s", got)
	}
}

func TestCompareIndeterminateShownApartWithReason(t *testing.T) {
	comp := app.AgentComparison{
		First:      "claude-code",
		Second:     "codex",
		Common:     []string{},
		OnlyFirst:  []string{},
		OnlySecond: []string{},
		Indeterminate: []app.IndeterminateSkill{
			{
				ID:     "mystery",
				Agents: []string{"claude-code"},
				Reasons: []app.Sighting{
					{Harness: "claude-code", Kind: app.SightingIndeterminate, Entry: ".claude/skills/mystery"},
				},
			},
		},
	}
	out := tui.RenderCompareBodyForTest(comp, func(id string) string {
		if id == "claude-code" {
			return "Claude Code"
		}
		return "Codex"
	}, func(id string) string { return id }, 80, false)
	if !strings.Contains(out, text.TUICompareIndeterminate) {
		t.Errorf("output lacks indeterminate section:\n%s", out)
	}
	if !strings.Contains(out, "mystery") || !strings.Contains(out, text.TUISightingReason(app.SightingIndeterminate)) {
		t.Errorf("output lacks skill or indeterminate reason:\n%s", out)
	}
}

func TestGoldenCompareAgents(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			mc := newMachine(t)
			a := mc.newApp()
			m := openSized(t, a, width, 30)
			pressRun(m, "g")
			moveTo(t, m, "Claude Code")
			pressRun(m, "m")
			moveTo(t, m, "Codex")
			pressRun(m, "m")
			teatest.RequireEqualOutput(t, stable(mc, screen(m)))
		})
	}
}

func collectScrolledScreen(m *tui.Model) string {
	var parts []string
	seen := map[string]bool{}
	for range 30 {
		s := screen(m)
		if !seen[s] {
			seen[s] = true
			parts = append(parts, s)
		}
		pressRun(m, "down")
	}
	return strings.Join(parts, "\n")
}

var _ = slices.Equal[[]string, string]

func TestCompareHomonymsFromDoctorAndSkillDetail(t *testing.T) {
	setupHomonyms := func(t *testing.T) (*machine, *app.App, *tui.Model) {
		t.Helper()
		mc := newMachine(t)
		writeTree(t, mc.h.Path(".claude-omni/skills/orca-cli"), map[string]string{
			"SKILL.md": "---\nname: orca-cli\ndescription: Omni copy\n---\n# orca-cli\nfrom claude-omni\n",
		})
		a := mc.newApp()
		m := openSized(t, a, 100, 40)
		return mc, a, m
	}

	t.Run("from Salud homonyms finding", func(t *testing.T) {
		_, a, m := setupHomonyms(t)
		pressRun(m, "4")

		rows := healthOracle(t, a)
		idx := slices.IndexFunc(rows, func(r healthRow) bool {
			return r.finding.ID == "homonyms:orca-cli"
		})
		if idx < 0 {
			t.Fatal("homonyms:orca-cli not found in Salud")
		}
		for range idx {
			pressRun(m, "down")
		}
		doctorScreen := screen(m)
		if !strings.Contains(doctorScreen, "homonyms:orca-cli") {
			t.Fatalf("Salud did not select homonyms:orca-cli:\n%s", doctorScreen)
		}
		if !strings.Contains(doctorScreen, text.TUIActionCompare) {
			t.Errorf("Salud panel for homonyms lacks %q:\n%s", text.TUIActionCompare, doctorScreen)
		}

		pressRun(m, "m")
		got := collectScrolledScreen(m)
		for _, want := range []string{
			text.TUISkillCompareTitle("orca-cli"),
			"Claude Code",
			"~/.agents/skills/orca-cli",
			"~/.claude-omni/skills/orca-cli",
			"SKILL.md",
			"--- a/SKILL.md",
			"+++ b/SKILL.md",
			"-from claude-omni",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("compare view from Salud lacks %q:\n%s", want, got)
			}
		}

		// esc returns to Salud.
		pressRun(m, "esc")
		if back := screen(m); !strings.Contains(back, text.TUIFindingsHint) {
			t.Errorf("esc did not return to Salud:\n%s", back)
		}
	})

	t.Run("from skill detail and non-homonym skill", func(t *testing.T) {
		_, _, m := setupHomonyms(t)

		// Open detail for brainstorming (non-homonym): m should not be offered and should do nothing.
		selectSkill(t, m, "brainstorming")
		pressRun(m, "enter")
		if detail := screen(m); strings.Contains(detail, text.TUIKeyCompareSkills) {
			t.Errorf("non-homonym detail should not show %q:\n%s", text.TUIKeyCompareSkills, detail)
		}
		pressRun(m, "m")
		if detail := screen(m); strings.Contains(detail, text.TUISkillCompareTitle("brainstorming")) {
			t.Errorf("m on non-homonym should not open compare view:\n%s", detail)
		}
		pressRun(m, "esc")
		// Clear the active filter before selecting orca-cli.
		pressRun(m, "/")
		pressRun(m, "esc")

		// Open detail for orca-cli: m should open the comparison view and esc returns to detail.
		selectSkill(t, m, "orca-cli")
		pressRun(m, "enter")
		if detail := screen(m); !strings.Contains(detail, text.TUIKeyCompareSkills) {
			t.Errorf("homonym detail lacks %q:\n%s", text.TUIKeyCompareSkills, detail)
		}
		pressRun(m, "m")
		got := collectScrolledScreen(m)
		for _, want := range []string{
			text.TUISkillCompareTitle("orca-cli"),
			"Claude Code",
			"~/.agents/skills/orca-cli",
			"~/.claude-omni/skills/orca-cli",
			"--- a/SKILL.md",
			"+++ b/SKILL.md",
			"+from claude-omni",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("compare view from detail lacks %q:\n%s", want, got)
			}
		}

		pressRun(m, "esc")
		if back := screen(m); !strings.Contains(back, "Skill: orca-cli") {
			t.Errorf("esc did not return to skill detail:\n%s", back)
		}
	})

	t.Run("sanitizes hostile text in paths and diffs", func(t *testing.T) {
		mc := newMachine(t)
		writeTree(t, mc.h.Path(".claude-omni/skills/orca-cli"), map[string]string{
			"SKILL.md": "---\nname: orca-cli\ndescription: Omni\n---\nline" + hostile + "hidden\n",
		})
		a := mc.newApp()
		m := openSized(t, a, 100, 35)
		pressRun(m, "4")
		rows := healthOracle(t, a)
		idx := slices.IndexFunc(rows, func(r healthRow) bool {
			return r.finding.ID == "homonyms:orca-cli"
		})
		for range idx {
			pressRun(m, "down")
		}
		pressRun(m, "m")
		assertNothingActs(t, m.View().Content)
		if !strings.Contains(screen(m), "line"+hostileShown+"hidden") {
			t.Errorf("compare view did not escape hostile diff line:\n%s", screen(m))
		}
	})
}

func TestGoldenSkillCompare(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			mc := newMachine(t)
			writeTree(t, mc.h.Path(".claude-omni/skills/orca-cli"), map[string]string{
				"SKILL.md": "---\nname: orca-cli\ndescription: Omni copy\n---\n# orca-cli\nfrom claude-omni\n",
			})
			a := mc.newApp()
			m := openSized(t, a, width, 35)
			pressRun(m, "4")

			rows := healthOracle(t, a)
			idx := slices.IndexFunc(rows, func(r healthRow) bool {
				return r.finding.ID == "homonyms:orca-cli"
			})
			if idx < 0 {
				t.Fatal("homonyms:orca-cli not found")
			}
			for range idx {
				pressRun(m, "down")
			}
			pressRun(m, "m")
			teatest.RequireEqualOutput(t, stable(mc, screen(m)))
		})
	}
}
