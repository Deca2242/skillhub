package tui

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/updates"
)

type viewMode int

const (
	modeNormal viewMode = iota
	modeRecover
	modeFilter
	modeSkillDetail
	modeSkillDocument
	modeDiff
	modeConfirmApply
	modeConfirmApplyAll
	modeConfirmUndo
	modeEditInterval
	modeHelp
	// The version 2 modes (manage.go).
	modePrompt
	modePlan
	modeInstallSource
	modeInstallReview
	modeInstallTargets
	modeRepairs
	modeCanonical
	// The UX3 modes (nav.go, checks.go).
	modeAgentFilter
	modeCompareAgents
	modeCompareSkills
	modeChecks
	// modeUnignore shows the check boxes of the ignored skills (UX4).
	modeUnignore
)

type dataLoadedMsg struct {
	hasOp bool
	op    domain.Operation
	// managedPending reports a version 2 operation record pending: the TUI
	// offers recover, as for an interrupted operation of version 1.
	managedPending bool
	parked         []app.ParkedSkill
	visibility     map[string][]string
	sightings      map[string][]app.Sighting
	skills         []domain.Skill
	ignored        []app.IgnoredSkill
	candidates     []domain.UpdateCandidate
	lastCheck      time.Time
	unchecked      []app.UncheckedSkill
	history        []domain.HistoryEntry
	operations     map[string]app.OperationView
	settings       domain.Settings
	bgStatus       ports.BackgroundStatus
	err            error
}

type checkFinishedMsg struct {
	res        app.CheckResult
	candidates []domain.UpdateCandidate
	err        error
}

type diffLoadedMsg struct {
	candidate domain.UpdateCandidate
	review    app.ReviewDiff
	err       error
}

type applyFinishedMsg struct {
	result       updates.ApplyResult
	newCandidate *domain.UpdateCandidate
	err          error
}

type applyAllFinishedMsg struct {
	results []updates.ApplyResult
}

type skipFinishedMsg struct {
	err error
}

type undoFinishedMsg struct {
	id     int
	req    updates.UndoRequest
	result updates.UndoResult
	err    error
}

type recoverFinishedMsg struct {
	rec app.Recovery
	err error
}

type settingsSavedMsg struct {
	status string
	err    error
}

// Model is the Bubble Tea v2 model for skillhub's terminal UI.
type Model struct {
	app    *app.App
	styles styles

	width  int
	height int

	screen viewModeScreen
	mode   viewMode
	loaded bool
	// loadFailed reports that the initial data could not be read.
	loadFailed bool
	busy       bool
	status     string

	interruptedOp domain.Operation
	// managedPending is set while the recover screen offers to resolve a
	// version 2 operation rather than interruptedOp.
	managedPending bool

	skills []domain.Skill
	// parked are the disabled skills, listed after skills on the Skills
	// screen so that A enables one from its row.
	parked      []app.ParkedSkill
	skillRows   []SkillRow
	skillsTable table.Model
	// visibility names, per skill ID, the harnesses that see it; nil when
	// the app could not tell, and the panel falls back to the locations.
	visibility map[string][]string
	sightings  map[string][]app.Sighting
	// actions are, per skill ID, what the app can plan for it now: the
	// panel offers only those (L83). nil when the app could not tell, and
	// then no write action is offered.
	actions map[string]app.SkillActions
	// health counts the problems Salud can repair, gentle-ai's left out;
	// -1 until the overview of the load arrives.
	health int
	// actionsSeq numbers the loads of actions: only the latest is kept.
	// actionsPending is set while one is read.
	actionsSeq     int
	actionsPending bool
	// listTop is the first row the Skills list shows.
	listTop int
	// details shows the technical detail of the confirmation on screen,
	// under its plain summary (S13).
	details       bool
	filterInput   textinput.Model
	selectedSk    domain.Skill
	docView       app.SkillDocument
	docScroll     int
	docReturnMode viewMode
	// allRows are the Skills rows before the filters; skillRows are the
	// ones shown. stateFilter (f) and agentFilter (g, a harness id) keep
	// only some of them.
	allRows     []SkillRow
	stateFilter text.SkillFilter
	agentFilter string
	// agentCursor is the cursor of the agent filter's list.
	agentCursor         int
	compareFirst        string
	compareA            string
	compareB            string
	compareSingle       bool
	compareScroll       int
	skillComp           app.SkillComparison
	skillCompScroll     int
	skillCompReturnMode viewMode
	overviewFindings    []app.Finding
	// back is the tab Historial or Ajustes return to with esc.
	back Screen
	// last is the operation z undoes.
	last lastOp

	candidates   []domain.UpdateCandidate
	lastCheck    time.Time
	unchecked    []app.UncheckedSkill
	updateRows   []UpdateRow
	updatesTable table.Model
	updatesTop   int

	activeCandidate domain.UpdateCandidate
	activeReview    app.ReviewDiff
	diffView        DiffView
	diffViewport    viewport.Model

	applyAllPlan ApplyAllPlan

	history      []domain.HistoryEntry
	historyRows  []HistoryRow
	historyTable table.Model
	historyTop   int
	// operations tells, by snapshot, what each version 2 journal line did.
	operations map[string]app.OperationView

	pendingUndoReq   updates.UndoRequest
	pendingUndoSaved string

	settings       domain.Settings
	bgStatus       ports.BackgroundStatus
	settingsCursor int
	intervalInput  textinput.Model
	// ignored are the skills the ignore setting hides, and unignore the
	// check boxes Ajustes shows of them (UX4).
	ignored  []app.IgnoredSkill
	unignore checks

	v2 manage
}

type viewModeScreen = Screen

// NewModel constructs the TUI model over the application facade.
func NewModel(a *app.App) *Model {
	fi := textinput.New()
	fi.Prompt = msgFilterPrompt
	fi.CharLimit = 100

	ii := textinput.New()
	ii.Prompt = "Nuevo intervalo: "
	ii.Placeholder = "1h"
	ii.CharLimit = 20

	skTable, upTable, hiTable := newList(), newList(), newList()

	vp := viewport.New(viewport.WithWidth(94), viewport.WithHeight(16))

	m := &Model{
		app:           a,
		styles:        defaultStyles(),
		width:         100,
		height:        30,
		screen:        ScreenSkills,
		mode:          modeNormal,
		skillsTable:   skTable,
		updatesTable:  upTable,
		historyTable:  hiTable,
		filterInput:   fi,
		intervalInput: ii,
		diffViewport:  vp,
		v2:            newManage(),
		health:        -1,
	}
	m.resizeComponents()
	m.refreshTables()
	return m
}

// ActiveScreen returns the currently active screen.
func (m *Model) ActiveScreen() Screen { return m.screen }

// Init loads the initial state from the facade.
func (m *Model) Init() tea.Cmd {
	return loadDataCmd(m.app)
}

// Update handles Bubble Tea messages and user input.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if next, cmd, ok := m.updateManage(msg); ok {
		return next, cmd
	}
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.styles = newStyles(msg.IsDark())
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeComponents()
		return m, nil

	case dataLoadedMsg:
		m.handleDataLoaded(msg)
		cmd := m.searchIfWaiting()
		if msg.err == nil && !msg.managedPending && !msg.hasOp {
			// What each skill admits is read after the list is shown: it
			// asks every planner, and the list need not wait for it.
			m.actionsSeq++
			m.actionsPending = true
			cmd = tea.Batch(cmd, actionsCmd(m.app, m.actionsSeq))
		}
		return m, cmd

	case actionsLoadedMsg:
		if msg.seq == m.actionsSeq {
			m.actions = msg.actions
			m.health = msg.health
			m.overviewFindings = msg.findings
			m.actionsPending = false
			// The problems filter and the summary count broken links.
			m.refreshTables()
		}
		return m, nil

	case checkFinishedMsg:
		return m.handleCheckFinished(msg)

	case diffLoadedMsg:
		return m.handleDiffLoaded(msg), nil

	case applyFinishedMsg:
		return m.handleApplyFinished(msg)

	case applyAllFinishedMsg:
		m.busy = false
		m.mode = modeNormal
		m.status = PresentApplyAllResults(msg.results)
		m.last = lastOp{why: text.TUIZApplyAll}
		return m, loadDataCmd(m.app)

	case skipFinishedMsg:
		m.busy = false
		m.mode = modeNormal
		if msg.err != nil {
			m.status = ExplainError(msg.err)
		} else {
			m.status = "Actualización omitida."
		}
		return m, loadDataCmd(m.app)

	case undoFinishedMsg:
		return m.handleUndoFinished(msg)

	case recoverFinishedMsg:
		m.busy = false
		if msg.err != nil {
			m.status = explainRecover(msg.rec, msg.err)
			return m, nil
		}
		m.mode = modeNormal
		m.managedPending = false
		m.status = strings.TrimSpace(text.Recovered(msg.rec))
		return m, loadDataCmd(m.app)

	case settingsSavedMsg:
		m.busy = false
		if msg.err != nil {
			m.status = ExplainError(msg.err)
			return m, nil
		}
		m.intervalInput.Blur()
		m.mode = modeNormal
		m.status = msg.status
		return m, loadDataCmd(m.app)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleDataLoaded(msg dataLoadedMsg) *Model {
	if msg.err != nil {
		m.status = ExplainError(msg.err)
		m.loadFailed = true
		return m
	}
	m.loaded = true
	m.loadFailed = false
	m.managedPending = msg.managedPending
	if msg.managedPending || msg.hasOp {
		m.interruptedOp = msg.op
		m.mode = modeRecover
		return m
	}
	if m.mode == modeRecover {
		m.mode = modeNormal
	}
	m.skills = msg.skills
	m.ignored = msg.ignored
	m.parked = msg.parked
	m.visibility = msg.visibility
	m.sightings = msg.sightings
	m.candidates = msg.candidates
	m.lastCheck = msg.lastCheck
	m.unchecked = msg.unchecked
	m.history = msg.history
	m.operations = msg.operations
	m.settings = msg.settings
	m.bgStatus = msg.bgStatus
	m.refreshTables()
	return m
}

func (m *Model) handleCheckFinished(msg checkFinishedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	if msg.err != nil {
		m.status = ExplainError(msg.err)
		return m, loadDataCmd(m.app)
	}
	if (m.mode == modeDiff || m.mode == modeConfirmApply) && m.activeCandidate.ID != "" {
		foundSame := slices.ContainsFunc(msg.candidates, func(c domain.UpdateCandidate) bool {
			return c.ID == m.activeCandidate.ID && c.ToSHA == m.activeCandidate.ToSHA && c.State == domain.CandidatePending
		})
		if !foundSame {
			m.status = msgErrCandidateReplaced
			var newer *domain.UpdateCandidate
			for i := range msg.candidates {
				if msg.candidates[i].Target == m.activeCandidate.Target && msg.candidates[i].State == domain.CandidatePending {
					newer = &msg.candidates[i]
					break
				}
			}
			if newer != nil {
				m.activeCandidate = *newer
				m.mode = modeDiff
				return m, tea.Batch(loadDataCmd(m.app), loadDiffCmd(m.app, *newer))
			}
			m.mode = modeNormal
			return m, loadDataCmd(m.app)
		}
		m.status = "Revisión completada."
	} else if msg.res.Failed {
		m.status = "Revisión completada con errores en algún destino."
	} else {
		m.status = "Revisión completada."
	}
	return m, loadDataCmd(m.app)
}

func (m *Model) handleDiffLoaded(msg diffLoadedMsg) *Model {
	m.busy = false
	if msg.err != nil {
		m.status = ExplainError(msg.err)
		return m
	}
	m.activeCandidate = msg.candidate
	m.activeReview = msg.review
	m.diffView = DiffViewUser
	m.mode = modeDiff
	m.showDiff()
	return m
}

// showDiff puts the active review, in the chosen view, in the viewport.
func (m *Model) showDiff() {
	m.diffViewport.SetContent(PresentDiff(m.activeCandidate, m.activeReview, m.diffView))
	m.diffViewport.GotoTop()
}

func (m *Model) handleApplyFinished(msg applyFinishedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	isReplaced := errors.Is(msg.err, updates.ErrCandidateNotFound) ||
		errors.Is(msg.err, updates.ErrNotPending) ||
		errors.Is(msg.err, updates.ErrApprovalMismatch)
	if isReplaced && msg.newCandidate != nil {
		m.status = ExplainError(msg.err)
		m.activeCandidate = *msg.newCandidate
		m.mode = modeDiff
		return m, tea.Batch(loadDataCmd(m.app), loadDiffCmd(m.app, *msg.newCandidate))
	}
	m.mode = modeNormal
	switch {
	case msg.err != nil:
		m.status = ExplainError(msg.err)
	case msg.result.Outcome == updates.OutcomeRejected:
		m.status = msgErrRejected
	default:
		m.status = text.TUIWithUndo(text.TUIUpdated(targetName(msg.result.Target)))
		m.last = lastOp{update: updates.UndoRequest{Target: msg.result.Target, SnapshotID: msg.result.SnapshotID}}
	}
	return m, loadDataCmd(m.app)
}

func (m *Model) handleUndoFinished(msg undoFinishedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	if errors.Is(msg.err, updates.ErrUndoNeedsConfirmation) && !m.fresh(msg.id) {
		// The user left Historial: the confirmation is not asked, so that
		// a key typed for another screen never undoes anything.
		m.dropStale()
		return m, loadDataCmd(m.app)
	}
	if errors.Is(msg.err, updates.ErrUndoNeedsConfirmation) {
		m.pendingUndoReq = updates.UndoRequest{
			Target:     msg.req.Target,
			SnapshotID: msg.result.SnapshotID,
			Confirm:    true,
		}
		m.pendingUndoSaved = msg.result.SavedSnapshotID
		m.details = false
		m.mode = modeConfirmUndo
		return m, nil
	}
	if msg.err != nil {
		m.status = ExplainError(msg.err)
		return m, loadDataCmd(m.app)
	}
	m.status = text.TUIUpdateUndone(targetName(msg.req.Target))
	m.last = lastOp{why: text.TUIZUndone}
	return m, loadDataCmd(m.app)
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	k := keyOf(msg)
	if m.mode == modeRecover {
		if k == "q" {
			return m, tea.Quit
		}
		if (k == "r" || k == "enter") && !m.busy {
			m.busy = true
			m.status = msgRecovering
			return m, recoverCmd(m.app)
		}
		return m, nil
	}

	switch m.mode {
	case modeHelp:
		if k == "?" || k == "esc" || k == "q" {
			m.mode = modeNormal
			return m, nil
		}
		var cmd tea.Cmd
		m.v2.help, cmd = m.v2.help.Update(msg)
		return m, cmd
	case modeFilter:
		return m.handleFilterKey(msg)
	case modeEditInterval:
		return m.handleEditIntervalKey(msg)
	case modeSkillDetail:
		if k == keySkillDocument {
			return m.openSkillDocument(m.selectedSk, modeSkillDetail)
		}
		if k == keyCompareSkills {
			if first, second, ok := m.homonymPair(m.selectedSk.ID); ok {
				return m.openCompareSkills(first, second, modeSkillDetail)
			}
			return m, nil
		}
		if k == "esc" || k == "enter" || k == "q" {
			m.mode = modeNormal
		}
		return m, nil
	case modeSkillDocument:
		return m.handleDocumentKey(k)
	case modeDiff:
		return m.handleDiffKey(k, msg)
	case modeConfirmApply:
		return m.handleConfirmApplyKey(k)
	case modeConfirmApplyAll:
		return m.handleConfirmApplyAllKey(k)
	case modeConfirmUndo:
		return m.handleConfirmUndoKey(k)
	case modePrompt:
		return m.handlePromptKey(msg)
	case modePlan:
		return m.handlePlanKey(k, msg)
	case modeInstallSource, modeInstallTargets:
		return m.handleInstallInputKey(msg)
	case modeInstallReview:
		return m.handleInstallReviewKey(k, msg)
	case modeRepairs, modeCanonical:
		return m.handleChooseKey(k)
	case modeAgentFilter:
		return m.handleAgentFilterKey(k)
	case modeCompareAgents:
		return m.handleCompareKey(k)
	case modeCompareSkills:
		return m.handleCompareSkillsKey(k)
	case modeChecks:
		return m.handleChecksKey(k)
	case modeUnignore:
		return m.handleUnignoreKey(k)
	}

	switch k {
	case "q":
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
		m.layoutHelp()
		m.v2.help.GotoTop()
		return m, nil
	case "1", "2", "3", "4", "tab", "h", "c", "z":
		return m, m.handleNavKey(k)
	case "esc":
		if m.screen == ScreenHistory || m.screen == ScreenSettings {
			m.screen = m.back
			return m, nil
		}
	case "r":
		if m.loaded && !m.busy {
			m.busy = true
			m.status = msgChecking
			return m, checkNowCmd(m.app)
		}
		return m, nil
	}

	if !m.loaded {
		return m, nil
	}

	switch m.screen {
	case ScreenSkills:
		return m.handleSkillsKey(k, msg)
	case ScreenUpdates:
		return m.handleUpdatesKey(k, msg)
	case ScreenHistory:
		return m.handleHistoryKey(k, msg)
	case ScreenSettings:
		return m.handleSettingsKey(k)
	case ScreenInstall:
		return m.handleInstallKey(k)
	case ScreenDoctor:
		return m.handleDoctorKey(k, msg)
	}
	return m, nil
}

func (m *Model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filterInput.Blur()
		m.mode = modeNormal
		return m, nil
	case "esc":
		m.filterInput.SetValue("")
		m.filterInput.Blur()
		m.mode = modeNormal
		m.refreshTables()
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m.refreshTables()
	return m, cmd
}

func (m *Model) handleEditIntervalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if m.busy {
			return m, nil
		}
		val := strings.TrimSpace(m.intervalInput.Value())
		m.busy = true
		return m, saveSettingCmd(m.app, app.KeyCheckInterval, val, msgSettingsSaved)
	case "esc":
		m.intervalInput.Blur()
		m.mode = modeNormal
		return m, nil
	}
	var cmd tea.Cmd
	m.intervalInput, cmd = m.intervalInput.Update(msg)
	return m, cmd
}

func (m *Model) handleDiffKey(k string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k {
	case "esc":
		m.mode = modeNormal
		return m, nil
	case "v":
		if m.activeReview.Split() {
			m.diffView = m.diffView.Toggle()
			m.showDiff()
		}
		return m, nil
	case "a":
		if m.busy {
			return m, nil
		}
		if m.activeCandidate.State != domain.CandidatePending {
			m.status = fmt.Sprintf(msgBlockedNoApply, text.SafeLine(m.activeCandidate.ID), text.BlockReason(m.activeCandidate.BlockReason))
			return m, nil
		}
		m.details = false
		m.mode = modeConfirmApply
		return m, nil
	case "o":
		if m.activeCandidate.State == domain.CandidatePending && !m.busy {
			m.busy = true
			m.status = msgSkipping
			return m, skipCmd(m.app, updates.Approval{
				CandidateID: m.activeCandidate.ID,
				ToSHA:       m.activeCandidate.ToSHA,
			})
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.diffViewport, cmd = m.diffViewport.Update(msg)
	return m, cmd
}

func (m *Model) handleConfirmApplyKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "v":
		m.details = !m.details
		return m, nil
	case "s", "y", "enter":
		if m.busy {
			return m, nil
		}
		m.busy = true
		m.mode = modeNormal
		m.status = msgApplying
		return m, applyOneCmd(m.app, updates.Approval{
			CandidateID: m.activeCandidate.ID,
			ToSHA:       m.activeCandidate.ToSHA,
		})
	case "n", "esc":
		m.mode = modeNormal
		return m, nil
	}
	return m, nil
}

func (m *Model) handleConfirmApplyAllKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "v":
		m.details = !m.details
		return m, nil
	case "s", "y", "enter":
		if m.busy {
			return m, nil
		}
		aps := m.applyAllPlan.Approvals()
		m.busy = true
		m.mode = modeNormal
		m.status = msgApplyingAll
		return m, applyAllCmd(m.app, aps)
	case "n", "esc":
		m.mode = modeNormal
		return m, nil
	}
	return m, nil
}

func (m *Model) handleConfirmUndoKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "v":
		m.details = !m.details
		return m, nil
	case "s", "y", "enter":
		if m.busy {
			return m, nil
		}
		m.busy = true
		m.mode = modeNormal
		m.status = msgUndoing
		return m, undoCmd(m.app, m.pendingUndoReq, m.ask())
	case "n", "esc":
		m.mode = modeNormal
		return m, nil
	}
	return m, nil
}

func (m *Model) handleSkillsKey(k string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handleManageSkillsKey(k); ok {
		return m, cmd
	}
	switch k {
	case "/":
		m.mode = modeFilter
		m.filterInput.Focus()
		return m, nil
	case "f":
		m.nextFilter()
		return m, nil
	case "g":
		m.openAgentFilter()
		return m, nil
	case "enter":
		if sk, ok := m.selectedSkill(); ok {
			m.selectedSk = sk
			m.mode = modeSkillDetail
		}
		return m, nil
	case keySkillDocument:
		if sk, ok := m.selectedSkill(); ok {
			m.selectedSk = sk
			return m.openSkillDocument(sk, modeNormal)
		}
		return m, nil
	case "x":
		if row, ok := m.selectedRow(); ok && row.Ignored && !m.busy {
			return m, m.unignoreSkill(row.ID)
		}
		if sk, ok := m.selectedSkill(); ok && !m.busy {
			if strings.Contains(sk.Name, ",") {
				m.status = msgIgnoreComma
				return m, nil
			}
			m.busy = true
			return m, ignoreSkillCmd(m.app, m.settings.Ignore, sk.Name)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.skillsTable, cmd = m.skillsTable.Update(msg)
	return m, cmd
}

func (m *Model) handleUpdatesKey(k string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k {
	case "enter":
		if c, ok := m.selectedCandidate(); ok && !m.busy {
			m.busy = true
			m.status = msgLoadingDiff
			return m, loadDiffCmd(m.app, c)
		}
		return m, nil
	case "a":
		if m.busy {
			return m, nil
		}
		if c, ok := m.selectedCandidate(); ok {
			if c.State != domain.CandidatePending {
				m.status = fmt.Sprintf(msgBlockedNoApply, text.SafeLine(c.ID), text.BlockReason(c.BlockReason))
				return m, nil
			}
			m.activeCandidate = c
			m.details = false
			m.mode = modeConfirmApply
		}
		return m, nil
	case "o":
		if c, ok := m.selectedCandidate(); ok && c.State == domain.CandidatePending && !m.busy {
			m.busy = true
			m.status = msgSkipping
			return m, skipCmd(m.app, updates.Approval{CandidateID: c.ID, ToSHA: c.ToSHA})
		}
		return m, nil
	case "t":
		if m.busy || len(m.candidates) == 0 {
			return m, nil
		}
		plan := BuildApplyAllPlan(m.candidates)
		if len(plan.Included) == 0 {
			m.status = "No hay actualizaciones elegibles para aplicar todas."
			return m, nil
		}
		m.applyAllPlan = plan
		m.details = false
		m.mode = modeConfirmApplyAll
		return m, nil
	}
	var cmd tea.Cmd
	m.updatesTable, cmd = m.updatesTable.Update(msg)
	return m, cmd
}

func (m *Model) handleHistoryKey(k string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k == "u" && !m.busy {
		idx := m.historyTable.Cursor()
		if idx >= 0 && idx < len(m.historyRows) {
			row := m.historyRows[idx]
			if row.CanUndo && strings.HasPrefix(row.Target, "v2:") {
				return m, m.undoManaged(row)
			}
			if row.CanUndo {
				m.busy = true
				m.status = msgUndoing
				return m, undoCmd(m.app, updates.UndoRequest{
					Target:     row.Target,
					SnapshotID: row.SnapshotID,
				}, m.ask())
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.historyTable, cmd = m.historyTable.Update(msg)
	return m, cmd
}

func (m *Model) handleSettingsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}
		return m, nil
	case "down", "j":
		if m.settingsCursor < settingIgnored {
			m.settingsCursor++
		}
		return m, nil
	case "enter", " ":
		switch m.settingsCursor {
		case 0:
			m.intervalInput.SetValue("")
			m.intervalInput.Focus()
			m.mode = modeEditInterval
			return m, nil
		case settingIgnored:
			m.openUnignore()
			return m, nil
		}
		if !m.busy {
			m.busy = true
			nextVal := strconv.FormatBool(!m.settings.Notifications)
			return m, saveSettingCmd(m.app, app.KeyNotifications, nextVal, msgSettingsSaved)
		}
		return m, nil
	}
	return m, nil
}

// View renders the current screen or modal.
func (m *Model) View() tea.View {
	boxW := max(20, m.width)
	if m.mode == modeRecover {
		screen := PresentInterrupted(m.interruptedOp)
		if m.managedPending {
			screen = PresentManagedPending()
		}
		content := m.styles.warnBox.Width(boxW).Render(screen)
		if m.status != "" {
			content += "\n" + m.status
		}
		return tea.NewView(content)
	}

	var body string
	switch m.mode {
	case modeHelp:
		body = m.renderHelp(boxW)
	case modeSkillDetail:
		activity := app.SkillActivity(m.history, m.operations, m.selectedSk)
		body = m.styles.box.Width(boxW).Render(PresentSkillDetailWithActivity(
			m.selectedSk,
			m.sightings[m.selectedSk.ID],
			m.app.AgentName,
			activity,
			m.app.Now(),
		))
	case modeSkillDocument:
		body = m.renderSkillDocument(boxW)
	case modeDiff:
		body = m.renderDiffView(boxW)
	case modeConfirmApply:
		body = m.renderConfirmApply(boxW)
	case modeConfirmApplyAll:
		body = m.renderApplyAll(boxW)
	case modeConfirmUndo:
		body = m.renderConfirmUndo(boxW)
	case modePrompt:
		body = m.renderPrompt(boxW)
	case modePlan:
		body = m.renderPlan(boxW)
	case modeRepairs, modeCanonical:
		body = m.renderChoose(boxW)
	case modeAgentFilter:
		body = m.renderAgentFilter(boxW)
	case modeCompareAgents:
		body = m.renderCompareAgents(boxW)
	case modeCompareSkills:
		body = m.renderCompareSkills(boxW)
	case modeChecks:
		body = m.renderChecks(boxW)
	case modeUnignore:
		body = m.renderUnignore(boxW)
	default:
		body = m.renderActiveScreen()
	}

	parts := []string{m.renderHeader(), body, m.renderFooter()}
	return tea.NewView(strings.Join(parts, "\n"))
}

func (m *Model) renderActiveScreen() string {
	if !m.loaded && m.screen != ScreenSettings {
		if m.loadFailed {
			return msgLoadFailed
		}
		return msgLoadingData
	}
	switch m.screen {
	case ScreenSkills:
		return m.renderSkills()
	case ScreenUpdates:
		return m.renderUpdates()
	case ScreenHistory:
		return m.renderHistory()
	case ScreenSettings:
		return m.renderSettings()
	case ScreenInstall:
		return m.renderInstall(max(20, m.width))
	case ScreenDoctor:
		return m.renderDoctor(max(20, m.width))
	default:
		return ""
	}
}

func (m *Model) renderDiffView(boxW int) string {
	help := text.TUIKeysDiff
	if m.activeReview.Split() {
		help = text.TUIKeysDiffSplit
	}
	return m.styles.box.Width(boxW).Render(m.diffViewport.View()) + "\n" + m.styles.muted.Width(boxW).Render(help)
}

func (m *Model) renderConfirmApply(boxW int) string {
	c := m.activeCandidate
	sum := text.TUIUpdateSummary(targetName(c.Target), len(c.FileChanges), isLocallyModified(c))
	if !m.details {
		return m.renderSummary(boxW, sum)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Destino:   %s\n", text.SafeLine(c.Target))
	fmt.Fprintf(&b, "Candidato: %s\n", text.SafeLine(c.ID))
	fmt.Fprintf(&b, "Árbol:     %s (commit %s)\n", ShortSHA(c.ToSHA), ShortSHA(c.ToCommit))
	fmt.Fprintf(&b, "Archivos:  %d\n", len(c.FileChanges))
	fmt.Fprintf(&b, "Etiquetas: %s\n", formatLabels(c.Labels))
	if isLocallyModified(c) {
		b.WriteString("\n" + msgLocallyModified + "\n")
	}
	return m.renderSummary(boxW, sum) + "\n" + m.styles.frame.Width(boxW).Render(strings.TrimRight(b.String(), "\n"))
}

// renderConfirmUndo is the confirmation of undoing an update whose folder
// changed after it was applied.
func (m *Model) renderConfirmUndo(boxW int) string {
	sum := text.TUIUndoUpdateSummary(targetName(m.pendingUndoReq.Target), m.pendingUndoSaved)
	if !m.pendingUndoReq.Confirm {
		// z asks before undoing an update (UX3).
		sum = text.TUIUndoUpdateAsk(text.SafeLine(targetName(m.pendingUndoReq.Target)))
	}
	if !m.details {
		return m.renderSummary(boxW, sum)
	}
	detail := fmt.Sprintf(msgConfirmUndoPrompt, text.SafeLine(m.pendingUndoSaved)) + "\n" +
		fmt.Sprintf("Destino: %s\nSnapshot: %s", text.SafeLine(m.pendingUndoReq.Target), text.SafeLine(m.pendingUndoReq.SnapshotID))
	return m.renderSummary(boxW, sum) + "\n" + m.styles.frame.Width(boxW).Render(detail)
}

// renderSummary is the box every confirmation starts with: its question,
// its plain lines and its keys.
func (m *Model) renderSummary(boxW int, s text.Summary) string {
	inner := max(10, boxW-4)
	keys := text.TUIConfirmKeys
	if m.details {
		keys = text.TUIConfirmKeysHide
	}
	var b strings.Builder
	b.WriteString(m.styles.title.Render(s.Question) + "\n")
	for _, l := range s.Lines {
		b.WriteString(l + "\n")
	}
	b.WriteString(lipgloss.NewStyle().Width(inner).Align(lipgloss.Right).Render(m.styles.keys.Render(keys)))
	return m.styles.frame.Width(boxW).Render(b.String())
}

// targetName is the skill a version 1 target names: lock:alpha is alpha,
// git:.agents/skills/myclone is myclone.
func targetName(target string) string {
	_, rest, found := strings.Cut(target, ":")
	if !found {
		rest = target
	}
	return path.Base(rest)
}

// helpLines are the lines of the help: one per screen, so that a letter
// means one thing in each.
func helpLines() []string {
	return []string{
		msgHelpTitle,
		"",
		text.TUIHelpGlobal,
		text.TUIHelpLists,
		text.TUIHelpSkills,
		text.TUIHelpSkills2,
		text.TUISkillsExclusions,
		text.TUIHelpUpdates,
		text.TUIHelpDiff,
		text.TUIHelpInstall,
		text.TUIHelpDoctor,
		text.TUIHelpHistory,
		text.TUIHelpSetting,
		text.TUIHelpPlan,
		msgHelpClose,
	}
}

// layoutHelp fits the help to the terminal: when it does not fit, it goes
// in a viewport the arrows scroll, with a line that says so.
func (m *Model) layoutHelp() {
	boxW := max(20, m.width)
	inner := max(10, boxW-4) // the frame's border and padding
	wrapped := lipgloss.NewStyle().Width(inner).Render(strings.Join(helpLines(), "\n"))
	footer := lipgloss.Height(m.renderFooter())
	// header, frame border, and the footer
	room := m.height - 1 - 2 - footer
	m.v2.helpScroll = lipgloss.Height(wrapped) > room
	if !m.v2.helpScroll {
		return
	}
	m.v2.help.SetWidth(inner)
	m.v2.help.SetHeight(max(3, room-1)) // the scroll hint takes a line
	m.v2.help.SetContent(wrapped)
}

func (m *Model) renderHelp(boxW int) string {
	if m.v2.helpScroll {
		return m.styles.frame.Width(boxW).Render(m.v2.help.View()) + "\n" +
			m.styles.muted.Width(boxW).Render(text.TUIHelpScroll)
	}
	return m.styles.frame.Width(boxW).Render(strings.Join(helpLines(), "\n"))
}

func (m *Model) renderFooter() string {
	w := max(20, m.width)
	var keys string
	switch m.mode {
	case modePlan:
		// A confirmation shows its own keys, whatever the screen; a plan's
		// detail scrolls.
		keys = text.TUIConfirmFooter
	case modeConfirmApply, modeConfirmApplyAll, modeConfirmUndo:
		keys = text.TUIConfirmFooterShort
	case modeNormal, modeFilter, modeEditInterval:
		keys = m.screenKeys()
	case modeSkillDetail:
		keys = text.TUIKeysSkillDetail
		if _, _, ok := m.homonymPair(m.selectedSk.ID); ok {
			keys = text.TUIKeysSkillDetailCompare
		}
	case modeSkillDocument:
		keys = text.TUIKeysDocument
	case modeAgentFilter:
		keys = text.TUIKeysAgentFilter
	case modeCompareAgents:
		keys = text.TUIKeysCompareAgents
	case modeCompareSkills:
		keys = text.TUIKeysCompareSkills
	case modeChecks, modeInstallTargets, modeUnignore:
		keys = text.TUIKeysChecks
	}
	var parts []string
	if m.status != "" {
		parts = append(parts, m.styles.statusBar.Width(w).Render(m.status))
	}
	if keys != "" {
		parts = append(parts, m.styles.muted.Width(w).Render(keys))
	}
	return strings.Join(parts, "\n")
}

// screenKeys are the keys of the active screen, and of its selected row.
func (m *Model) screenKeys() string {
	if !m.loaded && m.screen != ScreenSettings {
		return text.TUIHelpGlobal
	}
	switch m.screen {
	case ScreenSkills:
		return m.skillsKeys()
	case ScreenUpdates:
		return m.updatesKeys()
	case ScreenHistory:
		return m.historyKeys()
	case ScreenSettings:
		return m.settingsKeys()
	case ScreenInstall:
		return text.TUIKeysInstallStart
	case ScreenDoctor:
		return m.doctorKeys()
	}
	return text.TUIHelpGlobal
}

func (m *Model) resizeComponents() {
	tableHeight := max(6, m.height-8)
	w := max(40, m.width)

	m.skillsTable.SetWidth(w)
	m.skillsTable.SetHeight(tableHeight)
	m.updatesTable.SetWidth(w)
	m.updatesTable.SetHeight(tableHeight)
	m.historyTable.SetWidth(w)
	m.historyTable.SetHeight(tableHeight)

	m.diffViewport.SetWidth(max(30, w-8))
	m.diffViewport.SetHeight(max(8, m.height-10))
	m.refreshTables()
	m.resizeManage()
	m.layoutHelp()
}

func (m *Model) refreshTables() {
	w := max(40, m.width)
	m.allRows = append(PresentSkills(m.skills, ""), PresentParked(m.parked, "")...)
	q := m.filterInput.Value()
	m.skillRows = m.filterRows(slices.Concat(PresentSkills(m.skills, q), PresentParked(m.parked, q), m.presentIgnored(q)))
	skW := allocateColumns(w, []int{12, 12, 10, 20, 12}, []int{2, 1, 1, 3, 2})
	m.skillsTable.SetColumns([]table.Column{
		{Title: colSkillName, Width: skW[0]},
		{Title: colSkillOrigin, Width: skW[1]},
		{Title: colSkillHarns, Width: skW[2]},
		{Title: colSkillState, Width: skW[3]},
		{Title: colSkillIssues, Width: skW[4]},
	})
	skRows := make([]table.Row, 0, len(m.skillRows))
	for _, r := range m.skillRows {
		skRows = append(skRows, table.Row{r.Name, r.Origin, r.Harnesses, r.State, r.Issues})
	}
	m.skillsTable.SetRows(skRows)

	m.updateRows = PresentUpdates(m.candidates)
	upW := allocateColumns(w, []int{14, 12, 10, 14, 14}, []int{2, 1, 1, 2, 2})
	m.updatesTable.SetColumns([]table.Column{
		{Title: colUpdTarget, Width: upW[0]},
		{Title: colUpdID, Width: upW[1]},
		{Title: colUpdCommit, Width: upW[2]},
		{Title: colUpdState, Width: upW[3]},
		{Title: colUpdLabels, Width: upW[4]},
	})
	upRows := make([]table.Row, 0, len(m.updateRows))
	for _, r := range m.updateRows {
		upRows = append(upRows, table.Row{text.SafeLine(r.Target), text.SafeLine(r.CandidateID), r.ToCommit, r.State, r.Labels})
	}
	m.updatesTable.SetRows(upRows)

	m.historyRows = PresentHistory(m.history)
	hiW := allocateColumns(w, []int{16, 8, 12, 16, 10, 6}, []int{0, 0, 2, 1, 1, 1})
	m.historyTable.SetColumns([]table.Column{
		{Title: colHistAt, Width: hiW[0]},
		{Title: colHistAction, Width: hiW[1]},
		{Title: colHistTarget, Width: hiW[2]},
		{Title: colHistVersions, Width: hiW[3]},
		{Title: colHistSnapshot, Width: hiW[4]},
		{Title: colHistDetail, Width: hiW[5]},
	})
	hiRows := make([]table.Row, 0, len(m.historyRows))
	for _, r := range m.historyRows {
		hiRows = append(hiRows, table.Row{r.At, r.Action, text.SafeLine(r.Target), r.Versions, text.SafeLine(r.SnapshotID), text.SafeLine(r.Detail)})
	}
	m.historyTable.SetRows(hiRows)
}

func (m *Model) selectedSkill() (domain.Skill, bool) {
	idx := m.skillsTable.Cursor()
	if idx < 0 || idx >= len(m.skillRows) {
		return domain.Skill{}, false
	}
	id := m.skillRows[idx].ID
	for _, s := range m.skills {
		if s.ID == id {
			return s, true
		}
	}
	return m.ignoredByID(id)
}

func (m *Model) selectedCandidate() (domain.UpdateCandidate, bool) {
	idx := m.updatesTable.Cursor()
	if idx < 0 || idx >= len(m.candidates) {
		return domain.UpdateCandidate{}, false
	}
	return m.candidates[idx], true
}

func loadDataCmd(a *app.App) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// A version 2 record is resolved first, as RecoverOperation does.
		pending, err := a.ManagedPending()
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		if pending {
			return dataLoadedMsg{managedPending: true}
		}
		op, hasOp, err := a.Interrupted()
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		if hasOp {
			return dataLoadedMsg{hasOp: true, op: op}
		}
		skills, err := a.Skills(ctx)
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		cands, err := a.Candidates()
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		lastCheck, err := a.LastSuccessfulCheck()
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		unchecked, err := a.UncheckedSkills(ctx)
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		hist, err := a.History()
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		st, err := a.Settings()
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		bg, err := a.BackgroundStatus(ctx)
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		parked, err := a.Parked()
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		ignored, err := a.Ignored(ctx)
		if err != nil {
			return dataLoadedMsg{err: err}
		}
		// Who sees each skill, and why, only completes the Skills panel and
		// the full view: when the graph cannot be built, they tell the
		// locations instead.
		vis, err := a.Visibility(ctx)
		if err != nil {
			vis = nil
		}
		sightings, err := a.Sightings(ctx)
		if err != nil {
			sightings = nil
		}
		return dataLoadedMsg{
			operations: a.Operations(ctx, hist),
			visibility: vis,
			sightings:  sightings,
			parked:     parked,
			skills:     skills,
			ignored:    ignored,
			candidates: cands,
			lastCheck:  lastCheck,
			unchecked:  unchecked,
			history:    hist,
			settings:   st,
			bgStatus:   bg,
		}
	}
}

// actionsLoadedMsg carries what each skill admits and the Salud counter,
// from the load seq.
type actionsLoadedMsg struct {
	seq      int
	actions  map[string]app.SkillActions
	findings []app.Finding
	health   int
}

// actionsCmd reads, on one observation, what each skill admits and what
// Salud can repair: once per load, never per key (UX3). When it cannot be
// told, no write action is offered rather than one that would fail (L83),
// and Salud shows no counter.
func actionsCmd(a *app.App, seq int) tea.Cmd {
	return func() tea.Msg {
		ov, err := a.Overview(context.Background())
		if err != nil {
			return actionsLoadedMsg{seq: seq, health: -1}
		}
		n := 0
		for _, f := range ov.Findings {
			if f.Kind != text.TUIGentleKind && ov.Repairable[f.ID] {
				n++
			}
		}
		return actionsLoadedMsg{seq: seq, actions: ov.Actions, findings: ov.Findings, health: n}
	}
}

func checkNowCmd(a *app.App) tea.Cmd {
	return func() tea.Msg {
		res, err := a.Check(context.Background(), app.CheckOptions{})
		cands, _ := a.Candidates()
		return checkFinishedMsg{res: res, candidates: cands, err: err}
	}
}

func loadDiffCmd(a *app.App, c domain.UpdateCandidate) tea.Cmd {
	return func() tea.Msg {
		review, err := a.DiffForReview(context.Background(), c)
		return diffLoadedMsg{candidate: c, review: review, err: err}
	}
}

func applyOneCmd(a *app.App, ap updates.Approval) tea.Cmd {
	return func() tea.Msg {
		res, err := a.Apply(context.Background(), ap)
		var newer *domain.UpdateCandidate
		if errors.Is(err, updates.ErrRejected) && res.Target != "" {
			if c, cerr := a.CandidateFor(res.Target); cerr == nil {
				newer = &c
			}
		}
		return applyFinishedMsg{result: res, newCandidate: newer, err: err}
	}
}

func applyAllCmd(a *app.App, aps []updates.Approval) tea.Cmd {
	return func() tea.Msg {
		results := a.ApplyAll(context.Background(), aps)
		return applyAllFinishedMsg{results: results}
	}
}

func skipCmd(a *app.App, ap updates.Approval) tea.Cmd {
	return func() tea.Msg {
		err := a.Skip(context.Background(), ap)
		return skipFinishedMsg{err: err}
	}
}

func undoCmd(a *app.App, req updates.UndoRequest, id int) tea.Cmd {
	return func() tea.Msg {
		res, err := a.Undo(context.Background(), req)
		return undoFinishedMsg{id: id, req: req, result: res, err: err}
	}
}

// recoverCmd resolves the pending operation, of version 2 or 1, as
// «skillhub recover» does. Only the user's R or enter starts it.
func recoverCmd(a *app.App) tea.Cmd {
	return func() tea.Msg {
		rec, err := a.RecoverOperation(context.Background())
		return recoverFinishedMsg{rec: rec, err: err}
	}
}

// explainRecover tells, in the CLI's words, why recover did not resolve
// the operation.
func explainRecover(rec app.Recovery, err error) string {
	switch {
	case errors.Is(err, updates.ErrNoOperation):
		return strings.TrimSpace(text.RecoverNoOp)
	case rec.Version == 2:
		if msg, ok := text.Refusal(err, app.Request{}); ok {
			return sentence(msg)
		}
		return sentence(text.RecoverFailed(err))
	}
	return explainManage(err, app.Request{})
}

func ignoreSkillCmd(a *app.App, existing []string, name string) tea.Cmd {
	return func() tea.Msg {
		items := append(slices.Clone(existing), name)
		_, err := a.SetSetting(app.KeyIgnore, strings.Join(items, ","), false)
		return settingsSavedMsg{status: fmt.Sprintf(msgIgnoredAdded, name), err: err}
	}
}

func saveSettingCmd(a *app.App, key, val, okMsg string) tea.Cmd {
	return func() tea.Msg {
		_, err := a.SetSetting(key, val, false)
		return settingsSavedMsg{status: okMsg, err: err}
	}
}
