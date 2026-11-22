package inventory_test

import (
	"context"
	"slices"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

// scanOneClone scans a home holding only a GitHub clone with its skill at
// its root, described by git.
func scanOneClone(t *testing.T, setup func(c *fakes.Clone, status *ports.CloneStatus)) domain.Skill {
	t.Helper()
	h := testsupport.NewHome(t)
	h.WriteFile(".claude/skills/repo/SKILL.md", skillMD("repo", "A clone"))
	h.Mkdir(".claude/skills/repo/.git")
	git := fakes.NewGit()
	status := ports.CloneStatus{
		Remote: "https://github.com/owner/repo.git", Branch: "main", Upstream: "origin/main",
		Head: "1111111111111111111111111111111111111111", Clean: true,
	}
	c := git.AddClone(h.Path(".claude/skills/repo"), status)
	setup(c, &status)
	c.SetStatus(status)
	guard := h.Paths().Guard()
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: h.Paths(), Agents: []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Dirs: fsadapter.NewScanner(guard), Files: fsadapter.NewFileOps(guard), Lock: fakes.NewSkillsLockStore(), Git: git,
	}, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(skills) != 1 || skills[0].Origin.Kind != domain.OriginGitClone {
		t.Fatalf("Scan = %+v, want one clone skill", skills)
	}
	for _, call := range git.Calls() {
		if call.Op == "Fetch" || call.Op == "FastForward" {
			t.Errorf("Scan ran %s", call.Op)
		}
	}
	return skills[0]
}

// The fake Git knows commits by name: origin/main is a commit whose parent
// is the commit the tracking branch points at.
func TestScanMarksAClonesLocalCommits(t *testing.T) {
	const head, local = "1111111111111111111111111111111111111111", "5555555555555555555555555555555555555555"
	tests := []struct {
		name  string
		setup func(c *fakes.Clone, st *ports.CloneStatus)
		want  []domain.Issue
	}{
		{"clean and behind origin", func(c *fakes.Clone, _ *ports.CloneStatus) {
			c.AddCommit(head, "", "t1")
			c.AddCommit("origin/main", head, "t1")
		}, []domain.Issue{}},
		{"local commit not on origin", func(c *fakes.Clone, st *ports.CloneStatus) {
			c.AddCommit(head, "", "t1")
			c.AddCommit(local, head, "t2")
			c.AddCommit("origin/main", head, "t1")
			st.Head = local
		}, []domain.Issue{domain.IssueLocallyModified}},
		{"tracking branch unknown to the clone", func(c *fakes.Clone, _ *ports.CloneStatus) {
			c.AddCommit(head, "", "t1")
		}, []domain.Issue{}},
		{"detached HEAD is not compared", func(c *fakes.Clone, st *ports.CloneStatus) {
			c.AddCommit(local, head, "t2")
			st.Head, st.Branch, st.Upstream = local, "", ""
		}, []domain.Issue{domain.IssueUntrackedBranch}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanOneClone(t, tt.setup)
			if !slices.Equal(got.Issues, tt.want) {
				t.Errorf("issues = %v, want %v", got.Issues, tt.want)
			}
		})
	}
}
