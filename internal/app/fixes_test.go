package app_test

import (
	"errors"
	"os"
	"slices"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/manage/repair"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// A revision that is not applied is discarded: the staged copy and the
// staging levels it created go away, and the home is as before ResolveInstall
// (B2).
func TestDiscardRevisionLeavesTheHomeAsItWas(t *testing.T) {
	m := newMachine(t)
	m.publishTool()
	before := homeLines(t, m)
	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	if _, err := os.Stat(rev.Prepared.StagedPath); err != nil {
		t.Fatalf("ResolveInstall did not stage: %v", err)
	}
	must(t, m.app.DiscardRevision(rev))
	if d := homeDiff(before, homeLines(t, m)); d != "" {
		t.Fatalf("DiscardRevision left:\n%s", d)
	}
	// A staged path outside the install staging is never removed.
	other := m.h.WriteFile("keep/tidy/SKILL.md", "x")
	rev.Prepared.StagedPath = m.h.Path("keep/tidy")
	if err := m.app.DiscardRevision(rev); !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("DiscardRevision outside the staging = %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("DiscardRevision removed a path outside the staging")
	}
}

// Install, then undo: the copy the undo moves back into the staging is
// removed, and the home is as before the install was resolved (B2).
func TestUndoOfAnInstallEmptiesTheStaging(t *testing.T) {
	m := newMachine(t)
	m.publishTool()
	before := homeLines(t, m)
	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	p, err := m.app.PlanInstall(bg, rev, []string{"claude-code"})
	must(t, err)
	res := m.apply(p)
	u, err := m.app.PlanUndo(bg, res.SnapshotID)
	must(t, err)
	m.apply(u)
	if d := homeDiff(before, homeLines(t, m)); d != "" {
		t.Fatalf("undo of the install left:\n%s", d)
	}
}

// Every step of the lock names its entry: lockUnpark keeps it in the parked
// entry, and the removal of the last entry is lockRemoveLast (B4).
func TestLockStepsNameTheirEntry(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	m.apply(p)
	p, err = m.app.PlanEnable(bg, "agent-browser")
	must(t, err)
	if !hasStep(p, "lockUnpark", "agent-browser") {
		t.Fatalf("enable steps = %+v", p.Steps)
	}

	must(t, os.Remove(m.p.SkillsLockFile()))
	m.publishTool()
	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	p, err = m.app.PlanInstall(bg, rev, []string{"claude-code"})
	must(t, err)
	res := m.apply(p)
	u, err := m.app.PlanUndo(bg, res.SnapshotID)
	must(t, err)
	if !hasStep(u, "lockRemoveLast", "tidy") || hasStep(u, "lockRemove", "tidy") {
		t.Fatalf("undo steps = %+v", u.Steps)
	}
	for _, s := range append(slices.Clone(p.Steps), u.Steps...) {
		if (s.Kind == "lockAdd" || s.Kind == "lockRemoveLast") && s.Name == "" {
			t.Errorf("step %s has no entry name", s.Kind)
		}
	}
}

// A dry run tells a pending operation as the real run would (B5).
func TestADryRunTellsAPendingOperation(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	crashAfterBegin(t, m, p)
	q, err := m.app.PlanDisable(bg, "archify")
	must(t, err)
	req := q.Request
	req.DryRun = true
	if _, err := m.app.ApplyPlan(bg, req, q.PlanID); !errors.Is(err, app.ErrOperationPending) {
		t.Fatalf("dry run with a pending operation = %v, want ErrOperationPending", err)
	}
	if err := m.app.CheckPending(); !errors.Is(err, app.ErrOperationPending) {
		t.Fatalf("CheckPending = %v", err)
	}
	_, err = m.app.RecoverOperation(bg)
	must(t, err)
	if err := m.app.CheckPending(); err != nil {
		t.Fatalf("CheckPending after recover = %v", err)
	}
}

// --canonical on a repair that does not choose a canonical folder is a
// refusal, never ignored (B6).
func TestCanonicalOnlyForAdopt(t *testing.T) {
	m := newMachine(t)
	m.h.Symlink(".claude/skills/ghost", "../../.agents/skills/ghost")
	_, err := m.app.PlanRepair(bg, "broken_link:.claude/skills/ghost", string(repair.DeleteLink), "/x")
	if !errors.Is(err, app.ErrCanonicalNotUsed) {
		t.Fatalf("PlanRepair with --canonical = %v, want ErrCanonicalNotUsed", err)
	}
}

// The GitHub errors of resolving a source are typed refusals (B3).
func TestGitHubErrorsAreRefusals(t *testing.T) {
	tests := []struct {
		name  string
		input string
		fail  error
		kind  error
	}{
		{"no repository", "acme/none", nil, app.ErrRepoNotFound},
		{"no ref", "github.com/acme/tools/tree/nope/skills/tidy", nil, app.ErrRefNotFound},
		{"rate limit", "acme/tools", ports.ErrRateLimited, app.ErrRateLimited},
		{"not authenticated", "acme/tools", ports.ErrNotAuthenticated, app.ErrNotAuthenticated},
		{"no network", "acme/tools", errors.New("gh api repos/acme/tools exited with 1: dial tcp: lookup api.github.com: no such host"), app.ErrGitHubUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			m.publishTool()
			if tt.fail != nil {
				m.gh.Fail = func(fakes.Call) error { return tt.fail }
			}
			before := homeLines(t, m)
			_, err := m.app.ResolveInstall(bg, tt.input)
			var ref *app.RefusalError
			if !errors.As(err, &ref) || !errors.Is(err, tt.kind) {
				t.Fatalf("ResolveInstall = %v, want a refusal %v", err, tt.kind)
			}
			if d := homeDiff(before, homeLines(t, m)); d != "" {
				t.Errorf("a refused resolve left:\n%s", d)
			}
		})
	}
}
