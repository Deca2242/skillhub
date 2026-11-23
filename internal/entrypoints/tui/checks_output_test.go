package tui_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestAgentCheckboxesEscapeKeyAndLabelsWhilePreservingRawKeys(t *testing.T) {
	f := newFixture(t)
	evilKey := "evil" + hostile + "skill"

	f.installNamedLock(evilKey, map[string]string{
		"SKILL.md": "---\nname: " + yamlQuote(t, evilKey) + "\ndescription: demo\n---\nbody\n",
	})
	if err := os.MkdirAll(f.home.Path(".claude/skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := f.home.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	lock := skillslock.New(p, files)
	a, err := app.New(app.Deps{
		Paths:       p,
		Agents:      []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Files:       files,
		Dirs:        fsadapter.NewScanner(p.Guard()),
		Lock:        lock,
		ManagedLock: lock,
		Links:       fsadapter.NewLinkOps(p.Guard()),
		State:       statefile.New(p),
		Settings:    settings.New(p),
		GitHub:      f.gh,
		Git:         f.git,
		Notifier:    f.notifier,
		Background:  f.bg,
		Clock:       f.clock,
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	m := openSized(t, a, 120, 30)
	pressRun(m, "l")

	raw := m.View().Content
	assertNothingActs(t, raw)
	if !strings.Contains(raw, "\x1b[") {
		t.Errorf("trusted Lip Gloss ANSI styling disappeared from checks view")
	}

	out := screen(m)
	wantQuestion := "¿En qué agentes está evil" + hostileShown + "skill?"
	if !strings.Contains(out, wantQuestion) {
		t.Errorf("checks view lacks escaped question %q:\n%s", wantQuestion, out)
	}
	if !strings.Contains(out, "> [ ] Claude Code") {
		t.Errorf("checks view lacks Claude Code checkbox:\n%s", out)
	}

	// Mark Claude Code, confirm plan, and verify the raw skill key was preserved
	// when creating the symlink.
	pressRun(m, " ")
	if !strings.Contains(screen(m), "[x] Claude Code") {
		t.Fatalf("space did not mark Claude Code checkbox:\n%s", screen(m))
	}
	pressRun(m, "enter")
	if !m.InPlanForTest() {
		t.Fatalf("confirming checkbox did not open link plan:\n%s", screen(m))
	}
	assertNothingActs(t, m.View().Content)
	pressRun(m, "s")
	assertNothingActs(t, m.View().Content)

	linkPath := filepath.Join(f.home.Path(".claude/skills"), evilKey)
	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("expected symlink at %q for raw skill key: %v", linkPath, err)
	}
}

func TestUnignoreCheckboxesEscapeLabelsAndFeedbackWhilePreservingRawSettings(t *testing.T) {
	h := testsupport.NewHome(t)
	evilSkill := "evil" + hostile + "name"
	ghostEntry := "ghost" + hostile + "\nentry"

	p := h.Paths()
	writeTree(t, filepath.Join(p.StoreDir(), evilSkill), map[string]string{
		"SKILL.md": "---\nname: " + yamlQuote(t, evilSkill) + "\ndescription: ignored\n---\nbody\n",
	})
	files := fsadapter.NewFileOps(p.Guard())
	lock := skillslock.New(p, files)
	a, err := app.New(app.Deps{
		Paths:       p,
		Agents:      []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Files:       files,
		Dirs:        fsadapter.NewScanner(p.Guard()),
		Lock:        lock,
		ManagedLock: lock,
		Links:       fsadapter.NewLinkOps(p.Guard()),
		State:       statefile.New(p),
		Settings:    settings.New(p),
		GitHub:      fakes.NewGitHub(),
		Git:         fakes.NewGit(),
		Notifier:    fakes.NewNotifier(),
		Background:  fakes.NewBackgroundService(),
		Clock:       fakes.NewClock(t0),
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if _, err := a.SetSetting(app.KeyIgnore, evilSkill+","+ghostEntry, false); err != nil {
		t.Fatalf("SetSetting ignore: %v", err)
	}

	m := openSized(t, a, 120, 30)
	openIgnored(m)
	pressRun(m, "enter")

	assertNothingActs(t, m.View().Content)
	out := screen(m)
	for _, want := range []string{
		"> [x] evil" + hostileShown + "name",
		"  [x] ghost" + hostileShown + `\nentry` + " (ninguna skill tiene ese nombre)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unignore checkboxes lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nentry") {
		t.Errorf("unignore checkbox label split across lines:\n%s", out)
	}

	// Unmark only the first entry (evilSkill) and confirm: singular feedback is
	// escaped and raw ghostEntry stays persisted untouched in settings.toml.
	pressRun(m, "space")
	pressRun(m, "enter")
	assertNothingActs(t, m.View().Content)

	wantStatus := "Ya no se ignora evil" + hostileShown + "name: vuelve a aparecer en Skills."
	if got := m.StatusForTest(); got != wantStatus {
		t.Errorf("status = %q, want %q", got, wantStatus)
	}
	assertNoHostileRaw(t, "unignore singular status", m.StatusForTest())

	gotSettings, err := a.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if !slices.Equal(gotSettings.Ignore, []string{ghostEntry}) {
		t.Errorf("persisted Ignore = %q, want raw %q", gotSettings.Ignore, []string{ghostEntry})
	}
}
