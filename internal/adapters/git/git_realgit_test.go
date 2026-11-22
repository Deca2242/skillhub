//go:build realgit

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"skillhub/internal/adapters/git"
	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// realGit runs the real git for test setup, in dir, with an identity and no
// signing, since the fake home has no git config.
func realGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=skillhub test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)
	res, err := proc.New().Run(context.Background(), ports.Command{Name: "git", Args: full, Dir: dir})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("git %s: %v, exit %d, stderr %s", strings.Join(args, " "), err, res.ExitCode, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout))
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// fixture is a clone of a local upstream repository with two commits, on
// main tracking origin/main.
type fixture struct {
	dir      string // holds upstream, clone and markers
	upstream string
	clone    string
	first    string
	second   string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{dir: dir, upstream: filepath.Join(dir, "upstream"), clone: filepath.Join(dir, "clone")}
	realGit(t, dir, "init", "-q", f.upstream)
	writeFile(t, filepath.Join(f.upstream, "skill", "SKILL.md"), "---\nname: skill\n---\nfirst\n", 0o644)
	realGit(t, f.upstream, "add", "-A")
	realGit(t, f.upstream, "commit", "-q", "-m", "first")
	f.first = realGit(t, f.upstream, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(f.upstream, "skill", "SKILL.md"), "---\nname: skill\n---\nsecond\n", 0o644)
	writeFile(t, filepath.Join(f.upstream, "skill", "scripts", "run.sh"), "#!/bin/sh\n", 0o755)
	realGit(t, f.upstream, "add", "-A")
	realGit(t, f.upstream, "commit", "-q", "-m", "second")
	f.second = realGit(t, f.upstream, "rev-parse", "HEAD")
	realGit(t, dir, "clone", "-q", f.upstream, f.clone)
	return f
}

// snapshot hashes the clone's work tree and its .git directory.
func snapshot(t *testing.T, clone string) [2]string {
	t.Helper()
	work, err := treehash.Dir(clone)
	if err != nil {
		t.Fatalf("hash work tree: %v", err)
	}
	gitDir, err := treehash.Dir(filepath.Join(clone, ".git"))
	if err != nil {
		t.Fatalf("hash .git: %v", err)
	}
	return [2]string{work.SHA, gitDir.SHA}
}

// arm sets traps that leave a marker if Describe runs anything of the
// clone's own: a post-index-change hook, which fires when the index is
// rewritten, and a file system monitor command. It also makes the index
// stale, so a git status allowed to take optional locks rewrites it.
func (f fixture) arm(t *testing.T) (marker string) {
	t.Helper()
	marker = filepath.Join(f.dir, "marker")
	script := "#!/bin/sh\necho \"$0 $*\" >> '" + marker + "'\n"
	writeFile(t, filepath.Join(f.clone, ".git", "hooks", "post-index-change"), script, 0o755)
	monitor := filepath.Join(f.dir, "fsmonitor.sh")
	writeFile(t, monitor, script, 0o755)
	realGit(t, f.clone, "config", "core.fsmonitor", monitor)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(f.clone, "skill", "SKILL.md"), future, future); err != nil {
		t.Fatal(err)
	}
	return marker
}

func describeUnchanged(t *testing.T, f fixture) ports.CloneStatus {
	t.Helper()
	marker := f.arm(t)
	before := snapshot(t, f.clone)

	got, err := git.New(proc.New()).Describe(context.Background(), f.clone)
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}

	if after := snapshot(t, f.clone); after != before {
		t.Errorf("Describe changed the clone: work tree and .git hashes %v, were %v", after, before)
	}
	if out, err := os.ReadFile(marker); err == nil {
		t.Errorf("Describe ran something of the clone's own:\n%s", out)
	}
	return got
}

func TestRealDescribeCleanClone(t *testing.T) {
	f := newFixture(t)

	got := describeUnchanged(t, f)

	want := ports.CloneStatus{Remote: f.upstream, Branch: "main", Upstream: "origin/main", Head: f.second, Clean: true}
	if got != want {
		t.Errorf("Describe() = %+v, want %+v", got, want)
	}
}

func TestRealDescribeDirtyClone(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, f fixture)
	}{
		{"untracked file", func(t *testing.T, f fixture) {
			writeFile(t, filepath.Join(f.clone, "skill", "notes.md"), "mine\n", 0o644)
		}},
		{"untracked file hidden by the clone's config", func(t *testing.T, f fixture) {
			realGit(t, f.clone, "config", "status.showUntrackedFiles", "no")
			writeFile(t, filepath.Join(f.clone, "skill", "deep", "notes.md"), "mine\n", 0o644)
		}},
		{"modified tracked file", func(t *testing.T, f fixture) {
			writeFile(t, filepath.Join(f.clone, "skill", "SKILL.md"), "edited\n", 0o644)
		}},
		{"mode change", func(t *testing.T, f fixture) {
			if err := os.Chmod(filepath.Join(f.clone, "skill", "scripts", "run.sh"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.make(t, f)

			got := describeUnchanged(t, f)

			if got.Clean {
				t.Errorf("Describe().Clean = true for a clone with a %s", tt.name)
			}
			if got.Head != f.second || got.Branch != "main" {
				t.Errorf("Describe() = %+v, want head %s on main", got, f.second)
			}
		})
	}
}

func TestRealDescribeBranchStates(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, f fixture)
		want func(f fixture) ports.CloneStatus
	}{
		{"detached HEAD", func(t *testing.T, f fixture) {
			realGit(t, f.clone, "checkout", "-q", "--detach", f.first)
		}, func(f fixture) ports.CloneStatus {
			return ports.CloneStatus{Remote: f.upstream, Head: f.first, Clean: true}
		}},
		{"branch without upstream", func(t *testing.T, f fixture) {
			realGit(t, f.clone, "checkout", "-q", "-b", "local")
		}, func(f fixture) ports.CloneStatus {
			return ports.CloneStatus{Remote: f.upstream, Branch: "local", Head: f.second, Clean: true}
		}},
		{"no origin", func(t *testing.T, f fixture) {
			realGit(t, f.clone, "remote", "remove", "origin")
		}, func(f fixture) ports.CloneStatus {
			return ports.CloneStatus{Branch: "main", Head: f.second, Clean: true}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.make(t, f)

			got := describeUnchanged(t, f)

			if want := tt.want(f); got != want {
				t.Errorf("Describe() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestRealDescribeRefusesAFolderInsideAnotherRepository checks that a
// folder without its own .git is not described as the repository around it.
func TestRealDescribeRefusesAFolderInsideAnotherRepository(t *testing.T) {
	f := newFixture(t)
	inner := filepath.Join(f.clone, "skill")

	got, err := git.New(proc.New()).Describe(context.Background(), inner)
	if err == nil {
		t.Fatalf("Describe(%s) = %+v, want an error: it is not a clone root", inner, got)
	}
}

func TestRealIsAncestor(t *testing.T) {
	f := newFixture(t)
	before := snapshot(t, f.clone)
	c := git.New(proc.New())
	ctx := context.Background()

	tests := []struct {
		ancestor, descendant string
		want                 bool
	}{
		{f.first, f.second, true},
		{f.second, f.second, true},
		{f.second, f.first, false},
	}
	for _, tt := range tests {
		got, err := c.IsAncestor(ctx, f.clone, tt.ancestor, tt.descendant)
		if err != nil || got != tt.want {
			t.Errorf("IsAncestor(%s, %s) = %v, %v; want %v", tt.ancestor[:7], tt.descendant[:7], got, err, tt.want)
		}
	}
	if _, err := c.IsAncestor(ctx, f.clone, strings.Repeat("e", 40), f.second); err == nil {
		t.Error("IsAncestor(unknown commit) succeeded, want an error")
	}
	if after := snapshot(t, f.clone); after != before {
		t.Errorf("IsAncestor changed the clone")
	}
}

func TestRealTreeSHA(t *testing.T) {
	f := newFixture(t)
	before := snapshot(t, f.clone)
	c := git.New(proc.New())
	ctx := context.Background()

	got, err := c.TreeSHA(ctx, f.clone, f.second)
	if err != nil {
		t.Fatalf("TreeSHA() error = %v", err)
	}
	if want := realGit(t, f.clone, "rev-parse", f.second+"^{tree}"); got != want {
		t.Errorf("TreeSHA() = %s, git rev-parse = %s", got, want)
	}
	work, err := treehash.Dir(f.clone)
	if err != nil {
		t.Fatal(err)
	}
	if got != work.SHA {
		t.Errorf("TreeSHA() = %s, treehash.Dir of the checked-out clone = %s", got, work.SHA)
	}
	if _, err := c.TreeSHA(ctx, f.clone, strings.Repeat("e", 40)); err == nil {
		t.Error("TreeSHA(unknown commit) succeeded, want an error")
	}
	tree := realGit(t, f.clone, "rev-parse", f.second+"^{tree}")
	if _, err := c.TreeSHA(ctx, f.clone, tree); err == nil {
		t.Error("TreeSHA(a tree SHA) succeeded, want an error: it must name a commit")
	}
	if after := snapshot(t, f.clone); after != before {
		t.Errorf("TreeSHA changed the clone")
	}
}

// publish adds a third commit upstream that the clone does not have yet; it
// returns the commit.
func (f fixture) publish(t *testing.T, files map[string]string) string {
	t.Helper()
	for rel, content := range files {
		writeFile(t, filepath.Join(f.upstream, filepath.FromSlash(rel)), content, 0o644)
	}
	realGit(t, f.upstream, "add", "-A")
	realGit(t, f.upstream, "commit", "-q", "-m", "third")
	return realGit(t, f.upstream, "rev-parse", "HEAD")
}

// armWriteTraps sets every hook a fetch or a merge could fire, and a file
// system monitor, each leaving a line in the marker when it runs. Unlike arm,
// it leaves the index as it is.
func (f fixture) armWriteTraps(t *testing.T) (marker string) {
	t.Helper()
	marker = filepath.Join(f.dir, "marker")
	script := "#!/bin/sh\necho \"$0 $*\" >> '" + marker + "'\n"
	monitor := filepath.Join(f.dir, "fsmonitor.sh")
	writeFile(t, monitor, script, 0o755)
	realGit(t, f.clone, "config", "core.fsmonitor", monitor)
	for _, hook := range []string{"post-merge", "post-checkout", "reference-transaction", "pre-merge-commit", "post-index-change", "pre-auto-gc"} {
		writeFile(t, filepath.Join(f.clone, ".git", "hooks", hook), script, 0o755)
	}
	return marker
}

// files maps every file below dir, .git included, to its content.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// changed names the files that differ between two maps of files.
func changed(before, after map[string]string) string {
	var out []string
	for k, v := range after {
		if old, ok := before[k]; !ok || old != v {
			out = append(out, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, k+" (removed)")
		}
	}
	return strings.Join(out, ", ")
}

func noMarker(t *testing.T, marker string) {
	t.Helper()
	if out, err := os.ReadFile(marker); err == nil {
		t.Errorf("git ran something of the clone's own:\n%s", out)
	}
}

func TestRealFetchAndFastForwardMoveACleanClone(t *testing.T) {
	f := newFixture(t)
	third := f.publish(t, map[string]string{"skill/SKILL.md": "---\nname: skill\n---\nthird\n", "skill/new.md": "new\n"})
	marker := f.armWriteTraps(t)
	c := git.New(proc.New())
	ctx := context.Background()

	if err := c.Fetch(ctx, f.clone, third); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if ok, err := c.IsAncestor(ctx, f.clone, f.second, third); err != nil || !ok {
		t.Fatalf("after Fetch, IsAncestor(head, third) = %v, %v; want true", ok, err)
	}
	if err := c.FastForward(ctx, f.clone, third); err != nil {
		t.Fatalf("FastForward() error = %v", err)
	}

	got, err := c.Describe(ctx, f.clone)
	if err != nil {
		t.Fatal(err)
	}
	if got.Head != third || got.Branch != "main" || !got.Clean {
		t.Errorf("Describe() after the fast-forward = %+v, want a clean main at %s", got, third)
	}
	work, err := treehash.Dir(f.clone)
	if err != nil {
		t.Fatal(err)
	}
	if want := realGit(t, f.upstream, "rev-parse", third+"^{tree}"); work.SHA != want {
		t.Errorf("work tree = %s, want the tree of %s, %s", work.SHA, third, want)
	}
	if _, err := os.Stat(filepath.Join(f.clone, ".git", "FETCH_HEAD")); err == nil {
		t.Error("Fetch wrote FETCH_HEAD")
	}
	noMarker(t, marker)
}

// TestRealFastForwardRefusesWithoutLosingAnything covers each case where git
// must refuse: the clone keeps every file, and the work tree and .git stay
// byte for byte what they were.
func TestRealFastForwardRefusesWithoutLosingAnything(t *testing.T) {
	tests := []struct {
		name string
		// make prepares the clone; it returns the file that must survive,
		// relative to the clone, with its content.
		make func(t *testing.T, f fixture) (keep, content string)
	}{
		{"ignored file in a path the commit brings", func(t *testing.T, f fixture) (string, string) {
			writeFile(t, filepath.Join(f.clone, ".git", "info", "exclude"), "cache.txt\n", 0o644)
			writeFile(t, filepath.Join(f.clone, "cache.txt"), "my cache\n", 0o644)
			return "cache.txt", "my cache\n"
		}},
		{"untracked file in a path the commit brings", func(t *testing.T, f fixture) (string, string) {
			writeFile(t, filepath.Join(f.clone, "cache.txt"), "mine\n", 0o644)
			return "cache.txt", "mine\n"
		}},
		{"local commit: not a fast-forward", func(t *testing.T, f fixture) (string, string) {
			writeFile(t, filepath.Join(f.clone, "local.md"), "local\n", 0o644)
			realGit(t, f.clone, "add", "-A")
			realGit(t, f.clone, "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "local")
			return "local.md", "local\n"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			third := f.publish(t, map[string]string{"cache.txt": "upstream\n"})
			keep, content := tt.make(t, f)
			c := git.New(proc.New())
			ctx := context.Background()
			if err := c.Fetch(ctx, f.clone, third); err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			marker := f.armWriteTraps(t)
			before := files(t, f.clone)

			if err := c.FastForward(ctx, f.clone, third); err == nil {
				t.Fatal("FastForward() succeeded, want a refusal")
			}

			// git merge writes ORIG_HEAD before it refuses; nothing else may
			// change. The update flow restores its snapshot after a refusal,
			// so the clone ends byte for byte what it was.
			after := files(t, f.clone)
			delete(before, filepath.Join(".git", "ORIG_HEAD"))
			delete(after, filepath.Join(".git", "ORIG_HEAD"))
			if diff := changed(before, after); diff != "" {
				t.Errorf("the refused fast-forward changed the clone: %s", diff)
			}
			data, err := os.ReadFile(filepath.Join(f.clone, keep))
			if err != nil || string(data) != content {
				t.Errorf("%s = %q, %v; want %q kept", keep, data, err, content)
			}
			noMarker(t, marker)
		})
	}
}

func TestRealFetchOfAnUnknownCommitChangesNothing(t *testing.T) {
	f := newFixture(t)
	marker := f.armWriteTraps(t)
	before := snapshot(t, f.clone)

	err := git.New(proc.New()).Fetch(context.Background(), f.clone, strings.Repeat("e", 40))
	if err == nil {
		t.Fatal("Fetch(unknown commit) succeeded, want an error")
	}
	if after := snapshot(t, f.clone); after != before {
		t.Errorf("the failed fetch changed the clone: %v, was %v", after, before)
	}
	noMarker(t, marker)
}

// envRunner adds entries to every command's environment, the way a GIT_DIR
// or GIT_WORK_TREE inherited by skillhub would reach git.
type envRunner struct {
	inner ports.ProcessRunner
	env   []string
}

func (r envRunner) Run(ctx context.Context, cmd ports.Command) (ports.Result, error) {
	cmd.Env = append(append([]string(nil), cmd.Env...), r.env...)
	return r.inner.Run(ctx, cmd)
}

// TestRealInheritedGitDirIsIgnored points GIT_DIR and GIT_WORK_TREE at
// another repository: every operation must still work on the clone, and the
// other repository must not change.
func TestRealInheritedGitDirIsIgnored(t *testing.T) {
	f := newFixture(t)
	third := f.publish(t, map[string]string{"skill/SKILL.md": "---\nname: skill\n---\nthird\n"})
	other := newFixture(t)
	otherBefore := snapshot(t, other.clone)
	c := git.New(envRunner{inner: proc.New(), env: []string{
		"GIT_DIR=" + filepath.Join(other.clone, ".git"), "GIT_WORK_TREE=" + other.clone,
	}})
	ctx := context.Background()

	got, err := c.Describe(ctx, f.clone)
	if err != nil || got.Remote != f.upstream || got.Head != f.second {
		t.Fatalf("Describe() = %+v, %v; want the clone, origin %s at %s", got, err, f.upstream, f.second)
	}
	if err := c.Fetch(ctx, f.clone, third); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if err := c.FastForward(ctx, f.clone, third); err != nil {
		t.Fatalf("FastForward() error = %v", err)
	}
	if head := realGit(t, f.clone, "rev-parse", "HEAD"); head != third {
		t.Errorf("clone HEAD = %s, want %s", head, third)
	}
	if after := snapshot(t, other.clone); after != otherBefore {
		t.Errorf("the repository GIT_DIR named changed: %v, was %v", after, otherBefore)
	}
}
