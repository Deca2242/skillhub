package statefile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

const childRequestFile = "child_lock_request.txt"

// TestStateStoreContract runs the shared ports.StateStore contract suite against
// both the in-memory fake and the file-backed adapter.
func TestStateStoreContract(t *testing.T) {
	stores := []struct {
		name string
		new  func(t *testing.T) ports.StateStore
		// newVersion1 builds a store holding data, a version 1 state.json.
		newVersion1 func(t *testing.T, data []byte) ports.StateStore
		// migrationBlocked is the implementation's ErrMigrationBlocked.
		migrationBlocked error
	}{
		{
			name: "fakes.StateStore",
			new:  func(*testing.T) ports.StateStore { return fakes.NewStateStore() },
			newVersion1: func(t *testing.T, data []byte) ports.StateStore {
				st, op := v1State(t, data)
				s := fakes.NewStateStore()
				if err := s.SeedVersion1(st, op); err != nil {
					t.Fatalf("SeedVersion1: %v", err)
				}
				return s
			},
			migrationBlocked: fakes.ErrMigrationBlocked,
		},
		{
			name: "statefile.Store",
			new:  func(t *testing.T) ports.StateStore { return New(testsupport.NewHome(t).Paths()) },
			newVersion1: func(t *testing.T, data []byte) ports.StateStore {
				s, _ := seedState(t, data)
				return s
			},
			migrationBlocked: ErrMigrationBlocked,
		},
	}

	for _, impl := range stores {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("load missing returns empty", func(t *testing.T) {
				s := impl.new(t)
				got, err := s.Load()
				if err != nil {
					t.Fatalf("Load(): %v", err)
				}
				if len(got.Candidates) != 0 || len(got.Skips) != 0 || !got.Checks.LastRun.IsZero() || len(got.Notified.CandidateIDs) != 0 {
					t.Fatalf("Load() = %+v, want empty", got)
				}
			})

			t.Run("save and load preserve operation", func(t *testing.T) {
				s := impl.new(t)
				now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
				op := domain.Operation{
					ID:                "op-1",
					Kind:              domain.OperationApply,
					Target:            "lock:demo",
					CandidateID:       "cand-1",
					RestoreSnapshotID: "snap-1",
					StartedAt:         now,
				}
				if err := s.RecordOperation(op); err != nil {
					t.Fatalf("RecordOperation: %v", err)
				}
				want := domain.PersistedState{
					Candidates: []domain.UpdateCandidate{{
						ID:                  "cand-1",
						Target:              "lock:demo",
						Skills:              []string{"lock:demo"},
						FromSHA:             "aaa",
						InstalledSHAAtStage: "aaa",
						ToCommit:            "ccc",
						ToSHA:               "bbb",
						StagedPath:          "/tmp/cand-1",
						FileChanges:         []domain.FileChange{{Path: "SKILL.md", Change: domain.ChangeModified}},
						FrontmatterDelta:    domain.FrontmatterDelta{Changed: []string{"description"}},
						Labels:              []domain.Label{domain.LabelTextOnly},
						RulesetVersion:      1,
						FirstSeenAt:         now,
						State:               domain.CandidatePending,
					}},
					Skips:    []domain.Skip{{Target: "lock:other", ToSHA: "ddd", At: now}},
					Checks:   domain.CheckTimes{LastRun: now, LastSuccess: now},
					Notified: domain.NotifiedMarks{CandidateIDs: []string{"cand-1"}, OperationID: "op-1"},
					Results: map[string]domain.TargetResult{
						"lock:demo":    {State: domain.StateUpdateAvailable, CheckedAt: now},
						"lock:gone":    {State: domain.StateUpToDate, CheckedAt: now, RemovedUpstream: true},
						"lock:offline": {State: domain.StateCheckFailed, CheckedAt: now, Detail: "gh api: timeout"},
					},
				}
				if err := s.Save(want); err != nil {
					t.Fatalf("Save: %v", err)
				}
				got, err := s.Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Load() = %+v, want %+v", got, want)
				}
				gotOp, ok, err := s.Operation()
				if err != nil || !ok || !reflect.DeepEqual(gotOp, op) {
					t.Fatalf("Operation() = (%+v, %v, %v), want (%+v, true, nil)", gotOp, ok, err, op)
				}
				if err := s.ClearOperation(); err != nil {
					t.Fatalf("ClearOperation: %v", err)
				}
				if _, ok, err := s.Operation(); err != nil || ok {
					t.Fatalf("after ClearOperation Operation() = (_, %v, %v), want false, nil", ok, err)
				}
			})

			t.Run("save and load preserve version 2 fields", func(t *testing.T) {
				s := impl.new(t)
				want := version2State()
				if err := s.Save(want); err != nil {
					t.Fatalf("Save: %v", err)
				}
				if err := s.RecordOperation(domain.Operation{ID: "op-2", Kind: domain.OperationUndo, Target: "lock:a"}); err != nil {
					t.Fatalf("RecordOperation: %v", err)
				}
				got, err := s.Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Load() = %+v, want %+v", got, want)
				}
			})

			t.Run("a version 1 operation blocks version 2 fields until cleared", func(t *testing.T) {
				data := readFixture(t, fixtureV1Operation)
				want, wantOp := v1State(t, data)
				s := impl.newVersion1(t, data)
				assertState := func(t *testing.T, want domain.PersistedState, wantOp *domain.Operation) {
					t.Helper()
					if got, err := s.Load(); err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("Load() = %+v, %v\nwant %+v", got, err, want)
					}
					gotOp, ok, err := s.Operation()
					if err != nil || ok != (wantOp != nil) || (wantOp != nil && !reflect.DeepEqual(gotOp, *wantOp)) {
						t.Fatalf("Operation() = (%+v, %v, %v), want %+v", gotOp, ok, err, wantOp)
					}
				}
				assertState(t, want, wantOp)

				v2Fields := map[string]func(st *domain.PersistedState){
					"managed":     func(st *domain.PersistedState) { st.Managed = version2State().Managed },
					"parked":      func(st *domain.PersistedState) { st.Parked = version2State().Parked },
					"preferences": func(st *domain.PersistedState) { st.Preferences = version2State().Preferences },
				}
				for field, set := range v2Fields {
					st := want
					set(&st)
					if err := s.Save(st); !errors.Is(err, impl.migrationBlocked) {
						t.Fatalf("Save with %s while a version 1 operation is recorded = %v, want ErrMigrationBlocked", field, err)
					}
					assertState(t, want, wantOp)
				}

				// Writes without version 2 fields still go through, and
				// another operation keeps the migration waiting.
				kept := want
				kept.Skips = nil
				if err := s.Save(kept); err != nil {
					t.Fatalf("Save without version 2 fields: %v", err)
				}
				other := domain.Operation{ID: "op-3", Kind: domain.OperationUndo, Target: "lock:grill-me", RestoreSnapshotID: "s"}
				if err := s.RecordOperation(other); err != nil {
					t.Fatalf("RecordOperation: %v", err)
				}
				v2 := version2State()
				if err := s.Save(v2); !errors.Is(err, impl.migrationBlocked) {
					t.Fatalf("Save of version 2 fields after another operation = %v, want ErrMigrationBlocked", err)
				}
				assertState(t, kept, &other)

				// Once the operation is cleared the migration proceeds.
				if err := s.ClearOperation(); err != nil {
					t.Fatalf("ClearOperation: %v", err)
				}
				if err := s.Save(v2); err != nil {
					t.Fatalf("Save of version 2 fields after ClearOperation: %v", err)
				}
				assertState(t, v2, nil)
			})

			t.Run("a version 1 state without an operation takes version 2 fields", func(t *testing.T) {
				s := impl.newVersion1(t, readFixture(t, fixtureV1))
				st, err := s.Load()
				if err != nil {
					t.Fatal(err)
				}
				st.Parked = version2State().Parked
				if err := s.Save(st); err != nil {
					t.Fatalf("Save: %v", err)
				}
				if got, err := s.Load(); err != nil || !reflect.DeepEqual(got, st) {
					t.Fatalf("Load() = %+v, %v\nwant %+v", got, err, st)
				}
			})

			t.Run("journal preserves order", func(t *testing.T) {
				s := impl.new(t)
				t0 := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
				e1 := domain.HistoryEntry{At: t0, Action: domain.ActionApplied, Target: "lock:a", Labels: []domain.Label{domain.LabelTextOnly}}
				e2 := domain.HistoryEntry{At: t0.Add(time.Minute), Action: domain.ActionUndone, Target: "lock:a", Labels: []domain.Label{}}
				if err := s.AppendJournal(e1); err != nil {
					t.Fatalf("AppendJournal(e1): %v", err)
				}
				if err := s.AppendJournal(e2); err != nil {
					t.Fatalf("AppendJournal(e2): %v", err)
				}
				got, err := s.Journal()
				if err != nil {
					t.Fatalf("Journal: %v", err)
				}
				if !reflect.DeepEqual(got, []domain.HistoryEntry{e1, e2}) {
					t.Fatalf("Journal() = %+v, want [%+v, %+v]", got, e1, e2)
				}
			})

			t.Run("lock rejects concurrent holder and allows after release", func(t *testing.T) {
				s := impl.new(t)
				release, err := s.Lock(context.Background())
				if err != nil {
					t.Fatalf("Lock: %v", err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				if _, err := s.Lock(ctx); !errors.Is(err, ports.ErrStateLocked) {
					_ = release()
					t.Fatalf("second Lock err = %v, want ErrStateLocked", err)
				}
				if err := release(); err != nil {
					t.Fatalf("release: %v", err)
				}
				release2, err := s.Lock(context.Background())
				if err != nil {
					t.Fatalf("Lock after release: %v", err)
				}
				if err := release2(); err != nil {
					t.Fatalf("release2: %v", err)
				}
			})
		})
	}
}

func TestStateWritesVersion2AndRejectsUnknownVersionOrFields(t *testing.T) {
	h := testsupport.NewHome(t)
	store := New(h.Paths())

	if err := store.Save(domain.PersistedState{Candidates: []domain.UpdateCandidate{}, Skips: []domain.Skip{}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(h.Paths().StateFile())
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode raw state.json: %v", err)
	}
	if doc["version"] != float64(2) {
		t.Fatalf("state.json version = %v, want 2", doc["version"])
	}

	cases := []struct {
		name string
		body string
	}{
		{name: "broken json", body: `{not json`},
		{name: "version 2 with unknown field", body: `{"version": 2, "packs": [{"name":"p"}], "candidates": [], "skips": []}`},
		{name: "version 0", body: `{"version": 0, "candidates": [], "skips": []}`},
		{name: "version 3", body: `{"version": 3, "candidates": [], "skips": []}`},
		{name: "unknown field with version 1", body: `{"version": 1, "extra": true, "candidates": [], "skips": []}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := testsupport.NewHome(t)
			h.WriteFile(".local/state/skillhub/state.json", tc.body)
			store := New(h.Paths())

			if _, err := store.Load(); err == nil {
				t.Fatalf("Load() succeeded on %s, want error", tc.name)
			}
			if _, _, err := store.Operation(); err == nil {
				t.Fatalf("Operation() succeeded on %s, want error", tc.name)
			}
			if err := store.Save(domain.PersistedState{}); err == nil {
				t.Fatalf("Save() succeeded on %s, want error", tc.name)
			}
			if err := store.RecordOperation(domain.Operation{ID: "op", Kind: domain.OperationApply}); err == nil {
				t.Fatalf("RecordOperation() succeeded on %s, want error", tc.name)
			}
			if err := store.ClearOperation(); err == nil {
				t.Fatalf("ClearOperation() succeeded on %s, want error", tc.name)
			}
			after, err := os.ReadFile(h.Paths().StateFile())
			if err != nil {
				t.Fatalf("read state.json after failed ops: %v", err)
			}
			if string(after) != tc.body {
				t.Fatalf("corrupt/unsupported state.json was modified:\ngot:  %s\nwant: %s", after, tc.body)
			}
		})
	}

	t.Run("missing version is treated as version 1", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".local/state/skillhub/state.json", `{"candidates": [], "skips": []}`)
		store := New(h.Paths())
		if _, err := store.Load(); err != nil {
			t.Fatalf("Load() on versionless v1 state.json: %v", err)
		}
	})
}

func TestStateRejectsUnreadableWriteWithoutTouchingFile(t *testing.T) {
	h := testsupport.NewHome(t)
	store := New(h.Paths())

	valid := domain.PersistedState{
		Candidates: []domain.UpdateCandidate{},
		Skips:      []domain.Skip{{Target: "lock:keep", ToSHA: "abc", At: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}},
	}
	if err := store.Save(valid); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	before, err := os.ReadFile(h.Paths().StateFile())
	if err != nil {
		t.Fatalf("read initial state.json: %v", err)
	}

	// Empty Kind cannot be decoded back by OperationKind.UnmarshalText.
	if err := store.RecordOperation(domain.Operation{ID: "keep"}); err == nil {
		t.Fatal("RecordOperation with empty Kind succeeded, want error")
	}

	after, err := os.ReadFile(h.Paths().StateFile())
	if err != nil {
		t.Fatalf("read state.json after rejected write: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("state.json changed after rejected write:\ngot:  %s\nwant: %s", after, before)
	}
}

func TestStateReadOnlyDirPreservesFileAndLeavesNoTempFiles(t *testing.T) {
	h := testsupport.NewHome(t)
	store := New(h.Paths())

	if err := store.Save(domain.PersistedState{Skips: []domain.Skip{{Target: "lock:ok", ToSHA: "111"}}}); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	before, err := os.ReadFile(h.Paths().StateFile())
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}

	dir := h.Paths().StateDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod 0555: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := store.Save(domain.PersistedState{}); err == nil {
		t.Fatal("Save into read-only dir succeeded, want error")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("restore chmod 0755: %v", err)
	}
	after, err := os.ReadFile(h.Paths().StateFile())
	if err != nil {
		t.Fatalf("read state.json after failure: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("state.json changed after failed write:\ngot:  %s\nwant: %s", after, before)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %q in %s", e.Name(), dir)
		}
	}
}

func TestStateFailedRenameCleansTempFiles(t *testing.T) {
	h := testsupport.NewHome(t)

	// Make state.json a directory so CreateTemp succeeds and os.Rename fails.
	if err := os.MkdirAll(h.Paths().StateFile(), 0o755); err != nil {
		t.Fatalf("mkdir state.json: %v", err)
	}
	if err := writeFileAtomic(h.Paths().StateFile(), []byte("{}\n"), 0o644); err == nil {
		t.Fatal("writeFileAtomic over directory succeeded, want error")
	}
	entries, err := os.ReadDir(h.Paths().StateDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %q in %s", e.Name(), h.Paths().StateDir())
		}
	}
}

func TestOperationSurvivesRestartAndClears(t *testing.T) {
	h := testsupport.NewHome(t)
	store1 := New(h.Paths())

	op := domain.Operation{
		ID:                "op-restart",
		Kind:              domain.OperationUndo,
		Target:            "lock:demo",
		RestoreSnapshotID: "snap-42",
		StartedAt:         time.Date(2026, 11, 21, 13, 30, 0, 0, time.UTC),
	}
	if err := store1.RecordOperation(op); err != nil {
		t.Fatalf("RecordOperation: %v", err)
	}

	store2 := New(h.Paths())
	got, ok, err := store2.Operation()
	if err != nil || !ok || !reflect.DeepEqual(got, op) {
		t.Fatalf("reopened Operation() = (%+v, %v, %v), want (%+v, true, nil)", got, ok, err, op)
	}

	if err := store2.ClearOperation(); err != nil {
		t.Fatalf("ClearOperation: %v", err)
	}

	store3 := New(h.Paths())
	if _, ok, err := store3.Operation(); err != nil || ok {
		t.Fatalf("after ClearOperation Operation() = (_, %v, %v), want false, nil", ok, err)
	}
}

func TestJournalAppendOnlyPreservesSeededBytes(t *testing.T) {
	h := testsupport.NewHome(t)
	seeded := `{"at":"2026-11-21T09:00:00Z",  "action": "applied", "target":"lock:seeded", "from_sha":"0","to_sha":"1","snapshot_id":"s0","labels":[],"detail":"seeded", "extra_key": "keep-me"}` + "\n"
	h.WriteFile(".local/state/skillhub/journal.jsonl", seeded)

	store := New(h.Paths())
	e2 := domain.HistoryEntry{
		At:         time.Date(2026, 11, 21, 10, 1, 0, 0, time.UTC),
		Action:     domain.ActionUndone,
		Target:     "lock:one",
		FromSHA:    "222",
		ToSHA:      "111",
		SnapshotID: "s1",
		Labels:     []domain.Label{},
		Detail:     "second",
	}
	if err := store.AppendJournal(e2); err != nil {
		t.Fatalf("AppendJournal(e2): %v", err)
	}
	after, err := os.ReadFile(h.Paths().JournalFile())
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if !strings.HasPrefix(string(after), seeded) {
		t.Fatalf("seeded line was altered:\ngot:\n%s\nwant prefix:\n%s", after, seeded)
	}

	entries, err := store.Journal()
	if err != nil {
		t.Fatalf("Journal(): %v", err)
	}
	if len(entries) != 2 || entries[0].Target != "lock:seeded" || entries[1].Target != "lock:one" {
		t.Fatalf("Journal() = %+v, want [lock:seeded, lock:one]", entries)
	}
}

func TestJournalHandlesTruncatedLineAndReportsSkipped(t *testing.T) {
	h := testsupport.NewHome(t)
	store := New(h.Paths())

	t0 := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	e1 := domain.HistoryEntry{
		At:     t0,
		Action: domain.ActionApplied,
		Target: "lock:valid-1",
		Labels: []domain.Label{domain.LabelTextOnly},
	}
	if err := store.AppendJournal(e1); err != nil {
		t.Fatalf("AppendJournal(e1): %v", err)
	}

	// Simulate an interrupted write leaving an incomplete trailing line without newline.
	f, err := os.OpenFile(h.Paths().JournalFile(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := f.WriteString(`{"at":"2026-11-21T10:01:00Z","action":"ap`); err != nil {
		_ = f.Close()
		t.Fatalf("write truncated fragment: %v", err)
	}
	_ = f.Close()

	e2 := domain.HistoryEntry{
		At:     t0.Add(2 * time.Minute),
		Action: domain.ActionSkipped,
		Target: "lock:valid-2",
		Labels: []domain.Label{},
	}
	if err := store.AppendJournal(e2); err != nil {
		t.Fatalf("AppendJournal(e2) after truncated line: %v", err)
	}

	entries, skipped, err := store.JournalWithSkipped()
	if err != nil {
		t.Fatalf("JournalWithSkipped(): %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	want := []domain.HistoryEntry{e1, e2}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %+v, want %+v", entries, want)
	}

	// ports.StateStore.Journal() also returns the valid entries without error.
	viaPort, err := store.Journal()
	if err != nil {
		t.Fatalf("Journal(): %v", err)
	}
	if !reflect.DeepEqual(viaPort, want) {
		t.Fatalf("Journal() = %+v, want %+v", viaPort, want)
	}
}

func TestLockBackgroundContextTimesOutWithErrStateLocked(t *testing.T) {
	h := testsupport.NewHome(t)
	store1 := New(h.Paths())
	store2 := New(h.Paths())

	release, err := store1.Lock(context.Background())
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		release2, lockErr := store2.Lock(context.Background())
		if lockErr == nil {
			_ = release2()
		}
		errCh <- lockErr
	}()

	select {
	case err = <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("second Lock(Background) hung past 2s deadline")
	}
	if !errors.Is(err, ports.ErrStateLocked) {
		t.Fatalf("second Lock(Background) err = %v, want ErrStateLocked", err)
	}
}

func TestLockRejectsChildProcessAndSucceedsAfterRelease(t *testing.T) {
	h := testsupport.NewHome(t)
	store := New(h.Paths())

	release, err := store.Lock(context.Background())
	if err != nil {
		t.Fatalf("parent Lock: %v", err)
	}

	if exitCode, out := runLockChild(t, h.Paths().StateLockFile()); exitCode == 0 {
		_ = release()
		t.Fatalf("child acquired lock while parent held it; output:\n%s", out)
	} else if !strings.Contains(out, "LOCKED") {
		_ = release()
		t.Fatalf("child failed unexpectedly (exit %d), want LOCKED:\n%s", exitCode, out)
	}

	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	if exitCode, out := runLockChild(t, h.Paths().StateLockFile()); exitCode != 0 {
		t.Fatalf("child failed after release (exit %d):\n%s", exitCode, out)
	}
}

// TestLockChild is the child-process entry point for TestLockRejectsChildProcessAndSucceedsAfterRelease.
func TestLockChild(t *testing.T) {
	raw, err := os.ReadFile(childRequestFile)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("helper for child process lock test")
	}
	if err != nil {
		t.Fatalf("read child request: %v", err)
	}
	lockFile := strings.TrimSpace(string(raw))
	stateDir := filepath.Dir(lockFile)
	stateRoot := filepath.Dir(stateDir)
	home := filepath.Join(filepath.Dir(stateRoot), "child-home")
	p, err := paths.New(paths.Env{Home: home, XDGStateHome: stateRoot})
	if err != nil {
		t.Fatalf("paths.New in child: %v", err)
	}
	store := New(p)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	release, err := store.Lock(ctx)
	if errors.Is(err, ports.ErrStateLocked) {
		t.Log("LOCKED")
		t.FailNow()
	}
	if err != nil {
		t.Fatalf("unexpected lock error: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("child release: %v", err)
	}
}

func runLockChild(t *testing.T, lockFile string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, childRequestFile), []byte(lockFile), 0o600); err != nil {
		t.Fatalf("write child request: %v", err)
	}
	res, err := proc.New().Run(context.Background(), ports.Command{
		Name: "sh",
		Args: []string{"-c", `exec "$1" -test.run=^TestLockChild$ -test.count=1`, "sh", exe},
		Dir:  workDir,
	})
	if err != nil {
		t.Fatalf("run lock child: %v", err)
	}
	return res.ExitCode, string(res.Stdout) + string(res.Stderr)
}
