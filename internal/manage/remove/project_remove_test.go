package remove_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

func (e *env) projectSetup(t *testing.T, projectDir string) (*engine.Engine, *remove.Remover) {
	t.Helper()
	p := e.h.Paths()
	ops := fsadapter.NewProjectOps(p.Guard())
	eng := engine.New(engine.Deps{
		Paths:      p,
		Files:      e.files,
		Links:      fsadapter.NewLinkOps(p.Guard()),
		Lock:       e.lock,
		DropLock:   e.lock,
		State:      e.state,
		Dirs:       fsadapter.NewScanner(p.Guard()),
		Clock:      e.clock,
		ProjectDir: projectDir,
		ProjectFS:  ops,
	})
	harnesses, err := data.LoadHarnesses(p, e.files)
	must(t, err)
	profiles, err := data.DiagnosticProfiles()
	must(t, err)
	rem := remove.New(remove.Deps{
		Paths:      p,
		Planner:    eng,
		Lock:       e.lock,
		State:      e.state,
		Observe:    e.observe,
		Harnesses:  harnesses,
		Profiles:   profiles,
		Files:      e.files,
		Dirs:       fsadapter.NewScanner(p.Guard()),
		Links:      osLinks{},
		ProjectDir: projectDir,
		Project:    ops,
	})
	return eng, rem
}

type stubCrossDevInspector struct {
	*fsadapter.ProjectOps
}

func (s stubCrossDevInspector) SameDevice(_, _, _ string) (bool, error) {
	return false, nil
}

func TestRemoveExactProjectSkillWithGlobalAndSiblingHomonymsAndUndo(t *testing.T) {
	e := newEnv(t)
	e.skill("shared")
	e.addEntry("shared", "owner/shared", "skills/shared/SKILL.md", e.treeSHA(".agents/skills/shared"))

	parent := filepath.Dir(e.h.Root())
	proj := filepath.Join(parent, "my-app")
	agentsShared := filepath.Join(proj, ".agents", "skills", "shared")
	claudeShared := filepath.Join(proj, ".claude", "skills", "shared")
	parentShared := filepath.Join(parent, ".agents", "skills", "shared")
	must(t, os.MkdirAll(agentsShared, 0o755))
	must(t, os.WriteFile(filepath.Join(agentsShared, "SKILL.md"), []byte("---\nname: shared\n---\nproject agents\n"), 0o644))
	must(t, os.MkdirAll(claudeShared, 0o755))
	must(t, os.WriteFile(filepath.Join(claudeShared, "SKILL.md"), []byte("---\nname: shared\n---\nproject claude\n"), 0o644))
	must(t, os.MkdirAll(parentShared, 0o755))
	must(t, os.WriteFile(filepath.Join(parentShared, "SKILL.md"), []byte("---\nname: shared\n---\nparent dir\n"), 0o644))

	beforeGlobal, err := treehash.Dir(e.h.Path(".agents/skills/shared"))
	must(t, err)
	beforeAgents, err := treehash.Dir(agentsShared)
	must(t, err)
	beforeClaude, err := treehash.Dir(claudeShared)
	must(t, err)
	beforeParent, err := treehash.Dir(parentShared)
	must(t, err)
	beforeWorld := e.everything()

	eng, rem := e.projectSetup(t, proj)

	// Bare "shared" is ambiguous between global store copy, project:.agents, and project:.claude.
	if _, err := rem.Remove("shared"); !errors.Is(err, remove.ErrAmbiguous) {
		t.Fatalf("Remove(shared) = %v, want ErrAmbiguous", err)
	}

	// Removing project:.agents/skills/shared touches only proj/.agents/skills/shared.
	plan, err := rem.Remove("project:.agents/skills/shared")
	must(t, err)
	if !plan.Preview.Project || plan.Preview.ProjectRoot != ".agents/skills" || plan.Preview.Folder != agentsShared {
		t.Fatalf("unexpected project preview: %+v", plan.Preview)
	}
	if plan.Plan.Project == nil || plan.Plan.Project.ProjectDir != proj || plan.Plan.Project.SkillName != "shared" {
		t.Fatalf("plan not bound to exact project skill: %+v", plan.Plan.Project)
	}
	if len(plan.Preview.Homonyms) < 2 {
		t.Errorf("expected homonyms (global + project .claude) in preview, got %+v", plan.Preview.Homonyms)
	}
	e.sameAs(beforeWorld, "planning project removal")

	out, err := eng.Apply(t.Context(), plan.Plan)
	must(t, err)

	if _, err := os.Lstat(agentsShared); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proj/.agents/skills/shared still exists: %v", err)
	}
	for _, untouched := range []struct {
		path string
		want string
	}{
		{e.h.Path(".agents/skills/shared"), beforeGlobal.SHA},
		{claudeShared, beforeClaude.SHA},
		{parentShared, beforeParent.SHA},
	} {
		got, err := treehash.Dir(untouched.path)
		if err != nil || got.SHA != untouched.want {
			t.Fatalf("untouched copy %s modified: err=%v got=%v want=%s", untouched.path, err, got, untouched.want)
		}
	}

	// Undo restores proj/.agents/skills/shared byte for byte without altering global/claude/parent.
	_, err = eng.Undo(t.Context(), out.SnapshotID)
	must(t, err)
	restored, err := treehash.Dir(agentsShared)
	must(t, err)
	if restored.SHA != beforeAgents.SHA {
		t.Fatalf("restored SHA = %s, want %s", restored.SHA, beforeAgents.SHA)
	}
}

func TestRemoveProjectRefusalsAndBoundaries(t *testing.T) {
	e := newEnv(t)
	parent := filepath.Dir(e.h.Root())
	proj := filepath.Join(parent, "proj")
	secret := filepath.Join(parent, "external-secret")
	must(t, os.MkdirAll(filepath.Join(secret), 0o755))
	must(t, os.WriteFile(filepath.Join(secret, "SKILL.md"), []byte("secret\n"), 0o644))
	beforeSecret, err := treehash.Dir(secret)
	must(t, err)

	must(t, os.MkdirAll(filepath.Join(proj, ".agents", "skills", "git-skill", ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(proj, ".agents", "skills", "git-skill", "SKILL.md"), []byte("git\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(proj, ".agents", "skills", "git-skill", ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	must(t, os.Symlink(secret, filepath.Join(proj, ".agents", "skills", "sym-skill")))
	must(t, os.MkdirAll(filepath.Join(proj, ".claude", "skills", "only-proj"), 0o755))
	must(t, os.WriteFile(filepath.Join(proj, ".claude", "skills", "only-proj", "SKILL.md"), []byte("only\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(proj, ".codex", "skills", "out-of-scope"), 0o755))
	must(t, os.WriteFile(filepath.Join(proj, ".codex", "skills", "out-of-scope", "SKILL.md"), []byte("codex\n"), 0o644))

	_, rem := e.projectSetup(t, proj)
	_, noProjRem := e.projectSetup(t, "")

	// No project configured -> ErrNoProject.
	if _, err := noProjRem.Remove("project:.claude/skills/only-proj"); !errors.Is(err, remove.ErrNoProject) {
		t.Errorf("Remove without project = %v, want ErrNoProject", err)
	}
	// Unsupported project roots or traversal -> ErrInvalidKey.
	for _, bad := range []string{"project:.codex/skills/out-of-scope", "project:.agents/skills/a/b", "project:../evil", "project:.agents/skills/.git"} {
		if _, err := rem.Remove(bad); !errors.Is(err, remove.ErrInvalidKey) {
			t.Errorf("Remove(%q) = %v, want ErrInvalidKey", bad, err)
		}
	}
	// Symlink and .git rejections.
	if _, err := rem.Remove("project:.agents/skills/sym-skill"); !errors.Is(err, remove.ErrProjectLinked) {
		t.Errorf("Remove(sym-skill) = %v, want ErrProjectLinked", err)
	}
	if _, err := rem.Remove("project:.agents/skills/git-skill"); !errors.Is(err, engine.ErrUnsupportedTree) {
		t.Errorf("Remove(git-skill) = %v, want ErrUnsupportedTree", err)
	}
	// Unambiguous bare name when only in current project resolves to that project skill.
	pOnly, err := rem.Remove("only-proj")
	must(t, err)
	if !pOnly.Preview.Project || pOnly.Preview.Key != "project:.claude/skills/only-proj" {
		t.Fatalf("bare project resolution = %+v", pOnly.Preview)
	}
	// Cross-device refusal.
	p := e.h.Paths()
	harnesses, err := data.LoadHarnesses(p, e.files)
	must(t, err)
	profiles, err := data.DiagnosticProfiles()
	must(t, err)
	crossRem := remove.New(remove.Deps{
		Paths: p, Planner: e.eng, Lock: e.lock, State: e.state,
		Observe:   func() (graph.Graph, error) { return e.observe() },
		Harnesses: harnesses, Profiles: profiles, Files: e.files,
		Dirs: fsadapter.NewScanner(p.Guard()), Links: osLinks{},
		ProjectDir: proj, Project: stubCrossDevInspector{fsadapter.NewProjectOps(p.Guard())},
	})
	if _, err := crossRem.Remove("project:.claude/skills/only-proj"); !errors.Is(err, ports.ErrCrossDevice) {
		t.Errorf("Remove(cross-device) = %v, want ErrCrossDevice", err)
	}

	// External symlink target stays completely intact.
	afterSecret, err := treehash.Dir(secret)
	must(t, err)
	if afterSecret.SHA != beforeSecret.SHA {
		t.Fatalf("symlink target mutated: %s -> %s", beforeSecret.SHA, afterSecret.SHA)
	}
}

func TestRemoveProjectSkillRemovesPointingSymlinksAndRestoresOnUndo(t *testing.T) {
	e := newEnv(t)
	parent := filepath.Dir(e.h.Root())
	proj := filepath.Join(parent, "proj-symlinks")
	claudeSkill := filepath.Join(proj, ".claude", "skills", "golang-code-style")
	otherSkill := filepath.Join(proj, ".claude", "skills", "other-skill")
	extSkill := filepath.Join(parent, "external-skill")
	must(t, os.MkdirAll(claudeSkill, 0o755))
	must(t, os.WriteFile(filepath.Join(claudeSkill, "SKILL.md"), []byte("---\nname: golang-code-style\n---\n"), 0o644))
	must(t, os.MkdirAll(otherSkill, 0o755))
	must(t, os.WriteFile(filepath.Join(otherSkill, "SKILL.md"), []byte("---\nname: other-skill\n---\n"), 0o644))
	must(t, os.MkdirAll(extSkill, 0o755))
	must(t, os.WriteFile(filepath.Join(extSkill, "SKILL.md"), []byte("---\nname: external-skill\n---\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(proj, ".agents", "skills"), 0o755))

	const relTarget = "../../.claude/skills/golang-code-style"
	agentsLink := filepath.Join(proj, ".agents", "skills", "golang-code-style")
	aliasLink := filepath.Join(proj, ".agents", "skills", "style-alias")
	otherLink := filepath.Join(proj, ".agents", "skills", "other-skill")
	extLink := filepath.Join(proj, ".agents", "skills", "external-skill")
	must(t, os.Symlink(relTarget, agentsLink))
	must(t, os.Symlink(relTarget, aliasLink))
	must(t, os.Symlink("../../.claude/skills/other-skill", otherLink))
	must(t, os.Symlink(extSkill, extLink))

	beforeReal, err := treehash.Dir(claudeSkill)
	must(t, err)

	eng, rem := e.projectSetup(t, proj)

	// Selecting the symlink directly must still be rejected with ErrProjectLinked.
	if _, err := rem.Remove("project:.agents/skills/golang-code-style"); !errors.Is(err, remove.ErrProjectLinked) {
		t.Fatalf("Remove(direct link) = %v, want ErrProjectLinked", err)
	}

	// Removing the real folder plans StepUnlink for pointing links before StepRemoveTree.
	plan, err := rem.Remove("project:.claude/skills/golang-code-style")
	must(t, err)
	if len(plan.Preview.Links) != 2 {
		t.Fatalf("Preview.Links = %+v, want 2 links", plan.Preview.Links)
	}
	if len(plan.Plan.Steps) != 3 ||
		plan.Plan.Steps[0].Kind != engine.StepUnlink ||
		plan.Plan.Steps[1].Kind != engine.StepUnlink ||
		plan.Plan.Steps[2].Kind != engine.StepRemoveTree {
		t.Fatalf("unexpected plan steps: %+v", plan.Plan.Steps)
	}

	out, err := eng.Apply(t.Context(), plan.Plan)
	must(t, err)

	for _, gone := range []string{claudeSkill, agentsLink, aliasLink} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists after Apply: %v", gone, err)
		}
	}
	// Unrelated links must remain untouched.
	if got, err := os.Readlink(otherLink); err != nil || got != "../../.claude/skills/other-skill" {
		t.Fatalf("otherLink mutated: target=%q err=%v", got, err)
	}
	if got, err := os.Readlink(extLink); err != nil || got != extSkill {
		t.Fatalf("extLink mutated: target=%q err=%v", got, err)
	}

	// Undo restores the real folder and both symlinks with their exact original targets.
	_, err = eng.Undo(t.Context(), out.SnapshotID)
	must(t, err)
	restoredReal, err := treehash.Dir(claudeSkill)
	must(t, err)
	if restoredReal.SHA != beforeReal.SHA {
		t.Fatalf("restored SHA = %s, want %s", restoredReal.SHA, beforeReal.SHA)
	}
	for _, l := range []string{agentsLink, aliasLink} {
		got, err := os.Readlink(l)
		must(t, err)
		if got != relTarget {
			t.Fatalf("restored link %s = %q, want %q", l, got, relTarget)
		}
	}

	// Inverse direction: removing .agents/skills/foo removes .claude/skills/foo -> ../../.agents/skills/foo,
	// while preserving any real homonym in .claude/skills/homonym-real.
	agentsFoo := filepath.Join(proj, ".agents", "skills", "foo")
	must(t, os.MkdirAll(agentsFoo, 0o755))
	must(t, os.WriteFile(filepath.Join(agentsFoo, "SKILL.md"), []byte("---\nname: foo\n---\n"), 0o644))
	const invTarget = "../../.agents/skills/foo"
	claudeFooLink := filepath.Join(proj, ".claude", "skills", "foo")
	must(t, os.Symlink(invTarget, claudeFooLink))

	invPlan, err := rem.Remove("project:.agents/skills/foo")
	must(t, err)
	if len(invPlan.Preview.Links) != 1 || invPlan.Preview.Links[0].Path != claudeFooLink || invPlan.Preview.Links[0].Target != invTarget {
		t.Fatalf("inverse Preview.Links = %+v, want 1 link to %s", invPlan.Preview.Links, claudeFooLink)
	}
	invOut, err := eng.Apply(t.Context(), invPlan.Plan)
	must(t, err)
	if _, err := os.Lstat(claudeFooLink); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inverse link still exists after Apply: %v", err)
	}
	if _, err := os.Lstat(agentsFoo); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agentsFoo still exists after Apply: %v", err)
	}
	_, err = eng.Undo(t.Context(), invOut.SnapshotID)
	must(t, err)
	if got, err := os.Readlink(claudeFooLink); err != nil || got != invTarget {
		t.Fatalf("restored inverse link = %q (err=%v), want %q", got, err, invTarget)
	}
}
