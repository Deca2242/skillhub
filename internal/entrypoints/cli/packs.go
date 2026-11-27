package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func (r *runner) packsCmd() *cobra.Command {
	return group(
		"packs",
		text.PacksShort,
		r.packsListCmd(),
		r.packsCreateCmd(),
		r.packsShowCmd(),
		r.packsAddCmd(),
		r.packsRemoveCmd(),
	)
}

func (r *runner) packsListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   text.PacksListUse,
		Short: text.PacksListShort,
		Long:  text.PacksListLong,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			list, err := a.PackList()
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), list)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), text.PackList(list))
			return err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return c
}

func (r *runner) packsCreateCmd() *cobra.Command {
	var description string
	c := &cobra.Command{
		Use:   text.PacksCreateUse,
		Short: text.PacksCreateShort,
		Long:  text.PacksCreateLong,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			p, err := a.PackCreate(r.ctx, args[0], description)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), text.PackCreated, text.SafeLine(p.Name))
			return err
		},
	}
	c.Flags().StringVar(&description, "description", "", text.FlagPackDescription)
	return c
}

func (r *runner) packsShowCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   text.PacksShowUse,
		Short: text.PacksShowShort,
		Long:  text.PacksShowLong,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			v, err := a.PackShow(r.ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), text.PackShow(v))
			return err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, flagJSON)
	return c
}

func (r *runner) packsAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   text.PacksAddUse,
		Short: text.PacksAddShort,
		Long:  text.PacksAddLong,
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			p, err := a.PackAdd(r.ctx, args[0], args[1])
			if err != nil {
				if errors.Is(err, app.ErrSkillNotFound) {
					return fail(msgNotFound, args[1])
				}
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), text.PackMemberAdded, text.SafeLine(args[1]), text.SafeLine(p.Name))
			return err
		},
	}
}

func (r *runner) packsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   text.PacksRemoveUse,
		Short: text.PacksRemoveShort,
		Long:  text.PacksRemoveLong,
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			p, err := a.PackRemove(r.ctx, args[0], args[1])
			if err != nil {
				if errors.Is(err, app.ErrSkillNotFound) {
					return fail(msgNotFound, args[1])
				}
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), text.PackMemberRemoved, text.SafeLine(args[1]), text.SafeLine(p.Name))
			return err
		},
	}
}
