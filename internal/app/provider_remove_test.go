package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/ports"
)

func TestAppProviderRemoveLifecycleAndSkillActions(t *testing.T) {
	t.Run("ZoneGentle skill via Plan, ApplyPlan, PlanUndo", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".gentle-ai/state.json", `{"components":["skills"]}`)
		m.h.WriteFile(".claude/skills/gentle-one/SKILL.md", "---\nname: gentle-one\ndescription: gentle\nmetadata:\n  author: gentleman-programming\n---\n")

		target := "dir:.claude/skills/gentle-one"
		if _, err := m.app.PlanRemove(bg, target); !errors.Is(err, app.ErrForeignZone) {
			t.Fatalf("default PlanRemove(%q) = %v, want ErrForeignZone", target, err)
		}

		p, err := m.app.Plan(bg, app.Request{
			Action:        app.ActionRemove,
			Key:           target,
			AllowProvider: true,
		})
		must(t, err)
		if p.Remove == nil || p.Remove.Provider != app.ProviderGentleAI {
			t.Fatalf("Remove preview = %+v, want Provider=%q", p.Remove, app.ProviderGentleAI)
		}

		// Approval bound to PlanID must reject mismatched AllowProvider: false.
		if _, err := m.app.ApplyPlan(bg, app.Request{Action: app.ActionRemove, Key: target, AllowProvider: false}, p.PlanID); err == nil {
			t.Fatal("ApplyPlan with AllowProvider=false succeeded for provider plan")
		}

		res := m.apply(p)
		if m.exists(".claude/skills/gentle-one") {
			t.Fatal("gentle-one still exists after ApplyPlan")
		}

		u, err := m.app.PlanUndo(bg, res.SnapshotID)
		must(t, err)
		line := m.lastV2()
		if _, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: res.SnapshotID, PlanID: u.PlanID}); err != nil {
			t.Fatalf("UndoOperation: %v", err)
		}
		if !m.exists(".claude/skills/gentle-one/SKILL.md") {
			t.Fatal("undo did not restore gentle-one")
		}
	})

	t.Run("ZoneProvider codex system skill", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".codex/skills/.system/sys-skill/SKILL.md", graphtest.SkillMD("sys-skill", "system"))

		target := "dir:.codex/skills/.system/sys-skill"
		p, err := m.app.Plan(bg, app.Request{
			Action:        app.ActionRemove,
			Key:           target,
			AllowProvider: true,
		})
		must(t, err)
		if p.Remove == nil || p.Remove.Provider != app.ProviderCodex {
			t.Fatalf("Remove preview = %+v, want Provider=%q", p.Remove, app.ProviderCodex)
		}
	})

	t.Run("SkillActions exposes Remove alongside Provider for non-git plugin", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".claude/skills/local-plugin/.claude-plugin/plugin.json", `{"name":"local-plugin"}`)
		m.h.WriteFile(".claude/skills/local-plugin/SKILL.md", graphtest.SkillMD("local-plugin", "plugin"))

		acts, err := m.app.SkillActions(bg)
		must(t, err)

		localPlugin := acts["dir:.claude/skills/local-plugin"]
		if localPlugin.Provider != app.ProviderClaudePlugin || !localPlugin.Remove || localPlugin.RemoveKey != "dir:.claude/skills/local-plugin" {
			t.Errorf("local-plugin = %+v, want Provider=%q Remove=true RemoveKey=%q", localPlugin, app.ProviderClaudePlugin, "dir:.claude/skills/local-plugin")
		}

		karpathy := acts["git:"+graphtest.KarpathySkill]
		if karpathy.Provider != app.ProviderClaudePlugin || !karpathy.Remove || karpathy.RemoveKey != "git:"+graphtest.KarpathySkill {
			t.Errorf("karpathy = %+v, want Provider=%q Remove=true RemoveKey=%q", karpathy, app.ProviderClaudePlugin, "git:"+graphtest.KarpathySkill)
		}
		// Shared _shared and profile skills must never offer Remove.
		if a := acts["dir:.claude/skills/_shared"]; a.Remove || a.RemoveGroup {
			t.Errorf("_shared skill offered removal: %+v", a)
		}
		if a := acts["dir:.claude-omni/skills/orca-cli"]; a.Remove || a.RemoveGroup {
			t.Errorf("profile skill offered removal: %+v", a)
		}
		_ = filepath.Base
	})

	t.Run("karpathy-guidelines plugin git clone remove and byte-for-byte undo", func(t *testing.T) {
		m := newMachine(t)
		relSkill := graphtest.KarpathySkill + "/SKILL.md"
		origBytes, err := os.ReadFile(m.h.Path(relSkill))
		must(t, err)
		target := "git:" + graphtest.KarpathySkill
		if _, err := m.app.PlanRemove(bg, target); !errors.Is(err, app.ErrForeignZone) {
			t.Fatalf("default PlanRemove(%q) = %v, want ErrForeignZone", target, err)
		}

		p, err := m.app.Plan(bg, app.Request{
			Action:        app.ActionRemove,
			Key:           target,
			AllowProvider: true,
		})
		must(t, err)
		if p.Remove == nil || !p.Remove.Clone || p.Remove.Provider != app.ProviderClaudePlugin {
			t.Fatalf("Remove preview = %+v, want Clone=true Provider=%q", p.Remove, app.ProviderClaudePlugin)
		}
		if p.Remove.Folder != m.h.Path(graphtest.KarpathyClone) {
			t.Errorf("Remove.Folder = %q, want %q", p.Remove.Folder, m.h.Path(graphtest.KarpathyClone))
		}
		if !slices.Contains(p.Remove.CloneSkills, "karpathy-guidelines") {
			t.Errorf("Remove.CloneSkills = %v, want karpathy-guidelines", p.Remove.CloneSkills)
		}

		res := m.apply(p)
		if m.exists(graphtest.KarpathyClone) {
			t.Fatal("KarpathyClone still exists after ApplyPlan")
		}

		u, err := m.app.PlanUndo(bg, res.SnapshotID)
		must(t, err)
		line := m.lastV2()
		if _, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: res.SnapshotID, PlanID: u.PlanID}); err != nil {
			t.Fatalf("UndoOperation: %v", err)
		}
		gotBytes, err := os.ReadFile(m.h.Path(relSkill))
		must(t, err)
		if string(gotBytes) != string(origBytes) {
			t.Fatalf("restored bytes differ: got %q want %q", gotBytes, origBytes)
		}
	})

	t.Run("non-provider git clone stays rejected even with AllowProvider", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".claude/skills/plain-git/SKILL.md", graphtest.SkillMD("plain-git", "plain"))
		m.h.Mkdir(".claude/skills/plain-git/.git/objects")
		realPlain, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/plain-git"))
		must(t, err)
		m.git.AddClone(realPlain, ports.CloneStatus{
			Remote: "https://github.com/org/plain-git.git", Branch: "main",
			Head: "1111111111111111111111111111111111111111", Clean: true,
		})

		acts, err := m.app.SkillActions(bg)
		must(t, err)
		if a := acts["git:.claude/skills/plain-git"]; a.Remove || a.RemoveGroup {
			t.Errorf("non-provider git clone offered removal: %+v", a)
		}
		if _, err := m.app.Plan(bg, app.Request{
			Action:        app.ActionRemove,
			Key:           "git:.claude/skills/plain-git",
			AllowProvider: true,
		}); err == nil {
			t.Fatal("Plan(AllowProvider: true) on non-provider git clone succeeded, want error")
		}
	})
}
