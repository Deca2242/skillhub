package cli_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// pendingMsg is the refusal of a run, or a dry run, while a version 2
// operation is pending.
const pendingMsg = "hay una operación sin terminar; ejecuta «skillhub recover» antes de continuar"

// publishPinnable publishes acme/tools with tidy and tags its commit v1.
func (m *machine) publishPinnable() {
	m.t.Helper()
	m.gh.AddRepo("acme/tools", "main")
	writeTree(m.t, filepath.Join(m.upstream, "skills", "tidy"), map[string]string{
		"SKILL.md": "---\nname: tidy\ndescription: tidy up\n---\nbody\n",
	})
	m.gh.Tag("acme/tools", "v1", m.gh.Commit(m.t, "acme/tools", "main", m.upstream))
}

// An install that is only shown, or refused, leaves the whole home as it
// was: the copy prepared for its review is discarded (B2).
func TestAnInstallNotAppliedLeavesTheHomeAsItWas(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *machine)
		args  []string
		code  int
	}{
		{"dry run", nil, []string{"skills", "install", "acme/tools", "--to", "claude-code", "--dry-run"}, 0},
		{"existing key", func(m *machine) { m.approve("skills", "install", "acme/tools", "--to", "claude-code") },
			[]string{"skills", "install", "acme/tools", "--dry-run"}, 2},
		{"pinned ref", nil, []string{"skills", "install", "github.com/acme/tools/tree/v1/skills/tidy", "--dry-run"}, 2},
		{"unknown harness", nil, []string{"skills", "install", "acme/tools", "--to", "nope", "--dry-run"}, 2},
		{"changed plan", nil, []string{"skills", "install", "acme/tools", "--to", "claude-code", "--yes", "--plan", "deadbeef@1"}, 2},
		{"not confirmed", nil, []string{"skills", "install", "acme/tools", "--to", "claude-code"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			m.publishPinnable()
			if tt.setup != nil {
				tt.setup(m)
			}
			before := m.hash()
			r := m.run("", tt.args...)
			if r.code != tt.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, tt.code, r.stdout, r.stderr)
			}
			if m.hash() != before {
				t.Errorf("the home changed; staging left: %v", m.exists(".agents/.skillhub/staging"))
			}
		})
	}
}

// The errors of GitHub while resolving a source are told in Spanish (B3).
func TestInstallTellsGitHubErrorsInSpanish(t *testing.T) {
	tests := []struct {
		name  string
		input string
		fail  error
		want  string
	}{
		{"no repository", "acme/none", nil, "el repositorio acme/none no existe en GitHub o no es visible"},
		{"no ref", "github.com/acme/tools/tree/nope/skills/tidy", nil, "el ref o la ruta indicados no existen en el repositorio"},
		{"rate limit", "acme/tools", ports.ErrRateLimited, "se agotó el límite de uso de GitHub"},
		{"not authenticated", "acme/tools", ports.ErrNotAuthenticated, "gh no tiene una sesión iniciada"},
		{"no network", "acme/tools", errors.New("gh api repos/acme/tools exited with 1: dial tcp: no such host"), "no se pudo consultar GitHub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			m.publishPinnable()
			if tt.fail != nil {
				m.gh.Fail = func(fakes.Call) error { return tt.fail }
			}
			r := m.run("", "skills", "install", tt.input, "--dry-run")
			if r.code != 2 || !strings.Contains(r.stderr, tt.want) || strings.Contains(r.stderr, "github ") {
				t.Fatalf("exit %d, stderr %q, want %q", r.code, r.stderr, tt.want)
			}
		})
	}
}

// With ~/.claude a symlink to ~/dotfiles/a/claude, install creates a link
// that resolves, with no refusal about the linked directory (B3, WB).
func TestInstallUnderAStowedClaude(t *testing.T) {
	m := newMachine(t)
	m.publishPinnable()
	must(t, os.MkdirAll(m.h.Path("dotfiles/a"), 0o755))
	must(t, os.Rename(m.h.Path(".claude"), m.h.Path("dotfiles/a/claude")))
	must(t, os.Symlink("dotfiles/a/claude", m.h.Path(".claude")))
	clone, err := filepath.EvalSymlinks(m.h.Path(graphtest.KarpathyClone))
	must(t, err)
	m.git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	r := m.approve("skills", "install", "acme/tools", "--to", "claude-code")
	if strings.Contains(r.stdout+r.stderr, "symlink") {
		t.Errorf("install speaks of a symlink:\n%s\n%s", r.stdout, r.stderr)
	}
	got, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/tidy"))
	must(t, err)
	want, err := filepath.EvalSymlinks(m.h.Path(".agents/skills/tidy"))
	must(t, err)
	if got != want {
		t.Fatalf("the link resolves to %s, want %s", got, want)
	}
}

// A dry run with a version 2 operation pending is refused as the run is,
// undo included, and writes nothing (B5).
func TestADryRunWithAPendingOperationIsRefused(t *testing.T) {
	m := newMachine(t)
	m.approve("skills", "disable", "agent-browser")
	snap := m.lastV2().SnapshotID
	m.pendingV2()
	before := m.hash()
	for _, args := range [][]string{
		{"skills", "disable", "grill-me", "--dry-run"},
		{"undo", "v2:disable", "--to", snap, "--dry-run"},
		// The pending operation may be what would make the plan possible:
		// enabling what it disables asks for recover too, never says the
		// skill is not disabled (L65).
		{"skills", "enable", "grill-me", "--dry-run"},
		{"skills", "enable", "agent-browser", "--dry-run"},
	} {
		r := m.run("", args...)
		if r.code != 2 || !strings.Contains(r.stderr, pendingMsg) {
			t.Errorf("%v = exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
		}
	}
	if m.hash() != before {
		t.Error("a refused dry run wrote something")
	}
}

// --canonical on a repair that takes no canonical folder is refused, never
// ignored (B6).
func TestDoctorFixCanonicalNeedsAdopt(t *testing.T) {
	m := newMachine(t)
	id := m.ghostLink()
	before := m.hash()
	r := m.run("", "doctor", "fix", id, "--canonical", "/x", "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "--canonical solo se usa con la reparación adopt") {
		t.Fatalf("fix --canonical = exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if m.hash() != before {
		t.Error("a refused fix wrote something")
	}
}

// A lock of another version, or malformed, and an install whose key exists
// are told in Spanish, naming what they are about.
func TestLockAndKeyRefusalsInSpanish(t *testing.T) {
	for name, lock := range map[string]string{
		"another version": "",
		"malformed":       "{\"version\": 3, \"skills\": [\n",
	} {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			args := []string{"skills", "remove", "agent-browser"}
			if name == "another version" {
				// npx's lock with another version: planning refuses it.
				lock = strings.Replace(mustRead(t, m.p.SkillsLockFile()), `"version": 3`, `"version": 2`, 1)
			}
			must(t, os.WriteFile(m.p.SkillsLockFile(), []byte(lock), 0o644))
			args = append(args, "--dry-run")
			r := m.run("", args...)
			if r.code != 2 || !strings.Contains(r.stderr, "el lock de skills") || strings.Contains(r.stderr, "skills lock") {
				t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
			}
		})
	}
	m := newMachine(t)
	m.publishPinnable()
	m.approve("skills", "install", "acme/tools", "--to", "claude-code")
	r := m.run("", "skills", "install", "acme/tools", "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "«tidy»") || strings.Contains(r.stderr, "error:") {
		t.Fatalf("second install = exit %d, stderr %q", r.code, r.stderr)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return string(b)
}

// failJournal fails the journal write number fail, counting from 1.
type failJournal struct {
	ports.StateStore
	fail, writes int
}

func (f *failJournal) AppendJournal(e domain.HistoryEntry) error {
	f.writes++
	if f.writes == f.fail {
		return fmt.Errorf("journal write %d: disk full", f.writes)
	}
	return f.StateStore.AppendJournal(e)
}

// A journal write that fails while an approved install or reinstall runs, or
// while recover resolves it, never takes the staging recover needs: after
// two recovers the home is as before, or the skill is fully installed with
// no staging left (regression of B2).
func TestAFailedJournalWriteDuringAnInstallIsRecovered(t *testing.T) {
	for _, action := range []string{"install", "reinstall"} {
		for fail := 1; fail <= 3; fail++ {
			t.Run(fmt.Sprintf("%s, journal write %d", action, fail), func(t *testing.T) {
				m := newMachine(t)
				args := []string{"skills", "install", "acme/tools", "--to", "claude-code"}
				key := "tidy"
				if action == "reinstall" {
					args = []string{"doctor", "fix", loseGrillMe(t, m), "--repair", "reinstall", "--to", "claude-code"}
					key = "grill-me"
				} else {
					m.publishPinnable()
				}
				tok := planID(t, m.ok(append(slices.Clone(args), "--dry-run")...).stdout)
				// The journal and the snapshots of what ran live in the state.
				must(t, os.MkdirAll(m.p.StateDir(), 0o755))
				home := func() string { return m.hash(m.p.StateDir(), m.p.CacheDir()) }
				before := home()
				counter := &failJournal{fail: fail}
				m.state = func(s ports.StateStore) ports.StateStore { counter.StateStore = s; return counter }
				m.run("", append(slices.Clone(args), "--yes", "--plan", tok)...)
				m.run("", "recover")
				m.run("", "recover")
				m.state = nil
				if _, err := os.Stat(engine.RecordFile(m.p)); err == nil {
					t.Fatal("the operation is still pending after two recovers")
				}
				if home() == before {
					return
				}
				if m.exists(".agents/.skillhub/staging") || !m.exists(".agents/skills/"+key+"/SKILL.md") {
					t.Fatal("neither as before nor fully installed")
				}
				if _, err := filepath.EvalSymlinks(m.h.Path(".claude/skills/" + key)); err != nil {
					t.Fatalf("the link does not resolve: %v", err)
				}
			})
		}
	}
}
