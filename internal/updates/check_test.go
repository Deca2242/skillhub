package updates_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

func TestCheckStagesACandidateAndChangesNothingInstalled(t *testing.T) {
	f := newFixture(t)
	v1 := f.standard()
	commit2, v2 := f.publish("main", v2Files)
	before := f.agentsTree()

	report := f.check()

	f.assertAgentsTree(before, "after a check")
	tc := targetResult(t, report, target)
	if tc.Outcome != updates.CheckUpdateAvailable || tc.Err != nil {
		t.Fatalf("outcome = %s, err = %v; want update_available", tc.Outcome, tc.Err)
	}
	if !slices.Equal(report.NewCandidates, []string{tc.CandidateID}) {
		t.Errorf("NewCandidates = %v, want [%s]", report.NewCandidates, tc.CandidateID)
	}
	c := f.candidate(tc.CandidateID)
	if c.Target != target || !slices.Equal(c.Skills, []string{target}) {
		t.Errorf("target = %s, skills = %v", c.Target, c.Skills)
	}
	if c.State != domain.CandidatePending || c.FromSHA != v1 || c.InstalledSHAAtStage != v1 ||
		c.ToSHA != v2 || c.ToCommit != commit2 || c.RulesetVersion != 1 || !c.FirstSeenAt.Equal(t0) {
		t.Errorf("candidate = %+v", c)
	}
	if got := hashOf(t, c.StagedPath); got != v2 {
		t.Errorf("staged tree = %s, want %s", got, v2)
	}
	if dir := filepath.Dir(c.StagedPath); dir != f.p.StagingDir() {
		t.Errorf("staged under %s, want %s", dir, f.p.StagingDir())
	}
	wantChanges := []domain.FileChange{
		{Path: "SKILL.md", Change: domain.ChangeModified, OldMode: "100644", NewMode: "100644",
			OldSHA: treehash.BlobSHA([]byte(v1Files["SKILL.md"])), NewSHA: treehash.BlobSHA([]byte(v2Files["SKILL.md"]))},
		{Path: "notes.md", Change: domain.ChangeModified, OldMode: "100644", NewMode: "100644",
			OldSHA: treehash.BlobSHA([]byte(v1Files["notes.md"])), NewSHA: treehash.BlobSHA([]byte(v2Files["notes.md"]))},
		{Path: "scripts/run.sh", Change: domain.ChangeAdded, NewMode: "100755",
			NewSHA: treehash.BlobSHA([]byte(v2Files["scripts/run.sh*"]))},
	}
	if !slices.Equal(c.FileChanges, wantChanges) {
		t.Errorf("file changes:\n got %+v\nwant %+v", c.FileChanges, wantChanges)
	}
	if !slices.Equal(c.FrontmatterDelta.Changed, []string{"description"}) {
		t.Errorf("frontmatter delta = %v, want [description]", c.FrontmatterDelta.Changed)
	}
	wantLabels := []domain.Label{domain.LabelNonTextFiles, domain.LabelTouchesScripts, domain.LabelModeChange, domain.LabelSuspiciousText}
	if !slices.Equal(c.Labels, wantLabels) {
		t.Errorf("labels = %v, want %v", c.Labels, wantLabels)
	}
	checks := f.persisted().Checks
	if !checks.LastRun.Equal(t0) || !checks.LastSuccess.Equal(t0) || checks.LastFailed {
		t.Errorf("checks = %+v", checks)
	}
}

func TestCheckReportsUpToDate(t *testing.T) {
	f := newFixture(t)
	f.standard()

	tc := targetResult(t, f.check(), target)

	if tc.Outcome != updates.CheckUpToDate {
		t.Fatalf("outcome = %s (%v), want up_to_date", tc.Outcome, tc.Err)
	}
	if n := len(f.persisted().Candidates); n != 0 {
		t.Errorf("%d candidates, want 0", n)
	}
}

func TestCheckDoesNotFetchTheTreeWhenTheHeadDidNotMove(t *testing.T) {
	f := newFixture(t)
	f.standard()
	first := f.staged()
	trees := f.ghCalls("Tree")

	tc := targetResult(t, f.check(), target)

	if got := f.ghCalls("Tree"); got != trees {
		t.Errorf("tree fetched again: %d calls, want %d", got, trees)
	}
	if tc.CandidateID != first.ID || tc.Outcome != updates.CheckUpdateAvailable {
		t.Errorf("second check = %+v, want the same candidate %s", tc, first.ID)
	}

	// Upstream moves: a new candidate supersedes the first and its staging
	// is removed.
	_, v3 := f.publish("main", v3Files)
	tc = targetResult(t, f.check(), target)
	if got := f.ghCalls("Tree"); got != trees+1 {
		t.Errorf("tree calls = %d, want %d", got, trees+1)
	}
	if tc.CandidateID == first.ID || f.candidate(tc.CandidateID).ToSHA != v3 {
		t.Fatalf("third check = %+v, want a new candidate to %s", tc, v3)
	}
	if got := f.candidate(first.ID).State; got != domain.CandidateSuperseded {
		t.Errorf("first candidate state = %s, want superseded", got)
	}
	if _, err := os.Stat(first.StagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("superseded staging still there: %v", err)
	}
}

func TestCheckUpToDateWithUnchangedHeadUsesTheRecordedTree(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.check()
	trees := f.ghCalls("Tree")

	tc := targetResult(t, f.check(), target)

	if tc.Outcome != updates.CheckUpToDate || f.ghCalls("Tree") != trees {
		t.Errorf("outcome = %s, tree calls %d -> %d", tc.Outcome, trees, f.ghCalls("Tree"))
	}
}

func TestCheckRestagesWhenTheInstalledFolderChanged(t *testing.T) {
	f := newFixture(t)
	f.standard()
	first := f.staged()
	f.home.WriteFile(".agents/skills/alpha/extra.md", "mine\n")

	tc := targetResult(t, f.check(), target)

	c := f.candidate(tc.CandidateID)
	if c.ID == first.ID || c.InstalledSHAAtStage != hashOf(t, f.folder()) {
		t.Fatalf("candidate %s staged at %s, want a new one at the current folder", c.ID, c.InstalledSHAAtStage)
	}
	if !slices.Contains(c.Labels, domain.LabelLocallyModified) {
		t.Errorf("labels = %v, want locally_modified", c.Labels)
	}
	if f.candidate(first.ID).State != domain.CandidateSuperseded {
		t.Errorf("first candidate not superseded")
	}
}

func TestCheckCopiesUnchangedFilesFromTheInstalledFolder(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.publish("main", map[string]string{"SKILL.md": v1Files["SKILL.md"], "notes.md": "changed\n"})

	c := f.candidate(targetResult(t, f.check(), target).CandidateID)

	var fetched []string
	for _, call := range f.gh.Calls() {
		if call.Op == "Blob" {
			fetched = append(fetched, call.Args[1])
		}
	}
	if want := []string{treehash.BlobSHA([]byte("changed\n"))}; !slices.Equal(fetched, want) {
		t.Errorf("blobs fetched = %v, want only %v", fetched, want)
	}
	if !slices.Equal(c.Labels, []domain.Label{domain.LabelTextOnly}) {
		t.Errorf("labels = %v, want [text_only]", c.Labels)
	}
}

func TestCheckFollowsTheRefOfAPinnedEntry(t *testing.T) {
	f := newFixture(t)
	c1, v1 := f.publish("main", v1Files)
	f.gh.Tag(repo, "v1", c1)
	f.install(v1Files)
	f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: v1, ref: "v1"})
	f.publish("main", v2Files) // the default branch moves; the ref does not

	if tc := targetResult(t, f.check(), target); tc.Outcome != updates.CheckUpToDate {
		t.Fatalf("outcome = %s (%v), want up_to_date at the ref", tc.Outcome, tc.Err)
	}
	if n := f.ghCalls("DefaultBranch"); n != 0 {
		t.Errorf("DefaultBranch called %d times for a pinned entry", n)
	}

	c3, v3 := f.publish("other", v3Files)
	f.gh.Tag(repo, "v1", c3)
	tc := targetResult(t, f.check(), target)
	c := f.candidate(tc.CandidateID)
	if c.ToSHA != v3 || c.ToCommit != c3 {
		t.Fatalf("candidate to %s@%s, want %s@%s", c.ToSHA, c.ToCommit, v3, c3)
	}
	for _, call := range f.gh.Calls() {
		if call.Op == "ResolveCommit" && call.Args[1] != "v1" {
			t.Errorf("resolved %q, want only the ref v1", call.Args[1])
		}
	}
}

func TestCheckLeavesOutTargetsItMayNotUpdate(t *testing.T) {
	f := newFixture(t)
	_, v1 := f.publish("main", v1Files)
	f.publish("main", v2Files)
	writeTree(t, f.home.Path(".agents/skills/OFF/parked"), v1Files)
	writeTree(t, f.home.Path("elsewhere/outside"), v1Files)
	f.home.Symlink(".agents/skills/linked", "../../elsewhere/outside")
	writeTree(t, f.home.Path(".agents/skills/cloned"), v1Files)
	f.home.Mkdir(".agents/skills/cloned/.git")
	writeTree(t, f.home.Path(".agents/skills/real"), v1Files)
	f.home.Symlink(".agents/skills/alias", "real")
	entry := func(name string) lockEntry {
		return lockEntry{name: name, source: repo, skillPath: skillPath, hash: v1}
	}
	f.writeLock(entry("missing"), entry("parked"), entry("linked"), entry("cloned"), entry("alias"))
	before := f.agentsTree()

	report := f.check()

	f.assertAgentsTree(before, "after a check")
	for _, name := range []string{"missing", "parked", "linked", "cloned"} {
		if tc := targetResult(t, report, domain.LockID(name)); tc.Outcome != updates.CheckNotChecked {
			t.Errorf("%s: outcome = %s, want not_checked", name, tc.Outcome)
		}
	}
	// A store alias resolves to its sibling and is updated there.
	if tc := targetResult(t, report, domain.LockID("alias")); tc.Outcome != updates.CheckUpdateAvailable {
		t.Errorf("alias: outcome = %s (%v), want update_available", tc.Outcome, tc.Err)
	}
}

// lyingGitHub serves a blob whose content does not match its SHA.
type lyingGitHub struct {
	ports.GitHubPort
}

func (l lyingGitHub) Blob(ctx context.Context, r, sha string) ([]byte, error) {
	data, err := l.GitHubPort.Blob(ctx, r, sha)
	return append(data, 'x'), err
}

func TestCheckBlocksACandidateItCannotStageSafely(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(f *fixture)
		reason  domain.BlockReason
	}{
		{
			name: "symlink upstream",
			prepare: func(f *fixture) {
				f.publishWith(func(dir string) { _ = os.Symlink("SKILL.md", filepath.Join(dir, "link.md")) })
			},
			reason: domain.BlockUnsupportedEntry,
		},
		{
			name: "blob does not match its sha",
			prepare: func(f *fixture) {
				f.publish("main", v2Files)
				svc, err := updates.New(f.deps(lyingGitHub{f.gh}))
				if err != nil {
					t.Fatal(err)
				}
				f.svc = svc
			},
			reason: domain.BlockHashMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.standard()
			tt.prepare(f)
			before := f.agentsTree()

			tc := targetResult(t, f.check(), target)

			f.assertAgentsTree(before, "after a blocked check")
			if tc.Outcome != updates.CheckBlocked {
				t.Fatalf("outcome = %s (%v), want blocked", tc.Outcome, tc.Err)
			}
			c := f.candidate(tc.CandidateID)
			if c.State != domain.CandidateBlocked || c.BlockReason != tt.reason {
				t.Errorf("state = %s, reason = %s; want blocked, %s", c.State, c.BlockReason, tt.reason)
			}
			if entries, _ := os.ReadDir(f.p.StagingDir()); len(entries) != 0 {
				t.Errorf("staging left behind: %v", entries)
			}
			if _, err := f.svc.Apply(context.Background(), approval(c)); !errors.Is(err, updates.ErrNotPending) {
				t.Errorf("apply of a blocked candidate: %v, want ErrNotPending", err)
			}
		})
	}
}

func TestCheckReportsAFailingRepository(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.gh.Fail = func(c fakes.Call) error {
		if c.Op == "ResolveCommit" {
			return ports.ErrRateLimited
		}
		return nil
	}

	report, err := f.svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	tc := targetResult(t, report, target)
	if tc.Outcome != updates.CheckFailed || !errors.Is(tc.Err, ports.ErrRateLimited) || !report.Failed() {
		t.Fatalf("result = %+v, want failed with the rate limit", tc)
	}
	checks := f.persisted().Checks
	if !checks.LastRun.Equal(t0) || !checks.LastSuccess.IsZero() || !checks.LastFailed {
		t.Errorf("checks = %+v, want a failed run", checks)
	}
}

func TestCandidatesListsPendingAndBlocked(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()

	got, err := f.svc.Candidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("candidates = %+v, want [%s]", got, c.ID)
	}
}
