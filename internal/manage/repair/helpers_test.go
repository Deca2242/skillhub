package repair_test

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
	"slices"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/data"
	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/inventory"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/repair"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// The existing adapters, the engine and the toggle planner satisfy the
// repair planner's consumer interfaces unchanged.
var (
	_ repair.Plans       = (*engine.Engine)(nil)
	_ repair.Toggler     = (*toggle.Planner)(nil)
	_ repair.StateReader = (*statefile.Store)(nil)
	_ repair.LockReader  = (*skillslock.Store)(nil)
	_ repair.Stat        = (*fsadapter.FileOps)(nil)
)

// osLinks reads links straight from the disk, for the graph in tests.
type osLinks struct{}

func (osLinks) Readlink(path string) (string, error) { return os.Readlink(path) }

// machine is the fake home that reproduces this machine (graphtest), with the
// lock on disk, real adapters, the real engine and the real planners.
type machine struct {
	t       *testing.T
	h       *testsupport.Home
	p       paths.Paths
	git     *fakes.Git
	clock   *fakes.Clock
	harness []data.Harness
	entries []domain.LockEntry
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	h := testsupport.NewHome(t)
	entries := graphtest.Machine(h)
	m := &machine{t: t, h: h, p: h.Paths(), git: fakes.NewGit(),
		clock:   fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
		entries: entries}
	m.writeLock(entries)
	clone, err := filepath.EvalSymlinks(h.Path(graphtest.KarpathyClone))
	must(t, err)
	m.git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	m.harness, err = data.LoadHarnesses(m.p, m.files())
	must(t, err)
	return m
}

// writeLock writes entries as npx skills writes its lock.
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

func (m *machine) files() *fsadapter.FileOps { return fsadapter.NewFileOps(m.p.Guard()) }

func (m *machine) engine() *engine.Engine {
	files := m.files()
	lock := skillslock.New(m.p, files)
	return engine.New(engine.Deps{
		Paths: m.p, Files: files, Links: fsadapter.NewLinkOps(m.p.Guard()),
		Lock: lock, DropLock: lock, State: statefile.New(m.p),
		Dirs: fsadapter.NewScanner(m.p.Guard()), Clock: m.clock,
	})
}

func (m *machine) profiles() []data.DiagnosticProfile {
	profiles, err := data.DiagnosticProfiles()
	must(m.t, err)
	return profiles
}

func (m *machine) toggle() *toggle.Planner {
	files := m.files()
	return toggle.New(toggle.Deps{
		Paths: m.p, Harnesses: m.harness, Profiles: m.profiles(),
		Plans: m.engine(), State: statefile.New(m.p), Lock: skillslock.New(m.p, files), Stat: files,
		Dirs: fsadapter.NewScanner(m.p.Guard()),
	})
}

// observer observes the fake home as it is when asked.
type observer struct{ m *machine }

func (o observer) Observe() (graph.Graph, error) { return o.m.observe() }

func (m *machine) planner() *repair.Planner {
	files := m.files()
	return repair.New(repair.Deps{
		Paths: m.p, Harnesses: m.harness, Profiles: m.profiles(),
		Observe: observer{m}, Plans: m.engine(), Toggle: m.toggle(),
		State: statefile.New(m.p), Lock: skillslock.New(m.p, files), Stat: files,
		Dirs: fsadapter.NewScanner(m.p.Guard()),
	})
}

func (m *machine) observe() (graph.Graph, error) {
	files := m.files()
	dirs := fsadapter.NewScanner(m.p.Guard())
	agents, err := data.LoadAgents(m.p, files)
	if err != nil {
		return graph.Graph{}, err
	}
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: m.p, Agents: agents, Dirs: dirs, Files: files,
		Lock: skillslock.New(m.p, files), Git: m.git,
	}, nil)
	if err != nil {
		return graph.Graph{}, err
	}
	return graph.Build(graph.Deps{
		Paths: m.p, Harnesses: m.harness, Profiles: m.profiles(), Inventory: skills,
		Dirs: dirs, Files: files, Links: osLinks{},
	})
}

func (m *machine) graph() graph.Graph {
	m.t.Helper()
	g, err := m.observe()
	must(m.t, err)
	return g
}

// findings runs Doctor on the home as it is now.
func (m *machine) findings() []doctor.Finding { return doctor.Detect(m.graph()) }

// finding returns the finding id, failing the test when Doctor does not
// report it.
func (m *machine) finding(id string) doctor.Finding {
	m.t.Helper()
	for _, f := range m.findings() {
		if f.ID == id {
			return f
		}
	}
	m.t.Fatalf("Doctor does not report %s", id)
	return doctor.Finding{}
}

// reports tells whether Doctor reports id now.
func (m *machine) reports(id string) bool {
	return slices.ContainsFunc(m.findings(), func(f doctor.Finding) bool { return f.ID == id })
}

func (m *machine) state() domain.PersistedState {
	m.t.Helper()
	st, err := statefile.New(m.p).Load()
	must(m.t, err)
	return st
}

func (m *machine) lockBytes() []byte {
	m.t.Helper()
	data, err := os.ReadFile(m.p.SkillsLockFile())
	must(m.t, err)
	return data
}

// world is the whole fake home as the harnesses and npx skills see it, with
// skillhub's own areas (state, cache and parking area) left out.
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
				return nil
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
	slices.Sort(out)
	return out
}

func sameWorld(t *testing.T, what string, want, got map[string]string) {
	t.Helper()
	if d := diff(want, got); len(d) > 0 {
		t.Errorf("%s: the home changed: %v", what, d)
	}
}

// snapshot is everything a round trip must leave as it found it.
type snapshot struct {
	world map[string]string
	lock  []byte
	state domain.PersistedState
}

func (m *machine) snap() snapshot {
	return snapshot{world: m.world(), lock: m.lockBytes(), state: m.state()}
}

// same checks that the home, the lock and the state are as in s.
func (m *machine) same(what string, s snapshot) {
	m.t.Helper()
	sameWorld(m.t, what, s.world, m.world())
	if got := m.lockBytes(); string(got) != string(s.lock) {
		m.t.Errorf("%s: the lock changed:\n%s\nwant\n%s", what, got, s.lock)
	}
	st := m.state()
	if !reflect.DeepEqual(st.Managed, s.state.Managed) && (len(st.Managed) > 0 || len(s.state.Managed) > 0) {
		m.t.Errorf("%s: Managed = %+v, want %+v", what, st.Managed, s.state.Managed)
	}
	if !reflect.DeepEqual(st.Parked, s.state.Parked) && (len(st.Parked) > 0 || len(s.state.Parked) > 0) {
		m.t.Errorf("%s: Parked = %+v, want %+v", what, st.Parked, s.state.Parked)
	}
}

// noWrite checks that a refused call wrote nothing.
func (m *machine) noWrite(s snapshot) {
	m.t.Helper()
	m.same("refused plan", s)
	if exists(m.t, engine.RecordFile(m.p)) {
		m.t.Error("a planner left an operation record")
	}
}

// apply runs a proposal's plan with a fresh engine.
func (m *machine) apply(pr repair.Proposal) engine.Outcome {
	m.t.Helper()
	out, err := m.engine().Apply(m.t.Context(), pr.Plan)
	if err != nil {
		m.t.Fatalf("Apply(%s) error = %v", pr.Plan.Action, err)
	}
	return out
}

func (m *machine) undo(out engine.Outcome) {
	m.t.Helper()
	if _, err := m.engine().Undo(m.t.Context(), out.SnapshotID); err != nil {
		m.t.Fatalf("Undo() error = %v", err)
	}
}

// roundTrip applies pr, checks that Doctor no longer reports the finding,
// undoes it and checks that everything is back byte for byte.
func (m *machine) roundTrip(pr repair.Proposal, finding string, before snapshot) {
	m.t.Helper()
	out := m.apply(pr)
	if m.reports(finding) {
		m.t.Errorf("Doctor still reports %s after the repair", finding)
	}
	m.undo(out)
	m.same("plan, apply and undo", before)
	if !m.reports(finding) {
		m.t.Errorf("Doctor does not report %s after the undo", finding)
	}
}

// copyTree copies the store folder of key to rel, byte for byte.
func (m *machine) copyTree(key, rel string) {
	m.t.Helper()
	src := m.h.Path(".agents/skills/" + key)
	err := filepath.WalkDir(src, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m.h.WriteFile(filepath.Join(rel, r), string(data))
		return nil
	})
	must(m.t, err)
}

// stepPaths lists every path the plan's steps touch.
func stepPaths(p engine.Plan) []string {
	var out []string
	for _, s := range p.Steps {
		for _, img := range s.Pre {
			if img.Path != "" {
				out = append(out, img.Path)
			}
		}
	}
	return out
}

func kinds(p engine.Plan) []engine.StepKind {
	var out []engine.StepKind
	for _, s := range p.Steps {
		out = append(out, s.Kind)
	}
	return out
}

var osReadlink = os.Readlink

func removeAll(p string) error { return os.RemoveAll(p) }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
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
