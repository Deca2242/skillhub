package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/ports"
)

func TestCLIProviderRemoveRequiresAllowProviderAndWarnsAndUndoes(t *testing.T) {
	m := newMachine(t)
	target := "dir:.codex/skills/.system/skill-creator"
	relFile := ".codex/skills/.system/skill-creator/SKILL.md"
	origBytes, err := os.ReadFile(m.h.Path(relFile))
	must(t, err)
	before := m.hash()

	// Without --allow-provider it is refused with exit code 2 and zero mutations.
	refused := m.run("", "skills", "remove", target, "--dry-run")
	if refused.code != 2 {
		t.Fatalf("without --allow-provider = exit %d\nstdout: %s\nstderr: %s", refused.code, refused.stdout, refused.stderr)
	}
	if m.hash() != before {
		t.Fatal("refused provider removal wrote something")
	}

	// With --allow-provider --dry-run it plans, prints the provider warning and PlanID, and writes nothing.
	shown := m.ok("skills", "remove", target, "--allow-provider", "--dry-run")
	for _, want := range []string{
		"Codex",
		"solo elimina los archivos del disco",
		"manifiesto del proveedor",
		"snapshot",
		"skillhub undo",
		m.h.Path(".codex/skills/.system/skill-creator"),
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

	// With --allow-provider --yes --plan <id> it removes the folder, and undo restores byte-for-byte.
	r := m.ok("skills", "remove", target, "--allow-provider", "--yes", "--plan", planID(t, shown.stdout))
	if m.exists(".codex/skills/.system/skill-creator") {
		t.Fatal("provider skill still exists after approved removal")
	}
	snap := m.lastV2().SnapshotID
	if !strings.Contains(r.stdout, "skillhub undo v2:remove --to "+snap) {
		t.Errorf("stdout lacks undo command:\n%s", r.stdout)
	}
	m.approve("undo", "v2:remove", "--to", snap)
	if !m.exists(relFile) {
		t.Fatal("undo did not restore provider skill")
	}
	got, err := os.ReadFile(m.h.Path(relFile))
	must(t, err)
	if string(got) != string(origBytes) {
		t.Fatalf("restored bytes differ: got %q want %q", got, origBytes)
	}
}

func TestCLIProviderRemoveStillRefusesProtectedTargetsEvenWithAllowProvider(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".claude/skills/_shared/common/SKILL.md", graphtest.SkillMD("common", "shared"))
	m.h.WriteFile(".claude/skills/gitlab-clone/SKILL.md", graphtest.SkillMD("gitlab-clone", "gitlab"))
	m.h.Mkdir(".claude/skills/gitlab-clone/.git/objects")
	real, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/gitlab-clone"))
	must(t, err)
	m.git.AddClone(real, ports.CloneStatus{
		Remote: "https://gitlab.com/acme/gitlab-clone.git", Branch: "main",
		Head: "1234567890123456789012345678901234567890", Clean: true,
	})

	cases := []struct {
		name   string
		target string
	}{
		{"shared zone", "dir:.claude/skills/_shared/common"},
		{"diagnostic profile", "dir:.claude-omni/skills/orca-cli"},
		{"symlink", "dir:.claude/skills/archify"},
		{"unrecognized git clone", "dir:.claude/skills/gitlab-clone"},
		{"non-provider git clone", "git:.claude/skills/gitlab-clone"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := m.hash()
			r := m.run("", "skills", "remove", tc.target, "--allow-provider", "--dry-run")
			if r.code != 2 {
				t.Fatalf("skills remove %q --allow-provider = exit %d\nstdout: %s\nstderr: %s", tc.target, r.code, r.stdout, r.stderr)
			}
			if m.hash() != before {
				t.Fatal("refused removal wrote something")
			}
		})
	}
}

func TestCLIProviderCloneRemoveRequiresAllowProviderWarnsAndUndoes(t *testing.T) {
	m := newMachine(t)
	target := "git:" + graphtest.KarpathySkill
	relFile := graphtest.KarpathySkill + "/SKILL.md"
	origBytes, err := os.ReadFile(m.h.Path(relFile))
	must(t, err)
	before := m.hash()

	refused := m.run("", "skills", "remove", target, "--dry-run")
	if refused.code != 2 {
		t.Fatalf("without --allow-provider = exit %d\nstdout: %s\nstderr: %s", refused.code, refused.stdout, refused.stderr)
	}
	if m.hash() != before {
		t.Fatal("refused provider clone removal wrote something")
	}

	shown := m.ok("skills", "remove", target, "--allow-provider", "--dry-run")
	for _, want := range []string{
		"Aviso de proveedor (Claude Code (plugin))",
		"Carpeta (clon completo): " + m.h.Path(graphtest.KarpathyClone),
		"Skills del clon que se eliminan juntas: karpathy-guidelines",
		"borrar el clon " + m.h.Path(graphtest.KarpathyClone),
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

	r := m.ok("skills", "remove", target, "--allow-provider", "--yes", "--plan", planID(t, shown.stdout))
	if m.exists(graphtest.KarpathyClone) {
		t.Fatal("KarpathyClone still exists after approved removal")
	}
	snap := m.lastV2().SnapshotID
	if !strings.Contains(r.stdout, "skillhub undo v2:remove --to "+snap) {
		t.Errorf("stdout lacks undo command:\n%s", r.stdout)
	}
	m.approve("undo", "v2:remove", "--to", snap)
	if !m.exists(relFile) {
		t.Fatal("undo did not restore karpathy-guidelines")
	}
	got, err := os.ReadFile(m.h.Path(relFile))
	must(t, err)
	if string(got) != string(origBytes) {
		t.Fatalf("restored bytes differ: got %q want %q", got, origBytes)
	}
}
