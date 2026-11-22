package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/updates"
)

// strayCopy leaves in the install staging what a crash of install leaves
// there before its record is written: a copy no operation owns.
func strayCopy(t *testing.T, m *machine) string {
	t.Helper()
	stray := filepath.Join(m.p.InstallStagingDir(), "tidy-0123")
	must(t, os.MkdirAll(stray, 0o755))
	must(t, os.WriteFile(filepath.Join(stray, "SKILL.md"), []byte("---\nname: tidy\n---\nhalf\n"), 0o644))
	return stray
}

// With no version 2 record pending, everything in the install staging is an
// orphan: recover removes it and the levels left empty, and says it removed
// leftovers (D1).
func TestRecoverRemovesAnOrphanStaging(t *testing.T) {
	m := newMachine(t)
	before := homeLines(t, m)
	strayCopy(t, m)

	rec, err := m.app.RecoverOperation(bg)
	if err != nil {
		t.Fatalf("RecoverOperation = %v", err)
	}
	if rec.Version != 1 || !rec.Leftovers {
		t.Fatalf("RecoverOperation = %+v, want the leftovers reported", rec)
	}
	if m.exists(".agents/.skillhub") {
		t.Error("recover left the staging or its empty parent")
	}
	if d := homeDiff(before, homeLines(t, m)); d != "" {
		t.Errorf("recover changed more than the orphan staging:\n%s", d)
	}
	// Nothing is left, so a second recover has nothing to do.
	if _, err := m.app.RecoverOperation(bg); !errors.Is(err, updates.ErrNoOperation) {
		t.Fatalf("the second RecoverOperation = %v, want ErrNoOperation", err)
	}
}

// Resolving an install removes the orphans of the staging first.
func TestResolveInstallRemovesAnOrphanStaging(t *testing.T) {
	m := newMachine(t)
	m.publishTool()
	stray := strayCopy(t, m)

	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	if _, err := os.Lstat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the orphan copy is left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rev.Prepared.StagedPath, "SKILL.md")); err != nil {
		t.Errorf("the new revision is not staged: %v", err)
	}
}

// While a version 2 record is pending, the staging belongs to it: neither
// resolving an install nor anything but the recover of that record touches
// it (L63).
func TestAPendingRecordKeepsTheStaging(t *testing.T) {
	m := newMachine(t)
	m.publishTool()
	stray := strayCopy(t, m)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	crashAfterBegin(t, m, p)

	if _, err := m.app.ResolveInstall(bg, "acme/tools"); !errors.Is(err, app.ErrOperationPending) {
		t.Errorf("ResolveInstall with a pending record = %v, want ErrOperationPending", err)
	}
	if _, err := os.Stat(filepath.Join(stray, "SKILL.md")); err != nil {
		t.Fatalf("ResolveInstall touched the staging of a pending operation: %v", err)
	}
	// Recover resolves the record; then nothing owns the staging.
	rec, err := m.app.RecoverOperation(bg)
	must(t, err)
	if rec.Version != 2 || !rec.V2.RolledBack {
		t.Fatalf("RecoverOperation = %+v", rec)
	}
	if m.exists(".agents/.skillhub/staging") {
		t.Error("recover left the staging once the record was resolved")
	}
}

// An empty parking zone that an operation could not remove at its close
// (the tidying never fails the close) is a leftover too: with no record
// pending, recover removes it and the empty ~/.agents/.skillhub.
func TestRecoverRemovesAnEmptyParkingZone(t *testing.T) {
	m := newMachine(t)
	before := homeLines(t, m)
	must(t, os.MkdirAll(m.p.ParkedDir(), 0o755))
	if _, err := m.app.RecoverOperation(bg); err != nil && !errors.Is(err, updates.ErrNoOperation) {
		t.Fatalf("RecoverOperation = %v", err)
	}
	if d := homeDiff(before, homeLines(t, m)); d != "" {
		t.Errorf("recover left the empty parking zone:\n%s", d)
	}
}
