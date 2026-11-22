package app_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/background"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

var ctx = context.Background()

func TestResolve(t *testing.T) {
	f := newFixture(t)
	f.installLock(map[string]map[string]string{"alpha": v1, "beta": v1})
	// A GitHub clone in Claude's directory holding a skill also named alpha.
	f.home.WriteFile(".claude/skills/alpha/SKILL.md", "---\nname: alpha\n---\nclone\n")
	f.home.WriteFile(".claude/skills/alpha/.git/HEAD", "ref: refs/heads/main\n")
	f.git.AddClone(f.home.Path(".claude/skills/alpha"), githubRemote)

	t.Run("a name matching two rows is refused with both ids", func(t *testing.T) {
		_, err := f.app.Resolve(ctx, "alpha")
		var amb *app.AmbiguousError
		if !errors.As(err, &amb) {
			t.Fatalf("Resolve(alpha) error = %v, want *AmbiguousError", err)
		}
		want := []string{"git:.claude/skills/alpha", "lock:alpha"}
		if !reflect.DeepEqual(amb.IDs, want) {
			t.Errorf("IDs = %v, want %v", amb.IDs, want)
		}
	})
	t.Run("an id picks its row", func(t *testing.T) {
		for _, id := range []string{"lock:alpha", "git:.claude/skills/alpha"} {
			got, err := f.app.Resolve(ctx, id)
			if err != nil || got != id {
				t.Errorf("Resolve(%s) = %q, %v; want %q", id, got, err, id)
			}
		}
	})
	t.Run("a unique name picks its row", func(t *testing.T) {
		got, err := f.app.Resolve(ctx, "beta")
		if err != nil || got != "lock:beta" {
			t.Errorf("Resolve(beta) = %q, %v; want lock:beta", got, err)
		}
	})
	t.Run("an unknown name is not found", func(t *testing.T) {
		if _, err := f.app.Resolve(ctx, "nope"); !errors.Is(err, app.ErrSkillNotFound) {
			t.Errorf("Resolve(nope) error = %v, want ErrSkillNotFound", err)
		}
	})
}

func skillByID(t *testing.T, skills []domain.Skill, id string) domain.Skill {
	t.Helper()
	for _, s := range skills {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no skill %s in %v", id, skills)
	return domain.Skill{}
}

func TestSkillsShowPendingUpdatesAndInterruptedOperations(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.installLock(map[string]map[string]string{"alpha": v1, "beta": v1})
	f.publish("beta", v1)

	skills, err := f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := skillByID(t, skills, "lock:alpha").State; got != domain.StateUnchecked {
		t.Fatalf("before a check, alpha is %s, want unchecked", got)
	}

	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	f.record(domain.Operation{ID: "op1", Kind: domain.OperationApply, Target: "lock:beta", RestoreSnapshotID: "s1", StartedAt: t0})
	skills, err = f.app.Skills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alpha, beta := skillByID(t, skills, "lock:alpha"), skillByID(t, skills, "lock:beta")
	if alpha.State != domain.StateUpdateAvailable {
		t.Errorf("alpha state = %s, want update_available", alpha.State)
	}
	if slices.Contains(alpha.Issues, domain.IssueInterruptedUpdate) {
		t.Errorf("alpha issues = %v, want no interrupted_update", alpha.Issues)
	}
	if !slices.Contains(beta.Issues, domain.IssueInterruptedUpdate) {
		t.Errorf("beta issues = %v, want interrupted_update", beta.Issues)
	}
	// The interrupted target keeps the result of its last check.
	if beta.State != domain.StateUpToDate {
		t.Errorf("beta state = %s, want up_to_date", beta.State)
	}
}

func TestCheckIfDue(t *testing.T) {
	f := newFixture(t)
	f.standard()

	run := func(t *testing.T) app.CheckResult {
		t.Helper()
		res, err := f.app.Check(ctx, app.CheckOptions{IfDue: true})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run(t); !res.Ran {
		t.Fatal("first check --if-due did not run")
	}
	first := f.persisted().Checks
	calls := len(f.gh.Calls())

	f.clock.Advance(time.Hour - time.Second)
	if res := run(t); res.Ran {
		t.Error("check --if-due ran before the interval")
	}
	if got := len(f.gh.Calls()); got != calls {
		t.Errorf("a skipped check called GitHub %d times", got-calls)
	}
	if got := f.persisted().Checks; got != first {
		t.Errorf("a skipped check changed the check moments: %+v, want %+v", got, first)
	}

	f.clock.Advance(time.Second)
	if res := run(t); !res.Ran {
		t.Error("check --if-due did not run at the interval")
	}
	if got := f.persisted().Checks.LastSuccess; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("LastSuccess = %v, want %v", got, t0.Add(time.Hour))
	}

	t.Run("the interval comes from the settings", func(t *testing.T) {
		if _, err := f.app.SetSetting(app.KeyCheckInterval, "2h", false); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(time.Hour)
		if res := run(t); res.Ran {
			t.Error("ran one hour after the last success with a 2h interval")
		}
		f.clock.Advance(time.Hour)
		if res := run(t); !res.Ran {
			t.Error("did not run two hours after the last success with a 2h interval")
		}
	})

	t.Run("without IfDue the check always runs", func(t *testing.T) {
		res, err := f.app.Check(ctx, app.CheckOptions{})
		if err != nil || !res.Ran {
			t.Errorf("Check() = %+v, %v; want it to run", res, err)
		}
	})
}

func message(d background.Decision) (string, string) {
	parts := []string{}
	for _, r := range d.Reasons {
		parts = append(parts, string(r))
	}
	return "title", strings.Join(parts, ",")
}

func TestCheckNotify(t *testing.T) {
	notify := app.CheckOptions{Notify: true, Message: message}

	t.Run("a new candidate is announced once and marked", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		res, err := f.app.Check(ctx, notify)
		if err != nil || !res.Notified {
			t.Fatalf("Check() = %+v, %v; want a notification", res, err)
		}
		if got := f.notifier.Sent(); len(got) != 1 || got[0].Body != string(background.ReasonNewCandidates) {
			t.Fatalf("sent = %v", got)
		}
		cands, err := f.app.Candidates()
		if err != nil || len(cands) != 1 {
			t.Fatalf("Candidates() = %v, %v", cands, err)
		}
		if got := f.persisted().Notified.CandidateIDs; !reflect.DeepEqual(got, []string{cands[0].ID}) {
			t.Errorf("persisted marks = %v, want %v", got, []string{cands[0].ID})
		}
		res, err = f.app.Check(ctx, notify)
		if err != nil || res.Notified || len(f.notifier.Sent()) != 1 {
			t.Errorf("an unchanged pending list was announced again: %+v, %v, %v", res, err, f.notifier.Sent())
		}
	})

	t.Run("without Notify nothing is sent nor marked", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
			t.Fatal(err)
		}
		if len(f.notifier.Sent()) != 0 || len(f.persisted().Notified.CandidateIDs) != 0 {
			t.Errorf("sent %v, marks %v", f.notifier.Sent(), f.persisted().Notified)
		}
	})

	t.Run("notifications off silence it", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		if _, err := f.app.SetSetting(app.KeyNotifications, "false", false); err != nil {
			t.Fatal(err)
		}
		res, err := f.app.Check(ctx, notify)
		if err != nil || res.Notified || len(f.notifier.Sent()) != 0 {
			t.Errorf("Check() = %+v, %v; sent %v", res, err, f.notifier.Sent())
		}
	})

	t.Run("a failing check after a success is announced once", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		if _, err := f.app.Check(ctx, notify); err != nil {
			t.Fatal(err)
		}
		f.gh.Fail = func(fakes.Call) error { return errors.New("injected") }
		f.clock.Advance(time.Hour)
		res, err := f.app.Check(ctx, notify)
		if err != nil || !res.Failed || !res.Notified {
			t.Fatalf("Check() = %+v, %v; want a failed check announced", res, err)
		}
		if got := f.notifier.Sent(); len(got) != 2 || got[1].Body != string(background.ReasonCheckFailed) {
			t.Fatalf("sent = %v", got)
		}
		if !f.persisted().Notified.CheckFailure {
			t.Error("the failure mark was not saved")
		}
		res, err = f.app.Check(ctx, notify)
		if err != nil || res.Notified {
			t.Errorf("a second failure was announced: %+v, %v", res, err)
		}
	})

	t.Run("an interrupted operation is announced even when the check is not due", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		if _, err := f.app.Check(ctx, notify); err != nil {
			t.Fatal(err)
		}
		f.record(domain.Operation{ID: "op1", Kind: domain.OperationApply, Target: "lock:alpha", RestoreSnapshotID: "s1", StartedAt: t0})
		res, err := f.app.Check(ctx, app.CheckOptions{IfDue: true, Notify: true, Message: message})
		if err != nil || res.Ran || !res.Notified {
			t.Fatalf("Check() = %+v, %v; want a skipped check that announces the operation", res, err)
		}
		if got := f.notifier.Sent(); got[len(got)-1].Body != string(background.ReasonInterrupted) {
			t.Errorf("sent = %v", got)
		}
		if got := f.persisted().Notified.OperationID; got != "op1" {
			t.Errorf("operation mark = %q, want op1", got)
		}
	})

	t.Run("a notification that cannot be sent marks nothing", func(t *testing.T) {
		f := newFixture(t)
		f.standard()
		f.notifier.Fail = func(fakes.Call) error { return errors.New("no notify-send") }
		if _, err := f.app.Check(ctx, notify); err == nil {
			t.Fatal("Check() error = nil, want the notifier's error")
		}
		if got := f.persisted().Notified.CandidateIDs; len(got) != 0 {
			t.Errorf("marks = %v after a failed notification", got)
		}
	})
}

func TestCheckChangesNoSkillsFolder(t *testing.T) {
	f := newFixture(t)
	f.standard()
	before := f.homeTree()
	if _, err := f.app.Check(ctx, app.CheckOptions{Notify: true, Message: message}); err != nil {
		t.Fatal(err)
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Errorf("check changed the home outside skillhub's own state:\nbefore %v\nafter  %v", before, after)
	}
}

func TestSetSetting(t *testing.T) {
	tests := []struct {
		name, key, value string
		wantErr          error
		want             func(domain.Settings) domain.Settings
	}{
		{name: "interval", key: "check_interval", value: "2h", want: func(s domain.Settings) domain.Settings {
			s.CheckInterval = 2 * time.Hour
			return s
		}},
		{name: "interval under the minimum", key: "check_interval", value: "10m", wantErr: domain.ErrIntervalTooShort},
		{name: "interval that is not a duration", key: "check_interval", value: "often", wantErr: app.ErrInvalidSettingValue},
		{name: "notifications off", key: "notifications", value: "false", want: func(s domain.Settings) domain.Settings {
			s.Notifications = false
			return s
		}},
		{name: "notifications not a boolean", key: "notifications", value: "maybe", wantErr: app.ErrInvalidSettingValue},
		{name: "ignore list", key: "ignore", value: "alpha, lock:beta", want: func(s domain.Settings) domain.Settings {
			s.Ignore = []string{"alpha", "lock:beta"}
			return s
		}},
		{name: "empty ignore list", key: "ignore", value: "", want: func(s domain.Settings) domain.Settings {
			s.Ignore = []string{}
			return s
		}},
		{name: "unknown key", key: "colour", value: "red", wantErr: app.ErrUnknownSetting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			got, err := f.app.SetSetting(tt.key, tt.value, false)
			saved, lerr := f.settings.Load()
			if lerr != nil {
				t.Fatal(lerr)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if !reflect.DeepEqual(saved, domain.DefaultSettings()) {
					t.Errorf("a refused value was saved: %+v", saved)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want(domain.DefaultSettings())
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(saved, want) {
				t.Errorf("returned %+v, saved %+v; want %+v", got, saved, want)
			}
		})
	}

	t.Run("dry run saves nothing", func(t *testing.T) {
		f := newFixture(t)
		got, err := f.app.SetSetting(app.KeyCheckInterval, "3h", true)
		if err != nil || got.CheckInterval != 3*time.Hour {
			t.Fatalf("SetSetting dry run = %+v, %v", got, err)
		}
		if saved, _ := f.settings.Load(); saved.CheckInterval != domain.DefaultCheckInterval {
			t.Errorf("dry run saved %v", saved.CheckInterval)
		}
	})
}

func TestApplyGoesThroughTheUpdateFlow(t *testing.T) {
	f := newFixture(t)
	f.standard()
	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	c, err := f.app.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.app.Apply(ctx, updates.Approval{CandidateID: c.ID, ToSHA: c.ToSHA})
	if err != nil || res.Outcome != updates.OutcomeApplied {
		t.Fatalf("Apply = %+v, %v", res, err)
	}
	if got := hashOf(t, f.home.Path(".agents/skills/alpha")); got != c.ToSHA {
		t.Errorf("installed SHA = %s, want %s", got, c.ToSHA)
	}
	if _, err := f.app.CandidateFor("lock:alpha"); !errors.Is(err, app.ErrNoCandidate) {
		t.Errorf("CandidateFor after apply error = %v, want ErrNoCandidate", err)
	}
}

func TestDiff(t *testing.T) {
	f := newFixture(t)
	f.publish("alpha", map[string]string{"SKILL.md": "a\nb\n", "old.md": "gone\n", "img.bin": "x\x00y"})
	f.installLock(map[string]map[string]string{"alpha": {"SKILL.md": "a\nb\n", "old.md": "gone\n", "img.bin": "x\x00y"}})
	f.publish("alpha", map[string]string{"SKILL.md": "a\nc\n", "new.md": "hello\n", "img.bin": "x\x00z"})
	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	c, err := f.app.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	before := f.homeTree()
	diffs, err := f.app.Diff(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]app.FileDiff{}
	for _, d := range diffs {
		got[d.Path] = d
	}
	if len(diffs) != 4 {
		t.Fatalf("diffs = %+v, want 4 files", diffs)
	}
	if d := got["SKILL.md"]; d.Change != domain.ChangeModified || !strings.Contains(d.Unified, "-b\n") || !strings.Contains(d.Unified, "+c\n") {
		t.Errorf("SKILL.md diff = %+v", d)
	}
	if d := got["new.md"]; d.Change != domain.ChangeAdded || !strings.Contains(d.Unified, "+hello\n") {
		t.Errorf("new.md diff = %+v", d)
	}
	if d := got["old.md"]; d.Change != domain.ChangeRemoved || !strings.Contains(d.Unified, "-gone\n") {
		t.Errorf("old.md diff = %+v", d)
	}
	if d := got["img.bin"]; !d.Binary || d.Unified != "" {
		t.Errorf("img.bin diff = %+v, want binary without text", d)
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Error("Diff changed the home")
	}
}
