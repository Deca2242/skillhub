package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

// The version 2 operations in the TUI (docs/design-v2.md sections 9, 10
// and 11 point 8): every action plans first, shows the plan with the same
// preview the CLI prints, and runs only the plan whose identifier the user
// confirmed. A plan that changed meanwhile is shown again and needs its own
// confirmation; it never runs by itself. While a plan is made or runs, m.busy
// keeps every other write key inert, as in version 1.

// manage is the state of the version 2 flows.
type manage struct {
	// prompt asks the key of the skill to enable when no disabled row is
	// selected.
	prompt       textinput.Model
	promptAction app.Action
	promptKey    string

	// plan is the plan waiting for confirmation, shown in planView.
	plan     app.Proposal
	planView viewport.Model

	source   textinput.Model
	revision app.Revision
	// checks are the agent check boxes on screen (checks.go), of a skill or
	// of an install's step 3; chain is the rest of a choice of agents.
	checks checks
	chain  chain
	review viewport.Model

	findings       []app.Finding
	findingsLoaded bool
	// doctorFindings are the findings the Doctor list offers, in the order
	// of its groups; findingsTable keeps the cursor over them.
	doctorFindings []app.Finding
	doctorTop      int
	// searching is set while Doctor's findings are looked for.
	searching     bool
	findingsTable table.Model
	finding       app.Finding
	offer         app.Offer
	repair        string
	cursor        int

	// asked is the asynchronous request whose result the screen waits for,
	// and seq numbers them (B1 and B2 of W8's verifier).
	asked asked
	seq   int

	// help scrolls the help when it does not fit the terminal.
	help       viewport.Model
	helpScroll bool
}

// asked is a request in flight: its number and the screen that asked.
type asked struct {
	id     int
	screen Screen
}

func newManage() manage {
	pr := textinput.New()
	pr.CharLimit = 200
	src := textinput.New()
	src.Prompt = text.TUIPromptSource
	src.CharLimit = 300
	// Plans and reviews hold long paths: they wrap instead of being cut.
	pv := viewport.New(viewport.WithWidth(94), viewport.WithHeight(16))
	pv.SoftWrap = true
	rv := viewport.New(viewport.WithWidth(94), viewport.WithHeight(16))
	rv.SoftWrap = true
	return manage{
		prompt:        pr,
		source:        src,
		planView:      pv,
		review:        rv,
		findingsTable: newList(),
		help:          viewport.New(viewport.WithWidth(94), viewport.WithHeight(16)),
	}
}

type plannedMsg struct {
	id  int
	req app.Request
	p   app.Proposal
	err error
}

type appliedMsg struct {
	id  int
	req app.Request
	res app.Result
	err error
}

type resolvedMsg struct {
	id      int
	rev     app.Revision
	review  string
	targets []string
	readers []string
	err     error
}

type findingsMsg struct {
	findings []app.Finding
	err      error
}

type repairsMsg struct {
	id      int
	finding app.Finding
	offer   app.Offer
	err     error
}

// updateManage handles the messages of the version 2 flows; ok is false
// for any other message.
func (m *Model) updateManage(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case plannedMsg:
		return m, m.handlePlanned(msg), true
	case appliedMsg:
		return m, m.handleApplied(msg), true
	case resolvedMsg:
		m.handleResolved(msg)
		return m, nil, true
	case findingsMsg:
		m.handleFindings(msg)
		return m, nil, true
	case repairsMsg:
		m.handleRepairs(msg)
		return m, nil, true
	case choicesMsg:
		m.handleChoices(msg)
		return m, nil, true
	}
	return m, nil, false
}

// ask numbers a new asynchronous request from the active screen. Its
// result may change the mode only while fresh holds.
func (m *Model) ask() int {
	m.v2.seq++
	m.v2.asked = asked{id: m.v2.seq, screen: m.screen}
	return m.v2.seq
}

// fresh reports whether the result of request id may still change the
// mode: it is the request in flight, and the user is on the screen that
// asked, in no other mode. A key typed meanwhile for another mode, such as
// the filter, never confirms what arrives late.
func (m *Model) fresh(id int) bool {
	return id == m.v2.asked.id && m.screen == m.v2.asked.screen && m.mode == modeNormal
}

// dropStale tells the user a result arrived too late and was dropped.
func (m *Model) dropStale() {
	m.status = text.TUIStaleResult
}

// discardRevision discards the prepared copy of the install in progress,
// if any: an install that is not applied leaves no copy behind.
func (m *Model) discardRevision() {
	rev := m.v2.revision
	m.v2.revision = app.Revision{}
	if rev.Prepared.StagedPath == "" {
		return
	}
	if err := m.app.DiscardRevision(rev); err != nil {
		m.status = strings.TrimSpace(m.status + "\n" + text.TUIDiscardFailed)
	}
}

// startPlan plans req in the background.
func (m *Model) startPlan(req app.Request) tea.Cmd {
	m.busy = true
	m.status = text.TUIPlanning
	return planCmd(m.app, req, m.ask())
}

func (m *Model) handlePlanned(msg plannedMsg) tea.Cmd {
	m.busy = false
	if !m.fresh(msg.id) {
		m.dropStale()
		m.v2.chain = chain{}
		if msg.req.Action == app.ActionInstall {
			m.discardRevision()
		}
		return nil
	}
	if msg.err != nil || msg.p.PlanID == "" {
		defer m.stopChain()
	}
	if msg.err != nil {
		if msg.req.Action == app.ActionInstall {
			defer m.discardRevision()
		}
		if errors.Is(msg.err, app.ErrNeedsCanonical) && msg.req.Action == app.ActionRepair && len(m.v2.finding.Paths) > 0 {
			m.v2.cursor = 0
			m.mode = modeCanonical
			m.status = ""
			return nil
		}
		m.status = explainManage(msg.err, msg.req)
		return nil
	}
	p := msg.p
	if p.PlanID == "" {
		if p.Repair != nil && p.Repair.Reinstall != nil {
			m.status = strings.TrimSpace(text.Reinstall(p.Repair.Reinstall))
		} else {
			m.status = sentence(text.NoPlanToApply)
		}
		return nil
	}
	m.status = ""
	m.details = false
	m.showPlan(p)
	return nil
}

// showPlan puts p in the confirmation.
func (m *Model) showPlan(p app.Proposal) {
	m.v2.plan = p
	m.v2.planView.SetContent(text.Proposal(p) + "\n" + text.TUIPlanID(p.PlanID))
	m.v2.planView.GotoTop()
	m.mode = modePlan
	m.layoutPlan()
}

// planSummary is the plain summary of the plan waiting for confirmation.
func (m *Model) planSummary() text.Summary {
	return text.TUIPlanSummary(m.v2.plan, m.app.AgentName)
}

// layoutPlan gives the technical detail the room the summary, the header
// and the footer leave.
func (m *Model) layoutPlan() {
	w := max(40, m.width)
	summary := lipgloss.Height(m.renderSummary(w, m.planSummary()))
	room := m.height - 1 - summary - 2 - 1 - lipgloss.Height(m.renderFooter())
	m.v2.planView.SetWidth(max(30, w-4))
	m.v2.planView.SetHeight(max(3, room))
}

func (m *Model) handlePlanKey(k string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k {
	case "v":
		m.details = !m.details
		m.layoutPlan()
		return m, nil
	case "s", "y", "enter":
		if m.busy {
			return m, nil
		}
		m.busy = true
		m.mode = modeNormal
		m.status = text.TUIApplying
		return m, applyPlanCmd(m.app, m.v2.plan, m.ask())
	case "n", "esc":
		m.mode = modeNormal
		m.status = sentence(text.NotConfirmed)
		m.stopChain()
		if m.v2.plan.Request.Action == app.ActionInstall {
			m.discardRevision()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.v2.planView, cmd = m.v2.planView.Update(msg)
	return m, cmd
}

func (m *Model) handleApplied(msg appliedMsg) tea.Cmd {
	m.busy = false
	res := msg.res
	switch {
	case res.SnapshotID != "":
		// The operation ran: an error after it is only a warning (A4 of L51).
		// The mode is left as the user has it now.
		m.status = text.TUIDone(res, m.app.AgentName)
		m.last = lastOp{why: text.TUIZUndone}
		if msg.req.Action != app.ActionUndo {
			m.status = text.TUIWithUndo(m.status)
			m.last = lastOp{snapshot: res.SnapshotID}
		}
		if msg.err != nil {
			m.status += "\n" + strings.TrimSpace(text.SupersedeWarning(msg.err))
		}
		if msg.req.Action == app.ActionInstall {
			m.v2.revision = app.Revision{}
		}
	case errors.Is(msg.err, app.ErrPlanChanged) && res.Proposal.PlanID != "" && m.fresh(msg.id):
		m.showPlan(res.Proposal)
		m.status = sentence(text.PlanChanged(res.Proposal.PlanID))
		return nil
	case errors.Is(msg.err, app.ErrPlanChanged) && res.Proposal.PlanID != "":
		// Nothing ran, and the user left: the new plan is not shown.
		m.dropStale()
		if msg.req.Action == app.ActionInstall {
			m.discardRevision()
		}
	default:
		m.status = explainManage(msg.err, msg.req)
		if msg.req.Action == app.ActionInstall {
			m.discardRevision()
		}
	}
	cmds := []tea.Cmd{loadDataCmd(m.app)}
	if m.v2.findingsLoaded {
		cmds = append(cmds, findingsCmd(m.app))
	}
	if res.SnapshotID != "" {
		cmds = append(cmds, m.nextInChain(msg.id))
	} else {
		m.stopChain()
	}
	return tea.Batch(cmds...)
}

// explainManage tells a refusal of a version 2 operation, or any other
// error, in Spanish.
func explainManage(err error, req app.Request) string {
	if msg, ok := text.TUIRefusal(err, req); ok {
		return sentence(msg)
	}
	return ExplainError(err)
}

// handleManageSkillsKey handles the write keys of the Skills screen; ok is
// false for any other key. A key runs only when the selected row's panel
// offers it: any other is told why in plain words, never planned to fail
// (L83).
func (m *Model) handleManageSkillsKey(k string) (tea.Cmd, bool) {
	switch k {
	case "d", "e", "a", "o", "p", "l":
	default:
		return nil, false
	}
	if m.busy {
		return nil, true
	}
	if k == "a" {
		// A disabled skill's row names the key; any other row asks for it.
		if key := m.selectedParked(); key != "" {
			return m.startPlan(app.Request{Action: app.ActionEnable, Key: key}), true
		}
		m.openPrompt()
		return nil, true
	}
	row, ok := m.selectedRow()
	if !ok || row.ParkedKey != "" {
		return nil, true
	}
	if row.Ignored {
		m.status = text.TUIIgnoredRow(row.RawName)
		return nil, true
	}
	if m.actions == nil && m.actionsPending {
		m.status = text.TUIReadingActions
		return nil, true
	}
	acts := m.actions[row.ID]
	if !m.offers(row, k) {
		m.status = notOffered(k, acts, m.actions != nil)
		return nil, true
	}
	switch k {
	case "d":
		return m.startPlan(app.Request{Action: app.ActionDisable, Key: acts.Key}), true
	case "e":
		return m.startPlan(app.Request{Action: app.ActionRemove, Key: acts.Key}), true
	case "o":
		return m.startPlan(app.Request{Action: app.ActionAdoptOff}), true
	case "p":
		fix, _ := rowFix(acts)
		return m.startPlan(app.Request{Action: app.ActionRepair, Finding: fix.Finding.ID, Repair: fix.Repair}), true
	case "l":
		return m.openChecks(acts.Key), true
	}
	return nil, true
}

// keyActions are the store actions of the Skills screen's keys.
var keyActions = map[string]app.Action{
	"d": app.ActionDisable, "e": app.ActionRemove, "l": app.ActionLink,
}

// notOffered tells why the key does nothing on a skill with acts; known is
// false when the app could not tell what the skill admits.
func notOffered(k string, acts app.SkillActions, known bool) string {
	switch k {
	case "o":
		return text.TUINotOff
	case "p":
		switch {
		case len(acts.BrokenLinks) == 0:
			return text.TUINoBrokenLink
		case fixedByAdoptOff(acts):
			return text.TUIFixWithOff
		}
		return text.TUIFixInDoctor
	}
	action := keyActions[k]
	switch {
	case acts.Off:
		return text.TUIOffOnly(action)
	case known && acts.Key == "":
		return text.TUIOutsideStore(action)
	}
	return text.TUICannotNow(action)
}

// selectedParked is the key of the disabled skill on the selected row, or
// "" when the row is no disabled skill.
func (m *Model) selectedParked() string {
	idx := m.skillsTable.Cursor()
	if idx < 0 || idx >= len(m.skillRows) {
		return ""
	}
	return m.skillRows[idx].ParkedKey
}

// openPrompt asks the key of the disabled skill to enable.
func (m *Model) openPrompt() {
	m.v2.promptAction = app.ActionEnable
	m.v2.promptKey = ""
	m.v2.prompt.Prompt = text.TUIPromptEnable
	m.v2.prompt.SetValue("")
	m.v2.prompt.Focus()
	m.mode = modePrompt
}

func (m *Model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		val := strings.TrimSpace(m.v2.prompt.Value())
		if m.busy || val == "" {
			return m, nil
		}
		m.v2.prompt.Blur()
		m.mode = modeNormal
		return m, m.startPlan(app.Request{Action: app.ActionEnable, Key: val})
	case "esc":
		m.v2.prompt.Blur()
		m.mode = modeNormal
		return m, nil
	}
	var cmd tea.Cmd
	m.v2.prompt, cmd = m.v2.prompt.Update(msg)
	return m, cmd
}

// undoManaged plans the undo of a version 2 journal line.
func (m *Model) undoManaged(row HistoryRow) tea.Cmd {
	return m.startPlan(app.Request{Action: app.ActionUndo, Snapshot: row.SnapshotID})
}

// handleInstallKey handles the Install screen outside its prompts.
func (m *Model) handleInstallKey(k string) (tea.Model, tea.Cmd) {
	if k == "enter" && !m.busy {
		m.v2.source.SetValue("")
		m.v2.source.Focus()
		m.mode = modeInstallSource
	}
	return m, nil
}

func (m *Model) handleInstallInputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeInstallTargets {
		return m.handleInstallChecksKey(keyOf(msg))
	}
	input := &m.v2.source
	switch msg.String() {
	case "enter":
		if m.busy {
			return m, nil
		}
		src := strings.TrimSpace(input.Value())
		if src == "" {
			return m, nil
		}
		input.Blur()
		m.mode = modeNormal
		m.busy = true
		m.status = text.TUIResolving
		return m, resolveCmd(m.app, src, m.ask())
	case "esc":
		input.Blur()
		m.mode = modeNormal
		m.discardRevision()
		return m, nil
	}
	var cmd tea.Cmd
	*input, cmd = input.Update(msg)
	return m, cmd
}

// handleInstallChecksKey handles step 3 of an install: the agent check
// boxes, then the plan of the marked agents.
func (m *Model) handleInstallChecksKey(k string) (tea.Model, tea.Cmd) {
	if m.v2.checks.moveOrMark(k) {
		return m, nil
	}
	switch k {
	case "enter":
		if m.busy {
			return m, nil
		}
		return m, m.startPlanInstall(m.v2.checks.chosen())
	case "esc":
		m.mode = modeNormal
		m.discardRevision()
	}
	return m, nil
}

func (m *Model) startPlanInstall(targets []string) tea.Cmd {
	m.busy = true
	m.mode = modeNormal
	m.status = text.TUIPlanning
	return planInstallCmd(m.app, m.v2.revision, targets, m.ask())
}

func (m *Model) handleResolved(msg resolvedMsg) {
	m.busy = false
	if !m.fresh(msg.id) {
		m.dropStale()
		if msg.err == nil {
			m.v2.revision = msg.rev
			m.discardRevision()
		}
		return
	}
	if msg.err != nil {
		m.status = explainManage(msg.err, app.Request{Action: app.ActionInstall})
		return
	}
	m.status = ""
	m.v2.revision = msg.rev
	m.v2.review.SetContent(msg.review)
	m.v2.review.GotoTop()
	m.v2.checks = installChecks(msg.targets, msg.readers)
	m.mode = modeInstallReview
}

func (m *Model) handleInstallReviewKey(k string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k {
	case "enter":
		m.mode = modeInstallTargets
		return m, nil
	case "esc":
		m.mode = modeNormal
		m.discardRevision()
		return m, nil
	}
	var cmd tea.Cmd
	m.v2.review, cmd = m.v2.review.Update(msg)
	return m, cmd
}

// openDoctor shows the Doctor screen and looks for findings, unless an
// operation runs: they are looked for again when it ends.
func (m *Model) openDoctor() tea.Cmd {
	m.screen = ScreenDoctor
	if m.busy {
		return nil
	}
	return m.search()
}

// search looks for Doctor's findings.
func (m *Model) search() tea.Cmd {
	m.v2.searching = true
	m.status = text.TUISearching
	return findingsCmd(m.app)
}

// searchIfWaiting looks for the findings when Doctor waits for them: it was
// opened before the data load, which it needs, and nothing looks for them
// yet. It starts nothing while recover is offered or an operation runs.
func (m *Model) searchIfWaiting() tea.Cmd {
	if m.screen != ScreenDoctor || !m.loaded || m.mode == modeRecover || m.busy ||
		m.v2.findingsLoaded || m.v2.searching {
		return nil
	}
	return m.search()
}

func (m *Model) handleFindings(msg findingsMsg) {
	m.v2.searching = false
	if m.status == text.TUISearching {
		m.status = ""
	}
	if msg.err != nil {
		m.status = explainManage(msg.err, app.Request{Action: app.ActionRepair})
		return
	}
	m.v2.findings = msg.findings
	groups, _ := doctorGroups(msg.findings)
	m.v2.doctorFindings = slices.Concat(groups...)
	m.v2.findingsLoaded = true
	m.refreshFindings()
}

func (m *Model) handleDoctorKey(k string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k {
	case "enter":
		idx := m.v2.findingsTable.Cursor()
		if m.busy || idx < 0 || idx >= len(m.v2.doctorFindings) {
			return m, nil
		}
		m.busy = true
		m.status = text.TUIReadRepairs
		return m, repairsCmd(m.app, m.v2.doctorFindings[idx], m.ask())
	}
	var cmd tea.Cmd
	m.v2.findingsTable, cmd = m.v2.findingsTable.Update(msg)
	return m, cmd
}

func (m *Model) handleRepairs(msg repairsMsg) {
	m.busy = false
	if !m.fresh(msg.id) {
		m.dropStale()
		return
	}
	m.status = ""
	if msg.err != nil {
		m.status = explainManage(msg.err, app.Request{Action: app.ActionRepair, Finding: msg.finding.ID})
		return
	}
	if len(msg.offer.Repairs) == 0 {
		m.status = sentence(text.NoRepair(msg.offer))
		return
	}
	m.v2.finding = msg.finding
	m.v2.offer = msg.offer
	m.v2.cursor = 0
	m.mode = modeRepairs
}

// handleChooseKey moves through the repairs, or the canonical folders, and
// plans the one chosen.
func (m *Model) handleChooseKey(k string) (tea.Model, tea.Cmd) {
	items := m.v2.offer.Repairs
	if m.mode == modeCanonical {
		items = m.v2.finding.Paths
	}
	switch k {
	case "up", "k":
		if m.v2.cursor > 0 {
			m.v2.cursor--
		}
	case "down", "j":
		if m.v2.cursor < len(items)-1 {
			m.v2.cursor++
		}
	case "esc":
		m.mode = modeNormal
	case "enter":
		if m.busy || m.v2.cursor >= len(items) {
			return m, nil
		}
		req := app.Request{Action: app.ActionRepair, Finding: m.v2.finding.ID}
		if m.mode == modeCanonical {
			req.Repair, req.Canonical = m.v2.repair, items[m.v2.cursor]
		} else {
			m.v2.repair = items[m.v2.cursor]
			req.Repair = m.v2.repair
		}
		m.mode = modeNormal
		return m, m.startPlan(req)
	}
	return m, nil
}

// refreshFindings gives the cursor of the Doctor list its rows.
func (m *Model) refreshFindings() {
	m.v2.findingsTable.SetColumns([]table.Column{{Title: text.TUIColFinding, Width: max(10, m.width-4)}})
	rows := make([]table.Row, 0, len(m.v2.doctorFindings))
	for _, f := range m.v2.doctorFindings {
		rows = append(rows, table.Row{f.ID})
	}
	m.v2.findingsTable.SetRows(rows)
	// An empty table leaves its cursor at -1; keep it on a row.
	if c := m.v2.findingsTable.Cursor(); c < 0 || c >= len(rows) {
		m.v2.findingsTable.SetCursor(max(0, min(c, len(rows)-1)))
	}
}

func (m *Model) resizeManage() {
	w := max(40, m.width)
	m.v2.findingsTable.SetWidth(w)
	m.v2.findingsTable.SetHeight(max(6, m.height-9))
	m.v2.review.SetWidth(max(30, w-8))
	// The install summary takes four rows above the review.
	m.v2.review.SetHeight(max(6, m.height-16))
	m.layoutPlan()
	m.refreshFindings()
}

// PresentChoices renders a list with the cursor at cursor.
func PresentChoices(items []string, cursor int) string {
	var b strings.Builder
	for i, it := range items {
		mark := "  "
		if i == cursor {
			mark = "> "
		}
		fmt.Fprintf(&b, "%s%s\n", mark, it)
	}
	return b.String()
}

// renderPlan is the confirmation of a plan: its plain summary and, on
// request, the technical detail the CLI prints, with the identifier the
// approval binds.
func (m *Model) renderPlan(boxW int) string {
	out := m.renderSummary(boxW, m.planSummary())
	switch {
	case m.v2.plan.Request.Action == app.ActionInstall:
		// The plan of an install is its last step.
		out = m.styles.title.Render(text.TUIInstallStep(4)) + "\n" + out
	case m.v2.chain.total > 1:
		out = m.styles.title.Render(text.TUIChainStep(m.v2.chain.step(), m.v2.chain.total)) + "\n" + out
	}
	if !m.details {
		return out
	}
	return out + "\n" + m.styles.frame.Width(boxW).Render(m.v2.planView.View()) + "\n" +
		m.styles.muted.Width(boxW).Render(text.TUIDetailsHelp)
}

func (m *Model) renderPrompt(boxW int) string {
	title := strings.TrimSpace(text.Title(app.Proposal{Request: app.Request{Action: m.v2.promptAction, Key: m.v2.promptKey, Harness: "…"}}))
	body := title + "\n\n" + m.v2.prompt.View() + "\n\n" + text.TUIPromptHint
	return m.styles.box.Width(boxW).Render(body)
}

func planCmd(a *app.App, req app.Request, id int) tea.Cmd {
	return func() tea.Msg {
		p, err := a.Plan(context.Background(), req)
		return plannedMsg{id: id, req: req, p: p, err: err}
	}
}

func planInstallCmd(a *app.App, rev app.Revision, targets []string, id int) tea.Cmd {
	return func() tea.Msg {
		p, err := a.PlanInstall(context.Background(), rev, targets)
		req := p.Request
		if err != nil {
			req = app.Request{Action: app.ActionInstall, Install: &app.InstallRequest{Revision: rev, Targets: targets}}
		}
		return plannedMsg{id: id, req: req, p: p, err: err}
	}
}

// applyPlanCmd runs p only if planning its request again gives the
// identifier shown.
func applyPlanCmd(a *app.App, p app.Proposal, id int) tea.Cmd {
	return func() tea.Msg {
		res, err := a.ApplyPlan(context.Background(), p.Request, p.PlanID)
		return appliedMsg{id: id, req: p.Request, res: res, err: err}
	}
}

// resolveCmd resolves and prepares source, then reads its review. A
// prepared copy whose review cannot be read is discarded at once.
func resolveCmd(a *app.App, source string, id int) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		rev, err := a.ResolveInstall(ctx, source)
		if err != nil {
			return resolvedMsg{id: id, err: err}
		}
		fail := func(err error) tea.Msg {
			return resolvedMsg{id: id, err: errors.Join(err, a.DiscardRevision(rev))}
		}
		diffs, err := a.RevisionDiff(ctx, rev)
		if err != nil {
			return fail(err)
		}
		targets, err := a.ProposeTargets(ctx)
		if err != nil {
			return fail(err)
		}
		ch, err := a.AgentChoices(ctx, "")
		if err != nil {
			return fail(err)
		}
		return resolvedMsg{id: id, rev: rev, review: text.InstallReview(rev, diffs), targets: targets, readers: ch.ReadStore}
	}
}

func findingsCmd(a *app.App) tea.Cmd {
	return func() tea.Msg {
		findings, err := a.Doctor(context.Background())
		return findingsMsg{findings: findings, err: err}
	}
}

func repairsCmd(a *app.App, f app.Finding, id int) tea.Cmd {
	return func() tea.Msg {
		o, err := a.Repairs(context.Background(), f.ID)
		return repairsMsg{id: id, finding: f, offer: o, err: err}
	}
}
