package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

// The Skills screen of S13: the list on the left, with an icon and a short
// state per row, and the panel of the selected skill on the right. Under
// wideLayout columns the panel goes under the list.

// RowKind is the icon of a row of the Skills list.
type RowKind int

const (
	// RowOK is an active skill with nothing to tell.
	RowOK RowKind = iota
	// RowUpdate is a skill with an update available.
	RowUpdate
	// RowOff is a disabled skill.
	RowOff
	// RowWarn is a skill with problems, unmanaged or locally modified.
	RowWarn
	// RowIgnored is a skill the ignore setting hides (UX4).
	RowIgnored
)

// Icon is the row's icon.
func (k RowKind) Icon() string {
	switch k {
	case RowUpdate:
		return "↑"
	case RowOff:
		return "○"
	case RowWarn:
		return "⚠"
	case RowIgnored:
		return "-"
	default:
		return "●"
	}
}

// wideLayout is the width from which the panel goes beside the list.
const wideLayout = 100

// rowState picks a skill's icon and short state: problems first, then an
// update, then a disabled skill, then an active one.
func rowState(s domain.Skill) (RowKind, string) {
	switch {
	case s.State == domain.StateNoOrigin:
		return RowWarn, text.TUIRowUnmanaged
	case len(s.Issues) > 0:
		return RowWarn, text.TUIRowIssue(s.Issues[0])
	case s.State == domain.StateCheckFailed || s.State == domain.StateMissing:
		return RowWarn, text.State(s.State)
	case s.State == domain.StateUpdateAvailable:
		return RowUpdate, text.TUIRowUpdate
	case s.State == domain.StateDisabled:
		return RowOff, text.TUIRowDisabled
	case s.State == domain.StateUnchecked:
		return RowOK, text.TUIRowUnchecked
	default:
		return RowOK, text.TUIRowUpToDate
	}
}

// selectedRow is the row under the cursor.
func (m *Model) selectedRow() (SkillRow, bool) {
	idx := m.skillsTable.Cursor()
	if idx < 0 || idx >= len(m.skillRows) {
		return SkillRow{}, false
	}
	return m.skillRows[idx], true
}

// renderSkills lays out the list and the panel in the room the header and
// the footer leave.
func (m *Model) renderSkills() string {
	w := max(20, m.width)
	c := m.counts()
	summary := ansi.Truncate(text.TUISkillsSummary(c), w, "…")
	if c.Ignored > 0 {
		// The ignored count ends the line: it wraps rather than be cut (UX4).
		summary = lipgloss.NewStyle().Width(w).Render(text.TUISkillsSummary(c))
	}
	visSummary := ansi.Truncate(text.TUISkillsVisibilitySummary(m.visibilityCounts()), w, "…")
	prefix := m.styles.muted.Render(summary) + "\n" +
		m.styles.muted.Render(visSummary) + "\n"
	if m.mode == modeFilter {
		prefix += m.filterInput.View() + "\n"
	} else if q := m.filterInput.Value(); q != "" {
		prefix += fmt.Sprintf("Búsqueda: %s\n", text.SafeLine(q))
	}
	if len(m.skillRows) == 0 {
		if len(m.allRows) > 0 {
			return prefix + m.listTitle() + "\n" + text.TUISkillsNoneShown
		}
		if len(m.ignored) > 0 {
			return prefix + msgAllSkillsIgnored
		}
		return prefix + msgNoSkills
	}
	var globalRows, projectRows []SkillRow
	for _, r := range m.skillRows {
		if r.Project != nil {
			projectRows = append(projectRows, r)
		} else {
			globalRows = append(globalRows, r)
		}
	}
	items := make([]listItem, 0, len(m.skillRows)+2)
	rowToItem := func(r SkillRow) listItem {
		return listItem{
			icon:  r.Kind.Icon(),
			style: m.styles.icons[r.Kind],
			name:  text.SafeLine(r.RawName),
			short: m.rowShort(r),
		}
	}
	if len(globalRows) > 0 {
		items = append(items, listItem{
			name:   fmt.Sprintf("── Globales (%d) ──", len(globalRows)),
			header: true,
		})
		for _, r := range globalRows {
			items = append(items, rowToItem(r))
		}
	}
	if len(projectRows) > 0 {
		items = append(items, listItem{
			name:   fmt.Sprintf("── Proyecto actual (%d) ──", len(projectRows)),
			header: true,
		})
		for _, r := range projectRows {
			items = append(items, rowToItem(r))
		}
	}
	tableCursor := m.skillsTable.Cursor()
	displayCursor := tableCursor
	if tableCursor >= 0 && tableCursor < len(m.skillRows) {
		if tableCursor < len(globalRows) {
			displayCursor = tableCursor + 1
		} else if len(globalRows) > 0 {
			displayCursor = tableCursor + 2
		} else {
			displayCursor = tableCursor + 1
		}
	}
	list := func(width, height int) []string {
		return m.listLines(m.listTitle(), items, displayCursor, &m.listTop, width, height)
	}
	return m.split(prefix, max(20, m.width) >= wideLayout, list, m.panelLines)
}

// listTitle heads the Skills list with the filters shown and its rows.
func (m *Model) listTitle() string {
	agent := ""
	if m.agentFilter != "" {
		agent = text.SafeLine(m.app.AgentName(m.agentFilter))
	}
	return text.TUISkillsListTitle(agent, m.stateFilter, len(m.skillRows))
}

// split lays out a list and its panel in the room the header and the
// footer leave: beside each other, or the panel under the list.
func (m *Model) split(prefix string, beside bool, list func(width, height int) []string, panel func(width int) []string) string {
	w := max(20, m.width)
	room := m.height - 1 - lipgloss.Height(m.renderFooter()) - strings.Count(prefix, "\n")
	room = max(8, room)
	if beside {
		lw := max(30, w*2/5)
		rw := w - lw
		return prefix + lipgloss.JoinHorizontal(lipgloss.Top,
			m.frame(lw, room, list(lw-4, room-2)),
			m.frame(rw, room, panel(rw-4)))
	}
	p := panel(w - 4)
	ph := min(len(p)+2, room/2)
	lh := room - ph
	return prefix + lipgloss.JoinVertical(lipgloss.Left,
		m.frame(w, lh, list(w-4, lh-2)),
		m.frame(w, ph, p))
}

// frame puts lines in a box of exactly width columns and height rows: the
// lines it cannot hold are cut, and the missing ones are blank.
func (m *Model) frame(width, height int, lines []string) string {
	inner := max(1, height-2)
	if len(lines) > inner {
		lines = lines[:inner]
	}
	for len(lines) < inner {
		lines = append(lines, "")
	}
	return m.styles.frame.Width(width).Render(strings.Join(lines, "\n"))
}

// listItem is a row of a list: an icon with its color, a name and a short
// state. A header names a group of rows and is never selected.
type listItem struct {
	icon   string
	style  lipgloss.Style
	name   string
	short  string
	header bool
}

// listLines are the title and the visible items, scrolled by *top so that
// the item at cursor is always shown, and highlighted.
func (m *Model) listLines(title string, items []listItem, cursor int, top *int, width, height int) []string {
	rows := max(1, height-1)
	if cursor < *top {
		*top = cursor
	}
	if cursor >= *top+rows {
		*top = cursor - rows + 1
	}
	*top = max(0, min(*top, len(items)-rows))

	shortW := 0
	for _, it := range items {
		shortW = max(shortW, ansi.StringWidth(it.short))
	}
	shortW = min(shortW, max(6, width*2/5))

	lines := []string{m.styles.title.Render(title)}
	for i := *top; i < len(items) && i < *top+rows; i++ {
		it := items[i]
		iconW := 0
		if it.icon != "" {
			iconW = ansi.StringWidth(it.icon) + 1
		}
		rest := ansi.Truncate(it.name, max(4, width-iconW), "…")
		if shortW > 0 && (!it.header || it.short != "") {
			name := ansi.Truncate(it.name, max(4, width-iconW-shortW-1), "…")
			short := ansi.Truncate(it.short, shortW, "…")
			pad := max(1, width-iconW-ansi.StringWidth(name)-ansi.StringWidth(short))
			rest = name + strings.Repeat(" ", pad) + short
		}
		switch {
		case it.header:
			lines = append(lines, m.styles.title.Render(rest))
		case i == cursor && it.icon != "":
			lines = append(lines, m.styles.selected.Render(it.icon+" "+rest))
		case i == cursor:
			lines = append(lines, m.styles.selected.Render(rest))
		case it.icon != "":
			lines = append(lines, it.style.Render(it.icon)+" "+rest)
		default:
			lines = append(lines, rest)
		}
	}
	return lines
}

// panelLines are the selected skill's panel, wrapped to width.
func (m *Model) panelLines(width int) []string {
	row, ok := m.selectedRow()
	if !ok {
		return nil
	}
	wrap := func(s string) []string {
		return strings.Split(lipgloss.NewStyle().Width(width).Render(s), "\n")
	}
	lines := []string{m.styles.title.Render(ansi.Truncate(text.SafeLine(row.RawName), width, "…"))}
	if match := text.TUISearchMatch(row.MatchFields); match != "" {
		for _, l := range wrap(match) {
			lines = append(lines, m.styles.muted.Render(l))
		}
	}
	var actions []string
	switch {
	case row.Ignored:
		s, _ := m.ignoredByID(row.ID)
		lines = append(lines, wrap(text.TUIPanelIgnored)...)
		lines = append(lines, "")
		lines = append(lines, m.skillLines(s, text.TUIStateIgnored, wrap)...)
		lines = append(lines, m.activityLines(s, wrap)...)
		if m.actions[row.ID].Remove {
			actions = append(actions, text.TUIActionRemove)
		}
		actions = append(actions, text.TUIActionUnignore, text.TUIActionDetail)
	case row.ParkedKey != "":
		p, _ := m.parkedByKey(row.ParkedKey)
		lines = append(lines, wrap(text.TUIPanelDisabledSince(FormatTime(p.Since)))...)
		if len(p.Harnesses) > 0 {
			lines = append(lines, wrap(text.TUIPanelReturnsTo(m.agentNames(p.Harnesses)))...)
		}
		lines = append(lines, "")
		lines = append(lines, wrap(text.TUIPanelSkillhubState(text.TUIStateDisabled))...)
		lines = append(lines, wrap(text.TUIPanelAvailable(text.TUIPanelNowhere))...)
		lines = append(lines, wrap(text.TUIPanelOrigin(text.TUIPanelDisabledBy))...)
		parkedSkill := domain.Skill{
			ID:        domain.LockID(p.Key),
			Name:      p.Key,
			Locations: []domain.Location{{AgentID: domain.StoreAgentID, Path: p.Key}},
		}
		lines = append(lines, m.activityLines(parkedSkill, wrap)...)
		actions = []string{text.TUIActionEnable, text.TUIActionRemove}
	case row.Project != nil:
		proj := *row.Project
		lines = append(lines, wrap("Skill del proyecto actual ("+text.SafeLine(proj.Root)+")")...)
		if proj.Removable {
			lines = append(lines, wrap(text.TUIGuideRecommended+m.actionLabel(text.TUIActionRemove))...)
		} else {
			for _, l := range wrap(projectNonRemovableReason(proj)) {
				lines = append(lines, m.styles.problem.Render(l))
			}
		}
		lines = append(lines, "")
		lines = append(lines, wrap("Proyecto: "+text.SafeLine(proj.ProjectDir))...)
		lines = append(lines, wrap("Carpeta: "+text.SafeLine(proj.Path))...)
		if proj.TreeSHA != "" {
			lines = append(lines, wrap("Árbol: "+ShortSHA(proj.TreeSHA))...)
		}
		if len(proj.Links) > 0 {
			safeLinks := make([]string, len(proj.Links))
			for i, l := range proj.Links {
				safeLinks[i] = text.SafeLine(l)
			}
			lines = append(lines, wrap("Enlaces en este proyecto: "+strings.Join(safeLinks, ", "))...)
		}
		for _, a := range m.rowActions(row) {
			if !proj.Removable || a.key != "e" {
				actions = append(actions, a.label)
			}
		}
	default:
		s, _ := m.skillByID(row.ID)
		acts := m.actions[row.ID]
		situation, rec := m.guide(row, s, acts)
		if situation != "" {
			lines = append(lines, wrap(situation)...)
		}
		if rec.key != "" {
			lines = append(lines, wrap(text.TUIGuideRecommended+m.actionLabel(rec.label))...)
		}
		if situation != "" || rec.key != "" {
			lines = append(lines, "")
		}
		mgmtState := text.TUIStateActive
		if acts.Off || s.State == domain.StateDisabled {
			mgmtState = text.TUIStateDisabledHand
		}
		lines = append(lines, m.skillLines(s, mgmtState, wrap)...)
		for _, p := range text.TUIPlainProblems(s.State, s.Issues) {
			for _, l := range wrap(p) {
				lines = append(lines, m.styles.problem.Render(l))
			}
		}
		if acts.Off {
			lines = append(lines, wrap(text.TUIPanelAdoptOff(acts.OffKeys, acts.OffKey))...)
			if fixedByAdoptOff(acts) {
				lines = append(lines, wrap(text.TUIPanelAdoptOffLinks(len(acts.BrokenLinks)))...)
			}
		}
		lines = append(lines, m.activityLines(s, wrap)...)
		for _, a := range m.rowActions(row) {
			if a.key != rec.key {
				actions = append(actions, a.label)
			}
		}
		// x ignores any skill; it goes before [enter], the last action.
		actions = slices.Insert(actions, max(0, len(actions)-1), text.TUIActionIgnore)
	}
	lines = append(lines, "")
	return append(lines, m.actionLines(actions, width)...)
}

// skillLines are what the panel tells of every skill: its description,
// its management state in skillhub, the agents that see it and where it comes from.
func (m *Model) skillLines(s domain.Skill, mgmtState string, wrap func(string) []string) []string {
	desc := text.Safe(s.Description)
	if desc == "" {
		desc = text.TUIPanelNoDescription
	}
	lines := wrap(desc)
	lines = append(lines, "")
	lines = append(lines, wrap(text.TUIPanelSkillhubState(mgmtState))...)
	lines = append(lines, wrap(text.TUIPanelAvailable(m.availableIn(s)))...)
	lines = append(lines, m.whyVisibleLines(s.ID, wrap)...)
	return append(lines, wrap(text.TUIPanelOrigin(origin(s)))...)
}

// activityLines formats up to panelActivityLimit recent skillhub activity
// entries for the Skills panel, or a single line when there is none.
const panelActivityLimit = 3

func (m *Model) activityLines(s domain.Skill, wrap func(string) []string) []string {
	entries := app.SkillActivity(m.history, m.operations, s)
	if len(entries) == 0 {
		return wrap(text.TUIActivityNone)
	}
	now := m.app.Now()
	limit := min(len(entries), panelActivityLimit)
	lines := wrap(text.TUIActivityHeader)
	for _, e := range entries[:limit] {
		lines = append(lines, wrap(text.TUIActivityLine(e, now))...)
	}
	return lines
}

var sightingOrder = []string{
	app.SightingDirect,
	app.SightingViaLink,
	app.SightingOwnCopy,
	app.SightingIndeterminate,
}

// whyVisibleLines formats one line per access reason, grouping agents that
// share the same reason, without paths.
func (m *Model) whyVisibleLines(skillID string, wrap func(string) []string) []string {
	sgs := m.sightings[skillID]
	if len(sgs) == 0 {
		return nil
	}
	byKind := make(map[string][]string, len(sightingOrder))
	for _, sg := range sgs {
		name := m.app.AgentName(sg.Harness)
		if !slices.Contains(byKind[sg.Kind], name) {
			byKind[sg.Kind] = append(byKind[sg.Kind], name)
		}
	}
	var lines []string
	for _, kind := range sightingOrder {
		names := byKind[kind]
		if len(names) == 0 {
			continue
		}
		slices.Sort(names)
		line := text.TUIPanelWhyVisible(text.JoinNames(names), text.TUISightingReason(kind))
		lines = append(lines, wrap(line)...)
	}
	return lines
}

// visibilityCounts counts how many skills are seen by agents vs hidden from all agents.
func (m *Model) visibilityCounts() text.SkillVisibilityCounts {
	var c text.SkillVisibilityCounts
	for _, r := range m.allRows {
		if r.Project != nil {
			continue
		}
		seen := len(m.seenBy(r)) > 0
		disabled := m.disabledRow(r)
		if seen {
			c.Visible++
			if disabled {
				c.DisabledVisible++
			}
		} else {
			c.Hidden++
			if !disabled {
				c.ActiveHidden++
			}
		}
	}
	return c
}

// actionLabel is an action "[k] Label" with its key highlighted.
func (m *Model) actionLabel(a string) string {
	key, label, _ := strings.Cut(a, " ")
	return m.styles.keys.Render(key) + " " + label
}

// actionLines puts the actions two per line when they fit, one per line
// otherwise, each with its key highlighted.
func (m *Model) actionLines(actions []string, width int) []string {
	var out []string
	render := m.actionLabel
	for i := 0; i < len(actions); i++ {
		if i+1 < len(actions) && ansi.StringWidth(actions[i])+3+ansi.StringWidth(actions[i+1]) <= width {
			out = append(out, render(actions[i])+"   "+render(actions[i+1]))
			i++
			continue
		}
		out = append(out, render(actions[i]))
	}
	return out
}

// availableIn names the agents that see s: the graph's when the app could
// build it, else the ones whose directories hold it.
func (m *Model) availableIn(s domain.Skill) string {
	ids, ok := m.visibility[s.ID]
	if !ok {
		for _, l := range s.Locations {
			if l.AgentID != domain.StoreAgentID && !slices.Contains(ids, l.AgentID) {
				ids = append(ids, l.AgentID)
			}
		}
	}
	if len(ids) == 0 {
		return text.TUIPanelNowhere
	}
	return m.agentNames(ids)
}

// agentNames joins the readable names of the harness ids.
func (m *Model) agentNames(ids []string) string {
	return text.JoinNames(m.agentList(ids))
}

// agentList are the readable names of the harness ids.
func (m *Model) agentList(ids []string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, text.SafeLine(m.app.AgentName(id)))
	}
	return names
}

// origin tells where s comes from.
func origin(s domain.Skill) string {
	switch {
	case s.Origin.Lock != nil && s.Origin.Lock.Source != "":
		return s.Origin.Lock.Source
	case s.Origin.GitClone != nil:
		return s.Origin.GitClone.Remote
	case s.Origin.Kind == domain.OriginNone:
		return text.TUIPanelNoOrigin
	}
	return text.OriginKind(s.Origin.Kind)
}

func (m *Model) skillByID(id string) (domain.Skill, bool) {
	for _, s := range m.skills {
		if s.ID == id {
			return s, true
		}
	}
	return domain.Skill{}, false
}

// ignoredByID is the ignored skill whose inventory ID is id.
func (m *Model) ignoredByID(id string) (domain.Skill, bool) {
	for _, s := range m.ignored {
		if s.Skill.ID == id {
			return s.Skill, true
		}
	}
	return domain.Skill{}, false
}

func (m *Model) parkedByKey(key string) (app.ParkedSkill, bool) {
	for _, p := range m.parked {
		if p.Key == key {
			return p, true
		}
	}
	return app.ParkedSkill{}, false
}

// skillsKeys are the keys of the Skills screen and of the selected row,
// for the footer.
func (m *Model) skillsKeys() string {
	var keys []string
	if row, ok := m.selectedRow(); ok {
		for _, a := range m.rowActions(row) {
			keys = append(keys, a.footer)
		}
		switch {
		case row.Ignored:
			keys = append(keys, text.TUIKeyUnignore)
		case row.ParkedKey == "" && row.Project == nil:
			keys = append(keys, text.TUIKeyIgnore)
		}
	}
	keys = append(keys, text.TUIKeyFind, text.TUIKeyStates, text.TUIKeyByAgent, text.TUIKeyHelp, text.TUIKeyQuit)
	return strings.Join(keys, " • ")
}

// panelAction is an action the selected row's panel offers: its key, its
// label in the panel and its words in the footer.
type panelAction struct {
	key, label, footer string
}

// rowActions are the actions the panel of row offers: the ones the app can
// plan for it now (L83), never one guessed from its state.
func (m *Model) rowActions(row SkillRow) []panelAction {
	if row.ParkedKey != "" {
		return []panelAction{
			{"a", text.TUIActionEnable, text.TUIKeyEnable},
			{"e", text.TUIActionRemove, text.TUIKeyRemove},
		}
	}
	if row.Project != nil {
		var out []panelAction
		if row.Project.Removable {
			out = append(out, panelAction{"e", text.TUIActionRemove, text.TUIKeyRemove})
		}
		return append(out, panelAction{"enter", text.TUIActionDetail, text.TUIKeyDetail})
	}
	if row.Ignored {
		// An ignored skill keeps its ignore preference; e still removes it
		// when the planner accepts its exact target or group ID.
		var out []panelAction
		if m.actions[row.ID].RemoveGroup {
			out = append(out, panelAction{"e", text.TUIActionRemoveComplete, text.TUIKeyRemoveComplete})
		} else if m.actions[row.ID].Remove {
			out = append(out, panelAction{"e", text.TUIActionRemove, text.TUIKeyRemove})
		}
		return append(out, panelAction{"enter", text.TUIActionDetail, text.TUIKeyDetail})
	}
	acts := m.actions[row.ID]
	var out []panelAction
	if acts.Disable {
		out = append(out, panelAction{"d", text.TUIActionDisable, text.TUIKeyDisable})
	}
	if acts.Off {
		out = append(out, panelAction{"o", text.TUIActionAdoptOff, text.TUIKeyAdoptOff})
	}
	if acts.RemoveGroup {
		out = append(out, panelAction{"e", text.TUIActionRemoveComplete, text.TUIKeyRemoveComplete})
	} else if acts.Remove {
		out = append(out, panelAction{"e", text.TUIActionRemove, text.TUIKeyRemove})
	}
	if acts.Key != "" && (len(acts.Unlink) > 0 || acts.LinkIn != "") {
		out = append(out, panelAction{"l", text.TUIActionAgents, text.TUIKeyAgents})
	}
	switch _, one := rowFix(acts); {
	case one:
		out = append(out, panelAction{"p", text.TUIActionFixLink, text.TUIKeyFixLink})
	case len(acts.BrokenLinks) > 0 && !fixedByAdoptOff(acts):
		out = append(out, panelAction{"4", text.TUIActionDoctor, text.TUIKeyDoctor})
	}
	return append(out, panelAction{"enter", text.TUIActionDetail, text.TUIKeyDetail})
}

// guide is how the panel of a skill starts (UX3): its situation and the
// recommended step, one of the actions the row offers or a tab to go to.
// The other actions follow under it.
func (m *Model) guide(row SkillRow, s domain.Skill, acts app.SkillActions) (string, panelAction) {
	offered := func(key string) panelAction {
		for _, a := range m.rowActions(row) {
			if a.key == key {
				return a
			}
		}
		return panelAction{}
	}
	fix, one := rowFix(acts)
	switch {
	case acts.Off:
		return text.TUIPanelOffHand(m.agentList(m.visibility[s.ID])), offered("o")
	case one:
		return text.TUIGuideBrokenLinks(1, m.agentList(fix.Harnesses)), offered("p")
	case len(acts.BrokenLinks) > 0 && !fixedByAdoptOff(acts):
		return text.TUIPanelLinksInDoctor(len(acts.BrokenLinks)), offered("4")
	case s.State == domain.StateUpdateAvailable:
		return text.TUIGuideUpdate, panelAction{"2", text.TUIActionSeeUpdate, text.TUIKeySeeUpdate}
	case acts.Provider != "":
		return text.TUIGuideProvider(acts.Provider), panelAction{}
	case m.isManual(s):
		if rec := offered("e"); rec.key != "" {
			return text.TUIGuideManual, rec
		}
		return text.TUIGuideManual, panelAction{"4", text.TUIActionSeeHealth, text.TUIKeySeeHealth}
	case m.actions != nil && acts.Key == "" && s.State == domain.StateNoOrigin:
		return text.TUIGuideUnmgd, panelAction{"4", text.TUIActionSeeHealth, text.TUIKeySeeHealth}
	case m.actions != nil && acts.Key == "":
		return text.TUIPanelUnmanaged, panelAction{}
	case row.Kind == RowWarn:
		return text.TUIGuideProbs, panelAction{}
	}
	return "", panelAction{}
}

// rowFix is the repair [p] plans: the one of the skill's only broken link,
// when Doctor offers one other than adopting OFF/, which [o] already is.
func rowFix(acts app.SkillActions) (app.LinkFix, bool) {
	if len(acts.BrokenLinks) != 1 {
		return app.LinkFix{}, false
	}
	l := acts.BrokenLinks[0]
	return l, l.Repair != "" && l.Repair != repairAdoptOff
}

// repairAdoptOff is Doctor's repair of a broken link towards a skill in OFF/.
const repairAdoptOff = "adopt_off"

// fixedByAdoptOff reports whether adopting OFF/ ([o]) removes every broken
// link of the skill.
func fixedByAdoptOff(acts app.SkillActions) bool {
	if !acts.Off || len(acts.BrokenLinks) == 0 {
		return false
	}
	for _, l := range acts.BrokenLinks {
		if l.Repair != repairAdoptOff {
			return false
		}
	}
	return true
}

// isManual reports whether s is a confirmed independent manual installation.
func (m *Model) isManual(s domain.Skill) bool {
	return m.actions != nil && m.actions[s.ID].Manual
}

func (m *Model) rowShort(r SkillRow) string {
	if r.ParkedKey == "" && !r.Ignored {
		if s, ok := m.skillByID(r.ID); ok {
			if m.isManual(s) {
				return text.TUIRowManual
			}
			if m.actions != nil && m.actions[s.ID].Provider != "" {
				return text.TUIRowProvider
			}
		}
	}
	return r.Short
}

// offers reports whether the panel of row offers key.
func (m *Model) offers(row SkillRow, key string) bool {
	return slices.ContainsFunc(m.rowActions(row), func(a panelAction) bool { return a.key == key })
}
