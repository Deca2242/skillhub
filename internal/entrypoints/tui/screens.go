package tui

import (
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
	if len(m.candidates) == 0 {
		return msgNoUpdates
	}
	items := make([]listItem, 0, len(m.candidates))
	for _, c := range m.candidates {
		risk, short := updateState(c)
		items = append(items, listItem{icon: risk.Icon(), style: m.styles.risks[risk], name: targetName(c.Target), short: short})
	}
	list := func(width, height int) []string {
		return m.listLines(text.TUIUpdatesTitle(len(items)), items, m.updatesTable.Cursor(), &m.updatesTop, width, height)
	}
	return m.split("", max(20, m.width) >= wideLayout, list, m.updatePanel)
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
	lines := []string{m.styles.title.Render(ansi.Truncate(targetName(c.Target), width, "…"))}
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
		lines = append(lines, ansi.Truncate(fmt.Sprintf("  %s  %s", text.FileChangeKind(f.Change), f.Path), width, "…"))
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
	lines = append(lines, wrap("Operación: "+r.Target)...)
	if r.SnapshotID != msgEmpty {
		lines = append(lines, wrap("Snapshot: "+r.SnapshotID)...)
	}
	if !strings.HasPrefix(r.Target, "v2:") {
		lines = append(lines, wrap("Versiones: "+r.Versions)...)
	}
	if r.Detail != msgEmpty {
		// The detail holds long identifiers: one per line, cut, never wrapped.
		lines = append(lines, "Detalle:")
		for _, f := range strings.Fields(r.Detail) {
			lines = append(lines, ansi.Truncate("  "+f, width, "…"))
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

// renderInstall is the Instalar tab. CAT2 adds the search of skills.sh
// here, before the source; until then the tab starts at the source.
func (m *Model) renderInstall(boxW int) string {
	step := func(n int) string { return m.styles.title.Render(text.TUIInstallStep(n)) }
	switch m.mode {
	case modeInstallSource:
		return m.styles.box.Width(boxW).Render(step(1) + "\n\n" + text.TUIInstallSourceHelp + "\n\n" +
			m.v2.source.View() + "\n\n" + text.TUIPromptHint)
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
	return m.styles.box.Width(boxW).Render(text.TUIInstallIntro)
}

// doctorGroups puts the findings in their groups, in TUIFindingOrder and
// then by kind, leaving gentle-ai's copies out: they are counted apart.
func doctorGroups(findings []app.Finding) (groups [][]app.Finding, gentle int) {
	byKind := map[string][]app.Finding{}
	var kinds []string
	for _, f := range findings {
		if f.Kind == text.TUIGentleKind {
			gentle++
			continue
		}
		if _, ok := byKind[f.Kind]; !ok {
			kinds = append(kinds, f.Kind)
		}
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}
	rank := func(k string) int {
		if i := slices.Index(text.TUIFindingOrder, k); i >= 0 {
			return i
		}
		return len(text.TUIFindingOrder)
	}
	slices.SortStableFunc(kinds, func(a, b string) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for _, k := range kinds {
		groups = append(groups, byKind[k])
	}
	return groups, gentle
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

func (m *Model) renderDoctor(boxW int) string {
	if !m.v2.findingsLoaded {
		return text.TUISearching
	}
	groups, gentle := doctorGroups(m.v2.findings)
	var prefix string
	if gentle > 0 {
		prefix = m.styles.muted.Width(boxW).Render(text.TUIGentleFolded(gentle)) + "\n"
	}
	if len(groups) == 0 {
		return prefix + text.TUINoFindings
	}
	prefix += m.styles.muted.Width(boxW).Render(text.TUIFindingsHint) + "\n"
	var items []listItem
	cursor, n := 0, 0
	for _, g := range groups {
		items = append(items, listItem{name: text.TUIFindingGroupTitle(g[0].Kind, len(g)), header: true})
		for _, f := range g {
			if n == m.v2.findingsTable.Cursor() {
				cursor = len(items)
			}
			n++
			items = append(items, listItem{icon: "·", style: m.styles.muted, name: findingName(f), short: m.agentNames(f.Harnesses)})
		}
	}
	list := func(width, height int) []string {
		return m.listLines(text.TUIDoctorTitle(n), items, cursor, &m.v2.doctorTop, width, height)
	}
	return m.split(prefix, boxW >= wideLayout, list, m.doctorPanel)
}

// doctorPanel explains the selected finding's group and shows the finding.
func (m *Model) doctorPanel(width int) []string {
	i := m.v2.findingsTable.Cursor()
	if i < 0 || i >= len(m.v2.doctorFindings) {
		return nil
	}
	f := m.v2.doctorFindings[i]
	n := 0
	for _, g := range m.v2.doctorFindings {
		if g.Kind == f.Kind {
			n++
		}
	}
	wrap := wrapper(width)
	lines := []string{m.styles.title.Render(ansi.Truncate(text.TUIFindingGroupTitle(f.Kind, n), width, "…"))}
	lines = append(lines, wrap(text.TUIFindingGroup(f.Kind, n))...)
	lines = append(lines, "", m.styles.title.Render(ansi.Truncate(findingName(f), width, "…")))
	if len(f.Harnesses) > 0 {
		lines = append(lines, wrap(text.TUIDoctorAgents+m.agentNames(f.Harnesses))...)
	}
	if len(f.Paths) > 0 {
		lines = append(lines, text.TUIDoctorWhere)
		for _, p := range f.Paths {
			lines = append(lines, wrap("  "+m.tilde(p))...)
		}
	}
	lines = append(lines, wrap("Hallazgo: "+f.ID)...)
	lines = append(lines, "")
	return append(lines, m.actionLines([]string{text.TUIDoctorActionRepair}, width)...)
}

func (m *Model) doctorKeys() string {
	var keys []string
	if len(m.v2.doctorFindings) > 0 {
		keys = append(keys, text.TUIKeyRepair)
	}
	return strings.Join(append(keys, text.TUIKeyCheck, text.TUIKeyHelpQuit), " • ")
}

// renderChoose asks how to repair a finding, or which copy stays as the
// original, in plain words.
func (m *Model) renderChoose(boxW int) string {
	f := m.v2.finding
	n := 0
	for _, g := range m.v2.findings {
		if g.Kind == f.Kind {
			n++
		}
	}
	var b strings.Builder
	b.WriteString(m.styles.title.Render(text.TUIRepairsQuestion) + "\n")
	b.WriteString(text.TUIFindingGroup(f.Kind, max(1, n)) + "\n\n")
	b.WriteString(findingName(f) + "\n")
	if m.mode != modeCanonical {
		// The canonical choice lists the paths itself.
		for _, p := range f.Paths {
			fmt.Fprintf(&b, "  %s\n", m.tilde(p))
		}
	}
	b.WriteString("\n")
	if m.mode == modeCanonical {
		b.WriteString(text.TUIChooseCanon + "\n")
		shown := make([]string, 0, len(f.Paths))
		for _, p := range f.Paths {
			shown = append(shown, m.tilde(p))
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
