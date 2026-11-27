package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

type packFixture struct {
	t    *testing.T
	home *testsupport.Home
	gh   *fakes.GitHub
	git  *fakes.Git
}

func newPackMachine(t *testing.T) *packFixture {
	t.Helper()
	return &packFixture{
		t:    t,
		home: testsupport.NewHome(t),
		gh:   fakes.NewGitHub(),
		git:  fakes.NewGit(),
	}
}

func (p *packFixture) newApp() (*app.App, error) {
	paths := p.home.Paths()
	files := fsadapter.NewFileOps(paths.Guard())
	lock := skillslock.New(paths, files)
	st := statefile.New(paths)
	return app.New(app.Deps{
		Paths:       paths,
		Agents:      []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Files:       files,
		Dirs:        fsadapter.NewScanner(paths.Guard()),
		Lock:        lock,
		ManagedLock: lock,
		Links:       fsadapter.NewLinkOps(paths.Guard()),
		State:       st,
		Packs:       st,
		Settings:    settings.New(paths),
		GitHub:      p.gh,
		Git:         p.git,
		Notifier:    fakes.NewNotifier(),
		Background:  fakes.NewBackgroundService(),
		Clock:       fakes.NewClock(t0),
	})
}

func (p *packFixture) run(args ...string) result {
	p.t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, cli.Env{
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
		NewApp: p.newApp,
		TUI:    func(_ io.Writer, _ io.Writer) int { return 0 },
	})
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func (p *packFixture) ok(args ...string) result {
	p.t.Helper()
	r := p.run(args...)
	if r.code != 0 {
		p.t.Fatalf("skillhub %v failed (exit %d):\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

func TestPacksCLICreateListAddShowRemoveAndNoDiskSideEffects(t *testing.T) {
	p := newPackMachine(t)

	// Install one lock skill into the fixture home.
	p.home.WriteFile(".agents/skills/alpha/SKILL.md", "---\nname: alpha\ndescription: Alpha skill\n---\nbody\n")
	p.home.Symlink(".claude/skills/alpha", "../../.agents/skills/alpha")
	lockContent := `{
  "version": 3,
  "skills": {
    "alpha": {
      "source": "acme/skills",
      "sourceType": "github",
      "sourceUrl": "https://github.com/acme/skills.git",
      "skillPath": "skills/alpha/SKILL.md",
      "skillFolderHash": "1111111111111111111111111111111111111111",
      "installedAt": "2026-11-21T10:00:00.000Z",
      "updatedAt": "2026-11-21T10:00:00.000Z"
    }
  }
}
`
	p.home.WriteFile(".agents/.skill-lock.json", lockContent)

	scanBefore := p.ok("scan", "--json").stdout

	// Help lists packs.
	help := p.ok("--help").stdout
	if !strings.Contains(help, "packs") {
		t.Errorf("root --help does not list packs:\n%s", help)
	}

	// Empty list.
	empty := p.ok("packs", "list").stdout
	if !strings.Contains(empty, "No hay borradores de packs") {
		t.Errorf("unexpected empty list:\n%s", empty)
	}

	// Create draft pack.
	created := p.ok("packs", "create", "seguridad", "--description", "Auditoría básica").stdout
	if !strings.Contains(created, "seguridad") {
		t.Errorf("unexpected create output:\n%s", created)
	}

	// Reject duplicate pack.
	dup := p.run("packs", "create", "seguridad")
	if dup.code == 0 || !strings.Contains(dup.stderr, "ya existe") {
		t.Errorf("expected duplicate pack rejection, got code=%d stderr=%q", dup.code, dup.stderr)
	}

	// Reject unobserved / nonexistent member.
	badAdd := p.run("packs", "add", "seguridad", "lock:no-existe")
	if badAdd.code == 0 || badAdd.stderr == "" {
		t.Errorf("expected unobserved member rejection, got code=%d stderr=%q", badAdd.code, badAdd.stderr)
	}

	// Add observed member.
	p.ok("packs", "add", "seguridad", "alpha")

	// List JSON.
	listOut := p.ok("packs", "list", "--json").stdout
	var listed []app.PackSummary
	if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
		t.Fatalf("packs list --json: %v\n%s", err, listOut)
	}
	if len(listed) != 1 || listed[0].Name != "seguridad" || listed[0].MemberCount != 1 {
		t.Fatalf("unexpected packs list --json: %+v", listed)
	}

	// Show text & JSON.
	showOut := p.ok("packs", "show", "seguridad").stdout
	if !strings.Contains(showOut, "lock:alpha") || !strings.Contains(showOut, "observada") {
		t.Errorf("unexpected packs show:\n%s", showOut)
	}

	// Remove skill from disk + lock -> pack show keeps reference as not_observed.
	if err := os.RemoveAll(p.home.Path(".agents/skills/alpha")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.home.Path(".claude/skills/alpha")); err != nil {
		t.Fatal(err)
	}
	p.home.WriteFile(".agents/.skill-lock.json", `{"version":3,"skills":{}}`+"\n")

	showUnobs := p.ok("packs", "show", "seguridad").stdout
	if !strings.Contains(showUnobs, "lock:alpha") || !strings.Contains(showUnobs, "no observada") {
		t.Errorf("expected not_observed status in packs show:\n%s", showUnobs)
	}
	if strings.Contains(strings.ToLower(showUnobs), "faltante") || strings.Contains(strings.ToLower(showUnobs), "missing") {
		t.Errorf("packs show must not say missing/faltante:\n%s", showUnobs)
	}

	var showJSON app.PackView
	if err := json.Unmarshal([]byte(p.ok("packs", "show", "seguridad", "--json").stdout), &showJSON); err != nil {
		t.Fatalf("packs show --json: %v", err)
	}
	if len(showJSON.Members) != 1 || showJSON.Members[0].Presence != app.MemberNotObserved {
		t.Fatalf("unexpected packs show --json: %+v", showJSON)
	}

	// Remove member by exact ID.
	p.ok("packs", "remove", "seguridad", "lock:alpha")
	if err := json.Unmarshal([]byte(p.ok("packs", "show", "seguridad", "--json").stdout), &showJSON); err != nil {
		t.Fatalf("packs show --json after remove: %v", err)
	}
	if len(showJSON.Members) != 0 {
		t.Fatalf("expected 0 members after remove, got %+v", showJSON.Members)
	}

	// Restore alpha and verify scan --json and lock are unchanged.
	p.home.WriteFile(".agents/skills/alpha/SKILL.md", "---\nname: alpha\ndescription: Alpha skill\n---\nbody\n")
	p.home.Symlink(".claude/skills/alpha", "../../.agents/skills/alpha")
	p.home.WriteFile(".agents/.skill-lock.json", lockContent)

	scanAfter := p.ok("scan", "--json").stdout
	if scanBefore != scanAfter {
		t.Errorf("scan --json changed:\nbefore:\n%s\nafter:\n%s", scanBefore, scanAfter)
	}
	gotLock, err := os.ReadFile(p.home.Paths().SkillsLockFile())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal([]byte(lockContent), gotLock) {
		t.Errorf("skills lock modified by packs commands")
	}
}

func TestPacksCLIRemoveUnknownAndEmptyRefErrors(t *testing.T) {
	p := newPackMachine(t)
	p.ok("packs", "create", "core")

	for _, ref := range []string{"lock:unknown", ""} {
		res := p.run("packs", "remove", "core", ref)
		if res.code != 2 {
			t.Errorf("packs remove core %q code = %d, want 2", ref, res.code)
		}
		if res.stdout != "" {
			t.Errorf("packs remove core %q stdout = %q, want empty", ref, res.stdout)
		}
		wantErr := "skillhub: no hay ninguna skill «" + ref + "»\n"
		if res.stderr != wantErr {
			t.Errorf("packs remove core %q stderr = %q, want %q", ref, res.stderr, wantErr)
		}
	}
}

func TestPacksCLICorruptSidecarFailsClosedAndPreservesBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"duplicate pack names", `{"version":1,"packs":[{"name":"dup","members":[]},{"name":"dup","members":[]}]}`},
		{"empty member id", `{"version":1,"packs":[{"name":"core","members":[""]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPackMachine(t)
			sidecar := filepath.Join(p.home.Paths().StateDir(), "packs.json")
			if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sidecar, []byte(tc.raw), 0o644); err != nil {
				t.Fatal(err)
			}

			for _, args := range [][]string{
				{"packs", "list"},
				{"packs", "create", "nuevo"},
				{"packs", "show", "dup"},
			} {
				res := p.run(args...)
				if res.code != 2 {
					t.Errorf("%v code = %d, want 2", args, res.code)
				}
				if res.stdout != "" {
					t.Errorf("%v stdout = %q, want empty", args, res.stdout)
				}
				if res.stderr == "" {
					t.Errorf("%v stderr is empty, want localized error", args)
				}
			}

			got, err := os.ReadFile(sidecar)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.raw {
				t.Fatalf("corrupt sidecar changed:\ngot:  %s\nwant: %s", got, tc.raw)
			}
		})
	}
}
