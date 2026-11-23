package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

func (m *Model) openSkillDocument(sk domain.Skill, returnMode viewMode) (tea.Model, tea.Cmd) {
	doc, err := m.app.SkillDocument(sk)
	if err != nil {
		m.status = text.SafeLine(err.Error())
		return m, nil
	}
	m.docView = doc
	m.docScroll = 0
	m.docReturnMode = returnMode
	m.mode = modeSkillDocument
	return m, nil
}

func (m *Model) handleDocumentKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "q":
		m.mode = m.docReturnMode
	case "up", "k":
		m.docScroll = max(0, m.docScroll-1)
	case "down", "j":
		m.docScroll++
	case "pgup":
		m.docScroll = max(0, m.docScroll-10)
	case "pgdown":
		m.docScroll += 10
	case "home":
		m.docScroll = 0
	case "end":
		m.docScroll = 1_000_000
	}
	return m, nil
}

func (m *Model) renderSkillDocument(boxW int) string {
	innerW := max(20, boxW-6)
	formatted := strings.TrimRight(text.TUIDocumentFormat(m.docView), "\n")
	wrapped := lipgloss.NewStyle().Width(innerW).Render(formatted)
	lines := strings.Split(wrapped, "\n")

	maxH := max(6, m.height-6)
	if len(lines) <= maxH {
		m.docScroll = 0
		return m.styles.box.Width(boxW).Render(strings.Join(lines, "\n"))
	}

	viewH := max(1, maxH-1)
	maxScroll := len(lines) - viewH
	m.docScroll = min(max(0, m.docScroll), maxScroll)
	end := min(len(lines), m.docScroll+viewH)
	hint := ansi.Truncate(text.TUIDocumentScrollHint(m.docScroll+1, end, len(lines)), innerW, "…")
	visible := append(slices.Clone(lines[m.docScroll:end]), m.styles.muted.Render(hint))
	return m.styles.box.Width(boxW).Render(strings.Join(visible, "\n"))
}
