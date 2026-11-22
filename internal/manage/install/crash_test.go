package install_test

import (
	"context"
	"errors"
	iofs "io/fs"

	"skillhub/internal/manage/engine"
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
