package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

var update = flag.Bool("update", false, "rewrite the golden files of the version 2 previews")

// machine is the home of graphtest.Machine behind an App composed as
// cmd/skillhub composes it, version 2 ports included. Each run builds a
// fresh App, as a new process would.
type machine struct {
	t           *testing.T
	h           *testsupport.Home
	p           paths.Paths
	gh          *fakes.GitHub
	git         *fakes.Git
	clock       *fakes.Clock
	proc        string
	upstream    string
	interactive bool
	// state, when set, wraps the state store the App gets.
	state func(ports.StateStore) ports.StateStore
	// dirs, when set, wraps the directory scanner the App gets.
	dirs func(ports.SkillDirScanner) ports.SkillDirScanner
	// web, when set, is the App's Web port: websites read through it.
	web ports.HTTPSFetcher
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	h := testsupport.NewHome(t)
	entries := graphtest.Machine(h)
	m := &machine{
		t: t, h: h, p: h.Paths(), gh: fakes.NewGitHub(), git: fakes.NewGit(),
		clock: fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
		proc:  t.TempDir(), upstream: filepath.Join(t.TempDir(), "upstream"),
	}
	m.writeLock(entries)
	clone, err := filepath.EvalSymlinks(h.Path(graphtest.KarpathyClone))
	must(t, err)
	m.git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	return m
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (m *machine) newApp() (*app.App, error) {
	files := fsadapter.NewFileOps(m.p.Guard())
	agents, err := data.LoadAgents(m.p, files)
	if err != nil {
		return nil, err
	}
	lock := skillslock.New(m.p, files)
	var state ports.StateStore = statefile.New(m.p)
	if m.state != nil {
		state = m.state(state)
	}
	var dirs ports.SkillDirScanner = fsadapter.NewScanner(m.p.Guard())
	if m.dirs != nil {
		dirs = m.dirs(dirs)
	}
	return app.New(app.Deps{
		Paths: m.p, Agents: agents, Files: files, Dirs: dirs,
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(m.p.Guard()),
		State: state, Settings: settings.New(m.p), GitHub: m.gh, Git: m.git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: m.clock,
		ProcDir: m.proc, Web: m.web,
	})
}

// run runs the command line with stdin as the user's answers.
func (m *machine) run(stdin string, args ...string) result {
	m.t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, cli.Env{
		Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
		Interactive: m.interactive, Executable: "/opt/skillhub/bin/skillhub",
		NewApp: m.newApp,
		TUI:    func(io.Writer, io.Writer) int { return 0 },
	})
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// ok runs args and fails the test unless the exit code is 0.
func (m *machine) ok(args ...string) result {
	m.t.Helper()
	r := m.run("", args...)
	if r.code != 0 {
		m.t.Fatalf("skillhub %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

// writeLock writes the lock as npx skills 1.9.0 does.
func (m *machine) writeLock(entries []domain.LockEntry) {
	m.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n    %q: {\n      \"source\": %q,\n      \"sourceType\": %q,\n", e.Name, e.Source, e.SourceType)
		if e.SourceURL != "" {
			fmt.Fprintf(&b, "      \"sourceUrl\": %q,\n", e.SourceURL)
		}
		fmt.Fprintf(&b, "      \"skillPath\": %q,\n      \"skillFolderHash\": %q\n    }", e.SkillPath, e.SkillFolderHash)
	}
	if len(entries) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("},\n  \"dismissed\": {}\n}\n")
	must(m.t, os.MkdirAll(filepath.Dir(m.p.SkillsLockFile()), 0o755))
	must(m.t, os.WriteFile(m.p.SkillsLockFile(), []byte(b.String()), 0o644))
}

// hash is the SHA-256 of every path, mode, file content and link content
// under the home and skillhub's own directories, except the paths skipped:
// any other write, a lock file or an empty directory included, changes it.
func (m *machine) hash(skip ...string) string {
	m.t.Helper()
	roots := []string{m.h.Root()}
	for _, d := range []string{m.p.StateDir(), m.p.CacheDir()} {
		if !strings.HasPrefix(d, m.h.Root()+string(filepath.Separator)) {
			roots = append(roots, d)
		}
	}
	sum := sha256.New()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return filepath.SkipDir
			}
			if err != nil {
				return err
			}
			for _, s := range skip {
				if p == s {
					return filepath.SkipDir
				}
			}
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			line := p + " " + info.Mode().String()
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				line += " -> " + target
			case info.Mode().IsRegular():
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				s := sha256.Sum256(data)
				line += " " + hex.EncodeToString(s[:])
			}
			sum.Write([]byte(line + "\n"))
			return nil
		})
		must(m.t, err)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (m *machine) exists(rel string) bool {
	_, err := os.Lstat(m.h.Path(rel))
	return err == nil
}

func (m *machine) persisted() domain.PersistedState {
	m.t.Helper()
	st, err := statefile.New(m.p).Load()
	must(m.t, err)
	return st
}

// lastV2 is the journal line of the last version 2 operation.
func (m *machine) lastV2() domain.HistoryEntry {
	m.t.Helper()
	journal, err := statefile.New(m.p).Journal()
	must(m.t, err)
	for i := len(journal) - 1; i >= 0; i-- {
		if strings.HasPrefix(journal[i].Target, "v2:") {
			return journal[i]
		}
	}
	m.t.Fatal("no version 2 line in the journal")
	return domain.HistoryEntry{}
}

// planIDPattern finds the plan identifier a preview prints.
var planIDPattern = regexp.MustCompile(`--plan ([0-9a-f]{64}(?:@[0-9]+)?)`)

// planID is the identifier the output of a preview tells to approve.
func planID(t *testing.T, out string) string {
	t.Helper()
	match := planIDPattern.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no plan identifier in:\n%s", out)
	}
	return match[1]
}

// pendingV2 leaves a version 2 operation begun and not finished, as a crash
// would: a record that unlinks .claude/skills/archify.
func (m *machine) pendingV2() {
	m.t.Helper()
	files := fsadapter.NewFileOps(m.p.Guard())
	lock := skillslock.New(m.p, files)
	e := engine.New(engine.Deps{
		Paths: m.p, Files: files, Links: fsadapter.NewLinkOps(m.p.Guard()),
		Lock: lock, DropLock: lock, State: statefile.New(m.p),
		Dirs: fsadapter.NewScanner(m.p.Guard()), Clock: m.clock,
	})
	b := e.NewBuilder(engine.Meta{Action: "unlink", Targets: []string{"claude-code"}})
	must(m.t, b.Unlink(m.h.Path(".claude/skills/archify"), "../../.agents/skills/archify"))
	plan, err := b.Build()
	must(m.t, err)
	run, err := e.Begin(bgCtx, plan)
	must(m.t, err)
	must(m.t, run.Release())
	if _, err := os.Stat(engine.RecordFile(m.p)); err != nil {
		m.t.Fatalf("no pending record: %v", err)
	}
}

// normalize puts the output in a form a golden file can hold: the home is
// ~, the plan identifiers and snapshot ids are placeholders.
func (m *machine) normalize(s string) string {
	s = strings.ReplaceAll(s, m.h.Root(), "~")
	s = regexp.MustCompile(`[0-9a-f]{64}(@[0-9]+)?`).ReplaceAllString(s, "<plan>")
	return s
}

// golden compares got with testdata/<name>.golden, or rewrites it with
// -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	file := filepath.Join("testdata", name+".golden")
	if *update {
		must(t, os.MkdirAll("testdata", 0o755))
		must(t, os.WriteFile(file, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to create it)", file, err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
