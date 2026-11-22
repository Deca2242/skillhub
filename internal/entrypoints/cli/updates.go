package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

func (r *runner) updatesCmd() *cobra.Command {
	return group("updates", updatesShort, r.listCmd(), r.showCmd(), r.applyCmd(), r.skipCmd())
}

// candidateFor resolves what the user typed to the candidate waiting for
// that skill.
func (r *runner) candidateFor(a *app.App, ref string) (domain.UpdateCandidate, error) {
	id, err := r.resolve(a, ref)
	if err != nil {
		return domain.UpdateCandidate{}, err
	}
	c, err := a.CandidateFor(id)
	if errors.Is(err, app.ErrNoCandidate) {
		return domain.UpdateCandidate{}, fail(msgNoCandidate, ref)
	}
	return c, err
}

// resolve turns a name or an id into an id; a name of several rows is
// refused listing the ids.
func (r *runner) resolve(a *app.App, ref string) (string, error) {
	id, err := a.Resolve(r.ctx, ref)
	if errors.Is(err, app.ErrSkillNotFound) {
		return "", fail(msgNotFound, ref)
	}
	return id, err
}

func (r *runner) listCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list [--json]",
		Short: listShort,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			cands, err := a.Candidates()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, cands)
			}
			if len(cands) == 0 {
				fmt.Fprint(out, msgNoUpdates)
				return nil
			}
			tw := table(out)
			fmt.Fprint(tw, hdrUpdates)
			for _, c := range cands {
				state := text.CandidateState(c.State)
				if c.State == domain.CandidateBlocked {
					state += ": " + text.BlockReason(c.BlockReason)
				}
				fmt.Fprintf(tw, fmtUpdateRow, c.Target, c.ID, shortSHA(c.ToCommit), state, labelNames(c.Labels))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return c
}

func (r *runner) showCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <skill> [--json]",
		Short: showShort,
		Long:  showLong,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			c, err := r.candidateFor(a, args[0])
			if err != nil {
				return err
			}
			review, err := a.DiffForReview(r.ctx, c)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, newShowJSON(c, review))
			}
			fmt.Fprintf(out, fmtShowHeader, c.Target, c.ID, shortSHA(c.FromSHA), shortSHA(c.ToCommit),
				shortSHA(c.ToSHA), text.CandidateState(c.State), labelNames(c.Labels))
			if c.State == domain.CandidateBlocked {
				fmt.Fprintf(out, fmtShowBlocked, text.BlockReason(c.BlockReason))
			}
			if review.LocallyModified || isLocallyModified(c) {
				fmt.Fprint(out, msgShowLocal)
			}
			switch {
			case review.Split():
				if err := writeDiffs(out, fmt.Sprintf(fmtShowSection, msgShowUser), review.User); err != nil {
					return err
				}
				return writeDiffs(out, fmt.Sprintf(fmtShowSection, msgShowAuthor), review.Author)
			case review.LocallyModified:
				fmt.Fprint(out, msgShowRegisteredGone)
				return writeDiffs(out, fmt.Sprintf(fmtShowSection, msgShowInstalled), review.Installed)
			default:
				return writeDiffs(out, msgShowFiles, review.Installed)
			}
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return cmd
}

// writeDiffs writes header, the list of files and each file's diff.
func writeDiffs(out io.Writer, header string, diffs []app.FileDiff) error {
	fmt.Fprint(out, header)
	tw := table(out)
	for _, d := range diffs {
		fmt.Fprintf(tw, fmtShowFile, text.FileChangeKind(d.Change), d.Path)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, d := range diffs {
		fmt.Fprintln(out)
		if d.Binary {
			fmt.Fprintf(out, "%s: %s", d.Path, msgShowBinary)
			continue
		}
		fmt.Fprint(out, d.Unified)
	}
	return nil
}

// showJSON is what updates show --json prints: the candidate and its
// review, each diff in its own field; a diff the review does not have is
// null.
type showJSON struct {
	Candidate             domain.UpdateCandidate `json:"candidate"`
	LocallyModified       bool                   `json:"locally_modified"`
	RegisteredAvailable   bool                   `json:"registered_available"`
	InstalledToUpstream   []fileDiffJSON         `json:"installed_to_upstream"`
	RegisteredToInstalled []fileDiffJSON         `json:"registered_to_installed"`
	RegisteredToUpstream  []fileDiffJSON         `json:"registered_to_upstream"`
}

// fileDiffJSON is one file of a diff in updates show --json.
type fileDiffJSON struct {
	Path    string                `json:"path"`
	Change  domain.FileChangeKind `json:"change"`
	Unified string                `json:"unified"`
	Binary  bool                  `json:"binary"`
}

func newShowJSON(c domain.UpdateCandidate, r app.ReviewDiff) showJSON {
	s := showJSON{Candidate: c, LocallyModified: r.LocallyModified, RegisteredAvailable: r.RegisteredAvailable}
	if r.Split() {
		s.RegisteredToInstalled = fileDiffsJSON(r.User)
		s.RegisteredToUpstream = fileDiffsJSON(r.Author)
	} else {
		s.InstalledToUpstream = fileDiffsJSON(r.Installed)
	}
	return s
}

func fileDiffsJSON(diffs []app.FileDiff) []fileDiffJSON {
	out := make([]fileDiffJSON, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, fileDiffJSON{Path: d.Path, Change: d.Change, Unified: d.Unified, Binary: d.Binary})
	}
	return out
}

func (r *runner) applyCmd() *cobra.Command {
	var yes, dryRun, all bool
	c := &cobra.Command{
		Use:   "apply <skill>... | --all [--yes] [--dry-run]",
		Short: applyShort,
		Long:  applyLong,
		Args: func(_ *cobra.Command, args []string) error {
			switch {
			case all && len(args) > 0:
				return fail(msgApplyBoth)
			case !all && len(args) == 0:
				return fail(msgApplyWhat)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			if err := r.refuseInterrupted(a); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			plan, err := r.applyPlan(out, a, args, all)
			if err != nil || len(plan) == 0 {
				return err
			}
			fmt.Fprint(out, msgPlanHeader)
			tw := table(out)
			for _, c := range plan {
				fmt.Fprintf(tw, fmtPlanRow, c.Target, c.ID, shortSHA(c.ToCommit), shortSHA(c.ToSHA), labelNames(c.Labels))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprint(out, msgDryRun)
				return nil
			}
			if !yes {
				if !r.env.Interactive {
					return fail(msgNeedsYes)
				}
				if !r.confirm(out, msgConfirmApply) {
					return fail(msgNotConfirmed)
				}
			}
			// The approval names exactly the candidates printed above.
			approvals := make([]updates.Approval, 0, len(plan))
			for _, c := range plan {
				approvals = append(approvals, updates.Approval{CandidateID: c.ID, ToSHA: c.ToSHA})
			}
			var results []updates.ApplyResult
			if all {
				results = a.ApplyAll(r.ctx, approvals)
			} else {
				for _, ap := range approvals {
					res, _ := a.Apply(r.ctx, ap) //nolint:errcheck // the error is res.Err, reported per target.
					results = append(results, res)
				}
			}
			return reportApply(out, plan, results)
		},
	}
	c.Flags().BoolVar(&all, "all", false, flagAll)
	c.Flags().BoolVar(&yes, "yes", false, flagYes)
	c.Flags().BoolVar(&dryRun, "dry-run", false, flagDryRun)
	return c
}

// applyPlan returns the candidates to apply. Named skills must each have a
// candidate that is not blocked; --all leaves out the locally modified and
// the blocked ones and says which.
func (r *runner) applyPlan(out io.Writer, a *app.App, refs []string, all bool) ([]domain.UpdateCandidate, error) {
	var plan []domain.UpdateCandidate
	if !all {
		for _, ref := range refs {
			c, err := r.candidateFor(a, ref)
			if err != nil {
				return nil, err
			}
			if c.State == domain.CandidateBlocked {
				return nil, fail(msgBlocked, c.Target, text.BlockReason(c.BlockReason))
			}
			if !containsCandidate(plan, c.ID) {
				plan = append(plan, c)
			}
		}
		return plan, nil
	}
	cands, err := a.Candidates()
	if err != nil {
		return nil, err
	}
	var left []string
	for _, c := range cands {
		switch {
		case c.State == domain.CandidateBlocked:
			left = append(left, fmt.Sprintf(fmtExcludedBlock, c.Target, text.BlockReason(c.BlockReason)))
		case isLocallyModified(c):
			left = append(left, fmt.Sprintf(fmtExcludedLocal, c.Target))
		default:
			plan = append(plan, c)
		}
	}
	if len(left) > 0 {
		fmt.Fprint(out, msgExcludedHeader)
		tw := table(out)
		for _, l := range left {
			fmt.Fprint(tw, l)
		}
		if err := tw.Flush(); err != nil {
			return nil, err
		}
	}
	if len(plan) == 0 {
		fmt.Fprint(out, msgNothingToApply)
	}
	return plan, nil
}

func containsCandidate(cs []domain.UpdateCandidate, id string) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}

// reportApply prints the result of each target; any rejection or failure
// makes the exit code 2.
func reportApply(out io.Writer, plan []domain.UpdateCandidate, results []updates.ApplyResult) error {
	failed := false
	for i, res := range results {
		target := res.Target
		if target == "" && i < len(plan) {
			target = plan[i].Target
		}
		switch res.Outcome {
		case updates.OutcomeApplied:
			fmt.Fprintf(out, fmtApplied, target, res.SnapshotID)
		case updates.OutcomeRejected:
			failed = true
			fmt.Fprintf(out, fmtRejected, target, explain(res.Err))
		case updates.OutcomeExcluded:
			fmt.Fprintf(out, fmtExcluded, target)
		default:
			failed = true
			fmt.Fprintf(out, fmtFailed, target, explain(res.Err))
		}
	}
	if failed {
		return fail(msgSomeNotApplied)
	}
	return nil
}

func (r *runner) skipCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "skip <skill> [--dry-run]",
		Short: skipShort,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			if err := r.refuseInterrupted(a); err != nil {
				return err
			}
			c, err := r.candidateFor(a, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				fmt.Fprintf(out, fmtSkipPlan, shortSHA(c.ToCommit), c.Target)
				fmt.Fprint(out, msgDryRun)
				return nil
			}
			if err := a.Skip(r.ctx, updates.Approval{CandidateID: c.ID, ToSHA: c.ToSHA}); err != nil {
				return err
			}
			fmt.Fprintf(out, fmtSkipped, shortSHA(c.ToCommit), c.Target)
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, flagDryRun)
	return c
}
