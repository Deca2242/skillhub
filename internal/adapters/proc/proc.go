// Package proc runs external commands. It is the only package in skillhub
// that imports os/exec (enforced by internal/arch).
//
// A command is always a bare name looked up on PATH, never a path, and it
// cannot override PATH. That keeps the test shims that testsupport.Main puts
// first on PATH in front of every real tool: a command given as /usr/bin/gh,
// or a child started with another PATH, would otherwise bypass them. Its
// working directory, explicit or inherited, is never under a forbidden
// prefix.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// waitDelay bounds how long Run waits for output pipes after the process was
// killed, in case a grandchild still holds them open.
const waitDelay = time.Second

// Runner runs commands; it implements ports.ProcessRunner.
type Runner struct {
	guard paths.Guard
}

// New builds a Runner that refuses working directories under the prefixes
// listed in paths.ForbidPrefixEnv.
func New() Runner {
	return NewWithGuard(paths.NewGuard(os.Getenv(paths.ForbidPrefixEnv)))
}

// NewWithGuard builds a Runner that refuses working directories g forbids.
func NewWithGuard(g paths.Guard) Runner { return Runner{guard: g} }

// Run implements ports.ProcessRunner. Before starting anything it refuses a
// name that is not bare, an environment entry that sets PATH, and a working
// directory under a forbidden prefix, the current directory included when
// c.Dir is empty. A process that exited on its own is not an error, whatever
// its exit code and even when ctx ended right after; Run returns an error
// when c is refused, when the process cannot start, or when ctx ended before
// the process finished, in which case the process is killed and the error
// wraps ctx.Err().
func (r Runner) Run(ctx context.Context, c ports.Command) (ports.Result, error) {
	if c.Name == "" || strings.ContainsRune(c.Name, filepath.Separator) || strings.ContainsRune(c.Name, '/') {
		return ports.Result{}, fmt.Errorf("%w: %q must be a bare command name looked up on PATH", ports.ErrInvalidCommand, c.Name)
	}
	for _, entry := range c.Env {
		if key, _, _ := strings.Cut(entry, "="); key == "PATH" {
			return ports.Result{}, fmt.Errorf("%w: %s may not set PATH; it would hide the test shims from the processes it starts", ports.ErrInvalidCommand, c.Name)
		}
	}
	// Without Dir the child inherits this process's current directory, so
	// that is the directory to check.
	dir := c.Dir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ports.Result{}, fmt.Errorf("run %s: cannot read the current directory to check it: %w", c.Name, err)
		}
		dir = cwd
	}
	if err := r.guard.Check(dir); err != nil {
		return ports.Result{}, fmt.Errorf("run %s: %w", c.Name, err)
	}

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	// The checked directory, not whatever the current one is by now.
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := ports.Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	// A process that exited on its own is reported as such, even when ctx
	// ended after it exited but before Run returned (for instance while a
	// grandchild still held the output pipes).
	if state := cmd.ProcessState; state != nil && state.Exited() && exitedNormally(err) {
		res.ExitCode = state.ExitCode()
		return res, nil
	}
	res.ExitCode = -1
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, fmt.Errorf("run %s: %w", c.Name, ctxErr)
	}
	return res, fmt.Errorf("run %s: %w", c.Name, err)
}

// exitedNormally reports whether the error of a process that exited on its
// own carries nothing beyond its exit: no error, its exit status, or the
// context ending after the exit.
func exitedNormally(err error) bool {
	var exitErr *exec.ExitError
	return err == nil ||
		errors.As(err, &exitErr) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}
