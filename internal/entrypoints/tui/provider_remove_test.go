package tui_test

import (
	"os"
	"strings"
	"testing"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/graph/graphtest"
)

func TestTUIProviderSkillShowsBadgeGuidanceRemoveWarnsAndUndoes(t *testing.T) {
	mc := newMachine(t)
	relDir := ".claude/skills/local-plugin"
	relFile := relDir + "/SKILL.md"
	mc.h.WriteFile(relDir+"/.claude-plugin/plugin.json", `{"name":"local-plugin"}`)
	mc.h.WriteFile(relFile, graphtest.SkillMD("local-plugin", "plugin skill"))
	origBytes, err := os.ReadFile(mc.h.Path(relFile))
	if err != nil {
		t.Fatal(err)
	}

	m := openSized(t, mc.newApp(), 120, 40)
	selectSkill(t, m, "local-plugin")

	got := screen(m)
	for _, want := range []string{
		text.TUIRowProvider,
		"Gestionada por un plugin de Claude Code",
		text.TUIActionRemove,
		text.TUIKeyRemove,
	} {
		if !strings.Contains(flat(got), flat(want)) {
			t.Errorf("local-plugin screen lacks %q:\n%s", want, got)
		}
	}

	before := mc.hash()
	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on provider skill did not open confirmation plan; status=%q", m.StatusForTest())
	}
	if mc.hash() != before {
		t.Fatal("opening plan wrote something")
	}

	confirm := flat(screen(m))
	for _, want := range []string{
		"Claude Code (plugin)",
		"solo elimina los archivos del disco",
		"manifiesto del proveedor",
		"snapshot",
		"skillhub undo",
	} {
		if !strings.Contains(confirm, flat(want)) {
			t.Errorf("provider confirmation lacks %q:\n%s", want, screen(m))
		}
	}

	// Confirm removes the provider skill, and z restores it byte-for-byte.
	pressRun(m, "s")
	if mc.exists(relDir) {
		t.Fatal("provider skill still exists after confirmed removal")
	}

	pressRun(m, "z")
	if !m.InPlanForTest() {
		t.Fatalf("z did not open undo confirmation; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if !mc.exists(relFile) {
		t.Fatal("undo did not restore provider skill")
	}
	gotBytes, err := os.ReadFile(mc.h.Path(relFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBytes) != string(origBytes) {
		t.Fatalf("restored bytes differ: got %q want %q", gotBytes, origBytes)
	}
}

func TestTUIProviderGitCloneRemoveWarnsConfirmsAndUndoes(t *testing.T) {
	mc := newMachine(t)
	relFile := graphtest.KarpathySkill + "/SKILL.md"
	origBytes, err := os.ReadFile(mc.h.Path(relFile))
	if err != nil {
		t.Fatal(err)
	}

	m := openSized(t, mc.newApp(), 120, 40)
	selectSkill(t, m, "karpathy-guidelines")

	before := mc.hash()
	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on provider git clone did not open confirmation plan; status=%q", m.StatusForTest())
	}
	if mc.hash() != before {
		t.Fatal("opening plan wrote something")
	}

	confirm := strings.ReplaceAll(flat(screen(m)), "- ", "-")
	for _, want := range []string{
		"¿Eliminar karpathy-guidelines?",
		"Carpeta (clon completo):",
		mc.h.Path(graphtest.KarpathyClone),
		"Skills del clon que se eliminan juntas: karpathy-guidelines",
		"Aviso de proveedor (Claude Code (plugin))",
		"solo elimina los archivos del disco",
		"snapshot",
		"skillhub undo",
	} {
		if !strings.Contains(confirm, flat(want)) {
			t.Errorf("provider clone confirmation lacks %q:\n%s", want, screen(m))
		}
	}

	// Confirm removes the whole clone, and z restores it byte-for-byte.
	pressRun(m, "s")
	if mc.exists(graphtest.KarpathyClone) {
		t.Fatal("KarpathyClone still exists after confirmed removal")
	}

	pressRun(m, "z")
	if !m.InPlanForTest() {
		t.Fatalf("z did not open undo confirmation; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if !mc.exists(relFile) {
		t.Fatal("undo did not restore karpathy-guidelines")
	}
	gotBytes, err := os.ReadFile(mc.h.Path(relFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBytes) != string(origBytes) {
		t.Fatalf("restored bytes differ: got %q want %q", gotBytes, origBytes)
	}
}
