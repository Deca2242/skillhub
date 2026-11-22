// Package background decides when the background check runs and when it
// sends a desktop notification (docs/design.md section 8). It is pure: the
// caller reads the state, runs the check, sends the notification and saves
// the marks this package computes.
package background

import (
	"slices"
	"time"

	"skillhub/internal/shared/domain"
)

// Due reports whether check --if-due runs now: when no check ever succeeded,
// or when interval has passed since the last successful check.
func Due(checks domain.CheckTimes, interval time.Duration, now time.Time) bool {
	if checks.LastSuccess.IsZero() {
		return true
	}
	return !now.Before(checks.LastSuccess.Add(interval))
}

// Reason is one of the three cases that send a notification.
type Reason string

const (
	// ReasonNewCandidates: a candidate appeared since the last notification,
	// even when it supersedes another.
	ReasonNewCandidates Reason = "new_candidates"
	// ReasonCheckFailed: a check failed after the previous one succeeded.
	ReasonCheckFailed Reason = "check_failed"
	// ReasonInterrupted: an interrupted operation was not announced yet.
	ReasonInterrupted Reason = "interrupted"
)

// Input is what one background run knows after its check.
type Input struct {
	// Marks are what earlier runs already announced.
	Marks domain.NotifiedMarks
	// Enabled is the notifications setting; false silences every
	// notification.
	Enabled bool
	// Checked is false when --if-due skipped the check; then only the
	// interrupted operation can be announced.
	Checked bool
	// CheckFailed is true when this run's check failed.
	CheckFailed bool
	// Open are the candidates waiting for the user after the check: pending
	// and blocked.
	Open []domain.UpdateCandidate
	// Operation is the interrupted operation, if any.
	Operation *domain.Operation
}

// Decision is whether to notify, why, and the marks to save once the
// notification was sent.
type Decision struct {
	// Reasons are empty when nothing is to be announced.
	Reasons []Reason
	// NewCandidates are the candidates announced for ReasonNewCandidates.
	NewCandidates []domain.UpdateCandidate
	// Marks replace the saved marks.
	Marks domain.NotifiedMarks
}

// Notify reports whether the run sends a notification.
func (d Decision) Notify() bool { return len(d.Reasons) > 0 }

// Decide applies the notification rule of docs/design.md section 8: one
// notification per run, only for a candidate that appeared since the last
// notification, a check that fails after a success, or an interrupted
// operation not announced yet. With notifications off nothing is announced
// and nothing new is marked; marks of what is gone are still dropped, so
// turning notifications back on announces only what is news then.
func Decide(in Input) Decision {
	d := Decision{NewCandidates: []domain.UpdateCandidate{}, Marks: in.Marks}
	d.Marks.CandidateIDs = slices.Clone(in.Marks.CandidateIDs)

	if in.Checked {
		open := map[string]bool{}
		for _, c := range in.Open {
			open[c.ID] = true
		}
		announced := map[string]bool{}
		for _, id := range in.Marks.CandidateIDs {
			announced[id] = true
		}
		kept := []string{}
		for _, id := range in.Marks.CandidateIDs {
			if open[id] {
				kept = append(kept, id)
			}
		}
		for _, c := range in.Open {
			if !announced[c.ID] {
				d.NewCandidates = append(d.NewCandidates, c)
			}
		}
		if in.Enabled {
			for _, c := range d.NewCandidates {
				kept = append(kept, c.ID)
			}
			if len(d.NewCandidates) > 0 {
				d.Reasons = append(d.Reasons, ReasonNewCandidates)
			}
			if in.CheckFailed && !in.Marks.CheckFailure {
				d.Reasons = append(d.Reasons, ReasonCheckFailed)
			}
			d.Marks.CheckFailure = in.CheckFailed
		} else {
			d.NewCandidates = []domain.UpdateCandidate{}
			d.Marks.CheckFailure = in.Marks.CheckFailure && in.CheckFailed
		}
		slices.Sort(kept)
		d.Marks.CandidateIDs = slices.Compact(kept)
	}

	switch {
	case in.Operation == nil:
		d.Marks.OperationID = ""
	case in.Enabled && in.Operation.ID != in.Marks.OperationID:
		d.Reasons = append(d.Reasons, ReasonInterrupted)
		d.Marks.OperationID = in.Operation.ID
	case !in.Enabled && in.Operation.ID != in.Marks.OperationID:
		d.Marks.OperationID = ""
	}
	return d
}
