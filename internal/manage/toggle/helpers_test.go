package toggle_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/inventory"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// The existing adapters and the engine satisfy the planner's consumer
// interfaces unchanged.
var (
	_ toggle.Plans       = (*engine.Engine)(nil)
	_ toggle.StateReader = (*statefile.Store)(nil)
	_ toggle.LockReader  = (*skillslock.Store)(nil)
	_ toggle.Stat        = (*fsadapter.FileOps)(nil)
	_ toggle.DirLister   = (*fsadapter.Scanner)(nil)
)

// osLinks reads links straight from the disk, for the graph in tests.
type osLinks struct{}

func (osLinks) Readlink(path string) (string, error) { return os.Readlink(path) }

// machine is the fake home that reproduces this machine (graphtest), with the
// lock on disk, real adapters, an engine and a planner.
type machine struct {
	t       *testing.T
	h       *testsupport.Home
	p       paths.Paths
	git     *fakes.Git
	clock   *fakes.Clock
	harness []data.Harness
	// entries are the lock entries the fixture wrote, in order.
	entries []domain.LockEntry
	// wrap, when set, wraps the engine's dependencies (fault injection).
	wrap func(engine.Deps) engine.Deps
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	h := testsupport.NewHome(t)
	entries := graphtest.Machine(h)
	h.Mkdir(".agents/.skillhub/disabled")
	m := &machine{t: t, h: h, p: h.Paths(), git: fakes.NewGit(),
		clock:   fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
		entries: entries}
	writeLock(t, m.p, entries)
	clone, err := filepath.EvalSymlinks(h.Path(graphtest.KarpathyClone))
	must(t, err)
	m.git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	m.harness, err = data.LoadHarnesses(m.p, fsadapter.NewFileOps(m.p.Guard()))
	must(t, err)
	return m
}

// writeLock writes entries as npx skills writes its lock: version 3, the
// entries in order, two-space indentation.
func writeLock(t *testing.T, p paths.Paths, entries []domain.LockEntry) {
	t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, e := range entries {
		raw, err := json.MarshalIndent(struct {
			Source          string `json:"source"`
			SourceType      string `json:"sourceType"`
			SkillPath       string `json:"skillPath"`
			SkillFolderHash string `json:"skillFolderHash"`
		}{e.Source, e.SourceType, e.SkillPath, e.SkillFolderHash}, "    ", "  ")
		must(t, err)
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    \"" + e.Name + "\": " + string(raw))
	}
	if len(entries) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("},\n  \"dismissed\": {}\n}\n")
	must(t, os.MkdirAll(filepath.Dir(p.SkillsLockFile()), 0o755))
	must(t, os.WriteFile(p.SkillsLockFile(), []byte(b.String()), 0o644))
}

func (m *machine) files() *fsadapter.FileOps { return fsadapter.NewFileOps(m.p.Guard()) }

func (m *machine) deps() engine.Deps {
	files := m.files()
	lock := skillslock.New(m.p, files)
	d := engine.Deps{
		Paths: m.p, Files: files, Links: fsadapter.NewLinkOps(m.p.Guard()),
		Lock: lock, DropLock: lock, State: statefile.New(m.p),
		Dirs: fsadapter.NewScanner(m.p.Guard()), Clock: m.clock,
	}
	if m.wrap != nil {
		d = m.wrap(d)
	}
	return d
}

// engine is an engine over the real adapters (wrapped if m.wrap is set).
func (m *machine) engine() *engine.Engine { return engine.New(m.deps()) }

// clean is an engine over the real adapters with nothing wrapped: what a
// later process sees.
func (m *machine) clean() *engine.Engine {
	saved := m.wrap
	m.wrap = nil
	defer func() { m.wrap = saved }()
	return m.engine()
}

func (m *machine) planner() *toggle.Planner {
	profiles, err := data.DiagnosticProfiles()
	must(m.t, err)
	files := m.files()
	return toggle.New(toggle.Deps{
		Paths: m.p, Harnesses: m.harness, Profiles: profiles,
		Plans: m.clean(), State: statefile.New(m.p), Lock: skillslock.New(m.p, files), Stat: files,
		Dirs: fsadapter.NewScanner(m.p.Guard()),
	})
}

// graph observes the fake home as it is now.
func (m *machine) graph() graph.Graph {
	m.t.Helper()
	files := m.files()
	dirs := fsadapter.NewScanner(m.p.Guard())
	agents, err := data.LoadAgents(m.p, files)
	must(m.t, err)
	profiles, err := data.DiagnosticProfiles()
	must(m.t, err)
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: m.p, Agents: agents, Dirs: dirs, Files: files,
		Lock: skillslock.New(m.p, files), Git: m.git,
	}, nil)
	must(m.t, err)
	g, err := graph.Build(graph.Deps{
		Paths: m.p, Harnesses: m.harness, Profiles: profiles, Inventory: skills,
		Dirs: dirs, Files: files, Links: osLinks{},
	})
	must(m.t, err)
	return g
}

// state loads skillhub's persisted state.
func (m *machine) state() domain.PersistedState {
	m.t.Helper()
	st, err := statefile.New(m.p).Load()
	must(m.t, err)
	return st
}

// lockKeys lists the lock's entry names in order.
func (m *machine) lockKeys() []string {
	m.t.Helper()
	lock, err := skillslock.New(m.p, m.files()).Read()
	must(m.t, err)
	var out []string
	for _, e := range lock.Entries {
		out = append(out, e.Name)
	}
	return out
}

// apply runs a proposal's plan with a fresh engine.
func (m *machine) apply(pr toggle.Proposal) engine.Outcome {
	m.t.Helper()
	out, err := m.engine().Apply(m.t.Context(), pr.Plan)
	if err != nil {
		m.t.Fatalf("Apply(%s) error = %v", pr.Plan.Action, err)
	}
	return out
}

// world is the whole fake home as the harnesses and npx skills see it: every
// path with its type, mode, content or link text. skillhub's own areas (its
// state, cache and parking area) are left out: they are compared on their
// own.
func (m *machine) world() map[string]string {
	m.t.Helper()
	skip := []string{m.p.StateDir(), m.p.CacheDir(), filepath.Dir(m.p.ParkedDir())}
	out := map[string]string{}
	root := m.p.Home()
	err := filepath.WalkDir(root, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, s := range skip {
			if p == s {
				return filepath.SkipDir
			}
			if d.IsDir() && strings.HasPrefix(s, p+string(filepath.Separator)) && p != root {
				return nil // a parent of skillhub's own area, which its stores create
			}
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		desc := info.Mode().String()
		switch {
		case info.Mode()&iofs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			desc += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			desc += " " + hex.EncodeToString(sum[:])
		}
		out[rel] = desc
		return nil
	})
	must(m.t, err)
	return out
}

// diff lists the paths whose description differs between two worlds.
func diff(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, "-"+k)
		} else if w != v {
			out = append(out, "~"+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "+"+k)
		}
	}
	return out
}

func sameWorld(t *testing.T, what string, want, got map[string]string) {
	t.Helper()
	if d := diff(want, got); len(d) > 0 {
		t.Errorf("%s: the home changed: %v", what, d)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	return data
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return false
	}
	must(t, err)
	return true
}

func readlink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	must(t, err)
	return target
}

// noWrite checks that a refused planner call wrote nothing.
func noWrite(t *testing.T, m *machine, before map[string]string, stateBefore domain.PersistedState) {
	t.Helper()
	sameWorld(t, "refused plan", before, m.world())
	if got := m.state(); !reflect.DeepEqual(got, stateBefore) {
		t.Errorf("a planner changed the state: %+v", got)
	}
	if exists(t, engine.RecordFile(m.p)) {
		t.Error("a planner left an operation record")
	}
}
