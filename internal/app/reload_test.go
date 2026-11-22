package app_test

import (
	"slices"
	"testing"

	"skillhub/internal/app"
)

// shown are the harnesses the plan's preview lists for the links it removes
// and creates.
func shown(p app.Proposal) []string {
	var out []string
	if p.Toggle == nil {
		return nil
	}
	for _, s := range p.Toggle.Skills {
		for _, l := range append(slices.Clone(s.RemovedLinks), s.CreatedLinks...) {
			out = append(out, l.Harnesses...)
		}
	}
	return out
}

// checkReload: the reload list holds every harness the preview shows and
// every one of want.
func checkReload(t *testing.T, what string, p app.Proposal, want ...string) {
	t.Helper()
	for _, h := range append(shown(p), want...) {
		if !slices.Contains(p.Reload, h) {
			t.Errorf("%s: reload %v lacks %s (the plan shows %v)", what, p.Reload, h, shown(p))
		}
	}
}

// Unlinking from Claude Code and linking back change ~/.claude/skills, which
// OpenCode reads too: both are to reload, never fewer than the plan shows
// (L65).
func TestTheReloadListHasEveryHarnessReadingTheChangedDirectory(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanUnlink(bg, "archify", "claude-code")
	must(t, err)
	checkReload(t, "unlink", p, "claude-code", "opencode")
	m.apply(p)

	p, err = m.app.PlanLink(bg, "archify", "claude-code")
	must(t, err)
	checkReload(t, "link", p, "claude-code", "opencode")
}

// adopt-off removes the links in ~/.claude/skills/OFF/: Claude Code is among
// its targets and on the reload list (L65).
func TestAdoptOffTargetsTheHarnessesWhoseLinksItRemoves(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanAdoptOff(bg)
	must(t, err)
	if !slices.Contains(p.Targets, "claude-code") {
		t.Errorf("adopt-off targets %v lack claude-code", p.Targets)
	}
	checkReload(t, "adopt-off", p, "claude-code")
}
