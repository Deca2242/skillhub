package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

// The agent check boxes of UX3: l on a skill, and step 3 of Instalar, list
// the agents that need a link ([x] Claude Code, [ ] Kiro), with one line on
// the agents that read the store and so are not listed. space marks, enter
// confirms. For a skill, the changes become chained plans, one link or
// unlink each, every one with its summary and its own confirmation; no
// agent id is ever typed.

// checks is the state of the check boxes on screen.
type checks struct {
	// key is the skill whose agents are chosen; "" for an install.
	key    string
	agents []app.AgentChoice
	// marked holds, by agent, whether its box is marked now.
	marked  map[string]bool
	readers []string
	cursor  int
	// labels, when set, name each box instead of the agent's name: the
	// ignored skills of Ajustes reuse the boxes (UX4).
	labels map[string]string
}

// chain is the rest of a choice of agents: the plans still to show, and
// how many it had.
type chain struct {
	left  []app.Request
	total int
}

// step is the number of the plan shown, from 1.
func (c chain) step() int { return c.total - len(c.left) }

type choicesMsg struct {
	id  int
	ch  app.AgentChoices
	err error
}

func choicesCmd(a *app.App, key string, id int) tea.Cmd {
	return func() tea.Msg {
		ch, err := a.AgentChoices(context.Background(), key)
		return choicesMsg{id: id, ch: ch, err: err}
	}
}

// openChecks reads the agent choices of the store skill key.
func (m *Model) openChecks(key string) tea.Cmd {
	m.busy = true
	m.status = text.TUIChecksReading
	return choicesCmd(m.app, key, m.ask())
}

func (m *Model) handleChoices(msg choicesMsg) {
	m.busy = false
	if !m.fresh(msg.id) {
		m.dropStale()
		return
	}
	if msg.err != nil {
		m.status = explainManage(msg.err, app.Request{Action: app.ActionLink, Key: msg.ch.Key})
		return
	}
	m.status = ""
	m.v2.checks = newChecks(msg.ch.Key, msg.ch.Agents, msg.ch.ReadStore)
	m.mode = modeChecks
}

func newChecks(key string, agents []app.AgentChoice, readers []string) checks {
	c := checks{key: key, agents: agents, readers: readers, marked: map[string]bool{}}
	for _, a := range agents {
		c.marked[a.Harness] = a.Linked
	}
	return c
}

// installChecks are the boxes of step 3 of an install: the proposed
// targets, all marked.
func installChecks(targets, readers []string) checks {
	agents := make([]app.AgentChoice, 0, len(targets))
	for _, t := range targets {
		agents = append(agents, app.AgentChoice{Harness: t, Changeable: true})
	}
	c := newChecks("", agents, readers)
	for _, t := range targets {
		c.marked[t] = true
	}
	return c
}

// moveOrMark handles the keys the boxes share; ok is false for any other.
func (c *checks) moveOrMark(k string) bool {
	switch k {
	case "up", "k":
		c.cursor = max(0, c.cursor-1)
	case "down", "j":
		c.cursor = max(0, min(len(c.agents)-1, c.cursor+1))
	case "space", " ":
		if c.cursor < len(c.agents) && c.agents[c.cursor].Changeable {
			h := c.agents[c.cursor].Harness
			c.marked[h] = !c.marked[h]
		}
	default:
		return false
	}
	return true
}

// chosen are the marked agents, in the list's order.
func (c checks) chosen() []string {
	var out []string
	for _, a := range c.agents {
		if c.marked[a.Harness] {
			out = append(out, a.Harness)
		}
	}
	return out
}

// changes are the plans the boxes ask for: the unlinks, then the links.
func (c checks) changes() []app.Request {
	var unlink, link []app.Request
	for _, a := range c.agents {
		switch {
		case !a.Changeable || c.marked[a.Harness] == a.Linked:
		case a.Linked:
			unlink = append(unlink, app.Request{Action: app.ActionUnlink, Key: c.key, Harness: a.Harness})
		default:
			link = append(link, app.Request{Action: app.ActionLink, Key: c.key, Harness: a.Harness})
		}
	}
	return append(unlink, link...)
}

func (m *Model) handleChecksKey(k string) (tea.Model, tea.Cmd) {
	if m.v2.checks.moveOrMark(k) {
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
		reqs := m.v2.checks.changes()
		if len(reqs) == 0 {
			m.status = text.TUIChecksNone
			return m, nil
		}
		m.v2.chain = chain{left: reqs[1:], total: len(reqs)}
		return m, m.startPlan(reqs[0])
	}
	return m, nil
}

// nextInChain plans the next change of agents after a step ran; when the
// user left meanwhile, the rest is dropped and told.
func (m *Model) nextInChain(id int) tea.Cmd {
	c := m.v2.chain
	if len(c.left) == 0 {
		m.v2.chain = chain{}
		return nil
	}
	if !m.fresh(id) {
		m.v2.chain = chain{}
		m.status += "\n" + text.TUIChainStale
		return nil
	}
	m.v2.chain.left = c.left[1:]
	return m.startPlan(c.left[0])
}

// stopChain drops the rest of a chain that did not go on, and says so.
func (m *Model) stopChain() {
	if len(m.v2.chain.left) > 0 {
		m.status = strings.TrimSpace(m.status + " " + text.TUIChainLeft)
	}
	m.v2.chain = chain{}
}

// checkLines are the boxes, with the cursor, and the line on the agents
// that read the store.
func (m *Model) checkLines(c checks) string {
	var b strings.Builder
	if len(c.agents) == 0 {
		b.WriteString(text.TUIChecksEmpty + "\n")
	}
	for i, a := range c.agents {
		cur, box := "  ", "[ ]"
		if i == c.cursor {
			cur = "> "
		}
		if c.marked[a.Harness] {
			box = "[x]"
		}
		name := m.app.AgentName(a.Harness)
		if c.labels != nil {
			name = c.labels[a.Harness]
		}
		line := fmt.Sprintf("%s%s %s", cur, box, name)
		if !a.Changeable {
			line += " " + text.TUIChecksFixed
		}
		b.WriteString(line + "\n")
	}
	if r := text.TUIChecksReaders(m.agentList(c.readers)); r != "" {
		b.WriteString("\n" + r + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderChecks(boxW int) string {
	body := m.styles.title.Render(text.TUIChecksQuestion(m.v2.checks.key)) + "\n\n" + m.checkLines(m.v2.checks)
	return m.styles.box.Width(boxW).Render(body)
}
