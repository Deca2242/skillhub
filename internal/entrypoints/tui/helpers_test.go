package tui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

const repo = "acme/skills"

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

type fixture struct {
	t        *testing.T
	home     *testsupport.Home
	gh       *fakes.GitHub
	git      *fakes.Git
	notifier *fakes.Notifier
	bg       *fakes.BackgroundService
	clock    *fakes.Clock
	upstream string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t:        t,
		home:     testsupport.NewHome(t),
		gh:       fakes.NewGitHub(),
		git:      fakes.NewGit(),
		notifier: fakes.NewNotifier(),
		bg:       fakes.NewBackgroundService(),
		clock:    fakes.NewClock(t0),
		upstream: filepath.Join(t.TempDir(), "upstream"),
	}
	f.gh.AddRepo(repo, "main")
	return f
}

func (f *fixture) newApp() *app.App {
	f.t.Helper()
	p := f.home.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	a, err := app.New(app.Deps{
		Paths:      p,
		Agents:     []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Files:      files,
		Dirs:       fsadapter.NewScanner(p.Guard()),
		Lock:       skillslock.New(p, files),
		State:      statefile.New(p),
		Settings:   settings.New(p),
		GitHub:     f.gh,
		Git:        f.git,
		Notifier:   f.notifier,
		Background: f.bg,
		Clock:      f.clock,
	})
	if err != nil {
		f.t.Fatalf("app.New: %v", err)
	}
	return a
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func hashOf(t *testing.T, dir string) string {
	t.Helper()
	tree, err := treehash.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tree.SHA
}

func (f *fixture) publish(name string, files map[string]string) string {
	f.t.Helper()
	dir := filepath.Join(f.upstream, "skills", name)
	if err := os.RemoveAll(dir); err != nil {
		f.t.Fatal(err)
	}
	writeTree(f.t, dir, files)
	f.gh.Commit(f.t, repo, "main", f.upstream)
	return hashOf(f.t, dir)
}

func (f *fixture) installLock(names []string, files map[string]map[string]string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, name := range names {
		dir := filepath.Join(f.home.Paths().StoreDir(), name)
		writeTree(f.t, dir, files[name])
		sha := hashOf(f.t, dir)
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    \"" + name + "\": {\n" +
			"      \"source\": \"" + repo + "\",\n" +
			"      \"sourceType\": \"github\",\n" +
			"      \"sourceUrl\": \"https://github.com/" + repo + ".git\",\n" +
			"      \"skillPath\": \"skills/" + name + "/SKILL.md\",\n" +
			"      \"skillFolderHash\": \"" + sha + "\",\n" +
			"      \"installedAt\": \"2026-11-20T00:00:00.000Z\",\n" +
			"      \"updatedAt\": \"2026-11-20T00:00:00.000Z\"\n    }")
	}
	b.WriteString("\n  }\n}\n")
	f.home.WriteFile(".agents/.skill-lock.json", b.String())
}

// setupLocallyModified installs name at registered, overwrites its
// SKILL.md with local, and publishes upstream. Unless publishRegistered,
// upstream never held the registered version.
func (f *fixture) setupLocallyModified(name, registered, local, upstream string, publishRegistered bool) {
	f.t.Helper()
	if publishRegistered {
		f.publish(name, map[string]string{"SKILL.md": registered})
	}
	f.installLock([]string{name}, map[string]map[string]string{
		name: {"SKILL.md": registered},
	})
	writeTree(f.t, filepath.Join(f.home.Paths().StoreDir(), name), map[string]string{"SKILL.md": local})
	f.publish(name, map[string]string{"SKILL.md": upstream})
}

func (f *fixture) setupSkillWithUpdate(name, v1, v2 string) {
	f.t.Helper()
	f.publish(name, map[string]string{"SKILL.md": v1})
	f.installLock([]string{name}, map[string]map[string]string{
		name: {"SKILL.md": v1},
	})
	f.publish(name, map[string]string{"SKILL.md": v2})
}
