package cli_test

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

const repo = "acme/skills"

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// fixture is a fake home with the real file, lock, state and settings
// adapters, and doubles for GitHub, git, the notifier, the timer and the
// clock. Each run builds a fresh App over it, like a new process would.
type fixture struct {
	t        *testing.T
	home     *testsupport.Home
	gh       *fakes.GitHub
	git      *fakes.Git
	notifier *fakes.Notifier
	bg       *fakes.BackgroundService
	clock    *fakes.Clock
	upstream string
	// interactive is what Env.Interactive reports.
	interactive bool
	tuiCalls    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t: t, home: testsupport.NewHome(t),
		gh: fakes.NewGitHub(), git: fakes.NewGit(), notifier: fakes.NewNotifier(),
		bg: fakes.NewBackgroundService(), clock: fakes.NewClock(t0),
		upstream: filepath.Join(t.TempDir(), "upstream"),
	}
	f.gh.AddRepo(repo, "main")
	return f
}

func (f *fixture) newApp() (*app.App, error) {
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
	})
}

// result is what one run of the command line produced.
type result struct {
	stdout, stderr string
	code           int
}

// run runs the command line with stdin as the user's input.
func (f *fixture) run(stdin io.Reader, args ...string) result {
	f.t.Helper()
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, cli.Env{
		Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
		Interactive: f.interactive, Executable: "/opt/skillhub/bin/skillhub",
		NewApp: f.newApp,
		TUI: func(stdout, _ io.Writer) int {
			f.tuiCalls++
			fmt.Fprint(stdout, "tui\n")
			return 0
		},
	})
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func (f *fixture) ok(args ...string) result {
	f.t.Helper()
	r := f.run(nil, args...)
	if r.code != 0 {
		f.t.Fatalf("skillhub %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func hashOf(t *testing.T, dir string) string {
	t.Helper()
	tree, err := treehash.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tree.SHA
}

// publish commits files as upstream's skills/<name> on main and returns the
// folder's tree SHA.
func (f *fixture) publish(name string, files map[string]string) string {
	f.t.Helper()
	dir := filepath.Join(f.upstream, "skills", name)
	if err := os.RemoveAll(dir); err != nil {
		f.t.Fatal(err)
	}
	writeTree(f.t, dir, files)
	f.gh.Commit(f.t, repo, "main", f.upstream)
	return hashOf(f.t, dir)
}

// installLock installs each skill's files in the store and records them in
// the lock, in the given order, the way npx skills does.
func (f *fixture) installLock(names []string, files map[string]map[string]string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, name := range names {
		dir := filepath.Join(f.home.Paths().StoreDir(), name)
		writeTree(f.t, dir, files[name])
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n    %q: {\n      \"source\": %q,\n      \"sourceType\": \"github\",\n", name, repo)
		fmt.Fprintf(&b, "      \"sourceUrl\": \"https://github.com/%s.git\",\n", repo)
		fmt.Fprintf(&b, "      \"skillPath\": \"skills/%s/SKILL.md\",\n      \"skillFolderHash\": %q,\n", name, hashOf(f.t, dir))
		b.WriteString("      \"installedAt\": \"2026-01-01T00:00:00.000Z\",\n      \"updatedAt\": \"2026-01-02T00:00:00.000Z\"\n    }")
	}
	b.WriteString("\n  }\n}\n")
	f.home.WriteFile(".agents/.skill-lock.json", b.String())
}

func skillFiles(name, version string) map[string]string {
	return map[string]string{"SKILL.md": fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nbody %s\n", name, version, version)}
}

// standard installs the given skills at v1 and publishes v2 of each
// upstream; it returns the upstream v2 tree SHA of each.
func (f *fixture) standard(names ...string) map[string]string {
	f.t.Helper()
	v1 := map[string]map[string]string{}
	for _, n := range names {
		v1[n] = skillFiles(n, "v1")
		f.publish(n, v1[n])
	}
	f.installLock(names, v1)
	out := map[string]string{}
	for _, n := range names {
		out[n] = f.publish(n, skillFiles(n, "v2"))
	}
	return out
}

func (f *fixture) state() *statefile.Store { return statefile.New(f.home.Paths()) }

func (f *fixture) persisted() domain.PersistedState {
	f.t.Helper()
	st, err := f.state().Load()
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fixture) journal() []domain.HistoryEntry {
	f.t.Helper()
	j, err := f.state().Journal()
	if err != nil {
		f.t.Fatal(err)
	}
	return j
}

func (f *fixture) record(op domain.Operation) {
	f.t.Helper()
	if err := f.state().RecordOperation(op); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) installedSHA(name string) string {
	f.t.Helper()
	return hashOf(f.t, filepath.Join(f.home.Paths().StoreDir(), name))
}

// candidate returns the open candidate of target.
func (f *fixture) candidate(target string) domain.UpdateCandidate {
	f.t.Helper()
	for _, c := range f.persisted().Candidates {
		if c.Target == target && (c.State == domain.CandidatePending || c.State == domain.CandidateBlocked) {
			return c
		}
	}
	f.t.Fatalf("no open candidate for %s", target)
	return domain.UpdateCandidate{}
}

// homeTree records every directory, file and link under the home except
// skillhub's own cache and state: mode and content, byte for byte.
func (f *fixture) homeTree() map[string]string {
	f.t.Helper()
	root := f.home.Root()
	own := []string{f.home.Paths().CacheDir(), f.home.Paths().StateDir()}
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, o := range own {
			if p == o {
				return filepath.SkipDir
			}
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "link " + target
		case info.IsDir():
			for _, o := range own {
				if strings.HasPrefix(o, p+string(filepath.Separator)) {
					return nil
				}
			}
			out[rel] = "dir " + info.Mode().String()
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = info.Mode().String() + " " + string(data)
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// cliRun runs the command line with newApp as the composition root.
func cliRun(args []string, stdout, stderr io.Writer, newApp func() (*app.App, error)) int {
	return cli.Run(args, cli.Env{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr, NewApp: newApp})
}
