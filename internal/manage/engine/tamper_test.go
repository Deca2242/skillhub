package engine_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/ports"
)

// editJSON rewrites a JSON file through edit.
func editJSON(t *testing.T, path string, edit func(m map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	var m map[string]any
	must(t, json.Unmarshal(data, &m))
	edit(m)
	data, err = json.MarshalIndent(m, "", "  ")
	must(t, err)
	must(t, os.WriteFile(path, data, 0o644))
}

// crashAfterPark interrupts disablePlan after the park's progress is on
// disk, so the record holds the entry the park took.
func crashAfterPark(t *testing.T, recordWritten bool) (*env, fixturePaths, world) {
	t.Helper()
	e, fp := newEnv(t)
	before := observeWorld(t, e)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	at := scenario{}.firstStepWrite(failingWrites(t, scenarios()["disable"].setup)) + 5 // the record write after the park
	mode := crashBefore
	if recordWritten {
		mode = crashAfter
	}
	e.files.arm(at, mode)
	if _, err := eng.Apply(t.Context(), plan); !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	return e, fp, before
}

// TestARecordWhoseParkedNeighboursWereEditedIsRefused moves the parked
// entry's neighbours in the record: Recover would put alpha back after beta.
func TestARecordWhoseParkedNeighboursWereEditedIsRefused(t *testing.T) {
	e, _, _ := crashAfterPark(t, true)
	editJSON(t, engine.RecordFile(e.p), func(m map[string]any) {
		parked := m["parked"].(map[string]any)["2"].(map[string]any)
		delete(parked, "after")
		parked["before"] = "beta"
	})
	stopped := observeWorld(t, e)

	if _, err := e.clean().Recover(t.Context()); !errors.Is(err, engine.ErrUnsupportedRecord) {
		t.Fatalf("error = %v, want ErrUnsupportedRecord", err)
	}
	assertWorld(t, "a refused record", observeWorld(t, e), stopped)
}

// TestASnapshotWhoseLockOrderWasEditedIsRefused swaps the lock order in
// snapshot.json while the park's progress is not recorded, so Recover would
// derive alpha's neighbours from it.
func TestASnapshotWhoseLockOrderWasEditedIsRefused(t *testing.T) {
	e, _, _ := crashAfterPark(t, false)
	rec := pendingRecord(t, e)
	editJSON(t, filepath.Join(e.clean().SnapshotDir(rec.SnapshotID), "snapshot.json"), func(m map[string]any) {
		m["lock_keys"] = []any{"beta", "alpha"}
	})
	stopped := observeWorld(t, e)

	if _, err := e.clean().Recover(t.Context()); !errors.Is(err, engine.ErrUnsupportedRecord) {
		t.Fatalf("error = %v, want ErrUnsupportedRecord", err)
	}
	assertWorld(t, "a refused snapshot", observeWorld(t, e), stopped)
}

// TestASnapshotWhoseStateWasEditedIsNotUndone edits the state image an undo
// would write back as managed.
func TestASnapshotWhoseStateWasEditedIsNotUndone(t *testing.T) {
	e, fp := newEnv(t)
	out, err := e.clean().Apply(t.Context(), disableEffectPlan(t, e, e.clean(), fp))
	must(t, err)
	editJSON(t, filepath.Join(e.clean().SnapshotDir(out.SnapshotID), "snapshot.json"), func(m map[string]any) {
		managed := m["state"].(map[string]any)["managed"].(map[string]any)
		managed["alpha"].(map[string]any)["targets"] = []any{"forged-harness"}
	})
	disabled := observeWorld(t, e)

	if _, err := e.clean().Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrUnsupportedRecord) {
		t.Fatalf("error = %v, want ErrUnsupportedRecord", err)
	}
	assertWorld(t, "a refused undo", observeWorld(t, e), disabled)
	if recordExists(t, e.p) {
		t.Error("a refused undo left a record")
	}
}

// reorderedLock is fixtureLock with beta written before alpha, each entry
// byte for byte the same, as another writer could leave it.
const reorderedLock = `{
  "version": 3,
  "skills": {
    "beta": {
      "source": "owner/beta",
      "sourceType": "github",
      "skillFolderHash": "2222222222222222222222222222222222222222"
    },
    "alpha": {
      "source": "owner/alpha",
      "sourceType": "github",
      "ref": "v1",
      "skillFolderHash": "1111111111111111111111111111111111111111"
    }
  },
  "dismissed": {}
}`

// TestALockRewrittenBeforeTheParkIsRolledBackWhereItWas reorders the lock
// right before the park: alpha's bytes are the same, so the step runs, but
// its neighbours are not the ones the snapshot derives. The operation must
// not record them; it rolls back, and alpha goes back where the other
// writer had put it.
func TestALockRewrittenBeforeTheParkIsRolledBackWhereItWas(t *testing.T) {
	e, fp := newEnv(t)
	// The world to come back to: as it is, with the other writer's lock.
	writeLock(t, e.p, []byte(reorderedLock))
	before := observeWorld(t, e)
	writeLock(t, e.p, []byte(fixtureLock))
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	records := 0
	e.files.afterWrite = func(_ int, line string) {
		if strings.HasPrefix(line, "WriteFileAtomic") && strings.HasSuffix(line, "operation-v2.json") {
			records++
			if records == 3 { // after the move's progress, before the park
				writeLock(t, e.p, []byte(reorderedLock))
			}
		}
	}
	_, err := eng.Apply(t.Context(), plan)
	if !errors.Is(err, ports.ErrLockChanged) {
		t.Fatalf("error = %v, want ErrLockChanged", err)
	}
	assertWorld(t, "after the rollback", observeWorld(t, e), before)
	if recordExists(t, e.p) {
		t.Error("the rolled back operation left its record")
	}
}

// TestBeginClearsTheRestoreRoot plants what an interrupted restore could
// leave: the next operation removes it before its first step, not only when
// it closes.
func TestBeginClearsTheRestoreRoot(t *testing.T) {
	e, fp := newEnv(t)
	e.home.WriteFile(".agents/.skillhub/restore/.x.copy-1/SKILL.md", "leftover\n")
	root := e.home.Path(".agents/.skillhub/restore")
	checked := false
	e.files.afterWrite = func(_ int, line string) {
		if !checked && strings.HasPrefix(line, "WriteFileAtomic") && strings.HasSuffix(line, "operation-v2.json") {
			checked = true
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the restore root is still there when the record is written: %v", err)
			}
		}
	}
	_, err := e.engine().Apply(t.Context(), disablePlan(t, e.engine(), fp))
	must(t, err)
	if !checked {
		t.Fatal("the record was never written")
	}
}
