package engine_test

import (
	"errors"
	"reflect"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// samplePlan sets every field of a plan, so every leaf can be changed.
func samplePlan() engine.Plan {
	return engine.Plan{
		Meta: engine.Meta{
			Action: "install", Targets: []string{"claude-code", "codex"}, Canonical: "/h/.agents/skills/a",
			Source: "owner/repo", ReviewedTreeSHA: "abc",
		},
		LockSHA:     "lockhash",
		TouchesLock: true,
		Steps: []engine.Step{{
			Kind: engine.StepLockUnpark, Src: "/s", Dst: "/d", Path: "/p", Target: "t", Name: "n",
			Raw:   []byte(`{"a":1}`),
			Entry: &domain.ParkedEntry{Name: "n", Raw: []byte(`{"b":2}`), Before: "m", After: "o", LockSHA: "x"},
			Pre:   []engine.Image{{Path: "/p", Entry: "n", State: engine.StateTree, TreeSHA: "t1", Target: "l", Raw: []byte("r")}},
			Post:  []engine.Image{{Path: "/q", Entry: "o", State: engine.StateLink, TreeSHA: "t2", Target: "m", Raw: []byte("s")}},
		}},
	}
}

// leaves visits every exported leaf field below v: strings, booleans,
// integers and byte slices, through structs, pointers and slices.
func leaves(v reflect.Value, name string, visit func(name string, v reflect.Value)) {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if f.IsExported() {
				leaves(v.Field(i), name+"."+f.Name, visit)
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			leaves(v.Elem(), name, visit)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			visit(name, v)
			return
		}
		for i := range v.Len() {
			leaves(v.Index(i), name+"[]", visit)
		}
	case reflect.String, reflect.Bool, reflect.Int:
		visit(name, v)
	}
}

func change(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "x")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int:
		v.SetInt(v.Int() + 1)
	case reflect.Slice:
		v.SetBytes(append([]byte{'x'}, v.Bytes()...))
	}
}

func TestPlanIdentifierChangesWithEveryField(t *testing.T) {
	base, err := samplePlan().ComputeID()
	must(t, err)
	if again, _ := samplePlan().ComputeID(); again != base {
		t.Fatalf("the identifier is not stable: %s then %s", base, again)
	}
	var names []string
	leaves(reflect.ValueOf(samplePlan()), "Plan", func(name string, _ reflect.Value) { names = append(names, name) })
	if len(names) < 30 {
		t.Fatalf("only %d leaves found: %v", len(names), names)
	}
	for k, name := range names {
		if name == "Plan.ID" {
			continue
		}
		p := samplePlan()
		i := 0
		leaves(reflect.ValueOf(&p).Elem(), "Plan", func(_ string, v reflect.Value) {
			if i == k {
				change(v)
			}
			i++
		})
		id, err := p.ComputeID()
		must(t, err)
		if id == base {
			t.Errorf("changing %s keeps the identifier", name)
		}
	}
}

func TestPlanIdentifierChangesWithTheShapeOfItsLists(t *testing.T) {
	base, _ := samplePlan().ComputeID()
	edits := map[string]func(*engine.Plan){
		"one more target":       func(p *engine.Plan) { p.Targets = append(p.Targets, "pi") },
		"targets reordered":     func(p *engine.Plan) { p.Targets[0], p.Targets[1] = p.Targets[1], p.Targets[0] },
		"one more step":         func(p *engine.Plan) { p.Steps = append(p.Steps, p.Steps[0]) },
		"no parked entry":       func(p *engine.Plan) { p.Steps[0].Entry = nil },
		"one more precondition": func(p *engine.Plan) { p.Steps[0].Pre = append(p.Steps[0].Pre, p.Steps[0].Pre[0]) },
		"no raw":                func(p *engine.Plan) { p.Steps[0].Raw = nil },
	}
	for name, edit := range edits {
		p := samplePlan()
		edit(&p)
		if id, _ := p.ComputeID(); id == base {
			t.Errorf("%s keeps the identifier", name)
		}
	}
	p := samplePlan()
	p.ID = "anything"
	if id, _ := p.ComputeID(); id != base {
		t.Error("the identifier depends on the ID field itself")
	}
}

func TestBuiltPlanCarriesItsIdentifierAndChangesNothing(t *testing.T) {
	e, fp := newEnv(t)
	before := tree(t, e.home.Root())
	eng := e.engine()

	plan := disablePlan(t, eng, fp)
	again := disablePlan(t, eng, fp)

	if want, _ := plan.ComputeID(); plan.ID != want || plan.ID == "" {
		t.Errorf("plan id %q, its content hashes to %q", plan.ID, want)
	}
	if again.ID != plan.ID {
		t.Errorf("the same plan built twice has ids %s and %s", plan.ID, again.ID)
	}
	if got := tree(t, e.home.Root()); !reflect.DeepEqual(got, before) {
		t.Errorf("building a plan changed the disk:\n%v\nwant:\n%v", got, before)
	}
	if len(e.files.writeLog()) != 0 {
		t.Errorf("building a plan wrote: %v", e.files.writeLog())
	}
	must(t, eng.Check(plan))
}

func TestCheckRefusesAPlanWhoseContentDoesNotMatchItsIdentifier(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)

	tampered := plan
	tampered.Steps = append([]engine.Step(nil), plan.Steps...)
	tampered.Steps[1].Dst = fp.parked + "-elsewhere"
	if err := eng.Check(tampered); !errors.Is(err, engine.ErrPlanChanged) {
		t.Errorf("a step changed after approval: error = %v, want ErrPlanChanged", err)
	}

	// Rehashed, but the images no longer name the step's objects.
	tampered.ID, _ = tampered.ComputeID()
	if err := eng.Check(tampered); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("a step whose images name other objects: error = %v, want ErrInvalidStep", err)
	}

	regenerated := plan
	regenerated.Targets = []string{"claude-code", "codex"}
	if err := eng.Check(regenerated); !errors.Is(err, engine.ErrPlanChanged) {
		t.Errorf("other targets with the shown id: error = %v, want ErrPlanChanged", err)
	}
}

func TestCheckRefusesAPlanWhoseStepsDoNotFollowEachOther(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove"})
	must(t, b.Move(fp.alpha, fp.parked))
	must(t, b.LockPark("alpha"))
	must(t, b.LockUnpark(domain.ParkedEntry{Name: "alpha", Raw: []byte(`{"source":"owner/alpha"}`), Before: "beta"}))
	plan, err := b.Build()
	must(t, err)
	must(t, eng.Check(plan))

	// The third step expects the entry the second one takes out.
	plan.Steps = append([]engine.Step(nil), plan.Steps...)
	plan.Steps[2].Pre = []engine.Image{{Entry: "alpha", State: engine.StateEntry, Raw: []byte(`{"source":"x"}`)}}
	plan.ID, err = plan.ComputeID()
	must(t, err)
	if err := eng.Check(plan); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("error = %v, want ErrInvalidStep", err)
	}
	if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Begin error = %v, want ErrInvalidStep", err)
	}
}
