package toggle_test

import (
	"errors"
	"slices"
	"testing"

	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/toggle"
)

// TestForeignZonesAreNeverTouched: OFF/README.md, synced/, plugins, .system,
// _shared/, gentle-ai's copies and ~/.claude-omni are left exactly as they
// were by every planner, even when they hold a link to the skill.
func TestForeignZonesAreNeverTouched(t *testing.T) {
	m := newMachine(t)
	// Links to find-skills inside zones a planner must not touch.
	m.h.Symlink(".claude/skills/synced/2637_ab12/find-skills", "../../../../.agents/skills/find-skills")
	m.h.Symlink(".codex/skills/.system/find-skills", "../../../.agents/skills/find-skills")
	m.h.Symlink(".claude-omni/skills/fs-link", "../../.agents/skills/find-skills")
	m.h.Symlink(".claude-omni/skills/off-link", "../../.agents/skills/OFF/accessibility")
	m.h.Symlink(graphtest.KarpathyClone+"/skills/find-skills", "../../../../.agents/skills/find-skills")

	zones := map[string]string{
		"OFF/README.md": ".agents/skills/OFF/README.md",
		"synced":        ".claude/skills/synced",
		"plugin":        graphtest.KarpathyClone,
		".system":       ".codex/skills/.system",
		"claude-omni":   ".claude-omni",
	}
	for _, dir := range graphtest.GentleDirs {
		for _, n := range graphtest.GentleSkills {
			zones["gentle-ai "+dir+"/"+n] = dir + "/" + n
		}
	}
	for _, dir := range []string{".agents/skills", ".claude/skills", ".codex/skills", ".config/opencode/skills"} {
		zones["_shared "+dir] = dir + "/_shared"
	}
	before := map[string]map[string]string{}
	world := m.world()
	for name, rel := range zones {
		before[name] = subtree(world, rel)
		if len(before[name]) == 0 {
			t.Fatalf("zone %s (%s) is not in the fixture", name, rel)
		}
	}

	dis, err := m.planner().Disable(m.graph(), "find-skills")
	must(t, err)
	if !slices.Contains(dis.Preview.Kept, m.h.Path(".claude-omni/skills/fs-link")) {
		t.Errorf("kept = %v, want the read-only profile's link", dis.Preview.Kept)
	}
	m.apply(dis)
	adopt, err := m.planner().AdoptOff(m.graph())
	must(t, err)
	m.apply(adopt)
	un, err := m.planner().Unlink(m.graph(), "grill-me", "claude-code")
	must(t, err)
	m.apply(un)

	after := m.world()
	for name, rel := range zones {
		if got := subtree(after, rel); !equalMaps(got, before[name]) {
			t.Errorf("zone %s changed: %v", name, diff(before[name], got))
		}
	}
}

// TestForeignZonesAreRefused: a key whose store folder is a foreign zone is
// never disabled, linked or unlinked.
func TestForeignZonesAreRefused(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".agents/skills/plug/.claude-plugin/plugin.json", `{"name":"plug"}`)
	m.h.WriteFile(".agents/skills/plug/SKILL.md", graphtest.SkillMD("plug", "a plugin"))
	world, st := m.world(), m.state()
	for _, key := range []string{"go-testing", "_shared", "plug"} {
		if _, err := m.planner().Disable(m.graph(), key); !errors.Is(err, toggle.ErrForeignZone) {
			t.Errorf("Disable(%s) error = %v, want ErrForeignZone", key, err)
		}
		if _, err := m.planner().Link(m.graph(), key, "junie"); !errors.Is(err, toggle.ErrForeignZone) {
			t.Errorf("Link(%s) error = %v, want ErrForeignZone", key, err)
		}
	}
	if _, err := m.planner().Unlink(m.graph(), "work-unit-commits", "claude-code"); !errors.Is(err, toggle.ErrForeignZone) {
		t.Errorf("Unlink(work-unit-commits) error = %v, want ErrForeignZone", err)
	}
	noWrite(t, m, world, st)
}

// subtree keeps the entries of a world at rel or below it.
func subtree(world map[string]string, rel string) map[string]string {
	out := map[string]string{}
	for k, v := range world {
		if k == rel || len(k) > len(rel) && k[:len(rel)+1] == rel+"/" {
			out[k] = v
		}
	}
	return out
}

func equalMaps(a, b map[string]string) bool { return len(diff(a, b)) == 0 }
