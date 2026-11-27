package tui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
)

func newProjectTUIApp(t *testing.T, mc *machine, projectDir string) *app.App {
	t.Helper()
	files := fsadapter.NewFileOps(mc.p.Guard())
	agents, err := data.LoadAgents(mc.p, files)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	karpathy, err := filepath.EvalSymlinks(mc.h.Path(graphtest.KarpathyClone))
	if err != nil {
		t.Fatalf("EvalSymlinks karpathy: %v", err)
	}
	git := fakes.NewGit()
	git.AddClone(karpathy, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	lock := skillslock.New(mc.p, files)
	a, err := app.New(app.Deps{
		Paths:       mc.p,
		Agents:      agents,
		Files:       files,
		Dirs:        fsadapter.NewScanner(mc.p.Guard()),
		Lock:        lock,
		ManagedLock: lock,
		Links:       fsadapter.NewLinkOps(mc.p.Guard()),
		State:       statefile.New(mc.p),
		Settings:    settings.New(mc.p),
		GitHub:      mc.gh,
		Git:         git,
		Notifier:    fakes.NewNotifier(),
		Background:  fakes.NewBackgroundService(),
		Clock:       mc.clock,
		ProcDir:     mc.proc,
		ProjectDir:  projectDir,
		Project:     fsadapter.NewProjectOps(mc.p.Guard()),
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return a
}

func writeTUIProjectSkill(t *testing.T, root, rel, content string) {
	t.Helper()
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func realTUITempDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	realDir, err := filepath.EvalSymlinks(d)
	if err != nil {
		return d
	}
	return realDir
}

func selectProjectSkillRow(t *testing.T, m *tui.Model, mustContain ...string) string {
	t.Helper()
	for range 60 {
		got := flat(screen(m))
		matched := true
		for _, want := range mustContain {
			if !strings.Contains(got, flat(want)) {
				matched = false
				break
			}
		}
		if matched {
			return screen(m)
		}
		pressRun(m, "down")
	}
	t.Fatalf("could not find project row containing %v; last screen:\n%s", mustContain, screen(m))
	return ""
}

func TestTUIProjectRemoveCancelConfirmAndUndoIsolatesGlobalAndHomonyms(t *testing.T) {
	mc := newMachine(t)
	mc.h.WriteFile(".claude/skills/solo/SKILL.md", graphtest.SkillMD("solo", "global independent copy"))
	lockBefore, err := os.ReadFile(mc.p.SkillsLockFile())
	if err != nil {
		t.Fatal(err)
	}

	parent := realTUITempDir(t)
	projA := filepath.Join(parent, "proj-a")
	projB := filepath.Join(parent, "proj-b")
	outside := filepath.Join(parent, "outside-linked")
	writeTUIProjectSkill(t, parent, ".agents/skills/parent-only/SKILL.md", graphtest.SkillMD("parent-only", "parent scope"))
	writeTUIProjectSkill(t, projA, ".agents/skills/solo/SKILL.md", graphtest.SkillMD("solo", "projA agents copy"))
	writeTUIProjectSkill(t, projA, ".agents/skills/solo/notes.txt", "keep exact content\n")
	writeTUIProjectSkill(t, projA, ".claude/skills/solo/SKILL.md", graphtest.SkillMD("solo", "projA claude copy"))
	writeTUIProjectSkill(t, outside, "SKILL.md", graphtest.SkillMD("linked-proj", "linked target"))
	if err := os.MkdirAll(filepath.Join(projA, ".claude/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projA, ".claude/skills/linked-proj")); err != nil {
		t.Fatal(err)
	}
	writeTUIProjectSkill(t, projA, ".agents/skills/git-proj/SKILL.md", graphtest.SkillMD("git-proj", "git dir"))
	if err := os.MkdirAll(filepath.Join(projA, ".agents/skills/git-proj/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTUIProjectSkill(t, projB, ".claude/skills/solo/SKILL.md", graphtest.SkillMD("solo", "projB copy"))

	wantProjAgentsHash, err := treehash.Dir(filepath.Join(projA, ".agents/skills/solo"))
	if err != nil {
		t.Fatal(err)
	}
	wantGlobalHash, err := treehash.Dir(mc.h.Path(".claude/skills/solo"))
	if err != nil {
		t.Fatal(err)
	}
	wantProjClaudeHash, err := treehash.Dir(filepath.Join(projA, ".claude/skills/solo"))
	if err != nil {
		t.Fatal(err)
	}

	mGlobal := openSized(t, newProjectTUIApp(t, mc, ""), goldenWidth, 40)
	globalVisLine := ""
	for _, line := range strings.Split(screen(mGlobal), "\n") {
		if strings.Contains(line, "Visibles:") {
			globalVisLine = line
			break
		}
	}

	m := openSized(t, newProjectTUIApp(t, mc, projA), goldenWidth, 40)
	if strings.Contains(flat(screen(m)), "parent-only") {
		t.Fatalf("startup project view leaked parent directory skill:\n%s", screen(m))
	}
	if globalVisLine != "" && !strings.Contains(screen(m), globalVisLine) {
		t.Fatalf("project skills distorted global visibility summary:\nwant line: %q\nscreen:\n%s", globalVisLine, screen(m))
	}

	// Non-eligible symlinked project skill shows explanation and disables remove.
	linkedScreen := selectProjectSkillRow(t, m, filepath.Join(projA, ".claude/skills/linked-proj"), "enlace simbólico")
	if strings.Contains(linkedScreen, text.TUIActionRemove) {
		t.Fatalf("linked project skill wrongly offered remove:\n%s", linkedScreen)
	}
	beforeHome := mc.hash()
	pressRun(m, "e")
	if m.InPlanForTest() || mc.hash() != beforeHome {
		t.Fatalf("e on linked project skill planned or wrote:\n%s", screen(m))
	}
	if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); err != nil {
		t.Fatalf("outside target modified: %v", err)
	}

	// Non-eligible git-backed project skill shows explanation and disables remove.
	m = openSized(t, newProjectTUIApp(t, mc, projA), goldenWidth, 40)
	gitScreen := selectProjectSkillRow(t, m, filepath.Join(projA, ".agents/skills/git-proj"), ".git")
	if strings.Contains(gitScreen, text.TUIActionRemove) {
		t.Fatalf("git project skill wrongly offered remove:\n%s", gitScreen)
	}
	pressRun(m, "e")
	if m.InPlanForTest() || mc.hash() != beforeHome {
		t.Fatalf("e on git project skill planned or wrote:\n%s", screen(m))
	}

	// Select eligible projA/.agents/skills/solo, verify detail box, preview, cancel with n, then confirm with s.
	m = openSized(t, newProjectTUIApp(t, mc, projA), goldenWidth, 40)
	projScreen := selectProjectSkillRow(t, m, filepath.Join(projA, ".agents/skills/solo"), "Skill del proyecto actual (.agents/skills)")
	if !strings.Contains(projScreen, text.TUIActionRemove) {
		t.Fatalf("eligible project skill panel lacks remove action:\n%s", projScreen)
	}
	pressRun(m, "enter")
	detailScreen := flat(screen(m))
	for _, want := range []string{"ID: project:.agents/skills/solo", "Ámbito: proyecto actual", projA} {
		if !strings.Contains(detailScreen, flat(want)) {
			t.Errorf("detail view lacks %q:\n%s", want, screen(m))
		}
	}
	pressRun(m, "esc")

	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on eligible project skill did not open plan; status=%q", m.StatusForTest())
	}
	confirmText := flat(screen(m))
	if !strings.Contains(confirmText, flat(filepath.Join(projA, ".agents/skills/solo"))) || !strings.Contains(confirmText, flat(".agents/skills de este proyecto")) {
		t.Errorf("confirmation lacks exact project folder or project scope:\n%s", screen(m))
	}

	pressRun(m, "n")
	if m.InPlanForTest() || mc.hash() != beforeHome {
		t.Fatalf("n cancel kept plan or changed home")
	}
	if _, err := os.Stat(filepath.Join(projA, ".agents/skills/solo/SKILL.md")); err != nil {
		t.Fatalf("n cancel deleted project folder: %v", err)
	}

	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("second e did not open plan; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if _, err := os.Lstat(filepath.Join(projA, ".agents/skills/solo")); !os.IsNotExist(err) {
		t.Fatalf("projA/.agents/skills/solo still exists after confirm: %v", err)
	}
	if got, err := treehash.Dir(mc.h.Path(".claude/skills/solo")); err != nil || got.SHA != wantGlobalHash.SHA {
		t.Fatalf("global copy modified after project delete: %q err=%v", got.SHA, err)
	}
	if got, err := treehash.Dir(filepath.Join(projA, ".claude/skills/solo")); err != nil || got.SHA != wantProjClaudeHash.SHA {
		t.Fatalf("projA/.claude/skills/solo modified: %q err=%v", got.SHA, err)
	}
	if _, err := os.Stat(filepath.Join(projB, ".claude/skills/solo/SKILL.md")); err != nil {
		t.Fatalf("projB copy modified: %v", err)
	}
	lockAfter, err := os.ReadFile(mc.p.SkillsLockFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(lockAfter) != string(lockBefore) {
		t.Error("removing project skill modified skills-lock.json")
	}

	// Undo with z restores projA/.agents/skills/solo identically.
	pressRun(m, "z")
	if !m.InPlanForTest() {
		t.Fatalf("z in origin project did not open undo plan; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if got, err := treehash.Dir(filepath.Join(projA, ".agents/skills/solo")); err != nil || got.SHA != wantProjAgentsHash.SHA {
		t.Fatalf("restored tree hash = %q err=%v, want %q", got.SHA, err, wantProjAgentsHash.SHA)
	}
	if got, err := treehash.Dir(mc.h.Path(".claude/skills/solo")); err != nil || got.SHA != wantGlobalHash.SHA {
		t.Fatalf("undo modified global copy: %q err=%v", got.SHA, err)
	}
}

func TestTUIProjectWrongProjectUndoRefused(t *testing.T) {
	mc := newMachine(t)
	projA := realTUITempDir(t)
	projB := realTUITempDir(t)
	writeTUIProjectSkill(t, projA, ".agents/skills/solo/SKILL.md", graphtest.SkillMD("solo", "projA only"))
	writeTUIProjectSkill(t, projB, ".claude/skills/solo/SKILL.md", graphtest.SkillMD("solo", "projB only"))
	wantHash, err := treehash.Dir(filepath.Join(projA, ".agents/skills/solo"))
	if err != nil {
		t.Fatal(err)
	}

	mA := openSized(t, newProjectTUIApp(t, mc, projA), goldenWidth, 40)
	selectProjectSkillRow(t, mA, filepath.Join(projA, ".agents/skills/solo"), "Skill del proyecto actual (.agents/skills)")
	pressRun(mA, "e")
	if !mA.InPlanForTest() {
		t.Fatalf("e did not open plan in projA; status=%q", mA.StatusForTest())
	}
	pressRun(mA, "s")
	if _, err := os.Lstat(filepath.Join(projA, ".agents/skills/solo")); !os.IsNotExist(err) {
		t.Fatalf("projA skill still exists: %v", err)
	}

	// Wrong project B must report z is session-only and refuse Historial u undo, naming the owning project.
	mB := openSized(t, newProjectTUIApp(t, mc, projB), goldenWidth, 40)
	pressRun(mB, "z")
	if mB.InPlanForTest() || !strings.Contains(mB.StatusForTest(), text.TUIZNothing) {
		t.Fatalf("z in fresh session = plan:%v status:%q", mB.InPlanForTest(), mB.StatusForTest())
	}
	pressRun(mB, "h")
	pressRun(mB, "u")
	if mB.InPlanForTest() {
		t.Fatalf("u from wrong project opened undo plan:\n%s", screen(mB))
	}
	if !strings.Contains(mB.StatusForTest(), projA) {
		t.Errorf("wrong-project TUI status = %q, want owning project %q", mB.StatusForTest(), projA)
	}
	if _, err := os.Lstat(filepath.Join(projA, ".agents/skills/solo")); !os.IsNotExist(err) {
		t.Fatalf("wrong-project undo touched projA: %v", err)
	}

	// Reopening in origin project A succeeds via Historial (h -> u).
	mA2 := openSized(t, newProjectTUIApp(t, mc, projA), goldenWidth, 40)
	pressRun(mA2, "h")
	pressRun(mA2, "u")
	if !mA2.InPlanForTest() {
		t.Fatalf("u in reopened projA failed; status=%q", mA2.StatusForTest())
	}
	pressRun(mA2, "s")
	if got, err := treehash.Dir(filepath.Join(projA, ".agents/skills/solo")); err != nil || got.SHA != wantHash.SHA {
		t.Fatalf("reopened projA undo hash = %q err=%v, want %q", got.SHA, err, wantHash.SHA)
	}
}

func TestTUISymlinkedProjectRootAndSymlinkedCwdAreSafe(t *testing.T) {
	mc := newMachine(t)
	parent := realTUITempDir(t)
	outside := filepath.Join(parent, "outside-root-target")
	writeTUIProjectSkill(t, outside, "solo/SKILL.md", graphtest.SkillMD("solo", "outside target"))
	wantOutside, err := treehash.Dir(outside)
	if err != nil {
		t.Fatal(err)
	}

	// 1) Real startup project with symlinked .agents/skills root shows non-removable linked row.
	projLinkedRoot := filepath.Join(parent, "proj-linked-root")
	if err := os.MkdirAll(filepath.Join(projLinkedRoot, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projLinkedRoot, ".agents/skills")); err != nil {
		t.Fatal(err)
	}
	mRoot := openSized(t, newProjectTUIApp(t, mc, projLinkedRoot), goldenWidth, 40)
	rootScreen := selectProjectSkillRow(t, mRoot, filepath.Join(projLinkedRoot, ".agents/skills"), "enlace simbólico")
	if strings.Contains(rootScreen, text.TUIActionRemove) {
		t.Fatalf("linked root wrongly offered remove:\n%s", rootScreen)
	}
	pressRun(mRoot, "e")
	if mRoot.InPlanForTest() {
		t.Fatalf("e on linked root opened plan:\n%s", screen(mRoot))
	}

	// 2) Symlinked startup cwd does not add a bogus project row to the global inventory.
	symCwd := filepath.Join(parent, "sym-cwd")
	if err := os.Symlink(outside, symCwd); err != nil {
		t.Fatal(err)
	}
	mGlobal := openSized(t, newProjectTUIApp(t, mc, ""), goldenWidth, 40)
	mSymCwd := openSized(t, newProjectTUIApp(t, mc, symCwd), goldenWidth, 40)
	if strings.Contains(flat(screen(mSymCwd)), "sym-cwd") {
		t.Fatalf("symlinked startup cwd polluted inventory:\n%s", screen(mSymCwd))
	}
	if screen(mSymCwd) != screen(mGlobal) {
		t.Fatalf("symlinked startup cwd changed global Skills view:\ngot:\n%s\nwant:\n%s", screen(mSymCwd), screen(mGlobal))
	}

	gotOutside, err := treehash.Dir(outside)
	if err != nil || gotOutside.SHA != wantOutside.SHA {
		t.Fatalf("external target mutated: err=%v got=%q want=%q", err, gotOutside.SHA, wantOutside.SHA)
	}
}

func TestTUIProjectRemoveSymlinksAndUndo(t *testing.T) {
	mc := newMachine(t)
	proj := realTUITempDir(t)
	writeTUIProjectSkill(t, proj, ".claude/skills/golang-code-style/SKILL.md", graphtest.SkillMD("golang-code-style", "project claude skill"))
	writeTUIProjectSkill(t, proj, ".claude/skills/golang-code-style/notes.txt", "keep exact bytes\n")
	if err := os.MkdirAll(filepath.Join(proj, ".agents/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	const relTarget = "../../.claude/skills/golang-code-style"
	if err := os.Symlink(relTarget, filepath.Join(proj, ".agents/skills/golang-code-style")); err != nil {
		t.Fatal(err)
	}
	wantTreeHash, err := treehash.Dir(filepath.Join(proj, ".claude/skills/golang-code-style"))
	if err != nil {
		t.Fatal(err)
	}

	m := openSized(t, newProjectTUIApp(t, mc, proj), goldenWidth, 40)

	// Selecting the symlink row must not offer remove and pressing e must not open a plan.
	linkedScreen := selectProjectSkillRow(t, m, filepath.Join(proj, ".agents/skills/golang-code-style"), "enlace simbólico")
	if strings.Contains(linkedScreen, text.TUIActionRemove) {
		t.Fatalf("project symlink row wrongly offered remove:\n%s", linkedScreen)
	}
	beforeHome := mc.hash()
	pressRun(m, "e")
	if m.InPlanForTest() || mc.hash() != beforeHome {
		t.Fatalf("e on project symlink row opened plan or mutated home:\n%s", screen(m))
	}

	// Selecting the real folder row and pressing e opens preview showing both folder and symlink.
	m = openSized(t, newProjectTUIApp(t, mc, proj), goldenWidth, 40)
	selectProjectSkillRow(t, m, filepath.Join(proj, ".claude/skills/golang-code-style"), "Skill del proyecto actual (.claude/skills)")
	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on real project skill did not open plan; status=%q", m.StatusForTest())
	}
	confirmText := flat(screen(m))
	for _, want := range []string{
		flat(filepath.Join(proj, ".claude/skills/golang-code-style")),
		flat(filepath.Join(proj, ".agents/skills/golang-code-style")),
	} {
		if !strings.Contains(confirmText, want) {
			t.Errorf("TUI confirmation lacks %q:\n%s", want, screen(m))
		}
	}
	if _, err := os.Lstat(filepath.Join(proj, ".agents/skills/golang-code-style")); err != nil {
		t.Fatalf("preview mutated symlink on disk: %v", err)
	}

	// Confirming with s deletes both folder and symlink, and removes both rows from the list.
	pressRun(m, "s")
	if _, err := os.Lstat(filepath.Join(proj, ".claude/skills/golang-code-style")); !os.IsNotExist(err) {
		t.Fatalf("real folder still exists after confirm: err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(proj, ".agents/skills/golang-code-style")); !os.IsNotExist(err) {
		t.Fatalf("project symlink still exists after confirm: err=%v", err)
	}
	for _, line := range strings.Split(screen(m), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Listo:") {
			continue
		}
		if strings.Contains(trimmed, "golang-code-style") || strings.Contains(trimmed, "proyecto ·") {
			t.Errorf("deleted project skill/symlink row still shown in Skills: %q\n%s", trimmed, screen(m))
		}
	}

	// Undoing with z + s restores both the real folder and the symlink with exact target.
	pressRun(m, "z")
	if !m.InPlanForTest() {
		t.Fatalf("z did not open undo plan; status=%q", m.StatusForTest())
	}
	pressRun(m, "s")
	if got, err := treehash.Dir(filepath.Join(proj, ".claude/skills/golang-code-style")); err != nil || got.SHA != wantTreeHash.SHA {
		t.Fatalf("restored tree hash = %q err=%v, want %q", got.SHA, err, wantTreeHash.SHA)
	}
	if got, err := os.Readlink(filepath.Join(proj, ".agents/skills/golang-code-style")); err != nil || got != relTarget {
		t.Fatalf("restored symlink target = %q err=%v, want %q", got, err, relTarget)
	}
}
