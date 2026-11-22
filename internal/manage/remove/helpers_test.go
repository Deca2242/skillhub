package remove_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
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
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// The adapters satisfy the planner's read-only interfaces unchanged.
var (
	_ remove.Planner     = (*engine.Engine)(nil)
	_ remove.LockReader  = (*skillslock.Store)(nil)
	_ remove.StateReader = (*statefile.Store)(nil)
	_ remove.FileReader  = (*fsadapter.FileOps)(nil)
	_ remove.DirLister   = (*fsadapter.Scanner)(nil)
	_ remove.LinkReader  = (*fsadapter.LinkOps)(nil)
)

// osLinks reads links straight from the disk, for the graph only.
type osLinks struct{}

func (osLinks) Readlink(path string) (string, error) { return os.Readlink(path) }

// env is a fake home with the real adapters, the engine and the planner.
type env struct {
	t     *testing.T
	h     *testsupport.Home
	files *fsadapter.FileOps
	lock  *skillslock.Store
	state *statefile.Store
	eng   *engine.Engine
	git   *fakes.Git
	clock *fakes.Clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	h := testsupport.NewHome(t)
	p := h.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	lock := skillslock.New(p, files)
	e := &env{
		t: t, h: h, files: files, lock: lock, state: statefile.New(p),
		git:   fakes.NewGit(),
		clock: fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
	}
	e.eng = engine.New(engine.Deps{
		Paths: p, Files: files, Links: fsadapter.NewLinkOps(p.Guard()),
		Lock: lock, DropLock: lock, State: e.state,
		Dirs: fsadapter.NewScanner(p.Guard()), Clock: e.clock,
	})
	// skillhub has a state before it removes anything.
	must(t, e.state.Save(domain.PersistedState{}))
	return e
}

// machine is the fake home of this machine (graphtest.Machine), with its
// lock written through the lock store in the fixture's order.
func machine(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	entries := graphtest.Machine(e.h)
	real, err := filepath.EvalSymlinks(e.h.Path(graphtest.KarpathyClone))
	must(t, err)
	e.git.AddClone(real, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	for _, entry := range entries {
		e.addEntry(entry.Name, entry.Source, entry.SkillPath, entry.SkillFolderHash)
	}
	return e
}

// addEntry adds a lock entry in the layout npx skills writes.
func (e *env) addEntry(name, source, skillPath, folderHash string) {
	e.t.Helper()
	raw := fmt.Sprintf(`{"source":%q,"sourceType":"github","sourceUrl":"https://github.com/%s.git","skillPath":%q,"skillFolderHash":%q,"installedAt":"2026-11-01T00:00:00.000Z","updatedAt":"2026-11-01T00:00:00.000Z"}`,
		source, source, skillPath, folderHash)
	must(e.t, e.lock.Add(name, []byte(raw)))
}

// skill writes a skill folder in the store.
func (e *env) skill(key string) {
	e.t.Helper()
	e.h.WriteFile(".agents/skills/"+key+"/SKILL.md", graphtest.SkillMD(key, key))
	e.h.WriteFile(".agents/skills/"+key+"/notes.md", "notes of "+key+"\n")
}

// observe builds the graph of the fake home, the way the app does.
func (e *env) observe() (graph.Graph, error) {
	p := e.h.Paths()
	dirs := fsadapter.NewScanner(p.Guard())
	agents, err := data.LoadAgents(p, e.files)
	if err != nil {
		return graph.Graph{}, err
	}
	harnesses, err := data.LoadHarnesses(p, e.files)
	if err != nil {
		return graph.Graph{}, err
	}
	profiles, err := data.DiagnosticProfiles()
	if err != nil {
		return graph.Graph{}, err
	}
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: p, Agents: agents, Dirs: dirs, Files: e.files, Lock: e.lock, Git: e.git,
	}, nil)
	if err != nil {
		return graph.Graph{}, err
	}
	return graph.Build(graph.Deps{
		Paths: p, Harnesses: harnesses, Profiles: profiles, Inventory: skills,
		Dirs: dirs, Files: e.files, Links: osLinks{},
	})
}

func (e *env) remover() *remove.Remover {
	e.t.Helper()
	profiles, err := data.DiagnosticProfiles()
	must(e.t, err)
	return remove.New(remove.Deps{
		Paths: e.h.Paths(), Planner: e.eng, Lock: e.lock, State: e.state,
		Observe: e.observe, Profiles: profiles, Files: e.files,
		Dirs: fsadapter.NewScanner(e.h.Paths().Guard()), Links: osLinks{},
	})
}

// plan plans the removal of key and checks that planning wrote nothing.
func (e *env) plan(key string) remove.Plan {
	e.t.Helper()
	before := e.everything()
	p, err := e.remover().Remove(key)
	if err != nil {
		e.t.Fatalf("Remove(%q) error = %v", key, err)
	}
	e.sameAs(before, "planning")
	return p
}

// apply runs a planned removal.
func (e *env) apply(p remove.Plan) engine.Outcome {
	e.t.Helper()
	out, err := e.eng.Apply(context.Background(), p.Plan)
	if err != nil {
		e.t.Fatalf("Apply() error = %v", err)
	}
	return out
}

func (e *env) undo(out engine.Outcome) {
	e.t.Helper()
	if _, err := e.eng.Undo(context.Background(), out.SnapshotID); err != nil {
		e.t.Fatalf("Undo() error = %v", err)
	}
}

func (e *env) loadState() domain.PersistedState {
	e.t.Helper()
	st, err := e.state.Load()
	must(e.t, err)
	return st
}

func (e *env) saveState(st domain.PersistedState) {
	e.t.Helper()
	must(e.t, e.state.Save(st))
}

// world is everything a removal and its undo may change: the home tree
// (skillhub's own state, cache and restore scratch left out), the lock and
// state.json, as exact bytes.
type world struct {
	tree  map[string]string
	lock  string
	state string
}

func (e *env) everything() world {
	e.t.Helper()
	p := e.h.Paths()
	skip := []string{p.StateDir(), p.CacheDir(), filepath.Join(p.Home(), ".agents", ".skillhub", "restore")}
	t := tree(e.t, e.h.Root(), skip)
	// The directories that only hold skillhub's own areas are left out too;
	// what they hold (the parking zone included) is not.
	for _, dir := range skip {
		for d := filepath.Dir(dir); d != p.Home() && within(d, p.Home()); d = filepath.Dir(d) {
			rel, _ := filepath.Rel(e.h.Root(), d)
			if strings.HasPrefix(t[rel], "d") {
				delete(t, rel)
			}
		}
	}
	return world{
		tree:  t,
		lock:  fileOrAbsent(e.t, p.SkillsLockFile()),
		state: fileOrAbsent(e.t, p.StateFile()),
	}
}

func (e *env) sameAs(before world, when string) {
	e.t.Helper()
	after := e.everything()
	if before.lock != after.lock {
		e.t.Errorf("%s changed the lock:\nbefore %s\nafter  %s", when, before.lock, after.lock)
	}
	if before.state != after.state {
		e.t.Errorf("%s changed state.json:\nbefore %s\nafter  %s", when, before.state, after.state)
	}
	for _, d := range diffTrees(before.tree, after.tree) {
		e.t.Errorf("%s changed the home: %s", when, d)
	}
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

func fileOrAbsent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return "<absent>"
	}
	must(t, err)
	return string(data)
}

// tree lists every path below root with its type, permission bits, content
// hash or link target, skipping the given subtrees.
func tree(t *testing.T, root string, skip []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains(skip, p) {
			return filepath.SkipDir
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
	if errors.Is(err, iofs.ErrNotExist) {
		return map[string]string{}
	}
	must(t, err)
	return out
}

func diffTrees(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, "removed "+k)
		} else if v != w {
			out = append(out, fmt.Sprintf("changed %s: %s -> %s", k, v, w))
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "added "+k)
		}
	}
	slices.Sort(out)
	return out
}

// subtree is the part of a tree below rel.
func subtree(w world, rel string) map[string]string {
	out := map[string]string{}
	for k, v := range w.tree {
		if k == rel || strings.HasPrefix(k, rel+"/") {
			out[k] = v
		}
	}
	return out
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func linkPaths(links []remove.Link) []string {
	var out []string
	for _, l := range links {
		out = append(out, l.Path)
	}
	return out
}

// treeSHA is the tree SHA of a folder of the fake home.
func (e *env) treeSHA(rel string) string {
	e.t.Helper()
	tree, err := treehash.Dir(e.h.Path(rel))
	must(e.t, err)
	return tree.SHA
}

// disable parks key the way disabling does, through the engine: its link
// goes, its folder moves to the parking zone and its lock entry to Parked.
func (e *env) disable(key, linkRel string) {
	e.t.Helper()
	p := e.h.Paths()
	e.h.Mkdir(".agents/.skillhub/disabled")
	link := e.h.Path(linkRel)
	target, err := os.Readlink(link)
	must(e.t, err)
	parked := filepath.Join(p.ParkedDir(), key)
	b := e.eng.NewBuilder(engine.Meta{Action: "disable", Targets: []string{"claude-code"}})
	must(e.t, b.Unlink(link, target))
	must(e.t, b.Move(filepath.Join(p.StoreDir(), key), parked))
	must(e.t, b.LockPark(key))
	must(e.t, b.SetEffect(engine.Effect{
		DropManaged: []string{key},
		SetParked: []engine.ParkedEffect{{
			Skill:     domain.ParkedSkill{Key: key, Folder: parked, Links: []domain.LinkRecord{{Path: link, Target: target}}},
			EntryStep: 2,
		}},
	}))
	plan, err := b.Build()
	must(e.t, err)
	if _, err := e.eng.Apply(context.Background(), plan); err != nil {
		e.t.Fatalf("disable %s: %v", key, err)
	}
}
