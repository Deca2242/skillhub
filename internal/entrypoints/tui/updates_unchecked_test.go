package tui_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

func setupUpdatesUnchecked(t *testing.T, withCandidate bool) *fixture {
	t.Helper()
	f := newFixture(t)
	f.publish("alpha", map[string]string{"SKILL.md": "# Alpha v1\n"})
	f.home.WriteFile(".agents/skills/alpha/SKILL.md", "# Alpha v1\n")
	f.home.Symlink(".claude/skills/alpha", "../../.agents/skills/alpha")
	alphaTree, err := treehash.Dir(f.home.Path(".agents/skills/alpha"))
	if err != nil {
		t.Fatal(err)
	}

	f.home.WriteFile(".agents/skills/ui-taste/SKILL.md", "---\nname: ui-taste\ndescription: taste\n---\n")
	f.home.Symlink(".claude/skills/ui-taste", "../../.agents/skills/ui-taste")
	f.home.WriteFile(".claude/skills/manual-one/SKILL.md", "---\nname: manual-one\n---\n")
	f.home.WriteFile(".claude/skills/gitlab-skill/SKILL.md", "---\nname: gitlab-skill\n---\n")
	f.home.Mkdir(".claude/skills/gitlab-skill/.git")
	cloneRoot, err := filepath.EvalSymlinks(f.home.Path(".claude/skills/gitlab-skill"))
	if err != nil {
		t.Fatal(err)
	}
	f.git.AddClone(cloneRoot, ports.CloneStatus{
		Remote:   "https://gitlab.com/acme/gitlab-skill.git",
		Branch:   "main",
		Upstream: "origin/main",
		Head:     "1111",
		Clean:    true,
	})

	p := f.home.Paths()
	gammaEntry := ""
	if withCandidate {
		f.publish("gamma", map[string]string{"SKILL.md": "# Gamma v1\n"})
		f.home.WriteFile(".agents/skills/gamma/SKILL.md", "# Gamma v1\n")
		f.home.Symlink(".claude/skills/gamma", "../../.agents/skills/gamma")
		gammaTree, err := treehash.Dir(f.home.Path(".agents/skills/gamma"))
		if err != nil {
			t.Fatal(err)
		}
		f.publish("gamma", map[string]string{"SKILL.md": "# Gamma v2\n"})
		gammaEntry = fmt.Sprintf(`    "gamma": {
      "source": %q,
      "sourceType": "github",
      "sourceUrl": "https://github.com/%s.git",
      "skillPath": "skills/gamma/SKILL.md",
      "skillFolderHash": %q
    },
`, repo, repo, gammaTree.SHA)
	}
	f.home.WriteFile(".agents/.skill-lock.json", fmt.Sprintf(`{
  "version": 3,
  "skills": {
%s    "alpha": {
      "source": %q,
      "sourceType": "github",
      "sourceUrl": "https://github.com/%s.git",
      "skillPath": "skills/alpha/SKILL.md",
      "skillFolderHash": %q
    },
    "ui-taste": {
      "source": "uizze.sh",
      "sourceType": "well-known",
      "sourceUrl": "https://uizze.sh/.well-known/agent-skills/ui-taste/SKILL.md",
      "skillPath": "SKILL.md",
      "skillFolderHash": "abc"
    }
  }
}
`, gammaEntry, repo, repo, alphaTree.SHA))

	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("a.Check: %v", err)
	}

	stStore := statefile.New(p)
	st, err := stStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Results == nil {
		st.Results = map[string]domain.TargetResult{}
	}
	st.Results["lock:alpha"] = domain.TargetResult{
		State:     domain.StateCheckFailed,
		CheckedAt: f.clock.Now().Add(-3 * time.Hour),
		Detail:    "gh timeout",
	}
	if err := stStore.Save(st); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestUpdatesViewShowsUncheckedSkills(t *testing.T) {
	t.Run("no candidates with website, manual, and failed check", func(t *testing.T) {
		f := setupUpdatesUnchecked(t, false)
		m := openSized(t, f.newApp(), 100, 30)
		pressRun(m, "2")

		got := ansi.Strip(m.View().Content)
		for _, want := range []string{
			"ui-taste",
			"sin seguimiento",
			"manual-one",
			"no revisable",
			"gitlab-skill",
			"alpha",
			"hace 3 horas",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("want %q in view:\n%s", want, got)
			}
		}
	})

	t.Run("with candidates shows unchecked below", func(t *testing.T) {
		f := setupUpdatesUnchecked(t, true)
		m := openSized(t, f.newApp(), 100, 30)
		pressRun(m, "2")

		got := ansi.Strip(m.View().Content)
		for _, want := range []string{
			"gamma",
			"ui-taste",
			"sin seguimiento",
			"manual-one",
			"no revisable",
			"alpha",
			"hace 3 horas",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("want %q in view:\n%s", want, got)
			}
		}
	})
}

func TestGoldenUpdatesUnchecked(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("NoChanges%d", width), func(t *testing.T) {
			f := setupUpdatesUnchecked(t, false)
			m := openSized(t, f.newApp(), width, 30)
			pressRun(m, "2")
			teatest.RequireEqualOutput(t, []byte(ansi.Strip(m.View().Content)))
		})
		t.Run(fmt.Sprintf("WithCandidates%d", width), func(t *testing.T) {
			f := setupUpdatesUnchecked(t, true)
			m := openSized(t, f.newApp(), width, 30)
			pressRun(m, "2")
			teatest.RequireEqualOutput(t, []byte(ansi.Strip(m.View().Content)))
		})
	}
}
