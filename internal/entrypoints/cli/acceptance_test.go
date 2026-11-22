package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/cli"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// TestTheVersion2AcceptanceMatrix walks the acceptance matrix of
// docs/design-v2.md section 11 through the command line, on one skill
// installed from the fake GitHub:
//
//	install → update (v1 apply and undo) → disable → enable (and an enable
//	in the wrong order with parked neighbours) → unlink → link → remove →
//	undo v2:remove → adopt-off → doctor fix
//
// With the build tag acceptance (go test -tags acceptance), every command
// that writes is first run once per write it makes with that write crashing (a panic that stops the process there: that write and every
// later one never happen) and once per write with only that write failing,
// each followed by two recovers: the home, skillhub's state and cache aside,
// must then be exactly as before the command or exactly as after it. At every
// step the lock is a fixed point of JSON.stringify(..., null, 2) and no entry
// gets a ref it did not have. At the end, undoing every operation still in
// effect, newest first, gives back the starting home, lock, Managed and
// Parked byte for byte. Without the tag, or with -short, the matrix runs
// without the faults, in seconds instead of minutes.
func TestTheVersion2AcceptanceMatrix(t *testing.T) {
	m := newMachine(t)
	a := newAcceptance(t, m)
	a.faults = injectFaults && !testing.Short()
	m.ghostLink() // a broken link of our own, for doctor fix
	commit := a.publishTidy("body\n")
	a.npxLock()
	start := a.capture()

	// 1. Install.
	a.write("skills", "install", "acme/tools", "--to", "claude-code")
	a.checkInstalled(commit)

	// 2. Update: the version 1 flow on the skill version 2 installed.
	installed := a.capture()
	a.publishTidy("body\nupdated upstream\n")
	// The other sources are not in the fake GitHub: check exits 2 for them.
	if r := m.run("", "check"); !regexp.MustCompile(`lock:tidy +actualización disponible`).MatchString(r.stdout) {
		t.Fatalf("check does not find the update of tidy: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if r := m.ok("updates", "list"); !strings.Contains(r.stdout, "tidy") {
		t.Fatalf("updates list does not show tidy:\n%s", r.stdout)
	}
	a.direct("updates", "apply", "tidy", "--yes")
	if !strings.Contains(mustRead(t, m.h.Path(".agents/skills/tidy/SKILL.md")), "updated upstream") {
		t.Fatal("updates apply did not update tidy")
	}
	a.direct("undo", "tidy")
	a.same("after the v1 undo", installed, a.capture())

	// 3. Disable and enable, then an enable in the wrong order.
	a.write("skills", "disable", "tidy")
	if m.exists(".agents/skills/tidy") || m.exists(".claude/skills/tidy") || !m.exists(".agents/.skillhub/disabled/tidy") {
		t.Fatal("disable did not park tidy and drop its link")
	}
	a.write("skills", "enable", "tidy")
	a.same("after disable and enable", installed, a.capture())
	// The first two keys of the lock, both in the store: disabling the first
	// and then its neighbour makes enabling the first refuse until the
	// neighbour is back.
	first, neighbour := a.neighbours()
	a.write("skills", "disable", first)
	a.write("skills", "disable", neighbour)
	before := m.hash()
	r := m.run("", "skills", "enable", first, "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "skillhub skills enable "+neighbour) {
		t.Fatalf("enable in the wrong order = exit %d, stderr %q; want 2 naming %s", r.code, r.stderr, neighbour)
	}
	if m.hash() != before {
		t.Fatal("the refused enable wrote something")
	}
	a.write("skills", "enable", neighbour)
	a.write("skills", "enable", first)
	a.same("after the enables in the right order", installed, a.capture())

	// 4. Unlink and link in a harness with a link_dir.
	a.write("skills", "unlink", "tidy", "--from", "claude-code")
	if m.exists(".claude/skills/tidy") || !m.exists(".agents/skills/tidy") {
		t.Fatal("unlink did not drop only the link")
	}
	a.write("skills", "link", "tidy", "--to", "claude-code")
	a.same("after unlink and link", installed, a.capture())

	// 5. Remove, then undo v2:remove.
	a.write("skills", "remove", "tidy")
	if m.exists(".agents/skills/tidy") || m.exists(".claude/skills/tidy") || strings.Contains(a.lock(), `"tidy"`) {
		t.Fatal("remove left the folder, the link or the lock entry")
	}
	removed := a.ops[len(a.ops)-1]
	a.ops = a.ops[:len(a.ops)-1]
	a.write("undo", removed.target, "--to", removed.snapshot)
	a.same("after remove and its undo", installed, a.capture())

	// 6. Adopt OFF/ and repair our own broken link.
	a.write("skills", "adopt-off")
	if m.exists(".agents/skills/OFF/accessibility") || !m.exists(".agents/skills/OFF/README.md") {
		t.Fatal("adopt-off did not park OFF/ or touched its README")
	}
	a.write("doctor", "fix", "broken_link:.claude/skills/ghost", "--repair", "delete_link")
	if m.exists(".claude/skills/ghost") {
		t.Fatal("doctor fix left the broken link")
	}

	// 7. The round trip: undo every operation still in effect, newest first.
	ops := slices.Clone(a.ops)
	a.ops = nil
	for i := len(ops) - 1; i >= 0; i-- {
		a.write("undo", ops[i].target, "--to", ops[i].snapshot)
	}
	a.same("after undoing everything", start, a.capture())
}

// acceptance drives the matrix on one machine.
type acceptance struct {
	t *testing.T
	m *machine
	// refs are the refs the entries of the starting lock carry.
	refs map[string]string
	// ops are the version 2 operations applied and still in effect, oldest
	// first.
	ops []v2op
	// published is whether acme/tools is in the fake GitHub.
	published bool
	// faults is whether each write is also crashed and failed.
	faults bool
}

// v2op is a version 2 operation as undo names it.
type v2op struct{ target, snapshot string }

func newAcceptance(t *testing.T, m *machine) *acceptance {
	t.Helper()
	// The journal and the snapshots live in the state directory.
	must(t, os.MkdirAll(m.p.StateDir(), 0o755))
	return &acceptance{t: t, m: m}
}

// publishTidy commits acme/tools with skills/tidy holding body and returns
// the commit.
func (a *acceptance) publishTidy(body string) string {
	a.t.Helper()
	if !a.published {
		a.m.gh.AddRepo("acme/tools", "main")
		a.published = true
	}
	writeTree(a.t, filepath.Join(a.m.upstream, "skills", "tidy"), map[string]string{
		"SKILL.md": "---\nname: tidy\ndescription: tidy up\n---\n" + body,
	})
	return a.m.gh.Commit(a.t, "acme/tools", "main", a.m.upstream)
}

// npxLock rewrites the fixture lock with the bytes npx skills 1.9.0 writes,
// JSON.stringify(lock, null, 2) with no final newline, and keeps the refs of
// its entries.
func (a *acceptance) npxLock() {
	a.t.Helper()
	lock := []byte(a.lock())
	must(a.t, os.WriteFile(a.m.p.SkillsLockFile(), []byte(npxStringify(a.t, lock)), 0o644))
	a.refs = lockRefs(a.t, []byte(a.lock()))
	a.checkLock()
}

func (a *acceptance) lock() string { return mustRead(a.t, a.m.p.SkillsLockFile()) }

// checkLock is the interoperability check of every step: the lock is a fixed
// point of JSON.stringify(..., null, 2) and no entry carries a ref it did
// not have at the start.
func (a *acceptance) checkLock() {
	a.t.Helper()
	lock := a.lock()
	if got := npxStringify(a.t, []byte(lock)); got != lock {
		a.t.Fatalf("the lock is not what JSON.stringify writes:\n%s\nwant\n%s", lock, got)
	}
	for key, ref := range lockRefs(a.t, []byte(lock)) {
		if was, ok := a.refs[key]; !ok || was != ref {
			a.t.Fatalf("entry %s carries the ref %q it did not have", key, ref)
		}
	}
}

// home is the hash of the home without skillhub's state and cache.
func (a *acceptance) home() string {
	tree := a.tree()
	var lines []string
	for p, line := range tree {
		lines = append(lines, p+" "+line)
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// tree lists every path of the home, skillhub's state and cache aside, with
// its mode and its content's or link's identity, to name what differs. The
// directories that only hold the state or the cache (~/.cache, ~/.local)
// are left out with them.
func (a *acceptance) tree() map[string]string {
	a.t.Helper()
	out := map[string]string{}
	root := a.m.h.Root()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == a.m.p.StateDir() || p == a.m.p.CacheDir() {
			return filepath.SkipDir
		}
		if d.IsDir() && a.holdsOnlyOwn(p) {
			return filepath.SkipDir
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line := info.Mode().String()
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
			line += " " + string(data)
		}
		out[strings.TrimPrefix(p, root)] = line
		return nil
	})
	must(a.t, err)
	return out
}

// holdsOnlyOwn is whether dir is an ancestor of the state or cache
// directory holding nothing but them and other such ancestors.
func (a *acceptance) holdsOnlyOwn(dir string) bool {
	own := []string{a.m.p.StateDir(), a.m.p.CacheDir()}
	ancestor := false
	for _, o := range own {
		if strings.HasPrefix(o, dir+string(filepath.Separator)) {
			ancestor = true
		}
	}
	if !ancestor {
		return false
	}
	entries, err := os.ReadDir(dir)
	must(a.t, err)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if slices.Contains(own, p) {
			continue
		}
		if !e.IsDir() || !a.holdsOnlyOwn(p) {
			return false
		}
	}
	return true
}

// diff names the paths that differ between two trees, at most ten.
func diff(want, got map[string]string) string {
	var lines []string
	for p, w := range want {
		if g, ok := got[p]; !ok {
			lines = append(lines, "missing "+p)
		} else if g != w {
			lines = append(lines, "changed "+p)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			lines = append(lines, "extra "+p)
		}
	}
	slices.Sort(lines)
	if len(lines) > 10 {
		lines = append(lines[:10], "...")
	}
	return strings.Join(lines, "\n")
}

// snapshot is what the round trip compares: the home, the lock and the
// version 2 part of the state.
type snapshot struct {
	home, lock, v2 string
	tree           map[string]string
}

func (a *acceptance) capture() snapshot {
	a.t.Helper()
	st := a.m.persisted()
	v2, err := json.Marshal(struct {
		Managed map[string]domain.ManagedSkill `json:"managed,omitempty"`
		Parked  map[string]domain.ParkedSkill  `json:"parked,omitempty"`
	}{st.Managed, st.Parked})
	must(a.t, err)
	return snapshot{home: a.home(), lock: a.lock(), v2: string(v2), tree: a.tree()}
}

func (a *acceptance) same(when string, want, got snapshot) {
	a.t.Helper()
	if got.lock != want.lock {
		a.t.Errorf("%s the lock differs:\n%s\nwant\n%s", when, got.lock, want.lock)
	}
	if got.v2 != want.v2 {
		a.t.Errorf("%s Managed and Parked differ:\n%s\nwant\n%s", when, got.v2, want.v2)
	}
	if got.home != want.home {
		a.t.Errorf("%s the home differs:\n%s", when, diff(want.tree, got.tree))
	}
}

// checkInstalled checks what install leaves: the entry npx skills 1.9.0
// writes for a GitHub install with no ref, last in the lock; the relative
// link in Claude Code; and no Doctor finding about tidy.
func (a *acceptance) checkInstalled(commit string) {
	a.t.Helper()
	tree, err := a.m.gh.Tree(bgCtx, "acme/tools", commit, "skills/tidy")
	must(a.t, err)
	want := fmt.Sprintf(`,
    "tidy": {
      "source": "acme/tools",
      "sourceType": "github",
      "sourceUrl": "https://github.com/acme/tools.git",
      "commit": %q,
      "skillPath": "skills/tidy/SKILL.md",
      "skillFolderHash": %q,
      "installedAt": "2026-11-22T10:00:00.000Z",
      "updatedAt": "2026-11-22T10:00:00.000Z"
    }
  },
  "dismissed"`, commit, tree.SHA)
	if lock := a.lock(); !strings.Contains(lock, want) {
		a.t.Fatalf("the lock lacks the entry npx skills writes, last:\n%s\nwant it to contain\n%s", lock, want)
	}
	if target, err := os.Readlink(a.m.h.Path(".claude/skills/tidy")); err != nil || target != "../../.agents/skills/tidy" {
		a.t.Fatalf("link = %q, %v", target, err)
	}
	if !a.m.exists(".agents/skills/tidy/SKILL.md") || a.m.exists(".agents/.skillhub/staging") {
		a.t.Fatal("install did not publish tidy in the store, or left its staging")
	}
	composed, err := a.m.newApp()
	must(a.t, err)
	findings, err := composed.Doctor(bgCtx)
	must(a.t, err)
	for _, f := range findings {
		if f.Skill == "tidy" || f.Name == "tidy" || f.LockKey == "tidy" || strings.Contains(f.ID, "tidy") {
			a.t.Errorf("Doctor reports the installed skill: %+v", f)
		}
	}
	a.m.ok("doctor")
}

// neighbours are the first two keys of the lock, both folders in the store.
func (a *acceptance) neighbours() (string, string) {
	a.t.Helper()
	keys := lockKeys(a.t, []byte(a.lock()))
	if len(keys) < 2 {
		a.t.Fatalf("the lock has fewer than two entries: %q", keys)
	}
	for _, k := range keys[:2] {
		if info, err := os.Lstat(filepath.Join(a.m.p.StoreDir(), k)); err != nil || !info.IsDir() {
			a.t.Fatalf("%s is not a folder in the store: %v", k, err)
		}
	}
	return keys[0], keys[1]
}

// write runs a version 2 command the way a script does, --dry-run and then
// --yes --plan with the identifier shown, after its crash matrix.
func (a *acceptance) write(args ...string) {
	a.t.Helper()
	shown := a.m.ok(append(slices.Clone(args), "--dry-run")...)
	a.direct(append(slices.Clone(args), "--yes", "--plan", planID(a.t, shown.stdout))...)
	if args[0] != "undo" {
		last := a.m.lastV2()
		a.ops = append(a.ops, v2op{target: last.Target, snapshot: last.SnapshotID})
	}
}

// direct runs a command that writes: once to learn its writes and the home
// it leaves, then with each write crashing and with each write failing, each
// case followed by two recovers, and finally leaves the home as the clean
// run left it.
func (a *acceptance) direct(args ...string) {
	a.t.Helper()
	pre := a.save()
	before, beforeTree := a.home(), a.tree()
	count := &faults{}
	r, crashed := a.m.runFaulty(count, args...)
	if crashed || r.code != 0 {
		a.t.Fatalf("skillhub %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	after, afterTree := a.home(), a.tree()
	post := a.save()
	a.checkLock()
	if after == before {
		a.t.Fatalf("skillhub %v changed nothing in the home", args)
	}
	modes := []faultMode{crashAt, failAt}
	if !a.faults {
		modes = nil
	}
	for _, mode := range modes {
		for n := 1; n <= count.n; n++ {
			a.restore(pre)
			f := &faults{at: n, mode: mode}
			a.m.runFaulty(f, args...)
			a.m.run("", "recover")
			rec := a.m.run("", "recover")
			what := fmt.Sprintf("skillhub %v, %s write %d of %d", args, mode, n, count.n)
			if rec.code != 0 {
				a.t.Errorf("%s: the second recover = exit %d\nstdout: %s\nstderr: %s", what, rec.code, rec.stdout, rec.stderr)
			}
			if _, err := os.Lstat(engine.RecordFile(a.m.p)); err == nil {
				a.t.Errorf("%s: a version 2 operation is still pending after two recovers", what)
			}
			if _, pending, err := statefile.New(a.m.p).Operation(); err != nil || pending {
				a.t.Errorf("%s: a version 1 operation is still pending after two recovers (%v)", what, err)
			}
			if got := a.home(); got != before && got != after {
				now := a.tree()
				a.t.Errorf("%s (%s): after two recovers the home is neither as before nor as after\nagainst before:\n%s\nagainst after:\n%s",
					what, f.hit, diff(beforeTree, now), diff(afterTree, now))
			}
		}
	}
	a.restore(post)
	if a.t.Failed() {
		a.t.FailNow()
	}
}

// saved is a copy of the directories a command can write.
type saved map[string]string

// roots are the home and skillhub's own directories outside it.
func (a *acceptance) roots() []string {
	roots := []string{a.m.h.Root()}
	for _, d := range []string{a.m.p.StateDir(), a.m.p.CacheDir(), filepath.Dir(a.m.p.SkillsLockFile())} {
		if !strings.HasPrefix(d, a.m.h.Root()+string(filepath.Separator)) && !slices.Contains(roots, d) {
			roots = append(roots, d)
		}
	}
	return roots
}

func (a *acceptance) save() saved {
	a.t.Helper()
	s := saved{}
	for _, root := range a.roots() {
		dst := filepath.Join(a.t.TempDir(), "copy")
		if _, err := os.Lstat(root); err == nil {
			copyDir(a.t, root, dst)
		}
		s[root] = dst
	}
	return s
}

// restore puts every saved directory back exactly as it was.
func (a *acceptance) restore(s saved) {
	a.t.Helper()
	for root, copy := range s {
		removeDir(a.t, root)
		if _, err := os.Lstat(copy); err == nil {
			copyDir(a.t, copy, root)
		}
	}
}

// copyDir copies src to dst with every mode, file and link target.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	type dir struct {
		path string
		mode fs.FileMode
	}
	var dirs []dir
	err := filepath.WalkDir(src, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case info.IsDir():
			dirs = append(dirs, dir{to, info.Mode().Perm()})
			return os.MkdirAll(to, 0o700)
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.WriteFile(to, data, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(to, info.Mode().Perm())
		}
	})
	must(t, err)
	for i := len(dirs) - 1; i >= 0; i-- {
		must(t, os.Chmod(dirs[i].path, dirs[i].mode))
	}
}

// removeDir removes root even when a directory inside it is read-only.
func removeDir(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	must(t, os.RemoveAll(root))
}

// faultMode is how an injected write goes wrong.
type faultMode int

const (
	// crashAt stops the process at the write: neither it nor any later
	// write happens, and nothing runs after it but deferred calls.
	crashAt faultMode = iota
	// failAt makes only that write return an error.
	failAt
)

func (m faultMode) String() string {
	if m == crashAt {
		return "crash at"
	}
	return "failure of"
}

// crash is the panic that stands for the process dying.
type crash struct{}

// faults counts the writes a command makes through its ports and breaks
// write number at, counting from 1; at 0 only counts.
type faults struct {
	at, n   int
	mode    faultMode
	dead    bool
	hit     string
	release []func() error
}

func (f *faults) write(what string) error {
	f.n++
	if f.dead {
		panic(crash{})
	}
	if f.n != f.at {
		return nil
	}
	f.hit = what
	if f.mode == crashAt {
		f.dead = true
		panic(crash{})
	}
	return fmt.Errorf("injected failure of write %d (%s)", f.n, what)
}

// runFaulty runs args with an App whose files, links, lock and state go
// through f. A crash ends the run as a killed process would: the state lock
// it held is released, as the kernel releases it.
func (m *machine) runFaulty(f *faults, args ...string) (r result, crashed bool) {
	m.t.Helper()
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(crash); !ok {
				panic(p)
			}
			crashed = true
			for _, release := range f.release {
				_ = release()
			}
		}
	}()
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, cli.Env{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Executable: "/opt/skillhub/bin/skillhub",
		NewApp:     func() (*app.App, error) { return m.newFaultyApp(f) },
		TUI:        func(io.Writer, io.Writer) int { return 0 },
	})
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}, false
}

// newFaultyApp is newApp with every port that writes going through f.
func (m *machine) newFaultyApp(f *faults) (*app.App, error) {
	files := fsadapter.NewFileOps(m.p.Guard())
	agents, err := data.LoadAgents(m.p, files)
	if err != nil {
		return nil, err
	}
	lock := &faultyLock{f: f, lockStore: skillslock.New(m.p, files)}
	return app.New(app.Deps{
		Paths: m.p, Agents: agents, Files: &faultyFiles{f: f, FileOps: files},
		Dirs: fsadapter.NewScanner(m.p.Guard()), Lock: lock, ManagedLock: lock,
		Links: &faultyLinks{f: f, LinkOps: fsadapter.NewLinkOps(m.p.Guard())},
		State: &faultyState{f: f, StateStore: statefile.New(m.p)}, Settings: settings.New(m.p),
		GitHub: m.gh, Git: m.git, Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(),
		Clock: m.clock, ProcDir: m.proc,
	})
}

type faultyFiles struct {
	f *faults
	ports.FileOps
}

func (w *faultyFiles) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if err := w.f.write("WriteFileAtomic " + path); err != nil {
		return err
	}
	return w.FileOps.WriteFileAtomic(path, data, perm)
}

func (w *faultyFiles) Rename(oldPath, newPath string) error {
	if err := w.f.write("Rename " + oldPath); err != nil {
		return err
	}
	return w.FileOps.Rename(oldPath, newPath)
}

func (w *faultyFiles) CopyTree(src, dst string) error {
	if err := w.f.write("CopyTree " + dst); err != nil {
		return err
	}
	return w.FileOps.CopyTree(src, dst)
}

func (w *faultyFiles) RemoveTree(path string) error {
	if err := w.f.write("RemoveTree " + path); err != nil {
		return err
	}
	return w.FileOps.RemoveTree(path)
}

func (w *faultyFiles) MkdirAll(path string, perm fs.FileMode) error {
	if err := w.f.write("MkdirAll " + path); err != nil {
		return err
	}
	return w.FileOps.MkdirAll(path, perm)
}

func (w *faultyFiles) Chmod(path string, perm fs.FileMode) error {
	if err := w.f.write("Chmod " + path); err != nil {
		return err
	}
	return w.FileOps.Chmod(path, perm)
}

type faultyLinks struct {
	f *faults
	ports.LinkOps
}

func (w *faultyLinks) Symlink(target, path string) error {
	if err := w.f.write("Symlink " + path); err != nil {
		return err
	}
	return w.LinkOps.Symlink(target, path)
}

func (w *faultyLinks) RenameNoReplace(oldPath, newPath string) error {
	if err := w.f.write("RenameNoReplace " + oldPath + " " + newPath); err != nil {
		return err
	}
	return w.LinkOps.RenameNoReplace(oldPath, newPath)
}

func (w *faultyLinks) RemoveLink(path, target string) error {
	if err := w.f.write("RemoveLink " + path); err != nil {
		return err
	}
	return w.LinkOps.RemoveLink(path, target)
}

// lockStore is the skills lock as the App gets it, for version 1 and 2.
type lockStore interface {
	ports.SkillsLockStore
	app.ManagedLock
}

type faultyLock struct {
	f *faults
	lockStore
}

func (w *faultyLock) UpdateEntry(name string, update ports.LockEntryUpdate) error {
	if err := w.f.write("lock UpdateEntry " + name); err != nil {
		return err
	}
	return w.lockStore.UpdateEntry(name, update)
}

func (w *faultyLock) RestoreEntry(name string, raw []byte) error {
	if err := w.f.write("lock RestoreEntry " + name); err != nil {
		return err
	}
	return w.lockStore.RestoreEntry(name, raw)
}

func (w *faultyLock) Park(name string) (domain.ParkedEntry, error) {
	if err := w.f.write("lock Park " + name); err != nil {
		return domain.ParkedEntry{}, err
	}
	return w.lockStore.Park(name)
}

func (w *faultyLock) Unpark(entry domain.ParkedEntry) error {
	if err := w.f.write("lock Unpark"); err != nil {
		return err
	}
	return w.lockStore.Unpark(entry)
}

func (w *faultyLock) Add(name string, raw []byte) error {
	if err := w.f.write("lock Add " + name); err != nil {
		return err
	}
	return w.lockStore.Add(name, raw)
}

func (w *faultyLock) Remove(name string, expectedRaw []byte) (domain.ParkedEntry, error) {
	if err := w.f.write("lock Remove " + name); err != nil {
		return domain.ParkedEntry{}, err
	}
	return w.lockStore.Remove(name, expectedRaw)
}

func (w *faultyLock) DropEmpty() error {
	if err := w.f.write("lock DropEmpty"); err != nil {
		return err
	}
	return w.lockStore.DropEmpty()
}

type faultyState struct {
	f *faults
	ports.StateStore
}

// Lock keeps the release so a crash can release the lock as a killed
// process does; taking the lock is not a write.
func (w *faultyState) Lock(ctx context.Context) (func() error, error) {
	release, err := w.StateStore.Lock(ctx)
	if err == nil {
		w.f.release = append(w.f.release, release)
	}
	return release, err
}

func (w *faultyState) Save(st domain.PersistedState) error {
	if err := w.f.write("state Save"); err != nil {
		return err
	}
	return w.StateStore.Save(st)
}

func (w *faultyState) RecordOperation(op domain.Operation) error {
	if err := w.f.write("state RecordOperation"); err != nil {
		return err
	}
	return w.StateStore.RecordOperation(op)
}

func (w *faultyState) ClearOperation() error {
	if err := w.f.write("state ClearOperation"); err != nil {
		return err
	}
	return w.StateStore.ClearOperation()
}

func (w *faultyState) AppendJournal(e domain.HistoryEntry) error {
	if err := w.f.write("state AppendJournal"); err != nil {
		return err
	}
	return w.StateStore.AppendJournal(e)
}

// lockSkills decodes the skills of a lock, keys in order.
func lockSkills(t *testing.T, lock []byte) ([]string, map[string]map[string]json.RawMessage) {
	t.Helper()
	var doc struct {
		Skills json.RawMessage `json:"skills"`
	}
	must(t, json.Unmarshal(lock, &doc))
	entries := map[string]map[string]json.RawMessage{}
	must(t, json.Unmarshal(doc.Skills, &entries))
	dec := json.NewDecoder(bytes.NewReader(doc.Skills))
	var keys []string
	_, err := dec.Token()
	must(t, err)
	for dec.More() {
		tok, err := dec.Token()
		must(t, err)
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		must(t, dec.Decode(&skip))
	}
	return keys, entries
}

func lockKeys(t *testing.T, lock []byte) []string {
	t.Helper()
	keys, _ := lockSkills(t, lock)
	return keys
}

// lockRefs are the refs the entries of lock carry, by key.
func lockRefs(t *testing.T, lock []byte) map[string]string {
	t.Helper()
	_, entries := lockSkills(t, lock)
	refs := map[string]string{}
	for key, e := range entries {
		if raw, ok := e["ref"]; ok {
			refs[key] = string(raw)
		}
	}
	return refs
}

// npxStringify is JSON.stringify(JSON.parse(data), null, 2), as npx skills
// writes its lock (remove.mjs:4481-4486), for the values a lock holds: keys
// in order, two-space indentation, strings escaped as V8 escapes them. Like
// the emulator of the W5b tests, it is written independently of orderedjson.
func npxStringify(t *testing.T, data []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var b strings.Builder
	var value func(depth int) error
	value = func(depth int) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		pad := func(d int) { b.WriteString("\n" + strings.Repeat("  ", d)) }
		switch v := tok.(type) {
		case json.Delim:
			closing := map[json.Delim]string{'{': "}", '[': "]"}[v]
			b.WriteString(string(v))
			n := 0
			for dec.More() {
				if n > 0 {
					b.WriteString(",")
				}
				pad(depth + 1)
				if v == '{' {
					key, err := dec.Token()
					if err != nil {
						return err
					}
					b.WriteString(npxString(key.(string)) + ": ")
				}
				if err := value(depth + 1); err != nil {
					return err
				}
				n++
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			if n > 0 {
				pad(depth)
			}
			b.WriteString(closing)
		case string:
			b.WriteString(npxString(v))
		case json.Number:
			f, err := strconv.ParseFloat(string(v), 64)
			if err != nil {
				return err
			}
			b.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case nil:
			b.WriteString("null")
		}
		return nil
	}
	if err := value(0); err != nil {
		t.Fatalf("JSON.parse: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("JSON.parse: trailing data")
	}
	return b.String()
}

func npxString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
