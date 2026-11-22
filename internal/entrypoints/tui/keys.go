package tui

import (
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
)

// The keys of the TUI are lower case letters (L79). A capital letter does
// what its lower case does, in case Caps Lock is on, and is never shown.
// Only the keys that act are folded: the text typed in a filter or a
// prompt reaches its input as it was typed.

// keyOf is the key msg names, with a single capital letter folded to its
// lower case.
func keyOf(msg tea.KeyPressMsg) string {
	s := msg.String()
	r, size := utf8.DecodeRuneInString(s)
	if size == len(s) && unicode.IsUpper(r) {
		return string(unicode.ToLower(r))
	}
	return s
}

// listKeyMap moves the lists with the arrows, PgUp and PgDn, and home and
// end. The half page keys d and u of bubbles are left out: d disables a
// skill and u undoes an operation; so are g and G: g filters Skills by
// agent (UX3).
func listKeyMap() table.KeyMap {
	return table.KeyMap{
		LineUp:       key.NewBinding(key.WithKeys("up")),
		LineDown:     key.NewBinding(key.WithKeys("down")),
		PageUp:       key.NewBinding(key.WithKeys("pgup")),
		PageDown:     key.NewBinding(key.WithKeys("pgdown")),
		HalfPageUp:   key.NewBinding(key.WithDisabled()),
		HalfPageDown: key.NewBinding(key.WithDisabled()),
		GotoTop:      key.NewBinding(key.WithKeys("home")),
		GotoBottom:   key.NewBinding(key.WithKeys("end")),
	}
}

// newList is a table that only keeps a cursor: the screens draw their own
// list from it.
func newList() table.Model {
	return table.New(table.WithFocused(true), table.WithStyles(defaultTableStyles()),
		table.WithHeight(12), table.WithKeyMap(listKeyMap()))
}
