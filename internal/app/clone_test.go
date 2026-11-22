package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

// A clone of acme/clone in Claude's directory, holding two skills below
// skills/; the clone itself is the update target.
const (
	cloneRepo   = "acme/clone"
	cloneTarget = "git:.claude/skills/clone"
	cloneAlpha  = "git:.claude/skills/clone/skills/alpha"
	cloneBeta   = "git:.claude/skills/clone/skills/beta"
)

var (
	cloneV1 = map[string]string{
		"skills/alpha/SKILL.md": "---\nname: alpha-clone\ndescription: v1\n---\none\n",
		"skills/beta/SKILL.md":  "---\nname: beta-clone\ndescription: v1\n---\none\n",
	}
	cloneV2 = map[string]string{
		"skills/alpha/SKILL.md": "---\nname: alpha-clone\ndescription: v2\n---\ntwo\n",
		"skills/beta/SKILL.md":  "---\nname: beta-clone\ndescription: v1\n---\none\n",
	}
)

// cloneSetup installs cloneV1 as a clean clone on main tracking origin/main
// and publishes cloneV2 upstream. It returns the fake clone, the first and
// the second commit.
func (f *fixture) cloneSetup() (*fakes.Clone, string, string) {
	f.t.Helper()
	f.gh.AddRepo(cloneRepo, "main")
	dir := filepath.Join(f.t.TempDir(), "clone-upstream")
	writeTree(f.t, dir, cloneV1)
	c1 := f.gh.Commit(f.t, cloneRepo, "main", dir)
	t1 := hashOf(f.t, dir)
	if err := os.RemoveAll(dir); err != nil {
		f.t.Fatal(err)
	}
	writeTree(f.t, dir, cloneV2)
	c2 := f.gh.Commit(f.t, cloneRepo, "main", dir)
	t2 := hashOf(f.t, dir)

	root := f.home.Path(".claude/skills/clone")
	writeTree(f.t, root, cloneV1)
	f.home.WriteFile(".claude/skills/clone/.git/HEAD", "ref: refs/heads/main\n")
	c := f.git.AddClone(root, ports.CloneStatus{
		Remote: "git@github.com:acme/clone.git", Branch: "main", Upstream: "origin/main", Head: c1, Clean: true,
	})
	c.AddCommit(c1, "", t1)
	// The fake knows commits by name: origin/main points at c1.
	c.AddCommit("origin/main", c1, t1)
	c.AddRemoteCommit(c2, c1, t2)
	return c, c1, c2
}

func TestCheckFindsTheClonesOfTheInventory(t *testing.T) {
	f := newFixture(t)
	_, _, c2 := f.cloneSetup()

	res, err := f.app.Check(ctx, app.CheckOptions{})
	if err != nil || res.Failed {
		t.Fatalf("Check = %+v, %v", res, err)
	}

	i := slices.IndexFunc(res.Report.Targets, func(tc updates.TargetCheck) bool { return tc.Target == cloneTarget })
	if i < 0 || res.Report.Targets[i].Outcome != updates.CheckUpdateAvailable {
		t.Fatalf("report = %+v, want %s update_available", res.Report.Targets, cloneTarget)
	}
	c, err := f.app.CandidateFor(cloneAlpha)
	if err != nil {
		t.Fatalf("CandidateFor(%s): %v", cloneAlpha, err)
	}
	if c.Target != cloneTarget || c.ToCommit != c2 || !slices.Equal(c.Skills, []string{cloneAlpha, cloneBeta}) {
		t.Errorf("candidate = %+v", c)
	}
	skills, err := f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{cloneAlpha, cloneBeta} {
		if s := skillByID(t, skills, id); s.State != domain.StateUpdateAvailable {
			t.Errorf("%s state = %s, want update_available", id, s.State)
		}
	}
	diff, err := f.app.Diff(ctx, c)
	if err != nil || len(diff) != 1 || diff[0].Path != "skills/alpha/SKILL.md" || diff[0].Unified == "" {
		t.Errorf("Diff = %+v, %v; want the change of skills/alpha/SKILL.md", diff, err)
	}
}

func TestSkillsOfAnUpToDateClone(t *testing.T) {
	f := newFixture(t)
	c, c1, c2 := f.cloneSetup()
	st, _ := f.git.Describe(ctx, f.home.Path(".claude/skills/clone"))
	c.AddCommit(c2, c1, "")
	st.Head = c2
	c.SetStatus(st)

	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}

	skills, err := f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{cloneAlpha, cloneBeta} {
		if s := skillByID(t, skills, id); s.State != domain.StateUpToDate {
			t.Errorf("%s state = %s, want up_to_date", id, s.State)
		}
	}
}

// TestSkillsShowsAClonesLocalCommitsFoundByTheCheck: the inventory cannot
// see commits its tracking branch never had; the check, which knows the
// upstream commit, blocks the clone and the skills show locally_modified.
func TestSkillsShowsAClonesLocalCommitsFoundByTheCheck(t *testing.T) {
	f := newFixture(t)
	c, c1, c2 := f.cloneSetup()
	st, _ := f.git.Describe(ctx, f.home.Path(".claude/skills/clone"))
	c.AddCommit(c2, c1, "t2")
	c.AddCommit("local", "unknown-to-origin", "tl")
	st.Head = "local"
	c.SetStatus(st)
	// The tracking branch cannot be compared, as when it was never fetched.
	f.git.Fail = func(call fakes.Call) error {
		if call.Op == "IsAncestor" && call.Args[2] == "origin/main" {
			return errors.New("fatal: Not a valid commit name origin/main")
		}
		return nil
	}

	res, err := f.app.Check(ctx, app.CheckOptions{})
	if err != nil || res.Failed {
		t.Fatalf("Check = %+v, %v", res, err)
	}

	skills, err := f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{cloneAlpha, cloneBeta} {
		if s := skillByID(t, skills, id); !slices.Contains(s.Issues, domain.IssueLocallyModified) || s.Issues[0] != domain.IssueLocallyModified {
			t.Errorf("%s issues = %v, want locally_modified first", id, s.Issues)
		}
	}
	cand, err := f.app.CandidateFor(cloneAlpha)
	if err != nil || cand.State != domain.CandidateBlocked || cand.BlockReason != domain.BlockLocalCommits {
		t.Errorf("candidate = %+v, %v; want blocked by local_commits", cand, err)
	}
}

func TestUndoByTheNameOfASkillInsideAnAppliedClone(t *testing.T) {
	f := newFixture(t)
	f.cloneSetup()
	before := f.homeTree()
	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	c, err := f.app.CandidateFor(cloneAlpha)
	if err != nil {
		t.Fatal(err)
	}
	// The fake git moves HEAD only; check the commit's files out as git
	// would, since the apply verifies the files it installed.
	f.git.Fail = func(call fakes.Call) error {
		if call.Op == "FastForward" {
			writeTree(f.t, f.home.Path(".claude/skills/clone"), cloneV2)
		}
		return nil
	}
	if _, err := f.app.Apply(ctx, updates.Approval{CandidateID: c.ID, ToSHA: c.ToSHA}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	id, err := f.app.Resolve(ctx, "alpha")
	if err != nil || id != cloneAlpha {
		t.Fatalf("Resolve(alpha) = %q, %v", id, err)
	}

	res, err := f.app.Undo(ctx, updates.UndoRequest{Target: id})

	if err != nil || res.Target != cloneTarget {
		t.Fatalf("Undo(%s) = %+v, %v; want the undo of %s", id, res, err, cloneTarget)
	}
	if after := f.homeTree(); !mapsEqual(before, after) {
		t.Error("undo did not restore the clone")
	}
	if _, err := f.app.Undo(ctx, updates.UndoRequest{Target: "git:.claude/skills/clone-other/x"}); !errors.Is(err, updates.ErrNothingToUndo) {
		t.Errorf("Undo of an unrelated id = %v, want ErrNothingToUndo", err)
	}
}

func TestAnInterruptedCloneOperationMarksItsSkills(t *testing.T) {
	f := newFixture(t)
	f.cloneSetup()
	f.record(domain.Operation{ID: "apply-x", Kind: domain.OperationApply, Target: cloneTarget, RestoreSnapshotID: "x", StartedAt: t0})

	skills, err := f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{cloneAlpha, cloneBeta} {
		if s := skillByID(t, skills, id); !slices.Contains(s.Issues, domain.IssueInterruptedUpdate) {
			t.Errorf("%s issues = %v, want interrupted_update", id, s.Issues)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
