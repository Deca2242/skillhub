package tui

import (
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"skillhub/internal/shared/domain"
)

func TestPasteIntoTextInputs(t *testing.T) {
	const pasted = "owner/repo@my-skill"

	cases := []struct {
		name  string
		mode  viewMode
		input func(*Model) *textinput.Model
	}{
		{
			name:  "install source",
			mode:  modeInstallSource,
			input: func(m *Model) *textinput.Model { return &m.v2.source },
		},
		{
			name:  "catalog search",
			mode:  modeCatalogSearch,
			input: func(m *Model) *textinput.Model { return &m.catalogInput },
		},
		{
			name:  "skills filter",
			mode:  modeFilter,
			input: func(m *Model) *textinput.Model { return &m.filterInput },
		},
		{
			name:  "settings interval",
			mode:  modeEditInterval,
			input: func(m *Model) *textinput.Model { return &m.intervalInput },
		},
		{
			name:  "enable prompt",
			mode:  modePrompt,
			input: func(m *Model) *textinput.Model { return &m.v2.prompt },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(nil)
			m.mode = tc.mode
			in := tc.input(m)
			in.Focus()

			next, _ := m.Update(tea.PasteMsg{Content: pasted})
			got := next.(*Model)
			gotIn := tc.input(got)

			if gotIn.Value() != pasted {
				t.Fatalf("value = %q, want %q", gotIn.Value(), pasted)
			}
			if gotIn.Position() != len(pasted) {
				t.Fatalf("cursor position = %d, want %d", gotIn.Position(), len(pasted))
			}
		})
	}
}

func TestPasteIgnoredInNonInputMode(t *testing.T) {
	m := NewModel(nil)
	m.mode = modeNormal

	next, cmd := m.Update(tea.PasteMsg{Content: "owner/repo@my-skill"})
	got := next.(*Model)

	if cmd != nil {
		t.Fatalf("expected nil cmd in modeNormal, got non-nil")
	}
	if got.v2.source.Value() != "" || got.catalogInput.Value() != "" || got.filterInput.Value() != "" {
		t.Fatalf("expected all text inputs to remain empty in modeNormal")
	}
}

func TestPasteCtrlVForwardsCommandAndPreservesCursor(t *testing.T) {
	cases := []struct {
		name  string
		mode  viewMode
		input func(*Model) *textinput.Model
	}{
		{
			name:  "install source",
			mode:  modeInstallSource,
			input: func(m *Model) *textinput.Model { return &m.v2.source },
		},
		{
			name:  "catalog search",
			mode:  modeCatalogSearch,
			input: func(m *Model) *textinput.Model { return &m.catalogInput },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(nil)
			m.mode = tc.mode
			in := tc.input(m)
			in.Focus()

			// Ctrl+V must be forwarded to textinput and return its Paste tea.Cmd
			// (do not invoke cmd(), which would read the host clipboard).
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
			if cmd == nil {
				t.Fatalf("expected non-nil tea.Cmd from Ctrl+V in %s", tc.name)
			}

			// Feeding PasteMsg sequences inserts text and advances the cursor.
			next, _ = next.Update(tea.PasteMsg{Content: "owner/repo@"})
			next, _ = next.Update(tea.PasteMsg{Content: "my-skill"})
			gotIn := tc.input(next.(*Model))

			const want = "owner/repo@my-skill"
			if gotIn.Value() != want {
				t.Fatalf("value = %q, want %q", gotIn.Value(), want)
			}
			if gotIn.Position() != len(want) {
				t.Fatalf("cursor position = %d, want %d", gotIn.Position(), len(want))
			}
		})
	}
}

func TestPasteFilterRefreshesRows(t *testing.T) {
	m := NewModel(nil)
	m.skills = []domain.Skill{
		{ID: "alpha-skill", Name: "alpha-skill"},
		{ID: "my-skill", Name: "my-skill"},
	}
	m.refreshTables()
	if len(m.skillRows) != 2 {
		t.Fatalf("initial skillRows = %d, want 2", len(m.skillRows))
	}

	m.mode = modeFilter
	m.filterInput.Focus()

	next, _ := m.Update(tea.PasteMsg{Content: "my-skill"})
	got := next.(*Model)

	if got.filterInput.Value() != "my-skill" {
		t.Fatalf("filter value = %q, want %q", got.filterInput.Value(), "my-skill")
	}
	if len(got.skillRows) != 1 {
		t.Fatalf("filtered skillRows = %d, want 1", len(got.skillRows))
	}
	if got.skillRows[0].ID != "my-skill" {
		t.Fatalf("filtered row ID = %q, want %q", got.skillRows[0].ID, "my-skill")
	}
}
