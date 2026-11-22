package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/adapters/skillssh"
	"skillhub/internal/app"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// runFiles runs the binary's entry with files standing in for the terminal;
// HOME is the empty fake home testsupport.Main set up.
func runFiles(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	open := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	in, out, errOut := open("stdin"), open("stdout"), open("stderr")
	code = run(args, in, out, errOut)
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return read("stdout"), read("stderr"), code
}

func TestComposeBuildsTheFacadeOverTheRealAdapters(t *testing.T) {
	a, err := compose()
	if err != nil {
		t.Fatalf("compose() = %v", err)
	}
	skills, err := a.Skills(t.Context())
	if err != nil || len(skills) != 0 {
		t.Errorf("Skills() on an empty home = %v, %v", skills, err)
	}
}

func TestComposeWiresTheCatalog(t *testing.T) {
	a, err := compose()
	if err != nil {
		t.Fatalf("compose() = %v", err)
	}
	// A short query is refused before curl runs, so this reaches no tool:
	// without a catalog it would be ErrMissingDependency instead.
	_, err = a.SearchCatalog(t.Context(), "r", 5, app.CatalogOptions{})
	if !errors.Is(err, app.ErrCatalogQueryTooShort) {
		t.Errorf("SearchCatalog(r) = %v, want ErrCatalogQueryTooShort", err)
	}
}

// A website install reads the site through curl on the one process runner:
// here a fake one that answers HTTP 404 for both index places, so nothing
// reaches the network and the refusal is that the site publishes no index.
func TestComposeWiresTheWebsiteFetcher(t *testing.T) {
	runner := fakes.NewProcessRunner()
	for _, wk := range []string{"agent-skills", "skills"} {
		runner.Expect(ports.Command{Name: "curl", Args: []string{
			"-q", "--silent", "--show-error", "--fail", "--max-time", "30", "--proto", "=https", "--location", "--max-redirs", "0",
			"--max-filesize", "1048576", "--globoff", "https://uizze.sh/.well-known/" + wk + "/index.json",
		}}, ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 404")}, nil)
	}
	a, err := composeWith(func(paths.Paths) ports.ProcessRunner { return runner })
	if err != nil {
		t.Fatalf("composeWith() = %v", err)
	}
	if _, err := a.ResolveInstall(t.Context(), "https://uizze.sh@ui-taste"); !errors.Is(err, app.ErrNoWellKnown) {
		t.Errorf("ResolveInstall = %v, want ErrNoWellKnown", err)
	}
	if unused := runner.Unused(); len(unused) > 0 {
		t.Errorf("not run %q, ran %v", unused, runner.Commands())
	}
}

func TestCatalogBaseURLComesFromSkillsAPIURL(t *testing.T) {
	args := []string{"-q", "--silent", "--show-error", "--fail", "--max-time", "15", "--proto", "=https", "--location", "--max-redirs", "0"}
	for _, tc := range []struct{ env, url string }{
		{"", "https://skills.sh/api/search?limit=5&q=go"},
		{"https://mirror.example/", "https://mirror.example/api/search?limit=5&q=go"},
	} {
		runner := fakes.NewProcessRunner()
		runner.Expect(ports.Command{Name: "curl", Args: append(slices.Clone(args), tc.url)},
			ports.Result{Stdout: []byte(`{"skills":[]}`)}, nil)
		catalog := catalogFor(runner, func(key string) string {
			if key == "SKILLS_API_URL" {
				return tc.env
			}
			return ""
		})
		if _, err := catalog.Search(t.Context(), "go", 5); err != nil {
			t.Errorf("SKILLS_API_URL=%q: Search = %v", tc.env, err)
		}
		if unused := runner.Unused(); len(unused) > 0 {
			t.Errorf("SKILLS_API_URL=%q: not run %q, ran %v", tc.env, unused, runner.Commands())
		}
	}
}

func TestAnUnsafeSkillsAPIURLFailsOnlyTheCatalog(t *testing.T) {
	runner := fakes.NewProcessRunner()
	catalog := catalogFor(runner, func(string) string { return "http://plain.example" })
	_, err := catalog.Search(t.Context(), "go", 5)
	if !errors.Is(err, skillssh.ErrInvalidBaseURL) {
		t.Errorf("Search = %v, want ErrInvalidBaseURL", err)
	}
	if cmds := runner.Commands(); len(cmds) != 0 {
		t.Errorf("ran %v with an unsafe base URL", cmds)
	}
}

func TestScanOnAnEmptyHome(t *testing.T) {
	stdout, stderr, code := runFiles(t, "scan", "--json")
	if code != 0 || stdout != "[]\n" || stderr != "" {
		t.Errorf("scan --json = exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestNoCommandWithoutATerminalShowsTheHelp(t *testing.T) {
	stdout, _, code := runFiles(t)
	if code != 0 || !strings.Contains(stdout, "Uso:") {
		t.Errorf("no command = exit %d, stdout %q", code, stdout)
	}
}
