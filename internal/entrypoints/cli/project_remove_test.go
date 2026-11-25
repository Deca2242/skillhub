package cli_test

import (
	"bytes"
	"io"
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
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
)

func runWithProject(t *testing.T, m *machine, projectDir, stdin string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, cli.Env{
		Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
		Interactive: m.interactive, Executable: "/opt/skillhub/bin/skillhub",
		NewApp: func() (*app.App, error) {
			files := fsadapter.NewFileOps(m.p.Guard())
			lock := skillslock.New(m.p, files)
			agents, err := data.LoadAgents(m.p, files)
			if err != nil {
				return nil, err
			}
			return app.New(app.Deps{
				Paths:       m.p,
				Agents:      agents,
				Files:       files,
				Dirs:        fsadapter.NewScanner(m.p.Guard()),
				Lock:        lock,
				ManagedLock: lock,
				Links:       fsadapter.NewLinkOps(m.p.Guard()),
				State:       statefile.New(m.p),
				Settings:    settings.New(m.p),
				GitHub:      m.gh,
				Git:         m.git,
				Notifier:    fakes.NewNotifier(),
				Background:  fakes.NewBackgroundService(),
				Clock:       m.clock,
				ProcDir:     m.proc,
				ProjectDir:  projectDir,
				Project:     fsadapter.NewProjectOps(m.p.Guard()),
			})
		},
		TUI: func(io.Writer, io.Writer) int { return 0 },
	})
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func writeProjectSkillFile(t *testing.T, root, rel, content string) {
	t.Helper()
	dst := filepath.Join(root, filepath.FromSlash(rel))
	must(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	must(t, os.WriteFile(dst, []byte(content), 0o644))
}

func realTempDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	realDir, err := filepath.EvalSymlinks(d)
	if err != nil {
		return d
	}
	return realDir
}

func TestCLIProjectRemoveAndUndoIsolatesGlobalAndOtherProjects(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".claude/skills/solo/SKILL.md", graphtest.SkillMD("solo", "global copy"))
	m.h.WriteFile(".claude/skills/solo/notes.txt", "global notes\n")

	projA := realTempDir(t)
	projB := realTempDir(t)
	writeProjectSkillFile(t, projA, ".agents/skills/solo/SKILL.md", graphtest.SkillMD("solo", "project agents copy"))
	writeProjectSkillFile(t, projA, ".agents/skills/solo/helper.sh", "#!/bin/sh\necho agents\n")
	writeProjectSkillFile(t, projA, ".claude/skills/solo/SKILL.md", graphtest.SkillMD("solo", "project claude copy"))
	writeProjectSkillFile(t, projB, ".claude/skills/solo/SKILL.md", graphtest.SkillMD("solo", "project B copy"))

	wantAgentsProjHash, err := treehash.Dir(filepath.Join(projA, ".agents/skills/solo"))
	must(t, err)
	wantGlobalHash, err := treehash.Dir(m.h.Path(".claude/skills/solo"))
	must(t, err)
	wantClaudeProjHash, err := treehash.Dir(filepath.Join(projA, ".claude/skills/solo"))
	must(t, err)
	wantProjBHash, err := treehash.Dir(filepath.Join(projB, ".claude/skills/solo"))
	must(t, err)
	lockBefore, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)

	shown := runWithProject(t, m, projA, "", "skills", "remove", "project:.agents/skills/solo", "--dry-run")
	if shown.code != 0 {
		t.Fatalf("dry-run exit %d\nstdout: %s\nstderr: %s", shown.code, shown.stdout, shown.stderr)
	}
	for _, want := range []string{
		"Carpeta (proyecto (.agents/skills)): " + filepath.Join(projA, ".agents/skills/solo"),
		"--plan ",
	} {
		if !strings.Contains(shown.stdout, want) {
			t.Errorf("dry-run lacks %q:\n%s", want, shown.stdout)
		}
	}

	// Cancel interactively must not touch anything.
	m.interactive = true
	beforeHome := m.hash()
	cancelled := runWithProject(t, m, projA, "n\n", "skills", "remove", "project:.agents/skills/solo")
	if cancelled.code != 2 || !strings.Contains(cancelled.stderr, "no se confirmó") {
		t.Fatalf("cancelled = exit %d stderr=%q", cancelled.code, cancelled.stderr)
	}
	m.interactive = false
	if m.hash() != beforeHome {
		t.Fatal("cancelled project removal mutated home state")
	}
	if _, err := os.Stat(filepath.Join(projA, ".agents/skills/solo/SKILL.md")); err != nil {
		t.Fatalf("cancelled removal deleted project skill: %v", err)
	}

	// Approved removal removes ONLY projA/.agents/skills/solo.
	pid := planID(t, shown.stdout)
	removed := runWithProject(t, m, projA, "", "skills", "remove", "project:.agents/skills/solo", "--yes", "--plan", pid)
	if removed.code != 0 {
		t.Fatalf("remove exit %d\nstdout: %s\nstderr: %s", removed.code, removed.stdout, removed.stderr)
	}
	if _, err := os.Lstat(filepath.Join(projA, ".agents/skills/solo")); !os.IsNotExist(err) {
		t.Fatalf("projA/.agents/skills/solo still exists: err=%v", err)
	}
	if got, err := treehash.Dir(m.h.Path(".claude/skills/solo")); err != nil || got.SHA != wantGlobalHash.SHA {
		t.Fatalf("global copy modified: hash=%q err=%v", got.SHA, err)
	}
	if got, err := treehash.Dir(filepath.Join(projA, ".claude/skills/solo")); err != nil || got.SHA != wantClaudeProjHash.SHA {
		t.Fatalf("projA/.claude/skills/solo modified: hash=%q err=%v", got.SHA, err)
	}
	if got, err := treehash.Dir(filepath.Join(projB, ".claude/skills/solo")); err != nil || got.SHA != wantProjBHash.SHA {
		t.Fatalf("projB/.claude/skills/solo modified: hash=%q err=%v", got.SHA, err)
	}
	lockAfter, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	if string(lockAfter) != string(lockBefore) {
		t.Error("removing a project skill modified skills-lock.json")
	}

	snap := m.lastV2().SnapshotID
	if !strings.Contains(removed.stdout, "skillhub undo v2:remove --to "+snap) {
		t.Errorf("remove output lacks undo command:\n%s", removed.stdout)
	}

	// History lists the snapshot and undo from another project or no project refuses.
	hist := runWithProject(t, m, projB, "", "history")
	if hist.code != 0 || !strings.Contains(hist.stdout, snap) {
		t.Errorf("history output (%d) should show snapshot %q:\n%s", hist.code, snap, hist.stdout)
	}
	wrongUndo := runWithProject(t, m, projB, "", "undo", "v2:remove", "--to", snap, "--dry-run")
	if wrongUndo.code == 0 {
		t.Fatalf("undo from wrong project unexpectedly succeeded:\n%s", wrongUndo.stdout)
	}
	if !strings.Contains(wrongUndo.stderr, projA) {
		t.Errorf("wrong-project undo should name owning project %q:\n%s", projA, wrongUndo.stderr)
	}
	noProjUndo := runWithProject(t, m, "", "", "undo", "v2:remove", "--to", snap, "--dry-run")
	if noProjUndo.code == 0 {
		t.Fatalf("undo without project unexpectedly succeeded:\n%s", noProjUndo.stdout)
	}

	// Undo from projA restores projA/.agents/skills/solo identically without touching global or sibling copies.
	undoDry := runWithProject(t, m, projA, "", "undo", "v2:remove", "--to", snap, "--dry-run")
	if undoDry.code != 0 {
		t.Fatalf("undo dry-run from origin project failed (%d):\nstdout:%s\nstderr:%s", undoDry.code, undoDry.stdout, undoDry.stderr)
	}
	undone := runWithProject(t, m, projA, "", "undo", "v2:remove", "--to", snap, "--yes", "--plan", planID(t, undoDry.stdout))
	if undone.code != 0 {
		t.Fatalf("undo from origin project failed (%d):\nstdout:%s\nstderr:%s", undone.code, undone.stdout, undone.stderr)
	}
	if got, err := treehash.Dir(filepath.Join(projA, ".agents/skills/solo")); err != nil || got.SHA != wantAgentsProjHash.SHA {
		t.Fatalf("restored tree hash = %q err=%v, want %q", got.SHA, err, wantAgentsProjHash.SHA)
	}
	if got, err := treehash.Dir(m.h.Path(".claude/skills/solo")); err != nil || got.SHA != wantGlobalHash.SHA {
		t.Fatalf("undo altered global copy: hash=%q err=%v", got.SHA, err)
	}
	if got, err := treehash.Dir(filepath.Join(projA, ".claude/skills/solo")); err != nil || got.SHA != wantClaudeProjHash.SHA {
		t.Fatalf("undo altered projA/.claude/skills/solo: hash=%q err=%v", got.SHA, err)
	}
	if got, err := treehash.Dir(filepath.Join(projB, ".claude/skills/solo")); err != nil || got.SHA != wantProjBHash.SHA {
		t.Fatalf("undo altered projB/.claude/skills/solo: hash=%q err=%v", got.SHA, err)
	}

	// Global-only skill removal still works while inside a project.
	gPlan := runWithProject(t, m, projA, "", "skills", "remove", "dir:.claude/skills/humanizer", "--dry-run")
	if gPlan.code != 0 {
		t.Fatalf("global dry-run from project cwd failed (%d): %s", gPlan.code, gPlan.stderr)
	}
	gRem := runWithProject(t, m, projA, "", "skills", "remove", "dir:.claude/skills/humanizer", "--yes", "--plan", planID(t, gPlan.stdout))
	if gRem.code != 0 {
		t.Fatalf("global remove from project cwd failed (%d): %s", gRem.code, gRem.stderr)
	}
	if m.exists(".claude/skills/humanizer") {
		t.Fatal("global humanizer still exists after removal")
	}
	if got, err := treehash.Dir(filepath.Join(projA, ".agents/skills/solo")); err != nil || got.SHA != wantAgentsProjHash.SHA {
		t.Fatalf("global removal touched project skill: %v", err)
	}
}

func TestCLIProjectRemoveRefusesNonEligibleAndNoProject(t *testing.T) {
	m := newMachine(t)
	parent := realTempDir(t)
	proj := filepath.Join(parent, "app")
	outside := filepath.Join(parent, "outside-target")
	writeProjectSkillFile(t, parent, ".agents/skills/parent-only/SKILL.md", graphtest.SkillMD("parent-only", "parent"))
	writeProjectSkillFile(t, outside, "SKILL.md", graphtest.SkillMD("linked-proj", "outside target"))
	must(t, os.MkdirAll(filepath.Join(proj, ".claude/skills"), 0o755))
	must(t, os.Symlink(outside, filepath.Join(proj, ".claude/skills/linked-proj")))
	writeProjectSkillFile(t, proj, ".agents/skills/git-proj/SKILL.md", graphtest.SkillMD("git-proj", "git backed"))
	must(t, os.MkdirAll(filepath.Join(proj, ".agents/skills/git-proj/.git"), 0o755))
	writeProjectSkillFile(t, proj, ".agents/skills/git-proj/.git/HEAD", "ref: refs/heads/main\n")

	linkedRes := runWithProject(t, m, proj, "", "skills", "remove", "project:.claude/skills/linked-proj", "--dry-run")
	if linkedRes.code == 0 {
		t.Fatalf("symlink project skill should be refused:\n%s", linkedRes.stdout)
	}
	if !strings.Contains(linkedRes.stderr, "enlace") {
		t.Errorf("symlink refusal stderr = %q", linkedRes.stderr)
	}
	if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); err != nil {
		t.Fatalf("outside symlink target was modified: %v", err)
	}

	gitRes := runWithProject(t, m, proj, "", "skills", "remove", "project:.agents/skills/git-proj", "--dry-run")
	if gitRes.code == 0 {
		t.Fatalf("git-backed project skill should be refused:\n%s", gitRes.stdout)
	}
	if _, err := os.Stat(filepath.Join(proj, ".agents/skills/git-proj/.git/HEAD")); err != nil {
		t.Fatalf(".git directory was modified: %v", err)
	}

	parentRes := runWithProject(t, m, proj, "", "skills", "remove", "project:.agents/skills/parent-only", "--dry-run")
	if parentRes.code == 0 {
		t.Fatalf("parent project skill should not be reachable:\n%s", parentRes.stdout)
	}

	noProjRes := runWithProject(t, m, "", "", "skills", "remove", "project:.agents/skills/git-proj", "--dry-run")
	if noProjRes.code == 0 {
		t.Fatalf("remove without project dir should be refused:\n%s", noProjRes.stdout)
	}

	// Symlinked startup projectDir must refuse removal and leave target intact.
	symProj := filepath.Join(parent, "sym-proj-cwd")
	targetProj := filepath.Join(parent, "real-target-proj")
	writeProjectSkillFile(t, targetProj, ".agents/skills/solo/SKILL.md", graphtest.SkillMD("solo", "target"))
	must(t, os.Symlink(targetProj, symProj))
	symCwdRes := runWithProject(t, m, symProj, "", "skills", "remove", "project:.agents/skills/solo", "--dry-run")
	if symCwdRes.code == 0 || !strings.Contains(symCwdRes.stderr, "enlace") {
		t.Fatalf("symlinked startup cwd remove = exit %d stderr=%q, want refusal with enlace", symCwdRes.code, symCwdRes.stderr)
	}
	if _, err := os.Stat(filepath.Join(targetProj, ".agents/skills/solo/SKILL.md")); err != nil {
		t.Fatalf("symlinked startup cwd touched target: %v", err)
	}

	// Symlinked .agents/skills root must refuse removal and leave external target intact.
	projLinkedRoot := filepath.Join(parent, "proj-linked-root")
	must(t, os.MkdirAll(filepath.Join(projLinkedRoot, ".agents"), 0o755))
	must(t, os.Symlink(filepath.Join(targetProj, ".agents/skills"), filepath.Join(projLinkedRoot, ".agents/skills")))
	linkedRootRes := runWithProject(t, m, projLinkedRoot, "", "skills", "remove", "project:.agents/skills/solo", "--dry-run")
	if linkedRootRes.code == 0 || !strings.Contains(linkedRootRes.stderr, "enlace") {
		t.Fatalf("symlinked .agents/skills root remove = exit %d stderr=%q, want refusal with enlace", linkedRootRes.code, linkedRootRes.stderr)
	}
	if _, err := os.Stat(filepath.Join(targetProj, ".agents/skills/solo/SKILL.md")); err != nil {
		t.Fatalf("symlinked root touched target: %v", err)
	}
}

func TestCLIProjectPendingRecoverRequiresSameProject(t *testing.T) {
	m := newMachine(t)
	projA := realTempDir(t)
	projB := realTempDir(t)
	skillPath := filepath.Join(projA, ".agents", "skills", "solo")
	writeProjectSkillFile(t, projA, ".agents/skills/solo/SKILL.md", graphtest.SkillMD("solo", "projA pending"))
	wantHash, err := treehash.Dir(skillPath)
	must(t, err)

	files := fsadapter.NewFileOps(m.p.Guard())
	lock := skillslock.New(m.p, files)
	ops := fsadapter.NewProjectOps(m.p.Guard())
	eng := engine.New(engine.Deps{
		Paths: m.p, Files: files, Links: fsadapter.NewLinkOps(m.p.Guard()),
		Lock: lock, DropLock: lock, State: statefile.New(m.p),
		Dirs: fsadapter.NewScanner(m.p.Guard()), Clock: m.clock,
		ProjectDir: projA, ProjectFS: ops,
	})
	b := eng.NewBuilder(engine.Meta{
		Action:          "remove",
		Targets:         []string{},
		Canonical:       skillPath,
		ReviewedTreeSHA: wantHash.SHA,
		Project: &engine.ProjectScope{
			ProjectDir: projA,
			Root:       engine.ProjectRootAgents,
			SkillName:  "solo",
		},
	})
	must(t, b.RemoveTree(skillPath))
	plan, err := b.Build()
	must(t, err)
	run, err := eng.Begin(t.Context(), plan)
	must(t, err)
	must(t, run.Release())
	if _, err := os.Stat(engine.RecordFile(m.p)); err != nil {
		t.Fatalf("missing pending record: %v", err)
	}

	wrongProj := runWithProject(t, m, projB, "", "recover")
	if wrongProj.code == 0 || !strings.Contains(wrongProj.stderr, projA) {
		t.Fatalf("recover from wrong project = exit %d stderr=%q, want failure naming %q", wrongProj.code, wrongProj.stderr, projA)
	}
	noProj := runWithProject(t, m, "", "", "recover")
	if noProj.code == 0 || !strings.Contains(noProj.stderr, projA) {
		t.Fatalf("recover without project = exit %d stderr=%q, want failure naming %q", noProj.code, noProj.stderr, projA)
	}
	if _, err := os.Stat(engine.RecordFile(m.p)); err != nil {
		t.Fatalf("pending record disappeared after refused recover: %v", err)
	}

	rightProj := runWithProject(t, m, projA, "", "recover")
	if rightProj.code != 0 {
		t.Fatalf("recover from origin project failed (%d):\nstdout:%s\nstderr:%s", rightProj.code, rightProj.stdout, rightProj.stderr)
	}
	if _, err := os.Stat(engine.RecordFile(m.p)); !os.IsNotExist(err) {
		t.Fatalf("pending record still exists after recover: %v", err)
	}
	gotHash, err := treehash.Dir(skillPath)
	must(t, err)
	if gotHash.SHA != wantHash.SHA {
		t.Fatalf("recovered tree hash = %s, want %s", gotHash.SHA, wantHash.SHA)
	}
}
