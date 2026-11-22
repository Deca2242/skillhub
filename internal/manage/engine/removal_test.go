package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
)

// writable makes every directory under root writable again, so the test's
// temporary directories can be removed after a test planted read-only ones.
func writable(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
}

// TestRemovingAFolderWithAReadOnlyDirectoryCompletes removes beta while it
// holds a read-only directory with files: deleting in place fails half way;
// the step must still remove the folder whole and leave no trash.
func TestRemovingAFolderWithAReadOnlyDirectoryCompletes(t *testing.T) {
	e, fp := newEnv(t)
	writable(t, e.home.Root())
	e.home.WriteFile(".agents/skills/beta/locked/a.txt", "a\n")
	e.home.WriteFile(".agents/skills/beta/locked/b.txt", "b\n")
	must(t, os.Chmod(filepath.Join(fp.beta, "locked"), 0o555))
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove"})
	must(t, b.RemoveTree(fp.beta))
	plan, err := b.Build()
	must(t, err)

	_, err = eng.Apply(t.Context(), plan)
	must(t, err)
	if _, err := os.Lstat(fp.beta); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("beta is still there: %v", err)
	}
	if _, err := os.Lstat(e.home.Path(".agents/.skillhub/trash")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the trash is still there: %v", err)
	}
	if recordExists(t, e.p) {
		t.Error("the record is still there")
	}
}

// TestARemovalMovesTheFolderOutBeforeDeletingIt checks the order: the
// folder leaves the store by a rename into the engine's trash, outside every
// harness, and only that copy is deleted.
func TestARemovalMovesTheFolderOutBeforeDeletingIt(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove"})
	must(t, b.RemoveTree(fp.beta))
	plan, err := b.Build()
	must(t, err)
	var writes []string
	e.files.afterWrite = func(_ int, line string) { writes = append(writes, line) }

	_, err = eng.Apply(t.Context(), plan)
	must(t, err)
	trash := e.home.Path(".agents/.skillhub/trash") + string(filepath.Separator)
	renamed, deleted := false, false
	for _, w := range writes {
		switch {
		case w == "RemoveTree "+fp.beta:
			t.Errorf("the folder was deleted in place: %q", writes)
		case w == "RenameNoReplace "+fp.beta:
			renamed = true
		case strings.HasPrefix(w, "RemoveTree "+trash):
			deleted = renamed
		}
	}
	if !renamed || !deleted {
		t.Errorf("rename into the trash %t, then delete there %t: %q", renamed, deleted, writes)
	}
}

// TestAMoveCreatesTheParkingZoneOnlyWhereItIsTheEngines parks alpha in a home
// with no ~/.agents/.skillhub: the engine creates the parking zone, each new
// level published by a rename that syncs its parent. A move into a missing
// directory that is not the engine's own fails and creates nothing.
func TestAMoveCreatesTheParkingZoneOnlyWhereItIsTheEngines(t *testing.T) {
	e, fp := newEnv(t)
	must(t, os.RemoveAll(e.home.Path(".agents/.skillhub")))
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "disable"})
	must(t, b.Move(fp.alpha, fp.parked))
	plan, err := b.Build()
	must(t, err)
	var writes []string
	e.files.afterWrite = func(_ int, line string) { writes = append(writes, line) }

	_, err = eng.Apply(t.Context(), plan)
	must(t, err)
	if info, err := os.Lstat(filepath.Join(fp.parked, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("alpha is not parked: %v", err)
	}
	published := 0
	for _, w := range writes {
		if strings.HasPrefix(w, "RenameNoReplace ") && strings.Contains(w, ".skillhub-mkdir-") {
			published++
		}
	}
	if published != 2 {
		t.Errorf("%d levels published by a rename, want 2 (.skillhub, disabled): %q", published, writes)
	}
	for _, leftover := range []string{".agents/.skillhub-mkdir-.skillhub", ".agents/.skillhub/.skillhub-mkdir-disabled"} {
		if _, err := os.Lstat(e.home.Path(leftover)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is left: %v", leftover, err)
		}
	}

	// Somewhere that is not the engine's: nothing is created.
	foreign := e.home.Path(".claude/missing/beta")
	b = eng.NewBuilder(engine.Meta{Action: "move"})
	must(t, b.Move(fp.beta, foreign))
	plan, err = b.Build()
	must(t, err)
	if _, err := eng.Apply(t.Context(), plan); err == nil {
		t.Fatal("a move into a missing foreign directory worked")
	}
	if _, err := os.Lstat(filepath.Dir(foreign)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the engine created %s: %v", filepath.Dir(foreign), err)
	}
	if _, err := os.Lstat(filepath.Join(fp.beta, "SKILL.md")); err != nil {
		t.Errorf("beta did not stay: %v", err)
	}
}

// TestRollingBackAMoveOutOfTheParkingZoneRecreatesIt enables a parked skill,
// the parking zone goes away once empty, and the next write fails: the
// rollback must recreate the parking zone to put the folder back.
func TestRollingBackAMoveOutOfTheParkingZoneRecreatesIt(t *testing.T) {
	e, fp := newEnv(t)
	must(t, os.Remove(fp.link))
	must(t, os.Rename(fp.alpha, fp.parked))
	before := observeWorld(t, e)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "enable"})
	must(t, b.Move(fp.parked, fp.alpha))
	must(t, b.Link(fp.link, fp.linkTarget))
	plan, err := b.Build()
	must(t, err)
	e.files.afterWrite = func(_ int, line string) {
		if line == "RenameNoReplace "+fp.parked {
			must(t, os.Remove(e.p.ParkedDir()))
			e.files.afterWrite = nil
			e.files.arm(0, failOnce) // the progress update that follows
		}
	}

	if _, err := eng.Apply(t.Context(), plan); !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	assertWorld(t, "after the rollback", observeWorld(t, e), before)
}
