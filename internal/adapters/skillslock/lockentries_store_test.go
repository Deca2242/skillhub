package skillslock_test

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

// Rules of the real store that the double does not model: the file system,
// the mode, the re-read before writing and malformed input.

// entriesOps are the LockEntries writes, each over lock-v3.json.
var entriesOps = []struct {
	name string
	// prepare runs before the change underneath starts and returns the call.
	prepare func(t *testing.T, f lockFixture) func() error
}{
	{name: "park", prepare: func(*testing.T, lockFixture) func() error {
		return nil
	}},
	{name: "remove", prepare: func(t *testing.T, f lockFixture) func() error {
		raw := entryRaw(t, f.orig, "orca-cli")
		return func() error { _, err := f.store.Remove("orca-cli", raw); return err }
	}},
	{name: "unpark", prepare: func(t *testing.T, f lockFixture) func() error {
		parked, err := f.store.Park("orca-cli")
		if err != nil {
			t.Fatalf("Park: %v", err)
		}
		return func() error { return f.store.Unpark(parked) }
	}},
	{name: "add", prepare: func(t *testing.T, f lockFixture) func() error {
		return func() error { return f.store.Add("new-skill", []byte(newEntry)) }
	}},
}

func opCall(t *testing.T, f lockFixture, i int) func() error {
	t.Helper()
	call := entriesOps[i].prepare(t, f)
	if call == nil {
		call = func() error { _, err := f.store.Park("orca-cli"); return err }
	}
	return call
}

func TestEntriesWritesRetryALockChangedOnce(t *testing.T) {
	for i, op := range entriesOps {
		t.Run(op.name, func(t *testing.T) {
			f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
			call := opCall(t, f, i)
			before := f.content(t)
			npx := npxWrites(f, 2) // the first re-read before writing

			if err := call(); err != nil {
				t.Fatalf("%s after one change: %v", op.name, err)
			}
			got := f.content(t)
			if bytes.Equal(got, before) || bytes.Equal(got, npx()) {
				t.Fatalf("%s did not write over the change", op.name)
			}
			if !bytes.Contains(got, []byte("2026-12-02T00:00:00.000Z")) {
				t.Errorf("%s lost the change npx skills made:\n%s", op.name, got)
			}
		})
	}
}

func TestEntriesWritesRejectALockThatKeepsChanging(t *testing.T) {
	for i, op := range entriesOps {
		t.Run(op.name, func(t *testing.T) {
			f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
			call := opCall(t, f, i)
			writesBefore := f.writes()
			npx := npxWrites(f, 2, 4) // both re-reads before writing

			if err := call(); !errors.Is(err, ports.ErrLockChanged) {
				t.Errorf("error = %v, want ErrLockChanged", err)
			}
			if got := f.content(t); !bytes.Equal(got, npx()) || f.writes() != writesBefore {
				t.Errorf("lock is not what npx skills last wrote:\n%s", got)
			}
		})
	}
}

func TestParkAfterARetryReportsTheLockItLeft(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
	npx := npxWrites(f, 2)

	parked, err := f.store.Park("orca-cli")
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if parked.LockSHA != sha(npx()) {
		t.Errorf("LockSHA = %s, want the hash of what npx skills wrote", parked.LockSHA)
	}
	if err := f.store.Unpark(parked); err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	if got := f.content(t); !bytes.Equal(got, npx()) {
		t.Errorf("Unpark did not give back the lock Park left:\n%s", got)
	}
}

func TestRemoveRefusesAnEntryChangedWhileItWrote(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
	raw := entryRaw(t, f.orig, "agent-browser")
	npx := npxWrites(f, 2) // npx skills rewrites agent-browser itself

	if _, err := f.store.Remove("agent-browser", raw); !errors.Is(err, ports.ErrLockMoved) {
		t.Errorf("error = %v, want ErrLockMoved", err)
	}
	if got := f.content(t); !bytes.Equal(got, npx()) || f.writes() != 0 {
		t.Errorf("a refused Remove wrote:\n%s", got)
	}
}

func TestAddCreatingTheLockRetriesWhenItAppears(t *testing.T) {
	f := newFixture(t, nil)
	v3 := fixtureBytes(t, "lock-v3.json")
	reads := 0
	f.files.Fail = func(c fakes.Call) error {
		if c.Op == "ReadFile" {
			if reads++; reads == 2 { // npx skills creates it before the write
				f.files.AddFile(f.path, string(v3), 0o644)
			}
		}
		return nil
	}

	if err := f.store.Add("new-skill", []byte(newEntry)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := f.content(t)
	if !bytes.HasPrefix(got, v3[:bytes.Index(v3, []byte(`    "agent-browser"`))]) || !bytes.Contains(got, []byte(`"new-skill"`)) {
		t.Errorf("Add did not build on the lock npx skills created:\n%s", got)
	}
}

func TestEntriesWritesGoThroughAnAtomicWriteKeepingTheMode(t *testing.T) {
	f := newFixture(t, nil)
	f.files.AddFile(f.path, string(fixtureBytes(t, "lock-v3.json")), 0o600)

	if _, err := f.store.Park("orca-cli"); err != nil {
		t.Fatalf("Park: %v", err)
	}
	var atomic int
	for _, c := range f.files.Calls() {
		if c.Op == "WriteFileAtomic" {
			atomic++
		}
	}
	if atomic != 1 || f.writes() != 1 {
		t.Errorf("writes = %d, atomic writes = %d; want one atomic write", f.writes(), atomic)
	}
	if mode, err := f.files.Mode(f.path); err != nil || mode.Perm() != 0o600 {
		t.Errorf("mode = %v, %v; want 0600", mode, err)
	}
}

func TestAddCreatesTheLockAtTheXDGStatePath(t *testing.T) {
	home := testsupport.NewHome(t)
	env := home.Env()
	env.XDGStateHome = filepath.Join(home.Root(), "xdg-state")
	p, err := paths.New(env)
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	f := newFixtureAt(t, p, nil)

	if err := f.store.Add("new-skill", []byte(newEntry)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := f.content(t); !bytes.Contains(got, []byte(`"new-skill"`)) {
		t.Errorf("lock at the XDG path:\n%s", got)
	}
	if mode, err := f.files.Mode(filepath.Join(env.XDGStateHome, "skills", ".skill-lock.json")); err != nil || mode.Perm() != 0o644 {
		t.Errorf("mode = %v, %v; want 0644", mode, err)
	}
	defaultLock := filepath.Join(home.Root(), ".agents", ".skill-lock.json")
	if _, err := f.files.ReadFile(defaultLock); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the default lock was written: %v", err)
	}
}

func TestAFailedEntriesWriteLeavesTheLockUnchanged(t *testing.T) {
	errDisk := errors.New("disk full")
	for i, op := range entriesOps {
		t.Run(op.name, func(t *testing.T) {
			f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
			call := opCall(t, f, i)
			before := f.content(t)
			f.files.Fail = func(c fakes.Call) error {
				if c.Op == "WriteFileAtomic" {
					return errDisk
				}
				return nil
			}

			if err := call(); !errors.Is(err, errDisk) {
				t.Errorf("error = %v, want the write error", err)
			}
			if got := f.content(t); !bytes.Equal(got, before) {
				t.Errorf("lock changed:\n%s", got)
			}
		})
	}
}

func TestEntriesRefuseMalformedInput(t *testing.T) {
	v3 := fixtureBytes(t, "lock-v3.json")
	good := domain.ParkedEntry{Name: "new-skill", Raw: []byte(newEntry), Before: "agent-browser"}
	tests := []struct {
		name string
		lock []byte
		call func(s *skillslock.Store) error
	}{
		{"park from a lock that is not JSON", []byte("{"), func(s *skillslock.Store) error { _, err := s.Park("orca-cli"); return err }},
		{"add to a lock that is an array", []byte("[]"), func(s *skillslock.Store) error { return s.Add("new-skill", []byte(newEntry)) }},
		{"add to a lock with no skills", []byte(`{"version": 3}`), func(s *skillslock.Store) error { return s.Add("new-skill", []byte(newEntry)) }},
		{"add an entry whose source is a number", v3, func(s *skillslock.Store) error {
			return s.Add("new-skill", []byte(strings.Replace(newEntry, `"owner/repo"`, `7`, 1)))
		}},
		{"unpark an entry that is not an object", v3, func(s *skillslock.Store) error {
			bad := good
			bad.Raw = []byte(`"text"`)
			return s.Unpark(bad)
		}},
		{"unpark an entry with no name", v3, func(s *skillslock.Store) error {
			bad := good
			bad.Name = ""
			return s.Unpark(bad)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.lock)
			if err := tt.call(f.store); !errors.Is(err, skillslock.ErrMalformedLock) {
				t.Errorf("error = %v, want ErrMalformedLock", err)
			}
			if f.writes() != 0 {
				t.Error("a refused call wrote")
			}
		})
	}
}
