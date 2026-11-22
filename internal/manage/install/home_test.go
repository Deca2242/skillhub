package install_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"skillhub/internal/manage/engine"
)

// TestInstallUnderALinkedAncestorResolves: with ~/.claude a symlink to
// ~/dotfiles/a/claude (stow), the link install creates in ~/.claude/skills
// is relative to the real link directory, as npx skills writes it, so it
// reaches the store folder.
func TestInstallUnderALinkedAncestorResolves(t *testing.T) {
	s := newSite(t, false)
	must(t, os.MkdirAll(s.home.Path("dotfiles/a"), 0o755))
	must(t, os.Rename(s.home.Path(".claude"), s.home.Path("dotfiles/a/claude")))
	must(t, os.Symlink("dotfiles/a/claude", s.home.Path(".claude")))
	s.publish("acme/tool", "main", tool)

	pr, err := s.planner().Install(s.review("acme/tool"), []string{"claude-code"})
	must(t, err)
	const want = "../../../../.agents/skills/tool"
	if got := pr.Preview.Links; len(got) != 1 || got[0].Target != want {
		t.Fatalf("links = %+v, want the target %s", got, want)
	}
	if _, err := s.engine().Apply(t.Context(), pr.Plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	link := s.home.Path(".claude/skills/tool")
	if got, err := os.Readlink(link); err != nil || got != want {
		t.Errorf("link text = %q, %v; want %q", got, err, want)
	}
	if resolved, err := filepath.EvalSymlinks(link); err != nil || resolved != filepath.Join(s.p.StoreDir(), "tool") {
		t.Errorf("the link resolves to %q, %v", resolved, err)
	}
}

// TestInstallCreatesAMissingStoreAndUndoRemovesIt: in a home without
// ~/.agents/skills the engine creates exactly that directory, durably, and
// the undo of the install takes it away again once it is empty, so the home
// is identical, the store's absence included.
func TestInstallCreatesAMissingStoreAndUndoRemovesIt(t *testing.T) {
	s := newSite(t, false)
	must(t, os.Remove(s.p.StoreDir()))
	s.publish("acme/tool", "main", tool)
	pr, err := s.planner().Install(s.review("acme/tool"), targets)
	must(t, err)
	home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

	out, err := s.engine().Apply(t.Context(), pr.Plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	store := filepath.Join(s.p.StoreDir(), "tool")
	if info, err := os.Lstat(store); err != nil || !info.IsDir() {
		t.Fatalf("store folder: %v, %v", info, err)
	}
	for _, l := range pr.Preview.Links {
		if resolved, err := filepath.EvalSymlinks(l.Path); err != nil || resolved != store {
			t.Errorf("link %s resolves to %q, %v", l.Path, resolved, err)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(s.p.StoreDir()), ".skillhub-mkdir-*")); len(leftovers) != 0 {
		t.Errorf("temporary levels left: %v", leftovers)
	}

	if _, err := s.engine().Undo(t.Context(), out.SnapshotID); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	sameHome(t, "install in a home without a store, then undo", home0, s.everything())
	if exists(t, s.p.StoreDir()) {
		t.Error("the store the install created is still there")
	}
	if !bytes.Equal(s.lockBytes(), lock0) {
		t.Errorf("lock after undo:\n%s", s.lockBytes())
	}
	if got := s.state(); !reflect.DeepEqual(got.Managed, state0.Managed) {
		t.Errorf("managed after undo: %v", got.Managed)
	}
}

// TestACrashDuringAnInstallThatCreatesTheStoreIsRecovered: a crash at every
// rename, symlink and link removal of an install into a home without a
// store, then Recover: the rollback takes the store it created away.
func TestACrashDuringAnInstallThatCreatesTheStoreIsRecovered(t *testing.T) {
	for at := 1; ; at++ {
		s := newSite(t, false)
		must(t, os.Remove(s.p.StoreDir()))
		s.publish("acme/tool", "main", tool)
		pr, err := s.planner().Install(s.review("acme/tool"), targets)
		must(t, err)
		home0, lock0 := s.everything(), s.lockBytes()

		c := &crash{at: at}
		s.wrap = c.wrap
		_, err = s.engine().Apply(t.Context(), pr.Plan)
		s.wrap = nil
		if !c.dead {
			if err != nil {
				t.Fatalf("Apply() error = %v without a crash", err)
			}
			if at < 4 {
				t.Fatalf("only %d crash points", at-1)
			}
			return
		}
		if !errors.Is(err, errCrash) {
			t.Fatalf("crash %d: Apply() error = %v, want the crash", at, err)
		}
		res, err := s.clean().Recover(t.Context())
		if err != nil || !res.Found {
			t.Fatalf("crash %d: Recover() = %+v, %v", at, res, err)
		}
		if res.RolledBack {
			sameHome(t, "crash then recover", home0, s.everything())
			if !bytes.Equal(s.lockBytes(), lock0) {
				t.Errorf("crash %d: lock after recover:\n%s", at, s.lockBytes())
			}
		} else if !exists(t, filepath.Join(s.p.StoreDir(), "tool")) {
			t.Errorf("crash %d: a completed install has no store folder", at)
		}
		if exists(t, engine.RecordFile(s.p)) {
			t.Errorf("crash %d: the record is still there", at)
		}
	}
}
