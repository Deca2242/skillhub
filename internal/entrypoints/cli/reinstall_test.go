package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"skillhub/internal/app"
)

// loseGrillMe deletes the store folder of grill-me and its Claude link,
// publishes owner/grill-me upstream, and returns the id of the finding of
// its lock entry left without a folder.
func loseGrillMe(t *testing.T, m *machine) string {
	t.Helper()
	must(t, os.RemoveAll(m.h.Path(".agents/skills/grill-me")))
	must(t, os.Remove(m.h.Path(".claude/skills/grill-me")))
	m.gh.AddRepo("owner/grill-me", "main")
	dir := filepath.Join(t.TempDir(), "upstream")
	writeTree(t, filepath.Join(dir, "skills", "grill-me"), map[string]string{
		"SKILL.md": "---\nname: grill-me\ndescription: grill me\n---\nnew body\n",
	})
	m.gh.Commit(t, "owner/grill-me", "main", dir)
	a, err := m.newApp()
	must(t, err)
	findings, err := a.Doctor(bgCtx)
	must(t, err)
	i := slices.IndexFunc(findings, func(f app.Finding) bool { return f.Kind == "lock_entry_without_folder" && f.LockKey == "grill-me" })
	if i < 0 {
		t.Fatalf("no lock entry finding for grill-me: %+v", findings)
	}
	return findings[i].ID
}

// doctor fix --repair reinstall shows the review of the entry's source and
// the plan that replaces the entry, runs it only with the identifier shown,
// and its undo leaves the home as it was.
func TestDoctorFixReinstallsThroughTheInstallReview(t *testing.T) {
	m := newMachine(t)
	id := loseGrillMe(t, m)
	args := []string{"doctor", "fix", id, "--repair", "reinstall", "--to", "claude-code"}

	shown := m.ok(append(slices.Clone(args), "--dry-run")...)
	for _, want := range []string{
		"Reinstalar «grill-me»", "owner/grill-me/skills/grill-me", "+new body", "permisos nuevos",
		"Plan: Reinstalar grill-me desde owner/grill-me", "Entrada del lock que se reemplaza:",
		"reemplazar la entrada «grill-me» del lock",
	} {
		if !strings.Contains(shown.stdout, want) {
			t.Errorf("the reinstall dry run lacks %q:\n%s", want, shown.stdout)
		}
	}
	if m.exists(".agents/skills/grill-me") {
		t.Fatal("the dry run reinstalled the skill")
	}
	must(t, os.MkdirAll(m.p.StateDir(), 0o755))
	before := m.hash(m.p.StateDir(), m.p.CacheDir())

	r := m.run("", args...)
	if r.code != 2 || !strings.Contains(r.stderr, needsTerminal) || m.exists(".agents/skills/grill-me") {
		t.Fatalf("reinstall without a terminal = exit %d, stderr %q", r.code, r.stderr)
	}
	tok := planID(t, shown.stdout)
	if !strings.Contains(tok, "@") {
		t.Fatalf("the reinstall token %q does not carry the lock entry's time", tok)
	}
	m.clock.Advance(3600e9)
	m.ok(append(slices.Clone(args), "--yes", "--plan", tok)...)
	body, err := os.ReadFile(m.h.Path(".agents/skills/grill-me/SKILL.md"))
	if err != nil || !strings.Contains(string(body), "new body") {
		t.Fatalf("reinstalled SKILL.md = %q, %v", body, err)
	}
	if _, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/grill-me")); err != nil {
		t.Fatalf("the Claude link does not resolve: %v", err)
	}
	line := m.lastV2()
	if line.Target != "v2:reinstall" {
		t.Fatalf("journal line = %+v", line)
	}

	m.approve("undo", "v2:reinstall", "--to", line.SnapshotID)
	if got := m.hash(m.p.StateDir(), m.p.CacheDir()); got != before {
		t.Error("undoing the reinstall did not leave the home as it was")
	}
}

// A plan other than the one shown is refused: a token whose time is not the
// one shown plans another entry, and nothing runs.
func TestDoctorFixReinstallRefusesAnotherPlan(t *testing.T) {
	m := newMachine(t)
	id := loseGrillMe(t, m)
	args := []string{"doctor", "fix", id, "--repair", "reinstall", "--to", "claude-code"}
	tok := planID(t, m.ok(append(slices.Clone(args), "--dry-run")...).stdout)
	plan, ms, _ := strings.Cut(tok, "@")
	at, err := strconv.ParseInt(ms, 10, 64)
	must(t, err)
	moved := plan + "@" + strconv.FormatInt(at+1, 10)
	r := m.run("", append(slices.Clone(args), "--yes", "--plan", moved)...)
	if r.code != 2 || !strings.Contains(r.stderr, planChanged) || m.exists(".agents/skills/grill-me") {
		t.Fatalf("another plan = exit %d, stderr %q", r.code, r.stderr)
	}
}

// --to chooses the targets of a reinstall only: given to any other repair
// it is refused before anything is planned, never ignored.
func TestDoctorFixToNeedsTheReinstallRepair(t *testing.T) {
	m := newMachine(t)
	id := m.ghostLink()
	before := m.hash()
	r := m.run("", "doctor", "fix", id, "--to", "claude-code", "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "--to solo se usa con la reparación reinstall") || r.stdout != "" {
		t.Fatalf("fix --to of a broken link = exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if m.hash() != before {
		t.Error("a refused fix wrote something")
	}
}
