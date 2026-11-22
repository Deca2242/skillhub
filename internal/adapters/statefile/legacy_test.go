package statefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/shared/domain"
)

// legacyTemps lists the .tmp-v1-* names next to state.json.
func legacyTemps(t *testing.T, s *Store) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(filepath.Dir(s.paths.StateFile()), ".tmp-v1-*"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestAFailedVersion1CopyConvertsNothing(t *testing.T) {
	op := domain.Operation{ID: "op-2", Kind: domain.OperationApply, Target: "lock:find-skills", RestoreSnapshotID: "snap"}
	writes := map[string]func(s *Store) error{
		"Save":            func(s *Store) error { st, _ := s.Load(); return s.Save(st) },
		"RecordOperation": func(s *Store) error { return s.RecordOperation(op) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			original := readFixture(t, fixtureV1)
			s, h := seedState(t, original)
			injected := errors.New("injected: disk full")
			s.link = func(string, string) error { return injected }

			if err := write(s); !errors.Is(err, injected) {
				t.Fatalf("%s with a failing state.v1.json copy = %v, want the injected error", name, err)
			}
			data, _ := readFile(t, h.Paths().StateFile())
			if string(data) != string(original) {
				t.Fatalf("state.json changed although the copy failed:\n%s", data)
			}
			if v := fileVersion(t, data); v != float64(1) {
				t.Fatalf("version = %v, want 1", v)
			}
			if _, ok := readFile(t, backupPath(s)); ok {
				t.Fatal("a failed copy left state.v1.json")
			}
			if left := legacyTemps(t, s); len(left) != 0 {
				t.Fatalf("a failed copy left temporary files: %v", left)
			}
			if _, ok, err := s.Operation(); err != nil || ok {
				t.Fatalf("Operation() = (_, %v, %v), want none", ok, err)
			}
		})
	}
}

func TestTheNextWriteCleansALeftoverVersion1Temp(t *testing.T) {
	writes := map[string]func(s *Store) error{
		"Save":           func(s *Store) error { st, _ := s.Load(); return s.Save(st) },
		"ClearOperation": func(s *Store) error { return s.ClearOperation() },
	}

	t.Run("the migrating write", func(t *testing.T) {
		original := readFixture(t, fixtureV1)
		s, _ := seedState(t, original)
		leftover := filepath.Join(filepath.Dir(s.paths.StateFile()), ".tmp-v1-123456")
		if err := os.WriteFile(leftover, []byte("half a copy"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, _ := s.Load()
		if err := s.Save(st); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if left := legacyTemps(t, s); len(left) != 0 {
			t.Fatalf("leftover temporary files after the write: %v", left)
		}
		if got, _ := readFile(t, backupPath(s)); string(got) != string(original) {
			t.Fatalf("state.v1.json is not the original:\n%s", got)
		}
	})

	for name, write := range writes {
		t.Run("a version 2 "+name+" keeps state.v1.json", func(t *testing.T) {
			original := readFixture(t, fixtureV1Operation)
			s, h := seedState(t, original)
			if err := s.ClearOperation(); err != nil { // still version 1
				t.Fatal(err)
			}
			st, _ := s.Load()
			if err := s.Save(st); err != nil { // migrates
				t.Fatal(err)
			}
			if err := s.RecordOperation(domain.Operation{ID: "op-9", Kind: domain.OperationUndo, Target: "lock:a"}); err != nil {
				t.Fatal(err)
			}
			backup, ok := readFile(t, backupPath(s))
			if !ok {
				t.Fatal("no state.v1.json after the migration")
			}
			// A crash after the copy was linked but before its temporary
			// name went away leaves both names on the same file.
			dir := filepath.Dir(h.Paths().StateFile())
			linked := filepath.Join(dir, ".tmp-v1-linked")
			if err := os.Link(backupPath(s), linked); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".tmp-v1-orphan"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := write(s); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if left := legacyTemps(t, s); len(left) != 0 {
				t.Fatalf("leftover temporary files after %s: %v", name, left)
			}
			if got, ok := readFile(t, backupPath(s)); !ok || string(got) != string(backup) {
				t.Fatalf("%s changed or removed state.v1.json (exists=%v):\n%s", name, ok, got)
			}
			data, _ := readFile(t, h.Paths().StateFile())
			if v := fileVersion(t, data); v != float64(2) {
				t.Fatalf("version = %v, want 2", v)
			}
		})
	}

	t.Run("a refused write leaves state.json alone", func(t *testing.T) {
		original := readFixture(t, fixtureV1Operation)
		s, h := seedState(t, original)
		if err := os.WriteFile(filepath.Join(filepath.Dir(s.paths.StateFile()), ".tmp-v1-1"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, _ := s.Load()
		st.Parked = map[string]domain.ParkedSkill{"demo": {Key: "demo", Folder: "/x"}}
		if err := s.Save(st); !errors.Is(err, ErrMigrationBlocked) {
			t.Fatalf("Save = %v, want ErrMigrationBlocked", err)
		}
		if data, _ := readFile(t, h.Paths().StateFile()); string(data) != string(original) {
			t.Fatal("a refused write changed state.json")
		}
		if _, ok := readFile(t, backupPath(s)); ok {
			t.Fatal("a refused write made state.v1.json")
		}
	})
}
