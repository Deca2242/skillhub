package app_test

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/skillssh"
	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// withCatalog builds a second App over the fixture's home and doubles, with
// catalog as its skills.sh port.
func (f *fixture) withCatalog(catalog ports.Catalog) *app.App {
	f.t.Helper()
	p := f.home.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	a, err := app.New(app.Deps{
		Paths:  p,
		Agents: []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Files:  files, Dirs: fsadapter.NewScanner(p.Guard()), Lock: skillslock.New(p, files),
		State: f.state, Settings: f.settings, GitHub: f.gh, Git: f.git,
		Notifier: f.notifier, Background: f.bg, Clock: f.clock,
		Catalog: catalog,
	})
	if err != nil {
		f.t.Fatalf("app.New: %v", err)
	}
	return a
}

func skill(source, skillID string, installs int) ports.CatalogSkill {
	id := source + "/" + skillID
	return ports.CatalogSkill{ID: id, Source: source, SkillID: skillID, Name: skillID, Installs: installs}
}

func ids(entries []app.CatalogEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

func TestHasCatalogReportsWhetherThePortIsConfigured(t *testing.T) {
	f := newFixture(t)
	var noApp *app.App
	if noApp.HasCatalog() || f.app.HasCatalog() {
		t.Error("HasCatalog reported an unconfigured catalog")
	}
	if !f.withCatalog(fakes.NewCatalog()).HasCatalog() {
		t.Error("HasCatalog did not report a configured catalog")
	}
}

func TestCatalogNeedsTheCatalogPort(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{}); !errors.Is(err, app.ErrMissingDependency) {
		t.Errorf("SearchCatalog without a catalog = %v, want ErrMissingDependency", err)
	}
	if _, err := f.app.PopularCatalog(t.Context(), app.CatalogOptions{}); !errors.Is(err, app.ErrMissingDependency) {
		t.Errorf("PopularCatalog without a catalog = %v, want ErrMissingDependency", err)
	}
}

func TestSearchCatalogMarksInstallableAndInstalledEntries(t *testing.T) {
	f := newFixture(t)
	// alpha is in the lock and in the store; beta is only a store folder.
	f.installLock(map[string]map[string]string{"alpha": v1})
	f.home.WriteFile(".agents/skills/beta/SKILL.md", "---\nname: beta\ndescription: b\n---\n")
	catalog := fakes.NewCatalog()
	catalog.Set("react",
		skill("acme/skills", "alpha", 900),
		skill("acme/skills", "beta", 800),
		skill("vercel-labs/agent-skills", "react-best", 700),
		ports.CatalogSkill{ID: "mintlify.com/mintlify", Source: "mintlify.com", SkillID: "mintlify", Name: "Mintlify", Installs: 5},
		skill("acme/skills", "1.2.3", 4),
	)
	before := f.homeTree()

	res, err := f.withCatalog(catalog).SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
	if err != nil {
		t.Fatalf("SearchCatalog = %v", err)
	}
	want := []app.CatalogEntry{
		{ID: "acme/skills/alpha", Source: "acme/skills", SkillID: "alpha", Name: "alpha", Installs: 900,
			Installable: true, InstallSource: "acme/skills@alpha", Installed: true, InstalledSource: "acme/skills"},
		{ID: "acme/skills/beta", Source: "acme/skills", SkillID: "beta", Name: "beta", Installs: 800,
			Installable: true, InstallSource: "acme/skills@beta", PossibleDuplicate: true, DuplicateOf: "beta"},
		{ID: "vercel-labs/agent-skills/react-best", Source: "vercel-labs/agent-skills", SkillID: "react-best",
			Name: "react-best", Installs: 700, Installable: true, InstallSource: "vercel-labs/agent-skills@react-best"},
		// A website host installs through its /.well-known/agent-skills/
		// index (L84).
		{ID: "mintlify.com/mintlify", Source: "mintlify.com", SkillID: "mintlify", Name: "Mintlify", Installs: 5,
			Installable: true, InstallSource: "https://mintlify.com@mintlify"},
		// A skill id that reads as a version pin would install a ref, not
		// this skill.
		{ID: "acme/skills/1.2.3", Source: "acme/skills", SkillID: "1.2.3", Name: "1.2.3", Installs: 4,
			InstallSource: "acme/skills@1.2.3"},
	}
	if !slices.Equal(res.Entries, want) {
		t.Errorf("entries =\n%+v\nwant\n%+v", res.Entries, want)
	}
	if res.Stale != nil || res.CacheErr != nil {
		t.Errorf("Stale = %v, CacheErr = %v; want neither", res.Stale, res.CacheErr)
	}
	if calls := catalog.Calls(); !slices.Equal(calls, []fakes.CatalogCall{{Query: "react", Limit: 10}}) {
		t.Errorf("calls = %v", calls)
	}
	// Searching installs nothing: the home is the same outside skillhub's
	// own cache.
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Errorf("SearchCatalog changed the home")
	}
}

func TestSearchCatalogRefusesAShortQuery(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	for _, q := range []string{"", "  ", "r"} {
		if _, err := f.withCatalog(catalog).SearchCatalog(t.Context(), q, 10, app.CatalogOptions{}); !errors.Is(err, app.ErrCatalogQueryTooShort) {
			t.Errorf("SearchCatalog(%q) = %v, want ErrCatalogQueryTooShort", q, err)
		}
	}
	if calls := catalog.Calls(); len(calls) != 0 {
		t.Errorf("a short query reached skills.sh: %v", calls)
	}
}

func TestSearchCatalogDefaultsAndBoundsTheLimit(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	a := f.withCatalog(catalog)
	for _, limit := range []int{0, -3, 500} {
		if _, err := a.SearchCatalog(t.Context(), fmt.Sprintf("q%d", limit), limit, app.CatalogOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var limits []int
	for _, c := range catalog.Calls() {
		limits = append(limits, c.Limit)
	}
	if want := []int{app.DefaultCatalogLimit, app.DefaultCatalogLimit, 100}; !slices.Equal(limits, want) {
		t.Errorf("limits = %v, want %v", limits, want)
	}
}

func TestSearchCatalogCachesEachQueryForAnHour(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	catalog.Set("react", skill("acme/skills", "alpha", 3))
	a := f.withCatalog(catalog)

	first, err := a.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.home.Paths().CatalogCacheFile())
	if err != nil || !strings.Contains(string(data), `"alpha"`) {
		t.Fatalf("cache file = %q, %v; want the search stored", data, err)
	}
	// skills.sh changes, but within the hour the cache answers.
	catalog.Set("react", skill("acme/skills", "gamma", 9))
	f.clock.Advance(59 * time.Minute)
	second, err := a.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
	if err != nil || !slices.Equal(ids(second.Entries), ids(first.Entries)) {
		t.Errorf("second search = %v, %v; want the cached %v", ids(second.Entries), err, ids(first.Entries))
	}
	if n := len(catalog.Calls()); n != 1 {
		t.Errorf("skills.sh was asked %d times within the hour, want 1", n)
	}
	// Another limit is another query.
	if _, err := a.SearchCatalog(t.Context(), "react", 5, app.CatalogOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(catalog.Calls()); n != 2 {
		t.Errorf("calls after another limit = %d, want 2", n)
	}
	// After the hour, skills.sh is asked again.
	f.clock.Advance(2 * time.Minute)
	third, err := a.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
	if err != nil || !slices.Equal(ids(third.Entries), []string{"acme/skills/gamma"}) {
		t.Errorf("search after the hour = %v, %v; want the new answer", ids(third.Entries), err)
	}
}

func TestSearchCatalogRefreshIgnoresTheCache(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	catalog.Set("react", skill("acme/skills", "alpha", 3))
	a := f.withCatalog(catalog)
	if _, err := a.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{}); err != nil {
		t.Fatal(err)
	}
	catalog.Set("react", skill("acme/skills", "gamma", 9))
	res, err := a.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{Refresh: true})
	if err != nil || !slices.Equal(ids(res.Entries), []string{"acme/skills/gamma"}) {
		t.Errorf("refreshed search = %v, %v; want the new answer", ids(res.Entries), err)
	}
	// The refreshed answer is what the cache holds now.
	catalog.Set("react")
	res, err = a.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
	if err != nil || !slices.Equal(ids(res.Entries), []string{"acme/skills/gamma"}) {
		t.Errorf("search after a refresh = %v, %v; want the refreshed cache", ids(res.Entries), err)
	}
}

func TestSearchCatalogFallsBackToTheCacheWhenSkillsShFails(t *testing.T) {
	offline := &skillssh.Error{Reason: skillssh.ReasonOffline}
	for _, tc := range []struct {
		name    string
		advance time.Duration
		opts    app.CatalogOptions
	}{
		{"expired cache", 2 * time.Hour, app.CatalogOptions{}},
		{"refresh", 0, app.CatalogOptions{Refresh: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			catalog := fakes.NewCatalog()
			catalog.Set("react", skill("acme/skills", "alpha", 3))
			a := f.withCatalog(catalog)
			if _, err := a.SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{}); err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(tc.advance)
			catalog.Fail("react", offline)
			res, err := a.SearchCatalog(t.Context(), "react", 10, tc.opts)
			if err != nil {
				t.Fatalf("SearchCatalog = %v, want the cache", err)
			}
			if !slices.Equal(ids(res.Entries), []string{"acme/skills/alpha"}) {
				t.Errorf("entries = %v, want the cached ones", ids(res.Entries))
			}
			if !errors.Is(res.Stale, app.ErrCatalogOffline) {
				t.Errorf("Stale = %v, want ErrCatalogOffline", res.Stale)
			}
			if !res.CachedAt.Equal(t0) {
				t.Errorf("CachedAt = %v, want %v", res.CachedAt, t0)
			}
		})
	}
}

func TestSearchCatalogWithoutCacheReportsTheFailure(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	catalog.Fail("react", &skillssh.Error{Reason: skillssh.ReasonOffline})
	_, err := f.withCatalog(catalog).SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
	if !errors.Is(err, app.ErrCatalogOffline) {
		t.Errorf("SearchCatalog = %v, want ErrCatalogOffline", err)
	}
	if _, statErr := os.Stat(f.home.Paths().CatalogCacheFile()); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a failed search wrote the cache: %v", statErr)
	}
}

func TestSearchCatalogIgnoresACorruptCache(t *testing.T) {
	f := newFixture(t)
	cache := f.home.Paths().CatalogCacheFile()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := fakes.NewCatalog()
	catalog.Set("react", skill("acme/skills", "alpha", 3))
	res, err := f.withCatalog(catalog).SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
	if err != nil || len(res.Entries) != 1 {
		t.Fatalf("SearchCatalog over a corrupt cache = %v, %v", res.Entries, err)
	}
	data, err := os.ReadFile(f.home.Paths().CatalogCacheFile())
	if err != nil || !strings.Contains(string(data), `"alpha"`) {
		t.Errorf("cache file = %q, %v; want it rewritten", data, err)
	}
}

// The facade's catalog errors come from the real adapter's reasons, over
// recorded curl results.
func TestCatalogErrorsFromTheAdapter(t *testing.T) {
	const url = "https://skills.sh/api/search?limit=10&q=react"
	args := []string{"-q", "--silent", "--show-error", "--fail", "--max-time", "15", "--proto", "=https", "--location", "--max-redirs", "0", url}
	for _, tc := range []struct {
		name   string
		res    ports.Result
		runErr error
		want   error
		status int
	}{
		{"curl missing", ports.Result{ExitCode: -1}, errors.New(`exec: "curl": executable file not found in $PATH`), app.ErrCatalogCurlMissing, 0},
		{"offline", ports.Result{ExitCode: 6}, nil, app.ErrCatalogOffline, 0},
		{"rate limited", ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 429")}, nil, app.ErrCatalogRateLimited, 429},
		{"server error", ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 503")}, nil, app.ErrCatalogHTTP, 503},
		{"bad response", ports.Result{Stdout: []byte("<html>")}, nil, app.ErrCatalogBadResponse, 0},
		{"other failure", ports.Result{ExitCode: 97}, nil, app.ErrCatalogFailed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			runner := fakes.NewProcessRunner()
			runner.Expect(ports.Command{Name: "curl", Args: args}, tc.res, tc.runErr)
			client, err := skillssh.New(runner, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.withCatalog(client).SearchCatalog(t.Context(), "react", 10, app.CatalogOptions{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("SearchCatalog = %v, want %v", err, tc.want)
			}
			ce, ok := errors.AsType[*app.CatalogError](err)
			if !ok || ce.Status != tc.status {
				t.Errorf("CatalogError = %+v, %v; want status %d", ce, ok, tc.status)
			}
		})
	}
}

func TestPopularCatalogMergesTheTopicsInSeries(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	catalog.SetDelay(2 * time.Millisecond)
	catalog.Set("react", skill("acme/skills", "alpha", 10), skill("vercel/skills", "react-best", 500))
	catalog.Set("testing", skill("acme/skills", "alpha", 10), skill("acme/skills", "tdd", 300))
	catalog.Set("agent", skill("other/repo", "agent-kit", 300))
	catalog.Set("docs", ports.CatalogSkill{ID: "mintlify.com/mintlify", Source: "mintlify.com", SkillID: "mintlify", Name: "Mintlify", Installs: 40})

	res, err := f.withCatalog(catalog).PopularCatalog(t.Context(), app.CatalogOptions{})
	if err != nil {
		t.Fatalf("PopularCatalog = %v", err)
	}
	topics := []string{"react", "testing", "python", "docs", "design", "git", "typescript", "security", "data", "agent"}
	if q := catalog.Queries(); !slices.Equal(q, topics) {
		t.Errorf("queries = %q, want %q", q, topics)
	}
	if n := catalog.MaxInFlight(); n != 1 {
		t.Errorf("%d searches overlapped, want them one after another", n)
	}
	// Merged by id, most installs first, ties by id.
	want := []string{"vercel/skills/react-best", "acme/skills/tdd", "other/repo/agent-kit", "mintlify.com/mintlify", "acme/skills/alpha"}
	if got := ids(res.Entries); !slices.Equal(got, want) {
		t.Fatalf("popular = %q, want %q", got, want)
	}
	if !res.Entries[3].Installable || res.Entries[3].InstallSource != "https://mintlify.com@mintlify" || !res.Entries[0].Installable {
		t.Errorf("installable flags = %+v", res.Entries)
	}
	// The second time, every topic comes from the cache.
	if _, err := f.withCatalog(catalog).PopularCatalog(t.Context(), app.CatalogOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(catalog.Calls()); n != len(topics) {
		t.Errorf("calls after a cached popular = %d, want %d", n, len(topics))
	}
}

func TestPopularCatalogStopsAtTheFirstFailure(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	catalog.Fail("python", &skillssh.Error{Reason: skillssh.ReasonRateLimited, Status: 429})
	_, err := f.withCatalog(catalog).PopularCatalog(t.Context(), app.CatalogOptions{})
	if !errors.Is(err, app.ErrCatalogRateLimited) {
		t.Errorf("PopularCatalog = %v, want ErrCatalogRateLimited", err)
	}
	if q := catalog.Queries(); !slices.Equal(q, []string{"react", "testing", "python"}) {
		t.Errorf("queries = %q; a rate limit must stop the series", q)
	}
}

func TestPopularCatalogUsesStaleTopicsWithAWarning(t *testing.T) {
	f := newFixture(t)
	catalog := fakes.NewCatalog()
	catalog.Set("git", skill("acme/skills", "gitflow", 50))
	a := f.withCatalog(catalog)
	if _, err := a.PopularCatalog(t.Context(), app.CatalogOptions{}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(3 * time.Hour)
	catalog.Fail("git", &skillssh.Error{Reason: skillssh.ReasonOffline})
	res, err := a.PopularCatalog(t.Context(), app.CatalogOptions{})
	if err != nil {
		t.Fatalf("PopularCatalog = %v", err)
	}
	if !errors.Is(res.Stale, app.ErrCatalogOffline) || !slices.Equal(ids(res.Entries), []string{"acme/skills/gitflow"}) {
		t.Errorf("popular = %v, stale %v; want the cached gitflow with a warning", ids(res.Entries), res.Stale)
	}
}

func TestSearchCatalogMatchesByOriginAndFlagsPossibleDuplicates(t *testing.T) {
	t.Run("lock entry distinguishes same origin from another origin", func(t *testing.T) {
		f := newFixture(t)
		f.installLock(map[string]map[string]string{"alpha": v1})
		catalog := fakes.NewCatalog()
		catalog.Set("alpha",
			skill("acme/skills", "alpha", 900),
			skill("other/repo", "alpha", 800),
			skill("acme/skills", "other-skill", 700),
		)

		res, err := f.withCatalog(catalog).SearchCatalog(t.Context(), "alpha", 10, app.CatalogOptions{})
		if err != nil {
			t.Fatalf("SearchCatalog = %v", err)
		}
		want := []app.CatalogEntry{
			{ID: "acme/skills/alpha", Source: "acme/skills", SkillID: "alpha", Name: "alpha", Installs: 900,
				Installable: true, InstallSource: "acme/skills@alpha", Installed: true, InstalledSource: "acme/skills"},
			{ID: "other/repo/alpha", Source: "other/repo", SkillID: "alpha", Name: "alpha", Installs: 800,
				Installable: true, InstallSource: "other/repo@alpha", PossibleDuplicate: true, DuplicateOf: "alpha", DuplicateSource: "acme/skills"},
			{ID: "acme/skills/other-skill", Source: "acme/skills", SkillID: "other-skill", Name: "other-skill", Installs: 700,
				Installable: true, InstallSource: "acme/skills@other-skill"},
		}
		if !slices.Equal(res.Entries, want) {
			t.Errorf("entries =\n%+v\nwant\n%+v", res.Entries, want)
		}
	})

	t.Run("git clone remote normalizes to owner/repo", func(t *testing.T) {
		for _, remote := range []string{
			"https://github.com/Owner/Repo",
			"https://github.com/owner/repo.git",
			"https://github.com/owner/repo/",
			"git@github.com:owner/repo.git",
			"ssh://git@github.com/owner/repo.git",
		} {
			t.Run(remote, func(t *testing.T) {
				f := newFixture(t)
				clone := f.home.Path(".claude/skills/clone-skill")
				f.home.WriteFile(".claude/skills/clone-skill/SKILL.md", "---\nname: clone-skill\ndescription: c\n---\n")
				f.home.WriteFile(".claude/skills/clone-skill/.git/HEAD", "ref: refs/heads/main\n")
				f.git.AddClone(clone, ports.CloneStatus{
					Remote: remote, Branch: "main", Upstream: "origin/main", Head: "abc123", Clean: true,
				})

				catalog := fakes.NewCatalog()
				catalog.Set("clone",
					skill("owner/repo", "clone-skill", 500),
					skill("different/repo", "clone-skill", 400),
				)
				res, err := f.withCatalog(catalog).SearchCatalog(t.Context(), "clone", 10, app.CatalogOptions{})
				if err != nil {
					t.Fatalf("SearchCatalog = %v", err)
				}
				if !res.Entries[0].Installed || res.Entries[0].InstalledSource != "owner/repo" || res.Entries[0].PossibleDuplicate {
					t.Errorf("same-repo clone entry = %+v, want Installed=true, InstalledSource=owner/repo", res.Entries[0])
				}
				if res.Entries[1].Installed || !res.Entries[1].PossibleDuplicate || res.Entries[1].DuplicateOf != "clone-skill" || res.Entries[1].DuplicateSource != "owner/repo" {
					t.Errorf("different-repo clone entry = %+v, want PossibleDuplicate=true, DuplicateOf=clone-skill, DuplicateSource=owner/repo", res.Entries[1])
				}
			})
		}
	})

	t.Run("alias and gitlab remote do not falsely match github", func(t *testing.T) {
		f := newFixture(t)
		f.installLock(map[string]map[string]string{"alpha": v1})
		if err := os.Symlink("alpha", f.home.Path(".agents/skills/alpha-alias")); err != nil {
			t.Fatal(err)
		}
		clone := f.home.Path(".claude/skills/gl-skill")
		f.home.WriteFile(".claude/skills/gl-skill/SKILL.md", "---\nname: gl-skill\ndescription: g\n---\n")
		f.home.WriteFile(".claude/skills/gl-skill/.git/HEAD", "ref: refs/heads/main\n")
		f.git.AddClone(clone, ports.CloneStatus{
			Remote: "https://gitlab.com/owner/repo.git", Branch: "main", Upstream: "origin/main", Head: "def456", Clean: true,
		})

		catalog := fakes.NewCatalog()
		catalog.Set("alias",
			skill("other/repo", "alpha-alias", 300),
			skill("owner/repo", "gl-skill", 200),
		)
		res, err := f.withCatalog(catalog).SearchCatalog(t.Context(), "alias", 10, app.CatalogOptions{})
		if err != nil {
			t.Fatalf("SearchCatalog = %v", err)
		}
		if res.Entries[0].Installed || !res.Entries[0].PossibleDuplicate || res.Entries[0].DuplicateOf != "alpha" || res.Entries[0].DuplicateSource != "acme/skills" {
			t.Errorf("alias entry = %+v, want PossibleDuplicate of alpha from acme/skills", res.Entries[0])
		}
		if res.Entries[1].Installed || !res.Entries[1].PossibleDuplicate || res.Entries[1].DuplicateOf != "gl-skill" || res.Entries[1].DuplicateSource != "" {
			t.Errorf("gitlab entry = %+v, want PossibleDuplicate of gl-skill with no recorded origin", res.Entries[1])
		}
	})
}
