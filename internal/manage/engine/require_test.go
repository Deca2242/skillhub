package engine_test

import (
	"errors"
	"os"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/treehash"
)

// requireTree is the image of the real folder at path as it is now.
func requireTree(t *testing.T, path string) engine.Image {
	t.Helper()
	tree, err := treehash.Dir(path)
	must(t, err)
	return engine.Image{Path: path, State: engine.StateTree, TreeSHA: tree.SHA}
}

// requirePlan keeps beta, the canonical folder, as a precondition and
// removes alpha and its link, as an adoption that removes a copy does.
func requirePlan(t *testing.T, e *env, fp fixturePaths) engine.Plan {
	t.Helper()
	b := e.engine().NewBuilder(engine.Meta{Action: "repair-adopt", Canonical: fp.beta})
	must(t, b.Require(requireTree(t, fp.beta)))
	must(t, b.Require(engine.Image{Path: fp.link, State: engine.StateLink, Target: fp.linkTarget}))
	must(t, b.RemoveTree(fp.alpha))
	plan, err := b.Build()
	must(t, err)
	return plan
}

// requireScenarios adds the plan with check-only steps, and its undo, to the
// crash and failure matrices.
func requireScenarios(out map[string]scenario) {
	out["require"] = scenario{closeAction: "applied", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
		e, fp := newEnv(t)
		plan := requirePlan(t, e, fp)
		return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }
	}}
	out["undo of require"] = scenario{closeAction: "undone", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
		e, fp := newEnv(t)
		res, err := e.clean().Apply(t.Context(), requirePlan(t, e, fp))
		must(t, err)
		return e, func(eng *engine.Engine) error { _, err := eng.Undo(t.Context(), res.SnapshotID); return err }
	}}
}

// TestRequireRefusesAChangedObjectBeforeAnythingRuns: a required folder that
// changed or is gone, or a required link that leads elsewhere, refuses the
// plan before the record is written; nothing changes.
func TestRequireRefusesAChangedObjectBeforeAnythingRuns(t *testing.T) {
	changes := map[string]func(t *testing.T, e *env, fp fixturePaths){
		"the folder changed": func(t *testing.T, e *env, fp fixturePaths) {
			e.home.WriteFile(".agents/skills/beta/SKILL.md", "---\nname: beta\n---\nUpdated.\n")
		},
		"the folder is gone": func(t *testing.T, e *env, fp fixturePaths) { must(t, os.RemoveAll(fp.beta)) },
		"the link leads elsewhere": func(t *testing.T, e *env, fp fixturePaths) {
			must(t, os.Remove(fp.link))
			e.home.Symlink(".claude/skills/alpha", "../../.agents/skills/beta")
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			e, fp := newEnv(t)
			plan := requirePlan(t, e, fp)
			change(t, e, fp)
			before := observeWorld(t, e)
			if _, err := e.engine().Apply(t.Context(), plan); !errors.Is(err, engine.ErrPrecondition) {
				t.Fatalf("Apply() error = %v, want ErrPrecondition", err)
			}
			assertWorld(t, "refused plan", observeWorld(t, e), before)
			if recordExists(t, e.p) {
				t.Error("a refused plan left a record")
			}
		})
	}
}

// TestRequireRefusesAWrongImageAtBuild: a required image that is not what
// the disk shows refuses the step.
func TestRequireRefusesAWrongImageAtBuild(t *testing.T) {
	e, fp := newEnv(t)
	b := e.engine().NewBuilder(engine.Meta{Action: "test"})
	wrong := requireTree(t, fp.alpha)
	wrong.Path = fp.beta
	if err := b.Require(wrong); !errors.Is(err, engine.ErrPrecondition) {
		t.Errorf("Require() error = %v, want ErrPrecondition", err)
	}
	if _, err := b.Build(); !errors.Is(err, engine.ErrPrecondition) {
		t.Errorf("Build() error = %v, want ErrPrecondition", err)
	}
	b = e.engine().NewBuilder(engine.Meta{Action: "test"})
	if err := b.Require(engine.Image{Path: "agents/skills/alpha", State: engine.StateTree, TreeSHA: wrong.TreeSHA}); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Require(relative path) error = %v, want ErrInvalidStep", err)
	}
	b = e.engine().NewBuilder(engine.Meta{Action: "test"})
	if err := b.Require(engine.Image{Path: fp.alpha, State: engine.StateAbsent}); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Require(absent) error = %v, want ErrInvalidStep", err)
	}
}

// TestRequireChangesNothingAndUndoes: the required objects stay as they
// were, the snapshot keeps no copy of them, and undo restores only what the
// plan changed, even when the required folder changed afterwards.
func TestRequireChangesNothingAndUndoes(t *testing.T) {
	e, fp := newEnv(t)
	plan := requirePlan(t, e, fp)
	if plan.Steps[0].Kind != engine.StepRequire || len(plan.Steps) != 3 {
		t.Fatalf("Steps = %+v", plan.Steps)
	}
	noOwnLevels(t, e)
	before := observeWorld(t, e)
	out, err := e.engine().Apply(t.Context(), plan)
	must(t, err)
	if _, err := os.Lstat(fp.alpha); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("alpha is still there: %v", err)
	}
	if target, err := os.Readlink(fp.link); err != nil || target != fp.linkTarget {
		t.Errorf("the required link changed: %q, %v", target, err)
	}
	snap, err := e.engine().ReadSnapshot(out.SnapshotID)
	must(t, err)
	for _, tc := range snap.Trees {
		if tc.Path == fp.beta {
			t.Error("the snapshot copies a folder the plan only requires")
		}
	}
	_, err = e.engine().Undo(t.Context(), out.SnapshotID)
	must(t, err)
	assertWorld(t, "after the undo", observeWorld(t, e), before)
}

// TestRequireUndoIgnoresALaterChangeOfTheRequiredObject: the required
// folder is not the operation's to undo.
func TestRequireUndoIgnoresALaterChangeOfTheRequiredObject(t *testing.T) {
	e, fp := newEnv(t)
	out, err := e.engine().Apply(t.Context(), requirePlan(t, e, fp))
	must(t, err)
	e.home.WriteFile(".agents/skills/beta/SKILL.md", "---\nname: beta\n---\nUpdated.\n")
	if _, err := e.engine().Undo(t.Context(), out.SnapshotID); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	if _, err := os.Lstat(fp.alpha); err != nil {
		t.Errorf("alpha is not back: %v", err)
	}
	if data, err := os.ReadFile(e.home.Path(".agents/skills/beta/SKILL.md")); err != nil || string(data) != "---\nname: beta\n---\nUpdated.\n" {
		t.Errorf("undo touched the required folder: %q, %v", data, err)
	}
}

// TestRequireTamperedStepIsRefused: a require step whose postimage differs
// from its preimage is invalid, even with its identifier recomputed.
func TestRequireTamperedStepIsRefused(t *testing.T) {
	e, fp := newEnv(t)
	plan := requirePlan(t, e, fp)
	plan.Steps[0].Post[0].TreeSHA = requireTree(t, fp.alpha).TreeSHA
	id, err := plan.ComputeID()
	must(t, err)
	plan.ID = id
	if err := e.engine().Check(plan); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Check() error = %v, want ErrInvalidStep", err)
	}
	plan = requirePlan(t, e, fp)
	plan.Steps[0].Target = "x"
	if id, err = plan.ComputeID(); err == nil {
		plan.ID = id
	}
	if err := e.engine().Check(plan); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Check() with a stray field error = %v, want ErrInvalidStep", err)
	}
}
