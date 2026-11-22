package skillssh_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/adapters/skillssh"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// curlArgs are the exact arguments the adapter must pass before the URL.
var curlArgs = []string{
	"-q", "--silent", "--show-error", "--fail", "--max-time", "15",
	"--proto", "=https", "--location", "--max-redirs", "0",
}

func curl(url string) ports.Command {
	return ports.Command{Name: "curl", Args: append(slices.Clone(curlArgs), url)}
}

func recorded(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newClient(t *testing.T, runner ports.ProcessRunner, base string) *skillssh.Client {
	t.Helper()
	c, err := skillssh.New(runner, base)
	if err != nil {
		t.Fatalf("New(%q) = %v", base, err)
	}
	return c
}

// search runs one Search against a runner that answers url with res and
// runErr, and checks that exactly that command ran.
func search(t *testing.T, url, query string, limit int, res ports.Result, runErr error) ([]ports.CatalogSkill, error) {
	t.Helper()
	runner := fakes.NewProcessRunner()
	runner.Expect(curl(url), res, runErr)
	got, err := newClient(t, runner, "").Search(t.Context(), query, limit)
	if unused := runner.Unused(); len(unused) > 0 {
		t.Errorf("commands not run: %q; ran %v", unused, runner.Commands())
	}
	return got, err
}

func TestSearchParsesARecordedResponse(t *testing.T) {
	got, err := search(t, "https://skills.sh/api/search?limit=10&q=react", "react", 10,
		ports.Result{Stdout: recorded(t, "search_valid.json")}, nil)
	if err != nil {
		t.Fatalf("Search = %v", err)
	}
	want := []ports.CatalogSkill{
		{ID: "vercel-labs/agent-skills/vercel-react-best-practices", Source: "vercel-labs/agent-skills",
			SkillID: "vercel-react-best-practices", Name: "vercel-react-best-practices", Installs: 48213},
		{ID: "mintlify.com/mintlify", Source: "mintlify.com", SkillID: "mintlify", Name: "Mintlify", Installs: 1204},
		{ID: "acme/skills/react-testing", Source: "acme/skills", SkillID: "react-testing", Name: "React testing", Installs: 0},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Search =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSearchEncodesTheQuery(t *testing.T) {
	_, err := search(t, "https://skills.sh/api/search?limit=5&q=react+hooks%26x%3D1", "  react hooks&x=1 ", 5,
		ports.Result{Stdout: recorded(t, "search_empty.json")}, nil)
	if err != nil {
		t.Fatalf("Search = %v", err)
	}
}

func TestSearchOfAnEmptyResultIsEmpty(t *testing.T) {
	got, err := search(t, "https://skills.sh/api/search?limit=10&q=zz", "zz", 10,
		ports.Result{Stdout: recorded(t, "search_empty.json")}, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("Search = %v, %v; want no skills and no error", got, err)
	}
}

func TestSearchDropsInvalidEntries(t *testing.T) {
	got, err := search(t, "https://skills.sh/api/search?limit=50&q=bad", "bad", 50,
		ports.Result{Stdout: recorded(t, "search_invalid_fields.json")}, nil)
	if err != nil {
		t.Fatalf("Search = %v", err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.SkillID)
	}
	if want := []string{"kept", "also-kept"}; !slices.Equal(ids, want) {
		t.Errorf("kept skills = %q, want %q", ids, want)
	}
}

func TestSearchKeepsAtMostLimit(t *testing.T) {
	got, err := search(t, "https://skills.sh/api/search?limit=2&q=react", "react", 2,
		ports.Result{Stdout: recorded(t, "search_valid.json")}, nil)
	if err != nil || len(got) != 2 {
		t.Errorf("Search = %d skills, %v; want 2", len(got), err)
	}
}

func TestSearchClampsTheLimit(t *testing.T) {
	for _, tc := range []struct {
		limit int
		url   string
	}{
		{0, "https://skills.sh/api/search?limit=1&q=react"},
		{500, "https://skills.sh/api/search?limit=100&q=react"},
	} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			if _, err := search(t, tc.url, "react", tc.limit, ports.Result{Stdout: recorded(t, "search_empty.json")}, nil); err != nil {
				t.Errorf("Search = %v", err)
			}
		})
	}
}

func TestSearchRefusesAQueryShorterThanTwoCharacters(t *testing.T) {
	for _, q := range []string{"", " ", "a", " é "} {
		t.Run(q, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			_, err := newClient(t, runner, "").Search(t.Context(), q, 10)
			if reason := reasonOf(t, err); reason != skillssh.ReasonBadQuery {
				t.Errorf("reason = %q, want %q", reason, skillssh.ReasonBadQuery)
			}
			if cmds := runner.Commands(); len(cmds) != 0 {
				t.Errorf("ran %v for a short query", cmds)
			}
		})
	}
}

func reasonOf(t *testing.T, err error) skillssh.Reason {
	t.Helper()
	e, ok := errors.AsType[*skillssh.Error](err)
	if !ok {
		t.Fatalf("error = %v, want a *skillssh.Error", err)
	}
	if got := e.CatalogReason(); got != string(e.Reason) {
		t.Errorf("CatalogReason() = %q, want %q", got, e.Reason)
	}
	return e.Reason
}

func TestSearchErrorsAreTyped(t *testing.T) {
	const url = "https://skills.sh/api/search?limit=10&q=react"
	for _, tc := range []struct {
		name   string
		res    ports.Result
		runErr error
		reason skillssh.Reason
		status int
	}{
		{"curl missing", ports.Result{ExitCode: -1},
			fmt.Errorf("run curl: %w", errors.New(`exec: "curl": executable file not found in $PATH`)),
			skillssh.ReasonCurlMissing, 0},
		{"cannot resolve host", ports.Result{ExitCode: 6, Stderr: []byte("curl: (6) Could not resolve host: skills.sh\n")},
			nil, skillssh.ReasonOffline, 0},
		{"cannot connect", ports.Result{ExitCode: 7, Stderr: []byte("curl: (7) Failed to connect\n")},
			nil, skillssh.ReasonOffline, 0},
		{"timeout", ports.Result{ExitCode: 28, Stderr: []byte("curl: (28) Operation timed out\n")},
			nil, skillssh.ReasonOffline, 0},
		{"server error", ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 503\n")},
			nil, skillssh.ReasonHTTP, 503},
		{"bad request", ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 400\n")},
			nil, skillssh.ReasonHTTP, 400},
		{"rate limited", ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 429\n")},
			nil, skillssh.ReasonRateLimited, 429},
		{"redirect refused", ports.Result{ExitCode: 47, Stderr: []byte("curl: (47) Maximum (0) redirects followed\n")},
			nil, skillssh.ReasonHTTP, 0},
		{"other curl failure", ports.Result{ExitCode: 97, Stderr: []byte("blocked\n")},
			nil, skillssh.ReasonFailed, 0},
		{"not JSON", ports.Result{Stdout: []byte("<html>busy</html>")}, nil, skillssh.ReasonBadResponse, 0},
		{"skills not a list", ports.Result{Stdout: []byte(`{"skills":{}}`)}, nil, skillssh.ReasonBadResponse, 0},
		{"no skills key", ports.Result{Stdout: []byte(`{"results":[]}`)}, nil, skillssh.ReasonBadResponse, 0},
		{"top level list", ports.Result{Stdout: []byte(`[]`)}, nil, skillssh.ReasonBadResponse, 0},
		{"trailing data", ports.Result{Stdout: []byte(`{"skills":[]} {}`)}, nil, skillssh.ReasonBadResponse, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := search(t, url, "react", 10, tc.res, tc.runErr)
			if got != nil {
				t.Errorf("skills = %v, want none on an error", got)
			}
			if reason := reasonOf(t, err); reason != tc.reason {
				t.Errorf("reason = %q, want %q (error %v)", reason, tc.reason, err)
			}
			if e, _ := errors.AsType[*skillssh.Error](err); e.HTTPStatus() != tc.status {
				t.Errorf("HTTPStatus() = %d, want %d", e.HTTPStatus(), tc.status)
			}
		})
	}
}

func TestSearchPassesACanceledContextThrough(t *testing.T) {
	runner := fakes.NewProcessRunner()
	const url = "https://skills.sh/api/search?limit=10&q=react"
	runner.Expect(curl(url), ports.Result{ExitCode: -1}, fmt.Errorf("run curl: %w", context.Canceled))
	_, err := newClient(t, runner, "").Search(t.Context(), "react", 10)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if _, ok := errors.AsType[*skillssh.Error](err); ok {
		t.Errorf("a canceled search is reported as %v, not as a catalog failure", err)
	}
}

func TestNewTakesTheBaseURL(t *testing.T) {
	for _, base := range []string{"https://mirror.example", "https://mirror.example/"} {
		runner := fakes.NewProcessRunner()
		runner.Expect(curl("https://mirror.example/api/search?limit=3&q=go"),
			ports.Result{Stdout: recorded(t, "search_empty.json")}, nil)
		if _, err := newClient(t, runner, base).Search(t.Context(), "go", 3); err != nil {
			t.Errorf("base %q: Search = %v", base, err)
		}
		if unused := runner.Unused(); len(unused) > 0 {
			t.Errorf("base %q: commands not run: %q; ran %v", base, unused, runner.Commands())
		}
	}
}

func TestNewRefusesAnUnsafeBaseURL(t *testing.T) {
	runner := fakes.NewProcessRunner()
	for _, base := range []string{
		"http://skills.sh", "skills.sh", "https://", "https://user:pw@skills.sh",
		"https://skills.sh/?q=1", "https://skills.sh/#x", "file:///etc", "https://skills.sh\n",
	} {
		if _, err := skillssh.New(runner, base); !errors.Is(err, skillssh.ErrInvalidBaseURL) {
			t.Errorf("New(%q) = %v, want ErrInvalidBaseURL", base, err)
		}
	}
	if _, err := skillssh.New(nil, ""); err == nil {
		t.Error("New(nil runner) succeeded")
	}
}
