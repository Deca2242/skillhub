package tui

import (
	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"

	"skillhub/internal/entrypoints/text"
)

type styles struct {
	header      lipgloss.Style
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style
	statusBar   lipgloss.Style
	errorText   lipgloss.Style
	box         lipgloss.Style
	warnBox     lipgloss.Style
	muted       lipgloss.Style

	// The Skills list and panel, and the confirmations (S13). The colors
	// follow the terminal's background through lipgloss.LightDark; Bubble
	// Tea drops them when NO_COLOR is set, and the icons still tell the
	// state without them.
	frame    lipgloss.Style
	title    lipgloss.Style
	selected lipgloss.Style
	keys     lipgloss.Style
	problem  lipgloss.Style
	icons    map[RowKind]lipgloss.Style
	// risks color the traffic light of the Actualizar list.
	risks map[text.Risk]lipgloss.Style
}

// defaultStyles are the styles of a dark background, until the terminal
// tells its own.
func defaultStyles() styles { return newStyles(true) }

func newStyles(dark bool) styles {
	pick := lipgloss.LightDark(dark)
	green := pick(lipgloss.Color("#1a7f37"), lipgloss.Color("#3fb950"))
	blue := pick(lipgloss.Color("#0969da"), lipgloss.Color("#58a6ff"))
	gray := pick(lipgloss.Color("#6e7781"), lipgloss.Color("#8b949e"))
	yellow := pick(lipgloss.Color("#9a6700"), lipgloss.Color("#d29922"))
	red := pick(lipgloss.Color("#cf222e"), lipgloss.Color("#f85149"))
	return styles{
		header:      lipgloss.NewStyle().Bold(true),
		tabActive:   lipgloss.NewStyle().Bold(true).Underline(true).Padding(0, 1),
		tabInactive: lipgloss.NewStyle().Padding(0, 1),
		statusBar:   lipgloss.NewStyle().Padding(0, 1),
		errorText:   lipgloss.NewStyle().Bold(true),
		box:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2),
		warnBox:     lipgloss.NewStyle().Border(lipgloss.ThickBorder()).Padding(1, 2),
		muted:       lipgloss.NewStyle().Faint(true),

		frame:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		title:    lipgloss.NewStyle().Bold(true),
		selected: lipgloss.NewStyle().Bold(true).Reverse(true),
		keys:     lipgloss.NewStyle().Bold(true).Foreground(blue),
		problem:  lipgloss.NewStyle().Foreground(yellow),
		icons: map[RowKind]lipgloss.Style{
			RowOK:      lipgloss.NewStyle().Foreground(green),
			RowUpdate:  lipgloss.NewStyle().Foreground(blue),
			RowOff:     lipgloss.NewStyle().Foreground(gray),
			RowWarn:    lipgloss.NewStyle().Foreground(yellow),
			RowIgnored: lipgloss.NewStyle().Foreground(gray),
		},
		risks: map[text.Risk]lipgloss.Style{
			text.RiskLow:    lipgloss.NewStyle().Foreground(green),
			text.RiskMedium: lipgloss.NewStyle().Foreground(yellow),
			text.RiskReview: lipgloss.NewStyle().Foreground(red),
		},
	}
}

func defaultTableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Bold(true).
		Reverse(true)
	return s
}
