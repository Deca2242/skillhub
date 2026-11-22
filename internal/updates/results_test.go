package updates_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

// removeUpstream deletes the upstream folder of alpha and commits on main;
// the repository itself still exists.
func (f *fixture) removeUpstream() {
	f.t.Helper()
	if err := os.RemoveAll(filepath.Join(f.upstream, "skills", skillName)); err != nil {
		f.t.Fatal(err)
	}
	f.gh.Commit(f.t, repo, "main", f.upstream)
}

// result returns the persisted check result of tgt.
func (f *fixture) result(tgt string) (domain.TargetResult, bool) {
	f.t.Helper()
	r, ok := f.persisted().Results[tgt]
	return r, ok
}

func TestCheckTreatsASkillRemovedUpstreamAsUpToDate(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.removeUpstream()
	before := f.agentsTree()

	// The second check reads the recorded head instead of the tree.
	for _, run := range []string{"first check", "check with an unchanged head"} {
		report := f.check()

		f.assertAgentsTree(before, run)
		tc := targetResult(t, report, target)
		if tc.Outcome != updates.CheckUpToDate || !tc.RemovedUpstream || tc.Err != nil || tc.CandidateID != "" {
			t.Fatalf("%s: result = %+v, want up_to_date and removed upstream", run, tc)
		}
		if report.Failed() || len(report.NewCandidates) != 0 {
			t.Errorf("%s: failed = %v, new candidates = %v", run, report.Failed(), report.NewCandidates)
		}
		st := f.persisted()
		if len(st.Candidates) != 0 {
			t.Errorf("%s: candidates = %+v, want none", run, st.Candidates)
		}
		if st.Checks.LastFailed || !st.Checks.LastSuccess.Equal(t0) {
			t.Errorf("%s: checks = %+v, want a successful run", run, st.Checks)
		}
		want := domain.TargetResult{State: domain.StateUpToDate, CheckedAt: t0, RemovedUpstream: true}
		if got, ok := f.result(target); !ok || got != want {
			t.Errorf("%s: persisted result = %+v, %v; want %+v", run, got, ok, want)
		}
	}
}

func TestCheckOfAMissingRepositoryStillFails(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.gh.Fail = func(c fakes.Call) error {
		if c.Op == "Tree" {
			return errors.New("gh: HTTP 404")
		}
		return nil
	}

	report := f.check()

	tc := targetResult(t, report, target)
	if tc.Outcome != updates.CheckFailed || tc.RemovedUpstream || !report.Failed() {
		t.Fatalf("result = %+v, want a failure", tc)
	}
}

func TestCheckRecordsTheResultOfEachTarget(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(f *fixture)
		want    domain.State
		detail  string
	}{
		{name: "up to date", prepare: func(*fixture) {}, want: domain.StateUpToDate},
		{name: "update available", prepare: func(f *fixture) { f.publish("main", v2Files) }, want: domain.StateUpdateAvailable},
		{
			name: "blocked candidate",
			prepare: func(f *fixture) {
				f.publishWith(func(dir string) { _ = os.Symlink("SKILL.md", filepath.Join(dir, "link.md")) })
			},
			want: domain.StateUpdateAvailable,
		},
		{
			name: "skipped version",
			prepare: func(f *fixture) {
				c := f.staged()
				if err := f.svc.Skip(context.Background(), approval(c)); err != nil {
					f.t.Fatal(err)
				}
				f.clock.Advance(time.Hour)
			},
			want: domain.StateUpToDate,
		},
		{
			name: "failed check",
			prepare: func(f *fixture) {
				f.gh.Fail = func(c fakes.Call) error {
					if c.Op == "ResolveCommit" {
						return ports.ErrRateLimited
					}
					return nil
				}
			},
			want:   domain.StateCheckFailed,
			detail: ports.ErrRateLimited.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.standard()
			tt.prepare(f)

			f.check()

			got, ok := f.result(target)
			if !ok || got.State != tt.want || !got.CheckedAt.Equal(f.clock.Now()) || got.RemovedUpstream {
				t.Fatalf("persisted result = %+v, %v; want %s at %s", got, ok, tt.want, f.clock.Now())
			}
			if tt.detail == "" && got.Detail != "" || !strings.Contains(got.Detail, tt.detail) {
				t.Errorf("detail = %q, want %q", got.Detail, tt.detail)
			}
		})
	}
}

func TestCheckKeepsTheResultOfATargetItDoesNotCheck(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.check()
	want, _ := f.result(target)
	f.clock.Advance(time.Hour)
	if err := os.RemoveAll(f.folder()); err != nil {
		t.Fatal(err)
	}

	if tc := targetResult(t, f.check(), target); tc.Outcome != updates.CheckNotChecked {
		t.Fatalf("outcome = %s, want not_checked", tc.Outcome)
	}
	if got, ok := f.result(target); !ok || got != want {
		t.Errorf("result = %+v, %v; want %+v kept", got, ok, want)
	}
}

func TestAFailedCheckKeepsThePendingCandidate(t *testing.T) {
	f := newFixture(t)
	f.standard()
	first := f.staged()
	f.publish("main", v3Files)
	f.gh.Fail = func(c fakes.Call) error {
		if c.Op == "Blob" {
			return ports.ErrRateLimited
		}
		return nil
	}

	tc := targetResult(t, f.check(), target)

	if tc.Outcome != updates.CheckFailed {
		t.Fatalf("outcome = %s, want failed", tc.Outcome)
	}
	if got := f.candidate(first.ID); got.State != domain.CandidatePending {
		t.Errorf("first candidate = %s, want pending", got.State)
	}
	if got := hashOf(t, first.StagedPath); got != first.ToSHA {
		t.Errorf("staging of the pending candidate = %s, want %s", got, first.ToSHA)
	}
	if got, _ := f.result(target); got.State != domain.StateCheckFailed {
		t.Errorf("result = %+v, want check_failed", got)
	}
	f.gh.Fail = nil
	if _, err := f.svc.Apply(context.Background(), approval(first)); err != nil {
		t.Errorf("apply of the kept candidate: %v", err)
	}
}

func TestApplySkipUndoAndRecoverKeepTheResultCoherent(t *testing.T) {
	ctx := context.Background()

	t.Run("apply leaves up_to_date", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		c := f.staged()
		f.clock.Advance(time.Hour)

		if _, err := f.svc.Apply(ctx, approval(c)); err != nil {
			t.Fatal(err)
		}

		want := domain.TargetResult{State: domain.StateUpToDate, CheckedAt: t0.Add(time.Hour)}
		if got, ok := f.result(target); !ok || got != want {
			t.Errorf("result = %+v, %v; want %+v", got, ok, want)
		}
	})

	t.Run("a reverted apply keeps the previous result", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		c := f.staged()
		want, _ := f.result(target)
		f.trip.armAfter("State.Save", false)

		if _, err := f.svc.Apply(ctx, approval(c)); !errors.Is(err, errInjected) {
			t.Fatalf("apply = %v, want the injected failure", err)
		}

		if f.candidate(c.ID).State != domain.CandidatePending {
			t.Fatal("candidate not pending after the revert")
		}
		if got, ok := f.result(target); !ok || got != want {
			t.Errorf("result = %+v, %v; want %+v", got, ok, want)
		}
	})

	t.Run("skip leaves up_to_date", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		c := f.staged()
		f.clock.Advance(time.Hour)

		if err := f.svc.Skip(ctx, approval(c)); err != nil {
			t.Fatal(err)
		}

		want := domain.TargetResult{State: domain.StateUpToDate, CheckedAt: t0.Add(time.Hour)}
		if got, ok := f.result(target); !ok || got != want {
			t.Errorf("result = %+v, %v; want %+v", got, ok, want)
		}
	})

	t.Run("undo clears the result", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		c := f.staged()
		if _, err := f.svc.Apply(ctx, approval(c)); err != nil {
			t.Fatal(err)
		}

		if _, err := f.svc.Undo(ctx, updates.UndoRequest{Target: target}); err != nil {
			t.Fatal(err)
		}

		if got, ok := f.result(target); ok {
			t.Errorf("result = %+v, want none after an undo", got)
		}
	})

	t.Run("recovering an apply clears the result", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		c := f.staged()
		// Killed right after the outcome was saved, before the record cleared.
		f.trip.armAfter("State.Save", true)
		if !interrupted(func() { _, _ = f.svc.Apply(ctx, approval(c)) }) {
			t.Fatal("apply was not interrupted")
		}
		f.reopen()
		if got, _ := f.result(target); got.State != domain.StateUpToDate {
			t.Fatalf("result before recover = %+v, want the saved up_to_date", got)
		}

		if _, err := f.svc.Recover(ctx); err != nil {
			t.Fatal(err)
		}

		if f.candidate(c.ID).State != domain.CandidatePending {
			t.Error("candidate not pending after the recovery")
		}
		if got, ok := f.result(target); ok {
			t.Errorf("result = %+v, want none after the recovery", got)
		}
	})
}
