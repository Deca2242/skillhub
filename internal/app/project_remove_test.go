package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
)

func newProjectTestApp(t *testing.T, mc *machine, projectDir string) *app.App {
	t.Helper()
	files := fsadapter.NewFileOps(mc.p.Guard())
	agents, err := data.LoadAgents(mc.p, files)
	must(t, err)
	lock := skillslock.New(mc.p, files)
	a, err := app.New(app.Deps{
		Paths:       mc.p,
		Agents:      agents,
		Files:       files,
		Dirs:        fsadapter.NewScanner(mc.p.Guard()),
		Lock:        lock,
		ManagedLock: lock,
		Links:       fsadapter.NewLinkOps(mc.p.Guard()),
		State:       mc.state,
		Settings:    mc.settings,
		GitHub:      mc.gh,
		Git:         mc.git,
		Notifier:    fakes.NewNotifier(),
		Background:  fakes.NewBackgroundService(),
		Clock:       mc.clock,
		ProcDir:     mc.proc,
		ProjectDir:  projectDir,
		Project:     fsadapter.NewProjectOps(mc.p.Guard()),
	})
	must(t, err)
	return a
}

func TestAppProjectDirNormalizationSkillsIsolationAndWrongProjectUndo(t *testing.T) {
	mc := newMachine(t)
	p := mc.p
	parent := filepath.Dir(mc.h.Root())
	projA := filepath.Join(parent, "proj-a")
	projB := filepath.Join(parent, "proj-b")
	siblingProj := filepath.Join(parent, "proj-sibling")
	must(t, os.MkdirAll(projB, 0o755))

	// Excluded directories never become an active project scope.
	for _, bad := range []string{"", "relative/dir", p.Home(), p.StoreDir(), filepath.Join(p.Home(), ".claude", "skills"), p.StateDir(), p.ConfigDir(), p.CacheDir()} {
		badApp := newProjectTestApp(t, mc, bad)
		if got := badApp.ProjectDir(); got != "" {
			t.Errorf("ProjectDir(%q) = %q, want empty", bad, got)
		}
		if rows, err := badApp.ProjectSkills(); err != nil || len(rows) != 0 {
			t.Errorf("ProjectSkills(%q) = %+v, err=%v, want empty", bad, rows, err)
		}
	}

	// Setup project A (.agents/skills/humanizer homonym with global ~/.claude/skills/humanizer,
	// .claude/skills/git-skill with .git, .agents/skills/sym-skill symlink, and parent/sibling homonyms).
	projAgentsHumanizer := filepath.Join(projA, ".agents", "skills", "humanizer")
	must(t, os.MkdirAll(projAgentsHumanizer, 0o755))
	must(t, os.WriteFile(filepath.Join(projAgentsHumanizer, "SKILL.md"), []byte("---\nname: humanizer\n---\nproject agents\n"), 0o644))

	must(t, os.MkdirAll(filepath.Join(projA, ".claude", "skills", "git-skill", ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(projA, ".claude", "skills", "git-skill", "SKILL.md"), []byte("git\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(projA, ".claude", "skills", "git-skill", ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))

	symTarget := filepath.Join(parent, "sym-target")
	must(t, os.MkdirAll(symTarget, 0o755))
	must(t, os.WriteFile(filepath.Join(symTarget, "SKILL.md"), []byte("sym\n"), 0o644))
	must(t, os.Symlink(symTarget, filepath.Join(projA, ".agents", "skills", "sym-skill")))

	must(t, os.MkdirAll(filepath.Join(parent, ".agents", "skills", "parent-only"), 0o755))
	must(t, os.WriteFile(filepath.Join(parent, ".agents", "skills", "parent-only", "SKILL.md"), []byte("parent\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(siblingProj, ".agents", "skills", "sibling-only"), 0o755))
	must(t, os.WriteFile(filepath.Join(siblingProj, ".agents", "skills", "sibling-only", "SKILL.md"), []byte("sibling\n"), 0o644))

	appA := newProjectTestApp(t, mc, projA)
	appB := newProjectTestApp(t, mc, projB)

	// ProjectSkills lists only direct entries under projA, with explicit reasons for ineligible ones.
	rows, err := appA.ProjectSkills()
	must(t, err)
	if len(rows) != 3 {
		t.Fatalf("ProjectSkills count = %d (%+v), want 3", len(rows), rows)
	}
	byID := map[string]app.ProjectSkillView{}
	for _, r := range rows {
		byID[r.ID] = r
		if r.Name == "parent-only" || r.Name == "sibling-only" {
			t.Fatalf("leaked non-project skill %+v", r)
		}
	}
	if !byID["project:.agents/skills/humanizer"].Removable {
		t.Errorf("humanizer Removable = false: %+v", byID["project:.agents/skills/humanizer"])
	}
	if sym := byID["project:.agents/skills/sym-skill"]; sym.Removable || !sym.Linked {
		t.Errorf("sym-skill = %+v, want Linked=true Removable=false", sym)
	}
	if g := byID["project:.claude/skills/git-skill"]; g.Removable || !g.HasGit {
		t.Errorf("git-skill = %+v, want HasGit=true Removable=false", g)
	}

	// Global Scan() remains unaffected by project skills.
	globalSkills, err := appA.Skills(t.Context())
	must(t, err)
	for _, s := range globalSkills {
		if s.Name == "git-skill" || s.Name == "sym-skill" {
			t.Fatalf("global Skills() included project skill: %+v", s)
		}
	}

	beforeGlobal, err := treehash.Dir(mc.h.Path(".claude/skills/humanizer"))
	must(t, err)
	beforeProj, err := treehash.Dir(projAgentsHumanizer)
	must(t, err)

	// Remove project:.agents/skills/humanizer -> removes ONLY project copy; global humanizer stays untouched.
	prop, err := appA.PlanRemove(t.Context(), "project:.agents/skills/humanizer")
	must(t, err)
	if prop.Remove == nil || !prop.Remove.Project || prop.Remove.ProjectDir != projA {
		t.Fatalf("proposal Remove = %+v", prop.Remove)
	}
	res, err := appA.ApplyPlan(t.Context(), prop.Request, prop.PlanID)
	must(t, err)
	if _, err := os.Lstat(projAgentsHumanizer); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project humanizer still exists: %v", err)
	}
	if gotGlobal, err := treehash.Dir(mc.h.Path(".claude/skills/humanizer")); err != nil || gotGlobal.SHA != beforeGlobal.SHA {
		t.Fatalf("global humanizer changed: err=%v got=%v", err, gotGlobal)
	}

	// OperationView records ProjectDir and exact relative label without colliding with global store keys.
	op, err := appA.Operation(t.Context(), res.SnapshotID)
	must(t, err)
	if op.ProjectDir != projA || op.Label != ".agents/skills/humanizer" {
		t.Errorf("OperationView = %+v, want ProjectDir=%q Label=.agents/skills/humanizer", op, projA)
	}

	// Undo from another project refuses with ErrWrongProject and touches nothing.
	var re *app.RefusalError
	if _, err := appB.PlanUndo(t.Context(), res.SnapshotID); !errors.Is(err, app.ErrWrongProject) || !errors.As(err, &re) || len(re.Related) == 0 || re.Related[0] != projA {
		t.Fatalf("appB.PlanUndo() = %v, want RefusalError(ErrWrongProject, %s)", err, projA)
	}

	// Undo from same project restores byte for byte.
	undoProp, err := appA.PlanUndo(t.Context(), res.SnapshotID)
	must(t, err)
	_, err = appA.UndoOperation(t.Context(), app.UndoLine{
		Target:     "v2:remove",
		SnapshotID: res.SnapshotID,
		PlanID:     undoProp.PlanID,
	})
	must(t, err)
	restoredProj, err := treehash.Dir(projAgentsHumanizer)
	must(t, err)
	if restoredProj.SHA != beforeProj.SHA {
		t.Fatalf("restored project SHA = %s, want %s", restoredProj.SHA, beforeProj.SHA)
	}
}

func TestAppProjectSkillsIgnoresSymlinkedStartupDirAndFlagsLinkedRoot(t *testing.T) {
	mc := newMachine(t)
	parent := filepath.Dir(mc.h.Root())
	realTarget := filepath.Join(parent, "real-target")
	must(t, os.MkdirAll(filepath.Join(realTarget, ".agents", "skills", "solo"), 0o755))
	must(t, os.WriteFile(filepath.Join(realTarget, ".agents", "skills", "solo", "SKILL.md"), []byte("---\nname: solo\n---\nsolo\n"), 0o644))
	beforeTarget, err := treehash.Dir(realTarget)
	must(t, err)

	symProj := filepath.Join(parent, "sym-proj")
	must(t, os.Symlink(realTarget, symProj))

	appSym := newProjectTestApp(t, mc, symProj)
	rows, err := appSym.ProjectSkills()
	if err != nil {
		t.Fatalf("ProjectSkills(symlinked cwd) error = %v, want nil", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ProjectSkills(symlinked cwd) = %+v, want empty", rows)
	}

	projWithLinkedRoot := filepath.Join(parent, "proj-linked-root")
	must(t, os.MkdirAll(filepath.Join(projWithLinkedRoot, ".agents"), 0o755))
	must(t, os.Symlink(filepath.Join(realTarget, ".agents", "skills"), filepath.Join(projWithLinkedRoot, ".agents", "skills")))

	appLinkedRoot := newProjectTestApp(t, mc, projWithLinkedRoot)
	rootRows, err := appLinkedRoot.ProjectSkills()
	must(t, err)
	if len(rootRows) != 1 || rootRows[0].ID != "project:.agents/skills" || !rootRows[0].Linked || rootRows[0].Removable {
		t.Fatalf("ProjectSkills(linked root) = %+v, want single non-removable linked root", rootRows)
	}

	afterTarget, err := treehash.Dir(realTarget)
	must(t, err)
	if afterTarget.SHA != beforeTarget.SHA {
		t.Fatalf("symlink target mutated: %s -> %s", beforeTarget.SHA, afterTarget.SHA)
	}
}
