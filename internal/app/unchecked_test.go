package app_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

func TestUncheckedSkills(t *testing.T) {
	t.Run("only checkable up-to-date github skills returns empty", func(t *testing.T) {
		f := newFixture(t)
		f.publish("alpha", v1)
		f.installLock(map[string]map[string]string{"alpha": v1})
		if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
			t.Fatal(err)
		}

		got, err := f.app.UncheckedSkills(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("UncheckedSkills() = %+v, want empty", got)
		}
	})

	t.Run("website well-known skill, manual folder, non-github clone, and check failure", func(t *testing.T) {
		f := newFixture(t)
		f.installLock(map[string]map[string]string{
			"alpha": v1,
		})
		// Add a well-known lock entry alongside alpha.
		f.home.WriteFile(".agents/skills/ui-taste/SKILL.md", "---\nname: ui-taste\ndescription: taste\n---\n")
		f.home.Symlink(".claude/skills/ui-taste", "../../.agents/skills/ui-taste")
		f.home.WriteFile(".agents/.skill-lock.json", fmt.Sprintf(`{
  "version": 3,
  "skills": {
    "alpha": {
      "source": %q,
      "sourceType": "github",
      "sourceUrl": "https://github.com/%s.git",
      "skillPath": "skills/alpha/SKILL.md",
      "skillFolderHash": "v1"
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
`, repo, repo))

		// Manual folder without origin.
		f.home.WriteFile(".claude/skills/manual-one/SKILL.md", "---\nname: manual-one\n---\n")

		// Non-GitHub git clone.
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

		// Recent check failure for alpha.
		failedAt := t0.Add(-3 * time.Hour)
		f.saveResults(map[string]domain.TargetResult{
			"lock:alpha": {State: domain.StateCheckFailed, CheckedAt: failedAt, Detail: "gh timeout"},
		})

		got, err := f.app.UncheckedSkills(ctx)
		if err != nil {
			t.Fatal(err)
		}

		wantByID := map[string]app.UncheckedSkill{
			"lock:alpha":                    {ID: "lock:alpha", Name: "alpha", Reason: app.UncheckedCheckFailed, CheckedAt: failedAt},
			"lock:ui-taste":                 {ID: "lock:ui-taste", Name: "ui-taste", Reason: app.UncheckedWebSource},
			"dir:.claude/skills/manual-one": {ID: "dir:.claude/skills/manual-one", Name: "manual-one", Reason: app.UncheckedNoOrigin},
			"dir:.claude/skills/gitlab-skill": {
				ID:     "dir:.claude/skills/gitlab-skill",
				Name:   "gitlab-skill",
				Reason: app.UncheckedNoOrigin,
			},
		}
		if len(got) != len(wantByID) {
			t.Fatalf("len(UncheckedSkills) = %d (%+v), want %d", len(got), got, len(wantByID))
		}
		for _, item := range got {
			want, ok := wantByID[item.ID]
			if !ok {
				t.Errorf("unexpected unchecked skill: %+v", item)
				continue
			}
			if item.Name != want.Name || item.Reason != want.Reason || !item.CheckedAt.Equal(want.CheckedAt) {
				t.Errorf("for %s: got %+v, want %+v", item.ID, item, want)
			}
		}
	})

	t.Run("disabled and missing skills are excluded", func(t *testing.T) {
		f := newFixture(t)
		f.home.WriteFile(".agents/skills/OFF/parked/SKILL.md", "---\nname: parked\n---\n")
		f.home.WriteFile(".agents/.skill-lock.json", `{
  "version": 3,
  "skills": {
    "parked": {
      "source": "uizze.sh",
      "sourceType": "well-known",
      "skillPath": "SKILL.md",
      "skillFolderHash": "abc"
    },
    "ghost": {
      "source": "uizze.sh",
      "sourceType": "well-known",
      "skillPath": "SKILL.md",
      "skillFolderHash": "def"
    }
  }
}
`)

		got, err := f.app.UncheckedSkills(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("UncheckedSkills() = %+v, want empty for disabled/missing skills", got)
		}
	})
}
