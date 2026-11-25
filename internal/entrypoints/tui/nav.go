package tui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// The navigation of UX3 (S13, L85): four tabs with counters that tab goes
// through, Historial (h) and Ajustes (c) opened from any tab and closed
// with esc, z that undoes the last operation of the session, and the
// filters of the Skills list by state (f) and by agent (g).

// tabs are the four tabs, in the order of their numbers and of tab.
var tabs = []Screen{ScreenSkills, ScreenUpdates, ScreenInstall, ScreenDoctor}

// lastOp is the operation z undoes: a version 2 operation by its snapshot,
// or a version 1 update. With neither, why tells z what to say.
type lastOp struct {
	snapshot string
	update   updates.UndoRequest
	why      string
}

// handleNavKey handles the keys every tab shares.
func (m *Model) handleNavKey(k string) tea.Cmd {
	switch k {
	case "1", "2", "3", "4":
		target := tabs[k[0]-'1']
		if target == ScreenDoctor {
			if !m.loaded {
				m.screen = ScreenDoctor
				return nil
			}
			return m.openDoctor()
		}
		m.screen = target
	case "tab":
		i := slices.Index(tabs, m.screen)
		next := tabs[(i+1)%len(tabs)]
		if i < 0 {
			next = ScreenSkills
		}
		if next == ScreenDoctor && m.loaded {
			return m.openDoctor()
		}
		m.screen = next
	case "h", "c":
		if slices.Contains(tabs, m.screen) {
			m.back = m.screen
		}
		m.screen = ScreenHistory
		if k == "c" {
			m.screen = ScreenSettings
		}
	case "z":
		return m.undoLast()
	}
	return m.loadCatalogOnEntry()
}

// undoLast opens the undo of the last operation of the session, with its
// summary and confirmation, as u does in Historial; or tells why there is
// none.
func (m *Model) undoLast() tea.Cmd {
	if m.busy || !m.loaded {
		return nil
	}
	switch l := m.last; {
	case l.snapshot != "":
		return m.startPlan(app.Request{Action: app.ActionUndo, Snapshot: l.snapshot})
	case l.update.Target != "":
		m.pendingUndoReq = l.update
		m.pendingUndoSaved = ""
		m.details = false
		m.mode = modeConfirmUndo
	case l.why != "":
		m.status = l.why
	default:
		m.status = text.TUIZNothing
	}
	return nil
}

// renderHeader names the four tabs with their counters and, when the
// terminal has room, the shortcuts; an open shortcut is named after them.
func (m *Model) renderHeader() string {
	var rendered []string
	for _, t := range tabs {
		label := text.TUITab(m.tabLabel(t))
		if t == m.screen {
			rendered = append(rendered, m.styles.tabActive.Render(label))
		} else {
			rendered = append(rendered, m.styles.tabInactive.Render(label))
		}
	}
	switch m.screen {
	case ScreenHistory:
		rendered = append(rendered, m.styles.tabActive.Render(text.TUITitleHistory))
	case ScreenSettings:
		rendered = append(rendered, m.styles.tabActive.Render(text.TUITitleSettings))
	}
	header := m.styles.header.Render(appTitle) + "  " + lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
	if more := header + "  " + m.styles.muted.Render(text.TUIHeaderShortcuts); lipgloss.Width(more) <= max(20, m.width) {
		return more
	}
	return header
}

// tabLabel is a tab's label and its counter, -1 for none: Skills counts
// its skills, Actualizar the pending updates and Salud what it can repair.
// A counter of what needs attention is shown only when it is not zero.
func (m *Model) tabLabel(t Screen) (string, int) {
	switch t {
	case ScreenSkills:
		if !m.loaded {
			return text.TUITabSkills, -1
		}
		return text.TUITabSkills, len(m.allRows)
	case ScreenUpdates:
		n := 0
		for _, c := range m.candidates {
			if c.State == domain.CandidatePending {
				n++
			}
		}
		return text.TUITabUpdates, nonZero(n)
	case ScreenInstall:
		return text.TUITabInstall, -1
	}
	return text.TUITabHealth, nonZero(m.health)
}

func nonZero(n int) int {
	if n <= 0 {
		return -1
	}
	return n
}

// The filters of the Skills list.

// disabledRow reports a disabled skill's row.
func (m *Model) disabledRow(r SkillRow) bool {
	if r.ParkedKey != "" {
		return true
	}
	s, _ := m.skillByID(r.ID)
	return s.State == domain.StateDisabled
}

// problemRow reports a row with something to fix: a warning state, a
// broken link, or a skill in OFF/ that agents still see.
func (m *Model) problemRow(r SkillRow) bool {
	if r.Kind == RowWarn {
		return true
	}
	acts := m.actions[r.ID]
	return len(acts.BrokenLinks) > 0 || acts.Off
}

// updateRow reports a skill with an update available.
func (m *Model) updateRow(r SkillRow) bool {
	s, ok := m.skillByID(r.ID)
	return ok && s.State == domain.StateUpdateAvailable
}

// keeps reports whether the state filter f keeps r.
func (m *Model) keeps(f text.SkillFilter, r SkillRow) bool {
	// Only the «ignoradas» filter shows the ignored skills, and only them.
	if r.Ignored != (f == text.FilterIgnored) {
		return false
	}
	switch f {
	case text.FilterActive:
		return !m.disabledRow(r)
	case text.FilterDisabled:
		return m.disabledRow(r)
	case text.FilterProblems:
		return m.problemRow(r)
	case text.FilterUpdates:
		return m.updateRow(r)
	}
	return true
}

// filterRows keeps the rows of the state filter and of the agent filter.
func (m *Model) filterRows(rows []SkillRow) []SkillRow {
	out := rows[:0:0]
	for _, r := range rows {
		if !m.keeps(m.stateFilter, r) {
			continue
		}
		if m.agentFilter != "" && !slices.Contains(m.seenBy(r), m.agentFilter) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// seenBy are the agents that see the row's skill: none for a disabled one.
func (m *Model) seenBy(r SkillRow) []string {
	if r.ParkedKey != "" || r.Project != nil {
		return nil
	}
	if ids, ok := m.visibility[r.ID]; ok || m.visibility != nil {
		return ids
	}
	s, _ := m.skillByID(r.ID)
	var ids []string
	for _, l := range s.Locations {
		if l.AgentID != domain.StoreAgentID {
			ids = append(ids, l.AgentID)
		}
	}
	return ids
}

// counts are the numbers of the summary line, over every row.
func (m *Model) counts() text.SkillCounts {
	c := text.SkillCounts{Total: len(m.allRows), Ignored: len(m.ignored)}
	for _, r := range m.allRows {
		if m.disabledRow(r) {
			c.Disabled++
		} else {
			c.Active++
		}
		if m.updateRow(r) {
			c.Updates++
		}
		if m.problemRow(r) {
			c.Problems++
		}
	}
	return c
}

// nextFilter moves f to the next state filter.
func (m *Model) nextFilter() {
	m.stateFilter = m.stateFilter.Next()
	m.refreshTables()
	m.skillsTable.SetCursor(0)
}

// agentDisplayName returns the human-readable name for a harness ID.
func (m *Model) agentDisplayName(id string) string {
	if m.app != nil {
		return m.app.AgentName(id)
	}
	return id
}

// agentSkillCounts returns, by harness ID, how many rows in m.allRows each
// agent sees.
func (m *Model) agentSkillCounts() map[string]int {
	counts := map[string]int{}
	for _, r := range m.allRows {
		var rowSeen []string
		for _, id := range m.seenBy(r) {
			if slices.Contains(rowSeen, id) {
				continue
			}
			rowSeen = append(rowSeen, id)
			counts[id]++
		}
	}
	return counts
}

// filterAgentsData returns the selectable agent IDs ("" for all, followed by
// detected agents sorted by display name) and the count of known agents that
// were not detected on this machine.
func (m *Model) filterAgentsData() (ids []string, undetected int) {
	var seen []string
	for _, r := range m.allRows {
		for _, id := range m.seenBy(r) {
			if !slices.Contains(seen, id) {
				seen = append(seen, id)
			}
		}
	}
	if m.app != nil {
		if presence, err := m.app.DetectedAgents(context.Background()); err == nil {
			for _, id := range presence.Detected {
				if !slices.Contains(seen, id) {
					seen = append(seen, id)
				}
			}
			undetected = len(presence.Undetected)
		}
	}
	slices.SortFunc(seen, func(a, b string) int {
		return strings.Compare(m.agentDisplayName(a), m.agentDisplayName(b))
	})
	return append([]string{""}, seen...), undetected
}

// filterAgents are the agents the filter offers: every detected agent (and
// any agent that sees a skill), by readable name. The first choice, "", is
// every agent.
func (m *Model) filterAgents() []string {
	ids, _ := m.filterAgentsData()
	return ids
}

func (m *Model) clampAgentCursor(n int) {
	if n <= 0 {
		m.agentCursor = 0
		return
	}
	m.agentCursor = min(max(0, m.agentCursor), n-1)
}

// openAgentFilter shows the list of agents, with the cursor on the one
// shown now.
func (m *Model) openAgentFilter() {
	m.compareFirst = ""
	m.agentCursor = max(0, slices.Index(m.filterAgents(), m.agentFilter))
	m.mode = modeAgentFilter
}

func (m *Model) handleAgentFilterKey(k string) (tea.Model, tea.Cmd) {
	agents := m.filterAgents()
	m.clampAgentCursor(len(agents))
	switch k {
	case "up", "k":
		m.agentCursor = max(0, m.agentCursor-1)
	case "down", "j":
		m.agentCursor = min(len(agents)-1, m.agentCursor+1)
	case "esc":
		m.compareFirst = ""
		m.mode = modeNormal
	case "m":
		if len(agents) == 0 {
			return m, nil
		}
		chosen := agents[m.agentCursor]
		if chosen == "" {
			return m, nil
		}
		detectedCount := len(agents) - 1
		if detectedCount <= 1 {
			m.compareFirst = ""
			m.openCompareAgents(chosen, chosen, true)
			return m, nil
		}
		if m.compareFirst == "" {
			m.compareFirst = chosen
			return m, nil
		}
		first := m.compareFirst
		m.compareFirst = ""
		m.openCompareAgents(first, chosen, false)
	case "enter":
		if len(agents) == 0 {
			m.mode = modeNormal
			return m, nil
		}
		m.compareFirst = ""
		m.agentFilter = agents[m.agentCursor]
		m.mode = modeNormal
		m.refreshTables()
		m.skillsTable.SetCursor(0)
	}
	return m, nil
}

func (m *Model) renderAgentFilter(boxW int) string {
	agents, undetected := m.filterAgentsData()
	cursor := m.agentCursor
	if len(agents) == 0 {
		cursor = 0
	} else {
		cursor = min(max(0, cursor), len(agents)-1)
	}
	counts := m.agentSkillCounts()
	items := make([]string, 0, len(agents))
	for _, id := range agents {
		if id == "" {
			items = append(items, text.TUIAgentFilterAll)
			continue
		}
		items = append(items, text.TUIAgentFilterItem(m.agentDisplayName(id), counts[id]))
	}
	body := m.styles.title.Render(text.TUIAgentFilterQuestion) + "\n" + text.TUIAgentFilterHint
	if m.compareFirst != "" {
		body += "\n" + m.styles.selected.Render(text.TUICompareFirstMarked(m.agentDisplayName(m.compareFirst)))
	}
	body += "\n\n" + strings.TrimRight(PresentChoices(items, cursor), "\n")
	if note := text.TUIAgentFilterUndetected(undetected); note != "" {
		body += "\n\n" + m.styles.muted.Render(note)
	}
	return m.styles.box.Width(boxW).Render(body)
}
