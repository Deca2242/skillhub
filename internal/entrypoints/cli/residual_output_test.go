package cli_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

type failingSettingsStore struct {
	err error
}

func (s failingSettingsStore) Load() (domain.Settings, error) { return domain.Settings{}, s.err }
func (s failingSettingsStore) Save(domain.Settings) error     { return s.err }

func TestResidualDiagnosticsEscapeControlText(t *testing.T) {
	t.Run("ambiguous error escapes ref and ids while preserving layout", func(t *testing.T) {
		f := newFixture(t)
		sharedName := "dup" + hostile + "\nname"
		f.installNamedLock(sharedName, map[string]string{
			"SKILL.md": "---\nname: " + yamlQuote(t, sharedName) + "\ndescription: first\n---\nbody one\n",
		})
		f.home.WriteFile(".claude/skills/"+sharedName+"/SKILL.md",
			"---\nname: "+yamlQuote(t, sharedName)+"\ndescription: second\n---\nbody two\n")
		f.home.WriteFile(".claude/skills/"+sharedName+"/.git/HEAD", "ref: refs/heads/main\n")
		f.git.AddClone(f.home.Path(".claude/skills/"+sharedName), ports.CloneStatus{
			Remote:   "https://github.com/acme/other.git",
			Branch:   "main",
			Upstream: "origin/main",
			Head:     "1111111111111111111111111111111111111111",
			Clean:    true,
		})

		r := f.run(nil, "skills", "show", sharedName)
		if r.code != 2 {
			t.Fatalf("skills show ambiguous exit = %d, want 2\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
		}
		assertNothingActs(t, "ambiguous diagnostic", r.stderr)

		shownRef := "dup" + hostileShown + `\nname`
		want := "skillhub: «" + shownRef + "» corresponde a varias skills; indica una por su id:\n" +
			"  git:.claude/skills/" + shownRef + "\n" +
			"  lock:" + shownRef + "\n"
		if r.stderr != want {
			t.Errorf("ambiguous diagnostic =\n%q\nwant:\n%q", r.stderr, want)
		}
		if strings.Contains(r.stderr, "\nname") {
			t.Errorf("ambiguous ref or id split across lines:\n%q", r.stderr)
		}
	})

	t.Run("generic error diagnostic escapes control bytes and newlines", func(t *testing.T) {
		f := newFixture(t)
		badErr := errors.New("boom" + hostile + "\nforged\tcol")
		newApp := func() (*app.App, error) {
			p := f.home.Paths()
			files := fsadapter.NewFileOps(p.Guard())
			lock := skillslock.New(p, files)
			return app.New(app.Deps{
				Paths:       p,
				Agents:      []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
				Files:       files,
				Dirs:        fsadapter.NewScanner(p.Guard()),
				Lock:        lock,
				ManagedLock: lock,
				Links:       fsadapter.NewLinkOps(p.Guard()),
				State:       statefile.New(p),
				Settings:    failingSettingsStore{err: badErr},
				GitHub:      f.gh,
				Git:         f.git,
				Notifier:    f.notifier,
				Background:  f.bg,
				Clock:       f.clock,
			})
		}

		var stdout, stderr bytes.Buffer
		code := cliRun([]string{"settings", "show"}, &stdout, &stderr, newApp)
		if code != 2 {
			t.Fatalf("settings show failing store exit = %d, want 2\nstderr: %s", code, stderr.String())
		}
		assertNothingActs(t, "generic diagnostic", stderr.String())
		want := "skillhub: error: load settings: boom" + hostileShown + `\nforged\tcol` + "\n"
		if stderr.String() != want {
			t.Errorf("generic diagnostic = %q, want %q", stderr.String(), want)
		}
	})

	t.Run("compose error diagnostic escapes control bytes and newlines", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliRun([]string{"scan"}, &stdout, &stderr, func() (*app.App, error) {
			return nil, errors.New("init" + hostile + "\nforged")
		})
		if code != 2 {
			t.Fatalf("compose failure exit = %d, want 2\nstderr: %s", code, stderr.String())
		}
		assertNothingActs(t, "compose diagnostic", stderr.String())
		want := "skillhub: no se pudo preparar skillhub: init" + hostileShown + `\nforged` + "\n"
		if stderr.String() != want {
			t.Errorf("compose diagnostic = %q, want %q", stderr.String(), want)
		}
	})

	t.Run("interrupted operation warning escapes target and keeps persisted op raw", func(t *testing.T) {
		f := newFixture(t)
		rawTarget := "lock:evil" + hostile + "\nforged"
		f.record(domain.Operation{
			ID:                "op-warn",
			Kind:              domain.OperationApply,
			Target:            rawTarget,
			RestoreSnapshotID: "snap-1",
			StartedAt:         t0,
		})

		r := f.ok("scan")
		assertNothingActs(t, "interrupted warning", r.stderr)
		wantPart := "aviso: hay una operación interrumpida (aplicación de lock:evil" + hostileShown + `\nforged` + ", iniciada el "
		if !strings.Contains(r.stderr, wantPart) {
			t.Errorf("interrupted warning lacks %q:\n%q", wantPart, r.stderr)
		}
		if strings.Contains(r.stderr, "\nforged") {
			t.Errorf("interrupted warning split lines:\n%q", r.stderr)
		}

		// JSON output remains valid and exit code stays 0.
		rj := f.ok("scan", "--json")
		assertNothingActs(t, "interrupted warning on json run", rj.stderr)
		if strings.TrimSpace(rj.stdout) != "[]" {
			t.Errorf("scan --json stdout = %q, want []", rj.stdout)
		}

		// Persisted operation target remains raw.
		a, err := f.newApp()
		if err != nil {
			t.Fatal(err)
		}
		op, ok, err := a.Interrupted()
		if err != nil {
			t.Fatal(err)
		}
		if !ok || op.Target != rawTarget {
			t.Errorf("persisted operation = (%+v, %v), want raw target %q", op, ok, rawTarget)
		}
	})

	t.Run("ordinary diagnostics preserve Spanish text and exit codes", func(t *testing.T) {
		f := newFixture(t)
		f.installLock([]string{"alpha"}, map[string]map[string]string{
			"alpha": {"SKILL.md": "---\nname: alpha\ndescription: first\n---\nbody one\n"},
		})
		f.home.WriteFile(".claude/skills/alpha/SKILL.md",
			"---\nname: alpha\ndescription: second\n---\nbody two\n")
		f.home.WriteFile(".claude/skills/alpha/.git/HEAD", "ref: refs/heads/main\n")
		f.git.AddClone(f.home.Path(".claude/skills/alpha"), ports.CloneStatus{
			Remote:   "https://github.com/acme/other.git",
			Branch:   "main",
			Upstream: "origin/main",
			Head:     "1111111111111111111111111111111111111111",
			Clean:    true,
		})

		r := f.run(nil, "skills", "show", "alpha")
		if r.code != 2 {
			t.Fatalf("ordinary ambiguous exit = %d, want 2", r.code)
		}
		want := "skillhub: «alpha» corresponde a varias skills; indica una por su id:\n  git:.claude/skills/alpha\n  lock:alpha\n"
		if r.stderr != want {
			t.Errorf("ordinary ambiguous stderr = %q, want %q", r.stderr, want)
		}

		var stdout, stderr bytes.Buffer
		code := cli.Run([]string{"scan"}, cli.Env{
			Stdout: &stdout,
			Stderr: &stderr,
			NewApp: func() (*app.App, error) { return nil, errors.New("fallo simple") },
		})
		if code != 2 || stderr.String() != "skillhub: no se pudo preparar skillhub: fallo simple\n" {
			t.Errorf("ordinary compose = (%d, %q)", code, stderr.String())
		}
	})
}

func TestSettingsIgnoreListEscapesDisplayAndPreservesPersistedValues(t *testing.T) {
	f := newFixture(t)
	entry1 := "alpha" + hostile + "\nforged"
	entry2 := "beta\t" + hostile
	rawList := entry1 + "," + entry2
	wantRendered := "Ignoradas (ignore): alpha" + hostileShown + `\nforged` + ", beta" + `\t` + hostileShown + "\n"

	dry := f.ok("settings", "set", "ignore", rawList, "--dry-run")
	assertNothingActs(t, "settings set --dry-run", dry.stdout)
	if !strings.Contains(dry.stdout, wantRendered) || !strings.Contains(dry.stdout, "Simulación: no se cambió nada.\n") {
		t.Errorf("settings set --dry-run output unexpected:\n%q", dry.stdout)
	}
	if strings.Contains(dry.stdout, "\nforged") {
		t.Errorf("settings set --dry-run allowed newline injection:\n%q", dry.stdout)
	}

	setRes := f.ok("settings", "set", "ignore", rawList)
	assertNothingActs(t, "settings set", setRes.stdout)
	if !strings.Contains(setRes.stdout, wantRendered) || !strings.Contains(setRes.stdout, "Ajustes guardados.\n") {
		t.Errorf("settings set output unexpected:\n%q", setRes.stdout)
	}

	// Persisted settings values must remain exact raw strings.
	a, err := f.newApp()
	if err != nil {
		t.Fatal(err)
	}
	gotSettings, err := a.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotSettings.Ignore, []string{entry1, entry2}) {
		t.Errorf("persisted Ignore = %q, want %q", gotSettings.Ignore, []string{entry1, entry2})
	}

	show := f.ok("settings", "show")
	assertNothingActs(t, "settings show", show.stdout)
	if !strings.Contains(show.stdout, wantRendered) {
		t.Errorf("settings show lacks %q:\n%q", wantRendered, show.stdout)
	}
	if strings.Contains(show.stdout, "\nforged") {
		t.Errorf("settings show allowed newline injection:\n%q", show.stdout)
	}

	// Regression: ordinary and empty ignore lists keep expected Spanish output.
	f.ok("settings", "set", "ignore", "uno,dos")
	if got := f.ok("settings", "show").stdout; !strings.Contains(got, "Ignoradas (ignore): uno, dos\n") {
		t.Errorf("ordinary settings show = %q", got)
	}
	f.ok("settings", "set", "ignore", "")
	if got := f.ok("settings", "show").stdout; !strings.Contains(got, "Ignoradas (ignore): —\n") {
		t.Errorf("empty settings show = %q", got)
	}
}
