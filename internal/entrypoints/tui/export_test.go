package tui

import (
	tea "charm.land/bubbletea/v2"

	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// NewApplyFinishedMsgForTest builds an applyFinishedMsg for Update tests.
func NewApplyFinishedMsgForTest(res updates.ApplyResult, newer *domain.UpdateCandidate, err error) tea.Msg {
	return applyFinishedMsg{result: res, newCandidate: newer, err: err}
}

// NewCheckFinishedMsgForTest builds a checkFinishedMsg for teatest flows.
func NewCheckFinishedMsgForTest(cands []domain.UpdateCandidate) tea.Msg {
	return checkFinishedMsg{candidates: cands}
}

// RowCountForTest is the number of rows of the Skills list.
func (m *Model) RowCountForTest() int { return len(m.skillRows) }

// SelectedIDForTest is the skill ID of the selected row.
func (m *Model) SelectedIDForTest() string {
	row, _ := m.selectedRow()
	return row.ID
}

// OfferedKeysForTest are the keys of the actions the selected row's panel
// offers.
func (m *Model) OfferedKeysForTest() []string {
	row, ok := m.selectedRow()
	if !ok {
		return nil
	}
	var keys []string
	for _, a := range m.rowActions(row) {
		keys = append(keys, a.key)
	}
	return keys
}

// InPlanForTest reports whether a plan waits for confirmation.
func (m *Model) InPlanForTest() bool { return m.mode == modePlan }

// InPromptForTest reports whether a prompt asks for a value.
func (m *Model) InPromptForTest() bool { return m.mode == modePrompt }

// StatusForTest is the status line.
func (m *Model) StatusForTest() string { return m.status }
