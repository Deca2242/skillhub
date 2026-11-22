package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"skillhub/internal/adapters/clock"
	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// The existing adapters satisfy the engine's consumer interfaces unchanged
// (docs/design-v2.md section 15, point 2).
var (
	_ engine.Files       = (*fsadapter.FileOps)(nil)
	_ engine.Links       = (*fsadapter.LinkOps)(nil)
	_ engine.Dirs        = (*fsadapter.Scanner)(nil)
	_ engine.Lock        = (*skillslock.Store)(nil)
	_ engine.LockDropper = (*skillslock.Store)(nil)
	_ engine.State       = (*statefile.Store)(nil)
	_ engine.Clock       = clock.System{}
)

// fixtureLock is a version 3 lock in the layout npx skills writes.
const fixtureLock = `{
  "version": 3,
  "skills": {
    "alpha": {
      "source": "owner/alpha",
      "sourceType": "github",
      "ref": "v1",
      "skillFolderHash": "1111111111111111111111111111111111111111"
    },
    "beta": {
      "source": "owner/beta",
      "sourceType": "github",
      "skillFolderHash": "2222222222222222222222222222222222222222"
    }
  },
  "dismissed": {}
}`

// env is a fake home with real adapters: skills in the store, a link in a
// harness, the lock, and an engine whose file operations can fail on demand.
type env struct {
	t     *testing.T
	home  *testsupport.Home
	p     paths.Paths
	files *faultFiles
	state *statefile.Store
	lock  *skillslock.Store
	clock *fakes.Clock
	hash  func(string) (treehash.Tree, error)
}

// Fixture paths.
type fixturePaths struct {
	alpha, beta, link, parked, linkTarget string
	// forbidden is a path under the real home, which the guard refuses.
	forbidden string
}

func newEnv(t *testing.T) (*env, fixturePaths) {
	t.Helper()
	h := testsupport.NewHome(t)
	p := h.Paths()
	h.WriteFile(".agents/skills/alpha/SKILL.md", "---\nname: alpha\n---\nAlpha.\n")
	h.WriteFileMode(".agents/skills/alpha/scripts/run.sh", "#!/bin/sh\necho alpha\n", 0o755)
	h.WriteFileMode(".agents/skills/alpha/notes/private.txt", "only me\n", 0o600)
	h.Symlink(".agents/skills/alpha/README.md", "SKILL.md")
	h.WriteFile(".agents/skills/beta/SKILL.md", "---\nname: beta\n---\nBeta.\n")
	target := "../../.agents/skills/alpha"
	h.Symlink(".claude/skills/alpha", target)
	h.Mkdir(".agents/.skillhub/disabled")
	writeLock(t, p, []byte(fixtureLock))
	real := fsadapter.NewFileOps(p.Guard())
	e := &env{
		t: t, home: h, p: p,
		files: &faultFiles{Files: real},
		state: statefile.New(p),
		lock:  skillslock.New(p, real),
		clock: fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
	}
	return e, fixturePaths{
		alpha:      h.Path(".agents/skills/alpha"),
		beta:       h.Path(".agents/skills/beta"),
		link:       h.Path(".claude/skills/alpha"),
		parked:     h.Path(".agents/.skillhub/disabled/alpha"),
		linkTarget: target,
		forbidden:  filepath.Join(filepath.SplitList(h.Env().ForbidPrefix)[0], ".agents", "skills", "x"),
	}
}

func writeLock(t *testing.T, p paths.Paths, data []byte) {
	t.Helper()
	if data == nil {
		if err := os.Remove(p.SkillsLockFile()); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(p.SkillsLockFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.SkillsLockFile(), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// engine builds an engine over the env's adapters, through the fault
// wrapper.
func (e *env) engine() *engine.Engine {
	return engine.New(engine.Deps{
		Paths:    e.p,
		Files:    e.files,
		Links:    &faultLinks{LinkOps: fsadapter.NewLinkOps(e.p.Guard()), f: e.files},
		Lock:     &faultLock{Store: e.lock, f: e.files},
		DropLock: &faultDrop{d: e.lock, f: e.files},
		State:    &faultState{Store: e.state, f: e.files},
		Dirs:     fsadapter.NewScanner(e.p.Guard()),
		Clock:    e.clock,
		HashDir:  e.hash,
	})
}

// engineWithoutDropper is engine() with no LockDropper.
func (e *env) engineWithoutDropper() *engine.Engine {
	return engine.New(engine.Deps{
		Paths:   e.p,
		Files:   e.files,
		Links:   &faultLinks{LinkOps: fsadapter.NewLinkOps(e.p.Guard()), f: e.files},
		Lock:    &faultLock{Store: e.lock, f: e.files},
		State:   &faultState{Store: e.state, f: e.files},
		Dirs:    fsadapter.NewScanner(e.p.Guard()),
		Clock:   e.clock,
		HashDir: e.hash,
	})
}

// clean is an engine over the real adapters with no fault injected: what a
// later process sees.
func (e *env) clean() *engine.Engine {
	real := fsadapter.NewFileOps(e.p.Guard())
	lock := skillslock.New(e.p, real)
	return engine.New(engine.Deps{
		Paths: e.p, Files: real, Links: fsadapter.NewLinkOps(e.p.Guard()),
		Lock: lock, DropLock: lock, State: statefile.New(e.p),
		Dirs: fsadapter.NewScanner(e.p.Guard()), Clock: e.clock,
	})
}

// disablePlan is the plan of disabling alpha, with beta removed too so the
// snapshot keeps two folders, a link and a lock entry.
func disablePlan(t *testing.T, eng *engine.Engine, fp fixturePaths) engine.Plan {
	t.Helper()
	b := eng.NewBuilder(engine.Meta{Action: "disable", Targets: []string{"claude-code"}})
	must(t, b.Unlink(fp.link, fp.linkTarget))
	must(t, b.Move(fp.alpha, fp.parked))
	must(t, b.LockPark("alpha"))
	must(t, b.RemoveTree(fp.beta))
	plan, err := b.Build()
	must(t, err)
	return plan
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// tree is a recursive listing of a directory: every path with its type,
// permission bits, content or link target. It is the "nothing changed" check.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
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
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// snapshotDirs lists the version 2 snapshot directories on disk.
func snapshotDirs(t *testing.T, p paths.Paths) []string {
	t.Helper()
	entries, err := os.ReadDir(p.SnapshotsDir())
	if errors.Is(err, iofs.ErrNotExist) {
		return nil
	}
	must(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "op-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func recordExists(t *testing.T, p paths.Paths) bool {
	t.Helper()
	_, err := os.Lstat(engine.RecordFile(p))
	if errors.Is(err, iofs.ErrNotExist) {
		return false
	}
	must(t, err)
	return true
}

// ---------------------------------------------------------------------------
// faultFiles

// faultMode is how an injected failure behaves.
type faultMode int

const (
	// failOnce fails the chosen call without doing it; later calls work.
	failOnce faultMode = iota
	// crashBefore fails the chosen call without doing it and every write
	// after it: the process died before the call.
	crashBefore
	// crashAfter does the chosen call, then fails it and every write after
	// it: the process died right after the call.
	crashAfter
	// doneButFailed does the chosen call and reports a failure; later calls
	// work.
	doneButFailed
)

func (m faultMode) String() string {
	return [...]string{"failOnce", "crashBefore", "crashAfter", "doneButFailed"}[m]
}

// faultFiles is engine.Files over the real adapter that records its writes
// and fails the write numbered failAt (0-based) as mode says.
type faultFiles struct {
	engine.Files

	mu      sync.Mutex
	writes  []string
	failAt  int // -1 or unset with armed=false: never
	armed   bool
	mode    faultMode
	crashed bool
	// afterCopy, when set, runs after a successful CopyTree.
	afterCopy func(dst string)
	// afterWrite, when set, runs after every write that did not fail, with
	// its number and its log line.
	afterWrite func(n int, line string)
}

var errInjected = errors.New("injected failure")

func (f *faultFiles) arm(n int, mode faultMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt, f.mode, f.armed, f.crashed = n, mode, true, false
	f.writes = nil
}

func (f *faultFiles) writeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

// write runs one write through the fault plan.
func (f *faultFiles) write(op, path string, do func() error) error {
	f.mu.Lock()
	n := len(f.writes)
	f.writes = append(f.writes, op+" "+path)
	crashed := f.crashed
	hit := f.armed && n == f.failAt
	mode := f.mode
	if hit && (mode == crashBefore || mode == crashAfter) {
		f.crashed = true
	}
	f.mu.Unlock()
	switch {
	case crashed:
		return errInjected
	case hit && (mode == crashAfter || mode == doneButFailed):
		return errors.Join(do(), errInjected)
	case hit:
		return errInjected
	}
	if err := do(); err != nil {
		return err
	}
	if f.afterWrite != nil {
		f.afterWrite(n, op+" "+path)
	}
	return nil
}

func (f *faultFiles) WriteFileAtomic(path string, data []byte, perm iofs.FileMode) error {
	return f.write("WriteFileAtomic", path, func() error { return f.Files.WriteFileAtomic(path, data, perm) })
}

func (f *faultFiles) CopyTree(src, dst string) error {
	return f.write("CopyTree", dst, func() error {
		if err := f.Files.CopyTree(src, dst); err != nil {
			return err
		}
		if f.afterCopy != nil {
			f.afterCopy(dst)
		}
		return nil
	})
}

func (f *faultFiles) MkdirAll(path string, perm iofs.FileMode) error {
	return f.write("MkdirAll", path, func() error { return f.Files.MkdirAll(path, perm) })
}

func (f *faultFiles) Chmod(path string, perm iofs.FileMode) error {
	return f.write("Chmod", path, func() error { return f.Files.Chmod(path, perm) })
}

func (f *faultFiles) RemoveTree(path string) error {
	return f.write("RemoveTree", path, func() error { return f.Files.RemoveTree(path) })
}

// faultLinks, faultLock, faultDrop and faultState send the writes of the
// other ports through the same fault plan as faultFiles, so a crash can hit
// any write of an operation.
type faultLinks struct {
	*fsadapter.LinkOps
	f *faultFiles
}

func (l *faultLinks) Symlink(target, path string) error {
	return l.f.write("Symlink", path, func() error { return l.LinkOps.Symlink(target, path) })
}

func (l *faultLinks) RenameNoReplace(oldPath, newPath string) error {
	return l.f.write("RenameNoReplace", oldPath, func() error { return l.LinkOps.RenameNoReplace(oldPath, newPath) })
}

func (l *faultLinks) RemoveLink(path, target string) error {
	return l.f.write("RemoveLink", path, func() error { return l.LinkOps.RemoveLink(path, target) })
}

type faultLock struct {
	*skillslock.Store
	f *faultFiles
}

func (l *faultLock) Park(name string) (domain.ParkedEntry, error) {
	var out domain.ParkedEntry
	err := l.f.write("Park", name, func() (err error) { out, err = l.Store.Park(name); return err })
	return out, err
}

func (l *faultLock) Unpark(entry domain.ParkedEntry) error {
	return l.f.write("Unpark", entry.Name, func() error { return l.Store.Unpark(entry) })
}

func (l *faultLock) Add(name string, raw []byte) error {
	return l.f.write("Add", name, func() error { return l.Store.Add(name, raw) })
}

func (l *faultLock) Remove(name string, expectedRaw []byte) (domain.ParkedEntry, error) {
	var out domain.ParkedEntry
	err := l.f.write("Remove", name, func() (err error) { out, err = l.Store.Remove(name, expectedRaw); return err })
	return out, err
}

func (l *faultLock) RestoreEntry(name string, raw []byte) error {
	return l.f.write("RestoreEntry", name, func() error { return l.Store.RestoreEntry(name, raw) })
}

type faultDrop struct {
	d engine.LockDropper
	f *faultFiles
}

func (d *faultDrop) DropEmpty() error { return d.f.write("DropEmpty", "lock", d.d.DropEmpty) }

type faultState struct {
	*statefile.Store
	f *faultFiles
}

func (s *faultState) AppendJournal(entry domain.HistoryEntry) error {
	return s.f.write("AppendJournal", string(entry.Action), func() error { return s.Store.AppendJournal(entry) })
}

func (s *faultState) Save(st domain.PersistedState) error {
	return s.f.write("Save", "state", func() error { return s.Store.Save(st) })
}
