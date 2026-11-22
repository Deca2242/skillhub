package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
)

// replacement is one of the in-place replacements of docs/design-v2.md
// section 8: a first step leaves a path absent and a second step puts
// something new there.
type replacement struct {
	// prepare plants what the replacement starts from and returns the path
	// replaced.
	prepare func(t *testing.T, e *env, fp fixturePaths) string
	// steps adds the two steps.
	steps func(b *engine.Builder, e *env, fp fixturePaths, path string) error
}

func replacements() map[string]replacement {
	return map[string]replacement{
		"repoint a link": {
			prepare: func(t *testing.T, e *env, fp fixturePaths) string { return fp.link },
			steps: func(b *engine.Builder, e *env, fp fixturePaths, path string) error {
				return errors.Join(b.Unlink(path, fp.linkTarget), b.Link(path, "../../.agents/skills/beta"))
			},
		},
		"replace a copy with a link": {
			prepare: func(t *testing.T, e *env, fp fixturePaths) string {
				e.home.WriteFile(".claude/skills/beta/SKILL.md", "---\nname: beta\n---\nBeta.\n")
				return e.home.Path(".claude/skills/beta")
			},
			steps: func(b *engine.Builder, e *env, fp fixturePaths, path string) error {
				return errors.Join(b.RemoveTree(path), b.Link(path, "../../.agents/skills/beta"))
			},
		},
		"adopt a copy": {
			prepare: func(t *testing.T, e *env, fp fixturePaths) string {
				e.home.WriteFile(".claude/skills/delta/SKILL.md", "---\nname: delta\n---\nDelta.\n")
				e.home.WriteFileMode(".claude/skills/delta/run.sh", "#!/bin/sh\n", 0o755)
				return e.home.Path(".claude/skills/delta")
			},
			steps: func(b *engine.Builder, e *env, fp fixturePaths, path string) error {
				return errors.Join(b.Move(path, e.home.Path(".agents/skills/delta")), b.Link(path, "../../.agents/skills/delta"))
			},
		},
	}
}

func replacementPlan(t *testing.T, r replacement) (*env, engine.Plan, string) {
	t.Helper()
	e, fp := newEnv(t)
	path := r.prepare(t, e, fp)
	b := e.engine().NewBuilder(engine.Meta{Action: "repair", Targets: []string{"claude-code"}})
	must(t, r.steps(b, e, fp, path))
	plan, err := b.Build()
	must(t, err)
	return e, plan, path
}

// replacementScenarios adds the three replacements and their undos to the
// crash and failure matrices.
func replacementScenarios(out map[string]scenario) {
	for name, r := range replacements() {
		out[name] = scenario{closeAction: "applied", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, plan, _ := replacementPlan(t, r)
			return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }
		}}
		out["undo of "+name] = scenario{closeAction: "undone", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, plan, _ := replacementPlan(t, r)
			res, err := e.clean().Apply(t.Context(), plan)
			must(t, err)
			return e, func(eng *engine.Engine) error { _, err := eng.Undo(t.Context(), res.SnapshotID); return err }
		}}
	}
}

// TestAReplacementInPlaceAppliesAndUndoesByteForByte applies each
// replacement, checks the path holds the new link, and undoes it.
func TestAReplacementInPlaceAppliesAndUndoesByteForByte(t *testing.T) {
	for name, r := range replacements() {
		t.Run(name, func(t *testing.T) {
			e, plan, path := replacementPlan(t, r)
			noOwnLevels(t, e)
			before := observeWorld(t, e)
			out, err := e.engine().Apply(t.Context(), plan)
			must(t, err)
			if target, err := os.Readlink(path); err != nil || !strings.HasPrefix(target, "../../.agents/skills/") {
				t.Errorf("%s is not the new link: %q, %v", path, target, err)
			}
			_, err = e.engine().Undo(t.Context(), out.SnapshotID)
			must(t, err)
			assertWorld(t, "after the undo", observeWorld(t, e), before)
		})
	}
}

// TestForeignStateBetweenTheTwoStepsIsNeverOverwritten plants a file where
// the first step left the path absent: the second step must stop without
// touching it, Recover must refuse while it is there, and once it is gone
// Recover brings everything back.
func TestForeignStateBetweenTheTwoStepsIsNeverOverwritten(t *testing.T) {
	for name, r := range replacements() {
		t.Run(name, func(t *testing.T) {
			e, plan, path := replacementPlan(t, r)
			before := observeWorld(t, e)
			records := 0
			e.files.afterWrite = func(_ int, line string) {
				if strings.HasPrefix(line, "WriteFileAtomic") && strings.HasSuffix(line, "operation-v2.json") {
					records++
					if records == 2 { // the first step's progress
						must(t, os.WriteFile(path, []byte("theirs\n"), 0o644))
					}
				}
			}
			_, err := e.engine().Apply(t.Context(), plan)
			var fe *engine.ForeignStateError
			if !errors.As(err, &fe) || fe.Step != 1 {
				t.Fatalf("error = %v, want foreign state at step 1", err)
			}
			e.files.afterWrite = nil
			stopped := observeWorld(t, e)
			if _, err := e.clean().Recover(t.Context()); !errors.Is(err, engine.ErrForeignState) {
				t.Fatalf("recover: error = %v, want ErrForeignState", err)
			}
			assertWorld(t, "recover over foreign state", observeWorld(t, e), stopped)
			if data, err := os.ReadFile(path); err != nil || string(data) != "theirs\n" {
				t.Errorf("their file was touched: %q, %v", data, err)
			}
			must(t, os.Remove(path))
			if _, err := e.clean().Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertWorld(t, "recover once it is gone", observeWorld(t, e), before)
			if _, err := os.Lstat(filepath.Dir(path)); err != nil {
				t.Error(err)
			}
		})
	}
}
