package tui

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

// The screens S13 gives the Skills treatment in UX2: Actualizaciones,
// Historial, Instalar, Doctor and Ajustes. Each one tells what it shows in
// plain words and names its keys; the technical detail stays in a panel.

// updateState picks an update's icon, its color and its short state: the
// traffic light of its risk (UX3), and "bloqueada" for one that cannot be
// applied.
func updateState(c domain.UpdateCandidate) (text.Risk, string) {
	risk, _ := text.RiskOf(c.Labels)
	if c.State == domain.CandidateBlocked {
		return risk, text.TUIUpdateShortBlocked
	}
	return risk, risk.Word()
}

func (m *Model) renderUpdates() string {
	w := max(20, m.width)
	unchecked := m.renderUncheckedUpdates(w)
	if len(m.candidates) == 0 {
		msg := text.TUIUpdatesNeverChecked
		if !m.lastCheck.IsZero() {
			msg = text.TUIUpdatesNoChanges(m.lastCheck, m.app.Now())
		}
		if unchecked == "" {
			return msg
		}
		return msg + "\n\n" + unchecked
	}
	items := make([]listItem, 0, len(m.candidates))
	for _, c := range m.candidates {
		risk, short := updateState(c)
		items = append(items, listItem{icon: risk.Icon(), style: m.styles.risks[risk], name: text.SafeLine(targetName(c.Target)), short: short})
	}
	list := func(width, height int) []string {
		return m.listLines(text.TUIUpdatesTitle(len(items)), items, m.updatesTable.Cursor(), &m.updatesTop, width, height)
	}
	out := m.split("", w >= wideLayout, list, m.updatePanel)
	if unchecked == "" {
		return out
	}
	return out + "\n" + unchecked
}

func (m *Model) renderUncheckedUpdates(width int) string {
	if len(m.unchecked) == 0 {
		return ""
	}
	lines := text.TUIUpdatesUnchecked(m.unchecked, m.app.Now())
	wrap := wrapper(width)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, wrapped := range wrap(line) {
			out = append(out, strings.TrimRight(wrapped, " "))
		}
	}
	return strings.Join(out, "\n")
}

// maxPanelFiles is how many changed files the panel lists.
const maxPanelFiles = 8

// updatePanel tells the selected update in plain words.
func (m *Model) updatePanel(width int) []string {
	c, ok := m.selectedCandidate()
	if !ok {
		return nil
	}
	wrap := wrapper(width)
	lines := []string{m.styles.title.Render(ansi.Truncate(text.SafeLine(targetName(c.Target)), width, "…"))}
	lines = append(lines, wrap(text.TUIUpdateVersions(ShortSHA(c.FromSHA), ShortSHA(c.ToCommit)))...)
	lines = append(lines, wrap(text.TUIRiskLine(c.Labels))...)
	lines = append(lines, wrap(text.TUIUpdateWhat(c.Labels))...)
	blocked := c.State != domain.CandidatePending
	if blocked {
		lines = append(lines, m.problemLines(wrap(text.TUIUpdateBlocked(text.BlockReason(c.BlockReason))))...)
	} else if isLocallyModified(c) {
		lines = append(lines, m.problemLines(wrap(text.TUIUpdateModifiedNote))...)
	}
	lines = append(lines, "", text.TUIUpdateFiles(len(c.FileChanges)))
	for i, f := range c.FileChanges {
		if i == maxPanelFiles {
			lines = append(lines, fmt.Sprintf("  … y %d más", len(c.FileChanges)-maxPanelFiles))
			break
		}
		lines = append(lines, ansi.Truncate(fmt.Sprintf("  %s  %s", text.FileChangeKind(f.Change), text.SafeLine(f.Path)), width, "…"))
	}
	actions := []string{text.TUIUpdateActionView}
	if !blocked {
		actions = append(actions, text.TUIUpdateActionApply, text.TUIUpdateActionSkip)
	}
	actions = append(actions, text.TUIUpdateActionAll)
	return append(append(lines, ""), m.actionLines(actions, width)...)
}

func (m *Model) updatesKeys() string {
	var keys []string
	if c, ok := m.selectedCandidate(); ok {
		keys = append(keys, text.TUIKeyView)
		if c.State == domain.CandidatePending {
			keys = append(keys, text.TUIKeyApply, text.TUIKeySkip)
		}
		keys = append(keys, text.TUIKeyApplyAll)
	}
	return strings.Join(append(keys, text.TUIKeyCheck, text.TUIKeyHelpQuit), " • ")
}

// renderApplyAll is the confirmation of applying every eligible update:
// its plain summary, and the technical list on request.
func (m *Model) renderApplyAll(boxW int) string {
	var included []string
	for _, r := range m.applyAllPlan.Included {
		included = append(included, targetName(r.Target))
	}
	var excluded []text.TUIExcluded
	for _, e := range m.applyAllPlan.Excluded {
		reason := text.TUIExcludedModified
		if e.Reason != msgExcludedLocal {
			reason = fmt.Sprintf(text.TUIExcludedBlocked, e.Reason)
		}
		excluded = append(excluded, text.TUIExcluded{Name: targetName(e.Target), Reason: reason})
	}
	out := m.renderSummary(boxW, text.TUIApplyAllSummary(included, excluded))
	if !m.details {
		return out
	}
	return out + "\n" + m.styles.frame.Width(boxW).Render(strings.TrimRight(PresentApplyAllPlan(m.applyAllPlan), "\n"))
}

func (m *Model) renderHistory() string {
	if len(m.historyRows) == 0 {
		return msgNoHistory
	}
	now := m.app.Now()
	items := make([]listItem, 0, len(m.historyRows))
	for i, r := range m.historyRows {
		items = append(items, listItem{name: text.TUIHistoryLine(m.historyDid(i), text.TUIAgo(r.at, now), r.CanUndo)})
	}
	list := func(width, height int) []string {
		return m.listLines(text.TUIHistoryTitle(len(items)), items, m.historyTable.Cursor(), &m.historyTop, width, height)
	}
	// The lines are sentences: the list takes the whole width.
	return m.split("", false, list, m.historyPanel)
}

// historyDid tells what the history row i did, in plain words.
func (m *Model) historyDid(i int) string {
	r := m.historyRows[i]
	if !strings.HasPrefix(r.Target, "v2:") {
		return text.TUIUpdateDid(r.action, targetName(r.Target))
	}
	return text.TUIOperationDid(r.action, m.operations[r.SnapshotID])
}

// historyPanel shows the selected line with its technical detail.
func (m *Model) historyPanel(width int) []string {
	i := m.historyTable.Cursor()
	if i < 0 || i >= len(m.historyRows) {
		return nil
	}
	r := m.historyRows[i]
	wrap := wrapper(width)
	lines := wrap(m.styles.title.Render(m.historyDid(i)))
	lines = append(lines, wrap(fmt.Sprintf("Cuándo: %s (%s)", r.At, text.TUIAgo(r.at, m.app.Now())))...)
	lines = append(lines, wrap("Operación: "+text.SafeLine(r.Target))...)
	if r.SnapshotID != msgEmpty {
		lines = append(lines, wrap("Snapshot: "+text.SafeLine(r.SnapshotID))...)
	}
	if !strings.HasPrefix(r.Target, "v2:") {
		lines = append(lines, wrap("Versiones: "+text.SafeLine(r.Versions))...)
	}
	if r.Detail != msgEmpty {
		// The detail holds long identifiers: one per line, cut, never wrapped.
		lines = append(lines, "Detalle:")
		for _, f := range strings.Fields(r.Detail) {
			lines = append(lines, ansi.Truncate("  "+text.SafeLine(f), width, "…"))
		}
	}
	if r.CanUndo {
		lines = append(lines, "")
		lines = append(lines, m.actionLines([]string{text.TUIHistoryActionUndo}, width)...)
	}
	return lines
}

func (m *Model) historyKeys() string {
	var keys []string
	if i := m.historyTable.Cursor(); i >= 0 && i < len(m.historyRows) && m.historyRows[i].CanUndo {
		keys = append(keys, text.TUIKeyUndo)
	}
	return strings.Join(append(keys, text.TUIKeyCheck, text.TUIKeyHelpQuit), " • ")
}

func (m *Model) renderSettings() string {
	sv := PresentSettings(m.settings, m.bgStatus)
	p0, p1, p2 := "  ", "  ", "  "
	switch m.settingsCursor {
	case 0:
		p0 = "> "
	case 1:
		p1 = "> "
	default:
		p2 = "> "
	}
	// The box's border and padding take 6 columns; each note is indented.
	inner := max(10, max(20, m.width)-6)
	note := func(s string) string { return m.styles.muted.PaddingLeft(4).Width(inner).Render(s) + "\n" }
	var b strings.Builder
	fmt.Fprintf(&b, "%sIntervalo de revisión:  %s\n", p0, sv.CheckInterval)
	b.WriteString(note(text.TUISettingInterval))
	fmt.Fprintf(&b, "%sNotificaciones:         %s\n", p1, sv.Notifications)
	b.WriteString(note(text.TUISettingNotifications))
	fmt.Fprintf(&b, "%sIgnoradas:              %s\n", p2, sv.Ignore)
	b.WriteString(note(text.TUISettingIgnore) + "\n")
	fmt.Fprintf(&b, "  Revisión en segundo plano: %s (instalada: %s, activa: %s)\n",
		sv.BackgroundState, sv.BackgroundInstalled, sv.BackgroundActive)
	b.WriteString(note(text.TUISettingBackground))
	fmt.Fprintf(&b, "  Comando: %s\n", sv.BackgroundCommand)
	if m.mode == modeEditInterval {
		b.WriteString("\n" + m.intervalInput.View() + "\n(enter para guardar, esc para cancelar)")
	}
	return m.styles.box.Width(max(20, m.width)).Render(strings.TrimRight(b.String(), "\n"))
}

func (m *Model) settingsKeys() string {
	return strings.Join([]string{text.TUIKeySettingsMove, text.TUIKeySettingsChange, text.TUIKeyCheck, text.TUIKeyHelpQuit}, " • ")
}

// renderInstall shows the optional catalog or the current install step.
func (m *Model) renderInstall(boxW int) string {
	step := func(n int) string { return m.styles.title.Render(text.TUIInstallStep(n)) }
	switch m.mode {
	case modeInstallSource:
		return m.styles.box.Width(boxW).Render(step(1) + "\n\n" + text.TUIInstallSourceHelp + "\n\n" +
			m.v2.source.View() + "\n\n" + text.TUIPromptHint)
	case modeInstallPick:
		return m.renderInstallPick(boxW)
	case modeInstallReview:
		// The plain summary comes before the files (S13).
		rev := m.v2.revision
		sum := text.TUIInstallSummary(rev.Prepared.Resolved.Key, rev.Repo, rev.Site)
		head := step(2) + "\n" + m.styles.title.Render(sum.Question) + "\n" + strings.Join(sum.Lines, "\n") +
			"\n" + text.TUIInstallRiskLine(rev.Prepared.Labels) + "\n" + text.TUIInstallBrings(rev.Prepared.Labels)
		return m.styles.frame.Width(boxW).Render(head) + "\n" +
			m.styles.box.Width(boxW).Render(m.v2.review.View()) + "\n" + m.styles.muted.Width(boxW).Render(text.TUIInstallReview)
	case modeInstallTargets:
		return m.styles.box.Width(boxW).Render(step(3) + "\n\n" + text.TUIChecksInstall + "\n\n" +
			m.checkLines(m.v2.checks))
	}
	if m.app.HasCatalog() {
		return m.renderCatalog(boxW)
	}
	return m.styles.box.Width(boxW).Render(text.TUIInstallIntro)
}

func (m *Model) renderInstallPick(boxW int) string {
	choices := m.v2.pickChoices
	visible := max(3, m.height-12)
	start := 0
	if m.v2.cursor >= visible {
		start = m.v2.cursor - visible + 1
	}
	end := min(len(choices), start+visible)
	innerW := max(20, boxW-4)

	var b strings.Builder
	b.WriteString(m.styles.title.Render(text.TUIInstallPickTitle) + "\n")
	b.WriteString(text.TUIInstallPickPrompt + "\n\n")
	for i := start; i < end; i++ {
		c := choices[i]
		mark := "  "
		if i == m.v2.cursor {
			mark = "> "
		}
		name := text.SafeLine(c.Name)
		source := text.SafeLine(c.Source)
		var line string
		if folder := text.SafeLine(c.Folder); folder != "" {
			line = fmt.Sprintf("%s%s — %s (%s)", mark, name, folder, source)
		} else {
			line = fmt.Sprintf("%s%s (%s)", mark, name, source)
		}
		b.WriteString(ansi.Truncate(line, innerW, "…") + "\n")
	}
	if len(choices) > visible {
		fmt.Fprintf(&b, "\n%d/%d", m.v2.cursor+1, len(choices))
	}
	return m.styles.box.Width(boxW).Render(strings.TrimRight(b.String(), "\n"))
}

// The Salud list of INV10 (S15 #9): Doctor's findings grouped first by the
// skill they affect. Each skill is one group, listed once on its own line;
// the group the cursor is in is open, with its findings under it, and the
// others tell how many they hold. A finding that names several skills is
// listed under each of them (A31): every row carries the finding itself, so
// its repair is the same request, and the same plan, from any of them. The
// findings that name no skill keep a group of their kind, and gentle-ai's
// copies are counted apart, in one line. Only the grouping changed: a row
// still leads to repairsCmd and to the plan its confirmation approves.

// healthGroup is a group of the Salud list: the findings of one skill, or
// the ones of one kind that name no skill (skill "").
type healthGroup struct {
	// key tells the group apart from the others: its skill's, or its kind's
	// when it has no skill.
	key   string
	skill string
	// name is what the list calls the skill, or the kind of a group without
	// skill; line is the group's line, which tells apart two skills that
	// share a name.
	name, line string
	// rank is the place, in text.TUIFindingOrder, of its most urgent
	// finding; class orders the groups of a rank: 0 without skill, 1 a skill
	// of the inventory, 2 any other skill.
	rank, class int
	findings    []app.Finding
}

func (g *healthGroup) add(f app.Finding) {
	g.findings = append(g.findings, f)
	g.rank = min(g.rank, kindRank(f.Kind))
}

// healthRow is a row of the Salud list the cursor stops at: a finding and
// the index of the group it is listed in.
type healthRow struct {
	finding app.Finding
	group   int
}

// kindRank is the place of kind in text.TUIFindingOrder, the order in which
// a repair fixes them; an unknown kind goes last.
func kindRank(kind string) int {
	if i := slices.Index(text.TUIFindingOrder, kind); i >= 0 {
		return i
	}
	return len(text.TUIFindingOrder)
}

// healthGroups puts the findings in the groups of Salud, in their order.
// actions tell, by their BrokenLinks, the skill each broken link leads to;
// inventory has the name of each skill of the inventory by its ID. gentle
// counts gentle-ai's copies, which are left out.
//
// The groups go by rank; in a rank, a group without skill first, then the
// skills of the inventory, then the others, each by name and ID. In a group,
// the findings go by their kind's rank, each kind in Doctor's order.
func healthGroups(findings []app.Finding, actions map[string]app.SkillActions, inventory map[string]string) (groups []*healthGroup, gentle int) {
	linked := map[string][]string{}
	for id, acts := range actions {
		for _, l := range acts.BrokenLinks {
			linked[l.Finding.ID] = append(linked[l.Finding.ID], id)
		}
	}
	byKey := map[string]*healthGroup{}
	group := func(key string, g healthGroup) *healthGroup {
		if byKey[key] == nil {
			g.key, g.rank = key, len(text.TUIFindingOrder)+1
			byKey[key] = &g
			groups = append(groups, &g)
		}
		return byKey[key]
	}
	for _, f := range findings {
		if f.Kind == text.TUIGentleKind {
			gentle++
			continue
		}
		skills := findingSkills(f, linked[f.ID])
		if len(skills) == 0 {
			group("kind:"+f.Kind, healthGroup{name: f.Kind, line: text.TUIHealthNoSkill(f.Kind)}).add(f)
			continue
		}
		for _, id := range skills {
			class := 2
			if _, ok := inventory[id]; ok {
				class = 1
			}
			g := group("skill:"+id, healthGroup{skill: id, class: class})
			g.add(f)
			if g.name == "" && (f.Skill == id || slices.Contains(f.Skills, id)) {
				g.name = f.Name
			}
		}
	}
	nameSkills(groups, inventory)
	slices.SortFunc(groups, func(a, b *healthGroup) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.class, b.class),
			strings.Compare(a.name, b.name), strings.Compare(a.skill, b.skill))
	})
	for _, g := range groups {
		slices.SortStableFunc(g.findings, func(a, b app.Finding) int {
			return cmp.Compare(kindRank(a.Kind), kindRank(b.Kind))
		})
	}
	return groups, gentle
}

// findingSkills are the skills f is about, each once: its own, the ones it
// names (A31), and linked, the ones whose broken link it is.
func findingSkills(f app.Finding, linked []string) []string {
	var out []string
	if f.Skill != "" {
		out = append(out, f.Skill)
	}
	out = append(out, f.Skills...)
	out = append(out, linked...)
	slices.Sort(out)
	return slices.Compact(out)
}

// nameSkills names each skill's group: as the inventory names the skill,
// else as its findings do, else by its folder. When two groups share a name,
// a skill that is a folder (dir: or git:) adds the folder that holds it, or
// its own folder when that one is shared too.
func nameSkills(groups []*healthGroup, inventory map[string]string) {
	names := map[string]int{}
	for _, g := range groups {
		if g.skill == "" {
			continue
		}
		if name := inventory[g.skill]; name != "" {
			g.name = name
		}
		_, rel, _ := strings.Cut(g.skill, ":")
		g.name = cmp.Or(g.name, path.Base(rel))
		g.line = g.name
		names[g.name]++
	}
	lines := map[string]int{}
	for _, g := range groups {
		if rel, ok := skillFolder(g.skill); ok && names[g.name] > 1 {
			g.line = text.TUIHealthAt(g.name, "~/"+path.Dir(rel))
		}
		lines[g.line]++
	}
	for _, g := range groups {
		if rel, ok := skillFolder(g.skill); ok && names[g.name] > 1 && lines[g.line] > 1 {
			g.line = text.TUIHealthAt(g.name, "~/"+rel)
		}
	}
}

// skillFolder is the home-relative folder of a skill whose ID names one
// (dir: or git:).
func skillFolder(id string) (string, bool) {
	kind, rel, ok := strings.Cut(id, ":")
	return rel, ok && (kind == "dir" || kind == "git")
}

// healthRows are the rows of groups, in the order the cursor walks them.
func healthRows(groups []*healthGroup) []healthRow {
	var rows []healthRow
	for i, g := range groups {
		for _, f := range g.findings {
			rows = append(rows, healthRow{finding: f, group: i})
		}
	}
	return rows
}

// healthList groups the findings on screen with what the load told of each
// skill: where its broken links are, and its name in the inventory.
func (m *Model) healthList() (groups []*healthGroup, rows []healthRow, gentle int) {
	inventory := make(map[string]string, len(m.skills))
	for _, s := range m.skills {
		inventory[s.ID] = s.Name
	}
	groups, gentle = healthGroups(m.v2.findings, m.actions, inventory)
	return groups, healthRows(groups), gentle
}

// healthSel is the row of the Salud list the cursor is on, kept by its
// finding and the group listing it rather than by its place: the rows change
// under the cursor when the findings or the skills of the broken links
// arrive, or a repair resolves some, and the cursor stays on its finding (B1
// of INV10's verifier). row, its last place, keeps the cursor in the list
// once the finding is gone.
type healthSel struct {
	finding, group string
	row            int
}

// at is the row of rows the cursor is on: the selected finding in its group,
// else in another group that lists it, else the row at the selected place,
// kept in the list; -1 when the list is empty.
func (s healthSel) at(groups []*healthGroup, rows []healthRow) int {
	if len(rows) == 0 {
		return -1
	}
	other := -1
	for i, r := range rows {
		if r.finding.ID != s.finding {
			continue
		}
		if groups[r.group].key == s.group {
			return i
		}
		if other < 0 {
			other = i
		}
	}
	if other >= 0 {
		return other
	}
	return max(0, min(s.row, len(rows)-1))
}

// healthAt selects row i of the list; nothing when the list has no row i.
func healthAt(groups []*healthGroup, rows []healthRow, i int) healthSel {
	if i < 0 || i >= len(rows) {
		return healthSel{}
	}
	return healthSel{finding: rows[i].finding.ID, group: groups[rows[i].group].key, row: i}
}

// findingAgents are the agents a finding listed under skill concerns: for a
// broken link, the ones whose folder holds it, as the skill's LinkFix tells.
func (m *Model) findingAgents(f app.Finding, skill string) []string {
	for _, l := range m.actions[skill].BrokenLinks {
		if l.Finding.ID == f.ID {
			return l.Harnesses
		}
	}
	return f.Harnesses
}

// healthLines are the lines of the skill groups that list f, other than
// except.
func healthLines(groups []*healthGroup, f app.Finding, except *healthGroup) []string {
	var out []string
	for _, g := range groups {
		listed := slices.ContainsFunc(g.findings, func(o app.Finding) bool { return o.ID == f.ID })
		if g != except && g.skill != "" && listed {
			out = append(out, g.line)
		}
	}
	return out
}

// findingName is what the list calls a finding: its skill, else its first
// path's folder.
func findingName(f app.Finding) string {
	switch {
	case f.Skill != "":
		return f.Skill
	case f.Name != "":
		return f.Name
	case len(f.Paths) > 0:
		return path.Base(f.Paths[0])
	}
	return f.ID
}

// renderDoctor is the Salud list: a line per group, the one the cursor is in
// open with a row per finding, and the panel of the finding under the
// cursor.
func (m *Model) renderDoctor(boxW int) string {
	if !m.v2.findingsLoaded {
		return text.TUISearching
	}
	groups, rows, gentle := m.healthList()
	var prefix string
	if gentle > 0 {
		prefix = m.styles.muted.Width(boxW).Render(text.TUIGentleFolded(gentle)) + "\n"
	}
	if len(rows) == 0 {
		return prefix + text.TUINoFindings
	}
	prefix += m.styles.muted.Width(boxW).Render(text.TUIFindingsHint) + "\n"
	sel := m.v2.health.at(groups, rows)
	open := rows[sel].group
	var items []listItem
	cursor, head, n, skills := 0, 0, 0, 0
	for i, g := range groups {
		if g.skill != "" {
			skills++
		}
		mark := text.TUIHealthClosed
		if i == open {
			mark, head = text.TUIHealthOpen, len(items)
		}
		items = append(items, listItem{name: mark + " " + text.SafeLine(g.line), short: text.TUIHealthCount(len(g.findings)), header: true})
		if i != open {
			n += len(g.findings)
			continue
		}
		for _, f := range g.findings {
			if n == sel {
				cursor = len(items)
			}
			n++
			name := text.SafeLine(findingName(f))
			if g.skill != "" {
				name = text.TUIHealthRow(f.Kind)
			}
			items = append(items, listItem{icon: "·", style: m.styles.muted, name: name, short: m.agentNames(m.findingAgents(f, g.skill))})
		}
	}
	// Each finding is counted once, however many skills list it.
	title := text.TUIHealthTitle(skills, len(m.v2.findings)-gentle)
	list := func(width, height int) []string {
		// The line of the open group stays in sight above its findings.
		m.v2.doctorTop = min(m.v2.doctorTop, head)
		return m.listLines(ansi.Truncate(title, width, "…"), items, cursor, &m.v2.doctorTop, width, height)
	}
	return m.split(prefix, boxW >= wideLayout, list, m.doctorPanel)
}

// doctorPanel tells the finding under the cursor: the skill it is listed
// under, whose other findings the open group shows, or the group of its
// kind when it names no skill; then the agents it concerns, where it is and
// the other skills it is listed under.
func (m *Model) doctorPanel(width int) []string {
	groups, rows, _ := m.healthList()
	i := m.v2.health.at(groups, rows)
	if i < 0 {
		return nil
	}
	g, f := groups[rows[i].group], rows[i].finding
	wrap := wrapper(width)
	title := func(s string) string { return m.styles.title.Render(ansi.Truncate(s, width, "…")) }
	var lines []string
	if g.skill == "" {
		n := len(g.findings)
		lines = append(lines, title(text.TUIFindingGroupTitle(f.Kind, n)))
		lines = append(lines, wrap(text.TUIHealthFindings(f.Kind, n))...)
		lines = append(lines, "", title(text.SafeLine(findingName(f))))
	} else {
		lines = append(lines, title(text.SafeLine(g.line)), "")
		lines = append(lines, wrap(text.TUIHealthFindings(f.Kind, 1))...)
	}
	if agents := m.findingAgents(f, g.skill); len(agents) > 0 {
		lines = append(lines, wrap(text.TUIDoctorAgents+m.agentNames(agents))...)
	}
	if len(f.Paths) > 0 {
		lines = append(lines, text.TUIDoctorWhere)
		for _, p := range f.Paths {
			lines = append(lines, wrap("  "+text.SafeLine(m.tilde(p)))...)
		}
	}
	if others := healthLines(groups, f, g); len(others) > 0 {
		lines = append(lines, wrap(text.TUIHealthAlsoIn(others))...)
	}
	lines = append(lines, wrap("Hallazgo: "+text.SafeLine(f.ID))...)
	lines = append(lines, "")
	actions := []string{text.TUIDoctorActionRepair}
	if f.Kind == "homonyms" && len(f.Skills) >= 2 {
		actions = append(actions, text.TUIActionCompare)
	}
	return append(lines, m.actionLines(actions, width)...)
}

func (m *Model) doctorKeys() string {
	var keys []string
	groups, rows, _ := m.healthList()
	if len(rows) > 0 {
		keys = append(keys, text.TUIKeyRepair)
		if i := m.v2.health.at(groups, rows); i >= 0 && rows[i].finding.Kind == "homonyms" && len(rows[i].finding.Skills) >= 2 {
			keys = append(keys, text.TUIKeyCompareSkills)
		}
	}
	return strings.Join(append(keys, text.TUIKeyCheck, text.TUIKeyHelpQuit), " • ")
}

// renderChoose asks how to repair a finding, or which copy stays as the
// original, in plain words. It tells the one finding chosen, under the
// skills that list it (INV10), whichever of them it was chosen from: its
// repair is one plan for all of them.
func (m *Model) renderChoose(boxW int) string {
	f := m.v2.finding
	groups, _, _ := m.healthList()
	name := text.SafeLine(findingName(f))
	if skills := healthLines(groups, f, nil); len(skills) > 0 {
		name = text.JoinNames(skills)
	}
	var b strings.Builder
	b.WriteString(m.styles.title.Render(text.TUIRepairsQuestion) + "\n")
	b.WriteString(text.TUIHealthFindings(f.Kind, 1) + "\n\n")
	b.WriteString(name + "\n")
	if m.mode != modeCanonical {
		// The canonical choice lists the paths itself.
		for _, p := range f.Paths {
			fmt.Fprintf(&b, "  %s\n", text.SafeLine(m.tilde(p)))
		}
	}
	b.WriteString("\n")
	if m.mode == modeCanonical {
		b.WriteString(text.TUIChooseCanon + "\n")
		shown := make([]string, 0, len(f.Paths))
		for _, p := range f.Paths {
			shown = append(shown, text.SafeLine(m.tilde(p)))
		}
		b.WriteString(PresentChoices(shown, m.v2.cursor))
	} else {
		items := make([]string, 0, len(m.v2.offer.Repairs))
		for _, r := range m.v2.offer.Repairs {
			items = append(items, text.TUIRepairPlain(r))
		}
		b.WriteString(PresentChoices(items, m.v2.cursor))
	}
	return m.styles.box.Width(boxW).Render(strings.TrimRight(b.String(), "\n")) + "\n" +
		m.styles.muted.Width(boxW).Render(text.TUIHelpChoose)
}

// tilde shows a path under the home as ~/...: shorter, and what the user
// recognizes.
func (m *Model) tilde(p string) string {
	home := m.app.Paths().Home()
	if rest, ok := strings.CutPrefix(p, home+"/"); ok && home != "" {
		return "~/" + rest
	}
	return p
}

// problemLines colors lines as a problem.
func (m *Model) problemLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, m.styles.problem.Render(l))
	}
	return out
}

// wrapper wraps a text to width, as lines.
func wrapper(width int) func(string) []string {
	return func(s string) []string {
		return strings.Split(lipgloss.NewStyle().Width(width).Render(s), "\n")
	}
}
