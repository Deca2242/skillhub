package updates_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

const (
	repo      = "acme/skills"
	skillName = "alpha"
	skillPath = "skills/alpha/SKILL.md"
	target    = "lock:alpha"
)

var (
	errInjected = errors.New("injected failure")
	t0          = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
)

// interruption is what a tripwire panics with to simulate the process dying
// right before a port call.
type interruption struct{ op string }

// tripwire counts the calls that reach the file system, the lock store and
// the state store, and fails or interrupts exactly one of them.
type tripwire struct {
	mu sync.Mutex
	n  int
	// at is the 1-based call to fail or interrupt; 0 disarms.
	at        int
	interrupt bool
	ops       []string
	// after, when set, fails or interrupts the first call that follows the
	// first call to the op it names.
	after string
	seen  bool
}

func (w *tripwire) step(op string) error {
	w.mu.Lock()
	w.n++
	n, at, interrupt := w.n, w.at, w.interrupt
	w.ops = append(w.ops, op)
	if w.after != "" {
		hit := w.seen
		if op == w.after {
			w.seen = true
		}
		if hit {
			w.after, w.seen, at = "", false, n
		}
	}
	w.mu.Unlock()
	if at == 0 || n != at {
		return nil
	}
	if interrupt {
		panic(interruption{op: op})
	}
	return fmt.Errorf("%s: %w", op, errInjected)
}

// arm resets the count and fails (or interrupts) call at.
func (w *tripwire) arm(at int, interrupt bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n, w.at, w.interrupt, w.ops, w.after, w.seen = 0, at, interrupt, nil, "", false
}

// armAfter fails (or interrupts) the call right after the first call to op.
func (w *tripwire) armAfter(op string, interrupt bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n, w.at, w.interrupt, w.ops, w.after, w.seen = 0, 0, interrupt, nil, op, false
}

// mark records that op finished, for armAfter, without counting a call.
func (w *tripwire) mark(op string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.after == op {
		w.seen = true
	}
}

func (w *tripwire) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// lockStore wraps the real lock store with the tripwire and a hook run
// before every Read, which tests use to play npx skills writing the lock.
type lockStore struct {
	inner      ports.SkillsLockStore
	trip       *tripwire
	reads      int
	beforeRead func(n int)
	// dropUpdates makes UpdateEntry report success without writing.
	dropUpdates bool
}

func (l *lockStore) Read() (domain.SkillsLock, error) {
	if err := l.trip.step("Lock.Read"); err != nil {
		return domain.SkillsLock{}, err
	}
	l.reads++
	if l.beforeRead != nil {
		l.beforeRead(l.reads)
	}
	return l.inner.Read()
}

func (l *lockStore) UpdateEntry(name string, u ports.LockEntryUpdate) error {
	if err := l.trip.step("Lock.UpdateEntry"); err != nil {
		return err
	}
	if l.dropUpdates {
		return nil
	}
	if err := l.inner.UpdateEntry(name, u); err != nil {
		return err
	}
	l.trip.mark("Lock.UpdateEntry.done")
	return nil
}

func (l *lockStore) RestoreEntry(name string, raw []byte) error {
	if err := l.trip.step("Lock.RestoreEntry"); err != nil {
		return err
	}
	return l.inner.RestoreEntry(name, raw)
}

// stateStore wraps the real state store with the tripwire. Lock is not
// counted: a process that dies releases its flock.
type stateStore struct {
	inner ports.StateStore
	trip  *tripwire
}

func (s *stateStore) Lock(ctx context.Context) (func() error, error) { return s.inner.Lock(ctx) }

func (s *stateStore) Load() (domain.PersistedState, error) {
	if err := s.trip.step("State.Load"); err != nil {
		return domain.PersistedState{}, err
	}
	return s.inner.Load()
}

func (s *stateStore) Save(st domain.PersistedState) error {
	if err := s.trip.step("State.Save"); err != nil {
		return err
	}
	return s.inner.Save(st)
}

func (s *stateStore) Operation() (domain.Operation, bool, error) {
	if err := s.trip.step("State.Operation"); err != nil {
		return domain.Operation{}, false, err
	}
	return s.inner.Operation()
}

func (s *stateStore) RecordOperation(op domain.Operation) error {
	if err := s.trip.step("State.RecordOperation"); err != nil {
		return err
	}
	return s.inner.RecordOperation(op)
}

func (s *stateStore) ClearOperation() error {
	if err := s.trip.step("State.ClearOperation"); err != nil {
		return err
	}
	return s.inner.ClearOperation()
}

func (s *stateStore) AppendJournal(e domain.HistoryEntry) error {
	if err := s.trip.step("State.AppendJournal"); err != nil {
		return err
	}
	return s.inner.AppendJournal(e)
}

func (s *stateStore) Journal() ([]domain.HistoryEntry, error) {
	if err := s.trip.step("State.Journal"); err != nil {
		return nil, err
	}
	return s.inner.Journal()
}

// fixture is a fake home with the real file system, lock and state adapters,
// a fake GitHub and a fake clock.
type fixture struct {
	t     *testing.T
	home  *testsupport.Home
	p     paths.Paths
	trip  *tripwire
	files *fakes.FailingFileOps
	lock  *lockStore
	state *stateStore
	gh    *fakes.GitHub
	clock *fakes.Clock
	svc   *updates.Service
	// upstream is the working copy of the fake repository.
	upstream string
	// extend, when set, completes the ports of every service the fixture
	// builds, such as the git clones of clone tests.
	extend func(*updates.Deps)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := testsupport.NewHome(t)
	p := home.Paths()
	f := &fixture{
		t:        t,
		home:     home,
		p:        p,
		trip:     &tripwire{},
		gh:       fakes.NewGitHub(),
		clock:    fakes.NewClock(t0),
		upstream: filepath.Join(t.TempDir(), "upstream"),
	}
	f.files = &fakes.FailingFileOps{Inner: fsadapter.NewFileOps(p.Guard())}
	f.files.Fail = func(c fakes.Call) error { return f.trip.step("Files." + c.Op) }
	f.lock = &lockStore{inner: skillslock.New(p, f.files), trip: f.trip}
	f.state = &stateStore{inner: statefile.New(p), trip: f.trip}
	f.gh.AddRepo(repo, "main")
	f.svc = f.service()
	return f
}

// deps are the fixture's ports, with gh as GitHub.
func (f *fixture) deps(gh ports.GitHubPort) updates.Deps {
	return updates.Deps{
		Paths:  f.p,
		Files:  f.files,
		Dirs:   fsadapter.NewScanner(f.p.Guard()),
		Lock:   f.lock,
		State:  f.state,
		GitHub: gh,
		Clock:  f.clock,
	}
}

// service builds a Service over the fixture's ports.
func (f *fixture) service() *updates.Service {
	f.t.Helper()
	d := f.deps(f.gh)
	if f.extend != nil {
		f.extend(&d)
	}
	svc, err := updates.New(d)
	if err != nil {
		f.t.Fatalf("updates.New: %v", err)
	}
	return svc
}

// reopen simulates a new process: fresh real stores over the same home,
// with no tripwire armed.
func (f *fixture) reopen() {
	f.t.Helper()
	f.trip.arm(0, false)
	f.lock = &lockStore{inner: skillslock.New(f.p, f.files), trip: f.trip}
	f.state = &stateStore{inner: statefile.New(f.p), trip: f.trip}
	f.svc = f.service()
}

// writeTree writes files (slash path -> content) below dir. A path ending in
// "*" is written executable, without the star.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, "*") {
			rel, mode = strings.TrimSuffix(rel, "*"), 0o755
		}
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(abs, mode); err != nil {
			t.Fatal(err)
		}
	}
}

// publish replaces the upstream folder of the skill alpha with files and
// commits it on branch; it returns the commit and the folder's tree SHA.
func (f *fixture) publish(branch string, files map[string]string) (commit, sha string) {
	f.t.Helper()
	return f.publishSkill(branch, skillName, files)
}

// publishSkill is publish for the skill name, under skills/<name>.
func (f *fixture) publishSkill(branch, name string, files map[string]string) (commit, sha string) {
	f.t.Helper()
	dir := filepath.Join(f.upstream, "skills", name)
	if err := os.RemoveAll(dir); err != nil {
		f.t.Fatal(err)
	}
	writeTree(f.t, dir, files)
	writeTree(f.t, f.upstream, map[string]string{"README.md": "repo readme\n"})
	return f.gh.Commit(f.t, repo, branch, f.upstream), hashOf(f.t, dir)
}

// publishWith publishes v2 after edit changed its folder.
func (f *fixture) publishWith(edit func(dir string)) {
	f.t.Helper()
	dir := filepath.Join(f.upstream, "skills", skillName)
	if err := os.RemoveAll(dir); err != nil {
		f.t.Fatal(err)
	}
	writeTree(f.t, dir, v2Files)
	edit(dir)
	f.gh.Commit(f.t, repo, "main", f.upstream)
}

func hashOf(t *testing.T, dir string) string {
	t.Helper()
	tree, err := treehash.Dir(dir)
	if err != nil {
		t.Fatalf("hash %s: %v", dir, err)
	}
	return tree.SHA
}

// install writes files as the installed folder of alpha and returns its
// tree SHA.
func (f *fixture) install(files map[string]string) string {
	f.t.Helper()
	return f.installSkill(skillName, files)
}

func (f *fixture) installSkill(name string, files map[string]string) string {
	f.t.Helper()
	dir := filepath.Join(f.p.StoreDir(), name)
	if err := os.RemoveAll(dir); err != nil {
		f.t.Fatal(err)
	}
	writeTree(f.t, dir, files)
	return hashOf(f.t, dir)
}

func (f *fixture) folder() string { return filepath.Join(f.p.StoreDir(), skillName) }

// lockEntry is one entry written by writeLock.
type lockEntry struct {
	name, source, skillPath, hash, ref, commit string
}

// writeLock writes the lock the way npx skills does, with unknown keys that
// must survive.
func (f *fixture) writeLock(entries ...lockEntry) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n    %q: {\n      \"source\": %q,\n      \"sourceType\": \"github\",\n", e.name, e.source)
		fmt.Fprintf(&b, "      \"sourceUrl\": \"https://github.com/%s.git\",\n", e.source)
		if e.ref != "" {
			fmt.Fprintf(&b, "      \"ref\": %q,\n", e.ref)
		}
		fmt.Fprintf(&b, "      \"skillPath\": %q,\n      \"skillFolderHash\": %q,\n", e.skillPath, e.hash)
		b.WriteString("      \"installedAt\": \"2026-01-01T00:00:00.000Z\",\n")
		b.WriteString("      \"updatedAt\": \"2026-01-02T00:00:00.000Z\",\n")
		// An unknown key and a value with the characters encoding/json escapes.
		b.WriteString("      \"note<&>\": \"q&a <b>\"")
		if e.commit != "" {
			fmt.Fprintf(&b, ",\n      \"commit\": %q", e.commit)
		}
		b.WriteString("\n    }")
	}
	b.WriteString("\n  },\n  \"dismissed\": {\n    \"findSkillsPrompt\": true\n  },\n  \"lastSelectedAgents\": [\n    \"claude-code\"\n  ]\n}\n")
	f.home.WriteFile(".agents/.skill-lock.json", b.String())
}

func (f *fixture) lockBytes() string {
	f.t.Helper()
	data, err := os.ReadFile(f.p.SkillsLockFile())
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

// agentsTree records every directory, file and link under ~/.agents, which
// holds both the store and the lock: mode and content, byte for byte.
func (f *fixture) agentsTree() map[string]string {
	f.t.Helper()
	root := f.home.Path(".agents")
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			out[rel] = "link " + target
			return err
		case info.IsDir():
			out[rel] = fmt.Sprintf("dir %v", info.Mode())
		default:
			data, err := os.ReadFile(p)
			out[rel] = fmt.Sprintf("%v %s", info.Mode(), data)
			return err
		}
		return nil
	})
	if err != nil {
		f.t.Fatalf("walk ~/.agents: %v", err)
	}
	return out
}

// assertAgentsTree fails when ~/.agents differs from want.
func (f *fixture) assertAgentsTree(want map[string]string, context string) {
	f.t.Helper()
	got := f.agentsTree()
	if maps.Equal(got, want) {
		return
	}
	keys := slices.Sorted(maps.Keys(got))
	for k := range want {
		if _, ok := got[k]; !ok {
			keys = append(keys, k)
		}
	}
	var diffs []string
	for _, k := range slices.Compact(slices.Sorted(slices.Values(keys))) {
		if got[k] != want[k] {
			diffs = append(diffs, fmt.Sprintf("  %s:\n    got  %.80q\n    want %.80q", k, got[k], want[k]))
		}
	}
	f.t.Fatalf("%s: ~/.agents changed:\n%s", context, strings.Join(diffs, "\n"))
}

func (f *fixture) persisted() domain.PersistedState {
	f.t.Helper()
	st, err := statefile.New(f.p).Load()
	if err != nil {
		f.t.Fatalf("load state: %v", err)
	}
	return st
}

func (f *fixture) candidate(id string) domain.UpdateCandidate {
	f.t.Helper()
	for _, c := range f.persisted().Candidates {
		if c.ID == id {
			return c
		}
	}
	f.t.Fatalf("candidate %s not in state", id)
	return domain.UpdateCandidate{}
}

func (f *fixture) operation() (domain.Operation, bool) {
	f.t.Helper()
	op, ok, err := statefile.New(f.p).Operation()
	if err != nil {
		f.t.Fatalf("read operation: %v", err)
	}
	return op, ok
}

func (f *fixture) journal() []domain.HistoryEntry {
	f.t.Helper()
	j, err := statefile.New(f.p).Journal()
	if err != nil {
		f.t.Fatalf("read journal: %v", err)
	}
	return j
}

func (f *fixture) check() updates.CheckReport {
	f.t.Helper()
	report, err := f.svc.Check(context.Background())
	if err != nil {
		f.t.Fatalf("Check: %v", err)
	}
	return report
}

// targetResult returns the check result of target t.
func targetResult(t *testing.T, r updates.CheckReport, tgt string) updates.TargetCheck {
	t.Helper()
	for _, tc := range r.Targets {
		if tc.Target == tgt {
			return tc
		}
	}
	t.Fatalf("no check result for %s in %+v", tgt, r.Targets)
	return updates.TargetCheck{}
}

// ghCalls counts the fake GitHub calls of op.
func (f *fixture) ghCalls(op string) int {
	n := 0
	for _, c := range f.gh.Calls() {
		if c.Op == op {
			n++
		}
	}
	return n
}

// Versions of the skill used across tests.
var (
	v1Files = map[string]string{
		"SKILL.md": "---\nname: alpha\ndescription: first\n---\nUse alpha.\n",
		"notes.md": "notes v1\n",
	}
	v2Files = map[string]string{
		"SKILL.md":        "---\nname: alpha\ndescription: second\n---\nUse alpha.\n",
		"notes.md":        "notes v2\nsee https://evil.example.io/x\n",
		"scripts/run.sh*": "#!/bin/sh\necho hi\n",
	}
	v3Files = map[string]string{
		"SKILL.md": "---\nname: alpha\ndescription: third\n---\nUse alpha.\n",
		"notes.md": "notes v3\n",
	}
)

// standard sets up v1 installed and recorded in the lock, and v1 published
// upstream on main; it returns v1's SHA.
func (f *fixture) standard() string {
	f.t.Helper()
	_, v1 := f.publish("main", v1Files)
	f.install(v1Files)
	f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: v1})
	return v1
}

// staged publishes v2 and checks, returning the pending candidate.
func (f *fixture) staged() domain.UpdateCandidate {
	f.t.Helper()
	f.publish("main", v2Files)
	r := f.check()
	tc := targetResult(f.t, r, target)
	if tc.Outcome != updates.CheckUpdateAvailable {
		f.t.Fatalf("check outcome = %s (%v), want update_available", tc.Outcome, tc.Err)
	}
	return f.candidate(tc.CandidateID)
}

func approval(c domain.UpdateCandidate) updates.Approval {
	return updates.Approval{CandidateID: c.ID, ToSHA: c.ToSHA}
}
