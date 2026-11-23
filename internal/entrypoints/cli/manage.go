package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

// The version 2 commands (docs/design-v2.md section 10): every one shows
// its plan with the identifier the approval binds, and runs it only on a
// yes in a terminal or with --yes --plan <id>, an identifier the user saw
// (section 11, point 8, and section 14). A plan that changed is refused and
// the regenerated one never runs. Every text comes from
// internal/entrypoints/text.

// approval holds the flags every version 2 command that writes takes.
type approval struct {
	yes    bool
	plan   string
	dryRun bool
}

func (ap *approval) flags(c *cobra.Command) {
	c.Flags().BoolVar(&ap.yes, "yes", false, text.FlagYes)
	c.Flags().StringVar(&ap.plan, "plan", "", text.FlagPlan)
	c.Flags().BoolVar(&ap.dryRun, "dry-run", false, text.FlagDryRun)
}

// check refuses --yes without --plan, and --plan without --yes, before
// anything is read.
func (ap approval) check() error {
	switch {
	case ap.yes && ap.plan == "":
		return fail(text.YesNeedsPlan)
	case ap.plan != "" && !ap.yes:
		return fail(text.PlanWithoutYes)
	}
	return nil
}

// token is the identifier the user approves. An install's plan covers the
// time its lock entry records, fixed when it was first planned; the token
// carries it, in Unix milliseconds, so that --yes --plan in another process
// plans exactly the same install again.
func token(p app.Proposal) string {
	if p.Request.Install != nil && p.PlanID != "" {
		return p.PlanID + "@" + strconv.FormatInt(p.Request.Install.At.UnixMilli(), 10)
	}
	return p.PlanID
}

// splitToken splits a token into the plan identifier and, for an install,
// the time it carries.
func splitToken(tok string) (id string, at time.Time, ok bool) {
	id, ms, found := strings.Cut(tok, "@")
	if !found {
		return id, time.Time{}, false
	}
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return id, time.Time{}, false
	}
	return id, time.UnixMilli(n).UTC(), true
}

// applyFn runs an approved plan.
type applyFn func(a *app.App, p app.Proposal, approvedID string) (app.Result, error)

func applyPlan(r *runner) applyFn {
	return func(a *app.App, p app.Proposal, id string) (app.Result, error) {
		return a.ApplyPlan(r.ctx, p.Request, id)
	}
}

// decide shows the plan p and runs it with apply once approved: --dry-run
// stops after showing it, --yes approves the identifier of --plan, and
// otherwise a terminal asks. req is what the user asked for, to explain a
// refusal.
func (r *runner) decide(cmd *cobra.Command, a *app.App, ap approval, p app.Proposal, apply applyFn) error {
	out := cmd.OutOrStdout()
	if p.PlanID == "" {
		if p.Repair != nil && p.Repair.Reinstall != nil {
			fmt.Fprint(out, text.Reinstall(p.Repair.Reinstall))
			return nil
		}
		return fail(text.NoPlanToApply)
	}
	fmt.Fprint(out, text.Proposal(p))
	fmt.Fprint(out, text.PlanID(token(p)))
	if ap.dryRun {
		// A dry run refuses what the run would refuse first (B5).
		if err := a.CheckPending(); err != nil {
			return r.refuse(err, p.Request, app.Proposal{})
		}
		fmt.Fprint(out, text.DryRun)
		return nil
	}
	approved := p.PlanID
	switch {
	case ap.yes:
		approved, _, _ = splitToken(ap.plan)
	case !r.env.Interactive:
		return fail(text.NeedsTerminal)
	case !r.confirm(out, text.ConfirmPlan):
		return fail(text.NotConfirmed)
	}
	res, err := apply(a, p, approved)
	return r.report(cmd, p.Request, res, err)
}

// report prints what a plan that ran did. Once the operation ran (its
// snapshot is set) it is a success, and an error after it only a warning
// (A4 of L51).
func (r *runner) report(cmd *cobra.Command, req app.Request, res app.Result, err error) error {
	if res.SnapshotID != "" {
		fmt.Fprint(cmd.OutOrStdout(), text.Done(res))
		if err != nil {
			fmt.Fprint(cmd.ErrOrStderr(), text.SupersedeWarning(err))
		}
		return nil
	}
	if err == nil {
		return nil
	}
	return r.refuse(err, req, res.Proposal)
}

// refuse explains an error of a version 2 operation. now is the plan as it
// is now, when planning again gave another one.
func (r *runner) refuse(err error, req app.Request, now app.Proposal) error {
	if errors.Is(err, app.ErrPlanChanged) && now.PlanID != "" {
		return fail("%s", text.PlanChanged(token(now)))
	}
	if msg, ok := text.Refusal(err, req); ok {
		return fail("%s", msg)
	}
	return err
}

// planned plans req and shows and runs it.
func (r *runner) planned(cmd *cobra.Command, ap approval, req app.Request) error {
	if err := ap.check(); err != nil {
		return err
	}
	a, err := r.facade(true)
	if err != nil {
		return err
	}
	p, err := a.Plan(r.ctx, req)
	if err != nil {
		return r.refuse(err, req, app.Proposal{})
	}
	return r.decide(cmd, a, ap, p, applyPlan(r))
}

func (r *runner) skillsCmd() *cobra.Command {
	return group("skills", text.SkillsShort,
		r.skillsShowCmd(),
		r.installCmd(),
		r.keyCmd(text.RemoveUse, text.RemoveShort, text.RemoveLong, app.ActionRemove),
		r.keyCmd(text.DisableUse, text.DisableShort, text.DisableLong, app.ActionDisable),
		r.keyCmd(text.EnableUse, text.EnableShort, "", app.ActionEnable),
		r.linkCmd(text.UnlinkUse, text.UnlinkShort, text.UnlinkLong, app.ActionUnlink, "from", text.FlagFrom),
		r.linkCmd(text.LinkUse, text.LinkShort, "", app.ActionLink, "to", text.FlagLinkTo),
		r.adoptOffCmd(),
		r.searchCmd(),
		r.popularCmd(),
	)
}

// keyCmd is a command that acts on one skill by its store key.
func (r *runner) keyCmd(use, short, long string, action app.Action) *cobra.Command {
	var ap approval
	c := &cobra.Command{
		Use: use, Short: short, Long: long, Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.planned(cmd, ap, app.Request{Action: action, Key: args[0]})
		},
	}
	ap.flags(c)
	return c
}

// linkCmd is unlink or link: a skill and the harness named by flag.
func (r *runner) linkCmd(use, short, long string, action app.Action, flag, usage string) *cobra.Command {
	var ap approval
	var harness string
	c := &cobra.Command{
		Use: use, Short: short, Long: long, Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if harness == "" {
				return fail("%s", text.NeedsHarness(cmd.CommandPath(), flag))
			}
			return r.planned(cmd, ap, app.Request{Action: action, Key: args[0], Harness: harness})
		},
	}
	c.Flags().StringVar(&harness, flag, "", usage)
	ap.flags(c)
	return c
}

func (r *runner) adoptOffCmd() *cobra.Command {
	var ap approval
	c := &cobra.Command{
		Use: text.AdoptOffUse, Short: text.AdoptOffShort, Long: text.AdoptOffLong, Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.planned(cmd, ap, app.Request{Action: app.ActionAdoptOff})
		},
	}
	ap.flags(c)
	return c
}

func (r *runner) installCmd() *cobra.Command {
	var ap approval
	var targets []string
	c := &cobra.Command{
		Use: text.InstallUse, Short: text.InstallShort, Long: text.InstallLong, Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ap.check(); err != nil {
				return err
			}
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			return r.reviewed(cmd, a, ap, app.ActionInstall, args[0], targets)
		},
	}
	c.Flags().StringSliceVar(&targets, "to", nil, text.FlagTargets)
	ap.flags(c)
	return c
}

// reviewed resolves input and shows its review, the diff of the prepared
// revision the plan covers (A2 of L51), then plans installing it, or
// reinstalling it when action is ActionReinstall, in targets (the proposed
// ones when there are none), and shows and runs that plan. With --yes, the
// plan is made again with the time the approved token carries. A revision
// that does not run, shown, refused or not confirmed, is discarded: the home
// stays as it was (B2).
func (r *runner) reviewed(cmd *cobra.Command, a *app.App, ap approval, action app.Action, input string, targets []string) (err error) {
	req := app.Request{Action: action, Install: &app.InstallRequest{Revision: app.Revision{Input: input}}}
	rev, err := a.ResolveInstall(r.ctx, input)
	if err != nil {
		return r.refuse(err, req, app.Proposal{})
	}
	ran := false
	defer func() {
		if !ran {
			err = errors.Join(err, a.DiscardRevision(rev))
		}
	}()
	req.Install = &app.InstallRequest{Revision: rev}
	if len(targets) == 0 {
		if targets, err = a.ProposeTargets(r.ctx); err != nil {
			return err
		}
	}
	diffs, err := a.RevisionDiff(r.ctx, rev)
	if err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), text.InstallReview(rev, diffs))
	var p app.Proposal
	switch _, at, ok := splitToken(ap.plan); {
	case ap.yes && ok:
		// The approved plan: the same revision, targets and time.
		req.Install = &app.InstallRequest{Revision: rev, Targets: targets, At: at}
		p, err = a.Plan(r.ctx, req)
	case action == app.ActionReinstall:
		p, err = a.PlanReinstall(r.ctx, rev, targets)
	default:
		p, err = a.PlanInstall(r.ctx, rev, targets)
	}
	if err != nil {
		return r.refuse(err, req, app.Proposal{})
	}
	return r.decide(cmd, a, ap, p, func(a *app.App, p app.Proposal, id string) (app.Result, error) {
		res, err := a.ApplyPlan(r.ctx, p.Request, id)
		ran = res.SnapshotID != ""
		return res, err
	})
}

func (r *runner) doctorCmd() *cobra.Command {
	c := &cobra.Command{
		Use: text.DoctorUse, Short: text.DoctorShort, Long: text.DoctorLong,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fail(msgUnknownCommand, cmd.Name()+" "+args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			findings, err := a.Doctor(r.ctx)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), text.Findings(findings))
			return nil
		},
	}
	c.AddCommand(r.fixCmd())
	return c
}

// reinstallRepair is Doctor's repair of a lock entry without its folder.
const reinstallRepair = "reinstall"

func (r *runner) fixCmd() *cobra.Command {
	var ap approval
	var kind, canonical string
	var targets []string
	c := &cobra.Command{
		Use: text.FixUse, Short: text.FixShort, Long: text.FixLong, Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ap.check(); err != nil {
				return err
			}
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			req := app.Request{Action: app.ActionRepair, Finding: args[0], Repair: kind, Canonical: canonical}
			if kind == "" {
				o, err := a.Repairs(r.ctx, args[0])
				if err != nil {
					return r.refuse(err, req, app.Proposal{})
				}
				switch len(o.Repairs) {
				case 0:
					return fail("%s", text.NoRepair(o))
				case 1:
					req.Repair = o.Repairs[0]
				default:
					return fail("%s", text.ChooseRepair(o))
				}
			}
			if len(targets) > 0 && req.Repair != reinstallRepair {
				return fail(text.ToNeedsReinstall)
			}
			p, err := a.Plan(r.ctx, req)
			if err != nil {
				return r.refuse(err, req, app.Proposal{})
			}
			if p.PlanID == "" && p.Repair != nil && p.Repair.Reinstall != nil && p.Repair.Reinstall.Input != "" {
				// The reinstall repair has no plan of its own: it is the
				// install review of the entry's source, then a plan that
				// replaces the entry.
				fmt.Fprint(cmd.OutOrStdout(), text.Reinstall(p.Repair.Reinstall))
				return r.reviewed(cmd, a, ap, app.ActionReinstall, p.Repair.Reinstall.Input, targets)
			}
			return r.decide(cmd, a, ap, p, applyPlan(r))
		},
	}
	c.Flags().StringVar(&kind, "repair", "", text.FlagRepair)
	c.Flags().StringVar(&canonical, "canonical", "", text.FlagCanonical)
	c.Flags().StringSliceVar(&targets, "to", nil, text.FlagFixTo)
	ap.flags(c)
	return c
}

// undoV2 undoes the version 2 operation of the journal line target, the
// one of snapshot or, without it, the last one, through the same approval
// as every plan.
func (r *runner) undoV2(cmd *cobra.Command, ap approval, target, snapshot string) error {
	if err := ap.check(); err != nil {
		return err
	}
	a, err := r.facade(true)
	if err != nil {
		return err
	}
	journal, err := a.History()
	if err != nil {
		return err
	}
	var line *domain.HistoryEntry
	for i := range journal {
		h := &journal[i]
		if h.Target == target && h.SnapshotID != "" && (snapshot == "" || h.SnapshotID == snapshot) {
			line = h
		}
	}
	if line == nil {
		return fail("%s", text.UndoLineNotFound(target, snapshot))
	}
	req := app.Request{Action: app.ActionUndo, Snapshot: line.SnapshotID}
	p, err := a.PlanUndo(r.ctx, line.SnapshotID)
	if err != nil {
		return r.refuse(err, req, app.Proposal{})
	}
	return r.decide(cmd, a, ap, p, func(a *app.App, p app.Proposal, id string) (app.Result, error) {
		done, err := a.UndoOperation(r.ctx, app.UndoLine{Target: line.Target, SnapshotID: line.SnapshotID, PlanID: id})
		return done.V2, err
	})
}

// recoverV2 resolves a pending version 2 operation and tells what it did,
// and whether a version 1 operation is still pending after it.
func (r *runner) recoverV2(out io.Writer, a *app.App, rec app.Recovery, err error) error {
	if err != nil {
		if msg, ok := text.Refusal(err, app.Request{}); ok {
			return fail("%s", msg)
		}
		return fail("%s", text.RecoverFailed(err))
	}
	fmt.Fprint(out, text.Recovery(rec.V2))
	if _, ok, err := a.Interrupted(); err == nil && ok {
		fmt.Fprint(out, text.V1StillPending)
	}
	return nil
}
