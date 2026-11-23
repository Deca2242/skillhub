// Package cli is the command-line entry point: the Cobra commands of
// docs/design.md section 9 over the use-case facade of internal/app.
//
// Every write is a user action: check never applies, and updates apply asks
// for a confirmation that covers only the candidates it printed, unless
// --yes. While an operation is interrupted every command warns, and apply,
// skip and undo refuse until recover.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/ports"
	"skillhub/internal/updates"
)

// Exit codes are part of the interface (docs/design.md section 9).
const (
	exitOK = 0
	// exitPending is check --exit-code with updates pending.
	exitPending = 1
	exitError   = 2
)

// Env is what the composition root hands the command line.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Interactive is true when stdin and stdout are a terminal: only then
	// does a command ask for a confirmation.
	Interactive bool
	// Executable is the path of the running binary, which background enable
	// points the timer at.
	Executable string
	// NewApp builds the facade; commands that need it call it once.
	NewApp func() (*app.App, error)
	// TUI runs the terminal UI and returns its exit code.
	TUI func(stdout, stderr io.Writer) int
}

// exitError carries an exit code; msg, when set, is printed to stderr.
type codeError struct {
	code int
	msg  string
}

func (e *codeError) Error() string { return e.msg }

// fail is an error exit with a message for the user.
func fail(format string, args ...any) error {
	return &codeError{code: exitError, msg: fmt.Sprintf(format, args...)}
}

// silent is an exit code whose reason was already printed.
func silent(code int) error { return &codeError{code: code} }

// runner holds one run's state.
type runner struct {
	env    Env
	ctx    context.Context
	in     *bufio.Reader
	app    *app.App
	warned bool
}

// Run runs the command line args and returns the exit code.
func Run(args []string, env Env) int {
	if env.Stdin == nil {
		env.Stdin = strings.NewReader("")
	}
	r := &runner{env: env, ctx: context.Background(), in: bufio.NewReader(env.Stdin)}
	root := r.root()
	root.SetArgs(args)
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	err := root.Execute()
	if err == nil {
		return exitOK
	}
	var ce *codeError
	if errors.As(err, &ce) {
		if ce.msg != "" {
			fmt.Fprintln(env.Stderr, msgErrorPrefix+ce.msg)
		}
		return ce.code
	}
	fmt.Fprintln(env.Stderr, msgErrorPrefix+explain(err))
	return exitError
}

// explain turns an error of the facade into a sentence for the user.
func explain(err error) string {
	var amb *app.AmbiguousError
	if msg, ok := text.CatalogError(err); ok {
		return msg
	}
	switch {
	case errors.As(err, &amb):
		ids := make([]string, len(amb.IDs))
		for i, id := range amb.IDs {
			ids[i] = text.SafeLine(id)
		}
		return fmt.Sprintf(msgAmbiguous, text.SafeLine(amb.Ref)) + "\n  " + strings.Join(ids, "\n  ")
	case errors.Is(err, updates.ErrInterrupted):
		return msgInterruptedRefuse
	case errors.Is(err, updates.ErrV2OperationPending):
		return text.V2OperationPending
	case errors.Is(err, ports.ErrStateLocked):
		return msgStateLocked
	default:
		return fmt.Sprintf(msgError, text.SafeLine(fmt.Sprint(err)))
	}
}

// facade builds the App once and warns about an interrupted operation; every
// command that reads skillhub's state goes through it.
func (r *runner) facade(warn bool) (*app.App, error) {
	if r.app == nil {
		a, err := r.env.NewApp()
		if err != nil {
			return nil, fail(msgCompose, text.SafeLine(fmt.Sprint(err)))
		}
		r.app = a
	}
	if warn && !r.warned {
		r.warned = true
		op, ok, err := r.app.Interrupted()
		if err != nil {
			return nil, err
		}
		if ok {
			fmt.Fprintf(r.env.Stderr, msgInterruptedWarning,
				opKind(op.Kind), text.SafeLine(op.Target), formatTime(op.StartedAt))
		}
	}
	return r.app, nil
}

// confirm asks a yes/no question on the terminal; anything but yes is no.
func (r *runner) confirm(out io.Writer, question string) bool {
	fmt.Fprint(out, question+msgConfirmYN)
	line, _ := r.in.ReadString('\n') //nolint:errcheck // EOF is a no, like an empty answer.
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "s", "si", "sí", "y", "yes":
		return true
	}
	return false
}

// refuseInterrupted refuses a command that changes something while an
// operation is interrupted, before it prints any plan.
func (r *runner) refuseInterrupted(a *app.App) error {
	_, ok, err := a.Interrupted()
	if err != nil {
		return err
	}
	if ok {
		return fail(msgInterruptedRefuse)
	}
	return nil
}

func (r *runner) root() *cobra.Command {
	root := &cobra.Command{
		Use:           "skillhub",
		Short:         rootShort,
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fail(msgUnknownCommand, args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolP("help", "h", false, helpFlagUsage)
	root.SetUsageTemplate(usageTemplate)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fail(msgBadFlag, translateFlagError(err), cmd.CommandPath())
	})
	root.SetHelpCommand(&cobra.Command{
		Use:   "help [comando]",
		Short: helpShort,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, _, err := cmd.Root().Find(args)
			if err != nil || target == nil {
				return fail(msgUnknownCommand, strings.Join(args, " "))
			}
			return target.Help()
		},
	})
	root.AddCommand(
		r.tuiCmd(), r.scanCmd(), r.checkCmd(), r.updatesCmd(), r.historyCmd(),
		r.undoCmd(), r.recoverCmd(), r.settingsCmd(), r.backgroundCmd(),
		r.skillsCmd(), r.doctorCmd(),
	)
	forEach(root, func(c *cobra.Command) { c.DisableFlagsInUseLine = true })
	return root
}

// translateFlagError puts pflag's English error prefixes in Spanish.
func translateFlagError(err error) string {
	msg := err.Error()
	for _, p := range flagErrorPrefixes {
		if rest, ok := strings.CutPrefix(msg, p[0]); ok {
			return p[1] + rest
		}
	}
	return msg
}

func forEach(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		forEach(sub, fn)
	}
}

// group is a command that only holds subcommands.
func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fail(msgUnknownCommand, cmd.Name()+" "+args[0])
			}
			return cmd.Help()
		},
	}
	c.AddCommand(subs...)
	return c
}

// exactArgs and minArgs are cobra's validators with Spanish messages.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return fail(msgArgsExact, cmd.CommandPath(), n, len(args))
		}
		return nil
	}
}

func (r *runner) tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: tuiShort,
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if code := r.env.TUI(cmd.OutOrStdout(), cmd.ErrOrStderr()); code != exitOK {
				return silent(code)
			}
			return nil
		},
	}
}
