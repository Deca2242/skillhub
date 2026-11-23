package tui

import tea "charm.land/bubbletea/v2"

// Accessors for the tests that deliver the load's messages in the order the
// runtime may: Salud's findings before what each skill admits (B1 of INV10's
// verifier). They read only what the screens already keep.

// LoadMsgKindForTest names the messages of the load: "data", "findings" and
// "actions" (what each skill admits); "" for any other.
func LoadMsgKindForTest(msg tea.Msg) string {
	switch msg.(type) {
	case dataLoadedMsg:
		return "data"
	case findingsMsg:
		return "findings"
	case actionsLoadedMsg:
		return "actions"
	}
	return ""
}

// RepairsForTest is the finding whose repairs are on screen; ok is false
// when no repairs are.
func (m *Model) RepairsForTest() (finding string, ok bool) {
	return m.v2.finding.ID, m.mode == modeRepairs
}

// PlanFindingForTest is the finding of the plan waiting for confirmation.
func (m *Model) PlanFindingForTest() string { return m.v2.plan.Request.Finding }
