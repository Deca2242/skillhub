package app_test

import (
	"os"
	"slices"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// saveResults stores results as the last check results.
func (f *fixture) saveResults(results map[string]domain.TargetResult) {
	f.t.Helper()
	st := f.persisted()
	st.Results = results
	if err := f.state.Save(st); err != nil {
		f.t.Fatal(err)
	}
}

func TestSkillsShowTheLastCheckResult(t *testing.T) {
	tests := []struct {
		name      string
		result    *domain.TargetResult
		wantState domain.State
		wantIssue bool
	}{
		{name: "never checked", wantState: domain.StateUnchecked},
		{name: "up to date", result: &domain.TargetResult{State: domain.StateUpToDate, CheckedAt: t0}, wantState: domain.StateUpToDate},
		{name: "update available", result: &domain.TargetResult{State: domain.StateUpdateAvailable, CheckedAt: t0}, wantState: domain.StateUpdateAvailable},
		{name: "check failed", result: &domain.TargetResult{State: domain.StateCheckFailed, CheckedAt: t0, Detail: "boom"}, wantState: domain.StateCheckFailed},
		{
			name:      "removed upstream",
			result:    &domain.TargetResult{State: domain.StateUpToDate, CheckedAt: t0, RemovedUpstream: true},
			wantState: domain.StateUpToDate, wantIssue: true,
		},
		{name: "a state a check never stores", result: &domain.TargetResult{State: domain.StateDisabled, CheckedAt: t0}, wantState: domain.StateUnchecked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.installLock(map[string]map[string]string{"alpha": v1})
			if tt.result != nil {
				f.saveResults(map[string]domain.TargetResult{"lock:alpha": *tt.result})
			}

			skills, err := f.app.Skills(ctx)
			if err != nil {
				t.Fatal(err)
			}

			alpha := skillByID(t, skills, "lock:alpha")
			if alpha.State != tt.wantState {
				t.Errorf("state = %s, want %s", alpha.State, tt.wantState)
			}
			if got := slices.Contains(alpha.Issues, domain.IssueRemovedUpstream); got != tt.wantIssue {
				t.Errorf("issues = %v, want removed_upstream: %v", alpha.Issues, tt.wantIssue)
			}
		})
	}
}

func TestAPendingCandidateWinsOverAFailedCheck(t *testing.T) {
	f := newFixture(t)
	f.standard()
	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	f.gh.Fail = func(fakes.Call) error { return ports.ErrRateLimited }
	if res, err := f.app.Check(ctx, app.CheckOptions{}); err != nil || !res.Failed {
		t.Fatalf("second check = %+v, %v; want a failed check", res, err)
	}
	if got := f.persisted().Results["lock:alpha"].State; got != domain.StateCheckFailed {
		t.Fatalf("stored result = %s, want check_failed", got)
	}

	skills, err := f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got := skillByID(t, skills, "lock:alpha").State; got != domain.StateUpdateAvailable {
		t.Errorf("state = %s, want update_available from the pending candidate", got)
	}
}

func TestResultsDoNotChangeSkillsThatAreNotChecked(t *testing.T) {
	f := newFixture(t)
	f.installLock(map[string]map[string]string{"alpha": v1, "parked": v1, "gone": v1})
	f.home.WriteFile(".agents/skills/OFF/parked/SKILL.md", "---\nname: parked\n---\nparked\n")
	removeAll(t, f.home.Path(".agents/skills/parked"))
	removeAll(t, f.home.Path(".agents/skills/gone"))
	f.home.WriteFile(".claude/skills/loose/SKILL.md", "---\nname: loose\n---\nloose\n")
	upToDate := domain.TargetResult{State: domain.StateUpToDate, CheckedAt: t0, RemovedUpstream: true}
	f.saveResults(map[string]domain.TargetResult{
		"lock:parked": upToDate, "lock:gone": upToDate, "dir:.claude/skills/loose": upToDate,
	})

	skills, err := f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for id, want := range map[string]domain.State{
		"lock:parked": domain.StateDisabled, "lock:gone": domain.StateMissing, "dir:.claude/skills/loose": domain.StateNoOrigin,
	} {
		s := skillByID(t, skills, id)
		if s.State != want || slices.Contains(s.Issues, domain.IssueRemovedUpstream) {
			t.Errorf("%s: state = %s, issues = %v; want %s and no removed_upstream", id, s.State, s.Issues, want)
		}
	}
}

func removeAll(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

func TestASkillRemovedUpstreamIsNeitherAFailureNorANotification(t *testing.T) {
	f := newFixture(t)
	f.installLock(map[string]map[string]string{"alpha": v1})
	f.publish("other", v1) // the repository exists; alpha is not in it

	res, err := f.app.Check(ctx, app.CheckOptions{Notify: true, Message: message})
	if err != nil {
		t.Fatal(err)
	}

	if res.Failed || res.Notified {
		t.Errorf("check = %+v, want neither failed nor notified", res)
	}
	if n := len(f.notifier.Sent()); n != 0 {
		t.Errorf("%d notifications sent, want none", n)
	}
	if st := f.persisted(); st.Checks.LastFailed || !st.Checks.LastSuccess.Equal(t0) {
		t.Errorf("checks = %+v, want a successful run", st.Checks)
	}
}
