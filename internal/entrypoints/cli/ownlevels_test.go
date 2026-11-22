package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
)

// undoLast undoes the last version 2 operation as a script does.
func (m *machine) undoLast() {
	m.t.Helper()
	last := m.lastV2()
	m.approve("undo", last.Target, "--to", last.SnapshotID)
}

// A level of ~/.agents/.skillhub that the user made a symbolic link to an
// empty directory is never removed: only a real, empty directory goes. An
// unlink, a remove and their undos all keep the link (B1 of P4).
func TestALinkedOwnLevelIsKept(t *testing.T) {
	for _, level := range []string{".agents/.skillhub", ".agents/.skillhub/disabled"} {
		t.Run(level, func(t *testing.T) {
			m := newMachine(t)
			ext := filepath.Join(t.TempDir(), "ext")
			must(t, os.MkdirAll(ext, 0o755))
			must(t, os.MkdirAll(filepath.Dir(m.h.Path(level)), 0o755))
			must(t, os.Symlink(ext, m.h.Path(level)))
			kept := func(when string) {
				t.Helper()
				if target, err := os.Readlink(m.h.Path(level)); err != nil || target != ext {
					t.Fatalf("%s: %s = %q, %v; want the link to %s", when, level, target, err, ext)
				}
			}
			m.approve("skills", "unlink", "archify", "--from", "claude-code")
			kept("after unlink")
			// recover tidies the install staging and its parent: never a link.
			if r := m.ok("recover"); !strings.Contains(r.stdout, "No hay ninguna operación interrumpida") {
				t.Errorf("recover:\n%s", r.stdout)
			}
			kept("after recover")
			m.undoLast()
			kept("after the undo of unlink")
			// remove moves the folder through ~/.agents/.skillhub/trash, which
			// the engine never creates through a link: it may refuse, and then
			// writes nothing.
			shown := m.ok("skills", "remove", "archify", "--dry-run")
			r := m.run("", "skills", "remove", "archify", "--yes", "--plan", planID(t, shown.stdout))
			if _, err := os.Lstat(engine.RecordFile(m.p)); err == nil {
				t.Fatal("remove left its record pending")
			}
			kept("after remove")
			if r.code == 0 {
				m.undoLast()
				kept("after the undo of remove")
			} else if !m.exists(".agents/skills/archify") {
				t.Fatalf("the refused remove took the folder: %s", r.stderr)
			}
		})
	}
}

// A level that is a regular file is left as it is: the close never fails
// over the tidying of empty levels, and recover never gets stuck (A2 of P4).
func TestAnOwnLevelThatIsAFileIsLeftAlone(t *testing.T) {
	for _, level := range []string{".agents/.skillhub", ".agents/.skillhub/disabled"} {
		t.Run(level, func(t *testing.T) {
			m := newMachine(t)
			must(t, os.MkdirAll(filepath.Dir(m.h.Path(level)), 0o755))
			must(t, os.WriteFile(m.h.Path(level), []byte("mine\n"), 0o644))
			m.approve("skills", "unlink", "archify", "--from", "claude-code")
			if _, err := os.Lstat(engine.RecordFile(m.p)); err == nil {
				t.Fatal("the unlink left its record pending")
			}
			if r := m.ok("recover"); !strings.Contains(r.stdout, "No hay ninguna operación interrumpida") {
				t.Errorf("recover after the unlink:\n%s", r.stdout)
			}
			if data, err := os.ReadFile(m.h.Path(level)); err != nil || string(data) != "mine\n" {
				t.Errorf("%s = %q, %v; want it untouched", level, data, err)
			}
		})
	}
}

// With an operation pending, installing refuses before it resolves
// anything: it asks for recover, as every other command does, and writes
// nothing, the install staging included (A1 of P4).
func TestInstallWithAPendingOperationWritesNothing(t *testing.T) {
	m := newMachine(t)
	m.publishPinnable()
	m.pendingV2()
	before := m.hash()
	for _, args := range [][]string{
		{"skills", "install", "acme/tools", "--to", "claude-code", "--dry-run"},
		{"skills", "install", "acme/tools", "--to", "claude-code", "--yes", "--plan", "x@1"},
	} {
		r := m.run("", args...)
		if r.code != 2 || !strings.Contains(r.stderr, pendingMsg) {
			t.Errorf("%v = exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
		}
	}
	if m.exists(".agents/.skillhub/staging") {
		t.Error("the refused install wrote its staging")
	}
	if m.hash() != before {
		t.Error("the refused install wrote something")
	}
}

// An install staging that cannot be emptied is explained in Spanish (A3 of
// P4).
func TestAStagingThatCannotBeEmptiedIsExplainedInSpanish(t *testing.T) {
	m := newMachine(t)
	locked := m.h.Path(".agents/.skillhub/staging/tidy-0123")
	must(t, os.MkdirAll(locked, 0o755))
	must(t, os.WriteFile(filepath.Join(locked, "SKILL.md"), []byte("half\n"), 0o644))
	must(t, os.Chmod(locked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	r := m.run("", "recover")
	want := "skillhub: no se pudo leer ni vaciar la copia preparada para instalar en ~/.agents/.skillhub/staging"
	if r.code != 2 || !strings.HasPrefix(r.stderr, want) {
		t.Errorf("recover = exit %d\nstdout: %s\nstderr: %s\nwant it to start with %q", r.code, r.stdout, r.stderr, want)
	}
}
