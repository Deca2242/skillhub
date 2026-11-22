package skillslock_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// lockFixture is a store over an in-memory file system holding one lock.
type lockFixture struct {
	store *skillslock.Store
	files *fakes.FileOps
	path  string
	orig  []byte
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// newFixture puts data at the lock path of a fake home; nil data leaves the
// lock missing.
func newFixture(t *testing.T, data []byte) lockFixture {
	t.Helper()
	p := testsupport.NewHome(t).Paths()
	return newFixtureAt(t, p, data)
}

func newFixtureAt(t *testing.T, p paths.Paths, data []byte) lockFixture {
	t.Helper()
	files := fakes.NewFileOps(p.Guard())
	if data != nil {
		files.AddFile(p.SkillsLockFile(), string(data), 0o644)
	}
	return lockFixture{
		store: skillslock.New(p, files),
		files: files,
		path:  p.SkillsLockFile(),
		orig:  data,
	}
}

// content returns the lock's bytes now, with no injected failure.
func (f lockFixture) content(t *testing.T) []byte {
	t.Helper()
	f.files.Fail = nil
	data, err := f.files.ReadFile(f.path)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	return data
}

func (f lockFixture) writes() int {
	n := 0
	for _, c := range f.files.Calls() {
		if c.Op == "WriteFileAtomic" || c.Op == "Rename" || c.Op == "RemoveTree" || c.Op == "Chmod" {
			n++
		}
	}
	return n
}

// replaceOnce replaces exactly one occurrence of old, failing the test when
// old is missing or ambiguous, so an expected file is built safely.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("%q occurs %d times, want 1", old, n)
	}
	return strings.Replace(s, old, new, 1)
}

func entryByName(t *testing.T, lock domain.SkillsLock, name string) domain.LockEntry {
	t.Helper()
	for _, e := range lock.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("entry %q not in lock", name)
	return domain.LockEntry{}
}

// ---------------------------------------------------------------------------
// Read

func TestReadDecodesEntriesInFileOrder(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v3.json"))

	lock, err := f.store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if lock.Version != 3 {
		t.Errorf("Version = %d, want 3", lock.Version)
	}
	var names []string
	for _, e := range lock.Entries {
		names = append(names, e.Name)
	}
	want := []string{"find-skills", "frontend-design", "orca-cli", "agent-browser"}
	if !slices.Equal(names, want) {
		t.Errorf("entry names = %v, want %v", names, want)
	}
	got := entryByName(t, lock, "frontend-design")
	wantEntry := domain.LockEntry{
		Name:            "frontend-design",
		Source:          "anthropics/skills",
		SourceType:      "github",
		SourceURL:       "https://github.com/anthropics/skills.git",
		SkillPath:       "skills/frontend-design/SKILL.md",
		SkillFolderHash: "2222222222222222222222222222222222222222",
		InstalledAt:     "2026-10-03T09:15:30.123Z",
		UpdatedAt:       "2026-10-04T09:15:30.123Z",
	}
	got.Raw = nil
	if !reflect.DeepEqual(got, wantEntry) {
		t.Errorf("entry = %+v, want %+v", got, wantEntry)
	}
	if lock.Revision == "" {
		t.Error("Revision is empty")
	}
	if f.writes() != 0 {
		t.Errorf("Read wrote: %v", f.files.Calls())
	}
}

func TestReadKeepsRefCommitAndRawEntry(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-variants.json"))

	lock, err := f.store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	tests := []struct {
		name, ref, commit string
	}{
		{name: "plain"},
		{name: "pinned", ref: "v1.2.0"},
		{name: "committed", commit: "c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0"},
		{
			name:   "pinned-committed",
			ref:    "d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0",
			commit: "d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := entryByName(t, lock, tt.name)
			if e.Ref != tt.ref || e.Commit != tt.commit {
				t.Errorf("ref, commit = %q, %q; want %q, %q", e.Ref, e.Commit, tt.ref, tt.commit)
			}
		})
	}

	raw := entryByName(t, lock, "renamed").Raw
	v, err := orderedjson.ParseValue(raw)
	if err != nil {
		t.Fatalf("Raw is not JSON: %v\n%s", err, raw)
	}
	wantKeys := []string{
		"source", "sourceType", "sourceUrl", "skillPath", "skillFolderHash",
		"skillName", "pluginName", "extra", "installedAt", "updatedAt",
	}
	if !slices.Equal(v.Keys(), wantKeys) {
		t.Errorf("Raw keys = %v, want %v", v.Keys(), wantKeys)
	}
	if !bytes.Contains(raw, []byte(`"pluginName": "tools <&> é"`)) || !bytes.Contains(raw, []byte("2.50")) {
		t.Errorf("Raw lost exact scalars:\n%s", raw)
	}
}

func TestReadMissingLockIsEmptyVersion3(t *testing.T) {
	f := newFixture(t, nil)

	lock, err := f.store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if lock.Version != 3 || len(lock.Entries) != 0 {
		t.Errorf("lock = %+v, want version 3 with no entries", lock)
	}
	if ok, _ := f.files.Exists(f.path); ok {
		t.Error("Read created the lock file")
	}
}

func TestReadReportsAnotherVersionAsFound(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v2.json"))

	lock, err := f.store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if lock.Version != 2 || len(lock.Entries) != 1 || lock.Entries[0].Name != "old-skill" {
		t.Errorf("lock = %+v, want version 2 with old-skill", lock)
	}
}

func TestReadFollowsXDGStateHome(t *testing.T) {
	home := testsupport.NewHome(t)
	env := home.Env()
	env.XDGStateHome = filepath.Join(home.Root(), "xdg-state")
	p, err := paths.New(env)
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	wantPath := filepath.Join(env.XDGStateHome, "skills", ".skill-lock.json")
	if p.SkillsLockFile() != wantPath {
		t.Fatalf("SkillsLockFile = %s, want %s", p.SkillsLockFile(), wantPath)
	}
	f := newFixtureAt(t, p, fixtureBytes(t, "lock-v3.json"))
	defaultLock := filepath.Join(home.Root(), ".agents", ".skill-lock.json")
	other := fixtureBytes(t, "lock-v2.json")
	f.files.AddFile(defaultLock, string(other), 0o644)

	lock, err := f.store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if lock.Version != 3 || len(lock.Entries) != 4 {
		t.Fatalf("Read the wrong lock: %+v", lock)
	}
	err = f.store.UpdateEntry("orca-cli", ports.LockEntryUpdate{
		FolderHash: "5555555555555555555555555555555555555555",
		UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("UpdateEntry: %v", err)
	}
	if !bytes.Contains(f.content(t), []byte("5555555555555555555555555555555555555555")) {
		t.Error("the XDG lock was not updated")
	}
	if got, _ := f.files.ReadFile(defaultLock); !bytes.Equal(got, other) {
		t.Errorf("the default lock changed:\n%s", got)
	}
}

func TestReadRejectsAMalformedLock(t *testing.T) {
	tests := []struct {
		name, data string
	}{
		{name: "invalid JSON", data: `{"version": 3, "skills": {`},
		{name: "top level array", data: `[3]`},
		{name: "skills not an object", data: `{"version": 3, "skills": []}`},
		{name: "entry not an object", data: `{"version": 3, "skills": {"a": "x"}}`},
		{name: "known field not a string", data: `{"version": 3, "skills": {"a": {"skillFolderHash": 5}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, []byte(tt.data))
			if _, err := f.store.Read(); !errors.Is(err, skillslock.ErrMalformedLock) {
				t.Errorf("Read error = %v, want ErrMalformedLock", err)
			}
			err := f.store.UpdateEntry("a", ports.LockEntryUpdate{
				FolderHash: "1", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			})
			if err == nil {
				t.Error("UpdateEntry on a malformed lock succeeded")
			}
			if got := f.content(t); !bytes.Equal(got, f.orig) || f.writes() != 0 {
				t.Errorf("malformed lock was written:\n%s", got)
			}
		})
	}
}

func TestRevisionIdentifiesTheBytesRead(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v3.json"))

	first, err := f.store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	again, _ := f.store.Read()
	if first.Revision != again.Revision {
		t.Errorf("Revision changed without a change: %q, %q", first.Revision, again.Revision)
	}
	err = f.store.UpdateEntry("orca-cli", ports.LockEntryUpdate{
		FolderHash: "5555555555555555555555555555555555555555",
		UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("UpdateEntry: %v", err)
	}
	after, _ := f.store.Read()
	if after.Revision == first.Revision {
		t.Errorf("Revision %q did not change after a write", after.Revision)
	}
}

// ---------------------------------------------------------------------------
// Round trip and restore

func TestRestoringEveryEntryAsReadIsByteIdentical(t *testing.T) {
	for _, name := range []string{"lock-v3.json", "lock-variants.json"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureBytes(t, name))
			lock, err := f.store.Read()
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			for _, e := range lock.Entries {
				if err := f.store.RestoreEntry(e.Name, e.Raw); err != nil {
					t.Fatalf("RestoreEntry(%s): %v", e.Name, err)
				}
			}
			if got := f.content(t); !bytes.Equal(got, f.orig) {
				t.Errorf("round trip changed the lock:\n got: %s\nwant: %s", got, f.orig)
			}
		})
	}
}

func TestRestoreAfterUpdateIsByteIdentical(t *testing.T) {
	data := fixtureBytes(t, "lock-variants.json")
	for _, name := range []string{"plain", "pinned", "committed", "pinned-committed", "renamed"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, data)
			lock, err := f.store.Read()
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			snapshot := entryByName(t, lock, name).Raw
			err = f.store.UpdateEntry(name, ports.LockEntryUpdate{
				FolderHash: "9999999999999999999999999999999999999999",
				UpdatedAt:  time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
				Commit:     "8888888888888888888888888888888888888888",
			})
			if err != nil {
				t.Fatalf("UpdateEntry: %v", err)
			}
			if bytes.Equal(f.content(t), f.orig) {
				t.Fatal("UpdateEntry changed nothing")
			}
			if err := f.store.RestoreEntry(name, snapshot); err != nil {
				t.Fatalf("RestoreEntry: %v", err)
			}
			if got := f.content(t); !bytes.Equal(got, f.orig) {
				t.Errorf("restore is not byte-identical:\n got: %s\nwant: %s", got, f.orig)
			}
		})
	}
}

func TestRestoreRefusesToChangeRef(t *testing.T) {
	data := fixtureBytes(t, "lock-variants.json")
	tests := []struct {
		name, entry, old, new string
	}{
		{name: "changes a ref", entry: "pinned", old: `"ref": "v1.2.0"`, new: `"ref": "v2.0.0"`},
		{name: "removes a ref", entry: "pinned", old: `"ref": "v1.2.0",`, new: ``},
		{name: "adds a ref", entry: "plain", old: `"skillPath"`, new: `"ref": "main", "skillPath"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, data)
			lock, err := f.store.Read()
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			raw := replaceOnce(t, string(entryByName(t, lock, tt.entry).Raw), tt.old, tt.new)
			err = f.store.RestoreEntry(tt.entry, []byte(raw))
			if !errors.Is(err, skillslock.ErrRefMismatch) {
				t.Errorf("RestoreEntry error = %v, want ErrRefMismatch", err)
			}
			if got := f.content(t); !bytes.Equal(got, f.orig) || f.writes() != 0 {
				t.Errorf("lock changed:\n%s", got)
			}
		})
	}
}

func TestRestoreRejectsAnEntryThatIsNotAnObject(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
	for _, raw := range []string{`"text"`, `[1]`, `{`, ``} {
		if err := f.store.RestoreEntry("orca-cli", []byte(raw)); err == nil {
			t.Errorf("RestoreEntry(%q) succeeded", raw)
		}
	}
	if got := f.content(t); !bytes.Equal(got, f.orig) || f.writes() != 0 {
		t.Errorf("lock changed:\n%s", got)
	}
}

func TestRestoreMissingEntryIsNotFound(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
	err := f.store.RestoreEntry("nope", []byte(`{"source": "a/b"}`))
	if !errors.Is(err, ports.ErrLockEntryNotFound) {
		t.Errorf("error = %v, want ErrLockEntryNotFound", err)
	}
	if got := f.content(t); !bytes.Equal(got, f.orig) || f.writes() != 0 {
		t.Errorf("lock changed:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// UpdateEntry

func TestUpdateEntryRewritesOnlyHashUpdatedAtAndAnExistingCommit(t *testing.T) {
	// 10:20:30.456 at UTC+1 is written as UTC, the way npx skills writes it.
	at := time.Date(2026, 11, 21, 10, 20, 30, 456_000_000, time.FixedZone("CET", 3600))
	const newAt = `"updatedAt": "2026-11-21T09:20:30.456Z"`
	const newHash = "9999999999999999999999999999999999999999"
	const newCommit = "8888888888888888888888888888888888888888"

	tests := []struct {
		fixture, entry       string
		oldHash, oldAt       string
		oldCommit            string // empty: the entry has no commit key
		wantRef, wantSkillNm string
	}{
		{
			fixture: "lock-v3.json", entry: "frontend-design",
			oldHash: "2222222222222222222222222222222222222222",
			oldAt:   `"updatedAt": "2026-10-04T09:15:30.123Z"`,
		},
		{
			fixture: "lock-variants.json", entry: "plain",
			oldHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			oldAt:   `"updatedAt": "2026-09-02T00:00:00.000Z"`,
		},
		{
			fixture: "lock-variants.json", entry: "pinned",
			oldHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			oldAt:   `"updatedAt": "2026-09-04T00:00:00.000Z"`,
			wantRef: "v1.2.0",
		},
		{
			fixture: "lock-variants.json", entry: "committed",
			oldHash:   "cccccccccccccccccccccccccccccccccccccccc",
			oldAt:     `"updatedAt": "2026-09-06T00:00:00.000Z"`,
			oldCommit: "c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0",
		},
		{
			// ref and commit hold the same SHA: only commit may move.
			fixture: "lock-variants.json", entry: "pinned-committed",
			oldHash:   "dddddddddddddddddddddddddddddddddddddddd",
			oldAt:     `"updatedAt": "2026-09-08T00:00:00.000Z"`,
			oldCommit: "d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0",
			wantRef:   "d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0",
		},
		{
			fixture: "lock-variants.json", entry: "renamed",
			oldHash:     "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			oldAt:       `"updatedAt": "2026-09-10T00:00:00.000Z"`,
			wantSkillNm: "original",
		},
	}
	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			f := newFixture(t, fixtureBytes(t, tt.fixture))
			before, err := f.store.Read()
			if err != nil {
				t.Fatalf("Read: %v", err)
			}

			err = f.store.UpdateEntry(tt.entry, ports.LockEntryUpdate{
				FolderHash: newHash, UpdatedAt: at, Commit: newCommit,
			})
			if err != nil {
				t.Fatalf("UpdateEntry: %v", err)
			}

			want := replaceOnce(t, string(f.orig), `"skillFolderHash": "`+tt.oldHash+`"`, `"skillFolderHash": "`+newHash+`"`)
			want = replaceOnce(t, want, tt.oldAt, newAt)
			wantCommit := ""
			if tt.oldCommit != "" {
				want = replaceOnce(t, want, `"commit": "`+tt.oldCommit+`"`, `"commit": "`+newCommit+`"`)
				wantCommit = newCommit
			}
			if got := f.content(t); string(got) != want {
				t.Errorf("lock after update:\n got: %s\nwant: %s", got, want)
			}

			after, err := f.store.Read()
			if err != nil {
				t.Fatalf("Read after update: %v", err)
			}
			old, e := entryByName(t, before, tt.entry), entryByName(t, after, tt.entry)
			if e.SkillFolderHash != newHash || e.UpdatedAt != "2026-11-21T09:20:30.456Z" || e.Commit != wantCommit {
				t.Errorf("entry = %+v", e)
			}
			if e.Ref != tt.wantRef || e.InstalledAt != old.InstalledAt {
				t.Errorf("ref, installedAt = %q, %q; want %q, %q", e.Ref, e.InstalledAt, tt.wantRef, old.InstalledAt)
			}
			if tt.wantSkillNm != "" && !bytes.Contains(e.Raw, []byte(`"skillName": "`+tt.wantSkillNm+`"`)) {
				t.Errorf("skillName lost:\n%s", e.Raw)
			}
		})
	}
}

func TestUpdateEntryWritesAtomicallyThroughFileOpsKeepingTheMode(t *testing.T) {
	f := newFixture(t, nil)
	f.files.AddFile(f.path, string(fixtureBytes(t, "lock-v3.json")), 0o600)

	err := f.store.UpdateEntry("orca-cli", ports.LockEntryUpdate{
		FolderHash: "5555555555555555555555555555555555555555",
		UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("UpdateEntry: %v", err)
	}
	var writes []fakes.Call
	for _, c := range f.files.Calls() {
		if c.Op == "WriteFileAtomic" || c.Op == "Rename" {
			writes = append(writes, c)
		}
	}
	if len(writes) != 1 || writes[0].Op != "WriteFileAtomic" || writes[0].Args[0] != f.path {
		t.Errorf("writes = %v, want one WriteFileAtomic of %s", writes, f.path)
	}
	if mode, err := f.files.Mode(f.path); err != nil || mode.Perm() != 0o600 {
		t.Errorf("mode = %v, %v; want 0600", mode, err)
	}
}

func TestWritesRefuseAVersionOtherThan3(t *testing.T) {
	v3 := string(fixtureBytes(t, "lock-v3.json"))
	tests := []struct {
		name, data string
	}{
		{name: "version 2", data: string(fixtureBytes(t, "lock-v2.json"))},
		{name: "version 4", data: replaceOnce(t, v3, `"version": 3,`, `"version": 4,`)},
		{name: "version 3.0", data: replaceOnce(t, v3, `"version": 3,`, `"version": 3.0,`)},
		{name: "version as a string", data: replaceOnce(t, v3, `"version": 3,`, `"version": "3",`)},
		{name: "no version", data: replaceOnce(t, v3, `"version": 3,`, ``)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, []byte(tt.data))
			lock, err := f.store.Read()
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(lock.Entries) == 0 {
				t.Fatal("fixture has no entries")
			}
			e := lock.Entries[0]

			err = f.store.UpdateEntry(e.Name, ports.LockEntryUpdate{
				FolderHash: "5555555555555555555555555555555555555555",
				UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
			})
			if !errors.Is(err, ports.ErrUnsupportedLockVersion) {
				t.Errorf("UpdateEntry error = %v, want ErrUnsupportedLockVersion", err)
			}
			if err := f.store.RestoreEntry(e.Name, e.Raw); !errors.Is(err, ports.ErrUnsupportedLockVersion) {
				t.Errorf("RestoreEntry error = %v, want ErrUnsupportedLockVersion", err)
			}
			if got := f.content(t); !bytes.Equal(got, f.orig) || f.writes() != 0 {
				t.Errorf("lock changed:\n%s", got)
			}
		})
	}
}

func TestUpdateEntryMissingEntryIsNotFound(t *testing.T) {
	update := ports.LockEntryUpdate{
		FolderHash: "5555555555555555555555555555555555555555",
		UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	}
	t.Run("absent entry", func(t *testing.T) {
		f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
		if err := f.store.UpdateEntry("nope", update); !errors.Is(err, ports.ErrLockEntryNotFound) {
			t.Errorf("error = %v, want ErrLockEntryNotFound", err)
		}
		if got := f.content(t); !bytes.Equal(got, f.orig) || f.writes() != 0 {
			t.Errorf("lock changed:\n%s", got)
		}
	})
	t.Run("missing lock", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.store.UpdateEntry("nope", update); !errors.Is(err, ports.ErrLockEntryNotFound) {
			t.Errorf("error = %v, want ErrLockEntryNotFound", err)
		}
		if ok, _ := f.files.Exists(f.path); ok || f.writes() != 0 {
			t.Error("UpdateEntry created the lock")
		}
	})
}

func TestUpdateEntryRejectsAnIncompleteUpdate(t *testing.T) {
	at := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name, entry string
		update      ports.LockEntryUpdate
	}{
		{name: "no folder hash", entry: "plain", update: ports.LockEntryUpdate{UpdatedAt: at}},
		{name: "no time", entry: "plain", update: ports.LockEntryUpdate{FolderHash: "1"}},
		{
			name: "no commit for an entry that has one", entry: "committed",
			update: ports.LockEntryUpdate{FolderHash: "1", UpdatedAt: at},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, fixtureBytes(t, "lock-variants.json"))
			if err := f.store.UpdateEntry(tt.entry, tt.update); !errors.Is(err, skillslock.ErrInvalidUpdate) {
				t.Errorf("error = %v, want ErrInvalidUpdate", err)
			}
			if got := f.content(t); !bytes.Equal(got, f.orig) || f.writes() != 0 {
				t.Errorf("lock changed:\n%s", got)
			}
		})
	}
}

func TestAFailedWriteLeavesTheLockUnchanged(t *testing.T) {
	f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
	errDisk := errors.New("disk full")
	f.files.Fail = func(c fakes.Call) error {
		if c.Op == "WriteFileAtomic" {
			return errDisk
		}
		return nil
	}

	err := f.store.UpdateEntry("orca-cli", ports.LockEntryUpdate{
		FolderHash: "5555555555555555555555555555555555555555",
		UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, errDisk) {
		t.Errorf("error = %v, want the write error", err)
	}
	if got := f.content(t); !bytes.Equal(got, f.orig) {
		t.Errorf("lock changed:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// A lock changed underneath, as npx skills does without any lock

// agentBrowserUpdatedAt is agent-browser's updatedAt in lock-v3.json, the
// value npxWrites moves.
const agentBrowserUpdatedAt = "2026-10-08T11:45:00.000Z"

// npxWrites makes the lock change while the store is between its first read
// and its write, as npx skills does without any lock: on the given reads
// (counting from 1 after the call) npx skills rewrites the current lock with
// a new updatedAt for agent-browser. It returns what npx skills last wrote.
func npxWrites(f lockFixture, onReads ...int) func() []byte {
	reads := 0
	stamp := agentBrowserUpdatedAt
	var last []byte
	f.files.Fail = func(c fakes.Call) error {
		if c.Op != "ReadFile" {
			return nil
		}
		reads++
		if !slices.Contains(onReads, reads) {
			return nil
		}
		// Tree describes a file as "file <perm> <content>".
		content := strings.SplitN(f.files.Tree(f.path)[f.path], " ", 3)[2]
		next := fmt.Sprintf("2026-12-%02dT00:00:00.000Z", reads)
		last = []byte(strings.Replace(content, stamp, next, 1))
		stamp = next
		f.files.AddFile(f.path, string(last), 0o644)
		return nil
	}
	return func() []byte { return last }
}

func TestALockChangedOnceIsRetried(t *testing.T) {
	update := ports.LockEntryUpdate{
		FolderHash: "5555555555555555555555555555555555555555",
		UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	}

	t.Run("update", func(t *testing.T) {
		f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
		npx := npxWrites(f, 2) // the first re-read before writing

		if err := f.store.UpdateEntry("orca-cli", update); err != nil {
			t.Fatalf("UpdateEntry after one change: %v", err)
		}
		want := replaceOnce(t, string(npx()), "3333333333333333333333333333333333333333", update.FolderHash)
		want = replaceOnce(t, want, "2026-10-06T10:30:00.000Z", "2026-11-21T00:00:00.000Z")
		if got := f.content(t); string(got) != want {
			t.Errorf("lock lost a change:\n got: %s\nwant: %s", got, want)
		}
	})

	t.Run("restore", func(t *testing.T) {
		f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
		lock, err := f.store.Read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		snapshot := entryByName(t, lock, "orca-cli").Raw
		if err := f.store.UpdateEntry("orca-cli", update); err != nil {
			t.Fatalf("UpdateEntry: %v", err)
		}
		npx := npxWrites(f, 2)

		if err := f.store.RestoreEntry("orca-cli", snapshot); err != nil {
			t.Fatalf("RestoreEntry after one change: %v", err)
		}
		want := replaceOnce(t, string(f.orig), agentBrowserUpdatedAt, "2026-12-02T00:00:00.000Z")
		if got := f.content(t); string(got) != want || !bytes.Contains(npx(), []byte(update.FolderHash)) {
			t.Errorf("lock lost a change:\n got: %s\nwant: %s", got, want)
		}
	})
}

func TestALockThatKeepsChangingIsRejected(t *testing.T) {
	update := ports.LockEntryUpdate{
		FolderHash: "5555555555555555555555555555555555555555",
		UpdatedAt:  time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name  string
		write func(f lockFixture, raw []byte) error
	}{
		{name: "update", write: func(f lockFixture, _ []byte) error { return f.store.UpdateEntry("orca-cli", update) }},
		{name: "restore", write: func(f lockFixture, raw []byte) error { return f.store.RestoreEntry("orca-cli", raw) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, fixtureBytes(t, "lock-v3.json"))
			lock, err := f.store.Read()
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			raw := entryByName(t, lock, "orca-cli").Raw
			npx := npxWrites(f, 2, 4) // both re-reads before writing

			if err := tt.write(f, raw); !errors.Is(err, ports.ErrLockChanged) {
				t.Errorf("error = %v, want ErrLockChanged", err)
			}
			if got := f.content(t); !bytes.Equal(got, npx()) || f.writes() != 0 {
				t.Errorf("lock is not what npx skills last wrote:\n%s", got)
			}
		})
	}
}
