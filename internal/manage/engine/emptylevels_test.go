package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
)

// noOwnLevels removes the empty parking zone of the fixture and
// ~/.agents/.skillhub: an operation leaves no own level empty, so a round
// trip starting with one would end without it.
func noOwnLevels(t *testing.T, e *env) {
	t.Helper()
	must(t, os.RemoveAll(e.home.Path(".agents/.skillhub")))
}

// parkAlpha applies a disable of alpha in e's home: its link goes and the
// folder moves to the parking zone.
func parkAlpha(t *testing.T, e *env, fp fixturePaths) {
	t.Helper()
	b := e.engine().NewBuilder(engine.Meta{Action: "disable"})
	must(t, b.Unlink(fp.link, fp.linkTarget))
	must(t, b.Move(fp.alpha, fp.parked))
	plan, err := b.Build()
	must(t, err)
	_, err = e.clean().Apply(t.Context(), plan)
	must(t, err)
}

// enableAlpha builds the enable of the parked alpha: the inverse moves.
func enableAlpha(t *testing.T, e *env, fp fixturePaths) engine.Plan {
	t.Helper()
	b := e.clean().NewBuilder(engine.Meta{Action: "enable"})
	must(t, b.Move(fp.parked, fp.alpha))
	must(t, b.Link(fp.link, fp.linkTarget))
	plan, err := b.Build()
	must(t, err)
	return plan
}

// TestAnOperationRemovesTheOwnLevelsItLeavesEmpty: enabling the last parked
// skill leaves the parking zone empty, and ~/.agents/.skillhub with it; the
// close removes both (D2). The same holds when the close is interrupted
// after its commit and Recover finishes it.
func TestAnOperationRemovesTheOwnLevelsItLeavesEmpty(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "enable", true: "enable, crash after commit, recover"}[interrupted], func(t *testing.T) {
			e, fp := newEnv(t)
			noOwnLevels(t, e)
			before := observeWorld(t, e)
			parkAlpha(t, e, fp)
			plan := enableAlpha(t, e, fp)
			eng := e.engine()
			if interrupted {
				e.files.afterWrite = func(_ int, line string) {
					if strings.HasPrefix(line, "AppendJournal ") {
						e.files.afterWrite = nil
						e.files.arm(0, crashBefore) // arm restarts the count
					}
				}
				if _, err := eng.Apply(t.Context(), plan); err == nil {
					t.Fatal("the crash after the commit was not reached")
				}
				res, err := e.clean().Recover(t.Context())
				must(t, err)
				if res.RolledBack {
					t.Fatalf("Recover rolled back a committed enable: %+v", res)
				}
			} else {
				_, err := eng.Apply(t.Context(), plan)
				must(t, err)
			}
			for _, dir := range []string{e.p.ParkedDir(), filepath.Dir(e.p.ParkedDir())} {
				if !absent(t, dir) {
					t.Errorf("%s is left empty after the enable", dir)
				}
			}
			assertWorld(t, "disable then enable", observeWorld(t, e), before)
		})
	}
}

// TestAnOperationKeepsTheOwnLevelsThatAreNotEmpty: another skill is still
// parked, so the parking zone and ~/.agents/.skillhub stay.
func TestAnOperationKeepsTheOwnLevelsThatAreNotEmpty(t *testing.T) {
	e, fp := newEnv(t)
	parkAlpha(t, e, fp)
	other := filepath.Join(e.p.ParkedDir(), "gamma")
	e.home.WriteFile(".agents/.skillhub/disabled/gamma/SKILL.md", "---\nname: gamma\n---\nGamma.\n")
	_, err := e.engine().Apply(t.Context(), enableAlpha(t, e, fp))
	must(t, err)
	if absent(t, filepath.Join(other, "SKILL.md")) {
		t.Error("the other parked skill is gone")
	}
}

// TestAnOperationNeverRemovesALevelThatIsNotItsOwn: moving the last skills
// out of the store leaves it empty, and it stays: the store is npx's, not a
// level of ~/.agents/.skillhub.
func TestAnOperationNeverRemovesALevelThatIsNotItsOwn(t *testing.T) {
	e, fp := newEnv(t)
	must(t, os.Remove(fp.link))
	b := e.engine().NewBuilder(engine.Meta{Action: "disable"})
	must(t, b.Move(fp.alpha, fp.parked))
	must(t, b.Move(fp.beta, filepath.Join(e.p.ParkedDir(), "beta")))
	plan, err := b.Build()
	must(t, err)
	_, err = e.engine().Apply(t.Context(), plan)
	must(t, err)
	if absent(t, e.p.StoreDir()) {
		t.Error("the operation removed the empty store")
	}
}
