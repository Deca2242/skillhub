package engine_test

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
)

// betaReplaced is beta's entry as a reinstall leaves it: the same keys in the
// same order, an unknown key kept, the hash and updatedAt new.
const betaReplaced = `{"source":"owner/beta","sourceType":"github","skillFolderHash":"4444444444444444444444444444444444444444","x-custom":1,"updatedAt":"2026-11-22T10:00:00.000Z"}`

// betaRaw is beta's entry as the lock store reads it.
func betaRaw(t *testing.T, e *env) []byte {
	t.Helper()
	lock, err := e.lock.Read()
	must(t, err)
	i := slices.IndexFunc(lock.Entries, func(en domain.LockEntry) bool { return en.Name == "beta" })
	if i < 0 {
		t.Fatal("no beta in the lock")
	}
	return lock.Entries[i].Raw
}

// reinstallSetup stages gamma for a move into the store.
func reinstallSetup(t *testing.T) (*env, gammaPaths) {
	t.Helper()
	e, _ := newEnv(t)
	e.home.WriteFile(".agents/.skillhub/staging/gamma/SKILL.md", "---\nname: gamma\n---\nGamma.\n")
	return e, gammaPaths{staged: e.home.Path(".agents/.skillhub/staging/gamma"), store: e.home.Path(".agents/skills/gamma")}
}

// reinstallPlan moves gamma into the store and replaces beta's entry in its
// place.
func reinstallPlan(t *testing.T, e *env, eng *engine.Engine, gp gammaPaths) engine.Plan {
	t.Helper()
	b := eng.NewBuilder(engine.Meta{Action: "reinstall", Targets: []string{}})
	must(t, b.Move(gp.staged, gp.store))
	must(t, b.LockReplace("beta", betaRaw(t, e), []byte(betaReplaced)))
	plan, err := b.Build()
	must(t, err)
	return plan
}

// lockReplaceScenarios adds the reinstall to the crash and failure
// matrices: its lockReplace, and the lockReplace of its undo.
func lockReplaceScenarios(out map[string]scenario) {
	out["reinstall"] = scenario{closeAction: "applied", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
		e, gp := reinstallSetup(t)
		plan := reinstallPlan(t, e, e.engine(), gp)
		return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }
	}}
	out["undo of reinstall"] = scenario{closeAction: "undone", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
		e, gp := reinstallSetup(t)
		applied, err := e.clean().Apply(t.Context(), reinstallPlan(t, e, e.clean(), gp))
		must(t, err)
		return e, func(eng *engine.Engine) error { _, err := eng.Undo(t.Context(), applied.SnapshotID); return err }
	}}
}

// TestLockReplaceKeepsThePositionAndUndoGivesTheBytesBack: the entry is
// replaced where it is, with exactly the new bytes, and undo writes the old
// ones back: the lock is identical byte for byte.
func TestLockReplaceKeepsThePositionAndUndoGivesTheBytesBack(t *testing.T) {
	e, gp := reinstallSetup(t)
	lock0, err := os.ReadFile(e.p.SkillsLockFile())
	must(t, err)
	plan := reinstallPlan(t, e, e.engine(), gp)
	if s := plan.Steps[1]; s.Kind != engine.StepLockReplace || !bytes.Equal(s.Pre[0].Raw, betaRaw(t, e)) {
		t.Fatalf("step = %+v, want a lockReplace whose preimage is beta's bytes", s)
	}

	out, err := e.engine().Apply(t.Context(), plan)
	must(t, err)
	lock, err := os.ReadFile(e.p.SkillsLockFile())
	must(t, err)
	want := strings.Replace(string(lock0), `"beta": {
      "source": "owner/beta",
      "sourceType": "github",
      "skillFolderHash": "2222222222222222222222222222222222222222"
    }`, `"beta": {
      "source": "owner/beta",
      "sourceType": "github",
      "skillFolderHash": "4444444444444444444444444444444444444444",
      "x-custom": 1,
      "updatedAt": "2026-11-22T10:00:00.000Z"
    }`, 1)
	if string(lock) != want {
		t.Errorf("lock after the replace:\n%s\nwant:\n%s", lock, want)
	}

	_, err = e.clean().Undo(t.Context(), out.SnapshotID)
	must(t, err)
	if got, err := os.ReadFile(e.p.SkillsLockFile()); err != nil || !bytes.Equal(got, lock0) {
		t.Errorf("lock after undo:\n%s\nwant:\n%s", got, lock0)
	}
}

// TestLockReplaceNeverTouchesARef: replacing must keep the ref exactly; a
// replacement that adds, drops or changes one is refused when planned, and
// an entry that is not the expected one is a changed precondition.
func TestLockReplaceNeverTouchesARef(t *testing.T) {
	e, _ := newEnv(t)
	lock, err := e.lock.Read()
	must(t, err)
	alpha := lock.Entries[slices.IndexFunc(lock.Entries, func(en domain.LockEntry) bool { return en.Name == "alpha" })].Raw
	cases := []struct {
		name, key string
		old, raw  []byte
	}{
		{"a ref added", "beta", betaRaw(t, e), []byte(`{"source":"owner/beta","ref":"v2"}`)},
		{"a ref dropped", "alpha", alpha, []byte(`{"source":"owner/alpha","sourceType":"github"}`)},
		{"a ref changed", "alpha", alpha, []byte(`{"source":"owner/alpha","ref":"v2"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := e.engine().NewBuilder(engine.Meta{Action: "reinstall"})
			_ = b.LockReplace(tc.key, tc.old, tc.raw)
			if _, err := b.Build(); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("Build() error = %v, want ErrInvalidStep", err)
			}
		})
	}
	b := e.engine().NewBuilder(engine.Meta{Action: "reinstall"})
	_ = b.LockReplace("beta", []byte(`{"source":"owner/beta"}`), []byte(betaReplaced))
	if _, err := b.Build(); err == nil || errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Build() with another entry than the expected one: %v, want a precondition error", err)
	}
}

// TestAForgedLockReplaceIsRefused: a plan that hashes right but whose
// lockReplace adds a ref, or does not leave the bytes it says, is refused by
// Check and Begin, whatever the builder would have done.
func TestAForgedLockReplaceIsRefused(t *testing.T) {
	e, gp := reinstallSetup(t)
	eng := e.engine()
	good := reinstallPlan(t, e, eng, gp)
	ref, err := orderedjson.ParseValue([]byte(`{"source":"owner/beta","sourceType":"github","ref":"v2"}`))
	must(t, err)
	withRef := ref.Bytes() // canonical, so only the ref is wrong
	edits := map[string]func(p *engine.Plan){
		"a replace that adds a ref": func(p *engine.Plan) {
			p.Steps[1].Raw = withRef
			p.Steps[1].Post[0].Raw = withRef
		},
		"a replace whose postimage is not its bytes": func(p *engine.Plan) {
			p.Steps[1].Post[0].Raw = []byte(`{"source":"owner/beta"}`)
		},
		"a replace of an entry that is not there": func(p *engine.Plan) {
			p.Steps[1].Pre[0] = engine.Image{Entry: "beta", State: engine.StateAbsent}
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			bad := clonePlan(t, good)
			edit(&bad)
			bad = reID(t, bad)
			if err := eng.Check(bad); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("Check error = %v, want ErrInvalidStep", err)

			}
			if _, err := eng.Begin(t.Context(), bad); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("Begin error = %v, want ErrInvalidStep", err)
			}
			if recordExists(t, e.p) {
				t.Error("a refused plan left a record")
			}
		})
	}
	if err := eng.Check(good); err != nil {
		t.Fatalf("the good plan: %v", err)
	}
}
