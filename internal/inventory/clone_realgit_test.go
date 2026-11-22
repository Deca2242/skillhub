//go:build realgit

package inventory_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	gitadapter "skillhub/internal/adapters/git"
	"skillhub/internal/adapters/proc"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func realGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{
		"-c", "user.name=skillhub test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
	}, args...)
	res, err := proc.New().Run(context.Background(), ports.Command{Name: "git", Args: full, Dir: dir})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("git %s: %v, exit %d, stderr %s", strings.Join(args, " "), err, res.ExitCode, res.Stderr)
	}
}

// githubRemote shows the clone's local origin as a GitHub URL: get-url
// expands insteadOf, so a fixture cannot point a GitHub URL at a local path.
type githubRemote struct {
	ports.GitPort
	upstream string
}

func (g githubRemote) Describe(ctx context.Context, root string) (ports.CloneStatus, error) {
	st, err := g.GitPort.Describe(ctx, root)
	if st.Remote == g.upstream {
		st.Remote = "https://github.com/owner/repo.git"
	}
	return st, err
}

// TestRealScanReportsEveryCloneSituation covers the rows of the "Clones de
// git" table as the inventory lists them, on real clones that the scan
// leaves byte for byte as they were.
func TestRealScanReportsEveryCloneSituation(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, clone, upstream string)
		want []domain.Issue
	}{
		{"clean, behind origin", func(t *testing.T, _, upstream string) {
			writeFile(t, filepath.Join(upstream, "new.md"), "new\n")
			realGit(t, upstream, "add", "-A")
			realGit(t, upstream, "commit", "-q", "-m", "new")
		}, []domain.Issue{}},
		{"modified file", func(t *testing.T, clone, _ string) {
			writeFile(t, filepath.Join(clone, "SKILL.md"), "mine\n")
		}, []domain.Issue{domain.IssueLocallyModified}},
		{"untracked file", func(t *testing.T, clone, _ string) {
			writeFile(t, filepath.Join(clone, "mine.md"), "mine\n")
		}, []domain.Issue{domain.IssueLocallyModified}},
		{"local commit", func(t *testing.T, clone, _ string) {
			writeFile(t, filepath.Join(clone, "mine.md"), "mine\n")
			realGit(t, clone, "add", "-A")
			realGit(t, clone, "commit", "-q", "-m", "mine")
		}, []domain.Issue{domain.IssueLocallyModified}},
		{"divergent history", func(t *testing.T, clone, upstream string) {
			writeFile(t, filepath.Join(upstream, "new.md"), "new\n")
			realGit(t, upstream, "add", "-A")
			realGit(t, upstream, "commit", "-q", "-m", "new")
			writeFile(t, filepath.Join(clone, "mine.md"), "mine\n")
			realGit(t, clone, "add", "-A")
			realGit(t, clone, "commit", "-q", "-m", "mine")
			realGit(t, clone, "fetch", "-q", "origin")
		}, []domain.Issue{domain.IssueLocallyModified}},
		{"detached HEAD", func(t *testing.T, clone, _ string) {
			realGit(t, clone, "checkout", "-q", "--detach")
		}, []domain.Issue{domain.IssueUntrackedBranch}},
		{"branch that does not track origin", func(t *testing.T, clone, _ string) {
			realGit(t, clone, "checkout", "-q", "-b", "mine")
		}, []domain.Issue{domain.IssueUntrackedBranch}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := testsupport.NewHome(t)
			upstream := filepath.Join(t.TempDir(), "upstream")
			realGit(t, filepath.Dir(upstream), "init", "-q", upstream)
			writeFile(t, filepath.Join(upstream, "SKILL.md"), skillMD("repo", "A clone"))
			realGit(t, upstream, "add", "-A")
			realGit(t, upstream, "commit", "-q", "-m", "first")
			clone := h.Path(".claude/skills/repo")
			if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
				t.Fatal(err)
			}
			realGit(t, filepath.Dir(clone), "clone", "-q", upstream, clone)
			tt.make(t, clone, upstream)
			before := cloneHash(t, clone)

			guard := h.Paths().Guard()
			skills, err := inventory.Scan(context.Background(), inventory.Deps{
				Paths: h.Paths(), Agents: []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
				Dirs: fsadapter.NewScanner(guard), Files: fsadapter.NewFileOps(guard), Lock: fakes.NewSkillsLockStore(),
				Git: githubRemote{GitPort: gitadapter.New(proc.New()), upstream: upstream},
			}, nil)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}

			if len(skills) != 1 || skills[0].Origin.Kind != domain.OriginGitClone {
				t.Fatalf("Scan = %+v, want one clone skill", skills)
			}
			if got := skills[0].Issues; !slices.Equal(got, tt.want) {
				t.Errorf("issues = %v, want %v", got, tt.want)
			}
			if after := cloneHash(t, clone); after != before {
				t.Errorf("the scan changed the clone: %s, was %s", after, before)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cloneHash hashes a clone's work tree and its .git directory.
func cloneHash(t *testing.T, clone string) string {
	t.Helper()
	work, err := treehash.Dir(clone)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := treehash.Dir(filepath.Join(clone, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	return work.SHA + "+" + gitDir.SHA
}
