package remove_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/ports"
)

func TestRemoveGroupValidWithLinksAndUndo(t *testing.T) {
	e := machine(t)
	shared := graphtest.SkillMD("shared-pack", "two identical global copies")
	e.h.WriteFile(".claude/skills/shared-pack/SKILL.md", shared)
	e.h.WriteFile(".codex/skills/shared-pack/SKILL.md", shared)
	e.h.Symlink(".kiro/skills/shared-pack-alias", "../../.claude/skills/shared-pack")
	before := e.everything()

	// CLI prefix must remain rejected.
	if _, err := e.remover().Remove("group:dir:.claude/skills/shared-pack"); !errors.Is(err, remove.ErrInvalidKey) {
		t.Fatalf("Remove(group:...) = %v, want ErrInvalidKey", err)
	}

	// Find the scanner-observed Skill ID for shared-pack.
	g, err := e.observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	var skillID string
	for _, s := range g.Skills {
		if s.Name == "shared-pack" {
			skillID = s.ID
			break
		}
	}
	if skillID == "" {
		t.Fatal("shared-pack not found in observed graph")
	}

	plan, err := e.remover().RemoveGroup(skillID)
	if err != nil {
		t.Fatalf("RemoveGroup(%q): %v", skillID, err)
	}
	pv := plan.Preview
	if !pv.Group || pv.SkillID != skillID {
		t.Fatalf("Preview Group=%v SkillID=%q, want true, %q", pv.Group, pv.SkillID, skillID)
	}
	var gotFolders []string
	for _, f := range pv.Folders {
		gotFolders = append(gotFolders, f.Path)
	}
	wantFolders := []string{e.h.Path(".claude/skills/shared-pack"), e.h.Path(".codex/skills/shared-pack")}
	slices.Sort(wantFolders)
	if !slices.Equal(gotFolders, wantFolders) {
		t.Errorf("Preview.Folders = %v, want %v", gotFolders, wantFolders)
	}
	wantLinks := []string{e.h.Path(".kiro/skills/shared-pack-alias")}
	if got := linkPaths(pv.Links); !slices.Equal(got, wantLinks) {
		t.Errorf("Preview.Links = %v, want %v", got, wantLinks)
	}
	for _, wantAgent := range []string{"claude-code", "codex", "kiro-cli", "opencode"} {
		if !slices.Contains(pv.Targets, wantAgent) {
			t.Errorf("Preview.Targets = %v, lacks %s", pv.Targets, wantAgent)
		}
	}

	out := e.apply(plan)
	for _, p := range append(wantFolders, wantLinks...) {
		if exists(t, p) {
			t.Errorf("removed path %s still exists", p)
		}
	}

	e.undo(out)
	e.sameAs(before, "remove group and undo")
}

func TestRemoveGroupPreservesHomonymAndProtectedCopy(t *testing.T) {
	e := machine(t)
	shared := graphtest.SkillMD("shared-pack", "identical copy")
	e.h.WriteFile(".claude/skills/shared-pack/SKILL.md", shared)
	// Protected copy: identical content in a git-backed folder in codex.
	e.h.WriteFile(".codex/skills/shared-pack/SKILL.md", shared)
	e.h.Mkdir(".codex/skills/shared-pack/.git/objects")
	realCodex, err := filepath.EvalSymlinks(e.h.Path(".codex/skills/shared-pack"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	e.git.AddClone(realCodex, ports.CloneStatus{
		Remote: "https://gitlab.com/org/shared-pack.git", Branch: "main",
		Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Clean: true,
	})
	// Unattributed same-name independent copy (different content) in gemini.
	e.h.WriteFile(".gemini/skills/shared-pack/SKILL.md", graphtest.SkillMD("shared-pack", "different independent copy"))

	g, err := e.observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	var skillID string
	for _, s := range g.Skills {
		if s.Name == "shared-pack" && len(s.Folders) > 1 {
			skillID = s.ID
			break
		}
	}
	if skillID == "" {
		t.Fatal("grouped shared-pack not found")
	}

	plan, err := e.remover().RemoveGroup(skillID)
	if err != nil {
		t.Fatalf("RemoveGroup: %v", err)
	}
	pv := plan.Preview
	if len(pv.Folders) != 1 || pv.Folders[0].Path != e.h.Path(".claude/skills/shared-pack") {
		t.Fatalf("Folders = %+v, want only .claude/skills/shared-pack", pv.Folders)
	}
	if !slices.ContainsFunc(pv.KeptCopies, func(c remove.KeptCopy) bool {
		return c.Path == e.h.Path(".codex/skills/shared-pack") && c.Reason != ""
	}) {
		t.Errorf("KeptCopies = %+v, want .codex/skills/shared-pack with reason", pv.KeptCopies)
	}
	if !slices.ContainsFunc(pv.Homonyms, func(h remove.Homonym) bool {
		return h.Path == e.h.Path(".gemini/skills/shared-pack")
	}) {
		t.Errorf("Homonyms = %+v, want .gemini/skills/shared-pack", pv.Homonyms)
	}
	// codex and gemini-cli still see retained copies, so they must NOT be in Targets (losing access).
	if slices.Contains(pv.Targets, "codex") || slices.Contains(pv.Targets, "gemini-cli") {
		t.Errorf("Targets = %v, must not include agents with retained copies (codex, gemini-cli)", pv.Targets)
	}
	if !slices.Contains(pv.Targets, "claude-code") {
		t.Errorf("Targets = %v, want claude-code", pv.Targets)
	}

	out := e.apply(plan)
	if exists(t, e.h.Path(".claude/skills/shared-pack")) {
		t.Fatal("eligible copy still exists")
	}
	if !exists(t, e.h.Path(".codex/skills/shared-pack/SKILL.md")) {
		t.Fatal("protected git copy was removed")
	}
	if !exists(t, e.h.Path(".gemini/skills/shared-pack/SKILL.md")) {
		t.Fatal("unattributed homonym was removed")
	}
	e.undo(out)
	if !exists(t, e.h.Path(".claude/skills/shared-pack/SKILL.md")) {
		t.Fatal("undo did not restore removed copy")
	}
}

func TestRemoveGroupRefusesAllProtectedAndMissing(t *testing.T) {
	e := machine(t)
	if _, err := e.remover().RemoveGroup("dir:.claude/skills/nonexistent"); !errors.Is(err, remove.ErrNotFound) {
		t.Errorf("RemoveGroup(nonexistent) = %v, want ErrNotFound", err)
	}
	if _, err := e.remover().RemoveGroup("project:/tmp/proj:.agents/skills/foo"); !errors.Is(err, remove.ErrNotFound) {
		t.Errorf("RemoveGroup(project:...) = %v, want ErrNotFound", err)
	}
	// Profile-only skill has 0 eligible folders.
	e.h.WriteFile(".claude-omni/skills/prof-only/SKILL.md", graphtest.SkillMD("prof-only", "profile only"))
	if _, err := e.remover().RemoveGroup("dir:.claude-omni/skills/prof-only"); err == nil {
		t.Error("RemoveGroup(profile-only) = nil, want error")
	}
}

func TestRemoveGroupStoreLockWithIdenticalHarnessCopyAndUndo(t *testing.T) {
	e := machine(t)
	content := graphtest.SkillMD("locked-pack", "shared store and harness copy")
	e.h.WriteFile(".agents/skills/locked-pack/SKILL.md", content)
	sha := e.treeSHA(".agents/skills/locked-pack")
	e.addEntry("locked-pack", "owner/locked-pack", "skills/locked-pack/SKILL.md", sha)
	// Identical copy in a configured harness directory, attributed to the same lock:locked-pack skill.
	e.h.WriteFile(".claude/skills/locked-pack/SKILL.md", content)
	if got := e.treeSHA(".claude/skills/locked-pack"); got != sha {
		t.Fatalf("harness copy treeSHA = %s, want %s", got, sha)
	}
	before := e.everything()

	plan, err := e.remover().RemoveGroup("lock:locked-pack")
	if err != nil {
		t.Fatalf("RemoveGroup(lock:locked-pack): %v", err)
	}
	pv := plan.Preview
	if !pv.Group || pv.SkillID != "lock:locked-pack" {
		t.Fatalf("Preview Group=%v SkillID=%q, want true, lock:locked-pack", pv.Group, pv.SkillID)
	}
	var gotFolders []string
	for _, f := range pv.Folders {
		gotFolders = append(gotFolders, f.Path)
	}
	wantFolders := []string{e.h.Path(".agents/skills/locked-pack"), e.h.Path(".claude/skills/locked-pack")}
	slices.Sort(wantFolders)
	if !slices.Equal(gotFolders, wantFolders) {
		t.Errorf("Preview.Folders = %v, want %v (KeptCopies=%+v)", gotFolders, wantFolders, pv.KeptCopies)
	}
	if pv.LockEntry == nil || pv.LockEntry.Name != "locked-pack" {
		t.Fatalf("Preview.LockEntry = %+v, want locked-pack", pv.LockEntry)
	}

	out := e.apply(plan)
	for _, p := range wantFolders {
		if exists(t, p) {
			t.Errorf("removed folder %s still exists", p)
		}
	}

	e.undo(out)
	e.sameAs(before, "remove store+lock+harness group and undo")
}
