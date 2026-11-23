package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/updates"
)

func (r *runner) historyCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "history [--json]",
		Short: historyShort,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			entries, err := a.History()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, entries)
			}
			if len(entries) == 0 {
				fmt.Fprint(out, msgNoHistory)
				return nil
			}
			tw := table(out)
			fmt.Fprint(tw, hdrHistory)
			for _, e := range entries {
				fmt.Fprintf(tw, fmtHistory, formatTime(e.At), text.HistoryAction(e.Action), text.SafeLine(e.Target),
					shortSHA(e.FromSHA), shortSHA(e.ToSHA), orNone(text.SafeLine(e.SnapshotID)), orNone(text.SafeLine(e.Detail)))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return c
}

func (r *runner) undoCmd() *cobra.Command {
	var to string
	var dryRun, yes bool
	var plan string
	c := &cobra.Command{
		Use:   "undo <skill> [--to <snapshot>] [--dry-run] [--yes --plan <id>]",
		Short: undoShort,
		Long:  text.UndoLong,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.HasPrefix(args[0], "v2:") {
				return r.undoV2(cmd, approval{yes: yes, plan: plan, dryRun: dryRun}, args[0], to)
			}
			if yes || plan != "" {
				return fail(text.UndoFlagsV2Only)
			}
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			if err := r.refuseInterrupted(a); err != nil {
				return err
			}
			target, err := r.resolve(a, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				// The undo itself refuses while a version 2 operation is
				// pending; its dry run says so too (A5 of L51).
				if pending, err := a.ManagedPending(); err != nil || pending {
					if err != nil {
						return err
					}
					return fail(text.V2OperationPending)
				}
				from := msgUndoLatest
				if to != "" {
					from = fmt.Sprintf(fmtUndoSnapshot, text.SafeLine(to))
				}
				fmt.Fprintf(out, fmtUndoPlan, text.SafeLine(target), from)
				fmt.Fprint(out, msgDryRun)
				return nil
			}
			req := updates.UndoRequest{Target: target, SnapshotID: to}
			res, err := a.Undo(r.ctx, req)
			if errors.Is(err, updates.ErrUndoNeedsConfirmation) {
				// The current folder is already saved, with keep set.
				fmt.Fprintf(out, fmtUndoChanged, text.SafeLine(target), text.SafeLine(res.SavedSnapshotID))
				if !r.env.Interactive {
					return fail(msgUndoNeedsTerminal)
				}
				if !r.confirm(out, msgConfirmUndo) {
					return fail(msgNotConfirmed)
				}
				req.SnapshotID, req.Confirm = res.SnapshotID, true
				res, err = a.Undo(r.ctx, req)
			}
			switch {
			case errors.Is(err, updates.ErrNothingToUndo):
				return fail(msgNothingToUndo, text.SafeLine(target))
			case errors.Is(err, updates.ErrSnapshotNotFound):
				return fail(msgSnapshotNotFound, text.SafeLine(target))
			case err != nil:
				return err
			}
			fmt.Fprintf(out, fmtUndone, text.SafeLine(target), text.SafeLine(res.SnapshotID))
			if res.Detail != "" {
				fmt.Fprintf(out, fmtDetail, text.SafeLine(res.Detail))
			}
			return nil
		},
	}
	c.Flags().StringVar(&to, "to", "", flagTo)
	c.Flags().BoolVar(&dryRun, "dry-run", false, flagDryRun)
	c.Flags().BoolVar(&yes, "yes", false, text.FlagUndoYes)
	c.Flags().StringVar(&plan, "plan", "", text.FlagUndoPlan)
	return c
}

func (r *runner) recoverCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "recover [--dry-run]",
		Short: recoverShort,
		Long:  recoverLong,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(false)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				// A pending version 2 operation is what recover resolves
				// first (A5 of L51).
				pending, err := a.ManagedPending()
				if err != nil {
					return err
				}
				if pending {
					fmt.Fprint(out, text.ManagedPendingPlan)
					fmt.Fprint(out, msgDryRun)
					return nil
				}
				op, ok, err := a.Interrupted()
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprint(out, text.RecoverNoOp)
				} else {
					fmt.Fprintf(out, fmtRecoverPlan, text.SafeLine(op.Target), text.SafeLine(op.RestoreSnapshotID), opKind(op.Kind), formatTime(op.StartedAt))
				}
				if orphan, err := a.OrphanStaging(); err != nil {
					return err
				} else if orphan {
					fmt.Fprint(out, msgRecoverWouldClear)
				}
				fmt.Fprint(out, msgDryRun)
				return nil
			}
			rec, err := a.RecoverOperation(r.ctx)
			if rec.Version == 2 {
				return r.recoverV2(out, a, rec, err)
			}
			res := rec.V1
			switch {
			case errors.Is(err, updates.ErrNoOperation):
				fmt.Fprint(out, text.RecoverNoOp)
				return nil
			case err != nil:
				if msg, ok := text.Refusal(err, app.Request{}); ok {
					return fail("%s", msg)
				}
				return err
			default:
				fmt.Fprint(out, text.RecoveryV1(res, rec.Leftovers))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, flagDryRun)
	return c
}
