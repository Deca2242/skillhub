package app_test

import (
	"fmt"
	"io/fs"
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
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

const repo = "acme/skills"

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// fixture is a fake home with the real file, lock, state and settings
// adapters, and doubles for GitHub, git, the notifier, the timer and the
// clock.
type fixture struct {
	t        *testing.T
	home     *testsupport.Home
	gh       *fakes.GitHub
	git      *fakes.Git
	notifier *fakes.Notifier
	bg       *fakes.BackgroundService
	clock    *fakes.Clock
	state    *statefile.Store
	settings *settings.Store
	app      *app.App
	upstream string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := testsupport.NewHome(t)
	f := &fixture{
		t: t, home: home,
		gh: fakes.NewGitHub(), git: fakes.NewGit(), notifier: fakes.NewNotifier(),
		bg: fakes.NewBackgroundService(), clock: fakes.NewClock(t0),
		state: statefile.New(home.Paths()), settings: settings.New(home.Paths()),
		upstream: filepath.Join(t.TempDir(), "upstream"),
	}
	f.gh.AddRepo(repo, "main")
	p := home.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	a, err := app.New(app.Deps{
		Paths:  p,
		Agents: []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
		Files:  files, Dirs: fsadapter.NewScanner(p.Guard()), Lock: skillslock.New(p, files),
		State: f.state, Settings: f.settings, GitHub: f.gh, Git: f.git,
		Notifier: f.notifier, Background: f.bg, Clock: f.clock,
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	f.app = a
	return f
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

// publish commits files as upstream's skills/<name> on main.
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

// installLock installs files as the store folder of name and records it in
// the lock, the way npx skills does.
func (f *fixture) installLock(names map[string]map[string]string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	i := 0
	for name, files := range names {
		dir := filepath.Join(f.home.Paths().StoreDir(), name)
		writeTree(f.t, dir, files)
		if i > 0 {
			b.WriteString(",")
		}
		i++
		fmt.Fprintf(&b, "\n    %q: {\n      \"source\": %q,\n      \"sourceType\": \"github\",\n", name, repo)
		fmt.Fprintf(&b, "      \"sourceUrl\": \"https://github.com/%s.git\",\n", repo)
		fmt.Fprintf(&b, "      \"skillPath\": \"skills/%s/SKILL.md\",\n      \"skillFolderHash\": %q,\n", name, hashOf(f.t, dir))
		b.WriteString("      \"installedAt\": \"2026-01-01T00:00:00.000Z\",\n      \"updatedAt\": \"2026-01-02T00:00:00.000Z\"\n    }")
	}
	b.WriteString("\n  }\n}\n")
	f.home.WriteFile(".agents/.skill-lock.json", b.String())
}

var (
	v1 = map[string]string{"SKILL.md": "---\nname: alpha\ndescription: v1\n---\nbody one\n"}
	v2 = map[string]string{"SKILL.md": "---\nname: alpha\ndescription: v2\n---\nbody two\n"}
)

// standard installs alpha at v1 and publishes v1 then v2 upstream.
func (f *fixture) standard() {
	f.t.Helper()
	f.publish("alpha", v1)
	f.installLock(map[string]map[string]string{"alpha": v1})
	f.publish("alpha", v2)
}

func (f *fixture) persisted() domain.PersistedState {
	f.t.Helper()
	st, err := f.state.Load()
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fixture) record(op domain.Operation) {
	f.t.Helper()
	if err := f.state.RecordOperation(op); err != nil {
		f.t.Fatal(err)
	}
}

// homeTree records every directory, file and link under the home except
// skillhub's own cache and state: mode and content, byte for byte.
func (f *fixture) homeTree() map[string]string {
	f.t.Helper()
	root := f.home.Root()
	own := []string{f.home.Paths().CacheDir(), f.home.Paths().StateDir()}
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, o := range own {
			if p == o {
				return filepath.SkipDir
			}
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "link " + target
		case info.IsDir():
			// A parent of skillhub's own directories may appear with them.
			for _, o := range own {
				if strings.HasPrefix(o, p+string(filepath.Separator)) {
					return nil
				}
			}
			out[rel] = "dir " + info.Mode().String()
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = info.Mode().String() + " " + string(data)
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// githubRemote is a clone status the inventory takes as a GitHub clone.
var githubRemote = ports.CloneStatus{
	Remote: "https://github.com/acme/other.git", Branch: "main", Upstream: "origin/main", Head: "abc123", Clean: true,
}
