package engine_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

func TestBeginWritesTheRecordAfterAVerifiedSnapshot(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	alphaBefore, betaBefore := tree(t, fp.alpha), tree(t, fp.beta)
	lockBefore, err := os.ReadFile(e.p.SkillsLockFile())
	must(t, err)

	run, err := eng.Begin(t.Context(), plan)
	must(t, err)
	defer func() { must(t, run.Release()) }()

	// Nothing the plan touches changed: Begin only records.
	if got := tree(t, fp.alpha); !reflect.DeepEqual(got, alphaBefore) {
		t.Error("Begin changed alpha")
	}
	if got := tree(t, fp.beta); !reflect.DeepEqual(got, betaBefore) {
		t.Error("Begin changed beta")
	}
	if got, _ := os.ReadFile(e.p.SkillsLockFile()); !bytes.Equal(got, lockBefore) {
		t.Error("Begin changed the lock")
	}
	if _, err := os.Readlink(fp.link); err != nil {
		t.Error("Begin removed the link")
	}
	// The writes, in order: every folder copy, the snapshot metadata, then
	// the record.
	snapDir := eng.SnapshotDir(run.Record().SnapshotID)
	wantWrites := []string{
		"CopyTree " + filepath.Join(snapDir, "trees", "0"),
		"CopyTree " + filepath.Join(snapDir, "trees", "1"),
		"WriteFileAtomic " + filepath.Join(snapDir, "snapshot.json"),
		"WriteFileAtomic " + engine.RecordFile(e.p),
	}
	if got := e.files.writeLog(); !reflect.DeepEqual(got, wantWrites) {
		t.Errorf("writes = %q\nwant %q", got, wantWrites)
	}

	// The record, read back by a later process, is the one Begin returned.
	rec, pending, err := e.clean().PendingRecord()
	must(t, err)
	if !pending || !reflect.DeepEqual(rec, run.Record()) {
		t.Errorf("record read back = %+v, %t\nwant %+v", rec, pending, run.Record())
	}
	if rec.PlanID != plan.ID || !reflect.DeepEqual(rec.Plan, plan) || rec.LastCompleted != -1 ||
		!rec.StartedAt.Equal(e.clock.Now()) {
		t.Errorf("record = %+v", rec)
	}

	// The snapshot keeps both folders byte for byte, with their modes and
	// internal link, the link's exact target and the entry's exact bytes.
	snap, err := e.clean().VerifySnapshot(rec.SnapshotID)
	must(t, err)
	if len(snap.Trees) != 2 || snap.Trees[0].Path != fp.alpha || snap.Trees[1].Path != fp.beta ||
		snap.Trees[0].TreeSHA != plan.Steps[1].Pre[0].TreeSHA {
		t.Errorf("trees = %+v", snap.Trees)
	}
	if got := tree(t, filepath.Join(snapDir, "trees", "0")); !reflect.DeepEqual(got, alphaBefore) {
		t.Errorf("copy of alpha:\n%v\nwant:\n%v", got, alphaBefore)
	}
	if got := tree(t, filepath.Join(snapDir, "trees", "1")); !reflect.DeepEqual(got, betaBefore) {
		t.Errorf("copy of beta:\n%v\nwant:\n%v", got, betaBefore)
	}
	if want := []engine.LinkCopy{{Path: fp.link, Target: fp.linkTarget}}; !reflect.DeepEqual(snap.Links, want) {
		t.Errorf("links = %+v", snap.Links)
	}
	if len(snap.Entries) != 1 || snap.Entries[0].Name != "alpha" || snap.Entries[0].State != engine.StateEntry ||
		!bytes.Equal(snap.Entries[0].Raw, plan.Steps[2].Pre[0].Raw) {
		t.Errorf("entries = %+v", snap.Entries)
	}
	if !snap.LockExists || snap.LockSHA != plan.LockSHA || snap.PlanID != plan.ID {
		t.Errorf("snapshot = %+v", snap)
	}
	orphans, err := e.clean().Orphans()
	must(t, err)
	if len(orphans) != 0 {
		t.Errorf("orphans = %+v", orphans)
	}
}

func TestASnapshotOfNoLockRecordsThatThereWasNone(t *testing.T) {
	e, _ := newEnv(t)
	writeLock(t, e.p, nil)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "install"})
	must(t, b.LockAdd("new", []byte(`{"source":"owner/new"}`)))
	plan, err := b.Build()
	must(t, err)

	run, err := eng.Begin(t.Context(), plan)
	must(t, err)
	must(t, run.Release())

	snap, err := e.clean().ReadSnapshot(run.Record().SnapshotID)
	must(t, err)
	if snap.LockExists || snap.LockSHA != "" {
		t.Errorf("lock = %t %q, want no lock", snap.LockExists, snap.LockSHA)
	}
	if want := []engine.EntryCopy{{Name: "new", State: engine.StateNoLock}}; !reflect.DeepEqual(snap.Entries, want) {
		t.Errorf("entries = %+v, want %+v", snap.Entries, want)
	}
	if _, err := os.Stat(e.p.SkillsLockFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Begin created the lock: %v", err)
	}
}

func TestBeginRefusesWhileAnOperationIsPending(t *testing.T) {
	t.Run("version 1", func(t *testing.T) {
		e, fp := newEnv(t)
		eng := e.engine()
		plan := disablePlan(t, eng, fp)
		must(t, e.state.RecordOperation(domain.Operation{ID: "op1", Kind: domain.OperationApply, Target: "lock:alpha",
			RestoreSnapshotID: "s1", StartedAt: time.Now()}))

		if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrV1OperationPending) {
			t.Errorf("error = %v, want ErrV1OperationPending", err)
		}
		if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 || len(e.files.writeLog()) != 0 {
			t.Errorf("a refused Begin wrote: %v", e.files.writeLog())
		}
		assertLockFree(t, e)
	})
	t.Run("version 2", func(t *testing.T) {
		e, fp := newEnv(t)
		eng := e.engine()
		plan := disablePlan(t, eng, fp)
		run, err := eng.Begin(t.Context(), plan)
		must(t, err)
		must(t, run.Release())
		recBefore, err := os.ReadFile(engine.RecordFile(e.p))
		must(t, err)
		e.files.arm(-1, failOnce)

		if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrOperationPending) {
			t.Errorf("error = %v, want ErrOperationPending", err)
		}
		if got, _ := os.ReadFile(engine.RecordFile(e.p)); !bytes.Equal(got, recBefore) || len(e.files.writeLog()) != 0 {
			t.Errorf("a refused Begin wrote: %v", e.files.writeLog())
		}
		if dirs := snapshotDirs(t, e.p); len(dirs) != 1 {
			t.Errorf("snapshots = %v, want only the first", dirs)
		}
		// Even a record this binary cannot read keeps the engine from
		// starting.
		must(t, os.WriteFile(engine.RecordFile(e.p), []byte("{}"), 0o600))
		if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrOperationPending) {
			t.Errorf("unreadable record: error = %v, want ErrOperationPending", err)
		}
		assertLockFree(t, e)
	})
}

// assertLockFree checks that the state lock is free again.
func assertLockFree(t *testing.T, e *env) {
	t.Helper()
	release, err := e.state.Lock(t.Context())
	if err != nil {
		t.Fatalf("the state lock is still held: %v", err)
	}
	must(t, release())
}

func TestTheWholeOperationHoldsTheStateLock(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)

	release, err := e.state.Lock(t.Context())
	must(t, err)
	if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, ports.ErrStateLocked) {
		t.Errorf("Begin while version 1 holds the lock: error = %v, want ErrStateLocked", err)
	}
	if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
		t.Error("Begin wrote without the lock")
	}
	must(t, release())

	run, err := eng.Begin(t.Context(), plan)
	must(t, err)
	if _, err := e.state.Lock(t.Context()); !errors.Is(err, ports.ErrStateLocked) {
		t.Errorf("version 1 took the lock during the operation: %v", err)
	}
	must(t, run.Release())
	assertLockFree(t, e)
}

func TestStepDoneRecordsProgressInOrder(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	run, err := eng.Begin(t.Context(), plan)
	must(t, err)
	defer func() { must(t, run.Release()) }()

	if err := run.StepDone(1); !errors.Is(err, engine.ErrProgress) {
		t.Errorf("skipping step 0: error = %v, want ErrProgress", err)
	}
	must(t, run.StepDone(0))
	if err := run.StepDone(0); !errors.Is(err, engine.ErrProgress) {
		t.Errorf("step 0 twice: error = %v, want ErrProgress", err)
	}
	readBack := func() int {
		t.Helper()
		rec, _, err := e.clean().PendingRecord()
		must(t, err)
		return rec.LastCompleted
	}
	if got := readBack(); got != 0 {
		t.Errorf("last completed on disk = %d, want 0", got)
	}

	e.files.arm(0, failOnce)
	if err := run.StepDone(1); !errors.Is(err, errInjected) {
		t.Errorf("failed write: error = %v", err)
	}
	if got := readBack(); got != 0 || run.Record().LastCompleted != 0 {
		t.Errorf("after a failed write, last completed = %d on disk, %d in memory; want 0", got, run.Record().LastCompleted)
	}
	must(t, run.StepDone(1))
	must(t, run.StepDone(2))
	must(t, run.StepDone(3))
	if err := run.StepDone(4); !errors.Is(err, engine.ErrProgress) {
		t.Errorf("past the last step: error = %v, want ErrProgress", err)
	}
	if got := readBack(); got != 3 {
		t.Errorf("last completed on disk = %d, want 3", got)
	}
}

func TestPendingRecordRefusesWhatThisBinaryDoesNotKnow(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	run, err := eng.Begin(t.Context(), plan)
	must(t, err)
	must(t, run.Release())
	good, err := os.ReadFile(engine.RecordFile(e.p))
	must(t, err)

	edit := func(f func(m map[string]any)) []byte {
		var m map[string]any
		must(t, json.Unmarshal(good, &m))
		f(m)
		data, err := json.Marshal(m)
		must(t, err)
		return data
	}
	tests := map[string][]byte{
		"another format":                edit(func(m map[string]any) { m["format"] = "skillhub.other" }),
		"version 3":                     edit(func(m map[string]any) { m["version"] = 3 }),
		"version 1":                     edit(func(m map[string]any) { m["version"] = 1 }),
		"no version":                    edit(func(m map[string]any) { delete(m, "version") }),
		"an unknown field":              edit(func(m map[string]any) { m["extra"] = true }),
		"another plan id":               edit(func(m map[string]any) { m["plan_id"] = strings.Repeat("0", 64) }),
		"a plan edited":                 edit(func(m map[string]any) { m["plan"].(map[string]any)["action"] = "remove" }),
		"progress past the end":         edit(func(m map[string]any) { m["last_completed"] = 4 }),
		"progress below -1":             edit(func(m map[string]any) { m["last_completed"] = -2 }),
		"a snapshot id that climbs out": edit(func(m map[string]any) { m["snapshot_id"] = "op-../../x" }),
		"a version 1 snapshot id":       edit(func(m map[string]any) { m["snapshot_id"] = "20261122T100000.000000000Z-lock_alpha" }),
		"the bare snapshot prefix":      edit(func(m map[string]any) { m["snapshot_id"] = "op-" }),
		"trailing data":                 append(append([]byte{}, good...), []byte("{}")...),
		"not JSON":                      []byte("{"),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			must(t, os.WriteFile(engine.RecordFile(e.p), data, 0o600))
			if _, _, err := e.clean().PendingRecord(); !errors.Is(err, engine.ErrUnsupportedRecord) {
				t.Errorf("error = %v, want ErrUnsupportedRecord", err)
			}
		})
	}
	must(t, os.WriteFile(engine.RecordFile(e.p), good, 0o600))
	if _, pending, err := e.clean().PendingRecord(); err != nil || !pending {
		t.Errorf("the original record: %t, %v", pending, err)
	}
}

func TestReadSnapshotRefusesWhatThisBinaryDoesNotKnow(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	run, err := eng.Begin(t.Context(), disablePlan(t, eng, fp))
	must(t, err)
	must(t, run.Release())
	id := run.Record().SnapshotID
	meta := filepath.Join(eng.SnapshotDir(id), "snapshot.json")
	good, err := os.ReadFile(meta)
	must(t, err)

	for name, from := range map[string]string{
		"another format": `"format": "skillhub.snapshot"`,
		"version 1":      `"version": 2`,
		"another id":     `"snapshot_id": "` + id + `"`,
		"a moved copy":   `"copy": "trees/0"`,
	} {
		t.Run(name, func(t *testing.T) {
			to := map[string]string{
				"another format": `"format": "other"`, "version 1": `"version": 1`,
				"another id": `"snapshot_id": "op-other"`, "a moved copy": `"copy": "../../x"`,
			}[name]
			if !bytes.Contains(good, []byte(from)) {
				t.Fatalf("%s not in snapshot.json", from)
			}
			must(t, os.WriteFile(meta, bytes.Replace(good, []byte(from), []byte(to), 1), 0o644))
			if _, err := e.clean().ReadSnapshot(id); !errors.Is(err, engine.ErrUnsupportedRecord) {
				t.Errorf("error = %v, want ErrUnsupportedRecord", err)
			}
		})
	}
	must(t, os.WriteFile(meta, good, 0o644))
	must(t, os.WriteFile(filepath.Join(eng.SnapshotDir(id), "trees", "1", "SKILL.md"), []byte("rot\n"), 0o644))
	if _, err := e.clean().VerifySnapshot(id); !errors.Is(err, engine.ErrSnapshotMismatch) {
		t.Errorf("a copy that changed: error = %v, want ErrSnapshotMismatch", err)
	}
	for _, bad := range []string{"", "op-", "../x", "op-a/b", "20261122-lock_alpha"} {
		if _, err := e.clean().ReadSnapshot(bad); !errors.Is(err, engine.ErrSnapshotNotFound) {
			t.Errorf("ReadSnapshot(%q) error = %v, want ErrSnapshotNotFound", bad, err)
		}
	}
}

func TestTheSnapshotRefusesACopyThatIsNotTheFolder(t *testing.T) {
	tests := []struct {
		name string
		rig  func(e *env)
	}{
		{"the copy hashes to another tree", func(e *env) {
			e.hash = func(dir string) (treehash.Tree, error) {
				tr, err := treehash.Dir(dir)
				if strings.HasPrefix(dir, e.p.SnapshotsDir()) {
					tr.SHA = strings.Repeat("0", 40)
				}
				return tr, err
			}
		}},
		{"a file of the copy has other permission bits", func(e *env) {
			e.files.afterCopy = func(dst string) {
				_ = os.Chmod(filepath.Join(dst, "notes", "private.txt"), 0o644)
			}
		}},
		{"a folder of the copy has other permission bits", func(e *env) {
			e.files.afterCopy = func(dst string) { _ = os.Chmod(dst, 0o700) }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, fp := newEnv(t)
			tt.rig(e)
			eng := e.engine()
			b := eng.NewBuilder(engine.Meta{Action: "remove"})
			must(t, b.RemoveTree(fp.alpha))
			plan, err := b.Build()
			must(t, err)
			alphaBefore := tree(t, fp.alpha)

			if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrSnapshotMismatch) {
				t.Errorf("error = %v, want ErrSnapshotMismatch", err)
			}
			if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
				t.Errorf("a refused snapshot left a record or %v", snapshotDirs(t, e.p))
			}
			if got := tree(t, fp.alpha); !reflect.DeepEqual(got, alphaBefore) {
				t.Error("the folder changed")
			}
		})
	}
}

func TestOrphansReportsSnapshotsNothingPointsTo(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	snaps := e.p.SnapshotsDir()
	// An interrupted copy: no snapshot.json.
	e.home.WriteFile(rel(t, e, filepath.Join(snaps, "op-incomplete", "trees", "0", "SKILL.md")), "x\n")
	// Version 1 snapshots and other names are not the engine's.
	e.home.WriteFile(rel(t, e, filepath.Join(snaps, "20261122T100000.000000000Z-lock_alpha", "folder", "SKILL.md")), "x\n")
	// A complete snapshot the journal points to: a finished operation.
	run, err := eng.Begin(t.Context(), disablePlan(t, eng, fp))
	must(t, err)
	journaled := run.Record().SnapshotID
	must(t, run.Release())
	must(t, os.Remove(engine.RecordFile(e.p)))
	must(t, e.state.AppendJournal(domain.HistoryEntry{At: time.Now(), Action: domain.ActionApplied, Target: "x", SnapshotID: journaled}))
	// A complete snapshot nothing points to: interrupted before the record.
	e.clock.Advance(time.Second)
	run, err = eng.Begin(t.Context(), disablePlan(t, eng, fp))
	must(t, err)
	unclaimed := run.Record().SnapshotID
	must(t, run.Release())
	must(t, os.Remove(engine.RecordFile(e.p)))
	// A complete snapshot the pending record points to.
	e.clock.Advance(time.Second)
	run, err = eng.Begin(t.Context(), disablePlan(t, eng, fp))
	must(t, err)
	must(t, run.Release())

	got, err := e.clean().Orphans()
	must(t, err)
	want := []engine.Orphan{{SnapshotID: unclaimed, Complete: true}, {SnapshotID: "op-incomplete", Complete: false}} // by name
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orphans = %+v\nwant %+v", got, want)
	}
}

// rel is path relative to the fake home, for testsupport.Home helpers.
func rel(t *testing.T, e *env, path string) string {
	t.Helper()
	r, err := filepath.Rel(e.home.Root(), path)
	must(t, err)
	return filepath.ToSlash(r)
}
