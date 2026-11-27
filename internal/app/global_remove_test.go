package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// App plans, applies and undoes exact removals of independent non-Git global
// harness skills, hand-disabled OFF/ skills, and parked skills while keeping
// unrelated same-name store/parked state and activity clean.
func TestGlobalRemoveLifecycleThroughApp(t *testing.T) {
	t.Run("harness skill with unrelated store homonym", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".agents/skills/humanizer/SKILL.md", graphtest.SkillMD("humanizer", "store copy"))
		st := m.persisted()
		if st.Managed == nil {
			st.Managed = map[string]domain.ManagedSkill{}
		}
		st.Managed["humanizer"] = domain.ManagedSkill{Key: "humanizer", Targets: []string{"claude-code"}}
		must(t, m.state.Save(st))

		p, err := m.app.PlanRemove(bg, "dir:.claude/skills/humanizer")
		must(t, err)
		if p.Remove == nil || p.Remove.Key != "humanizer" || p.Remove.Folder != m.h.Path(".claude/skills/humanizer") {
			t.Fatalf("PlanRemove = %+v", p.Remove)
		}
		if !slices.Contains(p.Reload, "claude-code") || !slices.Contains(p.Reload, "opencode") {
			t.Errorf("Reload = %v, want claude-code and opencode", p.Reload)
		}

		res := m.apply(p)
		if m.exists(".claude/skills/humanizer") {
			t.Fatal("selected harness skill still exists")
		}
		if !m.exists(".agents/skills/humanizer/SKILL.md") {
			t.Fatal("unrelated store homonym was removed")
		}
		if _, ok := m.persisted().Managed["humanizer"]; !ok {
			t.Error("unrelated Managed entry for store homonym was dropped")
		}

		// Operation describes the removed folder without attributing it to the
		// unrelated store skill's SkillActivity.
		op, err := m.app.Operation(bg, res.SnapshotID)
		must(t, err)
		if op.Action != "remove" || len(op.Keys) != 0 || op.Label == "" {
			t.Errorf("Operation = %+v, want Action=remove, empty Keys, non-empty Label", op)
		}
		hist, err := m.app.History()
		must(t, err)
		storeSkill := domain.Skill{
			ID:        domain.DirID(".agents/skills/humanizer"),
			Name:      "humanizer",
			Locations: []domain.Location{{AgentID: domain.StoreAgentID, Path: m.h.Path(".agents/skills/humanizer")}},
		}
		if act := app.SkillActivity(hist, map[string]app.OperationView{res.SnapshotID: op}, storeSkill); len(act) != 0 {
			t.Errorf("SkillActivity attributed harness removal to unrelated store skill: %+v", act)
		}

		u, err := m.app.PlanUndo(bg, res.SnapshotID)
		must(t, err)
		line := m.lastV2()
		if _, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: res.SnapshotID, PlanID: u.PlanID}); err != nil {
			t.Fatalf("UndoOperation: %v", err)
		}
		if !m.exists(".claude/skills/humanizer/SKILL.md") {
			t.Fatal("undo did not restore harness skill")
		}
	})

	t.Run("OFF skill", func(t *testing.T) {
		m := newMachine(t)
		p, err := m.app.PlanRemove(bg, "dir:.agents/skills/OFF/accessibility")
		must(t, err)
		if p.Remove == nil || !p.Remove.Off || p.Remove.Folder != m.h.Path(".agents/skills/OFF/accessibility") {
			t.Fatalf("PlanRemove OFF = %+v", p.Remove)
		}
		res := m.apply(p)
		if m.exists(".agents/skills/OFF/accessibility") {
			t.Fatal("OFF/ skill still exists")
		}
		if !m.exists(".agents/skills/OFF/README.md") {
			t.Fatal("OFF/README.md was touched")
		}
		u, err := m.app.PlanUndo(bg, res.SnapshotID)
		must(t, err)
		line := m.lastV2()
		if _, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: res.SnapshotID, PlanID: u.PlanID}); err != nil {
			t.Fatalf("UndoOperation: %v", err)
		}
		if !m.exists(".agents/skills/OFF/accessibility/SKILL.md") {
			t.Fatal("undo did not restore OFF/ skill")
		}
	})

	t.Run("git clone with unrecognized origin is refused", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".claude/skills/custom-git/SKILL.md", graphtest.SkillMD("custom-git", "non-github"))
		m.h.Mkdir(".claude/skills/custom-git/.git/objects")
		real, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/custom-git"))
		must(t, err)
		m.git.AddClone(real, ports.CloneStatus{Remote: "https://codeberg.org/org/custom-git.git", Branch: "main", Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Clean: true})

		_, err = m.app.PlanRemove(bg, "dir:.claude/skills/custom-git")
		if !errors.Is(err, app.ErrUnsupportedTree) {
			t.Fatalf("PlanRemove(custom-git) = %v, want ErrUnsupportedTree", err)
		}
	})
}

// SkillActions offers Remove with an exact RemoveKey for store skills, OFF/
// skills, and single-folder independent non-Git global harness skills, while
// refusing multi-copy skills, plugin clones, and non-GitHub clones.
func TestSkillActionsGlobalRemoveEligibility(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".claude/skills/custom-git/SKILL.md", graphtest.SkillMD("custom-git", "non-github"))
	m.h.Mkdir(".claude/skills/custom-git/.git/objects")
	real, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/custom-git"))
	must(t, err)
	m.git.AddClone(real, ports.CloneStatus{Remote: "https://gitlab.com/org/custom-git.git", Branch: "main", Head: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Clean: true})

	acts, err := m.app.SkillActions(bg)
	must(t, err)

	wantOffer := map[string]string{
		"lock:agent-browser":              "agent-browser",
		"lock:git-guardrails-claude-code": "dir:.agents/skills/OFF/git-guardrails-claude-code",
		"dir:.claude/skills/humanizer":    "dir:.claude/skills/humanizer",
		"dir:.codex/skills/brainstorming": "dir:.codex/skills/brainstorming",
	}
	for id, wantKey := range wantOffer {
		a := acts[id]
		if !a.Remove || a.RemoveKey != wantKey {
			t.Errorf("%s: Remove=%v RemoveKey=%q, want true, %q", id, a.Remove, a.RemoveKey, wantKey)
		}
		if _, err := m.app.PlanRemove(bg, a.RemoveKey); err != nil {
			t.Errorf("%s: PlanRemove(%q) refused: %v", id, a.RemoveKey, err)
		}
	}

	wantNoOffer := []string{
		"dir:.claude/skills/custom-git",
		"dir:.claude-omni/skills/orca-cli",
	}
	for _, id := range wantNoOffer {
		if a := acts[id]; a.Remove {
			t.Errorf("%s: Remove=true (RemoveKey=%q), want false", id, a.RemoveKey)
		}
	}
	// Multi-copy requirements-elicitation (7 identical copies outside the store)
	// must not offer ambiguous single-click removal, yet is unambiguously manual.
	for id, a := range acts {
		if filepath.Base(id) == "requirements-elicitation" {
			if a.Remove {
				t.Errorf("%s: multi-copy skill offered Remove=true (%q)", id, a.RemoveKey)
			}
			if !a.Manual {
				t.Errorf("%s: multi-copy manual skill has Manual=false, want true", id)
			}
		}
	}

	wantManual := []string{
		"dir:.claude/skills/humanizer",
		"dir:.codex/skills/brainstorming",
		"dir:.agents/skills/i-have-adhd",
	}
	for _, id := range wantManual {
		if a := acts[id]; !a.Manual {
			t.Errorf("%s: Manual=false, want true", id)
		}
	}

	wantNonManual := []string{
		"lock:agent-browser",
		"git:" + graphtest.KarpathySkill,
		"dir:.claude/skills/custom-git",
		"dir:.claude-omni/skills/orca-cli",
	}
	for _, id := range wantNonManual {
		if a := acts[id]; a.Manual {
			t.Errorf("%s: Manual=true, want false", id)
		}
	}
}

// SkillActions marks Manual=false when a copy is inside a walked .git parent
// directory or when grouped identical copies include a Git-backed folder,
// while preserving Manual=true when all grouped copies are confirmed non-Git.
func TestSkillActionsManualOwnershipEvidence(t *testing.T) {
	t.Run("nested skill under .git parent is non-manual", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".claude/skills/repo-pack/nested-skill/SKILL.md", graphtest.SkillMD("nested-skill", "inside git"))
		m.h.Mkdir(".claude/skills/repo-pack/.git/objects")
		real, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/repo-pack"))
		must(t, err)
		m.git.AddClone(real, ports.CloneStatus{Remote: "https://gitlab.com/org/repo-pack.git", Branch: "main", Head: "cccccccccccccccccccccccccccccccccccccccc", Clean: true})

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		if a := acts["dir:.claude/skills/repo-pack/nested-skill"]; a.Manual {
			t.Errorf("nested skill under .git parent has Manual=true, want false")
		}
	})

	t.Run("grouped copies with one Git-backed folder stay non-manual", func(t *testing.T) {
		m := newMachine(t)
		shared := graphtest.SkillMD("mixed-copy", "one manual one git")
		m.h.WriteFile(".claude/skills/mixed-copy/SKILL.md", shared)
		m.h.WriteFile(".codex/skills/mixed-copy/SKILL.md", shared)
		m.h.Mkdir(".codex/skills/mixed-copy/.git/objects")
		real, err := filepath.EvalSymlinks(m.h.Path(".codex/skills/mixed-copy"))
		must(t, err)
		m.git.AddClone(real, ports.CloneStatus{Remote: "https://gitlab.com/org/mixed-copy.git", Branch: "main", Head: "dddddddddddddddddddddddddddddddddddddddd", Clean: true})

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		for id, a := range acts {
			if filepath.Base(id) == "mixed-copy" && (a.Manual || a.Remove) {
				t.Errorf("%s: mixed-copy has Manual=%v Remove=%v, want false, false", id, a.Manual, a.Remove)
			}
		}
	})

	t.Run("grouped copies all non-Git are Manual without Remove", func(t *testing.T) {
		m := newMachine(t)
		shared := graphtest.SkillMD("pure-manual-copy", "two manual copies")
		m.h.WriteFile(".claude/skills/pure-manual-copy/SKILL.md", shared)
		m.h.WriteFile(".codex/skills/pure-manual-copy/SKILL.md", shared)

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		var found bool
		for id, a := range acts {
			if filepath.Base(id) == "pure-manual-copy" {
				found = true
				if !a.Manual || a.Remove {
					t.Errorf("%s: Manual=%v Remove=%v, want true, false", id, a.Manual, a.Remove)
				}
			}
		}
		if !found {
			t.Fatal("pure-manual-copy not found in SkillActions")
		}
	})

	t.Run(".git marker file at skill folder is non-manual", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".claude/skills/wt-skill/SKILL.md", graphtest.SkillMD("wt-skill", "worktree skill"))
		m.h.WriteFile(".claude/skills/wt-skill/.git", "gitdir: /some/repo/.git/worktrees/wt-skill\n")

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		a, ok := acts["dir:.claude/skills/wt-skill"]
		if !ok {
			t.Fatal("wt-skill not found in SkillActions")
		}
		if a.Manual {
			t.Errorf("wt-skill with .git marker file has Manual=true, want false")
		}
	})

	t.Run(".git at configured root is non-manual", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".kiro/skills/kiro-skill/SKILL.md", graphtest.SkillMD("kiro-skill", "under git root"))
		m.h.Mkdir(".kiro/skills/.git/objects")

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		a, ok := acts["dir:.kiro/skills/kiro-skill"]
		if !ok {
			t.Fatal("kiro-skill not found in SkillActions")
		}
		if a.Manual {
			t.Errorf("kiro-skill under .git root has Manual=true, want false")
		}
	})

	t.Run(".git at resolved ancestor of symlinked root is non-manual", func(t *testing.T) {
		m := newMachine(t)
		must(t, os.RemoveAll(m.h.Path(".kiro/skills")))
		m.h.WriteFile("dotfiles/kiro/skills/linked-root-skill/SKILL.md", graphtest.SkillMD("linked-root-skill", "in dotfiles git"))
		m.h.Mkdir("dotfiles/.git/objects")
		m.h.Symlink(".kiro/skills", m.h.Path("dotfiles/kiro/skills"))

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		a, ok := acts["dir:.kiro/skills/linked-root-skill"]
		if !ok {
			t.Fatal("linked-root-skill not found in SkillActions")
		}
		if a.Manual {
			t.Errorf("linked-root-skill under symlinked root with ancestor .git has Manual=true, want false")
		}
	})

	t.Run("nested .git inside skill folder is non-manual", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".claude/skills/nested-git/SKILL.md", graphtest.SkillMD("nested-git", "contains nested git"))
		m.h.WriteFile(".claude/skills/nested-git/vendor/sub/.git", "gitdir: ../../.git/modules/sub\n")

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		a, ok := acts["dir:.claude/skills/nested-git"]
		if !ok {
			t.Fatal("nested-git not found in SkillActions")
		}
		if a.Manual {
			t.Errorf("nested-git with nested .git marker has Manual=true, want false")
		}
	})

	t.Run("incomplete nested observation past maxWalk is non-manual", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".claude/skills/deep-skill/SKILL.md", graphtest.SkillMD("deep-skill", "too deep to inspect"))
		deep := ".claude/skills/deep-skill"
		for range 17 {
			deep = filepath.Join(deep, "d")
		}
		m.h.Mkdir(deep)

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		a, ok := acts["dir:.claude/skills/deep-skill"]
		if !ok {
			t.Fatal("deep-skill not found in SkillActions")
		}
		if a.Manual {
			t.Errorf("deep-skill with incomplete nested observation has Manual=true, want false")
		}
	})
}

// An independent global folder referenced by a symlink from a diagnostic
// profile stays eligible in SkillActions, matching PlanRemove in the CLI, while
// folders actually inside a diagnostic profile remain refused.
func TestSkillActionsAlignsWithCLIWhenProfileLinksIndependentFolder(t *testing.T) {
	m := newMachine(t)
	m.h.Symlink(".claude-omni/skills/humanizer-ref", "../../.claude/skills/humanizer")

	acts, err := m.app.SkillActions(bg)
	must(t, err)

	a := acts["dir:.claude/skills/humanizer"]
	if !a.Remove || a.RemoveKey != "dir:.claude/skills/humanizer" {
		t.Fatalf("humanizer SkillActions = %+v, want Remove=true RemoveKey=%q", a, "dir:.claude/skills/humanizer")
	}
	p, err := m.app.PlanRemove(bg, a.RemoveKey)
	if err != nil {
		t.Fatalf("PlanRemove(%q): %v", a.RemoveKey, err)
	}
	if len(p.Remove.KeptLinks) != 1 || p.Remove.KeptLinks[0].Profile != "claude-omni" {
		t.Errorf("KeptLinks = %+v, want claude-omni profile link", p.Remove.KeptLinks)
	}
	if profileAct := acts["dir:.claude-omni/skills/orca-cli"]; profileAct.Remove {
		t.Errorf("profile-owned skill offered Remove=true (%q)", profileAct.RemoveKey)
	}
}

// SkillActions attributes confirmed foreign provider/plugin ownership without
// granting removal eligibility, and leaves unknown/unproven clones unattributed.
func TestSkillActionsProviderAttribution(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".claude/skills/custom-git/SKILL.md", graphtest.SkillMD("custom-git", "non-github"))
	m.h.Mkdir(".claude/skills/custom-git/.git/objects")
	real, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/custom-git"))
	must(t, err)
	m.git.AddClone(real, ports.CloneStatus{Remote: "https://gitlab.com/org/custom-git.git", Branch: "main", Head: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Clean: true})

	acts, err := m.app.SkillActions(bg)
	must(t, err)

	pluginAct := acts["git:"+graphtest.KarpathySkill]
	if pluginAct.Provider != app.ProviderClaudePlugin {
		t.Errorf("karpathy Provider = %q, want %q", pluginAct.Provider, app.ProviderClaudePlugin)
	}
	if !pluginAct.Remove || pluginAct.RemoveKey != "git:"+graphtest.KarpathySkill || pluginAct.Manual {
		t.Errorf("karpathy Remove=%v RemoveKey=%q Manual=%v, want true, %q, false", pluginAct.Remove, pluginAct.RemoveKey, pluginAct.Manual, "git:"+graphtest.KarpathySkill)
	}

	for _, id := range []string{
		"dir:.claude/skills/custom-git",
		"dir:.claude/skills/humanizer",
		"lock:agent-browser",
		"dir:.claude-omni/skills/orca-cli",
	} {
		if got := acts[id].Provider; got != "" {
			t.Errorf("%s Provider = %q, want empty", id, got)
		}
	}
}

// Grouped folders outside the store and store skills with aliases preserve
// Provider="" (unknown) when not in a proven provider zone, without changing
// removal eligibility.
func TestSkillActionsGroupedAndAliasPreserveUnknownProvider(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".claude/skills/grouped-copy/SKILL.md", graphtest.SkillMD("grouped-copy", "shared manual"))
	m.h.WriteFile(".codex/skills/grouped-copy/SKILL.md", graphtest.SkillMD("grouped-copy", "shared manual"))

	m.h.Symlink(".agents/skills/agent-browser-alias", "agent-browser")

	acts, err := m.app.SkillActions(bg)
	must(t, err)

	for _, id := range []string{
		"dir:.claude/skills/grouped-copy",
		"dir:.codex/skills/grouped-copy",
	} {
		a, ok := acts[id]
		if !ok {
			t.Fatalf("%s missing from SkillActions", id)
		}
		if a.Provider != "" {
			t.Errorf("%s Provider = %q, want empty", id, a.Provider)
		}
		if a.Remove {
			t.Errorf("%s Remove = true, want false for grouped skill", id)
		}
	}

	lockAct := acts["lock:agent-browser"]
	if lockAct.Provider != "" {
		t.Errorf("lock:agent-browser with alias Provider = %q, want empty", lockAct.Provider)
	}
	if !lockAct.Remove || lockAct.RemoveKey != "agent-browser" {
		t.Errorf("lock:agent-browser Remove=%v RemoveKey=%q, want true, %q", lockAct.Remove, lockAct.RemoveKey, "agent-browser")
	}
}

func TestGroupRemoveLifecycleThroughApp(t *testing.T) {
	m := newMachine(t)
	shared := graphtest.SkillMD("group-app", "shared global")
	m.h.WriteFile(".claude/skills/group-app/SKILL.md", shared)
	m.h.WriteFile(".codex/skills/group-app/SKILL.md", shared)
	m.h.Symlink(".kiro/skills/group-app-link", "../../.claude/skills/group-app")
	// Unattributed same-name independent copy in gemini.
	m.h.WriteFile(".gemini/skills/group-app/SKILL.md", graphtest.SkillMD("group-app", "independent"))

	acts, err := m.app.SkillActions(bg)
	must(t, err)
	var groupID string
	for _, id := range []string{"dir:.claude/skills/group-app", "dir:.codex/skills/group-app"} {
		a := acts[id]
		if a.Remove {
			t.Errorf("%s: single Remove=true, want false", id)
		}
		if !a.RemoveGroup || a.RemoveGroupID == "" {
			t.Fatalf("%s: RemoveGroup=%v RemoveGroupID=%q, want true + non-empty", id, a.RemoveGroup, a.RemoveGroupID)
		}
		groupID = a.RemoveGroupID
	}

	p, err := m.app.PlanRemoveGroup(bg, groupID)
	must(t, err)
	if p.Remove == nil || !p.Remove.Group || len(p.Remove.Folders) != 2 {
		t.Fatalf("PlanRemoveGroup = %+v, want Group=true with 2 folders", p.Remove)
	}
	// gemini-cli still has its independent homonym, so it must not be in p.Remove.Targets.
	if slices.Contains(p.Remove.Targets, "gemini-cli") {
		t.Errorf("Targets = %v, must not include gemini-cli", p.Remove.Targets)
	}

	// Stale-state check: adding a new link invalidates the plan.
	m.h.Symlink(".kiro/skills/group-app-link2", "../../.codex/skills/group-app")
	if _, err := m.app.ApplyPlan(bg, p.Request, p.PlanID); !errors.Is(err, app.ErrPlanChanged) {
		t.Fatalf("ApplyPlan after external change = %v, want ErrPlanChanged", err)
	}

	// Re-plan and apply.
	p, err = m.app.PlanRemoveGroup(bg, groupID)
	must(t, err)
	res := m.apply(p)
	if m.exists(".claude/skills/group-app") || m.exists(".codex/skills/group-app") || m.exists(".kiro/skills/group-app-link") {
		t.Fatal("removed group copies/links still exist")
	}
	if !m.exists(".gemini/skills/group-app/SKILL.md") {
		t.Fatal("unattributed homonym was removed")
	}

	// One undo restores all removed copies and links.
	u, err := m.app.PlanUndo(bg, res.SnapshotID)
	must(t, err)
	line := m.lastV2()
	if _, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: res.SnapshotID, PlanID: u.PlanID}); err != nil {
		t.Fatalf("UndoOperation: %v", err)
	}
	if !m.exists(".claude/skills/group-app/SKILL.md") || !m.exists(".codex/skills/group-app/SKILL.md") || !m.exists(".kiro/skills/group-app-link") {
		t.Fatal("undo did not restore all group copies and links")
	}
}
