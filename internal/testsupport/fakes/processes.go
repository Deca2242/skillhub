package fakes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"skillhub/internal/shared/ports"
)

// ---------------------------------------------------------------------------
// Git

var (
	// ErrUnknownClone reports a root the fake Git does not know.
	ErrUnknownClone = errors.New("fake git: unknown clone")
	// ErrUnknownCommit reports a commit the clone does not have.
	ErrUnknownCommit = errors.New("fake git: unknown commit")
	// ErrNotFastForward reports a fast-forward git would refuse: a dirty
	// clone, or a target that HEAD is not an ancestor of.
	ErrNotFastForward = errors.New("fake git: not a fast-forward")
)

type fakeCommit struct {
	parent string
	tree   string
}

// Clone is one clone of the fake Git. Commits are either local, or remote
// until Fetch brings them in.
type Clone struct {
	Root string

	mu     *sync.Mutex
	status ports.CloneStatus
	local  map[string]fakeCommit
	remote map[string]fakeCommit
}

// AddCommit adds a commit the clone already has.
func (c *Clone) AddCommit(sha, parent, tree string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.local[sha] = fakeCommit{parent: parent, tree: tree}
}

// AddRemoteCommit adds a commit only origin has, until Fetch.
func (c *Clone) AddRemoteCommit(sha, parent, tree string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remote[sha] = fakeCommit{parent: parent, tree: tree}
}

// SetStatus replaces what Describe reports.
func (c *Clone) SetStatus(status ports.CloneStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
}

// Git is an in-memory ports.GitPort over scripted clones. It never touches
// files: a fast-forward only moves Head.
type Git struct {
	Faults

	mu     sync.Mutex
	clones map[string]*Clone
}

// NewGit builds a Git with no clones.
func NewGit() *Git { return &Git{clones: map[string]*Clone{}} }

// AddClone adds a clone at root with status.
func (g *Git) AddClone(root string, status ports.CloneStatus) *Clone {
	c := &Clone{Root: root, mu: &g.mu, status: status, local: map[string]fakeCommit{}, remote: map[string]fakeCommit{}}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clones[root] = c
	return c
}

func (g *Git) cloneLocked(root string) (*Clone, error) {
	c, ok := g.clones[root]
	if !ok {
		return nil, fmt.Errorf("%s: %w", root, ErrUnknownClone)
	}
	return c, nil
}

// Describe implements ports.GitPort.
func (g *Git) Describe(_ context.Context, root string) (ports.CloneStatus, error) {
	if err := g.enter("Describe", root); err != nil {
		return ports.CloneStatus{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c, err := g.cloneLocked(root)
	if err != nil {
		return ports.CloneStatus{}, err
	}
	return c.status, nil
}

// Fetch implements ports.GitPort.
func (g *Git) Fetch(_ context.Context, root, commit string) error {
	if err := g.enter("Fetch", root, commit); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c, err := g.cloneLocked(root)
	if err != nil {
		return err
	}
	if _, ok := c.local[commit]; ok {
		return nil
	}
	rc, ok := c.remote[commit]
	if !ok {
		return fmt.Errorf("fetch %s: %w", commit, ErrUnknownCommit)
	}
	// Fetching a commit brings its ancestors too.
	for sha, fc := commit, rc; ; {
		c.local[sha] = fc
		next, ok := c.remote[fc.parent]
		if fc.parent == "" || !ok {
			return nil
		}
		sha, fc = fc.parent, next
	}
}

// IsAncestor implements ports.GitPort.
func (g *Git) IsAncestor(_ context.Context, root, ancestor, descendant string) (bool, error) {
	if err := g.enter("IsAncestor", root, ancestor, descendant); err != nil {
		return false, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c, err := g.cloneLocked(root)
	if err != nil {
		return false, err
	}
	return c.isAncestorLocked(ancestor, descendant)
}

func (c *Clone) isAncestorLocked(ancestor, descendant string) (bool, error) {
	if _, ok := c.local[ancestor]; !ok {
		return false, fmt.Errorf("%s: %w", ancestor, ErrUnknownCommit)
	}
	for sha := descendant; sha != ""; {
		if sha == ancestor {
			return true, nil
		}
		fc, ok := c.local[sha]
		if !ok {
			return false, fmt.Errorf("%s: %w", sha, ErrUnknownCommit)
		}
		sha = fc.parent
	}
	return false, nil
}

// FastForward implements ports.GitPort.
func (g *Git) FastForward(_ context.Context, root, commit string) error {
	if err := g.enter("FastForward", root, commit); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c, err := g.cloneLocked(root)
	if err != nil {
		return err
	}
	if _, ok := c.local[commit]; !ok {
		return fmt.Errorf("fast-forward to %s: %w", commit, ErrUnknownCommit)
	}
	if !c.status.Clean {
		return fmt.Errorf("fast-forward %s: the working tree is not clean: %w", root, ErrNotFastForward)
	}
	ok, err := c.isAncestorLocked(c.status.Head, commit)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("fast-forward %s to %s: %w", root, commit, ErrNotFastForward)
	}
	c.status.Head = commit
	return nil
}

// TreeSHA implements ports.GitPort.
func (g *Git) TreeSHA(_ context.Context, root, commit string) (string, error) {
	if err := g.enter("TreeSHA", root, commit); err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c, err := g.cloneLocked(root)
	if err != nil {
		return "", err
	}
	fc, ok := c.local[commit]
	if !ok {
		return "", fmt.Errorf("%s: %w", commit, ErrUnknownCommit)
	}
	return fc.tree, nil
}

// ---------------------------------------------------------------------------
// Notifier

// Notification is one notification sent through the fake Notifier.
type Notification struct {
	Title string
	Body  string
}

// Notifier is an in-memory ports.Notifier that keeps what it sent.
type Notifier struct {
	Faults

	mu   sync.Mutex
	sent []Notification
}

// NewNotifier builds a Notifier that sent nothing.
func NewNotifier() *Notifier { return &Notifier{} }

// Notify implements ports.Notifier.
func (n *Notifier) Notify(_ context.Context, title, body string) error {
	if err := n.enter("Notify", title, body); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, Notification{Title: title, Body: body})
	return nil
}

// Sent returns the notifications sent, oldest first.
func (n *Notifier) Sent() []Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.sent)
}

// ---------------------------------------------------------------------------
// BackgroundService

// BackgroundService is an in-memory ports.BackgroundService.
type BackgroundService struct {
	Faults

	mu     sync.Mutex
	status ports.BackgroundStatus
	binary string
}

// NewBackgroundService builds a service with no units installed.
func NewBackgroundService() *BackgroundService { return &BackgroundService{} }

// Status implements ports.BackgroundService.
func (b *BackgroundService) Status(_ context.Context) (ports.BackgroundStatus, error) {
	if err := b.enter("Status"); err != nil {
		return ports.BackgroundStatus{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status, nil
}

// Enable implements ports.BackgroundService.
func (b *BackgroundService) Enable(_ context.Context, binary string) error {
	if err := b.enter("Enable", binary); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = ports.BackgroundStatus{Installed: true, Enabled: true, Active: true}
	b.binary = binary
	return nil
}

// Disable implements ports.BackgroundService.
func (b *BackgroundService) Disable(_ context.Context) error {
	if err := b.enter("Disable"); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = ports.BackgroundStatus{}
	b.binary = ""
	return nil
}

// Binary is the binary the units point at; empty when not enabled.
func (b *BackgroundService) Binary() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.binary
}

// ---------------------------------------------------------------------------
// ProcessRunner

// ErrUnexpectedCommand reports a command no expectation matched.
var ErrUnexpectedCommand = errors.New("fake process runner: unexpected command")

type expectation struct {
	name   string
	args   []string
	result ports.Result
	err    error
	always bool
	used   bool
}

// ProcessRunner is a ports.ProcessRunner that replays recorded output. It
// refuses what the real runner refuses before starting anything: a name
// that is not bare and an environment entry that sets PATH.
type ProcessRunner struct {
	mu       sync.Mutex
	expected []*expectation
	commands []ports.Command
}

// NewProcessRunner builds a runner that expects nothing.
func NewProcessRunner() *ProcessRunner { return &ProcessRunner{} }

// Expect makes the next call with cmd's name and args return result and
// err, once.
func (p *ProcessRunner) Expect(cmd ports.Command, result ports.Result, err error) {
	p.add(cmd, result, err, false)
}

// ExpectAlways makes every call with cmd's name and args return result and
// err.
func (p *ProcessRunner) ExpectAlways(cmd ports.Command, result ports.Result, err error) {
	p.add(cmd, result, err, true)
}

func (p *ProcessRunner) add(cmd ports.Command, result ports.Result, err error, always bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expected = append(p.expected, &expectation{
		name: cmd.Name, args: slices.Clone(cmd.Args), result: result, err: err, always: always,
	})
}

// Run implements ports.ProcessRunner.
func (p *ProcessRunner) Run(_ context.Context, cmd ports.Command) (ports.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cmd.Args = slices.Clone(cmd.Args)
	cmd.Env = slices.Clone(cmd.Env)
	p.commands = append(p.commands, cmd)
	if cmd.Name == "" || strings.ContainsRune(cmd.Name, '/') {
		return ports.Result{}, fmt.Errorf("%w: %q must be a bare command name", ports.ErrInvalidCommand, cmd.Name)
	}
	for _, entry := range cmd.Env {
		if key, _, _ := strings.Cut(entry, "="); key == "PATH" {
			return ports.Result{}, fmt.Errorf("%w: %s may not set PATH", ports.ErrInvalidCommand, cmd.Name)
		}
	}
	for _, e := range p.expected {
		if e.name == cmd.Name && slices.Equal(e.args, cmd.Args) && (e.always || !e.used) {
			e.used = true
			res := e.result
			res.Stdout = slices.Clone(res.Stdout)
			res.Stderr = slices.Clone(res.Stderr)
			return res, e.err
		}
	}
	return ports.Result{ExitCode: -1}, fmt.Errorf("%w: %s %s", ErrUnexpectedCommand, cmd.Name, strings.Join(cmd.Args, " "))
}

// Commands returns every command Run received, refused ones included.
func (p *ProcessRunner) Commands() []ports.Command {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.commands)
}

// Unused lists the once-only expectations no call consumed, as
// "name args".
func (p *ProcessRunner) Unused() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.expected {
		if !e.always && !e.used {
			out = append(out, strings.TrimSpace(e.name+" "+strings.Join(e.args, " ")))
		}
	}
	return out
}
