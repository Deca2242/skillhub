package app_test

import (
	"errors"
	"os"
	"slices"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/packs"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func newPackFixture(t *testing.T) *fixture {
	t.Helper()
	home := testsupport.NewHome(t)
	f := &fixture{
		t: t, home: home,
		gh: fakes.NewGitHub(), git: fakes.NewGit(), notifier: fakes.NewNotifier(),
		bg: fakes.NewBackgroundService(), clock: fakes.NewClock(t0),
		state: statefile.New(home.Paths()), settings: settings.New(home.Paths()),
		upstream: t.TempDir(),
	}
	f.gh.AddRepo(repo, "main")
	p := home.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	a, err := app.New(app.Deps{
		Paths:  p,
		Agents: []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Files:  files, Dirs: fsadapter.NewScanner(p.Guard()), Lock: skillslock.New(p, files),
		State: f.state, Packs: f.state, Settings: f.settings, GitHub: f.gh, Git: f.git,
		Notifier: f.notifier, Background: f.bg, Clock: f.clock,
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	f.app = a
	return f
}

func TestAppPacksCreateAddShowRemoveAndNotObserved(t *testing.T) {
	f := newPackFixture(t)
	v1 := map[string]string{"SKILL.md": "---\nname: alpha\ndescription: Alpha skill\n---\nbody\n"}
	f.installLock(map[string]map[string]string{"alpha": v1, "beta": v1})

	// Record initial lock bytes to verify nothing changes.
	lockBefore, err := os.ReadFile(f.home.Paths().SkillsLockFile())
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}

	if _, err := f.app.PackCreate(ctx, "seguridad", "Pack de prueba"); err != nil {
		t.Fatalf("PackCreate: %v", err)
	}

	// Reject unobserved skill.
	if _, err := f.app.PackAdd(ctx, "seguridad", "lock:no-existe"); !errors.Is(err, app.ErrSkillNotFound) {
		t.Fatalf("PackAdd(unobserved) error = %v, want ErrSkillNotFound", err)
	}

	// Add observed skill by name -> stores exact ID.
	if _, err := f.app.PackAdd(ctx, "seguridad", "alpha"); err != nil {
		t.Fatalf("PackAdd(alpha): %v", err)
	}
	if _, err := f.app.PackAdd(ctx, "seguridad", "lock:beta"); err != nil {
		t.Fatalf("PackAdd(lock:beta): %v", err)
	}

	// Membership lookup for skills show.
	memberships, err := f.app.SkillPacks("lock:alpha")
	if err != nil {
		t.Fatalf("SkillPacks: %v", err)
	}
	if len(memberships) != 1 || memberships[0].Name != "seguridad" {
		t.Fatalf("SkillPacks = %+v, want [seguridad]", memberships)
	}

	// Verify pack create/add/lookup did not touch the skills lock.
	lockAfterAdd, err := os.ReadFile(f.home.Paths().SkillsLockFile())
	if err != nil {
		t.Fatalf("read lock after add: %v", err)
	}
	if !slices.Equal(lockBefore, lockAfterAdd) {
		t.Errorf("skills lock changed during pack create/add operations")
	}

	// Remove beta from disk and lock so it is no longer observed in inventory.
	if err := os.RemoveAll(f.home.Path(".agents/skills/beta")); err != nil {
		t.Fatal(err)
	}
	f.installLock(map[string]map[string]string{"alpha": v1})
	lockAfterShrink, err := os.ReadFile(f.home.Paths().SkillsLockFile())
	if err != nil {
		t.Fatalf("read lock after shrink: %v", err)
	}

	view, err := f.app.PackShow(ctx, "seguridad")
	if err != nil {
		t.Fatalf("PackShow: %v", err)
	}
	if len(view.Members) != 2 {
		t.Fatalf("PackShow members = %d, want 2 (kept reference)", len(view.Members))
	}
	var alphaView, betaView app.PackMemberView
	for _, m := range view.Members {
		switch m.ID {
		case "lock:alpha":
			alphaView = m
		case "lock:beta":
			betaView = m
		}
	}
	if alphaView.Presence != app.MemberObserved || alphaView.Health != app.MemberHealthHealthy {
		t.Errorf("alpha view = %+v, want observed + healthy", alphaView)
	}
	if betaView.Presence != app.MemberNotObserved || betaView.Health != app.MemberHealthNotObserved {
		t.Errorf("beta view = %+v, want not_observed", betaView)
	}

	// Unobserved member can still be removed by exact ID.
	if _, err := f.app.PackRemove(ctx, "seguridad", "lock:beta"); err != nil {
		t.Fatalf("PackRemove(not_observed exact ID): %v", err)
	}
	view, err = f.app.PackShow(ctx, "seguridad")
	if err != nil {
		t.Fatalf("PackShow after remove: %v", err)
	}
	if len(view.Members) != 1 || view.Members[0].ID != "lock:alpha" {
		t.Fatalf("members after remove = %+v, want [lock:alpha]", view.Members)
	}

	// Verify PackShow and PackRemove did not touch the skills lock.
	lockAfterRemove, err := os.ReadFile(f.home.Paths().SkillsLockFile())
	if err != nil {
		t.Fatalf("read lock after remove: %v", err)
	}
	if !slices.Equal(lockAfterShrink, lockAfterRemove) {
		t.Errorf("skills lock changed during pack show/remove operations")
	}
	_ = packs.ErrPackNotFound
}

func TestAppPacksLockOnlyRowIsNotObserved(t *testing.T) {
	v1 := map[string]string{"SKILL.md": "---\nname: alpha\ndescription: Alpha skill\n---\nbody\n"}

	t.Run("whole member directory removed while lock entry remains", func(t *testing.T) {
		f := newPackFixture(t)
		f.installLock(map[string]map[string]string{"alpha": v1, "beta": v1})
		if _, err := f.app.PackCreate(ctx, "core", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := f.app.PackAdd(ctx, "core", "lock:alpha"); err != nil {
			t.Fatal(err)
		}

		// Remove whole directory of alpha and beta, keeping lock entries intact.
		if err := os.RemoveAll(f.home.Path(".agents/skills/alpha")); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(f.home.Path(".agents/skills/beta")); err != nil {
			t.Fatal(err)
		}

		if _, err := f.app.PackAdd(ctx, "core", "lock:beta"); !errors.Is(err, app.ErrSkillNotFound) {
			t.Fatalf("PackAdd(lock:beta without dir) error = %v, want ErrSkillNotFound", err)
		}

		view, err := f.app.PackShow(ctx, "core")
		if err != nil {
			t.Fatalf("PackShow: %v", err)
		}
		if len(view.Members) != 1 || view.Members[0].ID != "lock:alpha" {
			t.Fatalf("unexpected members: %+v", view.Members)
		}
		if view.Members[0].Presence != app.MemberNotObserved || view.Members[0].Health != app.MemberHealthNotObserved {
			t.Fatalf("lock-only member = %+v, want not_observed", view.Members[0])
		}

		if _, err := f.app.PackRemove(ctx, "core", "lock:alpha"); err != nil {
			t.Fatalf("PackRemove(not_observed exact ID): %v", err)
		}
	})

	t.Run("only SKILL.md removed while lock entry and directory remain", func(t *testing.T) {
		f := newPackFixture(t)
		f.installLock(map[string]map[string]string{"alpha": v1, "beta": v1})
		if _, err := f.app.PackCreate(ctx, "core", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := f.app.PackAdd(ctx, "core", "lock:alpha"); err != nil {
			t.Fatal(err)
		}

		if err := os.Remove(f.home.Path(".agents/skills/alpha/SKILL.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.home.Path(".agents/skills/beta/SKILL.md")); err != nil {
			t.Fatal(err)
		}

		if _, err := f.app.PackAdd(ctx, "core", "lock:beta"); !errors.Is(err, app.ErrSkillNotFound) {
			t.Fatalf("PackAdd(lock:beta without SKILL.md) error = %v, want ErrSkillNotFound", err)
		}

		view, err := f.app.PackShow(ctx, "core")
		if err != nil {
			t.Fatalf("PackShow: %v", err)
		}
		if len(view.Members) != 1 || view.Members[0].Presence != app.MemberNotObserved || view.Members[0].Health != app.MemberHealthNotObserved {
			t.Fatalf("member without SKILL.md = %+v, want not_observed", view.Members)
		}
	})
}
