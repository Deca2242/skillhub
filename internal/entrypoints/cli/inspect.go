package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/background"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

func (r *runner) scanCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "scan [--json]",
		Short: scanShort,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			skills, err := a.Skills(r.ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, skills)
			}
			if len(skills) == 0 {
				fmt.Fprint(out, msgNoSkills)
				return nil
			}
			tw := table(out)
			fmt.Fprint(tw, hdrSkills)
			for _, s := range skills {
				name := s.Name
				if len(s.Aliases) > 0 {
					name = fmt.Sprintf(fmtSkillAlias, s.Name, strings.Join(s.Aliases, ", "))
				}
				fmt.Fprintf(tw, fmtSkillRow, name, text.OriginKind(s.Origin.Kind), harnesses(s.Locations),
					text.State(s.State), issueNames(s.Issues))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return c
}

// checkJSON is the --json output of check.
type checkJSON struct {
	Ran           bool              `json:"ran"`
	Failed        bool              `json:"failed"`
	Notified      bool              `json:"notified"`
	Targets       []checkTargetJSON `json:"targets"`
	NewCandidates []string          `json:"new_candidates"`
	Pending       int               `json:"pending"`
}

type checkTargetJSON struct {
	Target      string `json:"target"`
	Outcome     string `json:"outcome"`
	CandidateID string `json:"candidate_id,omitempty"`
	// RemovedUpstream marks an up_to_date target whose folder is gone
	// upstream.
	RemovedUpstream bool `json:"removed_upstream,omitempty"`
	// WebSource marks a not_checked target whose skill comes from a
	// website: its updates are not checked yet (L84).
	WebSource bool   `json:"web_source,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (r *runner) checkCmd() *cobra.Command {
	var ifDue, notify, exitCode, asJSON bool
	c := &cobra.Command{
		Use:   "check [--if-due] [--notify] [--exit-code] [--json]",
		Short: checkShort,
		Long:  checkLong,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			res, checkErr := a.Check(r.ctx, app.CheckOptions{IfDue: ifDue, Notify: notify, Message: notification})
			cands, err := a.Candidates()
			if err != nil {
				return err
			}
			pending := 0
			for _, c := range cands {
				if c.State == domain.CandidatePending {
					pending++
				}
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if err := writeJSON(out, toCheckJSON(res, pending)); err != nil {
					return err
				}
			} else if err := r.printCheck(cmd, a, res, pending); err != nil {
				return err
			}
			switch {
			case checkErr != nil:
				return checkErr
			case res.Failed:
				return fail(msgCheckFailed)
			case exitCode && pending > 0:
				return silent(exitPending)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&ifDue, "if-due", false, flagIfDue)
	c.Flags().BoolVar(&notify, "notify", false, flagNotify)
	c.Flags().BoolVar(&exitCode, "exit-code", false, flagExitCode)
	c.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return c
}

func toCheckJSON(res app.CheckResult, pending int) checkJSON {
	out := checkJSON{
		Ran: res.Ran, Failed: res.Failed, Notified: res.Notified, Pending: pending,
		Targets: []checkTargetJSON{}, NewCandidates: []string{},
	}
	out.NewCandidates = append(out.NewCandidates, res.Report.NewCandidates...)
	for _, t := range res.Report.Targets {
		tj := checkTargetJSON{
			Target: t.Target, Outcome: string(t.Outcome), CandidateID: t.CandidateID, RemovedUpstream: t.RemovedUpstream,
			WebSource: t.WebSource,
		}
		if t.Err != nil {
			tj.Error = t.Err.Error()
		}
		out.Targets = append(out.Targets, tj)
	}
	return out
}

func (r *runner) printCheck(cmd *cobra.Command, a *app.App, res app.CheckResult, pending int) error {
	out := cmd.OutOrStdout()
	if !res.Ran {
		st, err := a.Settings()
		if err != nil {
			return err
		}
		last, err := a.LastSuccessfulCheck()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, msgNotDue, formatTime(last), formatDuration(st.CheckInterval))
	} else {
		tw := table(out)
		for _, t := range res.Report.Targets {
			fmt.Fprintf(tw, fmtCheckTarget, t.Target, outcomeName(t))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, msgPending, pending)
	return nil
}

func outcomeName(t updates.TargetCheck) string {
	switch t.Outcome {
	case updates.CheckUpToDate:
		if t.RemovedUpstream {
			return fmt.Sprintf(fmtOutcomeRemoved, msgOutcomeUp, text.Issue(domain.IssueRemovedUpstream))
		}
		return msgOutcomeUp
	case updates.CheckUpdateAvailable:
		return msgOutcomeAv
	case updates.CheckBlocked:
		return msgOutcomeBl
	case updates.CheckSkipped:
		return msgOutcomeSk
	case updates.CheckFailed:
		return fmt.Sprintf(msgOutcomeFail, t.Err)
	case updates.CheckNotChecked:
		if t.WebSource {
			return text.WebUpdatesLater
		}
		return msgOutcomeNo
	default:
		return msgOutcomeNo
	}
}

// notification writes the one notification of a background run.
func notification(d background.Decision) (string, string) {
	var lines []string
	for _, reason := range d.Reasons {
		switch reason {
		case background.ReasonNewCandidates:
			var names []string
			for _, c := range d.NewCandidates {
				name := strings.TrimPrefix(c.Target, domain.LockID(""))
				if !slices.Contains(names, name) {
					names = append(names, name)
				}
			}
			lines = append(lines, fmt.Sprintf(notifyNew, strings.Join(names, ", ")))
		case background.ReasonCheckFailed:
			lines = append(lines, notifyFailed)
		case background.ReasonInterrupted:
			lines = append(lines, notifyInterrupted)
		}
	}
	return notifyTitle, strings.Join(lines, "\n")
}
