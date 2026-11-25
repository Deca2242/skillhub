package remove_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// Removing an exact independent non-Git global skill in a configured harness
// directory removes only that folder and safely owned links resolving to it,
// leaves unrelated homonyms, store state, parked state and the lock untouched,
// and undo restores everything byte for byte.
func TestRemoveExactHarnessSkillWithHomonymsAndUnrelatedState(t *testing.T) {
	e := machine(t)
	p := e.h.Paths()

	// Create an unrelated store skill with the same name ("humanizer"), its lock
	// entry, Managed record, and an open update candidate, plus another homonym
	// in Codex and a safely owned symlink pointing to Claude Code's humanizer.
	e.skill("humanizer")
	e.addEntry("humanizer", "owner/humanizer", "skills/humanizer/SKILL.md", e.treeSHA(".agents/skills/humanizer"))
	e.h.WriteFile(".codex/skills/humanizer/SKILL.md", graphtest.SkillMD("humanizer", "codex copy"))
	e.h.Symlink(".kiro/skills/humanizer-alias", "../../.claude/skills/humanizer")
	e.h.Symlink(".claude-omni/skills/humanizer-ref", "../../.claude/skills/humanizer")
	e.saveState(domain.PersistedState{
		Managed: map[string]domain.ManagedSkill{
			"humanizer": {Key: "humanizer", Targets: []string{"claude-code"}},
		},
		Candidates: []domain.UpdateCandidate{
			{ID: "cand-humanizer", Target: domain.LockID("humanizer"), Skills: []string{domain.LockID("humanizer")}, State: domain.CandidatePending},
		},
	})
	before := e.everything()

	plan := e.plan("dir:.claude/skills/humanizer")

	pv := plan.Preview
	wantFolder := e.h.Path(".claude/skills/humanizer")
	if pv.Key != "humanizer" || pv.Parked || pv.Off || pv.Folder != wantFolder || pv.TreeSHA == "" {
		t.Fatalf("preview = %+v, want non-parked non-off humanizer at %s", pv, wantFolder)
	}
	if pv.LockEntry != nil || pv.ParkedEntry != nil || len(pv.PendingCandidates) != 0 {
		t.Errorf("preview has store/lock/candidate side effects: lock=%+v parked=%+v cands=%v",
			pv.LockEntry, pv.ParkedEntry, pv.PendingCandidates)
	}
	wantLinks := []string{e.h.Path(".kiro/skills/humanizer-alias")}
	if got := linkPaths(pv.Links); !slices.Equal(got, wantLinks) {
		t.Errorf("Links = %v, want %v", got, wantLinks)
	}
	wantKept := []remove.KeptLink{{
		Path:    e.h.Path(".claude-omni/skills/humanizer-ref"),
		Target:  "../../.claude/skills/humanizer",
		Profile: "claude-omni",
	}}
	if !reflect.DeepEqual(pv.KeptLinks, wantKept) {
		t.Errorf("KeptLinks = %+v, want %+v", pv.KeptLinks, wantKept)
	}
	for _, wantHomonym := range []string{filepath.Join(p.StoreDir(), "humanizer"), e.h.Path(".codex/skills/humanizer")} {
		if !slices.ContainsFunc(pv.Homonyms, func(h remove.Homonym) bool { return h.Path == wantHomonym }) {
			t.Errorf("Homonyms = %+v, lacks %s", pv.Homonyms, wantHomonym)
		}
	}
	if !plan.Plan.Effect.IsZero() {
		t.Errorf("Effect = %+v, want zero for independent harness skill", plan.Plan.Effect)
	}

	out := e.apply(plan)

	if exists(t, wantFolder) || exists(t, e.h.Path(".kiro/skills/humanizer-alias")) {
		t.Fatal("selected folder or its owned link still exists after removal")
	}
	after := e.everything()
	if after.lock != before.lock {
		t.Error("removing an independent harness skill modified the skills lock")
	}
	if after.state != before.state {
		t.Error("removing an independent harness skill modified state.json")
	}
	for _, untouched := range []string{".agents/skills/humanizer", ".codex/skills/humanizer", ".claude-omni/skills/humanizer-ref"} {
		if !reflect.DeepEqual(subtree(before, untouched), subtree(after, untouched)) {
			t.Errorf("unrelated path %s changed", untouched)
		}
	}

	e.undo(out)
	e.sameAs(before, "remove exact harness skill and undo")
}

// Removing an independent harness skill whose basename matches an unrelated
// parked skill never touches the parked skill or its state record.
func TestRemoveExactHarnessSkillLeavesUnrelatedParkedEntryIntact(t *testing.T) {
	e := machine(t)
	e.disable("grill-me", ".claude/skills/grill-me")
	e.h.WriteFile(".codex/skills/grill-me/SKILL.md", graphtest.SkillMD("grill-me", "independent codex copy"))
	before := e.everything()

	plan := e.plan("dir:.codex/skills/grill-me")
	if !plan.Plan.Effect.IsZero() || plan.Preview.Parked || plan.Preview.ParkedEntry != nil {
		t.Fatalf("plan touched unrelated parked state: effect=%+v preview=%+v", plan.Plan.Effect, plan.Preview)
	}

	out := e.apply(plan)

	if exists(t, e.h.Path(".codex/skills/grill-me")) {
		t.Fatal("selected folder still exists")
	}
	after := e.everything()
	if after.state != before.state || after.lock != before.lock {
		t.Error("removing independent harness copy changed state.json or skills lock")
	}
	if !reflect.DeepEqual(subtree(before, ".agents/.skillhub/disabled/grill-me"), subtree(after, ".agents/.skillhub/disabled/grill-me")) {
		t.Error("unrelated parked folder changed")
	}

	e.undo(out)
	e.sameAs(before, "remove independent copy with parked homonym and undo")
}

// Selecting one exact copy among multiple identical non-store copies removes
// only that copy and leaves all sibling copies untouched.
func TestRemoveOneExactCopyAmongIdenticalCopies(t *testing.T) {
	e := machine(t)
	before := e.everything()

	plan := e.plan("dir:.cursor/skills/requirements-elicitation")
	out := e.apply(plan)

	if exists(t, e.h.Path(".cursor/skills/requirements-elicitation")) {
		t.Fatal("selected copy still exists")
	}
	after := e.everything()
	for _, dir := range graphtest.RequirementsDirs {
		if dir == ".cursor/skills" {
			continue
		}
		rel := dir + "/requirements-elicitation"
		if !reflect.DeepEqual(subtree(before, rel), subtree(after, rel)) {
			t.Errorf("sibling copy %s changed", rel)
		}
	}

	e.undo(out)
	e.sameAs(before, "remove one identical copy and undo")
}

// Hand-disabled OFF/ skills can be removed by exact dir: or lock: ID; their
// links resolving to OFF/, their folder and their lock entry go, OFF/README.md
// and sibling OFF/ skills stay untouched, and undo restores byte for byte.
func TestRemoveHandDisabledOFFSkillAndUndo(t *testing.T) {
	for _, target := range []string{"dir:.agents/skills/OFF/accessibility", "lock:accessibility"} {
		t.Run(target, func(t *testing.T) {
			e := machine(t)
			before := e.everything()

			plan := e.plan(target)
			pv := plan.Preview
			wantFolder := e.h.Path(".agents/skills/OFF/accessibility")
			if pv.Key != "accessibility" || !pv.Off || pv.Parked || pv.Folder != wantFolder {
				t.Fatalf("preview = %+v, want OFF/ accessibility at %s", pv, wantFolder)
			}
			if pv.LockEntry == nil || pv.LockEntry.Name != "accessibility" {
				t.Fatalf("LockEntry = %+v, want accessibility", pv.LockEntry)
			}
			wantLinks := []string{e.h.Path(".claude/skills/OFF/accessibility")}
			if got := linkPaths(pv.Links); !slices.Equal(got, wantLinks) {
				t.Errorf("Links = %v, want %v", got, wantLinks)
			}

			out := e.apply(plan)

			if exists(t, wantFolder) || exists(t, e.h.Path(".claude/skills/OFF/accessibility")) {
				t.Fatal("OFF/ folder or its link still exists")
			}
			after := e.everything()
			for _, untouched := range []string{".agents/skills/OFF/README.md", ".agents/skills/OFF/web-quality-audit", ".claude-omni/skills/accessibility"} {
				if !reflect.DeepEqual(subtree(before, untouched), subtree(after, untouched)) {
					t.Errorf("%s changed", untouched)
				}
			}

			e.undo(out)
			e.sameAs(before, "remove OFF skill and undo")
		})
	}
}

// A hand-disabled OFF/ skill without a lock entry removes only its folder and
// links to it, leaving any unrelated parked entry with the same basename intact.
func TestRemoveManualOFFSkillLeavesUnrelatedParkedEntryIntact(t *testing.T) {
	e := machine(t)
	e.disable("grill-me", ".claude/skills/grill-me")
	e.h.WriteFile(".agents/skills/OFF/grill-me/SKILL.md", graphtest.SkillMD("grill-me", "manual off"))
	before := e.everything()

	plan := e.plan("dir:.agents/skills/OFF/grill-me")
	if !plan.Preview.Off || plan.Preview.LockEntry != nil || !plan.Plan.Effect.IsZero() {
		t.Fatalf("preview/effect = %+v / %+v, want off with no lock or state effect", plan.Preview, plan.Plan.Effect)
	}

	out := e.apply(plan)
	after := e.everything()
	if exists(t, e.h.Path(".agents/skills/OFF/grill-me")) {
		t.Fatal("manual OFF/ skill still exists")
	}
	if after.state != before.state || after.lock != before.lock {
		t.Error("manual OFF/ removal changed state or lock")
	}

	e.undo(out)
	e.sameAs(before, "remove manual OFF skill and undo")
}

// When both an active store skill and a parked skill share the same key, bare
// key remains ambiguous, while exact dir: and parked: IDs each remove only the
// selected target without affecting the other.
func TestDisambiguateActiveAndParkedWithExactIDs(t *testing.T) {
	t.Run("remove parked keeps active", func(t *testing.T) {
		e := machine(t)
		e.disable("grill-me", ".claude/skills/grill-me")
		e.skill("grill-me")
		e.saveState(func() domain.PersistedState {
			st := e.loadState()
			if st.Managed == nil {
				st.Managed = map[string]domain.ManagedSkill{}
			}
			st.Managed["grill-me"] = domain.ManagedSkill{Key: "grill-me", Targets: []string{"claude-code"}}
			return st
		}())
		before := e.everything()

		if _, err := e.remover().Remove("grill-me"); !errors.Is(err, remove.ErrAmbiguous) {
			t.Fatalf("bare key = %v, want ErrAmbiguous", err)
		}

		plan := e.plan("parked:grill-me")
		if !plan.Preview.Parked || plan.Preview.Folder != e.h.Path(".agents/.skillhub/disabled/grill-me") {
			t.Fatalf("preview = %+v", plan.Preview)
		}
		out := e.apply(plan)
		if exists(t, e.h.Path(".agents/.skillhub/disabled/grill-me")) {
			t.Fatal("parked folder still exists")
		}
		if !exists(t, e.h.Path(".agents/skills/grill-me/SKILL.md")) {
			t.Fatal("active store folder was removed")
		}
		if _, ok := e.loadState().Managed["grill-me"]; !ok {
			t.Error("active Managed entry was dropped")
		}

		e.undo(out)
		e.sameAs(before, "remove parked exact and undo")
	})

	t.Run("remove active keeps parked", func(t *testing.T) {
		e := machine(t)
		e.disable("grill-me", ".claude/skills/grill-me")
		e.skill("grill-me")
		before := e.everything()

		plan := e.plan("dir:.agents/skills/grill-me")
		if plan.Preview.Parked || plan.Preview.Folder != e.h.Path(".agents/skills/grill-me") {
			t.Fatalf("preview = %+v", plan.Preview)
		}
		out := e.apply(plan)
		if exists(t, e.h.Path(".agents/skills/grill-me")) {
			t.Fatal("active store folder still exists")
		}
		if !exists(t, e.h.Path(".agents/.skillhub/disabled/grill-me/SKILL.md")) {
			t.Fatal("parked folder was removed")
		}
		if _, ok := e.loadState().Parked["grill-me"]; !ok {
			t.Error("parked state record was dropped")
		}

		e.undo(out)
		e.sameAs(before, "remove active exact and undo")
	})
}

// Arbitrary/project paths, provider/profile/foreign zones, symlinks, Git clones
// and members of Git clones (even with unrecognized Git origins) are refused
// without mutating anything.
func TestRefusesArbitraryProviderSymlinkAndGitBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(e *env)
		target string
		want   error
	}{
		{
			name: "project path outside configured global roots",
			setup: func(e *env) {
				e.h.WriteFile("projects/app/.agents/skills/local-only/SKILL.md", graphtest.SkillMD("local-only", "project"))
			},
			target: "dir:projects/app/.agents/skills/local-only",
			want:   remove.ErrNotFound,
		},
		{
			name:   "path traversal in dir id",
			target: "dir:../outside",
			want:   remove.ErrInvalidKey,
		},
		{
			name:   "absolute path in dir id",
			target: "dir:/etc/passwd",
			want:   remove.ErrInvalidKey,
		},
		{
			name:   "provider .system skill",
			target: "dir:.codex/skills/.system/skill-creator",
			want:   remove.ErrForeignZone,
		},
		{
			name:   "provider synced skill",
			target: "dir:.claude/skills/synced/2637_ab12/pdf",
			want:   remove.ErrForeignZone,
		},
		{
			name:   "gentle-ai skill",
			target: "dir:.claude/skills/go-testing",
			want:   remove.ErrForeignZone,
		},
		{
			name:   "diagnostic profile skill",
			target: "dir:.claude-omni/skills/orca-cli",
			want:   remove.ErrForeignZone,
		},
		{
			name:   "plugin clone member via dir",
			target: "dir:" + graphtest.KarpathySkill,
			want:   remove.ErrForeignZone,
		},
		{
			name:   "plugin clone member via git",
			target: "git:" + graphtest.KarpathySkill,
			want:   remove.ErrForeignZone,
		},
		{
			name:   "symlink in harness dir",
			target: "dir:.claude/skills/archify",
			want:   remove.ErrAlias,
		},
		{
			name:   "symlink alias in store",
			target: "dir:.agents/skills/computer-use",
			want:   remove.ErrAlias,
		},
		{
			name: "github clone root in harness dir",
			setup: func(e *env) {
				e.h.WriteFile(".claude/skills/gh-clone/SKILL.md", graphtest.SkillMD("gh-clone", "clone"))
				e.h.Mkdir(".claude/skills/gh-clone/.git/objects")
				real, err := filepath.EvalSymlinks(e.h.Path(".claude/skills/gh-clone"))
				must(t, err)
				e.git.AddClone(real, ports.CloneStatus{
					Remote: "https://github.com/owner/gh-clone.git", Branch: "main", Upstream: "origin/main",
					Head: "2222222222222222222222222222222222222222", Clean: true,
				})
			},
			target: "dir:.claude/skills/gh-clone",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name: "github clone member via git id",
			setup: func(e *env) {
				e.h.WriteFile(".claude/skills/gh-pack/skills/sub/SKILL.md", graphtest.SkillMD("sub", "member"))
				e.h.Mkdir(".claude/skills/gh-pack/.git/objects")
				real, err := filepath.EvalSymlinks(e.h.Path(".claude/skills/gh-pack"))
				must(t, err)
				e.git.AddClone(real, ports.CloneStatus{
					Remote: "https://github.com/owner/gh-pack.git", Branch: "main", Upstream: "origin/main",
					Head: "3333333333333333333333333333333333333333", Clean: true,
				})
			},
			target: "git:.claude/skills/gh-pack/skills/sub",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name: "unrecognized git origin clone root",
			setup: func(e *env) {
				e.h.WriteFile(".claude/skills/gitlab-skill/SKILL.md", graphtest.SkillMD("gitlab-skill", "gitlab"))
				e.h.Mkdir(".claude/skills/gitlab-skill/.git/objects")
				real, err := filepath.EvalSymlinks(e.h.Path(".claude/skills/gitlab-skill"))
				must(t, err)
				e.git.AddClone(real, ports.CloneStatus{
					Remote: "https://gitlab.com/acme/gitlab-skill.git", Branch: "main", Head: "4444444444444444444444444444444444444444", Clean: true,
				})
			},
			target: "dir:.claude/skills/gitlab-skill",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name: "member of unrecognized git origin clone",
			setup: func(e *env) {
				e.h.WriteFile(".claude/skills/local-repo/skills/member/SKILL.md", graphtest.SkillMD("member", "member"))
				e.h.Mkdir(".claude/skills/local-repo/.git/objects")
				real, err := filepath.EvalSymlinks(e.h.Path(".claude/skills/local-repo"))
				must(t, err)
				e.git.AddClone(real, ports.CloneStatus{Remote: "", Branch: "main", Head: "5555555555555555555555555555555555555555", Clean: true})
			},
			target: "dir:.claude/skills/local-repo/skills/member",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name: "linked git worktree (.git file)",
			setup: func(e *env) {
				e.h.WriteFile(".codex/skills/wt-skill/SKILL.md", graphtest.SkillMD("wt-skill", "wt"))
				e.h.WriteFile(".codex/skills/wt-skill/.git", "gitdir: /some/repo/.git/worktrees/wt-skill\n")
			},
			target: "dir:.codex/skills/wt-skill",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name: "nested .git inside skill folder",
			setup: func(e *env) {
				e.h.WriteFile(".codex/skills/nested-git/SKILL.md", graphtest.SkillMD("nested-git", "nested"))
				e.h.Mkdir(".codex/skills/nested-git/vendor/.git/objects")
			},
			target: "dir:.codex/skills/nested-git",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name:   "legacy bare key does not remove harness-only skill",
			target: "brainstorming",
			want:   remove.ErrNotFound,
		},
		{
			name: "symlinked harness parent pointing to diagnostic profile",
			setup: func(e *env) {
				must(t, os.RemoveAll(e.h.Path(".kiro/skills")))
				e.h.Symlink(".kiro/skills", "../.claude-omni/skills")
			},
			target: "dir:.kiro/skills/orca-cli",
			want:   remove.ErrForeignZone,
		},
		{
			name: "symlinked harness parent pointing outside global roots",
			setup: func(e *env) {
				must(t, os.RemoveAll(e.h.Path(".kiro/skills")))
				e.h.WriteFile("projects/outside/sneaky/SKILL.md", graphtest.SkillMD("sneaky", "outside"))
				e.h.Symlink(".kiro/skills", "../projects/outside")
			},
			target: "dir:.kiro/skills/sneaky",
			want:   remove.ErrForeignZone,
		},
		{
			name: "symlinked harness parent pointing to git repository",
			setup: func(e *env) {
				must(t, os.RemoveAll(e.h.Path(".kiro/skills")))
				e.h.WriteFile("projects/gitroot/skills/repo-skill/SKILL.md", graphtest.SkillMD("repo-skill", "git"))
				e.h.Mkdir("projects/gitroot/.git/objects")
				e.h.Symlink(".kiro/skills", "../projects/gitroot/skills")
			},
			target: "dir:.kiro/skills/repo-skill",
			want:   remove.ErrForeignZone,
		},
		{
			name: "tree exceeding max git inspection depth fails closed",
			setup: func(e *env) {
				e.h.WriteFile(".claude/skills/deep-skill/SKILL.md", graphtest.SkillMD("deep-skill", "deep"))
				rel := ".claude/skills/deep-skill"
				for range 17 {
					rel = filepath.Join(rel, "sub")
				}
				e.h.WriteFile(filepath.Join(rel, "leaf.txt"), "deep")
			},
			target: "dir:.claude/skills/deep-skill",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name: "parked folder containing .git fails closed",
			setup: func(e *env) {
				e.disable("grill-me", ".claude/skills/grill-me")
				e.h.Mkdir(".agents/.skillhub/disabled/grill-me/.git/objects")
			},
			target: "parked:grill-me",
			want:   engine.ErrUnsupportedTree,
		},
		{
			name: "parked folder exceeding max git inspection depth fails closed",
			setup: func(e *env) {
				e.disable("grill-me", ".claude/skills/grill-me")
				rel := ".agents/.skillhub/disabled/grill-me"
				for range 17 {
					rel = filepath.Join(rel, "sub")
				}
				e.h.WriteFile(filepath.Join(rel, "leaf.txt"), "deep")
			},
			target: "parked:grill-me",
			want:   engine.ErrUnsupportedTree,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := machine(t)
			if tc.setup != nil {
				tc.setup(e)
			}
			before := e.everything()

			_, err := e.remover().Remove(tc.target)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Remove(%q) error = %v, want %v", tc.target, err, tc.want)
			}
			e.sameAs(before, "refused removal "+tc.name)
		})
	}
}

// Symlinks located inside Git repositories are never unlinked when removing
// the target skill; they are reported in KeptLinks and preserved on disk.
func TestPreservesLinksInsideGitRepositories(t *testing.T) {
	e := machine(t)
	e.h.Mkdir(".kiro/skills/.git/objects")
	e.h.Symlink(".kiro/skills/humanizer-alias", "../../.claude/skills/humanizer")
	before := e.everything()

	plan := e.plan("dir:.claude/skills/humanizer")
	if len(plan.Preview.Links) != 0 {
		t.Fatalf("Links = %+v, want empty so git-owned link is not removed", plan.Preview.Links)
	}
	wantKept := []remove.KeptLink{{
		Path:   e.h.Path(".kiro/skills/humanizer-alias"),
		Target: "../../.claude/skills/humanizer",
		Zone:   "git",
	}}
	if !reflect.DeepEqual(plan.Preview.KeptLinks, wantKept) {
		t.Fatalf("KeptLinks = %+v, want %+v", plan.Preview.KeptLinks, wantKept)
	}

	out := e.apply(plan)
	if exists(t, e.h.Path(".claude/skills/humanizer")) {
		t.Fatal("selected folder still exists")
	}
	after := e.everything()
	if !reflect.DeepEqual(subtree(before, ".kiro/skills/humanizer-alias"), subtree(after, ".kiro/skills/humanizer-alias")) {
		t.Fatal("symlink inside git repository was modified or removed")
	}

	e.undo(out)
	e.sameAs(before, "preserve git link and undo")
}
