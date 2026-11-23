package tui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func (m *Model) openCompareAgents(first, second string, single bool) {
	m.compareA = first
	m.compareB = second
	m.compareSingle = single
	m.compareScroll = 0
	m.mode = modeCompareAgents
}

func (m *Model) handleCompareKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc":
		m.mode = modeAgentFilter
	case "up", "k":
		m.compareScroll = max(0, m.compareScroll-1)
	case "down", "j":
		m.compareScroll++
	case "pgup":
		m.compareScroll = max(0, m.compareScroll-10)
	case "pgdown":
		m.compareScroll += 10
	case "home":
		m.compareScroll = 0
	case "end":
		m.compareScroll = 1_000_000
	}
	return m, nil
}

func (m *Model) skillDisplayName(id string) string {
	if sk, ok := m.skillByID(id); ok && sk.Name != "" {
		return sk.Name
	}
	for _, r := range m.allRows {
		if r.ID == id && r.Name != "" {
			return r.Name
		}
	}
	return id
}

func (m *Model) renderCompareAgents(boxW int) string {
	comp := app.CompareAgents(m.sightings, m.compareA, m.compareB)
	innerW := max(20, boxW-6)
	lines := buildCompareLines(
		m.styles,
		comp,
		m.agentDisplayName,
		m.skillDisplayName,
		innerW,
		m.compareSingle,
	)

	maxH := max(6, m.height-6)
	if len(lines) <= maxH {
		m.compareScroll = 0
		return m.styles.box.Width(boxW).Render(strings.Join(lines, "\n"))
	}

	viewH := max(1, maxH-1)
	maxScroll := len(lines) - viewH
	m.compareScroll = min(max(0, m.compareScroll), maxScroll)
	end := min(len(lines), m.compareScroll+viewH)
	visible := append(slices.Clone(lines[m.compareScroll:end]),
		m.styles.muted.Render(clipCompareLine(text.TUICompareScrollHint(m.compareScroll+1, end, len(lines)), innerW)),
	)
	return m.styles.box.Width(boxW).Render(strings.Join(visible, "\n"))
}

// RenderCompareBodyForTest renders the inner comparison lines without model state.
func RenderCompareBodyForTest(
	comp app.AgentComparison,
	agentName func(string) string,
	skillName func(string) string,
	width int,
	single bool,
) string {
	innerW := max(20, width-6)
	lines := buildCompareLines(defaultStyles(), comp, agentName, skillName, innerW, single)
	return strings.Join(lines, "\n")
}

func buildCompareLines(
	st styles,
	comp app.AgentComparison,
	agentName func(string) string,
	skillName func(string) string,
	innerW int,
	single bool,
) []string {
	firstName := text.SafeLine(agentName(comp.First))
	secondName := text.SafeLine(agentName(comp.Second))

	lines := []string{
		st.title.Render(clipCompareLine(text.TUICompareTitle(firstName, secondName), innerW)),
	}

	switch {
	case single:
		lines = append(lines, st.problem.Render(clipCompareLine(text.TUICompareSingleAgent, innerW)))
	case comp.First == comp.Second:
		lines = append(lines, st.muted.Render(clipCompareLine(text.TUICompareSameAgent, innerW)))
	}

	lines = append(lines, "")
	lines = append(lines, formatCompareSection(st, text.TUICompareCommon, comp.Common, skillName, innerW)...)
	lines = append(lines, "")
	lines = append(lines, formatCompareSection(st, text.TUICompareOnly(firstName), comp.OnlyFirst, skillName, innerW)...)
	lines = append(lines, "")
	lines = append(lines, formatCompareSection(st, text.TUICompareOnly(secondName), comp.OnlySecond, skillName, innerW)...)

	if len(comp.Indeterminate) > 0 {
		lines = append(lines, "")
		lines = append(lines, st.title.Render(clipCompareLine(text.TUICompareSection(text.TUICompareIndeterminate, len(comp.Indeterminate)), innerW)))
		type indetItem struct {
			name   string
			agents []string
			reason string
		}
		items := make([]indetItem, 0, len(comp.Indeterminate))
		for _, it := range comp.Indeterminate {
			agents := make([]string, 0, len(it.Agents))
			for _, id := range it.Agents {
				agents = append(agents, text.SafeLine(agentName(id)))
			}
			reason := text.TUISightingReason(app.SightingIndeterminate)
			if len(it.Reasons) > 0 {
				reason = text.TUISightingReason(it.Reasons[0].Kind)
			}
			items = append(items, indetItem{
				name:   text.SafeLine(skillName(it.ID)),
				agents: agents,
				reason: reason,
			})
		}
		slices.SortFunc(items, func(a, b indetItem) int {
			return strings.Compare(a.name, b.name)
		})
		for _, it := range items {
			line := "  • " + text.TUICompareIndeterminateItem(it.name, it.agents, it.reason)
			lines = append(lines, clipCompareLine(line, innerW))
		}
	}

	return lines
}

func formatCompareSection(st styles, label string, ids []string, skillName func(string) string, innerW int) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, text.SafeLine(skillName(id)))
	}
	slices.Sort(names)

	out := []string{
		st.title.Render(clipCompareLine(text.TUICompareSection(label, len(names)), innerW)),
	}
	if len(names) == 0 {
		out = append(out, st.muted.Render("  "+text.TUICompareEmptyList))
		return out
	}
	for _, name := range names {
		out = append(out, clipCompareLine("  • "+name, innerW))
	}
	return out
}

func clipCompareLine(s string, maxW int) string {
	if maxW <= 0 || lipgloss.Width(s) <= maxW {
		return s
	}
	runes := []rune(s)
	if maxW <= 1 {
		return string(runes[:maxW])
	}
	for len(runes) > 0 && lipgloss.Width(string(runes)) > maxW-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func (m *Model) homonymPair(skillID string) (string, string, bool) {
	if skillID == "" {
		return "", "", false
	}
	findings := m.overviewFindings
	if m.v2.findingsLoaded {
		findings = m.v2.findings
	}
	for _, f := range findings {
		if f.Kind != "homonyms" || len(f.Skills) < 2 || !slices.Contains(f.Skills, skillID) {
			continue
		}
		for _, other := range f.Skills {
			if other != skillID {
				return skillID, other, true
			}
		}
	}
	sk, ok := m.skillByID(skillID)
	if !ok || sk.Name == "" {
		return "", "", false
	}
	for _, other := range m.skills {
		if other.ID != skillID && other.Name == sk.Name {
			return skillID, other.ID, true
		}
	}
	return "", "", false
}

func (m *Model) openCompareSkills(firstID, secondID string, returnMode viewMode) (tea.Model, tea.Cmd) {
	comp, err := m.app.CompareSkills(context.Background(), firstID, secondID)
	if err != nil {
		m.status = text.SafeLine(err.Error())
		return m, nil
	}
	m.skillComp = comp
	m.skillCompScroll = 0
	m.skillCompReturnMode = returnMode
	m.mode = modeCompareSkills
	return m, nil
}

func (m *Model) handleCompareSkillsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "q":
		m.mode = m.skillCompReturnMode
	case "up", "k":
		m.skillCompScroll = max(0, m.skillCompScroll-1)
	case "down", "j":
		m.skillCompScroll++
	case "pgup":
		m.skillCompScroll = max(0, m.skillCompScroll-10)
	case "pgdown":
		m.skillCompScroll += 10
	case "home":
		m.skillCompScroll = 0
	case "end":
		m.skillCompScroll = 1_000_000
	}
	return m, nil
}

func (m *Model) renderCompareSkills(boxW int) string {
	innerW := max(20, boxW-6)
	lines := m.buildSkillCompareLines(innerW)

	maxH := max(6, m.height-6)
	if len(lines) <= maxH {
		m.skillCompScroll = 0
		return m.styles.box.Width(boxW).Render(strings.Join(lines, "\n"))
	}

	viewH := max(1, maxH-1)
	maxScroll := len(lines) - viewH
	m.skillCompScroll = min(max(0, m.skillCompScroll), maxScroll)
	end := min(len(lines), m.skillCompScroll+viewH)
	hint := ansi.Truncate(text.TUICompareScrollHint(m.skillCompScroll+1, end, len(lines)), innerW, "…")
	visible := append(slices.Clone(lines[m.skillCompScroll:end]), m.styles.muted.Render(hint))
	return m.styles.box.Width(boxW).Render(strings.Join(visible, "\n"))
}

func (m *Model) buildSkillCompareLines(innerW int) []string {
	wrap := lipgloss.NewStyle().Width(innerW)
	addWrapped := func(lines []string, s string) []string {
		rendered := wrap.Render(s)
		return append(lines, strings.Split(rendered, "\n")...)
	}

	lines := []string{
		m.styles.title.Render(clipCompareLine(text.TUISkillCompareTitle(m.skillComp.Name), innerW)),
	}

	renderSide := func(label string, side app.ComparedSkill) {
		lines = append(lines, "")
		lines = append(lines, m.styles.title.Render(clipCompareLine(text.TUISkillCompareSide(label, side.ID), innerW)))
		agents := ""
		if len(side.Agents) > 0 {
			agents = m.agentNames(side.Agents)
		}
		lines = addWrapped(lines, text.TUISkillCompareAgents(agents))
		if len(side.Paths) == 0 {
			lines = addWrapped(lines, text.TUISkillCompareNoPaths)
			return
		}
		lines = append(lines, text.TUISkillComparePaths)
		for _, p := range side.Paths {
			lines = addWrapped(lines, "    "+text.SafeLine(m.tilde(p)))
		}
	}

	renderSide("a", m.skillComp.First)
	renderSide("b", m.skillComp.Second)

	lines = append(lines, "")
	lines = append(lines, m.styles.title.Render(clipCompareLine(text.TUISkillCompareDiffHeader, innerW)))
	diffBody := strings.TrimRight(text.TUISkillCompareDiffs(m.skillComp.Diffs), "\n")
	for _, rawLine := range strings.Split(diffBody, "\n") {
		lines = addWrapped(lines, rawLine)
	}
	return lines
}
