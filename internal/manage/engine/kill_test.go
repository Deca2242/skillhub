package engine_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"skillhub/internal/adapters/clock"
	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
)

// The child process of TestAKillDuringARestoreLeavesNothingInTheHarnesses
// gets the fake home and the snapshot to undo as flags of the test binary:
// this package may not read the environment (internal/arch). It never looks
// at the real home: its guard is the one its own testsupport.Main set up.
var (
	childHome     = flag.String("engine-child-home", "", "fake home of the engine child process")
	childSnapshot = flag.String("engine-child-snapshot", "", "snapshot the engine child undoes")
	childMode     = flag.String("engine-child-mode", "", "what the engine child does: undo or remove-beta")
)

// bigBeta gives beta enough files that copying it, with an fsync per file,
// lasts long enough for the child to see its temporary copy.
func bigBeta(t *testing.T, e *env) {
	t.Helper()
	for i := range 300 {
		e.home.WriteFile(fmt.Sprintf(".agents/skills/beta/data/f%03d.txt", i), fmt.Sprintf("line %d\n", i))
	}
}

// TestARestoreIsPreparedOutsideEveryHarnessDirectory checks, in process,
// where a restore builds its copy: never in the store, the parking zone or a
// harness link directory, where a crash would leave it as a skill.
func TestARestoreIsPreparedOutsideEveryHarnessDirectory(t *testing.T) {
	e, fp := newEnv(t)
	out, err := e.clean().Apply(t.Context(), disableEffectPlan(t, e, e.clean(), fp))
	must(t, err)
	var copies []string
	e.files.afterCopy = func(dst string) { copies = append(copies, dst) }

	_, err = e.engine().Undo(t.Context(), out.SnapshotID)
	must(t, err)

	restores := 0
	for _, dst := range copies {
		if strings.HasPrefix(dst, e.p.SnapshotsDir()+string(filepath.Separator)) {
			continue
		}
		restores++
		for _, dir := range []string{e.p.StoreDir(), e.p.ParkedDir(), e.home.Path(".claude/skills")} {
			if strings.HasPrefix(dst, dir+string(filepath.Separator)) {
				t.Errorf("a restore copy is built in %s: %s", dir, dst)
			}
		}
	}
	if restores == 0 {
		t.Fatalf("no restore copy was made: %q", copies)
	}
}

// TestAKillDuringARestoreLeavesNothingInTheHarnesses runs an undo in a child
// process that kills itself with SIGKILL as soon as the temporary copy of a
// restore appears, then recovers twice: the home must be exactly as before
// the undo, with nothing left in the store, the parking zone or a harness.
func TestAKillDuringARestoreLeavesNothingInTheHarnesses(t *testing.T) {
	for attempt := range 5 {
		e, fp := newEnv(t)
		bigBeta(t, e)
		out, err := e.clean().Apply(t.Context(), disableEffectPlan(t, e, e.clean(), fp))
		must(t, err)
		before := observeWorld(t, e)

		res, err := runEngineChild(t, "undo", e.home.Root(), out.SnapshotID)
		if err == nil {
			if strings.Contains(string(res.Stdout), "CHILD-NOT-KILLED") {
				t.Logf("attempt %d: the copy finished before the child saw it", attempt)
				continue
			}
			t.Fatalf("the child exited %d without being killed\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
		}
		if !strings.Contains(err.Error(), "killed") {
			t.Fatalf("the child failed: %v\nstdout: %s\nstderr: %s", err, res.Stdout, res.Stderr)
		}
		if !recordExists(t, e.p) {
			t.Fatal("the killed undo left no record")
		}
		for range 2 {
			_, err := e.clean().Recover(t.Context())
			must(t, err)
		}
		assertWorld(t, "after a kill during a restore and two recovers", observeWorld(t, e), before)
		return
	}
	t.Fatal("the child never saw the temporary copy")
}

// runEngineChild runs this test binary again, only for TestEngineChild,
// through testsupport.RunTestChild: the arguments go as flags, since this
// package may not read the environment.
func runEngineChild(t *testing.T, mode, home, snapshot string) (ports.Result, error) {
	t.Helper()
	return testsupport.RunTestChild(t, "TestEngineChild",
		"-engine-child-mode="+mode, "-engine-child-home="+home, "-engine-child-snapshot="+snapshot)
}

// TestEngineChild is the body of the child process.
func TestEngineChild(t *testing.T) {
	if *childHome == "" {
		t.Skip("helper for TestAKillDuringARestoreLeavesNothingInTheHarnesses")
	}
	p, err := paths.New(paths.Env{Home: *childHome, ForbidPrefix: paths.EnvFromOS().ForbidPrefix})
	must(t, err)
	real := fsadapter.NewFileOps(p.Guard())
	lock := skillslock.New(p, real)
	d := engine.Deps{
		Paths: p, Files: &killOnCopy{Files: real, snapshots: p.SnapshotsDir()},
		Links: fsadapter.NewLinkOps(p.Guard()), Lock: lock, DropLock: lock,
		State: statefile.New(p), Dirs: fsadapter.NewScanner(p.Guard()), Clock: clock.System{},
	}
	switch *childMode {
	case "undo":
		_, err = engine.New(d).Undo(context.Background(), *childSnapshot)
	case "remove-beta":
		beta := filepath.Join(p.StoreDir(), "beta")
		d.Files = &killMidDelete{Files: real}
		eng := engine.New(d)
		b := eng.NewBuilder(engine.Meta{Action: "remove"})
		must(t, b.RemoveTree(beta))
		plan, buildErr := b.Build()
		must(t, buildErr)
		_, err = eng.Apply(context.Background(), plan)
	default:
		t.Fatalf("unknown child mode %q", *childMode)
	}
	fmt.Printf("CHILD-NOT-KILLED %v\n", err)
}

// killMidDelete kills its own process with SIGKILL in the middle of deleting
// the skill folder: the first RemoveTree of a path that holds its data/
// removes half of those files by hand and then dies.
type killMidDelete struct {
	engine.Files
}

func (k *killMidDelete) RemoveTree(path string) error {
	data := filepath.Join(path, "data")
	if entries, err := os.ReadDir(data); err == nil && len(entries) > 10 {
		for _, en := range entries[:len(entries)/2] {
			_ = os.Remove(filepath.Join(data, en.Name()))
		}
		self, err := os.FindProcess(os.Getpid())
		if err == nil {
			_ = self.Kill()
		}
		select {} // the kill arrives
	}
	return k.Files.RemoveTree(path)
}

// TestAKillInTheMiddleOfARemovalIsRecovered removes a skill in a child that
// dies by SIGKILL half way through deleting it: two recovers must bring the
// skill back exactly, with nothing left in the store, the parking zone or a
// harness, and no leftover of the engine's own.
func TestAKillInTheMiddleOfARemovalIsRecovered(t *testing.T) {
	e, _ := newEnv(t)
	bigBeta(t, e)
	before := observeWorld(t, e)

	res, err := runEngineChild(t, "remove-beta", e.home.Root(), "")
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("the child was not killed: %v\nstdout: %s\nstderr: %s", err, res.Stdout, res.Stderr)
	}
	if !recordExists(t, e.p) {
		t.Fatal("the killed removal left no record")
	}
	for i := range 2 {
		if _, err := e.clean().Recover(t.Context()); err != nil {
			t.Fatalf("recover %d: %v", i+1, err)
		}
	}
	assertWorld(t, "after a kill during a removal and two recovers", observeWorld(t, e), before)
}

// killOnCopy kills its own process with SIGKILL as soon as the temporary
// sibling CopyTree builds for a copy outside the snapshots shows up.
type killOnCopy struct {
	engine.Files
	snapshots string
}

func (k *killOnCopy) CopyTree(src, dst string) error {
	if strings.HasPrefix(dst, k.snapshots+string(filepath.Separator)) {
		return k.Files.CopyTree(src, dst)
	}
	var done atomic.Bool
	go func() {
		prefix := "." + filepath.Base(dst) + ".copy-"
		for !done.Load() {
			entries, _ := os.ReadDir(filepath.Dir(dst))
			for _, en := range entries {
				if strings.HasPrefix(en.Name(), prefix) {
					self, err := os.FindProcess(os.Getpid())
					if err == nil {
						_ = self.Kill()
					}
				}
			}
		}
	}()
	err := k.Files.CopyTree(src, dst)
	done.Store(true)
	return err
}
