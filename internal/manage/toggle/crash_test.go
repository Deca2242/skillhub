package toggle_test

import (
	"bytes"
	"context"
	"errors"
	iofs "io/fs"
	"reflect"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
)

var errCrash = errors.New("injected crash")

// crash simulates a process that dies right after its k-th link, symlink or
// rename call: that call happens, and every write after it fails.
type crash struct {
	at, calls int
	dead      bool
}

func (c *crash) check() error {
	if c.dead {
		return errCrash
	}
	return nil
}

func (c *crash) done(err error) error {
	if err != nil {
		return err
	}
	c.calls++
	if c.calls == c.at {
		c.dead = true
		return errCrash
	}
	return nil
}

func (c *crash) wrap(d engine.Deps) engine.Deps {
	d.Files = crashFiles{d.Files, c}
	d.Links = crashLinks{d.Links, c}
	d.Lock = crashLock{d.Lock, c}
	d.DropLock = crashDrop{d.DropLock, c}
	d.State = crashState{d.State, c}
	return d
}

type crashFiles struct {
	engine.Files
	c *crash
}

func (f crashFiles) WriteFileAtomic(p string, data []byte, perm iofs.FileMode) error {
	if err := f.c.check(); err != nil {
		return err
	}
	return f.Files.WriteFileAtomic(p, data, perm)
}

func (f crashFiles) CopyTree(src, dst string) error {
	if err := f.c.check(); err != nil {
		return err
	}
	return f.Files.CopyTree(src, dst)
}

func (f crashFiles) RemoveTree(p string) error {
	if err := f.c.check(); err != nil {
		return err
	}
	return f.Files.RemoveTree(p)
}

type crashLinks struct {
	engine.Links
	c *crash
}

func (l crashLinks) Symlink(target, p string) error {
	if err := l.c.check(); err != nil {
		return err
	}
	return l.c.done(l.Links.Symlink(target, p))
}

func (l crashLinks) RenameNoReplace(oldPath, newPath string) error {
	if err := l.c.check(); err != nil {
		return err
	}
	return l.c.done(l.Links.RenameNoReplace(oldPath, newPath))
}

func (l crashLinks) RemoveLink(p, target string) error {
	if err := l.c.check(); err != nil {
		return err
	}
	return l.c.done(l.Links.RemoveLink(p, target))
}

type crashLock struct {
	engine.Lock
	c *crash
}

func (l crashLock) Park(name string) (domain.ParkedEntry, error) {
	if err := l.c.check(); err != nil {
		return domain.ParkedEntry{}, err
	}
	return l.Lock.Park(name)
}

func (l crashLock) Unpark(entry domain.ParkedEntry) error {
	if err := l.c.check(); err != nil {
		return err
	}
	return l.Lock.Unpark(entry)
}

func (l crashLock) Add(name string, raw []byte) error {
	if err := l.c.check(); err != nil {
		return err
	}
	return l.Lock.Add(name, raw)
}

func (l crashLock) Remove(name string, raw []byte) (domain.ParkedEntry, error) {
	if err := l.c.check(); err != nil {
		return domain.ParkedEntry{}, err
	}
	return l.Lock.Remove(name, raw)
}

type crashDrop struct {
	engine.LockDropper
	c *crash
}

func (d crashDrop) DropEmpty() error {
	if err := d.c.check(); err != nil {
		return err
	}
	return d.LockDropper.DropEmpty()
}

type crashState struct {
	engine.State
	c *crash
}

func (s crashState) Lock(ctx context.Context) (func() error, error) { return s.State.Lock(ctx) }

func (s crashState) AppendJournal(e domain.HistoryEntry) error {
	if err := s.c.check(); err != nil {
		return err
	}
	return s.State.AppendJournal(e)
}

func (s crashState) Save(st domain.PersistedState) error {
	if err := s.c.check(); err != nil {
		return err
	}
	return s.State.Save(st)
}

// TestACrashInEachPlannerIsRecovered: for each planner, the process dies in
// the middle of its plan; a later process's recover rolls it back and the
// home, the lock and the state are exactly as before.
func TestACrashInEachPlannerIsRecovered(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *machine)
		plan  func(m *machine) (toggle.Proposal, error)
		// at is the link call after which the process dies.
		at int
	}{
		{"disable", nil, func(m *machine) (toggle.Proposal, error) {
			return m.planner().Disable(m.graph(), "find-skills")
		}, 3}, // two unlinks, then the move; the lock step never runs
		{"enable", func(m *machine) {
			pr, err := m.planner().Disable(m.graph(), "find-skills")
			must(m.t, err)
			m.apply(pr)
		}, func(m *machine) (toggle.Proposal, error) {
			return m.planner().Enable("find-skills")
		}, 2}, // the move and the first link
		{"unlink", nil, func(m *machine) (toggle.Proposal, error) {
			return m.planner().Unlink(m.graph(), "find-skills", "claude-code")
		}, 1},
		{"link", func(m *machine) {
			pr, err := m.planner().Unlink(m.graph(), "find-skills", "claude-code")
			must(m.t, err)
			m.apply(pr)
		}, func(m *machine) (toggle.Proposal, error) {
			return m.planner().Link(m.graph(), "find-skills", "claude-code")
		}, 1},
		{"adopt-off", nil, func(m *machine) (toggle.Proposal, error) {
			return m.planner().AdoptOff(m.graph())
		}, 9}, // accessibility's seven links, its move, then a link of the next skill
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t)
			m.saveState(domain.PersistedState{Managed: map[string]domain.ManagedSkill{
				"find-skills": {Key: "find-skills", Targets: []string{"claude-code"}}}})
			if tc.setup != nil {
				tc.setup(m)
			}
			pr, err := tc.plan(m)
			must(t, err)
			world0, lock0, state0 := m.world(), readFile(t, m.p.SkillsLockFile()), m.state()

			c := &crash{at: tc.at}
			m.wrap = c.wrap
			if _, err := m.engine().Apply(t.Context(), pr.Plan); !errors.Is(err, errCrash) {
				t.Fatalf("Apply() error = %v, want the crash", err)
			}
			m.wrap = nil
			if !c.dead {
				t.Fatal("the crash point was never reached")
			}
			if reflect.DeepEqual(m.world(), world0) {
				t.Fatal("the crash happened before any change")
			}

			res, err := m.clean().Recover(t.Context())
			if err != nil || !res.Found || !res.RolledBack {
				t.Fatalf("Recover() = %+v, %v; want a rollback", res, err)
			}
			sameWorld(t, "crash then recover", world0, m.world())
			if !bytes.Equal(readFile(t, m.p.SkillsLockFile()), lock0) {
				t.Error("the lock is not identical after recover")
			}
			if got := m.state(); !reflect.DeepEqual(got.Managed, state0.Managed) || !reflect.DeepEqual(got.Parked, state0.Parked) {
				t.Errorf("state after recover: managed %v parked %v", got.Managed, got.Parked)
			}
			if exists(t, engine.RecordFile(m.p)) {
				t.Error("the record is still there")
			}
		})
	}
}
