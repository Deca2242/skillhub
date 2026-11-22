package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
)

// emptyStore moves alpha to the parking zone and beta away, drops alpha's
// link, and removes the store: a home whose ~/.agents/skills is missing.
func emptyStore(t *testing.T, e *env, fp fixturePaths) {
	t.Helper()
	must(t, os.Remove(fp.link))
	must(t, os.Rename(fp.alpha, fp.parked))
	must(t, os.Rename(fp.beta, filepath.Join(e.p.ParkedDir(), "beta")))
	must(t, os.Remove(e.p.StoreDir()))
}

func absent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err != nil
}

// TestAMoveCreatesAMissingStoreAndItsUndoRemovesIt: a move into
// ~/.agents/skills/<key> in a home without the store creates exactly the
// store, published by a rename like the engine's own levels, and the undo,
// once the store is empty again, removes it: the home is identical.
func TestAMoveCreatesAMissingStoreAndItsUndoRemovesIt(t *testing.T) {
	e, fp := newEnv(t)
	emptyStore(t, e, fp)
	before := observeWorld(t, e)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "enable"})
	must(t, b.Move(fp.parked, fp.alpha))
	plan, err := b.Build()
	must(t, err)

	out, err := eng.Apply(t.Context(), plan)
	must(t, err)
	if info, err := os.Lstat(filepath.Join(fp.alpha, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("alpha is not in the store: %v", err)
	}
	if !absent(t, e.home.Path(".agents/.skillhub-mkdir-skills")) {
		t.Error("the temporary level of the store is left")
	}

	_, err = e.clean().Undo(t.Context(), out.SnapshotID)
	must(t, err)
	assertWorld(t, "move into a missing store, then undo", observeWorld(t, e), before)
	if !absent(t, e.p.StoreDir()) {
		t.Error("the undo left the store the operation created")
	}
}

// TestTheEngineCreatesOnlyTheStoreItself: a move deeper than
// ~/.agents/skills/<key>, into a missing store, fails and creates nothing.
func TestTheEngineCreatesOnlyTheStoreItself(t *testing.T) {
	e, fp := newEnv(t)
	emptyStore(t, e, fp)
	before := observeWorld(t, e)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "move"})
	must(t, b.Move(fp.parked, filepath.Join(e.p.StoreDir(), "nested", "alpha")))
	plan, err := b.Build()
	must(t, err)

	if _, err := eng.Apply(t.Context(), plan); err == nil {
		t.Fatal("a move below a missing store worked")
	}
	if !absent(t, e.p.StoreDir()) {
		t.Error("the engine created the store for a move below it")
	}
	assertWorld(t, "a refused move below the store", observeWorld(t, e), before)
}

// TestARollbackRemovesTheStoreItCreated: the move creates the store, the
// link after it fails, and the rollback leaves no store behind.
func TestARollbackRemovesTheStoreItCreated(t *testing.T) {
	e, fp := newEnv(t)
	emptyStore(t, e, fp)
	before := observeWorld(t, e)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "enable"})
	must(t, b.Move(fp.parked, fp.alpha))
	must(t, b.Link(e.home.Path(".claude/missing/alpha"), "../../.agents/skills/alpha"))
	plan, err := b.Build()
	must(t, err)

	if _, err := eng.Apply(t.Context(), plan); err == nil {
		t.Fatal("a link into a missing directory worked")
	}
	assertWorld(t, "a failed move into a missing store", observeWorld(t, e), before)
	if !absent(t, e.p.StoreDir()) {
		t.Error("the rollback left the store the operation created")
	}
}

// TestUndoRemovesTheOwnLevelsTheOperationCreated: a disable in a home with
// no ~/.agents/.skillhub creates it and the parking zone; its undo removes
// both once they are empty, so the home is identical, their absence
// included (L44). The same holds when the undo's close is interrupted after
// its commit and Recover finishes it.
func TestUndoRemovesTheOwnLevelsTheOperationCreated(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "undo", true: "undo, crash after commit, recover"}[interrupted], func(t *testing.T) {
			e, fp := newEnv(t)
			must(t, os.RemoveAll(e.home.Path(".agents/.skillhub")))
			before := observeWorld(t, e)
			eng := e.engine()
			b := eng.NewBuilder(engine.Meta{Action: "disable", Targets: []string{"claude-code"}})
			must(t, b.Unlink(fp.link, fp.linkTarget))
			must(t, b.Move(fp.alpha, fp.parked))
			plan, err := b.Build()
			must(t, err)
			out, err := eng.Apply(t.Context(), plan)
			must(t, err)

			if !interrupted {
				_, err = e.clean().Undo(t.Context(), out.SnapshotID)
				must(t, err)
			} else {
				undo, err := e.clean().UndoPlan(out.SnapshotID)
				must(t, err)
				// The commit is the journal line; the record's removal after
				// it is the last write of the close.
				e.files.afterWrite = func(_ int, line string) {
					if strings.HasPrefix(line, "AppendJournal ") {
						e.files.afterWrite = nil
						e.files.arm(0, crashBefore) // arm restarts the count
					}
				}
				if _, err := eng.Apply(t.Context(), undo); err == nil {
					t.Fatal("the crash after the commit was not reached")
				}
				if !recordExists(t, e.p) {
					t.Fatal("the interrupted undo left no record")
				}
				res, err := e.clean().Recover(t.Context())
				must(t, err)
				if res.RolledBack {
					t.Fatalf("Recover rolled back a committed undo: %+v", res)
				}
			}
			assertWorld(t, "disable then undo", observeWorld(t, e), before)
			for _, dir := range []string{e.p.ParkedDir(), filepath.Dir(e.p.ParkedDir())} {
				if !absent(t, dir) {
					t.Errorf("%s is left after the undo", dir)
				}
			}
		})
	}
}

// TestUndoKeepsAnOwnLevelThatIsNotEmpty: the parking zone the disable created
// holds another parked skill when the disable is undone, so it stays.
func TestUndoKeepsAnOwnLevelThatIsNotEmpty(t *testing.T) {
	e, fp := newEnv(t)
	must(t, os.RemoveAll(e.home.Path(".agents/.skillhub")))
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "disable"})
	must(t, b.Move(fp.alpha, fp.parked))
	plan, err := b.Build()
	must(t, err)
	out, err := eng.Apply(t.Context(), plan)
	must(t, err)
	other := filepath.Join(e.p.ParkedDir(), "beta")
	must(t, os.Rename(fp.beta, other))

	_, err = e.clean().Undo(t.Context(), out.SnapshotID)
	must(t, err)
	if _, err := os.Lstat(filepath.Join(other, "SKILL.md")); err != nil {
		t.Errorf("the other parked skill is gone: %v", err)
	}
}

// TestACrashWhileCreatingTheStoreLeavesNoTemporaryLevel: the store is made
// as a temporary sibling, ~/.agents/.skillhub-mkdir-skills, then renamed; a
// crash right after it is made leaves that sibling, and Recover removes it
// with the rest of the rollback.
func TestACrashWhileCreatingTheStoreLeavesNoTemporaryLevel(t *testing.T) {
	e, fp := newEnv(t)
	emptyStore(t, e, fp)
	before := observeWorld(t, e)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "enable"})
	must(t, b.Move(fp.parked, fp.alpha))
	plan, err := b.Build()
	must(t, err)
	tmp := e.home.Path(".agents/.skillhub-mkdir-skills")
	e.files.afterWrite = func(_ int, line string) {
		if line == "MkdirAll "+tmp {
			e.files.afterWrite = nil
			e.files.arm(0, crashBefore) // arm restarts the count
		}
	}

	if _, err := eng.Apply(t.Context(), plan); err == nil {
		t.Fatal("the crash was not reached")
	}
	if absent(t, tmp) {
		t.Fatal("the crash left no temporary level to recover")
	}
	res, err := e.clean().Recover(t.Context())
	must(t, err)
	if !res.RolledBack {
		t.Fatalf("Recover() = %+v, want a rollback", res)
	}
	assertWorld(t, "a crash while creating the store, then recover", observeWorld(t, e), before)
	if !absent(t, tmp) || !absent(t, e.p.StoreDir()) {
		t.Error("the recover left the temporary level or the store")
	}
}

// TestUndoRemovesAnEmptyStoreBesideAKeptParkingZone: one operation created
// the parking zone and the store; at its undo the parking zone holds another
// skill and stays, and the store, empty, still goes.
func TestUndoRemovesAnEmptyStoreBesideAKeptParkingZone(t *testing.T) {
	e, fp := newEnv(t)
	must(t, os.Remove(fp.link))
	alpha, beta := e.home.Path("elsewhere/alpha"), e.home.Path("elsewhere/beta")
	must(t, os.MkdirAll(e.home.Path("elsewhere"), 0o755))
	must(t, os.Rename(fp.alpha, alpha))
	must(t, os.Rename(fp.beta, beta))
	must(t, os.Remove(e.p.StoreDir()))
	must(t, os.RemoveAll(e.home.Path(".agents/.skillhub")))
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "move"})
	must(t, b.Move(alpha, fp.parked))
	must(t, b.Move(beta, filepath.Join(e.p.StoreDir(), "beta")))
	plan, err := b.Build()
	must(t, err)
	out, err := eng.Apply(t.Context(), plan)
	must(t, err)
	e.home.WriteFile(".agents/.skillhub/disabled/gamma/SKILL.md", "---\nname: gamma\n---\nGamma.\n")

	_, err = e.clean().Undo(t.Context(), out.SnapshotID)
	must(t, err)
	if absent(t, filepath.Join(e.p.ParkedDir(), "gamma", "SKILL.md")) {
		t.Error("the other parked skill is gone")
	}
	if !absent(t, e.p.StoreDir()) {
		t.Error("the empty store the operation created is left")
	}
}
