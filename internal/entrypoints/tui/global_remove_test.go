package tui_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// TUI removes an independent non-Git global harness skill (humanizer): panel
// and footer offer [e] Eliminar, confirmation shows the exact folder path, n
// cancels with zero mutations, s removes it, and z undoes byte for byte.
func TestTUIRemoveIndependentHarnessSkillConfirmCancelAndUndo(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := openSized(t, a, goldenWidth, 40)
	selectSkill(t, m, "humanizer")

	got := screen(m)
	for _, want := range []string{"manual", "Instalación manual", "Recomendado: [e] Eliminar", text.TUIKeyRemove} {
		if !strings.Contains(got, want) {
			t.Fatalf("humanizer list/panel/footer lacks %q:\n%s", want, got)
		}
	}

	before := mc.hash()
	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e did not open confirmation plan; status=%q", m.StatusForTest())
	}
	confirm := screen(m)
	for _, want := range []string{"¿Eliminar humanizer?", mc.h.Path(".claude/skills/humanizer")} {
		if !strings.Contains(flat(confirm), flat(want)) {
			t.Errorf("confirmation lacks %q:\n%s", want, confirm)
		}
	}

	// Cancel writes nothing.
	pressRun(m, "n")
	if m.InPlanForTest() || mc.hash() != before {
		t.Fatal("cancelling removal left plan open or wrote something")
	}

	// Confirm removes only humanizer, and z restores it.
	pressRun(m, "e")
	pressRun(m, "s")
	if mc.exists(".claude/skills/humanizer") {
		t.Fatal("humanizer still exists after confirmed removal")
	}
	if !strings.Contains(m.StatusForTest(), "Listo: humanizer se movió a la papelera de skillhub.") {
		t.Errorf("status = %q", m.StatusForTest())
	}

	pressRun(m, "z")
	if !m.InPlanForTest() {
		t.Fatalf("z did not open undo confirmation; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if !mc.exists(".claude/skills/humanizer/SKILL.md") {
		t.Fatal("z did not restore humanizer")
	}
}

// TUI removes a hand-disabled OFF/ skill with confirmation and undo.
func TestTUIRemoveOFFSkillAndUndo(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), goldenWidth, 40)
	selectSkill(t, m, "accessibility")

	got := screen(m)
	if !strings.Contains(got, text.TUIActionRemove) || !strings.Contains(got, text.TUIActionAdoptOff) {
		t.Fatalf("OFF skill panel lacks remove or adopt-off:\n%s", got)
	}

	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on OFF skill did not plan; status=%q", m.StatusForTest())
	}
	if confirm := flat(screen(m)); !strings.Contains(confirm, flat(mc.h.Path(".agents/skills/OFF/accessibility"))) {
		t.Errorf("OFF confirmation lacks exact path:\n%s", screen(m))
	}
	pressRun(m, "s")
	if mc.exists(".agents/skills/OFF/accessibility") {
		t.Fatal("OFF skill still exists after removal")
	}
	if !mc.exists(".agents/skills/OFF/README.md") {
		t.Fatal("OFF/README.md was touched")
	}
	pressRun(m, "z")
	pressRun(m, "s")
	if !mc.exists(".agents/skills/OFF/accessibility/SKILL.md") {
		t.Fatal("undo did not restore OFF skill")
	}
}

// TUI removes a parked (disabled) skill from its row and supports undo.
func TestTUIRemoveParkedSkillAndUndo(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	m := openSized(t, a, goldenWidth, 40)
	selectSkill(t, m, "agent-browser")

	got := screen(m)
	for _, want := range []string{text.TUIActionEnable, text.TUIActionRemove, text.TUIKeyRemove} {
		if !strings.Contains(got, want) {
			t.Errorf("parked row lacks %q:\n%s", want, got)
		}
	}

	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on parked row did not plan; status=%q", m.StatusForTest())
	}
	if confirm := flat(screen(m)); !strings.Contains(confirm, flat(mc.h.Path(".agents/.skillhub/disabled/agent-browser"))) {
		t.Errorf("parked confirmation lacks exact path:\n%s", screen(m))
	}
	pressRun(m, "s")
	if mc.exists(".agents/.skillhub/disabled/agent-browser") {
		t.Fatal("parked folder still exists")
	}
	if _, ok := mc.persisted().Parked["agent-browser"]; ok {
		t.Fatal("parked state entry still exists")
	}

	pressRun(m, "z")
	pressRun(m, "s")
	if !mc.exists(".agents/.skillhub/disabled/agent-browser/SKILL.md") {
		t.Fatal("undo did not restore parked folder")
	}
	if _, ok := mc.persisted().Parked["agent-browser"]; !ok {
		t.Fatal("undo did not restore parked state entry")
	}
}

// TUI removes an ignored skill from the ignoradas filter while preserving the
// ignore preference in settings.toml, and supports undo.
func TestTUIRemoveIgnoredSkillKeepsIgnorePreference(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "humanizer")
	m := openSized(t, a, 120, 40)
	for range 5 {
		pressRun(m, "f")
	}
	got := screen(m)
	if !strings.Contains(got, "humanizer") || !strings.Contains(got, text.TUIActionRemove) || !strings.Contains(got, text.TUIActionUnignore) {
		t.Fatalf("ignored row lacks remove or unignore:\n%s", got)
	}

	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on ignored row did not plan; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if mc.exists(".claude/skills/humanizer") {
		t.Fatal("ignored humanizer still exists after removal")
	}
	if got := ignored(t, a); !slices.Equal(got, []string{"humanizer"}) {
		t.Errorf("ignore preference changed = %v, want [humanizer]", got)
	}

	pressRun(m, "z")
	pressRun(m, "s")
	if !mc.exists(".claude/skills/humanizer/SKILL.md") {
		t.Fatal("undo did not restore ignored skill")
	}
	if got := ignored(t, a); !slices.Equal(got, []string{"humanizer"}) {
		t.Errorf("ignore preference after undo = %v, want [humanizer]", got)
	}
}

// A non-GitHub Git clone in a global harness directory does not offer [e]
// Eliminar and pressing e refuses without writing anything.
func TestTUIRefusesGitCloneRemoval(t *testing.T) {
	mc := newMachine(t)
	mc.h.WriteFile(".claude/skills/gitlab-only/SKILL.md", graphtest.SkillMD("gitlab-only", "gitlab clone"))
	mc.h.Mkdir(".claude/skills/gitlab-only/.git/objects")
	realGitLab, err := filepath.EvalSymlinks(mc.h.Path(".claude/skills/gitlab-only"))
	if err != nil {
		t.Fatal(err)
	}
	karpathy, err := filepath.EvalSymlinks(mc.h.Path(graphtest.KarpathyClone))
	if err != nil {
		t.Fatal(err)
	}
	files := fsadapter.NewFileOps(mc.p.Guard())
	agents, err := data.LoadAgents(mc.p, files)
	if err != nil {
		t.Fatal(err)
	}
	git := fakes.NewGit()
	git.AddClone(karpathy, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	git.AddClone(realGitLab, ports.CloneStatus{
		Remote: "https://gitlab.com/acme/gitlab-only.git", Branch: "main",
		Upstream: "origin/main", Head: "2222222222222222222222222222222222222222", Clean: true,
	})
	lock := skillslock.New(mc.p, files)
	a, err := app.New(app.Deps{
		Paths: mc.p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(mc.p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(mc.p.Guard()),
		State: statefile.New(mc.p), Settings: settings.New(mc.p), GitHub: mc.gh, Git: git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: mc.clock,
		ProcDir: mc.proc,
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	m := openSized(t, a, 120, 40)
	selectSkill(t, m, "gitlab-only")
	gotGit := screen(m)
	if strings.Contains(gotGit, text.TUIActionRemove) || strings.Contains(gotGit, "Instalación manual") {
		t.Fatalf("git clone offered remove or manual label:\n%s", gotGit)
	}
	if !strings.Contains(gotGit, text.TUIRowUnmanaged) {
		t.Fatalf("git clone lost unmanaged label:\n%s", gotGit)
	}
	before := mc.hash()
	pressRun(m, "e")
	if m.InPlanForTest() || mc.hash() != before {
		t.Fatalf("e on git clone planned or wrote:\n%s", screen(m))
	}
	if strings.Contains(gotGit, "Gestionada por") {
		t.Fatalf("unknown git clone wrongly claimed provider ownership:\n%s", gotGit)
	}
}

// A skill in a confirmed Claude Code plugin zone shows "proveedor" in the list
// and "Gestionada por…" with concrete Claude plugin CLI guidance in the panel,
// without offering removal.
func TestTUIPluginSkillShowsProviderAndGuidance(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), 120, 40)
	selectSkill(t, m, "karpathy-guidelines")

	got := screen(m)
	for _, want := range []string{
		text.TUIRowProvider,
		"Gestionada por un plugin de Claude Code",
		"enabledPlugins",
		"claude plugin list",
		"claude plugin uninstall",
		"plugin completo",
	} {
		if !strings.Contains(flat(got), flat(want)) {
			t.Errorf("karpathy-guidelines screen lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, text.TUIActionRemove) {
		t.Errorf("plugin skill wrongly offered remove:\n%s", got)
	}

	before := mc.hash()
	pressRun(m, "e")
	if m.InPlanForTest() || mc.hash() != before {
		t.Fatalf("e on plugin skill planned or wrote:\n%s", screen(m))
	}
}

// Grouped copies outside the store do not guess provider ownership in the TUI.
func TestTUIGroupedSkillPreservesUnknownProvider(t *testing.T) {
	mc := newMachine(t)
	mc.h.WriteFile(".claude/skills/grouped-copy/SKILL.md", graphtest.SkillMD("grouped-copy", "shared manual"))
	mc.h.WriteFile(".codex/skills/grouped-copy/SKILL.md", graphtest.SkillMD("grouped-copy", "shared manual"))

	m := openSized(t, mc.newApp(), 120, 40)
	selectSkill(t, m, "grouped-copy")

	got := screen(m)
	if strings.Contains(flat(got), "Gestionada por") || strings.Contains(got, text.TUIRowProvider) {
		t.Fatalf("grouped skill wrongly claimed provider ownership:\n%s", got)
	}
}

// TUI offers [e] Eliminar for an independent global folder referenced by a
// diagnostic profile symlink, removes only the folder while leaving the
// profile symlink untouched, and supports undo.
func TestTUIRemoveIndependentFolderReferencedByProfileLink(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".claude-omni/skills/humanizer-ref", "../../.claude/skills/humanizer")
	m := openSized(t, mc.newApp(), goldenWidth, 40)
	selectSkill(t, m, "humanizer")

	got := screen(m)
	if !strings.Contains(got, text.TUIActionRemove) || !strings.Contains(got, text.TUIKeyRemove) {
		t.Fatalf("humanizer with profile link lacks remove action:\n%s", got)
	}

	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e did not open confirmation plan; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if mc.exists(".claude/skills/humanizer") {
		t.Fatal("humanizer still exists after removal")
	}
	if _, err := os.Lstat(mc.h.Path(".claude-omni/skills/humanizer-ref")); err != nil {
		t.Fatalf("diagnostic profile symlink was removed: %v", err)
	}

	pressRun(m, "z")
	pressRun(m, "s")
	if !mc.exists(".claude/skills/humanizer/SKILL.md") {
		t.Fatal("undo did not restore humanizer")
	}
}

// Grouped (no_canonical) copies whose folders are all confirmed non-Git show
// "manual" / "Instalación manual" with the Salud fallback and do not offer
// [e] Eliminar; grouped copies with a Git-backed folder keep "sin gestionar".
func TestTUIGroupedCopiesManualAndProtectedLabels(t *testing.T) {
	mc := newMachine(t)
	shared := graphtest.SkillMD("shared-copy", "two identical copies")
	mc.h.WriteFile(".claude/skills/shared-copy/SKILL.md", shared)
	mc.h.WriteFile(".codex/skills/shared-copy/SKILL.md", shared)

	mixed := graphtest.SkillMD("mixed-copy", "one manual one git")
	mc.h.WriteFile(".claude/skills/mixed-copy/SKILL.md", mixed)
	mc.h.WriteFile(".codex/skills/mixed-copy/SKILL.md", mixed)
	mc.h.Mkdir(".codex/skills/mixed-copy/.git/objects")
	mc.h.WriteFile(".claude-omni/skills/profile-only/SKILL.md", graphtest.SkillMD("profile-only", "diagnostic profile"))
	realMixed, err := filepath.EvalSymlinks(mc.h.Path(".codex/skills/mixed-copy"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	karpathy, err := filepath.EvalSymlinks(mc.h.Path(graphtest.KarpathyClone))
	if err != nil {
		t.Fatalf("EvalSymlinks karpathy: %v", err)
	}
	files := fsadapter.NewFileOps(mc.p.Guard())
	agents, err := data.LoadAgents(mc.p, files)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	git := fakes.NewGit()
	git.AddClone(karpathy, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	git.AddClone(realMixed, ports.CloneStatus{Remote: "https://gitlab.com/org/mixed-copy.git", Branch: "main", Head: "dddddddddddddddddddddddddddddddddddddddd", Clean: true})
	lock := skillslock.New(mc.p, files)
	a, err := app.New(app.Deps{
		Paths: mc.p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(mc.p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(mc.p.Guard()),
		State: statefile.New(mc.p), Settings: settings.New(mc.p), GitHub: mc.gh, Git: git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: mc.clock,
		ProcDir: mc.proc,
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	m := openSized(t, a, 120, 40)

	selectSkill(t, m, "shared-copy")
	got := screen(m)
	for _, want := range []string{text.TUIRowManual, "Instalación manual", "Recomendado: " + text.TUIActionSeeHealth} {
		if !strings.Contains(got, want) {
			t.Errorf("shared-copy lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, text.TUIActionRemove) {
		t.Errorf("shared-copy wrongly offered remove:\n%s", got)
	}
	before := mc.hash()
	pressRun(m, "e")
	if m.InPlanForTest() || mc.hash() != before {
		t.Errorf("e on shared-copy planned or wrote:\n%s", screen(m))
	}

	m = openSized(t, a, 120, 40)
	selectSkill(t, m, "mixed-copy")
	gotMixed := screen(m)
	if !strings.Contains(gotMixed, text.TUIRowUnmanaged) {
		t.Errorf("mixed-copy lost %q:\n%s", text.TUIRowUnmanaged, gotMixed)
	}
	if strings.Contains(gotMixed, "Instalación manual") || strings.Contains(gotMixed, text.TUIActionRemove) {
		t.Errorf("mixed-copy wrongly showed manual or remove:\n%s", gotMixed)
	}

	m = openSized(t, a, 120, 40)
	selectSkill(t, m, "profile-only")
	gotProfile := screen(m)
	if strings.Contains(gotProfile, "Instalación manual") || strings.Contains(gotProfile, text.TUIActionRemove) {
		t.Errorf("profile-only wrongly showed manual or remove:\n%s", gotProfile)
	}
}

// A skill with a .git marker file or incomplete nested observation must not
// show as manual in the TUI.
func TestTUIWorktreeAndIncompleteGitNotManual(t *testing.T) {
	mc := newMachine(t)
	mc.h.WriteFile(".claude/skills/wt-skill/SKILL.md", graphtest.SkillMD("wt-skill", "worktree"))
	mc.h.WriteFile(".claude/skills/wt-skill/.git", "gitdir: /some/repo/.git/worktrees/wt-skill\n")

	mc.h.WriteFile(".claude/skills/deep-skill/SKILL.md", graphtest.SkillMD("deep-skill", "too deep"))
	deep := ".claude/skills/deep-skill"
	for range 17 {
		deep = filepath.Join(deep, "d")
	}
	mc.h.Mkdir(deep)

	a := mc.newApp()
	for _, name := range []string{"wt-skill", "deep-skill"} {
		m := openSized(t, a, 120, 40)
		selectSkill(t, m, name)
		got := screen(m)
		if !strings.Contains(got, text.TUIRowUnmanaged) {
			t.Errorf("%s lost %q:\n%s", name, text.TUIRowUnmanaged, got)
		}
		if strings.Contains(got, "Instalación manual") {
			t.Errorf("%s wrongly showed manual:\n%s", name, got)
		}
	}
}

// Golden snapshots of the new TUI global removal flows.
func TestGoldenGlobalRemove(t *testing.T) {
	t.Run("HarnessConfirm", func(t *testing.T) {
		mc := newMachine(t)
		m := openSized(t, mc.newApp(), goldenWidth, 30)
		selectSkill(t, m, "humanizer")
		pressRun(m, "e")
		teatest.RequireEqualOutput(t, mc.normalize(screen(m)))
	})
	t.Run("ParkedPanel100", func(t *testing.T) {
		mc := newMachine(t)
		a := mc.newApp()
		mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
		m := openSized(t, a, 100, 30)
		selectSkill(t, m, "agent-browser")
		teatest.RequireEqualOutput(t, stable(mc, screen(m)))
	})
}
