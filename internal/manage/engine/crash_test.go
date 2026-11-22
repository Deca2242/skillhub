package engine_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/manage/engine"
)

// beginWrites is how many writes a successful Begin of disablePlan makes: two
// folder copies, snapshot.json and the record.
const beginWrites = 4

// TestAnInterruptedBeginNeverLeavesARecordOnAnIncompleteSnapshot fails every
// write of Begin in turn, as a plain error and as a crash before or after the
// write, and checks what a later process finds: a record only when its
// snapshot is complete and verifies, every other snapshot directory reported
// as an orphan, and nothing the plan touches changed.
func TestAnInterruptedBeginNeverLeavesARecordOnAnIncompleteSnapshot(t *testing.T) {
	for n := range beginWrites {
		for _, mode := range []faultMode{failOnce, crashBefore, crashAfter, doneButFailed} {
			t.Run(fmt.Sprintf("write %d %s", n, mode), func(t *testing.T) {
				e, fp := newEnv(t)
				eng := e.engine()
				plan := disablePlan(t, eng, fp)
				alpha, beta := tree(t, fp.alpha), tree(t, fp.beta)
				lock, err := os.ReadFile(e.p.SkillsLockFile())
				must(t, err)

				e.files.arm(n, mode)
				run, err := eng.Begin(t.Context(), plan)
				if !errors.Is(err, errInjected) {
					t.Fatalf("Begin error = %v, want the injected failure", err)
				}
				if run != nil {
					t.Fatal("a failed Begin returned a run")
				}
				if got := e.files.writeLog(); len(got) <= n {
					t.Fatalf("only %d writes happened: %q", len(got), got)
				}

				later := e.clean()
				rec, pending, err := later.PendingRecord()
				must(t, err)
				if pending {
					if _, err := later.VerifySnapshot(rec.SnapshotID); err != nil {
						t.Errorf("the record points to a snapshot that does not verify: %v", err)
					}
					if n != beginWrites-1 || (mode != crashAfter && mode != doneButFailed) {
						t.Errorf("a record exists after failing write %d", n)
					}
				}
				orphans, err := later.Orphans()
				must(t, err)
				for _, dir := range snapshotDirs(t, e.p) {
					if pending && dir == rec.SnapshotID {
						continue
					}
					if !slices.ContainsFunc(orphans, func(o engine.Orphan) bool { return o.SnapshotID == dir }) {
						t.Errorf("snapshot %s is on disk, unreferenced and not reported (orphans %+v)", dir, orphans)
					}
				}
				if (mode == failOnce || (mode == doneButFailed && n < beginWrites-1)) && (pending || len(snapshotDirs(t, e.p)) != 0) {
					t.Errorf("a failure the process saw left a record (%t) or snapshots %v", pending, snapshotDirs(t, e.p))
				}

				if !reflect.DeepEqual(tree(t, fp.alpha), alpha) || !reflect.DeepEqual(tree(t, fp.beta), beta) {
					t.Error("a failed Begin changed a skill folder")
				}
				if got, _ := os.ReadFile(e.p.SkillsLockFile()); !bytes.Equal(got, lock) {
					t.Error("a failed Begin changed the lock")
				}
				if target, err := os.Readlink(fp.link); err != nil || target != fp.linkTarget {
					t.Errorf("a failed Begin changed the link: %q, %v", target, err)
				}
				assertLockFree(t, e)

				// A later process starts the same plan only when no record is
				// pending; an orphan does not block it.
				next, err := later.Begin(t.Context(), plan)
				switch {
				case pending && !errors.Is(err, engine.ErrOperationPending):
					t.Errorf("Begin over a pending record: error = %v, want ErrOperationPending", err)
				case !pending && err != nil:
					t.Errorf("Begin after the failure: %v", err)
				}
				if next != nil {
					must(t, next.Release())
				}
			})
		}
	}
}

func TestAnInterruptedProgressUpdateKeepsAValidRecord(t *testing.T) {
	for _, mode := range []faultMode{failOnce, crashBefore, crashAfter, doneButFailed} {
		t.Run(mode.String(), func(t *testing.T) {
			e, fp := newEnv(t)
			eng := e.engine()
			run, err := eng.Begin(t.Context(), disablePlan(t, eng, fp))
			must(t, err)
			defer func() { must(t, run.Release()) }()

			e.files.arm(0, mode)
			if err := run.StepDone(0); !errors.Is(err, errInjected) {
				t.Fatalf("StepDone error = %v, want the injected failure", err)
			}
			rec, pending, err := e.clean().PendingRecord()
			if err != nil || !pending {
				t.Fatalf("record after the failure: %t, %v", pending, err)
			}
			want := -1
			if mode == crashAfter || mode == doneButFailed {
				want = 0 // written, then reported as failed
			}
			if rec.LastCompleted != want {
				t.Errorf("last completed on disk = %d, want %d", rec.LastCompleted, want)
			}
			if run.Record().LastCompleted != -1 {
				t.Errorf("last completed in memory = %d, want -1", run.Record().LastCompleted)
			}
			if _, err := e.clean().VerifySnapshot(rec.SnapshotID); err != nil {
				t.Errorf("snapshot: %v", err)
			}
		})
	}
}
