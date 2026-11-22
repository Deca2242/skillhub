// Package notify implements ports.Notifier using notify-send via ports.ProcessRunner.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"skillhub/internal/shared/ports"
)

var _ ports.Notifier = (*Notifier)(nil)

// Notifier sends desktop notifications via notify-send.
type Notifier struct {
	runner ports.ProcessRunner
}

// New builds a Notifier that runs notify-send through runner.
func New(runner ports.ProcessRunner) *Notifier {
	return &Notifier{runner: runner}
}

// Notify sends a desktop notification with title and body.
func (n *Notifier) Notify(ctx context.Context, title, body string) error {
	if n.runner == nil {
		return errors.New("notify: process runner is nil")
	}

	res, err := n.runner.Run(ctx, ports.Command{
		Name: "notify-send",
		Args: []string{"--", title, body},
	})
	if err != nil {
		return fmt.Errorf("notify-send: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("notify-send exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
