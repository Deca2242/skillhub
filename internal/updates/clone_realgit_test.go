//go:build realgit

package updates_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gitadapter "skillhub/internal/adapters/git"
	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

// realGit runs the real git for test setup, in dir, with an identity, no
// signing and no hook, since the fake home has no git config.
func realGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=skillhub test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
	}, args...)
	res, err := proc.New().Run(context.Background(), ports.Command{Name: "git", Args: full, Dir: dir})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("git %s: %v, exit %d, stderr %s", strings.Join(args, " "), err, res.ExitCode, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// mappedGitHub serves the fake GitHub's repository under the real upstream's
// commit ids, so the clone, the real git and GitHub agree on every commit.
type mappedGitHub struct {
	*fakes.GitHub
	toReal, toFake map[string]string
}

func (g *mappedGitHub) ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	c, err := g.GitHub.ResolveCommit(ctx, repo, ref)
	if real, ok := g.toReal[c]; ok {
		return real, err
	}
	return c, err
}

func (g *mappedGitHub) Tree(ctx context.Context, repo, commit, path string) (treehash.Tree, error) {
	if fake, ok := g.toFake[commit]; ok {
		commit = fake
	}
	return g.GitHub.Tree(ctx, repo, commit, path)
}

// realClonePort is the real git adapter with the fixture's tripwire, and
// origin's local path shown as its GitHub URL: get-url expands insteadOf, so
// the fixture cannot point a GitHub URL at a local repository.
type realClonePort struct {
	inner    ports.GitPort
	trip     *tripwire
	upstream string
	// beforeFastForward, when set, runs right before the real merge.
	beforeFastForward func()
}

func (g *realClonePort) Describe(ctx context.Context, root string) (ports.CloneStatus, error) {
	if err := g.trip.step("Git.Describe"); err != nil {
		return ports.CloneStatus{}, err
	}
	st, err := g.inner.Describe(ctx, root)
	if st.Remote == g.upstream {
		st.Remote = cloneRemote
	}
	return st, err
}

func (g *realClonePort) Fetch(ctx context.Context, root, commit string) error {
	if err := g.trip.step("Git.Fetch"); err != nil {
		return err
	}
	return g.inner.Fetch(ctx, root, commit)
}

func (g *realClonePort) IsAncestor(ctx context.Context, root, a, d string) (bool, error) {
	if err := g.trip.step("Git.IsAncestor"); err != nil {
		return false, err
	}
	return g.inner.IsAncestor(ctx, root, a, d)
}

func (g *realClonePort) FastForward(ctx context.Context, root, commit string) error {
	if err := g.trip.step("Git.FastForward"); err != nil {
		return err
	}
	if g.beforeFastForward != nil {
		g.beforeFastForward()
	}
	return g.inner.FastForward(ctx, root, commit)
}

func (g *realClonePort) TreeSHA(ctx context.Context, root, commit string) (string, error) {
	if err := g.trip.step("Git.TreeSHA"); err != nil {
		return "", err
	}
	return g.inner.TreeSHA(ctx, root, commit)
}

// envRunner adds entries to every command's environment, as a GIT_DIR or
// GIT_WORK_TREE inherited by skillhub would.
type envRunner struct {
	inner ports.ProcessRunner
	env   []string
}

func (r envRunner) Run(ctx context.Context, cmd ports.Command) (ports.Result, error) {
	cmd.Env = append(slices.Clone(cmd.Env), r.env...)
	return r.inner.Run(ctx, cmd)
}

// realCloneFixture is a fixture with a real clone of a real upstream, in
// Claude Code's skills directory.
type realCloneFixture struct {
	*cloneFixture
	gh       *mappedGitHub
	upstream string
	runner   ports.ProcessRunner
	port     *realClonePort
}

func newRealCloneFixture(t *testing.T, runner ports.ProcessRunner) *realCloneFixture {
	t.Helper()
	f := newFixture(t)
	rf := &realCloneFixture{
		cloneFixture: &cloneFixture{
			fixture: f, root: f.home.Path(cloneRel), versions: map[string]map[string]string{}, trees: map[string]string{},
		},
		upstream: filepath.Join(t.TempDir(), "upstream"),
		runner:   runner,
	}
	rf.gh = &mappedGitHub{GitHub: f.gh, toReal: map[string]string{}, toFake: map[string]string{}}
	f.gh.AddRepo(cloneRepo, "main")
	realGit(t, filepath.Dir(rf.upstream), "init", "-q", rf.upstream)
	rf.commit(cloneV1)
	if err := os.MkdirAll(filepath.Dir(rf.root), 0o755); err != nil {
		t.Fatal(err)
	}
	realGit(t, filepath.Dir(rf.root), "clone", "-q", rf.upstream, rf.root)
	f.extend = func(d *updates.Deps) {
		d.GitHub = rf.gh
		rf.port = &realClonePort{inner: gitadapter.New(rf.runner), trip: f.trip, upstream: rf.upstream}
		d.Git = rf.port
		d.Clones = func(context.Context) ([]updates.Clone, error) {
			return []updates.Clone{{Root: rf.root, Skills: []string{cloneSkill}}}, nil
		}
	}
	f.svc = f.service()
	return rf
}

// commit replaces the upstream's files with files and commits them; it
// returns the real commit, published on the fake GitHub too.
func (rf *realCloneFixture) commit(files map[string]string) string {
	rf.t.Helper()
	entries, err := os.ReadDir(rf.upstream)
	if err != nil {
		rf.t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			if err := os.RemoveAll(filepath.Join(rf.upstream, e.Name())); err != nil {
				rf.t.Fatal(err)
			}
		}
	}
	writeTree(rf.t, rf.upstream, files)
	realGit(rf.t, rf.upstream, "add", "-A")
	realGit(rf.t, rf.upstream, "commit", "-q", "--allow-empty", "-m", "publish")
	real := realGit(rf.t, rf.upstream, "rev-parse", "HEAD")
	fake := rf.gh.GitHub.Commit(rf.t, cloneRepo, "main", rf.upstream)
	rf.gh.toReal[fake], rf.gh.toFake[real] = real, fake
	rf.trees[real] = realGit(rf.t, rf.upstream, "rev-parse", real+"^{tree}")
	return real
}

func (rf *realCloneFixture) head() string { return realGit(rf.t, rf.root, "rev-parse", "HEAD") }

// armTraps sets every hook git could fire in the clone and a file system
// monitor; each leaves a line in the marker it returns when it runs.
func (rf *realCloneFixture) armTraps() string {
	rf.t.Helper()
	dir := rf.t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := "#!/bin/sh\necho \"$0 $*\" >> '" + marker + "'\n"
	writeTree(rf.t, dir, map[string]string{"fsmonitor.sh*": script})
	realGit(rf.t, rf.root, "config", "core.fsmonitor", filepath.Join(dir, "fsmonitor.sh"))
	hooks := map[string]string{}
	for _, h := range []string{"post-merge", "post-checkout", "reference-transaction", "post-index-change", "pre-auto-gc", "post-rewrite"} {
		hooks[".git/hooks/"+h+"*"] = script
	}
	writeTree(rf.t, rf.root, hooks)
	return marker
}

func assertNoMarker(t *testing.T, marker string) {
	t.Helper()
	if out, err := os.ReadFile(marker); err == nil {
		t.Errorf("git ran something of the clone's own:\n%s", out)
	}
}

func TestRealCloneFastForwardsAndUndoesWithoutRunningAnythingOfTheClone(t *testing.T) {
	rf := newRealCloneFixture(t, proc.New())
	marker := rf.armTraps()
	before := rf.claudeTree()
	c2 := rf.commit(cloneV2)

	tc := targetResult(t, rf.check(), cloneTarget)
	if tc.Outcome != updates.CheckUpdateAvailable {
		t.Fatalf("check = %+v, want update_available", tc)
	}
	rf.assertClaudeTree(before, "after the check")
	c := rf.candidate(tc.CandidateID)
	if c.ToCommit != c2 || c.ToSHA != rf.trees[c2] {
		t.Fatalf("candidate to %s/%s, want %s/%s", c.ToCommit, c.ToSHA, c2, rf.trees[c2])
	}

	res, err := rf.svc.Apply(context.Background(), approval(c))
	if err != nil || res.Outcome != updates.OutcomeApplied {
		t.Fatalf("Apply = %+v, %v", res, err)
	}
	if got := rf.head(); got != c2 {
		t.Errorf("HEAD = %s, want %s", got, c2)
	}
	if got := hashOf(t, rf.root); got != c.ToSHA {
		t.Errorf("work tree = %s, want to_sha %s", got, c.ToSHA)
	}
	if out := realGit(t, rf.root, "status", "--porcelain"); out != "" {
		t.Errorf("clone not clean after the apply:\n%s", out)
	}

	if _, err := rf.svc.Undo(context.Background(), updates.UndoRequest{Target: cloneTarget}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	rf.assertClaudeTree(before, "after undo")
	assertNoMarker(t, marker)
}

// TestRealCloneTableRowsThatDoNotApply covers every row of the "Clones de
// git" table that must not apply: the clone stays byte for byte what it was,
// .git included, through a check and an attempted apply.
func TestRealCloneTableRowsThatDoNotApply(t *testing.T) {
	tests := []struct {
		name    string
		make    func(rf *realCloneFixture)
		outcome updates.CheckOutcome
		reason  domain.BlockReason
	}{
		{"modified file", func(rf *realCloneFixture) {
			writeTree(rf.t, rf.root, map[string]string{"skills/alpha/notes.md": "mine\n"})
		}, updates.CheckBlocked, domain.BlockDirtyWorktree},
		{"untracked file", func(rf *realCloneFixture) {
			writeTree(rf.t, rf.root, map[string]string{"skills/alpha/mine.md": "mine\n"})
		}, updates.CheckBlocked, domain.BlockDirtyWorktree},
		{"local commit", func(rf *realCloneFixture) {
			writeTree(rf.t, rf.root, map[string]string{"skills/alpha/mine.md": "mine\n"})
			realGit(rf.t, rf.root, "add", "-A")
			realGit(rf.t, rf.root, "commit", "-q", "-m", "mine")
		}, updates.CheckBlocked, domain.BlockLocalCommits},
		{"divergent history, upstream fetched", func(rf *realCloneFixture) {
			writeTree(rf.t, rf.root, map[string]string{"skills/alpha/mine.md": "mine\n"})
			realGit(rf.t, rf.root, "add", "-A")
			realGit(rf.t, rf.root, "commit", "-q", "-m", "mine")
			realGit(rf.t, rf.root, "fetch", "-q", "origin")
		}, updates.CheckBlocked, domain.BlockLocalCommits},
		{"detached HEAD", func(rf *realCloneFixture) {
			realGit(rf.t, rf.root, "checkout", "-q", "--detach")
		}, updates.CheckNotChecked, ""},
		{"branch that does not track origin", func(rf *realCloneFixture) {
			realGit(rf.t, rf.root, "checkout", "-q", "-b", "mine")
		}, updates.CheckNotChecked, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := newRealCloneFixture(t, proc.New())
			rf.commit(cloneV2)
			tt.make(rf)
			marker := rf.armTraps()
			before := rf.claudeTree()

			tc := targetResult(t, rf.check(), cloneTarget)

			if tc.Outcome != tt.outcome {
				t.Fatalf("check = %+v, want %s", tc, tt.outcome)
			}
			if tt.outcome == updates.CheckBlocked {
				c := rf.candidate(tc.CandidateID)
				if c.BlockReason != tt.reason || c.StagedPath != "" {
					t.Fatalf("candidate = %+v, want blocked by %s, not staged", c, tt.reason)
				}
				if _, err := rf.svc.Apply(context.Background(), approval(c)); !errors.Is(err, updates.ErrNotPending) {
					t.Errorf("Apply(blocked) = %v, want ErrNotPending", err)
				}
			} else if cands, _ := rf.svc.Candidates(); len(cands) != 0 {
				t.Errorf("candidates = %+v, want none", cands)
			}
			rf.assertClaudeTree(before, tt.name)
			assertNoMarker(t, marker)
		})
	}
}

// TestRealCloneApplyFailuresLeaveTheCloneIdentical: an ignored file in a path
// the commit brings, a fetch that fails, and a merge that fails after a real
// fetch all fail the apply and leave the clone byte for byte what it was,
// .git included.
func TestRealCloneApplyFailuresLeaveTheCloneIdentical(t *testing.T) {
	tests := []struct {
		name string
		make func(rf *realCloneFixture)
		want error
	}{
		{"ignored file in a path the commit brings", func(rf *realCloneFixture) {
			writeTree(rf.t, rf.root, map[string]string{".git/info/exclude": "cache.txt\n", "cache.txt": "my cache\n"})
		}, nil},
		{"fetch fails: origin is gone", func(rf *realCloneFixture) {
			if err := os.Rename(rf.upstream, rf.upstream+".gone"); err != nil {
				rf.t.Fatal(err)
			}
		}, nil},
		{"a step after a real fetch fails", func(rf *realCloneFixture) {
			rf.trip.armAfter("Git.Fetch", false)
		}, errInjected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := newRealCloneFixture(t, proc.New())
			v2 := maps2(cloneV2, map[string]string{"cache.txt": "upstream\n"})
			rf.commit(v2)
			c := rf.candidate(targetResult(t, rf.check(), cloneTarget).CandidateID)
			tt.make(rf)
			before := rf.claudeTree()

			res, err := rf.svc.Apply(context.Background(), approval(c))

			rf.trip.arm(0, false)
			if err == nil || res.Outcome != updates.OutcomeFailed || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Apply = %+v, %v; want a failure", res, err)
			}
			rf.assertClaudeTree(before, tt.name)
			if _, ok := rf.operation(); ok {
				t.Error("the failed apply left its operation record")
			}
			if got := rf.candidate(c.ID).State; got != domain.CandidatePending {
				t.Errorf("candidate = %s, want pending", got)
			}
			j := rf.journal()
			if last := j[len(j)-1]; last.Action != domain.ActionFailed || last.Target != cloneTarget {
				t.Errorf("journal = %+v, want failed", last)
			}
		})
	}
}

func maps2(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// TestRealCloneRewrittenUpstreamIsBlockedAfterTheFetch: the check cannot
// see that upstream rewrote its history without fetching; the apply finds
// out after its fetch, puts the clone back and blocks the candidate, and the
// next check keeps it blocked.
func TestRealCloneRewrittenUpstreamIsBlockedAfterTheFetch(t *testing.T) {
	rf := newRealCloneFixture(t, proc.New())
	realGit(t, rf.upstream, "checkout", "-q", "--orphan", "rewritten")
	realGit(t, rf.upstream, "branch", "-q", "-D", "main")
	realGit(t, rf.upstream, "branch", "-q", "-m", "main")
	rf.commit(cloneV2)
	before := rf.claudeTree()
	tc := targetResult(t, rf.check(), cloneTarget)
	if tc.Outcome != updates.CheckUpdateAvailable {
		t.Fatalf("check = %+v, want update_available", tc)
	}
	c := rf.candidate(tc.CandidateID)

	res, err := rf.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrNotFastForward) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("Apply = %+v, %v; want ErrNotFastForward", res, err)
	}
	rf.assertClaudeTree(before, "after the refused fast-forward")
	if again := targetResult(t, rf.check(), cloneTarget); again.Outcome != updates.CheckBlocked || again.CandidateID != c.ID {
		t.Errorf("check after the failure = %+v, want %s blocked", again, c.ID)
	}
}

func TestRealCloneApplyInterruptedAtEveryStepIsRecoveredExactly(t *testing.T) {
	recovered, finished := 0, false
	for at := 1; !finished; at++ {
		rf := newRealCloneFixture(t, proc.New())
		before := rf.claudeTree()
		rf.commit(cloneV2)
		c := rf.candidate(targetResult(t, rf.check(), cloneTarget).CandidateID)
		rf.trip.arm(at, true)

		killed := interrupted(func() { _, _ = rf.svc.Apply(context.Background(), approval(c)) })

		step := "end"
		if killed {
			step = rf.trip.ops[at-1]
		}
		rf.reopen()
		if !killed {
			finished = true
		}
		if _, ok := rf.operation(); !ok {
			continue
		}
		leftover := rf.claudeTree()
		if tc := targetResult(t, rf.check(), cloneTarget); tc.Outcome != updates.CheckNotChecked {
			t.Fatalf("%s: check = %+v, want not_checked", step, tc)
		}
		rf.assertClaudeTree(leftover, step+": a check repairs nothing")
		if _, err := rf.svc.Recover(context.Background()); err != nil {
			t.Fatalf("interrupted at %d (%s): Recover: %v", at, step, err)
		}
		recovered++
		rf.assertClaudeTree(before, "recover after "+step)
	}
	if recovered == 0 {
		t.Fatal("no interruption left a record to recover")
	}
	t.Logf("%d interruption points left a record and were recovered", recovered)
}

// TestRealCloneInheritedGitDirIsIgnored runs a whole check, apply and undo
// with GIT_DIR and GIT_WORK_TREE pointing at another repository: the clone
// moves and comes back, and the other repository never changes.
func TestRealCloneInheritedGitDirIsIgnored(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other")
	realGit(t, filepath.Dir(other), "init", "-q", other)
	writeTree(t, other, map[string]string{"x.md": "x\n"})
	realGit(t, other, "add", "-A")
	realGit(t, other, "commit", "-q", "-m", "other")
	otherBefore := treeOf(t, other)
	rf := newRealCloneFixture(t, envRunner{inner: proc.New(), env: []string{
		"GIT_DIR=" + filepath.Join(other, ".git"), "GIT_WORK_TREE=" + other,
	}})
	before := rf.claudeTree()
	c2 := rf.commit(cloneV2)

	tc := targetResult(t, rf.check(), cloneTarget)
	if tc.Outcome != updates.CheckUpdateAvailable {
		t.Fatalf("check = %+v, want update_available", tc)
	}
	if _, err := rf.svc.Apply(context.Background(), approval(rf.candidate(tc.CandidateID))); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := rf.head(); got != c2 {
		t.Errorf("clone HEAD = %s, want %s", got, c2)
	}
	if _, err := rf.svc.Undo(context.Background(), updates.UndoRequest{Target: cloneTarget}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	rf.assertClaudeTree(before, "after undo")
	if got := treeOf(t, other); got != otherBefore {
		t.Errorf("the repository GIT_DIR named changed: %s, was %s", got, otherBefore)
	}
}

// treeOf hashes a folder and its .git directory.
func treeOf(t *testing.T, dir string) string {
	t.Helper()
	return hashOf(t, dir) + "+" + hashOf(t, filepath.Join(dir, ".git"))
}

// userFilterTrap writes a global git config of the user's with a filter
// driver whose smudge and clean commands leave a line in the marker, the way
// git lfs install sets one up, and a global attributes file that applies it
// to every file. It returns the environment that makes git read them, and
// the marker.
func userFilterTrap(t *testing.T) (env []string, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "marker")
	script := "#!/bin/sh\necho \"filter $*\" >> '" + marker + "'\ncat\n"
	writeTree(t, dir, map[string]string{
		"filter.sh*": script,
		"attributes": "* filter=lfs\n",
		"gitconfig":  "[filter \"lfs\"]\n\tsmudge = " + filepath.Join(dir, "filter.sh") + " smudge\n\tclean = " + filepath.Join(dir, "filter.sh") + " clean\n\trequired = true\n[core]\n\tattributesFile = " + filepath.Join(dir, "attributes") + "\n\tautocrlf = true\n",
	})
	return []string{"GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "gitconfig")}, marker
}

// TestRealCloneUsersGlobalFilterNeverRuns: the user's global config names a
// filter for every file and turns on autocrlf. Neither the check, the apply
// nor the undo runs the filter, and the apply installs exactly the reviewed
// files.
func TestRealCloneUsersGlobalFilterNeverRuns(t *testing.T) {
	env, marker := userFilterTrap(t)
	rf := newRealCloneFixture(t, envRunner{inner: proc.New(), env: env})
	before := rf.claudeTree()
	rf.commit(cloneV2)

	tc := targetResult(t, rf.check(), cloneTarget)
	if tc.Outcome != updates.CheckUpdateAvailable {
		t.Fatalf("check = %+v, want update_available", tc)
	}
	c := rf.candidate(tc.CandidateID)
	if _, err := rf.svc.Apply(context.Background(), approval(c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := hashOf(t, rf.root); got != c.ToSHA {
		t.Errorf("work tree = %s, want the reviewed %s", got, c.ToSHA)
	}
	if _, err := rf.svc.Undo(context.Background(), updates.UndoRequest{Target: cloneTarget}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	rf.assertClaudeTree(before, "after undo")
	assertNoMarker(t, marker)
}

// TestRealCloneUpstreamAttributesBlock reproduces the verifier's probes:
// upstream publishes an eol=crlf .gitattributes (P2), or a file with
// filter=lfs while the user's config has an lfs smudge command (P7). The
// clone is blocked, nothing runs, and the clone does not change.
func TestRealCloneUpstreamAttributesBlock(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{"P2: eol=crlf", map[string]string{".gitattributes": "*.md text eol=crlf\n"}},
		{"P7: filter=lfs", map[string]string{".gitattributes": "skills/alpha/notes.md filter=lfs diff=lfs merge=lfs -text\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, marker := userFilterTrap(t)
			rf := newRealCloneFixture(t, envRunner{inner: proc.New(), env: env})
			rf.commit(maps2(cloneV2, tt.files))
			before := rf.claudeTree()

			tc := targetResult(t, rf.check(), cloneTarget)

			if tc.Outcome != updates.CheckBlocked {
				t.Fatalf("check = %+v, want blocked", tc)
			}
			c := rf.candidate(tc.CandidateID)
			if c.BlockReason != domain.BlockGitAttributes {
				t.Fatalf("block reason = %s, want git_attributes", c.BlockReason)
			}
			if _, err := rf.svc.Apply(context.Background(), approval(c)); !errors.Is(err, updates.ErrNotPending) {
				t.Errorf("Apply(blocked) = %v, want ErrNotPending", err)
			}
			rf.assertClaudeTree(before, tt.name)
			assertNoMarker(t, marker)
		})
	}
}

// TestRealCloneTransformedCheckoutFailsAndReverts skips every check on the
// attributes: right before the real merge, .git/info/attributes asks for
// eol=crlf. git writes CRLF files that are not the reviewed ones; the
// verification of the files catches it and the clone is put back.
func TestRealCloneTransformedCheckoutFailsAndReverts(t *testing.T) {
	rf := newRealCloneFixture(t, proc.New())
	before := rf.claudeTree()
	rf.commit(cloneV2)
	c := rf.candidate(targetResult(t, rf.check(), cloneTarget).CandidateID)
	rf.port.beforeFastForward = func() {
		writeTree(t, rf.root, map[string]string{".git/info/attributes": "*.md text eol=crlf\n"})
	}

	res, err := rf.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrVerifyFailed) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("Apply = %+v, %v; want ErrVerifyFailed", res, err)
	}
	rf.assertClaudeTree(before, "after the reverted apply")
	if got := rf.candidate(c.ID).State; got != domain.CandidatePending {
		t.Errorf("candidate = %s, want pending", got)
	}
}
