package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
)

func TestUndoPutsEverythingBackByteForByte(t *testing.T) {
	e2, fp2 := newEnv(t)
	eng2 := e2.engine()
	plan := disableEffectPlan(t, e2, eng2, fp2)
	noOwnLevels(t, e2)
	start := observeWorld(t, e2) // alpha managed, as disable expects
	out, err := eng2.Apply(t.Context(), plan)
	must(t, err)

	undo, err := eng2.UndoPlan(out.SnapshotID)
	must(t, err)
	kinds := []engine.StepKind{}
	for _, s := range undo.Steps {
		kinds = append(kinds, s.Kind)
	}
	want := []engine.StepKind{engine.StepRestoreTree, engine.StepLockUnpark, engine.StepMove, engine.StepLink}
	if strings.Join(stepKinds(kinds), ",") != strings.Join(stepKinds(want), ",") || undo.Undoes != out.SnapshotID {
		t.Errorf("undo plan = %v undoing %q, want %v undoing %q", kinds, undo.Undoes, want, out.SnapshotID)
	}

	res, err := eng2.Undo(t.Context(), out.SnapshotID)
	must(t, err)
	assertWorld(t, "after undo", observeWorld(t, e2), start)
	j := journal(t, e2)
	if len(j) != 2 || j[1].Action != domain.ActionUndone || j[1].SnapshotID != res.SnapshotID ||
		!strings.Contains(j[1].Detail, out.SnapshotID) || j[1].Target != "v2:undo" {
		t.Errorf("journal = %+v", j)
	}
	if _, err := eng2.Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrNothingToUndo) {
		t.Errorf("a second undo: error = %v, want ErrNothingToUndo", err)
	}
	if _, err := eng2.Undo(t.Context(), "op-unknown"); !errors.Is(err, engine.ErrNothingToUndo) {
		t.Errorf("an unknown snapshot: error = %v, want ErrNothingToUndo", err)
	}
}

func stepKinds(k []engine.StepKind) []string {
	out := make([]string, len(k))
	for i := range k {
		out[i] = string(k[i])
	}
	return out
}

// TestUndoRefusesWhatChangedAfterTheOperation changes, after a disable, one
// thing the disable left; Undo must refuse without touching anything, and
// work again once the change is gone.
func TestUndoRefusesWhatChangedAfterTheOperation(t *testing.T) {
	cases := map[string]struct {
		change, revert func(t *testing.T, e *env, fp fixturePaths)
	}{
		"the parked folder was edited": {
			change: func(t *testing.T, e *env, fp fixturePaths) {
				must(t, os.WriteFile(filepath.Join(fp.parked, "SKILL.md"), []byte("edited\n"), 0o644))
			},
			revert: func(t *testing.T, e *env, fp fixturePaths) {
				must(t, os.WriteFile(filepath.Join(fp.parked, "SKILL.md"), []byte("---\nname: alpha\n---\nAlpha.\n"), 0o644))
			},
		},
		"a link was created where the old one was": {
			change: func(t *testing.T, e *env, fp fixturePaths) { must(t, os.Symlink("../../x", fp.link)) },
			revert: func(t *testing.T, e *env, fp fixturePaths) { must(t, os.Remove(fp.link)) },
		},
		"npx installed alpha again": {
			change: func(t *testing.T, e *env, fp fixturePaths) {
				must(t, e.lock.Add("alpha", []byte(`{"source":"owner/alpha","sourceType":"github","skillFolderHash":"1111111111111111111111111111111111111111"}`)))
			},
			revert: func(t *testing.T, e *env, fp fixturePaths) {
				lock, err := e.lock.Read()
				must(t, err)
				for _, en := range lock.Entries {
					if en.Name == "alpha" {
						_, err := e.lock.Remove("alpha", en.Raw)
						must(t, err)
					}
				}
			},
		},
		"alpha was enabled by another path": {
			change: func(t *testing.T, e *env, fp fixturePaths) { editParked(t, e, "elsewhere") },
			revert: func(t *testing.T, e *env, fp fixturePaths) { editParked(t, e, fp.parked) },
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e, fp := newEnv(t)
			eng := e.engine()
			plan := disableEffectPlan(t, e, eng, fp)
			noOwnLevels(t, e)
			start := observeWorld(t, e)
			out, err := eng.Apply(t.Context(), plan)
			must(t, err)
			c.change(t, e, fp)
			changed := observeWorld(t, e)
			snaps := len(snapshotDirs(t, e.p))

			if _, err := eng.Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrChangedSince) {
				t.Fatalf("error = %v, want ErrChangedSince", err)
			}
			assertWorld(t, "a refused undo", observeWorld(t, e), changed)
			if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != snaps || len(journal(t, e)) != 1 {
				t.Error("a refused undo left a record, a snapshot or a journal line")
			}

			c.revert(t, e, fp)
			_, err = eng.Undo(t.Context(), out.SnapshotID)
			must(t, err)
			assertWorld(t, "undo once the change is gone", observeWorld(t, e), start)
		})
	}
}

// editParked changes the folder state.json records for parked alpha.
func editParked(t *testing.T, e *env, folder string) {
	t.Helper()
	st, err := e.state.Load()
	must(t, err)
	p := st.Parked["alpha"]
	p.Folder = folder
	st.Parked["alpha"] = p
	must(t, e.state.Save(st))
}

// TestTheWholeEngineOverRealAdaptersMovesLinksParksAndUndoes is the vertical
// pass: real file, link, lock and state adapters in a temporary home with the
// guard on.
func TestTheWholeEngineOverRealAdaptersMovesLinksParksAndUndoes(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.clean()
	noOwnLevels(t, e)
	start := observeWorld(t, e)
	moved := e.home.Path(".agents/skills/beta-moved")
	link := e.home.Path(".claude/skills/beta")
	b := eng.NewBuilder(engine.Meta{Action: "vertical", Targets: []string{"claude-code"}})
	must(t, b.Move(fp.beta, moved))
	must(t, b.Link(link, "../../.agents/skills/beta-moved"))
	must(t, b.LockPark("alpha"))
	plan, err := b.Build()
	must(t, err)

	out, err := eng.Apply(t.Context(), plan)
	must(t, err)
	if target, err := os.Readlink(link); err != nil || target != "../../.agents/skills/beta-moved" {
		t.Errorf("link = %q, %v", target, err)
	}
	if data, err := os.ReadFile(filepath.Join(link, "SKILL.md")); err != nil || !strings.Contains(string(data), "Beta.") {
		t.Errorf("the link does not reach beta: %q, %v", data, err)
	}
	if lock, _ := os.ReadFile(e.p.SkillsLockFile()); strings.Contains(string(lock), `"alpha"`) {
		t.Errorf("alpha is still in the lock:\n%s", lock)
	}

	_, err = eng.Undo(t.Context(), out.SnapshotID)
	must(t, err)
	assertWorld(t, "after the vertical undo", observeWorld(t, e), start)

	// The guard is on: a plan that reaches the real home is refused before
	// it is even built.
	bad := eng.NewBuilder(engine.Meta{Action: "vertical"})
	if err := bad.RemoveTree(fp.forbidden); err == nil {
		t.Error("a step under the real home was accepted")
	}
}

// TestNoLockPreimageNeedsTheLockDropper rolls back an install that created
// the lock: without a LockDropper the rollback stops with the record kept;
// a Recover that has one leaves no lock, as before.
func TestNoLockPreimageNeedsTheLockDropper(t *testing.T) {
	e, gp := installSetup(t)
	start := observeWorld(t, e)
	eng := e.engine()
	plan := installPlan(t, eng, gp)
	withoutDropper := e.engineWithoutDropper()
	writes := failingWrites(t, scenarios()["install with no lock"].setup)
	last := 0
	for i, w := range writes {
		if strings.HasPrefix(w, "Add ") {
			last = i
		}
	}
	e.files.arm(last+1, failOnce) // the record write after lockAdd
	_, err := withoutDropper.Apply(t.Context(), plan)
	if !errors.Is(err, engine.ErrLockDropUnsupported) || !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the failure and ErrLockDropUnsupported", err)
	}
	if !recordExists(t, e.p) {
		t.Fatal("the record went away although the rollback could not finish")
	}
	if _, err := e.clean().Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertWorld(t, "recover with a dropper", observeWorld(t, e), start)
}

// TestARestoredCopyThatDoesNotVerifyIsNotPublished makes the copy a restore
// prepares next to its destination hash wrong: the restore must not rename
// it into place, and the undo rolls back to the disabled state.
func TestARestoredCopyThatDoesNotVerifyIsNotPublished(t *testing.T) {
	e, fp := newEnv(t)
	out, err := e.clean().Apply(t.Context(), disableEffectPlan(t, e, e.clean(), fp))
	must(t, err)
	disabled := observeWorld(t, e)
	e.hash = func(dir string) (treehash.Tree, error) {
		tree, err := treehash.Dir(dir)
		if strings.HasPrefix(dir, filepath.Join(filepath.Dir(e.p.ParkedDir()), "restore")+string(filepath.Separator)) {
			tree.SHA = "0000000000000000000000000000000000000000"
		}
		return tree, err
	}
	_, err = e.engine().Undo(t.Context(), out.SnapshotID)
	if !errors.Is(err, engine.ErrSnapshotMismatch) {
		t.Fatalf("error = %v, want ErrSnapshotMismatch", err)
	}
	assertWorld(t, "after the refused restore", observeWorld(t, e), disabled)
}

// TestARestoreFromOutsideTheSnapshotsIsRefused builds a restore whose source
// is not a snapshot copy: running it must fail without publishing anything.
func TestARestoreFromOutsideTheSnapshotsIsRefused(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	sha, err := treehash.Dir(fp.beta)
	must(t, err)
	target := e.home.Path(".agents/skills/beta-copy")
	b := eng.NewBuilder(engine.Meta{Action: "restore"})
	must(t, b.RestoreTree(target, fp.beta, sha.SHA))
	plan, err := b.Build()
	must(t, err)
	before := observeWorld(t, e)
	if _, err := eng.Apply(t.Context(), plan); !errors.Is(err, engine.ErrInvalidStep) {
		t.Fatalf("error = %v, want ErrInvalidStep", err)
	}
	assertWorld(t, "a refused restore", observeWorld(t, e), before)
}
