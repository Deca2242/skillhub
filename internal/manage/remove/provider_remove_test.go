package remove_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/adapters/fs"
	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/ports"
)

// Default Remove rejects provider zones (ZoneGentle, ZonePlugin, ZoneProvider),
// while RemoveWithOptions with AllowProvider: true plans reversible removal
// backed by snapshot and byte-for-byte undo.
func TestProviderRemoveAllowedWithOptInAndUndo(t *testing.T) {
	t.Run("ZoneGentle under claude skills", func(t *testing.T) {
		e := newEnv(t)
		e.h.Mkdir(".agents/skills")
		e.h.WriteFile(".gentle-ai/state.json", `{"components":["skills"]}`)
		e.h.WriteFile(".claude/skills/gentle-skill/SKILL.md", "---\nname: gentle-skill\ndescription: gentle\nmetadata:\n  author: gentleman-programming\n---\n")
		before := e.everything()

		target := "dir:.claude/skills/gentle-skill"
		if _, err := e.remover().Remove(target); !errors.Is(err, remove.ErrForeignZone) {
			t.Fatalf("default Remove(%q) = %v, want ErrForeignZone", target, err)
		}

		plan, err := e.remover().RemoveWithOptions(target, remove.Options{AllowProvider: true})
		if err != nil {
			t.Fatalf("RemoveWithOptions(%q): %v", target, err)
		}
		if plan.Preview.ProviderZone != graph.ZoneGentle {
			t.Errorf("ProviderZone = %q, want %q", plan.Preview.ProviderZone, graph.ZoneGentle)
		}
		if plan.Preview.Folder != e.h.Path(".claude/skills/gentle-skill") {
			t.Errorf("Folder = %q, want %q", plan.Preview.Folder, e.h.Path(".claude/skills/gentle-skill"))
		}

		out := e.apply(plan)
		if exists(t, e.h.Path(".claude/skills/gentle-skill")) {
			t.Fatal("gentle-skill still exists after apply")
		}
		e.undo(out)
		e.sameAs(before, "gentle-skill remove and undo")
	})

	t.Run("ZonePlugin non-git folder", func(t *testing.T) {
		e := newEnv(t)
		e.h.Mkdir(".agents/skills")
		e.h.WriteFile(".claude/skills/local-plugin/SKILL.md", graphtest.SkillMD("local-plugin", "plugin skill"))
		e.h.WriteFile(".claude/skills/local-plugin/.claude-plugin/plugin.json", `{"name":"local-plugin"}`)
		before := e.everything()

		target := "dir:.claude/skills/local-plugin"
		if _, err := e.remover().Remove(target); !errors.Is(err, remove.ErrForeignZone) {
			t.Fatalf("default Remove(%q) = %v, want ErrForeignZone", target, err)
		}

		plan, err := e.remover().RemoveWithOptions(target, remove.Options{AllowProvider: true})
		if err != nil {
			t.Fatalf("RemoveWithOptions(%q): %v", target, err)
		}
		if plan.Preview.ProviderZone != graph.ZonePlugin {
			t.Errorf("ProviderZone = %q, want %q", plan.Preview.ProviderZone, graph.ZonePlugin)
		}

		out := e.apply(plan)
		if exists(t, e.h.Path(".claude/skills/local-plugin")) {
			t.Fatal("local-plugin still exists after apply")
		}
		e.undo(out)
		e.sameAs(before, "local-plugin remove and undo")
	})

	t.Run("ZoneProvider under excluded codex system and claude plugins", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			rel    string
			target string
		}{
			{
				name:   "codex system",
				rel:    ".codex/skills/.system/skill-creator",
				target: "dir:.codex/skills/.system/skill-creator",
			},
			{
				name:   "claude plugins",
				rel:    ".claude/plugins/my-plugin",
				target: "dir:.claude/plugins/my-plugin",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newEnv(t)
				e.h.Mkdir(".agents/skills")
				e.h.WriteFile(tc.rel+"/SKILL.md", graphtest.SkillMD(filepath.Base(tc.rel), "provider skill"))
				before := e.everything()

				if _, err := e.remover().Remove(tc.target); !errors.Is(err, remove.ErrForeignZone) {
					t.Fatalf("default Remove(%q) = %v, want ErrForeignZone", tc.target, err)
				}

				plan, err := e.remover().RemoveWithOptions(tc.target, remove.Options{AllowProvider: true})
				if err != nil {
					t.Fatalf("RemoveWithOptions(%q): %v", tc.target, err)
				}
				if plan.Preview.ProviderZone != graph.ZoneProvider {
					t.Errorf("ProviderZone = %q, want %q", plan.Preview.ProviderZone, graph.ZoneProvider)
				}

				out := e.apply(plan)
				if exists(t, e.h.Path(tc.rel)) {
					t.Fatalf("%s still exists after apply", tc.rel)
				}
				e.undo(out)
				e.sameAs(before, tc.name+" remove and undo")
			})
		}
	})
}

// Even with AllowProvider: true, ZoneShared, ZoneProfile, symlinks, Git clones,
// provider roots themselves, and ambiguous folders remain strictly blocked.
func TestProviderRemoveStrictRejections(t *testing.T) {
	opts := remove.Options{AllowProvider: true}

	t.Run("ZoneShared _shared is blocked", func(t *testing.T) {
		e := machine(t)
		if _, err := e.remover().RemoveWithOptions("dir:.agents/skills/_shared", opts); !errors.Is(err, remove.ErrForeignZone) {
			t.Fatalf("RemoveWithOptions(_shared) = %v, want ErrForeignZone", err)
		}
	})

	t.Run("ZoneProfile is blocked", func(t *testing.T) {
		e := machine(t)
		if _, err := e.remover().RemoveWithOptions("dir:.claude-omni/skills/orca-cli", opts); !errors.Is(err, remove.ErrForeignZone) {
			t.Fatalf("RemoveWithOptions(profile) = %v, want ErrForeignZone", err)
		}
	})

	t.Run("symlink inside provider root is blocked", func(t *testing.T) {
		e := newEnv(t)
		e.h.WriteFile(".claude/skills/real-one/SKILL.md", graphtest.SkillMD("real-one", "real"))
		e.h.Symlink(".claude/plugins/link-plugin", "../../.claude/skills/real-one")
		if _, err := e.remover().RemoveWithOptions("dir:.claude/plugins/link-plugin", opts); !errors.Is(err, remove.ErrAlias) {
			t.Fatalf("RemoveWithOptions(symlink) = %v, want ErrAlias", err)
		}
	})

	t.Run("git clone inside provider or plugin zone is blocked", func(t *testing.T) {
		e := machine(t)
		if _, err := e.remover().RemoveWithOptions("dir:"+graphtest.KarpathySkill, opts); !errors.Is(err, engine.ErrUnsupportedTree) {
			t.Fatalf("RemoveWithOptions(git plugin) = %v, want ErrUnsupportedTree", err)
		}
		e.h.WriteFile(".claude/plugins/git-plug/SKILL.md", graphtest.SkillMD("git-plug", "git"))
		e.h.Mkdir(".claude/plugins/git-plug/.git/objects")
		real, err := filepath.EvalSymlinks(e.h.Path(".claude/plugins/git-plug"))
		must(t, err)
		e.git.AddClone(real, ports.CloneStatus{
			Remote: "https://github.com/org/git-plug.git", Branch: "main",
			Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Clean: true,
		})
		if _, err := e.remover().RemoveWithOptions("dir:.claude/plugins/git-plug", opts); !errors.Is(err, engine.ErrUnsupportedTree) {
			t.Fatalf("RemoveWithOptions(git in provider) = %v, want ErrUnsupportedTree", err)
		}
	})

	t.Run("provider root itself is blocked", func(t *testing.T) {
		e := machine(t)
		for _, rootTarget := range []string{
			"dir:.claude/plugins",
			"dir:.codex/skills/.system",
			"dir:.claude/skills/synced",
		} {
			if _, err := e.remover().RemoveWithOptions(rootTarget, opts); err == nil {
				t.Fatalf("RemoveWithOptions(%q) succeeded, want error", rootTarget)
			}
		}
	})

	t.Run("ambiguous folder claimed by several skills is blocked", func(t *testing.T) {
		e := newEnv(t)
		e.h.WriteFile(".claude/skills/shared-ambig/SKILL.md", graphtest.SkillMD("shared-ambig", "ambig"))
		e.h.WriteFile(".claude/skills/shared-ambig/.claude-plugin/plugin.json", `{"name":"shared-ambig"}`)
		real, err := filepath.EvalSymlinks(e.h.Path(".claude/skills/shared-ambig"))
		must(t, err)
		harnesses, err := data.LoadHarnesses(e.h.Paths(), e.files)
		must(t, err)
		r := remove.New(remove.Deps{
			Paths:     e.h.Paths(),
			Planner:   e.eng,
			Lock:      e.lock,
			State:     e.state,
			Harnesses: harnesses,
			Observe: func() (graph.Graph, error) {
				return graph.Graph{
					Store: e.h.Paths().StoreDir(),
					Folders: []graph.Folder{
						{Path: real, Entry: e.h.Path(".claude/skills/shared-ambig"), Entries: []string{e.h.Path(".claude/skills/shared-ambig")}, Name: "shared-ambig"},
					},
					Skills: []graph.Skill{
						{ID: "s1", Canonical: real, Folders: []string{real}, Zone: graph.ZonePlugin, SharedWith: []string{"s2"}},
						{ID: "s2", Canonical: real, Folders: []string{real}, Zone: graph.ZonePlugin, SharedWith: []string{"s1"}},
					},
				}, nil
			},
			Dirs:  fs.NewScanner(e.h.Paths().Guard()),
			Links: osLinks{},
			Files: e.files,
		})
		if _, err := r.RemoveWithOptions("dir:.claude/skills/shared-ambig", opts); !errors.Is(err, remove.ErrAmbiguous) {
			t.Fatalf("RemoveWithOptions(ambiguous) = %v, want ErrAmbiguous", err)
		}
	})
}

// RemoveGroupWithOptions(..., Options{AllowProvider: true}) includes eligible
// provider copies while keeping protected copies (e.g. diagnostic profile or git)
// in KeptCopies.
func TestRemoveGroupWithOptionsProviderAndKeptCopies(t *testing.T) {
	e := newEnv(t)
	e.h.WriteFile(".claude/skills/grp-plug/SKILL.md", graphtest.SkillMD("grp-plug", "plugin"))
	e.h.WriteFile(".codex/skills/grp-plug/SKILL.md", graphtest.SkillMD("grp-plug", "git copy"))
	e.h.Mkdir(".codex/skills/grp-plug/.git/objects")
	realPlug, err := filepath.EvalSymlinks(e.h.Path(".claude/skills/grp-plug"))
	must(t, err)
	realCodex, err := filepath.EvalSymlinks(e.h.Path(".codex/skills/grp-plug"))
	must(t, err)
	harnesses, err := data.LoadHarnesses(e.h.Paths(), e.files)
	must(t, err)

	r := remove.New(remove.Deps{
		Paths:     e.h.Paths(),
		Planner:   e.eng,
		Lock:      e.lock,
		State:     e.state,
		Harnesses: harnesses,
		Observe: func() (graph.Graph, error) {
			return graph.Graph{
				Store: e.h.Paths().StoreDir(),
				Folders: []graph.Folder{
					{Path: realPlug, Entry: e.h.Path(".claude/skills/grp-plug"), Entries: []string{e.h.Path(".claude/skills/grp-plug")}, Name: "grp-plug", TreeSHA: "sha1", Git: graph.GitNone},
					{Path: realCodex, Entry: e.h.Path(".codex/skills/grp-plug"), Entries: []string{e.h.Path(".codex/skills/grp-plug")}, Name: "grp-plug", TreeSHA: "sha1", Git: graph.GitPresent},
				},
				Skills: []graph.Skill{
					{
						ID:        "dir:.claude/skills/grp-plug",
						Name:      "grp-plug",
						Canonical: realPlug,
						Folders:   []string{realPlug, realCodex},
						Zone:      graph.ZonePlugin,
					},
				},
			}, nil
		},
		Dirs:  fs.NewScanner(e.h.Paths().Guard()),
		Links: osLinks{},
		Files: e.files,
	})

	if _, err := r.RemoveGroup("dir:.claude/skills/grp-plug"); !errors.Is(err, remove.ErrForeignZone) {
		t.Fatalf("default RemoveGroup = %v, want ErrForeignZone", err)
	}

	plan, err := r.RemoveGroupWithOptions("dir:.claude/skills/grp-plug", remove.Options{AllowProvider: true})
	if err != nil {
		t.Fatalf("RemoveGroupWithOptions: %v", err)
	}
	if plan.Preview.ProviderZone != graph.ZonePlugin {
		t.Errorf("ProviderZone = %q, want %q", plan.Preview.ProviderZone, graph.ZonePlugin)
	}
	if len(plan.Preview.Folders) != 1 || plan.Preview.Folders[0].Path != e.h.Path(".claude/skills/grp-plug") {
		t.Errorf("Folders = %+v, want only .claude/skills/grp-plug", plan.Preview.Folders)
	}
	if len(plan.Preview.KeptCopies) != 1 || !slices.Contains([]string{"git"}, plan.Preview.KeptCopies[0].Reason) {
		t.Errorf("KeptCopies = %+v, want 1 kept copy with reason git", plan.Preview.KeptCopies)
	}
}

func TestProviderRemoveGitCloneAllowedWithOptInAndUndo(t *testing.T) {
	t.Run("karpathy-guidelines plugin clone with sibling skill, harness link, and profile link", func(t *testing.T) {
		e := machine(t)
		// Add a second skill inside the same plugin clone, a harness symlink, and a diagnostic profile symlink.
		e.h.WriteFile(graphtest.KarpathyClone+"/skills/second-skill/SKILL.md", graphtest.SkillMD("second-skill", "second"))
		e.h.Symlink(".codex/skills/karpathy-link", "../../.claude/skills/andrej-karpathy-skills/skills/karpathy-guidelines")
		e.h.Symlink(".claude-omni/skills/karpathy-prof", "../../.claude/skills/andrej-karpathy-skills/skills/karpathy-guidelines")
		before := e.everything()

		target := "git:" + graphtest.KarpathySkill
		if _, err := e.remover().Remove(target); !errors.Is(err, remove.ErrForeignZone) {
			t.Fatalf("default Remove(%q) = %v, want ErrForeignZone", target, err)
		}

		plan, err := e.remover().RemoveWithOptions(target, remove.Options{AllowProvider: true})
		if err != nil {
			t.Fatalf("RemoveWithOptions(%q): %v", target, err)
		}
		if !plan.Preview.Clone {
			t.Error("Preview.Clone = false, want true")
		}
		if plan.Preview.Folder != e.h.Path(graphtest.KarpathyClone) {
			t.Errorf("Preview.Folder = %q, want %q", plan.Preview.Folder, e.h.Path(graphtest.KarpathyClone))
		}
		if plan.Preview.TreeSHA == "" {
			t.Error("Preview.TreeSHA is empty, want CloneSHA")
		}
		if plan.Preview.ProviderZone != graph.ZonePlugin {
			t.Errorf("Preview.ProviderZone = %q, want %q", plan.Preview.ProviderZone, graph.ZonePlugin)
		}
		if plan.Preview.ProviderEntry != e.h.Path(graphtest.KarpathySkill) {
			t.Errorf("Preview.ProviderEntry = %q, want %q", plan.Preview.ProviderEntry, e.h.Path(graphtest.KarpathySkill))
		}
		wantSkills := []string{"karpathy-guidelines", "second-skill"}
		if !slices.Equal(plan.Preview.CloneSkills, wantSkills) {
			t.Errorf("Preview.CloneSkills = %v, want %v", plan.Preview.CloneSkills, wantSkills)
		}
		if len(plan.Preview.Links) != 1 || plan.Preview.Links[0].Path != e.h.Path(".codex/skills/karpathy-link") {
			t.Errorf("Preview.Links = %+v, want codex harness link", plan.Preview.Links)
		}
		if len(plan.Preview.KeptLinks) != 1 || plan.Preview.KeptLinks[0].Path != e.h.Path(".claude-omni/skills/karpathy-prof") {
			t.Errorf("Preview.KeptLinks = %+v, want profile link kept", plan.Preview.KeptLinks)
		}
		hasRemoveClone := slices.ContainsFunc(plan.Plan.Steps, func(s engine.Step) bool {
			return s.Kind == engine.StepRemoveClone && s.Path == e.h.Path(graphtest.KarpathyClone)
		})
		if !hasRemoveClone {
			t.Errorf("plan steps = %+v, want StepRemoveClone for %s", plan.Plan.Steps, e.h.Path(graphtest.KarpathyClone))
		}

		out := e.apply(plan)
		if exists(t, e.h.Path(graphtest.KarpathyClone)) {
			t.Fatal("KarpathyClone still exists after apply")
		}
		if exists(t, e.h.Path(".codex/skills/karpathy-link")) {
			t.Fatal("harness link still exists after apply")
		}
		if !exists(t, e.h.Path(".claude-omni/skills/karpathy-prof")) {
			t.Fatal("diagnostic profile link was removed")
		}
		e.undo(out)
		e.sameAs(before, "karpathy clone remove and undo")
	})

	t.Run("non-provider git clone and linked worktree stay rejected with AllowProvider", func(t *testing.T) {
		e := newEnv(t)
		e.h.WriteFile(".claude/skills/plain-git/SKILL.md", graphtest.SkillMD("plain-git", "plain"))
		e.h.Mkdir(".claude/skills/plain-git/.git/objects")
		realPlain, err := filepath.EvalSymlinks(e.h.Path(".claude/skills/plain-git"))
		must(t, err)
		e.git.AddClone(realPlain, ports.CloneStatus{
			Remote: "https://github.com/org/plain-git.git", Branch: "main",
			Head: "1111111111111111111111111111111111111111", Clean: true,
		})

		opts := remove.Options{AllowProvider: true}
		if _, err := e.remover().RemoveWithOptions("git:.claude/skills/plain-git", opts); !errors.Is(err, engine.ErrUnsupportedTree) {
			t.Fatalf("non-provider git clone = %v, want ErrUnsupportedTree", err)
		}

		e.h.WriteFile(".claude/plugins/wt-plugin/SKILL.md", graphtest.SkillMD("wt-plugin", "worktree"))
		e.h.WriteFile(".claude/plugins/wt-plugin/.git", "gitdir: /tmp/somewhere\n")
		if _, err := e.remover().RemoveWithOptions("git:.claude/plugins/wt-plugin", opts); err == nil {
			t.Fatal("linked worktree plugin succeeded, want error")
		}
	})
}
