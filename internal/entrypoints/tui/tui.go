// Package tui is the terminal UI entry point (docs/design.md section 9).
// It renders the four screens (Skills, Actualizaciones, Historial, Ajustes)
// over the app.App facade using Bubble Tea v2, Bubbles v2 and Lip Gloss v2.
package tui

import (
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"

	"skillhub/internal/app"
)

// exitError is the exit code of a failed run (docs/design.md section 9).
const exitError = 2

// Run runs the terminal UI and returns the exit code.
func Run(a *app.App, stdin io.Reader, stdout, stderr io.Writer) int {
	if a == nil {
		fmt.Fprint(stderr, msgTUINilApp)
		return exitError
	}
	opts := []tea.ProgramOption{tea.WithOutput(stdout)}
	if stdin != nil {
		opts = append(opts, tea.WithInput(stdin))
	}
	p := tea.NewProgram(program{NewModel(a)}, opts...)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "skillhub: %v\n", err)
		return exitError
	}
	return 0
}

// program is the model Run starts: it also asks the terminal for its
// background, so that the colors suit a light or a dark one (S13). The
// model handles the answer; tests start the model without asking.
type program struct{ *Model }

func (p program) Init() tea.Cmd {
	return tea.Batch(p.Model.Init(), tea.RequestBackgroundColor)
}
