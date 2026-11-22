package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

// machine is the home of graphtest.Machine behind an App built over the real
// adapters, as cmd/skillhub composes it.
type machine struct {
	t        *testing.T
	h        *testsupport.Home
	p        paths.Paths
	gh       *fakes.GitHub
	git      *fakes.Git
	clock    *fakes.Clock
	state    *statefile.Store
	settings *settings.Store
	proc     string
	app      *app.App
	upstream string
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	h := testsupport.NewHome(t)
	entries := graphtest.Machine(h)
	p := h.Paths()
	m := &machine{
		t: t, h: h, p: p, gh: fakes.NewGitHub(), git: fakes.NewGit(),
		clock: fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
		state: statefile.New(p), settings: settings.New(p),
		proc: t.TempDir(), upstream: filepath.Join(t.TempDir(), "upstream"),
	}
	m.writeLock(entries)
	clone, err := filepath.EvalSymlinks(h.Path(graphtest.KarpathyClone))
	must(t, err)
	m.git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	files := fsadapter.NewFileOps(p.Guard())
	agents, err := data.LoadAgents(p, files)
	must(t, err)
	lock := skillslock.New(p, files)
	m.app, err = app.New(app.Deps{
		Paths: p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(p.Guard()),
		State: m.state, Settings: m.settings, GitHub: m.gh, Git: m.git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: m.clock,
		ProcDir: m.proc,
	})
	must(t, err)
	return m
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// writeLock writes the lock as npx skills 1.9.0 does.
func (m *machine) writeLock(entries []domain.LockEntry) {
	m.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, e := range entries {
		raw, err := json.MarshalIndent(struct {
			Source          string `json:"source"`
			SourceType      string `json:"sourceType"`
			SourceURL       string `json:"sourceUrl,omitempty"`
			SkillPath       string `json:"skillPath"`
			SkillFolderHash string `json:"skillFolderHash"`
		}{e.Source, e.SourceType, e.SourceURL, e.SkillPath, e.SkillFolderHash}, "    ", "  ")
		must(m.t, err)
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    \"" + e.Name + "\": " + string(raw))
	}
	if len(entries) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("},\n  \"dismissed\": {}\n}\n")
	must(m.t, os.MkdirAll(filepath.Dir(m.p.SkillsLockFile()), 0o755))
	must(m.t, os.WriteFile(m.p.SkillsLockFile(), []byte(b.String()), 0o644))
}

// roots are every directory skillhub may write: the home and its own XDG
// directories, wherever the test harness put them.
func (m *machine) roots() []string {
	out := []string{m.h.Root()}
	for _, d := range []string{m.p.StateDir(), m.p.CacheDir()} {
		if !strings.HasPrefix(d, m.h.Root()+string(filepath.Separator)) {
			out = append(out, d)
		}
	}
	return out
}

// hash is the SHA-256 of every path, mode, file content and link content
// under the roots: any write, a lock file or an empty directory included,
// changes it.
func (m *machine) hash() string {
	m.t.Helper()
	sum := sha256.New()
	for _, root := range m.roots() {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return filepath.SkipDir
			}
			if err != nil {
				return err
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

// listing is every path under the home with its kind, for readable diffs.
func (m *machine) exists(rel string) bool {
	_, err := os.Lstat(m.h.Path(rel))
	return err == nil
}

func (m *machine) persisted() domain.PersistedState {
	m.t.Helper()
	st, err := m.state.Load()
	must(m.t, err)
	return st
}

// apply plans req, applies it with the identifier shown and fails the test
// on any error.
func (m *machine) apply(p app.Proposal) app.Result {
	m.t.Helper()
	res, err := m.app.ApplyPlan(context.Background(), p.Request, p.PlanID)
	if err != nil {
		m.t.Fatalf("ApplyPlan(%s): %v", p.Request.Action, err)
	}
	return res
}

// lastV2 is the journal line of the last version 2 operation.
func (m *machine) lastV2() domain.HistoryEntry {
	m.t.Helper()
	journal, err := m.app.History()
	must(m.t, err)
	for i := len(journal) - 1; i >= 0; i-- {
		if strings.HasPrefix(journal[i].Target, "v2:") {
			return journal[i]
		}
	}
	m.t.Fatal("no version 2 line in the journal")
	return domain.HistoryEntry{}
}

func hasStep(p app.Proposal, kind, path string) bool {
	return slices.ContainsFunc(p.Steps, func(s app.Step) bool {
		return s.Kind == kind && (s.Path == path || s.Src == path || s.Dst == path || s.Name == path)
	})
}

// newEngine is an engine over the machine's home, as the app composes it.
func newEngine(m *machine) *engine.Engine {
	files := fsadapter.NewFileOps(m.p.Guard())
	lock := skillslock.New(m.p, files)
	return engine.New(engine.Deps{
		Paths: m.p, Files: files, Links: fsadapter.NewLinkOps(m.p.Guard()),
		Lock: lock, DropLock: lock, State: m.state,
		Dirs: fsadapter.NewScanner(m.p.Guard()), Clock: m.clock,
	})
}
