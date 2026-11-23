package app_test

import (
	"slices"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
)

func TestSkillActivity(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	alpha := domain.Skill{
		ID:   "lock:alpha",
		Name: "alpha",
		Locations: []domain.Location{
			{AgentID: domain.StoreAgentID, Path: "/home/u/.agents/skills/alpha"},
			{AgentID: "claude-code", Path: "/home/u/.claude/skills/alpha"},
		},
	}
	beta := domain.Skill{
		ID:   "lock:beta",
		Name: "beta",
		Locations: []domain.Location{
			{AgentID: domain.StoreAgentID, Path: "/home/u/.agents/skills/beta"},
		},
	}
	unmanagedSameName := domain.Skill{
		ID:   "dir:.claude/skills/alpha",
		Name: "alpha",
		Locations: []domain.Location{
			{AgentID: "claude-code", Path: "/home/u/.claude/skills/alpha"},
		},
	}
	cloneChild := domain.Skill{
		ID:   "git:.claude/skills/my-repo/skills/sub",
		Name: "sub",
		Origin: domain.Origin{
			Kind:     domain.OriginGitClone,
			GitClone: &domain.GitCloneOrigin{Root: "/home/u/.claude/skills/my-repo"},
		},
	}

	history := []domain.HistoryEntry{
		{At: t0, Action: domain.ActionApplied, Target: "lock:alpha", SnapshotID: "s1"},
		{At: t0.Add(5 * time.Minute), Action: domain.ActionUndone, Target: "lock:alpha", SnapshotID: "s1"},
		{At: t0.Add(10 * time.Minute), Action: domain.ActionApplied, Target: "v2:disable", SnapshotID: "v2-1"},
		{At: t0.Add(15 * time.Minute), Action: domain.ActionUndone, Target: "v2:undo", SnapshotID: "v2-2"},
		{At: t0.Add(20 * time.Minute), Action: domain.ActionFailed, Target: "v2:link", SnapshotID: "v2-3"},
		{At: t0.Add(25 * time.Minute), Action: domain.ActionFailed, Target: "lock:beta", SnapshotID: "s2"},
		{At: t0.Add(30 * time.Minute), Action: domain.ActionApplied, Target: "git:.claude/skills/my-repo", SnapshotID: "s3"},
	}
	ops := map[string]app.OperationView{
		"v2-1": {Action: string(app.ActionDisable), Keys: []string{"alpha"}},
		"v2-2": {Action: string(app.ActionDisable), Keys: []string{"alpha"}, Undo: true},
		"v2-3": {Action: string(app.ActionLink), Keys: []string{"alpha"}},
	}

	t.Run("returns v1 and v2 entries newest first", func(t *testing.T) {
		got := app.SkillActivity(history, ops, alpha)
		if len(got) != 5 {
			t.Fatalf("len(SkillActivity(alpha)) = %d, want 5: %+v", len(got), got)
		}
		wantActions := []domain.HistoryAction{
			domain.ActionFailed,
			domain.ActionUndone,
			domain.ActionApplied,
			domain.ActionUndone,
			domain.ActionApplied,
		}
		for i, want := range wantActions {
			if got[i].Action != want {
				t.Errorf("got[%d].Action = %q, want %q", i, got[i].Action, want)
			}
			if i > 0 && got[i].At.After(got[i-1].At) {
				t.Errorf("got[%d].At (%v) is after got[%d].At (%v)", i, got[i].At, i-1, got[i-1].At)
			}
		}
		if !got[0].V2 || got[0].Operation.Action != string(app.ActionLink) {
			t.Errorf("got[0] = %+v, want v2 link failure", got[0])
		}
		if got[3].V2 || got[4].V2 {
			t.Errorf("v1 entries should have V2=false: %+v", got[3:5])
		}
	})

	t.Run("isolates activity per skill", func(t *testing.T) {
		gotBeta := app.SkillActivity(history, ops, beta)
		if len(gotBeta) != 1 || gotBeta[0].Target != "lock:beta" || gotBeta[0].Action != domain.ActionFailed {
			t.Fatalf("SkillActivity(beta) = %+v, want single failed lock:beta entry", gotBeta)
		}

		gotUnmanaged := app.SkillActivity(history, ops, unmanagedSameName)
		if len(gotUnmanaged) != 0 {
			t.Fatalf("SkillActivity(unmanagedSameName) = %+v, want empty", gotUnmanaged)
		}
	})

	t.Run("matches multi-skill git clone target", func(t *testing.T) {
		got := app.SkillActivity(history, ops, cloneChild)
		if len(got) != 1 || got[0].Target != "git:.claude/skills/my-repo" {
			t.Fatalf("SkillActivity(cloneChild) = %+v, want clone update", got)
		}
	})

	t.Run("returns empty for skill without activity", func(t *testing.T) {
		gamma := domain.Skill{ID: "lock:gamma", Name: "gamma"}
		if got := app.SkillActivity(history, ops, gamma); len(got) != 0 {
			t.Fatalf("SkillActivity(gamma) = %+v, want empty", got)
		}
	})
}

func TestAppOperations(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	res := m.apply(p)
	if res.SnapshotID == "" {
		t.Fatal("expected SnapshotID from PlanDisable")
	}

	hist, err := m.app.History()
	must(t, err)
	// Add v1 entry, v2 entry without snapshot, duplicate snapshot, and missing snapshot.
	hist = append(hist,
		domain.HistoryEntry{At: t0, Action: domain.ActionApplied, Target: "lock:alpha", SnapshotID: "v1-snap"},
		domain.HistoryEntry{At: t0, Action: domain.ActionFailed, Target: "v2:link", SnapshotID: ""},
		domain.HistoryEntry{At: t0, Action: domain.ActionApplied, Target: "v2:disable", SnapshotID: res.SnapshotID},
		domain.HistoryEntry{At: t0, Action: domain.ActionFailed, Target: "v2:remove", SnapshotID: "missing-snap"},
	)

	got := m.app.Operations(bg, hist)
	if len(got) != 1 {
		t.Fatalf("len(Operations) = %d, want 1: %+v", len(got), got)
	}
	want, err := m.app.Operation(bg, res.SnapshotID)
	must(t, err)
	op, ok := got[res.SnapshotID]
	if !ok {
		t.Fatalf("Operations missing %q: %+v", res.SnapshotID, got)
	}
	if op.Action != want.Action || op.Undo != want.Undo || !slices.Equal(op.Keys, want.Keys) {
		t.Errorf("Operations[%q] = %+v, want %+v", res.SnapshotID, op, want)
	}
}
