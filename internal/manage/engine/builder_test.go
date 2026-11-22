package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

func TestBuilderRecordsEveryStepWithItsPreimageAndPostimage(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	alphaRaw := "{\n  \"source\": \"owner/alpha\",\n  \"sourceType\": \"github\",\n  \"ref\": \"v1\",\n" +
		"  \"skillFolderHash\": \"1111111111111111111111111111111111111111\"\n}"

	if len(plan.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(plan.Steps))
	}
	unlink, move, park, remove := plan.Steps[0], plan.Steps[1], plan.Steps[2], plan.Steps[3]
	if want := []engine.Image{{Path: fp.link, State: engine.StateLink, Target: fp.linkTarget}}; !reflect.DeepEqual(unlink.Pre, want) {
		t.Errorf("unlink pre = %+v", unlink.Pre)
	}
	if want := []engine.Image{{Path: fp.link, State: engine.StateAbsent}}; !reflect.DeepEqual(unlink.Post, want) {
		t.Errorf("unlink post = %+v", unlink.Post)
	}
	sha := move.Pre[0].TreeSHA
	if move.Pre[0].State != engine.StateTree || len(sha) != 40 || move.Pre[1].State != engine.StateAbsent {
		t.Errorf("move pre = %+v", move.Pre)
	}
	if move.Post[0].State != engine.StateAbsent || move.Post[1].State != engine.StateTree || move.Post[1].TreeSHA != sha {
		t.Errorf("move post = %+v", move.Post)
	}
	if park.Pre[0].State != engine.StateEntry || string(park.Pre[0].Raw) != alphaRaw {
		t.Errorf("park pre = %+v (%s)", park.Pre, park.Pre[0].Raw)
	}
	if park.Post[0].State != engine.StateAbsent {
		t.Errorf("park post = %+v", park.Post)
	}
	if remove.Pre[0].State != engine.StateTree || remove.Post[0].State != engine.StateAbsent {
		t.Errorf("removeTree images = %+v -> %+v", remove.Pre, remove.Post)
	}
	lockSHA, _ := e.lock.Hash()
	if !plan.TouchesLock || plan.LockSHA != lockSHA {
		t.Errorf("plan lock = %t %q, want true %q", plan.TouchesLock, plan.LockSHA, lockSHA)
	}
}

func TestBuilderFollowsWhatEarlierStepsLeave(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove"})
	must(t, b.Move(fp.alpha, fp.parked))
	must(t, b.Unlink(fp.link, fp.linkTarget))
	must(t, b.RemoveTree(fp.beta))
	// Paths may not repeat in a plan; a lock entry may: it is parked, then
	// put back from what the park left.
	must(t, b.LockPark("alpha"))
	must(t, b.LockUnpark(domain.ParkedEntry{Name: "alpha", Raw: []byte(`{"source":"owner/alpha"}`), Before: "beta"}))
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	b = eng.NewBuilder(engine.Meta{Action: "remove"})
	must(t, b.Unlink(fp.link, fp.linkTarget))
	if err := b.Unlink(fp.link, fp.linkTarget); !errors.Is(err, engine.ErrPrecondition) {
		t.Errorf("unlinking twice: error = %v, want ErrPrecondition", err)
	}
	if err := b.LockPark("beta"); !errors.Is(err, engine.ErrPrecondition) {
		t.Errorf("a step after a refused one: error = %v, want the first error", err)
	}
	if _, err := b.Build(); !errors.Is(err, engine.ErrPrecondition) {
		t.Errorf("Build after a refused step: error = %v, want ErrPrecondition", err)
	}
}

func TestBuilderRefusesStepsWhoseObjectsAreNotAsTheyNeed(t *testing.T) {
	newEntry := []byte(`{"source":"owner/new","sourceType":"github"}`)
	tests := []struct {
		name  string
		setup func(t *testing.T, e *env)
		step  func(b *engine.Builder, fp fixturePaths) error
		want  error
	}{
		{"move a folder that is not there", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.Move(fp.parked, fp.alpha+"2") }, engine.ErrPrecondition},
		{"move over a path that exists", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.Move(fp.alpha, fp.beta) }, engine.ErrPrecondition},
		{"move a link as a folder", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.Move(fp.link, fp.parked) }, engine.ErrPrecondition},
		{"link over a path that exists", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.Link(fp.link, fp.linkTarget) }, engine.ErrPrecondition},
		{"unlink a link with another target", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.Unlink(fp.link, fp.linkTarget+"/") }, engine.ErrPrecondition},
		{"unlink a folder", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.Unlink(fp.alpha, fp.linkTarget) }, engine.ErrPrecondition},
		{"remove a link as a folder", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.RemoveTree(fp.link) }, engine.ErrPrecondition},
		{"remove a file as a folder", nil,
			func(b *engine.Builder, fp fixturePaths) error {
				return b.RemoveTree(filepath.Join(fp.alpha, "SKILL.md"))
			}, engine.ErrPrecondition},
		{"park an entry that is not in the lock", nil,
			func(b *engine.Builder, _ fixturePaths) error { return b.LockPark("gamma") }, engine.ErrPrecondition},
		{"park with no lock", func(t *testing.T, e *env) { writeLock(t, e.p, nil) },
			func(b *engine.Builder, _ fixturePaths) error { return b.LockPark("alpha") }, engine.ErrPrecondition},
		{"unpark an entry that is in the lock", nil,
			func(b *engine.Builder, _ fixturePaths) error {
				return b.LockUnpark(domain.ParkedEntry{Name: "alpha", Raw: []byte(`{"source":"owner/alpha"}`)})
			}, engine.ErrPrecondition},
		{"unpark with no lock", func(t *testing.T, e *env) { writeLock(t, e.p, nil) },
			func(b *engine.Builder, _ fixturePaths) error {
				return b.LockUnpark(domain.ParkedEntry{Name: "alpha", Raw: []byte(`{"source":"owner/alpha"}`)})
			}, engine.ErrPrecondition},
		{"unpark an entry that is not an object", nil,
			func(b *engine.Builder, _ fixturePaths) error {
				return b.LockUnpark(domain.ParkedEntry{Name: "gamma", Raw: []byte(`[1]`)})
			}, engine.ErrInvalidStep},
		{"add an entry that is in the lock", nil,
			func(b *engine.Builder, _ fixturePaths) error { return b.LockAdd("beta", newEntry) }, engine.ErrPrecondition},
		{"add an entry that is not JSON", nil,
			func(b *engine.Builder, _ fixturePaths) error { return b.LockAdd("gamma", []byte(`{`)) }, engine.ErrInvalidStep},
		{"remove an entry that changed", nil,
			func(b *engine.Builder, _ fixturePaths) error {
				return b.LockRemove("beta", []byte(`{"source":"owner/beta"}`))
			}, engine.ErrPrecondition},
		{"remove an entry that is not there", nil,
			func(b *engine.Builder, _ fixturePaths) error { return b.LockRemove("gamma", newEntry) }, engine.ErrPrecondition},
		{"a lock of version 4", func(t *testing.T, e *env) {
			writeLock(t, e.p, []byte(strings.Replace(fixtureLock, `"version": 3`, `"version": 4`, 1)))
		}, func(b *engine.Builder, _ fixturePaths) error { return b.LockPark("alpha") }, ports.ErrUnsupportedLockVersion},
		{"a relative path", nil,
			func(b *engine.Builder, _ fixturePaths) error { return b.RemoveTree(".agents/skills/alpha") }, engine.ErrInvalidStep},
		{"a path that is not clean", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.RemoveTree(fp.alpha + "/../alpha") }, engine.ErrInvalidStep},
		{"a link with no target", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.Link(fp.parked, "") }, engine.ErrInvalidStep},
		{"an entry with no name", nil,
			func(b *engine.Builder, _ fixturePaths) error { return b.LockPark("") }, engine.ErrInvalidStep},
		{"a folder holding a named pipe", func(t *testing.T, e *env) {
			must(t, syscall.Mkfifo(e.home.Path(".agents/skills/alpha/pipe"), 0o644))
		}, func(b *engine.Builder, fp fixturePaths) error { return b.Move(fp.alpha, fp.parked) }, engine.ErrUnsupportedTree},
		{"a clone", func(t *testing.T, e *env) {
			e.home.WriteFile(".agents/skills/beta/.git/HEAD", "ref: refs/heads/main\n")
		}, func(b *engine.Builder, fp fixturePaths) error { return b.RemoveTree(fp.beta) }, engine.ErrUnsupportedTree},
		{"a path the guard forbids", nil,
			func(b *engine.Builder, fp fixturePaths) error { return b.RemoveTree(fp.forbidden) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, fp := newEnv(t)
			if tt.setup != nil {
				tt.setup(t, e)
			}
			before := tree(t, e.home.Root())
			b := e.engine().NewBuilder(engine.Meta{Action: "test"})

			err := tt.step(b, fp)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
			if _, buildErr := b.Build(); buildErr == nil {
				t.Error("Build succeeded after a refused step")
			}
			if got := tree(t, e.home.Root()); !reflect.DeepEqual(got, before) {
				t.Error("a refused step changed the disk")
			}
		})
	}
}

func TestLockAddWithNoLockRecordsThatThereWasNone(t *testing.T) {
	e, _ := newEnv(t)
	writeLock(t, e.p, nil)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "install", Source: "owner/new", ReviewedTreeSHA: "abc"})
	raw := []byte("{\n  \"source\": \"owner/new\"\n}")
	must(t, b.LockAdd("new", raw))
	plan, err := b.Build()
	must(t, err)

	step := plan.Steps[0]
	if want := []engine.Image{{Entry: "new", State: engine.StateNoLock}}; !reflect.DeepEqual(step.Pre, want) {
		t.Errorf("pre = %+v, want no lock", step.Pre)
	}
	if step.Post[0].State != engine.StateEntry || string(step.Post[0].Raw) != string(raw) {
		t.Errorf("post = %+v", step.Post)
	}
	if !plan.TouchesLock || plan.LockSHA != "" {
		t.Errorf("plan lock = %t %q, want true and no hash", plan.TouchesLock, plan.LockSHA)
	}
	must(t, eng.Check(plan))
}

func TestCheckRefusesWhatChangedSinceThePlanWasBuilt(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, e *env, fp fixturePaths)
		want   error
	}{
		{"a file of a moved folder changed", func(t *testing.T, e *env, _ fixturePaths) {
			e.home.WriteFile(".agents/skills/alpha/SKILL.md", "changed\n")
		}, engine.ErrPrecondition},
		{"a removed folder gained a file", func(t *testing.T, e *env, _ fixturePaths) {
			e.home.WriteFile(".agents/skills/beta/extra.md", "new\n")
		}, engine.ErrPrecondition},
		{"the execute bit of a file changed", func(t *testing.T, e *env, _ fixturePaths) {
			must(t, os.Chmod(e.home.Path(".agents/skills/alpha/scripts/run.sh"), 0o644))
		}, engine.ErrPrecondition},
		{"the link points elsewhere", func(t *testing.T, e *env, fp fixturePaths) {
			must(t, os.Remove(fp.link))
			e.home.Symlink(".claude/skills/alpha", "../../.agents/skills/beta")
		}, engine.ErrPrecondition},
		{"the link is gone", func(t *testing.T, _ *env, fp fixturePaths) {
			must(t, os.Remove(fp.link))
		}, engine.ErrPrecondition},
		{"the parking place is taken", func(t *testing.T, e *env, _ fixturePaths) {
			e.home.Mkdir(".agents/.skillhub/disabled/alpha")
		}, engine.ErrPrecondition},
		{"another entry of the lock changed", func(t *testing.T, e *env, _ fixturePaths) {
			writeLock(t, e.p, []byte(strings.Replace(fixtureLock, "2222", "3333", 1)))
		}, engine.ErrPlanChanged},
		{"the lock is gone", func(t *testing.T, e *env, _ fixturePaths) {
			writeLock(t, e.p, nil)
		}, engine.ErrPlanChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, fp := newEnv(t)
			eng := e.engine()
			plan := disablePlan(t, eng, fp)
			tt.change(t, e, fp)
			release, err := e.state.Lock(t.Context()) // create the state lock file Begin opens
			must(t, err)
			must(t, release())
			before := tree(t, e.home.Root())

			if err := eng.Check(plan); !errors.Is(err, tt.want) {
				t.Errorf("Check error = %v, want %v", err, tt.want)
			}
			run, err := eng.Begin(t.Context(), plan)
			if !errors.Is(err, tt.want) {
				t.Errorf("Begin error = %v, want %v", err, tt.want)
			}
			if run != nil {
				must(t, run.Release())
			}
			if after := tree(t, e.home.Root()); !reflect.DeepEqual(after, before) {
				t.Errorf("a refused plan changed the disk:\n%v\nwant:\n%v", after, before)
			}
			if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
				t.Error("a refused plan left a record or a snapshot")
			}
		})
	}
}

func TestStepProgressTellsNotRunDoneAndForeign(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	unlink, move := plan.Steps[0], plan.Steps[1]

	check := func(s engine.Step, want engine.Progress) {
		t.Helper()
		got, _, err := eng.StepProgress(s)
		if err != nil || got != want {
			t.Errorf("%s progress = %q, %v; want %q", s.Kind, got, err, want)
		}
	}
	check(unlink, engine.NotRun)
	check(move, engine.NotRun)

	must(t, os.Remove(fp.link))
	must(t, os.Rename(fp.alpha, fp.parked))
	check(unlink, engine.Done)
	check(move, engine.Done)

	e.home.Symlink(".claude/skills/alpha", "elsewhere")
	check(unlink, engine.Foreign)
	e.home.WriteFile(".agents/.skillhub/disabled/alpha/SKILL.md", "rewritten\n")
	check(move, engine.Foreign)
	_, seen, err := eng.StepProgress(unlink)
	if err != nil || len(seen) != 1 || seen[0].Target != "elsewhere" {
		t.Errorf("seen = %+v, %v", seen, err)
	}
}
