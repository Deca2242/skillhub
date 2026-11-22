package app

import (
	"context"
	"errors"
	"fmt"

	"skillhub/internal/background"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// CheckOptions are the options of a check (docs/design.md sections 8 and 9).
type CheckOptions struct {
	// IfDue runs the check only when check_interval has passed since the
	// last successful check.
	IfDue bool
	// Notify sends a desktop notification in the three cases of section 8
	// and records what it announced.
	Notify bool
	// Message writes the notification's title and body; the entry point owns
	// the user-facing text. Required with Notify.
	Message func(background.Decision) (title, body string)
}

// CheckResult is what a check did.
type CheckResult struct {
	// Ran is false when IfDue found the check not due.
	Ran    bool
	Report updates.CheckReport
	// Failed is true when the check returned an error or a target failed.
	Failed bool
	// Notified is true when a notification was sent.
	Notified bool
}

// errNoMessage reports Notify without Message.
var errNoMessage = errors.New("app: check with Notify needs Message")

// Check detects, stages and labels updates. It never applies or recovers
// anything. A failed target makes Failed true without an error; the error is
// for a check that could not run, or a notification that could not be sent.
func (a *App) Check(ctx context.Context, opts CheckOptions) (CheckResult, error) {
	if opts.Notify && opts.Message == nil {
		return CheckResult{}, errNoMessage
	}
	settings, err := a.d.Settings.Load()
	if err != nil {
		return CheckResult{}, fmt.Errorf("load settings: %w", err)
	}
	var res CheckResult
	var checkErr error
	run := true
	if opts.IfDue {
		st, err := a.d.State.Load()
		if err != nil {
			return CheckResult{}, fmt.Errorf("load state: %w", err)
		}
		run = background.Due(st.Checks, settings.CheckInterval, a.d.Clock.Now())
	}
	if run {
		res.Ran = true
		res.Report, checkErr = a.upd.Check(ctx)
		res.Failed = checkErr != nil || res.Report.Failed()
		if checkErr != nil {
			checkErr = fmt.Errorf("check: %w", checkErr)
		}
	}
	if !opts.Notify {
		return res, checkErr
	}
	notified, err := a.announce(ctx, opts.Message, background.Input{
		Enabled: settings.Notifications, Checked: res.Ran, CheckFailed: res.Failed,
	})
	res.Notified = notified
	return res, errors.Join(checkErr, err)
}

// announce decides the notification under the state lock, sends it and then
// saves the marks; a notification that could not be sent marks nothing, and
// a pending version 2 operation neither sends nor marks anything.
func (a *App) announce(ctx context.Context, message func(background.Decision) (string, string), in background.Input) (bool, error) {
	release, err := a.d.State.Lock(ctx)
	if err != nil {
		return false, fmt.Errorf("take the state lock: %w", err)
	}
	defer release() //nolint:errcheck // releasing an advisory lock; the process exit releases it too.

	// Saving the marks is a write of state: a pending version 2 operation
	// stops it, as it stops every version 1 flow.
	if err := a.upd.GuardV2Operation(); err != nil {
		return false, err
	}
	st, err := a.d.State.Load()
	if err != nil {
		return false, fmt.Errorf("load state: %w", err)
	}
	if in.Open, in.Operation, err = a.openAndOperation(); err != nil {
		return false, err
	}
	in.Marks = st.Notified
	d := background.Decide(in)
	if d.Notify() {
		title, body := message(d)
		if err := a.d.Notifier.Notify(ctx, title, body); err != nil {
			return false, fmt.Errorf("send the notification: %w", err)
		}
	}
	st.Notified = d.Marks
	if err := a.d.State.Save(st); err != nil {
		return d.Notify(), fmt.Errorf("save the notification marks: %w", err)
	}
	return d.Notify(), nil
}

// openAndOperation reads what the background check announces.
func (a *App) openAndOperation() ([]domain.UpdateCandidate, *domain.Operation, error) {
	open, err := a.upd.Candidates()
	if err != nil {
		return nil, nil, err
	}
	op, ok, err := a.upd.Interrupted()
	if err != nil {
		return nil, nil, fmt.Errorf("read the operation in progress: %w", err)
	}
	if !ok {
		return open, nil, nil
	}
	return open, &op, nil
}
