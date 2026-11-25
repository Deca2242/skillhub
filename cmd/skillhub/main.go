// Command skillhub manages the agent skills the user installed. This is the
// composition root: it builds Paths from the environment and every real
// adapter, and hands them to an entry point.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"skillhub/internal/adapters/clock"
	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/git"
	"skillhub/internal/adapters/github"
	"skillhub/internal/adapters/httpsget"
	"skillhub/internal/adapters/notify"
	"skillhub/internal/adapters/proc"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/skillssh"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/adapters/systemd"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run starts the entry point args ask for. With no command, in a terminal,
// skillhub opens the terminal UI (docs/design.md section 9).
func run(args []string, stdin *os.File, stdout, stderr *os.File) int {
	interactive := isTerminal(stdin) && isTerminal(stdout)
	if len(args) == 0 && interactive {
		args = []string{"tui"}
	}
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	return cli.Run(args, cli.Env{
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Interactive: interactive,
		Executable:  exe,
		NewApp:      compose,
		TUI: func(out, errOut io.Writer) int {
			a, err := compose()
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 2
			}
			return tui.Run(a, stdin, out, errOut)
		},
	})
}

// isTerminal reports whether f is a character device, such as a terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// compose builds the facade over the real adapters. Every command a real
// tool runs goes through the one process runner.
func compose() (*app.App, error) {
	return composeWith(func(p paths.Paths) ports.ProcessRunner { return proc.NewWithGuard(p.Guard()) })
}

// composeWith is compose over the process runner newRunner builds.
func composeWith(newRunner func(paths.Paths) ports.ProcessRunner) (*app.App, error) {
	p, err := paths.FromOS()
	if err != nil {
		return nil, fmt.Errorf("resolve the paths: %w", err)
	}
	runner := newRunner(p)
	files := fsadapter.NewFileOps(p.Guard())
	agents, err := data.LoadAgents(p, files)
	if err != nil {
		return nil, fmt.Errorf("load the agents table: %w", err)
	}
	gh, err := github.New(runner, p)
	if err != nil {
		return nil, fmt.Errorf("prepare GitHub: %w", err)
	}
	// One lock store: version 1 rewrites entries through it, and the engine
	// takes it both as its lock and as the one that drops an empty lock.
	lock := skillslock.New(p, files)
	web, err := httpsget.New(runner)
	if err != nil {
		return nil, fmt.Errorf("prepare the website reader: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return app.New(app.Deps{
		Paths:       p,
		Agents:      agents,
		Files:       files,
		Dirs:        fsadapter.NewScanner(p.Guard()),
		Lock:        lock,
		ManagedLock: lock,
		Links:       fsadapter.NewLinkOps(p.Guard()),
		State:       statefile.New(p),
		Settings:    settings.New(p),
		GitHub:      gh,
		Git:         git.New(runner),
		Notifier:    notify.New(runner),
		Background:  systemd.New(p, runner, files),
		Clock:       clock.System{},
		Catalog:     catalogFor(runner, os.Getenv),
		Web:         web,
		ProjectDir:  cwd,
		Project:     fsadapter.NewProjectOps(p.Guard()),
	})
}

// catalogFor builds the skills.sh catalog over runner. SKILLS_API_URL, the
// variable the official CLI reads, replaces the base URL. An unsafe one
// fails only the catalog calls, so every other command keeps working.
func catalogFor(runner ports.ProcessRunner, getenv func(string) string) ports.Catalog {
	c, err := skillssh.New(runner, getenv("SKILLS_API_URL"))
	if err != nil {
		return brokenCatalog{err: err}
	}
	return c
}

// brokenCatalog answers every search with the error that kept the real one
// from being built.
type brokenCatalog struct{ err error }

func (b brokenCatalog) Search(context.Context, string, int) ([]ports.CatalogSkill, error) {
	return nil, b.err
}
