package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

// Stopping to ignore a skill (UX4, S13, L90). Ajustes lists the ignored
// skills as check boxes, all marked: unmarking one and confirming stops
// ignoring it. Under the «ignoradas» filter of Skills, x does the same on
// the selected row. Either way only the ignore setting changes, through
// App.SetSetting as x ignoring a skill does: no skill file is touched.

// settingIgnored is the cursor of the Ignoradas row of Ajustes, the last.
const settingIgnored = 2

// ignoreItem is one box of the ignored skills: a skill with every entry of
// the setting that names it, or an entry that names no skill.
type ignoreItem struct {
	// key is the item's first entry, unique in the setting.
	key     string
	name    string
	entries []string
	noSkill bool
}

// ignoreItems are the boxes, in the setting's order: one per ignored
// skill, so that unmarking it removes every entry that names it, and one
// per entry that names no skill now, so that no name is lost.
func (m *Model) ignoreItems() []ignoreItem {
	var out []ignoreItem
	for _, e := range m.settings.Ignore {
		i := slices.IndexFunc(m.ignored, func(s app.IgnoredSkill) bool { return slices.Contains(s.Entries, e) })
		switch {
		case i < 0:
			out = append(out, ignoreItem{key: e, name: e, entries: []string{e}, noSkill: true})
		case m.ignored[i].Entries[0] == e:
			s := m.ignored[i]
			out = append(out, ignoreItem{key: e, name: s.Skill.Name, entries: s.Entries})
		}
	}
	return out
}

// presentIgnored are the rows of the ignored skills, for the «ignoradas»
// filter, filtered by query as PresentSkills does.
func (m *Model) presentIgnored(query string) []SkillRow {
	skills := make([]domain.Skill, 0, len(m.ignored))
	for _, s := range m.ignored {
		skills = append(skills, s.Skill)
	}
	out := PresentSkills(skills, query)
	for i := range out {
		out[i].Ignored = true
		out[i].Kind = RowIgnored
		out[i].Short = text.TUIRowIgnored
	}
	return out
}

// openUnignore shows the boxes of the ignored skills, all marked; with
// none, it says how to ignore one instead.
func (m *Model) openUnignore() {
	items := m.ignoreItems()
	if len(items) == 0 {
		m.status = text.TUIIgnoredEmpty
		return
	}
	c := checks{marked: map[string]bool{}, labels: map[string]string{}}
	for _, it := range items {
		c.agents = append(c.agents, app.AgentChoice{Harness: it.key, Linked: true, Changeable: true})
		c.marked[it.key] = true
		c.labels[it.key] = it.name
		if it.noSkill {
			c.labels[it.key] += " " + text.TUIIgnoredNoSkill
		}
	}
	m.unignore = c
	m.mode = modeUnignore
}

func (m *Model) handleUnignoreKey(k string) (tea.Model, tea.Cmd) {
	if m.unignore.moveOrMark(k) {
		return m, nil
	}
	switch k {
	case "esc":
		m.mode = modeNormal
	case "enter":
		if m.busy {
			return m, nil
		}
		m.mode = modeNormal
		var drop []string
		for _, a := range m.unignore.agents {
			if !m.unignore.marked[a.Harness] {
				drop = append(drop, a.Harness)
			}
		}
		if len(drop) == 0 {
			m.status = text.TUIIgnoredNoChange
			return m, nil
		}
		return m, m.stopIgnoring(drop)
	}
	return m, nil
}

// unignoreSkill stops ignoring the ignored skill id, x under the
// «ignoradas» filter.
func (m *Model) unignoreSkill(id string) tea.Cmd {
	for _, s := range m.ignored {
		if s.Skill.ID == id {
			return m.stopIgnoring([]string{s.Entries[0]})
		}
	}
	return nil
}

// stopIgnoring saves the ignore setting without the items whose keys are
// in drop, and tells which skills come back. A kept entry with a comma is
// refused: the setting is saved as a comma-separated list and would split
// it.
func (m *Model) stopIgnoring(drop []string) tea.Cmd {
	var keep, skills, entries []string
	for _, it := range m.ignoreItems() {
		switch {
		case !slices.Contains(drop, it.key):
			keep = append(keep, it.entries...)
		case it.noSkill:
			entries = append(entries, it.name)
		default:
			skills = append(skills, it.name)
		}
	}
	if slices.ContainsFunc(keep, func(e string) bool { return strings.Contains(e, ",") }) {
		m.status = text.TUIIgnoredKeptComma
		return nil
	}
	m.busy = true
	m.unignoreReload = true
	m.unignoreFocus = ""
	if len(skills) > 0 {
		m.unignoreFocus = skills[0]
	}
	return unignoreCmd(m.app, keep, text.TUIUnignored(skills, entries))
}

func unignoreCmd(a *app.App, keep []string, done string) tea.Cmd {
	return func() tea.Msg {
		_, err := a.SetSetting(app.KeyIgnore, strings.Join(keep, ","), false)
		return settingsSavedMsg{status: done, err: err}
	}
}

func (m *Model) renderUnignore(boxW int) string {
	body := m.styles.title.Render(text.TUIIgnoredQuestion) + "\n" + text.TUIIgnoredHint + "\n\n" + m.checkLines(m.unignore)
	return m.styles.box.Width(boxW).Render(body)
}
