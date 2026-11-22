package engine_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// scenario is one operation the crash and failure tests run, chosen so that
// together they use every kind of step: disable (unlink, move, lockPark,
// removeTree), its undo (restoreTree, lockUnpark, move, link), install with
// no lock (move, link, lockAdd from no lock), its undo (lockRemove that
// leaves no lock, unlink, move) and remove (removeTree, lockRemove).
type scenario struct {
	// setup builds a fresh world and the action to run on an engine.
	setup func(t *testing.T) (*env, func(eng *engine.Engine) error)
	// closeAction is the journal action that commits the operation.
	closeAction string
}

// firstStepWrite is the first write after Begin: the one after the first
// write of the record.
func (scenario) firstStepWrite(writes []string) int {
	i := slices.IndexFunc(writes, func(w string) bool {
		return strings.HasPrefix(w, "WriteFileAtomic ") && strings.HasSuffix(w, "operation-v2.json")
	})
	return i + 1
}

// gammaRaw is an entry as an install adds it: no ref.
const gammaRaw = `{"source":"owner/gamma","sourceType":"github","skillFolderHash":"3333333333333333333333333333333333333333"}`

// gammaPaths are where an install of gamma starts and ends.
type gammaPaths struct{ staged, store, link, linkTarget string }

func installSetup(t *testing.T) (*env, gammaPaths) {
	t.Helper()
	e, _ := newEnv(t)
	writeLock(t, e.p, nil)
	e.home.WriteFile(".agents/.skillhub/staging/gamma/SKILL.md", "---\nname: gamma\n---\nGamma.\n")
	e.home.WriteFileMode(".agents/.skillhub/staging/gamma/bin/tool", "#!/bin/sh\n", 0o750)
	return e, gammaPaths{
		staged:     e.home.Path(".agents/.skillhub/staging/gamma"),
		store:      e.home.Path(".agents/skills/gamma"),
		link:       e.home.Path(".claude/skills/gamma"),
		linkTarget: "../../.agents/skills/gamma",
	}
}

func installPlan(t *testing.T, eng *engine.Engine, gp gammaPaths) engine.Plan {
	t.Helper()
	b := eng.NewBuilder(engine.Meta{Action: "install", Targets: []string{"claude-code"}, Source: "owner/gamma"})
	must(t, b.Move(gp.staged, gp.store))
	must(t, b.Link(gp.link, gp.linkTarget))
	must(t, b.LockAdd("gamma", []byte(gammaRaw)))
	must(t, b.SetEffect(engine.Effect{SetManaged: []domain.ManagedSkill{{Key: "gamma", Targets: []string{"claude-code"}}}}))
	plan, err := b.Build()
	must(t, err)
	return plan
}

func scenarios() map[string]scenario {
	out := baseScenarios()
	replacementScenarios(out)
	requireScenarios(out)
	lockReplaceScenarios(out)
	return out
}

func baseScenarios() map[string]scenario {
	return map[string]scenario{
		"disable": {closeAction: "applied", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, fp := newEnv(t)
			plan := disableEffectPlan(t, e, e.engine(), fp)
			return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }
		}},
		"disable with no parking zone": {closeAction: "applied", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, fp := newEnv(t)
			must(t, os.RemoveAll(e.home.Path(".agents/.skillhub")))
			plan := disableEffectPlan(t, e, e.engine(), fp)
			return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }
		}},
		"undo of disable": {closeAction: "undone", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, fp := newEnv(t)
			out, err := e.clean().Apply(t.Context(), disableEffectPlan(t, e, e.clean(), fp))
			must(t, err)
			return e, func(eng *engine.Engine) error { _, err := eng.Undo(t.Context(), out.SnapshotID); return err }
		}},
		"install with no lock": {closeAction: "applied", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, gp := installSetup(t)
			plan := installPlan(t, e.engine(), gp)
			return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }
		}},
		"undo of install with no lock": {closeAction: "undone", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, gp := installSetup(t)
			out, err := e.clean().Apply(t.Context(), installPlan(t, e.clean(), gp))
			must(t, err)
			return e, func(eng *engine.Engine) error { _, err := eng.Undo(t.Context(), out.SnapshotID); return err }
		}},
		"remove": {closeAction: "applied", setup: func(t *testing.T) (*env, func(*engine.Engine) error) {
			e, fp := newEnv(t)
			lock, err := e.lock.Read()
			must(t, err)
			i := slices.IndexFunc(lock.Entries, func(en domain.LockEntry) bool { return en.Name == "beta" })
			b := e.engine().NewBuilder(engine.Meta{Action: "remove", Targets: []string{}})
			must(t, b.RemoveTree(fp.beta))
			must(t, b.LockRemove("beta", lock.Entries[i].Raw))
			plan, err := b.Build()
			must(t, err)
			return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }
		}},
	}
}
