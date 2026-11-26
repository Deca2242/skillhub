package tui_test

import (
	"fmt"
	"strings"
	"testing"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/graph/graphtest"
)

func TestTUISkillsSectionsOrderBoundarySearchAndFilters(t *testing.T) {
	mc := newMachine(t)
	proj := realTUITempDir(t)
	writeTUIProjectSkill(t, proj, ".agents/skills/proj-alpha/SKILL.md", graphtest.SkillMD("proj-alpha", "project alpha skill"))
	writeTUIProjectSkill(t, proj, ".claude/skills/proj-grill/SKILL.md", graphtest.SkillMD("proj-grill", "project grill skill"))

	a := newProjectTUIApp(t, mc, proj)
	m := openSized(t, a, 100, 30)

	initial := screen(m)
	globalCount := m.RowCountForTest() - 2
	globalHeader := fmt.Sprintf("── Globales (%d) ──", globalCount)
	projectHeader := "── Proyecto actual (2) ──"

	gIdx := strings.Index(initial, globalHeader)
	pIdx := strings.Index(initial, projectHeader)
	if gIdx < 0 {
		t.Fatalf("missing global section header %q:\n%s", globalHeader, initial)
	}
	if pIdx < 0 {
		t.Fatalf("missing project section header %q:\n%s", projectHeader, initial)
	}
	if gIdx >= pIdx {
		t.Fatalf("expected global header before project header (%d >= %d):\n%s", gIdx, pIdx, initial)
	}

	// Navigating across the boundary skips the non-selectable project header
	// and selects the first project skill, updating the detail panel.
	for range globalCount - 1 {
		pressRun(m, "down")
	}
	lastGlobal := screen(m)
	if strings.Contains(flat(lastGlobal), flat("Skill del proyecto actual")) {
		t.Fatalf("expected last global row before crossing boundary:\n%s", lastGlobal)
	}

	pressRun(m, "down")
	firstProj := screen(m)
	if !strings.Contains(flat(firstProj), flat("Skill del proyecto actual (.agents/skills)")) || !strings.Contains(firstProj, "│ proj-alpha") {
		t.Fatalf("expected first project skill selected after crossing boundary:\n%s", firstProj)
	}

	pressRun(m, "up")
	backToGlobal := screen(m)
	if strings.Contains(flat(backToGlobal), flat("Skill del proyecto actual")) {
		t.Fatalf("expected up from first project skill to return to last global skill:\n%s", backToGlobal)
	}

	// Search matching both global and project skills updates both section counts.
	typeQuery(m, "grill")
	bothSearch := screen(m)
	if !strings.Contains(bothSearch, "── Globales (2) ──") || !strings.Contains(bothSearch, "── Proyecto actual (1) ──") {
		t.Fatalf("expected both sections (2 global, 1 project) for 'grill':\n%s", bothSearch)
	}

	// Search matching only global skills omits the project section header.
	pressRun(m, "/")
	pressRun(m, "esc")
	typeQuery(m, "agent-browser")
	globalOnlySearch := screen(m)
	if !strings.Contains(globalOnlySearch, "── Globales (1) ──") {
		t.Fatalf("expected global section with count 1:\n%s", globalOnlySearch)
	}
	if strings.Contains(globalOnlySearch, "Proyecto actual") {
		t.Fatalf("expected empty project section to be omitted:\n%s", globalOnlySearch)
	}

	// Search matching only project skills omits the global section header.
	pressRun(m, "/")
	pressRun(m, "esc")
	typeQuery(m, "proj-alpha")
	projOnlySearch := screen(m)
	if !strings.Contains(projOnlySearch, "── Proyecto actual (1) ──") {
		t.Fatalf("expected project section with count 1:\n%s", projOnlySearch)
	}
	if strings.Contains(projOnlySearch, "Globales") {
		t.Fatalf("expected empty global section to be omitted:\n%s", projOnlySearch)
	}

	// Search matching neither shows none-shown message and omits both headers.
	pressRun(m, "/")
	pressRun(m, "esc")
	typeQuery(m, "no-such-skill-xyz")
	noneSearch := screen(m)
	if !strings.Contains(noneSearch, text.TUISkillsNoneShown) {
		t.Fatalf("expected %q:\n%s", text.TUISkillsNoneShown, noneSearch)
	}
	if strings.Contains(noneSearch, "Globales") || strings.Contains(noneSearch, "Proyecto actual") {
		t.Fatalf("expected no section headers when no rows match:\n%s", noneSearch)
	}

	// Clear search and cycle filter to disabled: only global disabled skills match,
	// so project section is omitted.
	pressRun(m, "/")
	pressRun(m, "esc")
	pressRun(m, "f") // FilterActive
	pressRun(m, "f") // FilterDisabled
	disabledScreen := screen(m)
	if !strings.Contains(disabledScreen, "── Globales (4) ──") {
		t.Fatalf("expected global disabled count 4:\n%s", disabledScreen)
	}
	if strings.Contains(disabledScreen, "Proyecto actual") {
		t.Fatalf("expected project section omitted under disabled filter:\n%s", disabledScreen)
	}
}

func TestTUISkillsSectionsScrollingAcrossBoundary(t *testing.T) {
	mc := newMachine(t)
	proj := realTUITempDir(t)
	writeTUIProjectSkill(t, proj, ".agents/skills/proj-scroll-a/SKILL.md", graphtest.SkillMD("proj-scroll-a", "project scroll a"))
	writeTUIProjectSkill(t, proj, ".agents/skills/proj-scroll-b/SKILL.md", graphtest.SkillMD("proj-scroll-b", "project scroll b"))

	a := newProjectTUIApp(t, mc, proj)
	// Small height forces scrolling in the Skills list.
	m := openSized(t, a, 100, 14)

	total := m.RowCountForTest()
	for range total - 1 {
		pressRun(m, "down")
	}
	bottom := screen(m)
	if !strings.Contains(bottom, "── Proyecto actual (2) ──") {
		t.Fatalf("expected project section header visible when scrolled to bottom:\n%s", bottom)
	}
	if !strings.Contains(flat(bottom), flat("Skill del proyecto actual (.agents/skills)")) || !strings.Contains(bottom, "│ proj-scroll-b") {
		t.Fatalf("expected last project skill selected and visible:\n%s", bottom)
	}

	// Scroll back up to the top global skill.
	for range total - 1 {
		pressRun(m, "up")
	}
	top := screen(m)
	if !strings.Contains(top, "accessibility") {
		t.Fatalf("expected top global skill visible after scrolling up:\n%s", top)
	}
}
