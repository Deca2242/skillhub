package updates_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

// The clone of the hermetic tests: a GitHub repository with one skill at
// skills/alpha, cloned in Claude Code's skills directory.
const (
	cloneRepo   = "acme/clone-skills"
	cloneRemote = "https://github.com/acme/clone-skills.git"
	cloneRel    = ".claude/skills/clone-skills"
	cloneTarget = "git:" + cloneRel
	cloneSkill  = "git:" + cloneRel + "/skills/alpha"
	// trackingRef is the clone's origin/main. The fake Git knows commits by
	// name, so the tracking branch is a commit named origin/main whose parent
	// is the commit it points at.
	trackingRef = "origin/main"
)

// fileGit is the fake Git with a clone whose files really change: a fetch
// writes an object file into .git, and a fast-forward checks the commit's
// files out, as git would. Every call goes through the fixture's tripwire.
type fileGit struct {
	*fakes.Git
	cf *cloneFixture
	// afterFastForward, when set, runs after a successful fast-forward, to
	// play a git that left the clone in an unexpected state.
	afterFastForward func()
	// skipFastForward makes FastForward report success without moving.
	skipFastForward bool
	// afterFetch, when set, runs after a successful fetch.
	afterFetch func()
}

func (g *fileGit) Describe(ctx context.Context, root string) (ports.CloneStatus, error) {
	if err := g.cf.trip.step("Git.Describe"); err != nil {
		return ports.CloneStatus{}, err
	}
	return g.Git.Describe(ctx, root)
}

func (g *fileGit) IsAncestor(ctx context.Context, root, a, d string) (bool, error) {
	if err := g.cf.trip.step("Git.IsAncestor"); err != nil {
		return false, err
	}
	return g.Git.IsAncestor(ctx, root, a, d)
}

func (g *fileGit) TreeSHA(ctx context.Context, root, commit string) (string, error) {
	if err := g.cf.trip.step("Git.TreeSHA"); err != nil {
		return "", err
	}
	return g.Git.TreeSHA(ctx, root, commit)
}

func (g *fileGit) Fetch(ctx context.Context, root, commit string) error {
	if err := g.cf.trip.step("Git.Fetch"); err != nil {
		return err
	}
	if err := g.Git.Fetch(ctx, root, commit); err != nil {
		return err
	}
	if g.afterFetch != nil {
		g.afterFetch()
	}
	return os.WriteFile(filepath.Join(root, ".git", "objects", commit), []byte("pack "+commit), 0o444)
}

func (g *fileGit) FastForward(ctx context.Context, root, commit string) error {
	if err := g.cf.trip.step("Git.FastForward"); err != nil {
		return err
	}
	if g.skipFastForward {
		return nil
	}
	old, err := g.Git.Describe(ctx, root)
	if err != nil {
		return err
	}
	if g.afterFastForward != nil {
		defer g.afterFastForward()
	}
	if err := g.Git.FastForward(ctx, root, commit); err != nil {
		return err
	}
	for rel := range g.cf.versions[old.Head] {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(rel, "*")))); err != nil {
			return err
		}
	}
	writeTree(g.cf.t, root, g.cf.versions[commit])
	return os.WriteFile(filepath.Join(root, ".git", "HEAD.log"), []byte("moved to "+commit), 0o644)
}

// cloneFixture is a fixture with one clone of cloneRepo.
type cloneFixture struct {
	*fixture
	git   *fileGit
	clone *fakes.Clone
	root  string
	dir   string // the upstream working copy of cloneRepo
	// versions are the files of every published commit.
	versions map[string]map[string]string
	trees    map[string]string
}

// Versions of the clone's files.
var (
	cloneV1 = map[string]string{
		"README.md":                 "clone readme\n",
		"skills/alpha/SKILL.md":     "---\nname: alpha\ndescription: first\n---\nUse alpha.\n",
		"skills/alpha/notes.md":     "notes v1\n",
		"skills/beta/SKILL.md":      "---\nname: beta\ndescription: beta\n---\nUse beta.\n",
		"skills/beta/references.md": "plain\n",
	}
	cloneV2 = map[string]string{
		"README.md":                 "clone readme\n",
		"skills/alpha/SKILL.md":     "---\nname: alpha\ndescription: second\n---\nUse alpha.\n",
		"skills/alpha/notes.md":     "notes v2\n",
		"skills/alpha/scripts/x.sh": "#!/bin/sh\necho x\n",
		"skills/beta/SKILL.md":      "---\nname: beta\ndescription: beta\nallowed-tools: Bash\n---\nUse beta.\n",
		"skills/beta/references.md": "plain\n",
	}
)

// newCloneFixture installs cloneV1 as a clean clone on main tracking
// origin/main, published upstream as the clone's HEAD.
func newCloneFixture(t *testing.T) *cloneFixture {
	t.Helper()
	return newCloneFixtureWith(t, cloneV1)
}

// newCloneFixtureWith is newCloneFixture with v1 as the installed version.
func newCloneFixtureWith(t *testing.T, v1 map[string]string) *cloneFixture {
	t.Helper()
	f := newFixture(t)
	cf := &cloneFixture{
		fixture: f, root: f.home.Path(cloneRel), dir: filepath.Join(t.TempDir(), "clone-upstream"),
		versions: map[string]map[string]string{}, trees: map[string]string{},
	}
	f.gh.AddRepo(cloneRepo, "main")
	c1 := cf.publish(v1)
	writeTree(t, cf.root, v1)
	writeTree(t, cf.root, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[core]\n"})
	if err := os.MkdirAll(filepath.Join(cf.root, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	cf.git = &fileGit{Git: fakes.NewGit(), cf: cf}
	cf.clone = cf.git.AddClone(cf.root, ports.CloneStatus{
		Remote: cloneRemote, Branch: "main", Upstream: trackingRef, Head: c1, Clean: true,
	})
	cf.clone.AddCommit(c1, "", cf.trees[c1])
	cf.clone.AddCommit(trackingRef, c1, cf.trees[c1])
	f.extend = func(d *updates.Deps) {
		d.Git = cf.git
		d.Clones = func(context.Context) ([]updates.Clone, error) {
			return []updates.Clone{{Root: cf.root, Skills: []string{cloneSkill, "git:" + cloneRel + "/skills/beta"}}}, nil
		}
	}
	f.svc = f.service()
	return cf
}

// publish commits files upstream; it returns the commit.
func (cf *cloneFixture) publish(files map[string]string) string {
	cf.t.Helper()
	if err := os.RemoveAll(cf.dir); err != nil {
		cf.t.Fatal(err)
	}
	writeTree(cf.t, cf.dir, files)
	commit := cf.gh.Commit(cf.t, cloneRepo, "main", cf.dir)
	cf.versions[commit] = files
	cf.trees[commit] = hashOf(cf.t, cf.dir)
	return commit
}

// publishRemote publishes files as a child of parent that only origin has.
func (cf *cloneFixture) publishRemote(parent string, files map[string]string) string {
	cf.t.Helper()
	commit := cf.publish(files)
	cf.clone.AddRemoteCommit(commit, parent, cf.trees[commit])
	return commit
}

func (cf *cloneFixture) status() ports.CloneStatus {
	cf.t.Helper()
	st, err := cf.git.Git.Describe(context.Background(), cf.root)
	if err != nil {
		cf.t.Fatal(err)
	}
	return st
}

// claudeTree records everything under ~/.claude, which holds the clone, its
// .git and the folder an apply swaps through: mode and content, byte for
// byte.
func (cf *cloneFixture) claudeTree() map[string]string {
	cf.t.Helper()
	root := cf.home.Path(".claude")
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			out[rel] = fmt.Sprintf("dir %v", info.Mode())
			return nil
		}
		data, err := os.ReadFile(p)
		out[rel] = fmt.Sprintf("%v %s", info.Mode(), data)
		return err
	})
	if err != nil {
		cf.t.Fatalf("walk ~/.claude: %v", err)
	}
	return out
}

func (cf *cloneFixture) assertClaudeTree(want map[string]string, context string) {
	cf.t.Helper()
	got := cf.claudeTree()
	if maps.Equal(got, want) {
		return
	}
	var diffs []string
	keys := slices.Sorted(maps.Keys(got))
	for k := range want {
		keys = append(keys, k)
	}
	for _, k := range slices.Compact(slices.Sorted(slices.Values(keys))) {
		if got[k] != want[k] {
			diffs = append(diffs, fmt.Sprintf("  %s:\n    got  %.80q\n    want %.80q", k, got[k], want[k]))
		}
	}
	cf.t.Fatalf("%s: ~/.claude changed:\n%s", context, strings.Join(diffs, "\n"))
}

// gitCalls lists the fake Git calls of op.
func (cf *cloneFixture) gitCalls(op string) int {
	n := 0
	for _, c := range cf.git.Calls() {
		if c.Op == op {
			n++
		}
	}
	return n
}

// stagedClone publishes cloneV2 and checks; it returns the pending candidate.
func (cf *cloneFixture) stagedClone() (domain.UpdateCandidate, string) {
	cf.t.Helper()
	c1 := cf.status().Head
	c2 := cf.publishRemote(c1, cloneV2)
	tc := targetResult(cf.t, cf.check(), cloneTarget)
	if tc.Outcome != updates.CheckUpdateAvailable {
		cf.t.Fatalf("check outcome = %s (%v), want update_available", tc.Outcome, tc.Err)
	}
	return cf.candidate(tc.CandidateID), c2
}

func TestCloneCheckStagesTheUpstreamCommitAndChangesNothing(t *testing.T) {
	cf := newCloneFixture(t)
	c1 := cf.status().Head
	c2 := cf.publishRemote(c1, cloneV2)
	before := cf.claudeTree()

	report := cf.check()

	tc := targetResult(t, report, cloneTarget)
	if tc.Outcome != updates.CheckUpdateAvailable || tc.Err != nil {
		t.Fatalf("clone check = %+v, want update_available", tc)
	}
	c := cf.candidate(tc.CandidateID)
	want := domain.UpdateCandidate{
		ID: c.ID, Target: cloneTarget, Skills: []string{cloneSkill, "git:" + cloneRel + "/skills/beta"},
		FromSHA: cf.trees[c1], InstalledSHAAtStage: cf.trees[c1], ToCommit: c2, ToSHA: cf.trees[c2],
		StagedPath: c.StagedPath, State: domain.CandidatePending,
	}
	if c.ID == "" || c.Target != want.Target || !slices.Equal(c.Skills, want.Skills) || c.FromSHA != want.FromSHA ||
		c.InstalledSHAAtStage != want.InstalledSHAAtStage || c.ToCommit != want.ToCommit || c.ToSHA != want.ToSHA ||
		c.State != want.State || c.BlockReason != "" {
		t.Fatalf("candidate = %+v\nwant %+v", c, want)
	}
	if got := hashOf(t, c.StagedPath); got != c.ToSHA {
		t.Errorf("staged folder hashes to %s, want to_sha %s", got, c.ToSHA)
	}
	var changed []string
	for _, fc := range c.FileChanges {
		changed = append(changed, string(fc.Change)+" "+fc.Path)
	}
	wantChanged := []string{
		"modified skills/alpha/SKILL.md", "modified skills/alpha/notes.md", "added skills/alpha/scripts/x.sh",
		"modified skills/beta/SKILL.md",
	}
	if !slices.Equal(changed, wantChanged) {
		t.Errorf("file changes = %v, want %v", changed, wantChanged)
	}
	// Labels come from each skill folder: scripts/ of alpha, and the new
	// allowed-tools key of beta's frontmatter.
	wantLabels := []domain.Label{domain.LabelNonTextFiles, domain.LabelTouchesScripts, domain.LabelNewPermissions}
	if !slices.Equal(c.Labels, wantLabels) {
		t.Errorf("labels = %v, want %v", c.Labels, wantLabels)
	}
	cf.assertClaudeTree(before, "after a check")
	if n := cf.gitCalls("Fetch") + cf.gitCalls("FastForward"); n != 0 {
		t.Errorf("a check ran %d fetches or fast-forwards", n)
	}
	if got := cf.persisted().Results[cloneTarget].State; got != domain.StateUpdateAvailable {
		t.Errorf("stored result = %s, want update_available", got)
	}

	// A second check keeps the same candidate.
	if again := targetResult(t, cf.check(), cloneTarget); again.CandidateID != c.ID {
		t.Errorf("second check candidate = %s, want %s kept", again.CandidateID, c.ID)
	}
}

func TestCloneCheckUpToDate(t *testing.T) {
	cf := newCloneFixture(t)
	before := cf.claudeTree()

	tc := targetResult(t, cf.check(), cloneTarget)

	if tc.Outcome != updates.CheckUpToDate || tc.Err != nil {
		t.Fatalf("clone check = %+v, want up_to_date", tc)
	}
	if cands, _ := cf.svc.Candidates(); len(cands) != 0 {
		t.Errorf("candidates = %+v, want none", cands)
	}
	cf.assertClaudeTree(before, "after a check")
}

// TestCloneCheckBlocksAModifiedClone covers the rows of the "Clones de git"
// table that block: the candidate is blocked, nothing is staged, nothing can
// be applied, and the clone stays byte for byte what it was.
func TestCloneCheckBlocksAModifiedClone(t *testing.T) {
	tests := []struct {
		name string
		make func(cf *cloneFixture, c1 string)
		want domain.BlockReason
	}{
		{"dirty work tree", func(cf *cloneFixture, c1 string) {
			cf.publishRemote(c1, cloneV2)
			st := cf.status()
			st.Clean = false
			cf.clone.SetStatus(st)
		}, domain.BlockDirtyWorktree},
		{"local commit not upstream", func(cf *cloneFixture, c1 string) {
			cf.publishRemote(c1, cloneV2)
			cf.clone.AddCommit("local", c1, "tree-local")
			st := cf.status()
			st.Head = "local"
			cf.clone.SetStatus(st)
		}, domain.BlockLocalCommits},
		{"divergent history, upstream already fetched", func(cf *cloneFixture, c1 string) {
			c2 := cf.publish(cloneV2)
			cf.clone.AddCommit(c2, c1, cf.trees[c2])
			cf.clone.AddCommit("local", c1, "tree-local")
			st := cf.status()
			st.Head = "local"
			cf.clone.SetStatus(st)
		}, domain.BlockLocalCommits},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := newCloneFixture(t)
			tt.make(cf, cf.status().Head)
			before := cf.claudeTree()

			tc := targetResult(t, cf.check(), cloneTarget)

			if tc.Outcome != updates.CheckBlocked {
				t.Fatalf("clone check = %+v, want blocked", tc)
			}
			c := cf.candidate(tc.CandidateID)
			if c.State != domain.CandidateBlocked || c.BlockReason != tt.want || c.StagedPath != "" {
				t.Fatalf("candidate = %+v, want blocked by %s with no staging", c, tt.want)
			}
			if _, err := cf.svc.Apply(context.Background(), approval(c)); !errors.Is(err, updates.ErrNotPending) {
				t.Errorf("Apply(blocked) = %v, want ErrNotPending", err)
			}
			if again := targetResult(t, cf.check(), cloneTarget); again.CandidateID != c.ID {
				t.Errorf("second check candidate = %s, want %s kept", again.CandidateID, c.ID)
			}
			cf.assertClaudeTree(before, "a blocked clone")
			if n := cf.gitCalls("Fetch") + cf.gitCalls("FastForward"); n != 0 {
				t.Errorf("ran %d fetches or fast-forwards on a blocked clone", n)
			}
			if entries, err := os.ReadDir(cf.p.StagingDir()); err == nil && len(entries) != 0 {
				t.Errorf("staging holds %d entries for a blocked clone", len(entries))
			}
		})
	}
}

// TestCloneCheckUnblocksOnceTheUserFixesTheClone: the next check after the
// clone is clean again stages a pending candidate.
func TestCloneCheckUnblocksOnceTheUserFixesTheClone(t *testing.T) {
	cf := newCloneFixture(t)
	cf.publishRemote(cf.status().Head, cloneV2)
	st := cf.status()
	st.Clean = false
	cf.clone.SetStatus(st)
	blocked := targetResult(t, cf.check(), cloneTarget)

	st.Clean = true
	cf.clone.SetStatus(st)
	tc := targetResult(t, cf.check(), cloneTarget)

	if tc.Outcome != updates.CheckUpdateAvailable || tc.CandidateID == blocked.CandidateID {
		t.Fatalf("check after the fix = %+v, want a new pending candidate", tc)
	}
	if got := cf.candidate(blocked.CandidateID).State; got != domain.CandidateSuperseded {
		t.Errorf("blocked candidate is %s, want superseded", got)
	}
}

func TestCloneCheckSkipsAnUntrackedBranch(t *testing.T) {
	tests := []struct {
		name             string
		branch, upstream string
	}{
		{"detached HEAD", "", ""},
		{"branch without upstream", "local", ""},
		{"branch tracking another remote", "main", "fork/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := newCloneFixture(t)
			cf.publishRemote(cf.status().Head, cloneV2)
			st := cf.status()
			st.Branch, st.Upstream = tt.branch, tt.upstream
			cf.clone.SetStatus(st)
			before := cf.claudeTree()

			tc := targetResult(t, cf.check(), cloneTarget)

			if tc.Outcome != updates.CheckNotChecked || !errors.Is(tc.Err, updates.ErrUntrackedBranch) {
				t.Fatalf("clone check = %+v, want not_checked with ErrUntrackedBranch", tc)
			}
			if cands, _ := cf.svc.Candidates(); len(cands) != 0 {
				t.Errorf("candidates = %+v, want none", cands)
			}
			if n := cf.ghCalls("ResolveCommit"); n != 0 {
				t.Errorf("an untracked branch was checked upstream (%d calls)", n)
			}
			cf.assertClaudeTree(before, "an untracked branch")
		})
	}
}

func TestCloneApplyFastForwardsToTheApprovedCommit(t *testing.T) {
	cf := newCloneFixture(t)
	before := cf.claudeTree()
	c, c2 := cf.stagedClone()

	res, err := cf.svc.Apply(context.Background(), approval(c))

	if err != nil || res.Outcome != updates.OutcomeApplied || res.Target != cloneTarget {
		t.Fatalf("Apply = %+v, %v", res, err)
	}
	if st := cf.status(); st.Head != c2 || !st.Clean {
		t.Errorf("clone after apply = %+v, want a clean HEAD at %s", st, c2)
	}
	if got := hashOf(t, cf.root); got != c.ToSHA {
		t.Errorf("work tree = %s, want to_sha %s", got, c.ToSHA)
	}
	calls := []string{}
	for _, call := range cf.git.Calls() {
		if call.Op == "Fetch" || call.Op == "FastForward" {
			calls = append(calls, call.Op+" "+strings.Join(call.Args[1:], " "))
		}
	}
	if want := []string{"Fetch " + c2, "FastForward " + c2}; !slices.Equal(calls, want) {
		t.Errorf("git writes = %v, want %v", calls, want)
	}
	if got := cf.candidate(c.ID).State; got != domain.CandidateApplied {
		t.Errorf("candidate = %s, want applied", got)
	}
	if got := cf.persisted().Results[cloneTarget].State; got != domain.StateUpToDate {
		t.Errorf("stored result = %s, want up_to_date", got)
	}
	j := cf.journal()
	last := j[len(j)-1]
	if last.Action != domain.ActionApplied || last.Target != cloneTarget || last.FromSHA != c.FromSHA ||
		last.ToSHA != c.ToSHA || last.SnapshotID != res.SnapshotID {
		t.Errorf("journal = %+v", last)
	}
	snap := filepath.Join(cf.p.SnapshotsDir(), res.SnapshotID, "folder")
	if _, err := os.Stat(filepath.Join(snap, ".git", "HEAD")); err != nil {
		t.Errorf("the snapshot holds no .git: %v", err)
	}
	if _, ok := cf.operation(); ok {
		t.Error("the operation record is left")
	}
	if _, err := os.Stat(cf.home.Path(".claude/.skillhub-tmp")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("leftover swap folder: %v", err)
	}

	// Undo restores the full copy, .git included, without git.
	writes := cf.gitCalls("Fetch") + cf.gitCalls("FastForward")
	undo, err := cf.svc.Undo(context.Background(), updates.UndoRequest{Target: cloneTarget})
	if err != nil || undo.Changed {
		t.Fatalf("Undo = %+v, %v", undo, err)
	}
	cf.assertClaudeTree(before, "after undo")
	if got := cf.gitCalls("Fetch") + cf.gitCalls("FastForward"); got != writes {
		t.Error("undo ran git")
	}
	j = cf.journal()
	if last := j[len(j)-1]; last.Action != domain.ActionUndone || last.ToSHA != c.FromSHA || last.FromSHA != c.ToSHA {
		t.Errorf("undo journal = %+v", last)
	}
}

// TestCloneApplyRejectsAnApprovalThatIsNoLongerTrue revalidates under the
// state lock: the clone's head, cleanliness and branch must be those of the
// staging, and the staged copy must be whole.
func TestCloneApplyRejectsAnApprovalThatIsNoLongerTrue(t *testing.T) {
	tests := []struct {
		name string
		make func(cf *cloneFixture, c domain.UpdateCandidate)
		want error
	}{
		{"clone became dirty", func(cf *cloneFixture, _ domain.UpdateCandidate) {
			st := cf.status()
			st.Clean = false
			cf.clone.SetStatus(st)
		}, updates.ErrInstalledChanged},
		{"head moved", func(cf *cloneFixture, _ domain.UpdateCandidate) {
			st := cf.status()
			cf.clone.AddCommit("local", st.Head, cf.trees[st.Head])
			st.Head = "local"
			cf.clone.SetStatus(st)
		}, updates.ErrInstalledChanged},
		{"HEAD's tree changed", func(cf *cloneFixture, _ domain.UpdateCandidate) {
			st := cf.status()
			cf.clone.AddCommit(st.Head, "", "another-tree")
		}, updates.ErrInstalledChanged},
		{"branch changed", func(cf *cloneFixture, _ domain.UpdateCandidate) {
			st := cf.status()
			st.Branch = "other"
			cf.clone.SetStatus(st)
		}, updates.ErrInstalledChanged},
		{"staged copy changed", func(cf *cloneFixture, c domain.UpdateCandidate) {
			writeTree(cf.t, c.StagedPath, map[string]string{"extra.md": "x\n"})
		}, updates.ErrStagedChanged},
		{"other approved to_sha", nil, updates.ErrApprovalMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := newCloneFixture(t)
			c, _ := cf.stagedClone()
			a := approval(c)
			if tt.make != nil {
				tt.make(cf, c)
			} else {
				a.ToSHA = strings.Repeat("0", 40)
			}
			before := cf.claudeTree()

			res, err := cf.svc.Apply(context.Background(), a)

			if !errors.Is(err, updates.ErrRejected) || !errors.Is(err, tt.want) || res.Outcome != updates.OutcomeRejected {
				t.Fatalf("Apply = %+v, %v; want a rejection wrapping %v", res, err, tt.want)
			}
			cf.assertClaudeTree(before, "after a rejected apply")
			if n := cf.gitCalls("Fetch") + cf.gitCalls("FastForward"); n != 0 {
				t.Errorf("a rejected apply ran %d git writes", n)
			}
			if _, ok := cf.operation(); ok {
				t.Error("a rejected apply recorded an operation")
			}
		})
	}
}

func TestCloneApplyFailureAtEveryStepLeavesTheCloneIdentical(t *testing.T) {
	succeeded, sawWrite := false, false
	for at := 1; !succeeded; at++ {
		cf := newCloneFixture(t)
		before := cf.claudeTree()
		c, _ := cf.stagedClone()
		cf.trip.arm(at, false)

		res, err := cf.svc.Apply(context.Background(), approval(c))

		ops := cf.trip.ops
		cf.trip.arm(0, false)
		if at > len(ops) {
			if err != nil {
				t.Fatalf("apply with no failure: %v", err)
			}
			succeeded = true
			continue
		}
		step := ops[at-1]
		if err == nil {
			// A failure after the apply closed only costs disk space.
			if st := cf.status(); st.Head != c.ToCommit || hashOf(t, cf.root) != c.ToSHA {
				t.Fatalf("failure at call %d (%s): apply succeeded but the clone is %+v", at, step, st)
			}
			continue
		}
		if res.Outcome == updates.OutcomeApplied || !errors.Is(err, errInjected) {
			t.Fatalf("failure at call %d (%s): result %+v, err %v", at, step, res, err)
		}
		if step == "Git.FastForward" {
			sawWrite = true
		}
		cf.assertClaudeTree(before, "failure at "+step)
		if op, ok := cf.operation(); ok {
			t.Fatalf("failure at call %d (%s) left operation %+v", at, step, op)
		}
		if got := cf.candidate(c.ID).State; got != domain.CandidatePending {
			t.Fatalf("failure at call %d (%s): candidate %s, want pending", at, step, got)
		}
	}
	if !sawWrite {
		t.Fatal("no failure was injected into the fast-forward after a fetch")
	}
}

func TestCloneApplyInterruptedAtEveryStepIsRecoveredExactly(t *testing.T) {
	recovered, finished := 0, false
	for at := 1; !finished; at++ {
		cf := newCloneFixture(t)
		before := cf.claudeTree()
		c, _ := cf.stagedClone()
		cf.trip.arm(at, true)

		killed := interrupted(func() { _, _ = cf.svc.Apply(context.Background(), approval(c)) })

		step := "end"
		if killed {
			step = cf.trip.ops[at-1]
		}
		cf.reopen()
		if !killed {
			finished = true
		}
		op, ok := cf.operation()
		if !ok {
			continue
		}
		if op.Kind != domain.OperationApply || op.Target != cloneTarget || op.CandidateID != c.ID {
			t.Fatalf("interrupted at %d (%s): record %+v", at, step, op)
		}
		leftover := cf.claudeTree()
		ctx := context.Background()
		if _, err := cf.svc.Apply(ctx, approval(c)); !errors.Is(err, updates.ErrInterrupted) {
			t.Fatalf("%s: Apply = %v, want ErrInterrupted", step, err)
		}
		if _, err := cf.svc.Undo(ctx, updates.UndoRequest{Target: cloneTarget}); !errors.Is(err, updates.ErrInterrupted) {
			t.Fatalf("%s: Undo = %v, want ErrInterrupted", step, err)
		}
		if tc := targetResult(t, cf.check(), cloneTarget); tc.Outcome != updates.CheckNotChecked {
			t.Fatalf("%s: check = %+v, want not_checked", step, tc)
		}
		cf.assertClaudeTree(leftover, step+": nothing repairs itself")

		if _, err := cf.svc.Recover(ctx); err != nil {
			t.Fatalf("interrupted at %d (%s): Recover: %v", at, step, err)
		}
		recovered++
		cf.assertClaudeTree(before, "recover after "+step)
		if _, ok := cf.operation(); ok {
			t.Fatalf("recover after %s left the record", step)
		}
		if got := cf.candidate(c.ID).State; got != domain.CandidatePending {
			t.Fatalf("recover after %s: candidate %s, want pending", step, got)
		}
	}
	if recovered == 0 {
		t.Fatal("no interruption left a record to recover")
	}
	t.Logf("%d interruption points left a record and were recovered", recovered)
}

func TestCloneUndoInterruptedAtEveryStepIsRecovered(t *testing.T) {
	recovered, finished := 0, false
	for at := 1; !finished; at++ {
		cf := newCloneFixture(t)
		before := cf.claudeTree()
		c, _ := cf.stagedClone()
		if _, err := cf.svc.Apply(context.Background(), approval(c)); err != nil {
			t.Fatal(err)
		}
		cf.trip.arm(at, true)

		killed := interrupted(func() { _, _ = cf.svc.Undo(context.Background(), updates.UndoRequest{Target: cloneTarget}) })

		step := "end"
		if killed {
			step = cf.trip.ops[at-1]
		}
		cf.reopen()
		if !killed {
			finished = true
			cf.assertClaudeTree(before, "a whole undo")
		}
		if _, ok := cf.operation(); !ok {
			continue
		}
		if _, err := cf.svc.Recover(context.Background()); err != nil {
			t.Fatalf("undo interrupted at %d (%s): Recover: %v", at, step, err)
		}
		recovered++
		cf.assertClaudeTree(before, "recover of an undo after "+step)
	}
	if recovered == 0 {
		t.Fatal("no interruption left a record to recover")
	}
}

// TestCloneApplyThatCannotFastForwardIsBlocked: upstream was rewritten after
// the check, so HEAD is not an ancestor of the fetched commit. The apply
// fails, the clone is put back byte for byte, and the candidate is blocked
// rather than offered again.
func TestCloneApplyThatCannotFastForwardIsBlocked(t *testing.T) {
	cf := newCloneFixture(t)
	before := cf.claudeTree()
	c1 := cf.status().Head
	// The rewritten history: the new commit does not descend from c1.
	c2 := cf.publish(cloneV2)
	cf.clone.AddRemoteCommit(c2, "", cf.trees[c2])
	tc := targetResult(t, cf.check(), cloneTarget)
	if tc.Outcome != updates.CheckUpdateAvailable {
		t.Fatalf("check = %+v, want update_available: HEAD %s is on origin/main", tc, c1)
	}
	c := cf.candidate(tc.CandidateID)

	res, err := cf.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrNotFastForward) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("Apply = %+v, %v; want a failure with ErrNotFastForward", res, err)
	}
	cf.assertClaudeTree(before, "after a failed fast-forward")
	if got := cf.candidate(c.ID); got.State != domain.CandidateBlocked || got.BlockReason != domain.BlockLocalCommits {
		t.Errorf("candidate = %s %s, want blocked by local_commits", got.State, got.BlockReason)
	}
	if again := targetResult(t, cf.check(), cloneTarget); again.Outcome != updates.CheckBlocked || again.CandidateID != c.ID {
		t.Errorf("check after the failure = %+v, want %s still blocked", again, c.ID)
	}
}

func TestCloneUndoOfAChangedCloneKeepsACopyFirst(t *testing.T) {
	cf := newCloneFixture(t)
	c, _ := cf.stagedClone()
	if _, err := cf.svc.Apply(context.Background(), approval(c)); err != nil {
		t.Fatal(err)
	}
	writeTree(t, cf.root, map[string]string{"mine.md": "mine\n"})
	st := cf.status()
	st.Clean = false
	cf.clone.SetStatus(st)
	changed := cf.claudeTree()

	res, err := cf.svc.Undo(context.Background(), updates.UndoRequest{Target: cloneTarget})

	if !errors.Is(err, updates.ErrUndoNeedsConfirmation) || !res.Changed || res.SavedSnapshotID == "" {
		t.Fatalf("Undo = %+v, %v; want a confirmation request with a saved copy", res, err)
	}
	cf.assertClaudeTree(changed, "an undo waiting for confirmation")
	saved := filepath.Join(cf.p.SnapshotsDir(), res.SavedSnapshotID, "folder")
	if data, err := os.ReadFile(filepath.Join(saved, "mine.md")); err != nil || string(data) != "mine\n" {
		t.Errorf("saved copy lacks the user's file: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(saved, ".git", "HEAD")); err != nil {
		t.Errorf("saved copy lacks .git: %v", err)
	}
	snaps, _ := cf.svc.Snapshots(cloneTarget)
	if i := slices.IndexFunc(snaps, func(s domain.Snapshot) bool { return s.ID == res.SavedSnapshotID }); i < 0 || !snaps[i].Keep {
		t.Errorf("saved copy %s is not kept: %+v", res.SavedSnapshotID, snaps)
	}
}

// TestCloneApplyRevertsWhenVerificationFails: a fast-forward git reported as
// done that did not move HEAD, or that left the clone dirty, fails the
// verification; the clone is put back from the snapshot.
func TestCloneApplyRevertsWhenVerificationFails(t *testing.T) {
	tests := []struct {
		name string
		make func(cf *cloneFixture)
	}{
		{"HEAD did not move", func(cf *cloneFixture) { cf.git.skipFastForward = true }},
		{"a checked-out file is not the reviewed one", func(cf *cloneFixture) {
			cf.git.afterFastForward = func() {
				// What eol=crlf from an attribute nobody saw would do.
				writeTree(cf.t, cf.root, map[string]string{"skills/alpha/SKILL.md": strings.ReplaceAll(cloneV2["skills/alpha/SKILL.md"], "\n", "\r\n")})
			}
		}},
		{"clone left dirty", func(cf *cloneFixture) {
			cf.git.afterFastForward = func() {
				st := cf.status()
				st.Clean = false
				cf.clone.SetStatus(st)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := newCloneFixture(t)
			before := cf.claudeTree()
			c, _ := cf.stagedClone()
			tt.make(cf)

			res, err := cf.svc.Apply(context.Background(), approval(c))

			if !errors.Is(err, updates.ErrVerifyFailed) || res.Outcome != updates.OutcomeFailed {
				t.Fatalf("Apply = %+v, %v; want ErrVerifyFailed", res, err)
			}
			cf.assertClaudeTree(before, "after a failed verification")
			if got := cf.candidate(c.ID).State; got != domain.CandidatePending {
				t.Errorf("candidate = %s, want pending", got)
			}
		})
	}
}

// TestCloneCheckBlocksAttributesThatTransformTheCheckout: a .gitattributes
// that sets a filter or an end-of-line conversion would make git write files
// that are not the reviewed ones, so the clone is blocked. One that only
// changes how git diffs or merges does not block.
func TestCloneCheckBlocksAttributesThatTransformTheCheckout(t *testing.T) {
	tests := []struct {
		name, path, content string
		blocks              bool
	}{
		{"filter", ".gitattributes", "notes.md filter=lfs diff=lfs merge=lfs\n", true},
		{"eol", ".gitattributes", "*.md eol=crlf\n", true},
		{"text", ".gitattributes", "*.md text\n", true},
		{"-text", ".gitattributes", "*.md -text\n", true},
		{"text=auto", ".gitattributes", "* text=auto\n", true},
		{"ident", ".gitattributes", "*.sh ident\n", true},
		{"working-tree-encoding", ".gitattributes", "*.txt working-tree-encoding=UTF-16LE\n", true},
		{"crlf", ".gitattributes", "*.bat crlf\n", true},
		{"-crlf", ".gitattributes", "*.bat -crlf\n", true},
		{"binary, a macro of -text", ".gitattributes", "*.png binary\n", true},
		{"macro definition", ".gitattributes", "[attr]mine text eol=lf\n*.md mine\n", true},
		{"in a skill folder", "skills/alpha/.gitattributes", "*.md eol=crlf\n", true},
		{"after a comment and tabs", ".gitattributes", "# comment\n*.md\tlinguist-documentation\ttext\n", true},
		{"diff and merge only", ".gitattributes", "*.png -diff\n*.lock merge=ours\n", false},
		{"linguist only", ".gitattributes", "docs/** linguist-documentation\n# text eol=crlf\n", false},
		{"attribute name that only contains a word", ".gitattributes", "*.md mytext=x textual\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := newCloneFixture(t)
			files := maps.Clone(cloneV2)
			files[tt.path] = tt.content
			cf.publishRemote(cf.status().Head, files)
			before := cf.claudeTree()

			tc := targetResult(t, cf.check(), cloneTarget)

			if !tt.blocks {
				if tc.Outcome != updates.CheckUpdateAvailable {
					t.Fatalf("check = %+v, want update_available", tc)
				}
				return
			}
			if tc.Outcome != updates.CheckBlocked {
				t.Fatalf("check = %+v, want blocked", tc)
			}
			c := cf.candidate(tc.CandidateID)
			if c.BlockReason != domain.BlockGitAttributes || c.StagedPath != "" {
				t.Errorf("candidate = %s %s staged at %q, want blocked by git_attributes with no staging", c.State, c.BlockReason, c.StagedPath)
			}
			cf.assertClaudeTree(before, "a clone blocked by its attributes")
		})
	}
}

// TestCloneCheckBlocksAttributesTheCloneAlreadyHas: attributes of the
// installed commit, or of the clone's own .git/info/attributes, apply to the
// checkout too.
func TestCloneCheckBlocksAttributesTheCloneAlreadyHas(t *testing.T) {
	t.Run("in the installed commit", func(t *testing.T) {
		cf := newCloneFixtureWith(t, maps2Clone(cloneV1, map[string]string{".gitattributes": "*.md text\n"}))
		cf.publishRemote(cf.status().Head, cloneV2)

		tc := targetResult(t, cf.check(), cloneTarget)

		if tc.Outcome != updates.CheckBlocked || cf.candidate(tc.CandidateID).BlockReason != domain.BlockGitAttributes {
			t.Fatalf("check = %+v, want blocked by git_attributes", tc)
		}
	})
	t.Run("in .git/info/attributes", func(t *testing.T) {
		cf := newCloneFixture(t)
		writeTree(t, cf.root, map[string]string{".git/info/attributes": "*.md eol=crlf\n"})
		cf.publishRemote(cf.status().Head, cloneV2)

		tc := targetResult(t, cf.check(), cloneTarget)

		if tc.Outcome != updates.CheckBlocked || cf.candidate(tc.CandidateID).BlockReason != domain.BlockGitAttributes {
			t.Fatalf("check = %+v, want blocked by git_attributes", tc)
		}
	})
}

// TestCloneApplyRechecksAttributes: attributes that appear after the check
// in .git/info/attributes are refused at the revalidation, and a fetched
// commit whose tree is not the staged one is refused before the merge.
func TestCloneApplyRechecksAttributes(t *testing.T) {
	t.Run("info/attributes added after the check", func(t *testing.T) {
		cf := newCloneFixture(t)
		c, _ := cf.stagedClone()
		writeTree(t, cf.root, map[string]string{".git/info/attributes": "* text=auto\n"})
		before := cf.claudeTree()

		res, err := cf.svc.Apply(context.Background(), approval(c))

		if !errors.Is(err, updates.ErrRejected) || res.Outcome != updates.OutcomeRejected {
			t.Fatalf("Apply = %+v, %v; want a rejection", res, err)
		}
		cf.assertClaudeTree(before, "after the rejection")
		if n := cf.gitCalls("Fetch") + cf.gitCalls("FastForward"); n != 0 {
			t.Errorf("a rejected apply ran %d git writes", n)
		}
	})
	t.Run("info/attributes added after the fetch", func(t *testing.T) {
		cf := newCloneFixture(t)
		before := cf.claudeTree()
		c, _ := cf.stagedClone()
		cf.git.afterFetch = func() {
			writeTree(cf.t, cf.root, map[string]string{".git/info/attributes": "*.md filter=lfs\n"})
		}

		res, err := cf.svc.Apply(context.Background(), approval(c))

		if !errors.Is(err, updates.ErrGitAttributes) || res.Outcome != updates.OutcomeFailed {
			t.Fatalf("Apply = %+v, %v; want a failure with ErrGitAttributes", res, err)
		}
		if n := cf.gitCalls("FastForward"); n != 0 {
			t.Errorf("merged with attributes that transform the checkout")
		}
		cf.assertClaudeTree(before, "after the refused merge")
	})
	t.Run("fetched commit is not the staged tree", func(t *testing.T) {
		cf := newCloneFixture(t)
		before := cf.claudeTree()
		c, c2 := cf.stagedClone()
		cf.clone.AddRemoteCommit(c2, cf.status().Head, "a-tree-nobody-reviewed")

		res, err := cf.svc.Apply(context.Background(), approval(c))

		if err == nil || res.Outcome != updates.OutcomeFailed {
			t.Fatalf("Apply = %+v, %v; want a failure", res, err)
		}
		if n := cf.gitCalls("FastForward"); n != 0 {
			t.Errorf("merged a commit that is not the reviewed one")
		}
		cf.assertClaudeTree(before, "after the refused merge")
	})
}

func maps2Clone(a, b map[string]string) map[string]string {
	out := maps.Clone(a)
	maps.Copy(out, b)
	return out
}
