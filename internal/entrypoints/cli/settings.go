package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
)

func (r *runner) settingsCmd() *cobra.Command {
	show := &cobra.Command{
		Use:   "show",
		Short: settingsShowS,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			s, err := a.Settings()
			if err != nil {
				return err
			}
			printSettings(cmd, s)
			return nil
		},
	}
	var dryRun bool
	set := &cobra.Command{
		Use:   "set <key> <value> [--dry-run]",
		Short: settingsSetS,
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			s, err := a.SetSetting(args[0], args[1], dryRun)
			switch {
			case errors.Is(err, app.ErrUnknownSetting):
				return fail(msgUnknownSetting, args[0])
			case errors.Is(err, domain.ErrIntervalTooShort):
				return fail(msgIntervalTooShort)
			case errors.Is(err, app.ErrInvalidSettingValue):
				return fail(msgInvalidSetting, args[0], args[1])
			case err != nil:
				return err
			}
			printSettings(cmd, s)
			if dryRun {
				fmt.Fprint(cmd.OutOrStdout(), msgDryRun)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), msgSaved)
			}
			return nil
		},
	}
	set.Flags().BoolVar(&dryRun, "dry-run", false, flagDryRun)
	return group("settings", settingsShort, show, set)
}

func printSettings(cmd *cobra.Command, s domain.Settings) {
	ignore := msgNone
	if len(s.Ignore) > 0 {
		ignore = strings.Join(s.Ignore, ", ")
	}
	fmt.Fprintf(cmd.OutOrStdout(), fmtSettings, formatDuration(s.CheckInterval), yesNo(s.Notifications), ignore)
}

func (r *runner) backgroundCmd() *cobra.Command {
	status := &cobra.Command{
		Use:   "status",
		Short: bgStatusShort,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			st, err := a.BackgroundStatus(r.ctx)
			if err != nil {
				return err
			}
			on := msgBgOff
			if st.Enabled {
				on = msgBgOn
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, fmtBgStatus, on, yesNo(st.Installed), yesNo(st.Active))
			if !st.Enabled {
				fmt.Fprint(out, msgBgHowTo)
			}
			return nil
		},
	}
	var enableDry, disableDry bool
	enable := &cobra.Command{
		Use:   "enable [--dry-run]",
		Short: bgEnableShort,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			if r.env.Executable == "" {
				return fail(msgNoExecutable)
			}
			out := cmd.OutOrStdout()
			if enableDry {
				fmt.Fprintf(out, fmtBgEnablePlan, a.Paths().SystemdUserDir(), r.env.Executable)
				fmt.Fprint(out, msgDryRun)
				return nil
			}
			if err := a.EnableBackground(r.ctx, r.env.Executable); err != nil {
				return err
			}
			fmt.Fprint(out, msgBgEnabled)
			return nil
		},
	}
	enable.Flags().BoolVar(&enableDry, "dry-run", false, flagDryRun)
	disable := &cobra.Command{
		Use:   "disable [--dry-run]",
		Short: bgDisableShort,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := r.facade(true)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if disableDry {
				fmt.Fprint(out, msgBgDisablePl)
				fmt.Fprint(out, msgDryRun)
				return nil
			}
			if err := a.DisableBackground(r.ctx); err != nil {
				return err
			}
			fmt.Fprint(out, msgBgDisabled)
			return nil
		},
	}
	disable.Flags().BoolVar(&disableDry, "dry-run", false, flagDryRun)
	return group("background", backgroundShort, status, enable, disable)
}
