package git_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/adapters/git"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

const (
	root = "/clones/skills"
	head = "1111111111111111111111111111111111111111"
	tree = "2222222222222222222222222222222222222222"
)

// prefix is what every git command starts with: hooks, the file system
// monitor, automatic maintenance, submodule recursion, the user's global
// attributes and end-of-line conversion off, no optional
// lock that would rewrite the index, and the clone named explicitly so an
// inherited GIT_DIR or GIT_WORK_TREE cannot redirect git.
var prefix = []string{
	"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
	"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "submodule.recurse=false",
	"-c", "core.attributesFile=/dev/null", "-c", "core.autocrlf=false",
	"--no-optional-locks", "--git-dir=" + root + "/.git", "--work-tree=" + root,
}

func gitCmd(args ...string) ports.Command {
	return ports.Command{Name: "git", Args: append(slices.Clone(prefix), args...)}
}

var (
	statusCmd = gitCmd("status", "--porcelain=v2", "--branch", "--untracked-files=all", "--ignore-submodules=none")
	remoteCmd = gitCmd("remote", "get-url", "origin")
)

func ok(stdout string) ports.Result { return ports.Result{Stdout: []byte(stdout)} }

// readOnly lists the git subcommands the read-only operations may run.
var readOnly = map[string]bool{"status": true, "remote": true, "merge-base": true, "rev-parse": true}

// checkCommands asserts that every command ran in the clone, with hooks
// disabled, git discovery stopped at the clone, and a read-only subcommand.
func checkCommands(t *testing.T, runner *fakes.ProcessRunner) {
	t.Helper()
	checkCommandsAllowing(t, runner, readOnly)
}

// checkCommandsAllowing is checkCommands with the allowed subcommands.
func checkCommandsAllowing(t *testing.T, runner *fakes.ProcessRunner, allowed map[string]bool) {
	t.Helper()
	cmds := runner.Commands()
	if len(cmds) == 0 {
		t.Fatal("no git command ran")
	}
	for _, c := range cmds {
		line := c.Name + " " + strings.Join(c.Args, " ")
		if c.Name != "git" {
			t.Errorf("%s: want git", line)
		}
		if c.Dir != root {
			t.Errorf("%s: Dir = %q, want the clone %q", line, c.Dir, root)
		}
		if len(c.Args) <= len(prefix) || !slices.Equal(c.Args[:len(prefix)], prefix) {
			t.Errorf("%s: does not start with %v", line, prefix)
			continue
		}
		if sub := c.Args[len(prefix)]; !allowed[sub] {
			t.Errorf("%s: subcommand %q is not allowed here", line, sub)
		}
		if !slices.Contains(c.Env, "GIT_CEILING_DIRECTORIES=/clones") {
			t.Errorf("%s: Env = %v, want GIT_CEILING_DIRECTORIES=/clones so git never describes a parent repository", line, c.Env)
		}
		if !slices.Contains(c.Env, "GIT_ATTR_NOSYSTEM=1") {
			t.Errorf("%s: Env = %v, want GIT_ATTR_NOSYSTEM=1 so no system attribute applies a filter or an eol", line, c.Env)
		}
		if len(c.Stdin) != 0 {
			t.Errorf("%s: unexpected stdin", line)
		}
	}
	if unused := runner.Unused(); len(unused) > 0 {
		t.Errorf("expected commands not run: %v", unused)
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name   string
		status string
		remote ports.Result
		want   ports.CloneStatus
	}{
		{
			name:   "clean branch tracking origin",
			status: "# branch.oid " + head + "\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -0\n",
			remote: ok("https://github.com/acme/skills.git\n"),
			want:   ports.CloneStatus{Remote: "https://github.com/acme/skills.git", Branch: "main", Upstream: "origin/main", Head: head, Clean: true},
		},
		{
			name:   "modified tracked file",
			status: "# branch.oid " + head + "\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -0\n1 .M N... 100644 100644 100644 aaa aaa SKILL.md\n",
			remote: ok("https://github.com/acme/skills.git\n"),
			want:   ports.CloneStatus{Remote: "https://github.com/acme/skills.git", Branch: "main", Upstream: "origin/main", Head: head},
		},
		{
			name:   "untracked file",
			status: "# branch.oid " + head + "\n# branch.head main\n# branch.upstream origin/main\n? notes.md\n",
			remote: ok("git@github.com:acme/skills.git\n"),
			want:   ports.CloneStatus{Remote: "git@github.com:acme/skills.git", Branch: "main", Upstream: "origin/main", Head: head},
		},
		{
			name:   "unmerged file",
			status: "# branch.oid " + head + "\n# branch.head main\nu UU N... 100644 100644 100644 100644 a b c SKILL.md\n",
			remote: ok("https://github.com/acme/skills\n"),
			want:   ports.CloneStatus{Remote: "https://github.com/acme/skills", Branch: "main", Head: head},
		},
		{
			name:   "detached HEAD",
			status: "# branch.oid " + head + "\n# branch.head (detached)\n",
			remote: ok("https://github.com/acme/skills\n"),
			want:   ports.CloneStatus{Remote: "https://github.com/acme/skills", Head: head, Clean: true},
		},
		{
			name:   "branch without upstream",
			status: "# branch.oid " + head + "\n# branch.head local\n",
			remote: ok("https://github.com/acme/skills\n"),
			want:   ports.CloneStatus{Remote: "https://github.com/acme/skills", Branch: "local", Head: head, Clean: true},
		},
		{
			name:   "no origin remote",
			status: "# branch.oid " + head + "\n# branch.head main\n",
			remote: ports.Result{ExitCode: 2, Stderr: []byte("error: No such remote 'origin'\n")},
			want:   ports.CloneStatus{Branch: "main", Head: head, Clean: true},
		},
		{
			name:   "no commit yet",
			status: "# branch.oid (initial)\n# branch.head main\n",
			remote: ok("https://github.com/acme/skills\n"),
			want:   ports.CloneStatus{Remote: "https://github.com/acme/skills", Branch: "main", Clean: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			runner.Expect(statusCmd, ok(tt.status), nil)
			runner.Expect(remoteCmd, tt.remote, nil)

			got, err := git.New(runner).Describe(context.Background(), root)
			if err != nil {
				t.Fatalf("Describe() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Describe() = %+v, want %+v", got, tt.want)
			}
			checkCommands(t, runner)
		})
	}
}

func TestDescribeFailures(t *testing.T) {
	boom := errors.New("cannot start")
	tests := []struct {
		name      string
		setup     func(*fakes.ProcessRunner)
		wantErr   error
		wantInErr string
	}{
		{
			name: "not a repository",
			setup: func(r *fakes.ProcessRunner) {
				r.Expect(statusCmd, ports.Result{ExitCode: 128, Stderr: []byte("fatal: not a git repository\n")}, nil)
			},
			wantInErr: "fatal: not a git repository",
		},
		{
			name:    "git cannot start",
			setup:   func(r *fakes.ProcessRunner) { r.Expect(statusCmd, ports.Result{}, boom) },
			wantErr: boom,
		},
		{
			name: "remote lookup fails otherwise",
			setup: func(r *fakes.ProcessRunner) {
				r.Expect(statusCmd, ok("# branch.oid "+head+"\n# branch.head main\n"), nil)
				r.Expect(remoteCmd, ports.Result{ExitCode: 128, Stderr: []byte("fatal: bad config line 3\n")}, nil)
			},
			wantInErr: "fatal: bad config line 3",
		},
		{
			name: "status without branch headers",
			setup: func(r *fakes.ProcessRunner) {
				r.Expect(statusCmd, ok(""), nil)
			},
			wantInErr: "branch.oid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			tt.setup(runner)

			got, err := git.New(runner).Describe(context.Background(), root)
			if err == nil {
				t.Fatalf("Describe() = %+v, want an error", got)
			}
			if got != (ports.CloneStatus{}) {
				t.Errorf("Describe() = %+v with an error, want the zero status", got)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("Describe() error = %v, want it to wrap %v", err, tt.wantErr)
			}
			if tt.wantInErr != "" && !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("Describe() error = %v, want it to contain %q", err, tt.wantInErr)
			}
		})
	}
}

func TestRootMustBeAbsolute(t *testing.T) {
	for _, r := range []string{"", "clones/skills", "./skills"} {
		runner := fakes.NewProcessRunner()
		c := git.New(runner)
		ctx := context.Background()
		if _, err := c.Describe(ctx, r); err == nil {
			t.Errorf("Describe(%q) succeeded, want an error", r)
		}
		if _, err := c.IsAncestor(ctx, r, head, head); err == nil {
			t.Errorf("IsAncestor(%q) succeeded, want an error", r)
		}
		if _, err := c.TreeSHA(ctx, r, head); err == nil {
			t.Errorf("TreeSHA(%q) succeeded, want an error", r)
		}
		if err := c.Fetch(ctx, r, head); err == nil {
			t.Errorf("Fetch(%q) succeeded, want an error", r)
		}
		if err := c.FastForward(ctx, r, head); err == nil {
			t.Errorf("FastForward(%q) succeeded, want an error", r)
		}
		if cmds := runner.Commands(); len(cmds) != 0 {
			t.Errorf("root %q ran %v, want nothing", r, cmds)
		}
	}
}

func TestIsAncestor(t *testing.T) {
	const other = "3333333333333333333333333333333333333333"
	cmd := gitCmd("merge-base", "--is-ancestor", head, other)
	tests := []struct {
		name      string
		result    ports.Result
		want      bool
		wantInErr string
	}{
		{"ancestor", ports.Result{ExitCode: 0}, true, ""},
		{"not an ancestor", ports.Result{ExitCode: 1}, false, ""},
		{"unknown commit", ports.Result{ExitCode: 128, Stderr: []byte("fatal: Not a valid commit name " + other + "\n")}, false, "Not a valid commit name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			runner.Expect(cmd, tt.result, nil)

			got, err := git.New(runner).IsAncestor(context.Background(), root, head, other)
			if tt.wantInErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
					t.Fatalf("IsAncestor() = %v, %v; want an error containing %q", got, err, tt.wantInErr)
				}
			} else if err != nil {
				t.Fatalf("IsAncestor() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("IsAncestor() = %v, want %v", got, tt.want)
			}
			checkCommands(t, runner)
		})
	}
}

func TestTreeSHA(t *testing.T) {
	cmd := gitCmd("rev-parse", "--verify", "--quiet", "--end-of-options", head+"^{commit}^{tree}")
	tests := []struct {
		name    string
		result  ports.Result
		want    string
		wantErr error
	}{
		{"commit", ok(tree + "\n"), tree, nil},
		{"sha-256 repository", ok(strings.Repeat("ab", 32) + "\n"), strings.Repeat("ab", 32), nil},
		{"unknown commit", ports.Result{ExitCode: 1}, "", git.ErrUnknownRevision},
		{"unexpected output", ok("not a sha\n"), "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			runner.Expect(cmd, tt.result, nil)

			got, err := git.New(runner).TreeSHA(context.Background(), root, head)
			if tt.want != "" {
				if err != nil || got != tt.want {
					t.Fatalf("TreeSHA() = %q, %v; want %q", got, err, tt.want)
				}
			} else {
				if err == nil || got != "" {
					t.Fatalf("TreeSHA() = %q, %v; want an error", got, err)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("TreeSHA() error = %v, want it to wrap %v", err, tt.wantErr)
				}
			}
			checkCommands(t, runner)
		})
	}
}

// TestRevisionsCannotBeOptions refuses a revision git would read as an
// option, before running anything.
func TestRevisionsCannotBeOptions(t *testing.T) {
	ctx := context.Background()
	for _, rev := range []string{"", "--upload-pack=touch /tmp/x", "-n", "main\nHEAD", "HEAD BAR"} {
		runner := fakes.NewProcessRunner()
		c := git.New(runner)
		if _, err := c.IsAncestor(ctx, root, rev, head); err == nil {
			t.Errorf("IsAncestor(%q, head) succeeded, want an error", rev)
		}
		if _, err := c.IsAncestor(ctx, root, head, rev); err == nil {
			t.Errorf("IsAncestor(head, %q) succeeded, want an error", rev)
		}
		if _, err := c.TreeSHA(ctx, root, rev); err == nil {
			t.Errorf("TreeSHA(%q) succeeded, want an error", rev)
		}
		if err := c.Fetch(ctx, root, rev); err == nil {
			t.Errorf("Fetch(%q) succeeded, want an error", rev)
		}
		if err := c.FastForward(ctx, root, rev); err == nil {
			t.Errorf("FastForward(%q) succeeded, want an error", rev)
		}
		if cmds := runner.Commands(); len(cmds) != 0 {
			t.Errorf("revision %q ran %v, want nothing", rev, cmds)
		}
	}
}

// writers are the subcommands Fetch and FastForward run; nothing else may
// change a clone.
var writers = map[string]bool{"fetch": true, "merge": true}

func TestFetch(t *testing.T) {
	cmd := gitCmd("fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "origin", head)
	tests := []struct {
		name      string
		result    ports.Result
		wantInErr string
	}{
		{"fetched", ports.Result{}, ""},
		{"commit not on origin", ports.Result{ExitCode: 128, Stderr: []byte("fatal: remote error: upload-pack: not our ref " + head + "\n")}, "not our ref"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			runner.Expect(cmd, tt.result, nil)

			err := git.New(runner).Fetch(context.Background(), root, head)
			if tt.wantInErr == "" && err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if tt.wantInErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantInErr)) {
				t.Fatalf("Fetch() error = %v, want one containing %q", err, tt.wantInErr)
			}
			checkCommandsAllowing(t, runner, writers)
		})
	}
}

// TestFastForward checks that the only merge skillhub runs refuses
// anything but a fast-forward, never overwrites an ignored file and never
// stashes local changes away.
func TestFastForward(t *testing.T) {
	cmd := gitCmd("merge", "--ff-only", "--no-overwrite-ignore", "--no-autostash", head)
	tests := []struct {
		name      string
		result    ports.Result
		wantInErr string
	}{
		{"fast-forwarded", ok("Updating 1111111..2222222\nFast-forward\n"), ""},
		{"not a fast-forward", ports.Result{ExitCode: 128, Stderr: []byte("fatal: Not possible to fast-forward, aborting.\n")}, "Not possible to fast-forward"},
		{"ignored file in the way", ports.Result{ExitCode: 1, Stderr: []byte("error: The following untracked working tree files would be overwritten by merge:\n\tcache.txt\n")}, "would be overwritten"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			runner.Expect(cmd, tt.result, nil)

			err := git.New(runner).FastForward(context.Background(), root, head)
			if tt.wantInErr == "" && err != nil {
				t.Fatalf("FastForward() error = %v", err)
			}
			if tt.wantInErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantInErr)) {
				t.Fatalf("FastForward() error = %v, want one containing %q", err, tt.wantInErr)
			}
			checkCommandsAllowing(t, runner, writers)
		})
	}
}

// TestWritesRequireAFullCommitID refuses a branch or an abbreviated SHA:
// fetch and fast-forward move a clone only to the exact commit the user
// reviewed.
func TestWritesRequireAFullCommitID(t *testing.T) {
	ctx := context.Background()
	for _, rev := range []string{"main", "origin/main", head[:12], "HEAD", strings.Repeat("A", 40)} {
		runner := fakes.NewProcessRunner()
		c := git.New(runner)
		if err := c.Fetch(ctx, root, rev); err == nil {
			t.Errorf("Fetch(%q) succeeded, want an error", rev)
		}
		if err := c.FastForward(ctx, root, rev); err == nil {
			t.Errorf("FastForward(%q) succeeded, want an error", rev)
		}
		if cmds := runner.Commands(); len(cmds) != 0 {
			t.Errorf("revision %q ran %v, want nothing", rev, cmds)
		}
	}
}

func TestClientIsAGitPort(t *testing.T) {
	var _ ports.GitPort = git.New(fakes.NewProcessRunner())
}
