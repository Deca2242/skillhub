package engine_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// reID recomputes a plan's identifier after an edit, as a forged record
// would.
func reID(t *testing.T, p engine.Plan) engine.Plan {
	t.Helper()
	id, err := p.ComputeID()
	must(t, err)
	p.ID = id
	return p
}

// clonePlan deep-copies a plan so an edit does not reach the original.
func clonePlan(t *testing.T, p engine.Plan) engine.Plan {
	t.Helper()
	data, err := json.Marshal(p)
	must(t, err)
	var out engine.Plan
	must(t, json.Unmarshal(data, &out))
	return out
}

// TestIncoherentStepsAreRefusedEvenWithARecomputedIdentifier covers advisory
// A3 of the W2a verifier: shape alone accepted these.
func TestIncoherentStepsAreRefusedEvenWithARecomputedIdentifier(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	good := disablePlan(t, eng, fp)
	// disablePlan: 0 unlink, 1 move, 2 lockPark, 3 removeTree.
	edits := map[string]func(p *engine.Plan){
		"an unlink whose target is not its preimage's": func(p *engine.Plan) { p.Steps[0].Target = "../elsewhere" },
		"an unlink whose preimage is not a link":       func(p *engine.Plan) { p.Steps[0].Pre[0] = engine.Image{Path: fp.link, State: engine.StateAbsent} },
		"a move with a forged tree in its postimage": func(p *engine.Plan) {
			p.Steps[1].Post[1].TreeSHA = "0000000000000000000000000000000000000000"
		},
		"a move whose destination is not free": func(p *engine.Plan) {
			p.Steps[1].Pre[1] = engine.Image{Path: fp.parked, State: engine.StateTree, TreeSHA: p.Steps[1].Pre[0].TreeSHA}
		},
		"a move that leaves its source": func(p *engine.Plan) { p.Steps[1].Post[0] = p.Steps[1].Pre[0] },
		"a park whose postimage keeps the entry": func(p *engine.Plan) {
			p.Steps[2].Post[0] = p.Steps[2].Pre[0]
		},
		"a park that also sets raw bytes":      func(p *engine.Plan) { p.Steps[2].Raw = []byte(`{}`) },
		"a removeTree that sets a link target": func(p *engine.Plan) { p.Steps[3].Target = "x" },
		"a removeTree whose preimage is a link": func(p *engine.Plan) {
			p.Steps[3].Pre[0] = engine.Image{Path: fp.beta, State: engine.StateLink, Target: "x"}
		},
		"a tree image with a link target": func(p *engine.Plan) {
			p.Steps[3].Pre[0].Target = "x"
		},
		"an image of foreign state": func(p *engine.Plan) { p.Steps[3].Pre[0] = engine.Image{Path: fp.beta, State: engine.StateOther} },
		"an effect that takes its entry from an unlink": func(p *engine.Plan) {
			p.Effect = engine.Effect{SetParked: []engine.ParkedEffect{{Skill: domain.ParkedSkill{Key: "alpha"}, EntryStep: 0}}}
		},
		"an effect key twice": func(p *engine.Plan) {
			p.Effect = engine.Effect{DropManaged: []string{"alpha"}, SetManaged: []domain.ManagedSkill{{Key: "alpha"}}}
		},
		"an undo of a snapshot id that climbs out": func(p *engine.Plan) { p.Undoes = "op-../../x" },
		"a removal of an entry with a ref that leaves no lock": func(p *engine.Plan) {
			p.Steps[2].Kind = engine.StepLockRemove
			p.Steps[2].Raw = p.Steps[2].Pre[0].Raw
			p.Steps[2].Post[0] = engine.Image{Entry: "alpha", State: engine.StateNoLock}
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
			if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
				t.Error("a refused plan left a record or a snapshot")
			}
		})
	}
	if err := eng.Check(good); err != nil {
		t.Fatalf("the good plan: %v", err)
	}
}

// TestARecordWithAnIncoherentStepIsUnsupported forges a pending record whose
// plan hashes right but says one thing and does another.
func TestARecordWithAnIncoherentStepIsUnsupported(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	run, err := eng.Begin(t.Context(), disablePlan(t, eng, fp))
	must(t, err)
	must(t, run.Release())
	rec := run.Record()
	rec.Plan = clonePlan(t, rec.Plan)
	rec.Plan.Steps[0].Target = "../elsewhere"
	rec.Plan = reID(t, rec.Plan)
	rec.PlanID = rec.Plan.ID
	data, err := json.Marshal(rec)
	must(t, err)
	must(t, os.WriteFile(engine.RecordFile(e.p), data, 0o600))
	before := tree(t, e.home.Root())

	if _, _, err := e.clean().PendingRecord(); !errors.Is(err, engine.ErrUnsupportedRecord) {
		t.Errorf("PendingRecord error = %v, want ErrUnsupportedRecord", err)
	}
	if _, err := e.clean().Recover(t.Context()); !errors.Is(err, engine.ErrUnsupportedRecord) {
		t.Errorf("Recover error = %v, want ErrUnsupportedRecord", err)
	}
	if after := tree(t, e.home.Root()); !mapsEqual(before, after) {
		t.Errorf("a refused record changed the disk: %v", diffKeys(before, after))
	}

	// A record whose taken entry is not the one its step took.
	good := run.Record()
	good.LastCompleted = 2
	good.Parked = map[int]domain.ParkedEntry{2: {Name: "beta", Raw: []byte(`{}`)}}
	data, err = json.Marshal(good)
	must(t, err)
	must(t, os.WriteFile(engine.RecordFile(e.p), data, 0o600))
	if _, _, err := e.clean().PendingRecord(); !errors.Is(err, engine.ErrUnsupportedRecord) {
		t.Errorf("a forged taken entry: error = %v, want ErrUnsupportedRecord", err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// diffKeys lists the paths whose description differs.
func diffKeys(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if b[k] != v {
			out = append(out, k+": "+v+" => "+b[k])
		}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k+": (none) => "+v)
		}
	}
	slices.Sort(out)
	return out
}

// TestASnapshotThatDisagreesWithItsPlanIsUnsupported edits snapshot.json so
// that a link, an entry or the lock's existence no longer match the plan the
// snapshot was taken for.
func TestASnapshotThatDisagreesWithItsPlanIsUnsupported(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	run, err := eng.Begin(t.Context(), disablePlan(t, eng, fp))
	must(t, err)
	must(t, run.Release())
	id := run.Record().SnapshotID
	meta := filepath.Join(eng.SnapshotDir(id), "snapshot.json")
	good, err := os.ReadFile(meta)
	must(t, err)

	edits := map[string]func(m map[string]any){
		"a link with another target": func(m map[string]any) {
			m["links"].([]any)[0].(map[string]any)["target"] = "../elsewhere"
		},
		"a link the plan does not touch": func(m map[string]any) {
			m["links"] = append(m["links"].([]any), map[string]any{"path": fp.beta + "-x", "target": "y"})
		},
		"an entry with other bytes": func(m map[string]any) {
			m["entries"].([]any)[0].(map[string]any)["raw"] = "e30="
		},
		"no lock before":              func(m map[string]any) { m["lock_exists"] = false },
		"lock keys without the entry": func(m map[string]any) { m["lock_keys"] = []any{"beta"} },
		"a tree at another path": func(m map[string]any) {
			m["trees"].([]any)[0].(map[string]any)["path"] = fp.beta + "-x"
		},
		"a plan that is not the record's": func(m map[string]any) { m["plan_id"] = strings.Repeat("0", 64) },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			must(t, json.Unmarshal(good, &m))
			edit(m)
			data, err := json.Marshal(m)
			must(t, err)
			must(t, os.WriteFile(meta, data, 0o644))
			if _, err := e.clean().ReadSnapshot(id); !errors.Is(err, engine.ErrUnsupportedRecord) {
				t.Errorf("ReadSnapshot error = %v, want ErrUnsupportedRecord", err)
			}
		})
	}
	must(t, os.WriteFile(meta, good, 0o644))
	if _, err := e.clean().ReadSnapshot(id); err != nil {
		t.Errorf("the original snapshot: %v", err)
	}
}
