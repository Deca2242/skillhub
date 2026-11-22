package updates_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/updates"
)

// pendV2 writes a version 2 operation record, as an interrupted operation of
// the version 2 engine leaves it.
func (f *fixture) pendV2() {
	f.t.Helper()
	path := engine.RecordFile(f.p)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"format":"skillhub.operation","version":2}`+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// homeTree records every directory, file and link of the fake home, byte for
// byte: the store, the lock, skillhub's state, journal, staging, snapshots
// and caches. The state lock file is left out: taking the lock is not a
// write of state.
func (f *fixture) homeTree() map[string]string {
	f.t.Helper()
	root := f.home.Root()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == f.p.StateLockFile() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			out[rel] = "link " + target
			return err
		case info.IsDir():
			out[rel] = fmt.Sprintf("dir %v", info.Mode())
		default:
			data, err := os.ReadFile(p)
			out[rel] = fmt.Sprintf("%v %s", info.Mode(), data)
			return err
		}
		return nil
	})
	if err != nil {
		f.t.Fatalf("walk the home: %v", err)
	}
	return out
}

func (f *fixture) assertHomeUnchanged(before map[string]string, flow string) {
	f.t.Helper()
	after := f.homeTree()
	if maps.Equal(before, after) {
		return
	}
	for k, v := range after {
		if before[k] != v {
			f.t.Errorf("%s wrote %s:\n  got  %.120q\n  want %.120q", flow, k, v, before[k])
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			f.t.Errorf("%s removed %s", flow, k)
		}
	}
}

func TestEveryVersion1FlowRefusesAPendingVersion2Operation(t *testing.T) {
	ctx := context.Background()
	flows := []struct {
		name string
		// setup prepares the flow and returns the call to refuse.
		setup func(f *fixture) func() error
	}{
		{"check", func(f *fixture) func() error {
			f.standard()
			f.publish("main", v2Files)
			return func() error { _, err := f.svc.Check(ctx); return err }
		}},
		{"apply", func(f *fixture) func() error {
			f.standard()
			c := f.staged()
			return func() error {
				res, err := f.svc.Apply(ctx, approval(c))
				if res.Outcome != updates.OutcomeRejected {
					f.t.Errorf("apply outcome = %s, want rejected", res.Outcome)
				}
				return err
			}
		}},
		{"apply all", func(f *fixture) func() error {
			f.standard()
			c := f.staged()
			return func() error {
				out := f.svc.ApplyAll(ctx, []updates.Approval{approval(c)})
				if len(out) != 1 || out[0].Outcome != updates.OutcomeRejected {
					f.t.Fatalf("apply all = %+v, want one rejected", out)
				}
				return out[0].Err
			}
		}},
		{"skip", func(f *fixture) func() error {
			f.standard()
			c := f.staged()
			return func() error { return f.svc.Skip(ctx, approval(c)) }
		}},
		{"undo", func(f *fixture) func() error {
			f.applied()
			return func() error { _, err := f.svc.Undo(ctx, updates.UndoRequest{Target: target}); return err }
		}},
		{"recover a version 1 operation", func(f *fixture) func() error {
			f.interruptAfterTheLockWrite()
			return func() error { _, err := f.svc.Recover(ctx); return err }
		}},
		{"recover with nothing of version 1", func(f *fixture) func() error {
			f.standard()
			return func() error { _, err := f.svc.Recover(ctx); return err }
		}},
	}
	for _, flow := range flows {
		t.Run(flow.name, func(t *testing.T) {
			f := newFixture(t)
			call := flow.setup(f)
			f.pendV2()
			before := f.homeTree()

			err := call()

			if !errors.Is(err, updates.ErrV2OperationPending) {
				t.Fatalf("%s = %v, want ErrV2OperationPending", flow.name, err)
			}
			f.assertHomeUnchanged(before, flow.name)
		})
	}
}

func TestVersion1FlowsRunOnceTheVersion2RecordIsGone(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	f.pendV2()
	if _, err := f.svc.Apply(context.Background(), approval(c)); !errors.Is(err, updates.ErrV2OperationPending) {
		t.Fatalf("Apply = %v, want ErrV2OperationPending", err)
	}
	if err := os.Remove(engine.RecordFile(f.p)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Apply(context.Background(), approval(c)); err != nil {
		t.Fatalf("Apply after the record is gone: %v", err)
	}
}

func TestGuardV2OperationReportsTheRecord(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.GuardV2Operation(); err != nil {
		t.Fatalf("GuardV2Operation without a record = %v", err)
	}
	f.pendV2()
	if err := f.svc.GuardV2Operation(); !errors.Is(err, updates.ErrV2OperationPending) {
		t.Fatalf("GuardV2Operation with a record = %v, want ErrV2OperationPending", err)
	}
}
