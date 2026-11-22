package statefile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
)

// The fixtures were written by the version 1 adapter of the base (feat/v2 at
// ea62d69): state-v1.json is a state with candidates, skips, check moments,
// notified marks and results; state-v1-operation.json is the same state with
// an apply in progress.
const (
	fixtureV1          = "testdata/state-v1.json"
	fixtureV1Operation = "testdata/state-v1-operation.json"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// seedState writes data as state.json and a journal, and returns the store.
func seedState(t *testing.T, data []byte) (*Store, *testsupport.Home) {
	t.Helper()
	h := testsupport.NewHome(t)
	p := h.Paths()
	if err := os.MkdirAll(filepath.Dir(p.StateFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.StateFile(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.JournalFile(), []byte(seededJournal), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(p), h
}

const seededJournal = `{"at":"2026-11-19T10:00:00Z","action":"applied","target":"lock:grill-me","from_sha":"3333","to_sha":"4444","snapshot_id":"s1","labels":["text_only"],"detail":""}
`

func readFile(t *testing.T, path string) ([]byte, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return data, true
}

func fileVersion(t *testing.T, data []byte) any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode state.json: %v", err)
	}
	return doc["version"]
}

// v1State is what the version 1 reader of the base decodes from data, as a
// PersistedState.
func v1State(t *testing.T, data []byte) (domain.PersistedState, *domain.Operation) {
	t.Helper()
	sf, err := decodeV1StateFile(data)
	if err != nil {
		t.Fatalf("version 1 reader: %v", err)
	}
	return domain.PersistedState{
		Candidates: sf.Candidates, Skips: sf.Skips, Checks: sf.Checks, Notified: sf.Notified, Results: sf.Results,
	}, sf.Operation
}

func backupPath(s *Store) string {
	return filepath.Join(filepath.Dir(s.paths.StateFile()), "state.v1.json")
}

func TestVersion1StateRoundTripsThroughTheMigration(t *testing.T) {
	original := readFixture(t, fixtureV1)
	want, _ := v1State(t, original)
	s, h := seedState(t, original)

	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load of a version 1 state: %v", err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("Load() = %+v\nwant %+v", loaded, want)
	}
	if _, ok := readFile(t, backupPath(s)); ok {
		t.Fatal("a read made the state.v1.json copy")
	}
	if after, _ := readFile(t, h.Paths().StateFile()); string(after) != string(original) {
		t.Fatal("a read rewrote state.json")
	}

	if err := s.Save(loaded); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	migrated, _ := readFile(t, h.Paths().StateFile())
	if v := fileVersion(t, migrated); v != float64(2) {
		t.Fatalf("state.json version after the first write = %v, want 2", v)
	}
	backup, ok := readFile(t, backupPath(s))
	if !ok {
		t.Fatal("the first write left no state.v1.json")
	}
	if string(backup) != string(original) {
		t.Fatalf("state.v1.json is not the original byte for byte:\n%s", backup)
	}
	reloaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load after the migration: %v", err)
	}
	if !reflect.DeepEqual(reloaded, want) {
		t.Fatalf("after the migration Load() = %+v\nwant %+v", reloaded, want)
	}
	if _, ok, err := s.Operation(); err != nil || ok {
		t.Fatalf("Operation() after the migration = (_, %v, %v), want none", ok, err)
	}
	if j, _ := readFile(t, h.Paths().JournalFile()); string(j) != seededJournal {
		t.Fatalf("the migration changed journal.jsonl:\n%s", j)
	}
	if entries, err := s.Journal(); err != nil || len(entries) != 1 || entries[0].Target != "lock:grill-me" {
		t.Fatalf("Journal() after the migration = %+v, %v", entries, err)
	}
}

func TestEveryVersion1WriteMigrates(t *testing.T) {
	op := domain.Operation{ID: "op-2", Kind: domain.OperationApply, Target: "lock:find-skills", RestoreSnapshotID: "snap", StartedAt: time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)}
	writes := map[string]func(s *Store) error{
		"Save":            func(s *Store) error { st, _ := s.Load(); return s.Save(st) },
		"RecordOperation": func(s *Store) error { return s.RecordOperation(op) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			original := readFixture(t, fixtureV1)
			want, _ := v1State(t, original)
			s, h := seedState(t, original)
			if err := write(s); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			data, _ := readFile(t, h.Paths().StateFile())
			if v := fileVersion(t, data); v != float64(2) {
				t.Fatalf("version after %s = %v, want 2", name, v)
			}
			if backup, _ := readFile(t, backupPath(s)); string(backup) != string(original) {
				t.Fatalf("state.v1.json after %s is not the original", name)
			}
			if got, _ := s.Load(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Load() after %s = %+v\nwant %+v", name, got, want)
			}
		})
	}
}

func TestTheVersion1CopyIsNeverOverwritten(t *testing.T) {
	t.Run("an existing copy is kept", func(t *testing.T) {
		s, h := seedState(t, readFixture(t, fixtureV1))
		earlier := []byte("{\"version\": 1, \"candidates\": [], \"skips\": []}\n")
		if err := os.WriteFile(backupPath(s), earlier, 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Save(st); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got, _ := readFile(t, backupPath(s)); string(got) != string(earlier) {
			t.Fatalf("state.v1.json was overwritten:\n%s", got)
		}
		data, _ := readFile(t, h.Paths().StateFile())
		if v := fileVersion(t, data); v != float64(2) {
			t.Fatalf("version = %v, want 2", v)
		}
	})

	t.Run("later writes leave the copy alone", func(t *testing.T) {
		original := readFixture(t, fixtureV1)
		s, _ := seedState(t, original)
		st, _ := s.Load()
		if err := s.Save(st); err != nil {
			t.Fatal(err)
		}
		st.Skips = nil
		st.Managed = map[string]domain.ManagedSkill{"demo": {Key: "demo", Targets: []string{"claude-code"}}}
		if err := s.Save(st); err != nil {
			t.Fatalf("second Save: %v", err)
		}
		if err := s.ClearOperation(); err != nil {
			t.Fatal(err)
		}
		if got, _ := readFile(t, backupPath(s)); string(got) != string(original) {
			t.Fatalf("a later write changed state.v1.json:\n%s", got)
		}
	})

	t.Run("a fresh state makes no copy", func(t *testing.T) {
		h := testsupport.NewHome(t)
		s := New(h.Paths())
		if err := s.Save(domain.PersistedState{}); err != nil {
			t.Fatal(err)
		}
		if _, ok := readFile(t, backupPath(s)); ok {
			t.Fatal("a fresh state made a state.v1.json")
		}
		data, _ := readFile(t, h.Paths().StateFile())
		if v := fileVersion(t, data); v != float64(2) {
			t.Fatalf("version = %v, want 2", v)
		}
	})
}

func TestMigrationWaitsForTheVersion1Operation(t *testing.T) {
	original := readFixture(t, fixtureV1Operation)
	want, wantOp := v1State(t, original)
	s, h := seedState(t, original)

	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	st.Candidates[0].State = domain.CandidatePending
	st.Notified.OperationID = ""
	want.Notified.OperationID = ""
	if err := s.Save(st); err != nil {
		t.Fatalf("Save with a version 1 operation: %v", err)
	}
	data, _ := readFile(t, h.Paths().StateFile())
	if v := fileVersion(t, data); v != float64(1) {
		t.Fatalf("version with a version 1 operation = %v, want 1", v)
	}
	if _, ok := readFile(t, backupPath(s)); ok {
		t.Fatal("a blocked write made state.v1.json")
	}
	got, gotOp := v1State(t, data) // a version 1 binary can still read it, and recover
	if !reflect.DeepEqual(got, want) || gotOp == nil || !reflect.DeepEqual(*gotOp, *wantOp) {
		t.Fatalf("version 1 reader got %+v, %+v", got, gotOp)
	}

	// Nothing of version 2 can be written while the migration waits.
	before := data
	v2 := st
	v2.Parked = map[string]domain.ParkedSkill{"demo": {Key: "demo", Folder: "/x"}}
	if err := s.Save(v2); !errors.Is(err, ErrMigrationBlocked) {
		t.Fatalf("Save of version 2 fields while blocked = %v, want ErrMigrationBlocked", err)
	}
	if data, _ := readFile(t, h.Paths().StateFile()); string(data) != string(before) {
		t.Fatal("a refused write changed state.json")
	}

	// Recover's own writes stay version 1; the record clears in version 1.
	if err := s.ClearOperation(); err != nil {
		t.Fatalf("ClearOperation: %v", err)
	}
	cleared, _ := readFile(t, h.Paths().StateFile())
	if v := fileVersion(t, cleared); v != float64(1) {
		t.Fatalf("version after clearing the operation = %v, want 1", v)
	}
	if _, op := v1State(t, cleared); op != nil {
		t.Fatal("ClearOperation left the operation")
	}
	if _, ok := readFile(t, backupPath(s)); ok {
		t.Fatal("clearing the operation made state.v1.json")
	}

	// Once recovered, the next write migrates and keeps that state.
	if err := s.Save(v2); err != nil {
		t.Fatalf("Save after the operation cleared: %v", err)
	}
	migrated, _ := readFile(t, h.Paths().StateFile())
	if v := fileVersion(t, migrated); v != float64(2) {
		t.Fatalf("version after recovering = %v, want 2", v)
	}
	if backup, _ := readFile(t, backupPath(s)); string(backup) != string(cleared) {
		t.Fatal("state.v1.json is not the version 1 state it replaced")
	}
	if got, _ := s.Load(); !reflect.DeepEqual(got, v2) {
		t.Fatalf("Load() after the migration = %+v\nwant %+v", got, v2)
	}
	if j, _ := readFile(t, h.Paths().JournalFile()); string(j) != seededJournal {
		t.Fatal("the journal changed")
	}
}

func TestRecordingAnotherOperationWhileBlockedStaysVersion1(t *testing.T) {
	s, h := seedState(t, readFixture(t, fixtureV1Operation))
	op := domain.Operation{ID: "op-3", Kind: domain.OperationUndo, Target: "lock:grill-me", RestoreSnapshotID: "s", StartedAt: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	if err := s.RecordOperation(op); err != nil {
		t.Fatal(err)
	}
	data, _ := readFile(t, h.Paths().StateFile())
	if v := fileVersion(t, data); v != float64(1) {
		t.Fatalf("version = %v, want 1", v)
	}
	if _, got := v1State(t, data); got == nil || got.ID != "op-3" {
		t.Fatalf("operation = %+v, want op-3", got)
	}
}

func TestVersion2PersistsItsNewFields(t *testing.T) {
	h := testsupport.NewHome(t)
	s := New(h.Paths())
	want := version2State()
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %+v\nwant %+v", got, want)
	}
	// A fresh store reads the same from disk.
	again, err := New(h.Paths()).Load()
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("Load() from a new store = %+v, %v", again, err)
	}
	// Writing the operation keeps every version 2 field.
	if err := s.RecordOperation(domain.Operation{ID: "o", Kind: domain.OperationApply, Target: "lock:x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearOperation(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the operation calls Load() = %+v\nwant %+v", got, want)
	}
}

func version2State() domain.PersistedState {
	at := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	return domain.PersistedState{
		Candidates: []domain.UpdateCandidate{},
		Skips:      []domain.Skip{},
		Results:    map[string]domain.TargetResult{"lock:a": {State: domain.StateUpToDate, CheckedAt: at}},
		Managed: map[string]domain.ManagedSkill{
			"find-skills": {Key: "find-skills", Targets: []string{"claude-code", "kiro"}},
		},
		Parked: map[string]domain.ParkedSkill{
			"grill-me": {
				Key:    "grill-me",
				Folder: "/home/user/.agents/.skillhub/disabled/grill-me",
				Links:  []domain.LinkRecord{{Path: "/home/user/.claude/skills/grill-me", Target: "../../.agents/skills/grill-me"}},
				Entry: &domain.ParkedEntry{
					Name: "grill-me", Raw: []byte(`{"source":"o/r","ref":"v1"}`),
					Before: "find-skills", After: "zeta", LockSHA: "abc123",
				},
				At:         at,
				SnapshotID: "snap-1",
			},
		},
		Preferences: domain.Preferences{Targets: []string{"claude-code", "codex"}},
	}
}

func TestAVersion1BinaryRejectsTheVersion2State(t *testing.T) {
	cases := map[string]domain.PersistedState{
		"only version 1 fields": {Candidates: []domain.UpdateCandidate{}, Skips: []domain.Skip{}},
		"with version 2 fields": version2State(),
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			h := testsupport.NewHome(t)
			s := New(h.Paths())
			if err := s.Save(st); err != nil {
				t.Fatal(err)
			}
			data, _ := readFile(t, h.Paths().StateFile())
			_, err := decodeV1StateFile(data)
			if err == nil {
				t.Fatal("the version 1 reader accepted a version 2 state.json")
			}
			if name == "only version 1 fields" && !errors.Is(err, errV1UnsupportedStateVersion) {
				t.Fatalf("version 1 reader error = %v, want its ErrUnsupportedStateVersion", err)
			}
		})
	}
}

func TestStateRejectsVersion1FileWithVersion2Fields(t *testing.T) {
	body := `{"version": 1, "candidates": [], "skips": [], "managed": {"a": {"key": "a", "targets": []}}}`
	s, h := seedState(t, []byte(body))
	if _, err := s.Load(); err == nil {
		t.Fatal("Load accepted a version 1 state.json with version 2 fields")
	}
	if err := s.Save(domain.PersistedState{}); err == nil {
		t.Fatal("Save accepted a version 1 state.json with version 2 fields")
	}
	if data, _ := readFile(t, h.Paths().StateFile()); string(data) != body {
		t.Fatal("state.json changed")
	}
	if _, ok := readFile(t, backupPath(s)); ok {
		t.Fatal("a rejected state made state.v1.json")
	}
}

func TestStateRejectsUnknownVersionsByName(t *testing.T) {
	for _, body := range []string{
		`{"version": 0, "candidates": [], "skips": []}`,
		`{"version": 3, "candidates": [], "skips": []}`,
	} {
		s, _ := seedState(t, []byte(body))
		if _, err := s.Load(); !errors.Is(err, ErrUnsupportedStateVersion) {
			t.Fatalf("Load(%s) = %v, want ErrUnsupportedStateVersion", body, err)
		}
	}
}
