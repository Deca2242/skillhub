package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/ports"
)

// CLI removes an exact independent non-Git global harness skill, presents exact
// paths in its preview, supports cancellation, applies only with bound approval,
// leaves unrelated same-name store/lock/parked entries untouched, and undoes.
func TestCLIRemoveExactGlobalHarnessSkillAndUndo(t *testing.T) {
	m := newMachine(t)
	// Add an unrelated same-name store skill with a lock entry.
	m.h.WriteFile(".agents/skills/humanizer/SKILL.md", graphtest.SkillMD("humanizer", "store copy"))
	m.h.Symlink(".kiro/skills/humanizer-link", "../../.claude/skills/humanizer")
	before := m.hash()

	// Dry-run presents the exact folder and link paths and writes nothing.
	shown := m.ok("skills", "remove", "dir:.claude/skills/humanizer", "--dry-run")
	for _, want := range []string{
		m.h.Path(".claude/skills/humanizer"),
		m.h.Path(".kiro/skills/humanizer-link"),
		m.h.Path(".agents/skills/humanizer"),
		planIDLine,
		dryRunLine,
	} {
		if !strings.Contains(shown.stdout, want) {
			t.Errorf("dry-run stdout lacks %q:\n%s", want, shown.stdout)
		}
	}
	if m.hash() != before {
		t.Fatal("dry-run wrote something")
	}

	// Interactive cancellation writes nothing.
	m.interactive = true
	cancelled := m.run("n\n", "skills", "remove", "dir:.claude/skills/humanizer")
	if cancelled.code != 2 || !strings.Contains(cancelled.stderr, notConfirmed) {
		t.Fatalf("cancelled = exit %d stderr %q", cancelled.code, cancelled.stderr)
	}
	if m.hash() != before {
		t.Fatal("cancelled removal wrote something")
	}
	m.interactive = false

	// Approved removal deletes only the selected folder and its owned link.
	lockBefore, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	r := m.ok("skills", "remove", "dir:.claude/skills/humanizer", "--yes", "--plan", planID(t, shown.stdout))
	if m.exists(".claude/skills/humanizer") || m.exists(".kiro/skills/humanizer-link") {
		t.Fatal("selected folder or its owned link still exists")
	}
	if !m.exists(".agents/skills/humanizer/SKILL.md") {
		t.Fatal("unrelated same-name store folder was removed")
	}
	lockAfter, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	if string(lockAfter) != string(lockBefore) {
		t.Error("removing an independent harness skill modified the lock")
	}

	snap := m.lastV2().SnapshotID
	if !strings.Contains(r.stdout, "skillhub undo v2:remove --to "+snap) {
		t.Errorf("stdout lacks undo command:\n%s", r.stdout)
	}
	m.approve("undo", "v2:remove", "--to", snap)
	if !m.exists(".claude/skills/humanizer/SKILL.md") || !m.exists(".kiro/skills/humanizer-link") {
		t.Fatal("undo did not restore the harness skill and its link")
	}
}

// CLI removes a hand-disabled OFF/ skill via exact ID and supports undo.
func TestCLIRemoveOFFSkillAndUndo(t *testing.T) {
	m := newMachine(t)
	shown := m.ok("skills", "remove", "dir:.agents/skills/OFF/accessibility", "--dry-run")
	for _, want := range []string{
		m.h.Path(".agents/skills/OFF/accessibility"),
		m.h.Path(".claude/skills/OFF/accessibility"),
		"Entrada del lock que se quita: «accessibility»",
	} {
		if !strings.Contains(shown.stdout, want) {
			t.Errorf("dry-run lacks %q:\n%s", want, shown.stdout)
		}
	}
	m.ok("skills", "remove", "dir:.agents/skills/OFF/accessibility", "--yes", "--plan", planID(t, shown.stdout))
	if m.exists(".agents/skills/OFF/accessibility") || m.exists(".claude/skills/OFF/accessibility") {
		t.Fatal("OFF/ skill or link still exists")
	}
	if !m.exists(".agents/skills/OFF/README.md") {
		t.Fatal("OFF/README.md was touched")
	}
	snap := m.lastV2().SnapshotID
	m.approve("undo", "v2:remove", "--to", snap)
	if !m.exists(".agents/skills/OFF/accessibility/SKILL.md") || !m.exists(".claude/skills/OFF/accessibility") {
		t.Fatal("undo did not restore OFF/ skill")
	}
}

// CLI disambiguates active vs parked same-key skills using exact IDs while
// preserving ambiguity refusal for the bare key.
func TestCLIDisambiguatesActiveAndParkedRemove(t *testing.T) {
	m := newMachine(t)
	m.approve("skills", "disable", "grill-me")
	m.h.WriteFile(".agents/skills/grill-me/SKILL.md", graphtest.SkillMD("grill-me", "reinstalled"))

	amb := m.run("", "skills", "remove", "grill-me", "--dry-run")
	if amb.code != 2 || !strings.Contains(amb.stderr, "más de una skill") {
		t.Fatalf("bare ambiguous key = exit %d stderr %q", amb.code, amb.stderr)
	}

	m.approve("skills", "remove", "parked:grill-me")
	if m.exists(".agents/.skillhub/disabled/grill-me") {
		t.Fatal("parked skill still exists")
	}
	if !m.exists(".agents/skills/grill-me/SKILL.md") {
		t.Fatal("active store skill was removed")
	}
}

// CLI refuses project paths, provider/profile zones, symlinks, and Git clones
// (including unrecognized Git origins) with exit code 2 and zero mutations.
func TestCLIRemoveRefusesProjectProviderSymlinkAndGit(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile("projects/repo/.agents/skills/local/SKILL.md", graphtest.SkillMD("local", "project"))
	m.h.WriteFile(".claude/skills/gitlab-clone/SKILL.md", graphtest.SkillMD("gitlab-clone", "gitlab"))
	m.h.Mkdir(".claude/skills/gitlab-clone/.git/objects")
	real, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/gitlab-clone"))
	must(t, err)
	m.git.AddClone(real, ports.CloneStatus{Remote: "https://gitlab.com/acme/gitlab-clone.git", Branch: "main", Head: "1234567890123456789012345678901234567890", Clean: true})

	cases := []struct {
		name   string
		target string
	}{
		{"project path", "dir:projects/repo/.agents/skills/local"},
		{"provider path", "dir:.codex/skills/.system/skill-creator"},
		{"diagnostic profile", "dir:.claude-omni/skills/orca-cli"},
		{"symlink", "dir:.claude/skills/archify"},
		{"plugin git clone", "git:" + graphtest.KarpathySkill},
		{"unrecognized git clone", "dir:.claude/skills/gitlab-clone"},
		{"bare non-store name", "brainstorming"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := m.hash()
			r := m.run("", "skills", "remove", tc.target, "--dry-run")
			if r.code != 2 {
				t.Fatalf("skills remove %q = exit %d\nstdout: %s\nstderr: %s", tc.target, r.code, r.stdout, r.stderr)
			}
			if m.hash() != before {
				t.Fatal("refused removal wrote something")
			}
		})
	}
}
