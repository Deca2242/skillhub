package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/skillssh"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// runCatalog runs the command line over an App that searches catalog.
func (f *fixture) runCatalog(catalog ports.Catalog, args ...string) result {
	f.t.Helper()
	newApp := func() (*app.App, error) {
		p := f.home.Paths()
		files := fsadapter.NewFileOps(p.Guard())
		lock := skillslock.New(p, files)
		return app.New(app.Deps{
			Paths:  p,
			Agents: []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
			Files:  files, Dirs: fsadapter.NewScanner(p.Guard()), Lock: lock,
			ManagedLock: lock, Links: fsadapter.NewLinkOps(p.Guard()),
			State: statefile.New(p), Settings: settings.New(p), GitHub: f.gh, Git: f.git,
			Notifier: f.notifier, Background: f.bg, Clock: f.clock,
			Catalog: catalog,
		})
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, cli.Env{
		Stdout: &stdout, Stderr: &stderr, NewApp: newApp,
		TUI: func(_, _ io.Writer) int { return 0 },
	})
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func catalogSkill(source, skillID string, installs int) ports.CatalogSkill {
	return ports.CatalogSkill{ID: source + "/" + skillID, Source: source, SkillID: skillID, Name: skillID, Installs: installs}
}

func reactCatalog() *fakes.Catalog {
	c := fakes.NewCatalog()
	c.Set("react hooks",
		catalogSkill("vercel-labs/agent-skills", "react-best", 48213),
		catalogSkill("acme/skills", "alpha", 1204),
		ports.CatalogSkill{ID: "mintlify.com/mintlify", Source: "mintlify.com", SkillID: "mintlify", Name: "Mintlify", Installs: 5},
	)
	return c
}

func TestSkillsSearchPrintsATableAndHowToInstall(t *testing.T) {
	f := newFixture(t)
	f.installLock([]string{"alpha"}, map[string]map[string]string{"alpha": {"SKILL.md": "---\nname: alpha\ndescription: a\n---\n"}})
	catalog := reactCatalog()
	r := f.runCatalog(catalog, "skills", "search", "react", "hooks")
	want := "" +
		"NOMBRE      ORIGEN                    INSTALACIONES  ESTADO\n" +
		"react-best  vercel-labs/agent-skills  48 213         \n" +
		"alpha       acme/skills               1204           instalada\n" +
		"Mintlify    mintlify.com              5              sitio web (no es GitHub)\n" +
		"\n" +
		"Para instalar: skillhub skills install <origen>, con owner/repo@skill para GitHub o https://sitio@skill para un sitio web\n" +
		"Por ejemplo: skillhub skills install vercel-labs/agent-skills@react-best\n"
	if r.code != 0 || r.stdout != want || r.stderr != "" {
		t.Errorf("search = exit %d\nstdout:\n%s\nwant:\n%s\nstderr: %q", r.code, r.stdout, want, r.stderr)
	}
	if calls := catalog.Calls(); !slices.Equal(calls, []fakes.CatalogCall{{Query: "react hooks", Limit: app.DefaultCatalogLimit}}) {
		t.Errorf("calls = %v", calls)
	}
}

func TestSkillsSearchJSON(t *testing.T) {
	f := newFixture(t)
	r := f.runCatalog(reactCatalog(), "skills", "search", "react hooks", "--json", "--limit", "2")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("search --json = exit %d, stderr %q", r.code, r.stderr)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (--limit)", len(got))
	}
	want := map[string]any{
		"id": "vercel-labs/agent-skills/react-best", "source": "vercel-labs/agent-skills", "skill_id": "react-best",
		"name": "react-best", "installs": float64(48213), "installable": true,
		"install_source": "vercel-labs/agent-skills@react-best", "installed": false,
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("entry[0][%q] = %v, want %v", k, got[0][k], v)
		}
	}
	if len(got[0]) != len(want) {
		t.Errorf("entry[0] keys = %v, want exactly %v", got[0], want)
	}
}

func TestSkillsSearchEmptyJSONIsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	r := f.runCatalog(fakes.NewCatalog(), "skills", "search", "zz", "--json")
	if r.code != 0 || r.stdout != "[]\n" {
		t.Errorf("search --json with no results = exit %d, stdout %q", r.code, r.stdout)
	}
	r = f.runCatalog(fakes.NewCatalog(), "skills", "search", "zz")
	if r.code != 0 || r.stdout != "skills.sh no devolvió resultados para «zz».\n" {
		t.Errorf("search with no results = exit %d, stdout %q", r.code, r.stdout)
	}
}

func TestSkillsSearchRefusesABadLimitOrQuery(t *testing.T) {
	for _, tc := range []struct {
		args []string
		msg  string
	}{
		{[]string{"skills", "search", "react", "--limit", "0"}, "--limit debe estar entre 1 y 100 (se recibió 0)"},
		{[]string{"skills", "search", "react", "--limit", "101"}, "--limit debe estar entre 1 y 100 (se recibió 101)"},
		{[]string{"skills", "popular", "--limit", "-1"}, "--limit debe estar entre 1 y 100 (se recibió -1)"},
		{[]string{"skills", "search", "r"}, "la búsqueda necesita al menos dos caracteres"},
		{[]string{"skills", "search"}, "necesita al menos 1 argumento"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f := newFixture(t)
			catalog := fakes.NewCatalog()
			r := f.runCatalog(catalog, tc.args...)
			if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, tc.msg) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 and %q", r.code, r.stdout, r.stderr, tc.msg)
			}
			if calls := catalog.Calls(); len(calls) != 0 {
				t.Errorf("refused command reached skills.sh: %v", calls)
			}
		})
	}
}

func TestSkillsSearchExplainsFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		msg  string
	}{
		{"curl missing", &skillssh.Error{Reason: skillssh.ReasonCurlMissing}, "skillhub: no se pudo ejecutar curl"},
		{"offline", &skillssh.Error{Reason: skillssh.ReasonOffline}, "skillhub: no se pudo conectar con skills.sh; revisa la conexión a internet"},
		{"rate limited", &skillssh.Error{Reason: skillssh.ReasonRateLimited, Status: 429}, "skillhub: skills.sh alcanzó su límite de uso"},
		{"server error", &skillssh.Error{Reason: skillssh.ReasonHTTP, Status: 503}, "skillhub: skills.sh respondió con un error (HTTP 503)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			catalog := fakes.NewCatalog()
			catalog.Fail("react", tc.err)
			r := f.runCatalog(catalog, "skills", "search", "react")
			if r.code != 2 || r.stdout != "" || !strings.HasPrefix(r.stderr, tc.msg) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 and %q", r.code, r.stdout, r.stderr, tc.msg)
			}
		})
	}
}

func TestSkillsSearchShowsTheCacheWithAWarningWhenOffline(t *testing.T) {
	f := newFixture(t)
	catalog := reactCatalog()
	f.ok("scan") // the home exists before the first search.
	if r := f.runCatalog(catalog, "skills", "search", "react hooks"); r.code != 0 {
		t.Fatalf("first search = exit %d, %s", r.code, r.stderr)
	}
	f.clock.Advance(2 * time.Hour)
	catalog.Fail("react hooks", &skillssh.Error{Reason: skillssh.ReasonOffline})
	r := f.runCatalog(catalog, "skills", "search", "react hooks")
	if r.code != 0 || !strings.Contains(r.stdout, "react-best") {
		t.Errorf("offline search = exit %d, stdout %q; want the cached table", r.code, r.stdout)
	}
	if !strings.HasPrefix(r.stderr, "Aviso: no se pudo conectar con skills.sh") || !strings.Contains(r.stderr, "resultados guardados") {
		t.Errorf("stderr = %q, want the stale warning", r.stderr)
	}
}

func TestSkillsSearchRefreshAsksAgain(t *testing.T) {
	f := newFixture(t)
	catalog := reactCatalog()
	f.runCatalog(catalog, "skills", "search", "react hooks")
	f.runCatalog(catalog, "skills", "search", "react hooks")
	if n := len(catalog.Calls()); n != 1 {
		t.Fatalf("calls after two searches within the hour = %d, want 1", n)
	}
	if r := f.runCatalog(catalog, "skills", "search", "react hooks", "--refresh"); r.code != 0 {
		t.Fatalf("search --refresh = exit %d, %s", r.code, r.stderr)
	}
	if n := len(catalog.Calls()); n != 2 {
		t.Errorf("calls after --refresh = %d, want 2", n)
	}
}

func TestSkillsPopular(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	catalog.Set("react", catalogSkill("vercel/skills", "react-best", 500), catalogSkill("acme/skills", "alpha", 10))
	catalog.Set("agent", catalogSkill("other/repo", "agent-kit", 300))
	r := f.runCatalog(catalog, "skills", "popular", "--limit", "2")
	want := "" +
		"NOMBRE      ORIGEN         INSTALACIONES  ESTADO\n" +
		"react-best  vercel/skills  500            \n" +
		"agent-kit   other/repo     300            \n" +
		"\n" +
		"Para instalar: skillhub skills install <origen>, con owner/repo@skill para GitHub o https://sitio@skill para un sitio web\n" +
		"Por ejemplo: skillhub skills install vercel/skills@react-best\n"
	if r.code != 0 || r.stdout != want || r.stderr != "" {
		t.Errorf("popular = exit %d\nstdout:\n%s\nwant:\n%s\nstderr: %q", r.code, r.stdout, want, r.stderr)
	}
	if q := catalog.Queries(); len(q) != len(app.PopularTopics) {
		t.Errorf("popular asked %d topics, want %d", len(q), len(app.PopularTopics))
	}
	r = f.runCatalog(catalog, "skills", "popular", "--json")
	var got []app.CatalogEntry
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || len(got) != 3 {
		t.Errorf("popular --json = %d entries, %v", len(got), err)
	}
}

func TestSkillsSearchHelpSaysItOnlyReads(t *testing.T) {
	f := newFixture(t)
	for _, cmd := range []string{"search", "popular"} {
		r := f.runCatalog(fakes.NewCatalog(), "skills", cmd, "--help")
		if r.code != 0 || !strings.Contains(r.stdout, "Solo lee: no instala nada") ||
			!strings.Contains(r.stdout, "skillhub skills install <origen>") || !strings.Contains(r.stdout, "https://sitio@skill") ||
			!strings.Contains(r.stdout, "--refresh") {
			t.Errorf("skills %s --help = exit %d\n%s", cmd, r.code, r.stdout)
		}
	}
	r := f.runCatalog(fakes.NewCatalog(), "skills", "--help")
	if !strings.Contains(r.stdout, "search") || !strings.Contains(r.stdout, "popular") {
		t.Errorf("skills --help does not list search and popular:\n%s", r.stdout)
	}
}

func TestSkillsSearchWithoutACatalogFails(t *testing.T) {
	f := newFixture(t)
	r := f.run(nil, "skills", "search", "react")
	if r.code != 2 || !strings.Contains(r.stderr, "missing dependency") {
		t.Errorf("search without a catalog = exit %d, stderr %q", r.code, r.stderr)
	}
}
