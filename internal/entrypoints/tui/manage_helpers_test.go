package tui_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

// machine is the home of graphtest.Machine behind an App composed as
// cmd/skillhub composes it, version 2 ports included.
type machine struct {
	t        *testing.T
	h        *testsupport.Home
	p        paths.Paths
	gh       *fakes.GitHub
	clock    *fakes.Clock
	proc     string
	upstream string
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	h := testsupport.NewHome(t)
	entries := graphtest.Machine(h)
	m := &machine{
		t: t, h: h, p: h.Paths(), gh: fakes.NewGitHub(),
		clock: fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
		proc:  t.TempDir(), upstream: filepath.Join(t.TempDir(), "upstream"),
	}
	m.writeLock(entries)
	return m
}

func (m *machine) newApp() *app.App {
	m.t.Helper()
	files := fsadapter.NewFileOps(m.p.Guard())
	agents, err := data.LoadAgents(m.p, files)
	if err != nil {
		m.t.Fatal(err)
	}
	git := fakes.NewGit()
	clone, err := filepath.EvalSymlinks(m.h.Path(graphtest.KarpathyClone))
	if err != nil {
		m.t.Fatal(err)
	}
	git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	lock := skillslock.New(m.p, files)
	a, err := app.New(app.Deps{
		Paths: m.p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(m.p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(m.p.Guard()),
		State: statefile.New(m.p), Settings: settings.New(m.p), GitHub: m.gh, Git: git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: m.clock,
		ProcDir: m.proc,
	})
	if err != nil {
		m.t.Fatalf("app.New: %v", err)
	}
	return a
}

// writeLock writes the lock as npx skills 1.9.0 does.
func (m *machine) writeLock(entries []domain.LockEntry) {
	m.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n    %q: {\n      \"source\": %q,\n      \"sourceType\": %q,\n", e.Name, e.Source, e.SourceType)
		if e.SourceURL != "" {
			fmt.Fprintf(&b, "      \"sourceUrl\": %q,\n", e.SourceURL)
		}
		fmt.Fprintf(&b, "      \"skillPath\": %q,\n      \"skillFolderHash\": %q\n    }", e.SkillPath, e.SkillFolderHash)
	}
	if len(entries) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("},\n  \"dismissed\": {}\n}\n")
	if err := os.MkdirAll(filepath.Dir(m.p.SkillsLockFile()), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(m.p.SkillsLockFile(), []byte(b.String()), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

// publishTool publishes acme/tools with one skill, tidy.
func (m *machine) publishTool() {
	m.t.Helper()
	m.gh.AddRepo("acme/tools", "main")
	writeTree(m.t, filepath.Join(m.upstream, "skills", "tidy"), map[string]string{
		"SKILL.md": "---\nname: tidy\ndescription: tidy up\n---\nbody\nsecond line\n",
	})
	m.gh.Commit(m.t, "acme/tools", "main", m.upstream)
}

// hash is the SHA-256 of every path, mode, file content and link content
// under the home and skillhub's own directories, the install staging
// included: any write changes it.
func (m *machine) hash() string {
	m.t.Helper()
	roots := []string{m.h.Root()}
	for _, d := range []string{m.p.StateDir(), m.p.CacheDir()} {
		if !strings.HasPrefix(d, m.h.Root()+string(filepath.Separator)) {
			roots = append(roots, d)
		}
	}
	sum := sha256.New()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return filepath.SkipDir
			}
			if err != nil {
				return err
			}
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			line := p + " " + info.Mode().String()
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				line += " -> " + target
			case info.Mode().IsRegular():
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				s := sha256.Sum256(data)
				line += " " + hex.EncodeToString(s[:])
			}
			sum.Write([]byte(line + "\n"))
			return nil
		})
		if err != nil {
			m.t.Fatal(err)
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (m *machine) exists(rel string) bool {
	_, err := os.Lstat(m.h.Path(rel))
	return err == nil
}

func (m *machine) persisted() domain.PersistedState {
	m.t.Helper()
	st, err := statefile.New(m.p).Load()
	if err != nil {
		m.t.Fatal(err)
	}
	return st
}

// v2Lines are the journal lines of version 2 operations.
func (m *machine) v2Lines() []domain.HistoryEntry {
	m.t.Helper()
	journal, err := statefile.New(m.p).Journal()
	if err != nil {
		m.t.Fatal(err)
	}
	var out []domain.HistoryEntry
	for _, h := range journal {
		if strings.HasPrefix(h.Target, "v2:") {
			out = append(out, h)
		}
	}
	return out
}

// approve plans and applies req through the app, outside the TUI.
func (m *machine) approve(a *app.App, req app.Request) {
	m.t.Helper()
	p, err := a.Plan(m.t.Context(), req)
	if err != nil {
		m.t.Fatalf("plan %+v: %v", req, err)
	}
	if _, err := a.ApplyPlan(m.t.Context(), req, p.PlanID); err != nil {
		m.t.Fatalf("apply %+v: %v", req, err)
	}
}

var idPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// normalize puts a rendered screen in a form a golden file can hold: the
// home is ~ and the plan identifiers are placeholders. The padding that
// follows a path depends on the temporary home's length, so each line
// loses its trailing spaces and its right border. The screen must be wide
// enough that no path wraps (goldenWidth).
func (m *machine) normalize(s string) []byte {
	s = strings.ReplaceAll(s, m.h.Root(), "~")
	s = idPattern.ReplaceAllString(s, "<plan>")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, " ")
		l = strings.TrimSuffix(l, "│")
		lines[i] = strings.TrimRight(l, " ")
	}
	return []byte(strings.Join(lines, "\n"))
}

// goldenWidth keeps every temporary path of the fixture on one line.
const goldenWidth = 400

// open builds a TUI model over a, with its data loaded, in a terminal tall
// enough to show a whole plan of the fixture's long temporary paths.
func open(t *testing.T, a *app.App) *tui.Model {
	t.Helper()
	return openSized(t, a, 100, 60)
}

func openSized(t *testing.T, a *app.App, width, height int) *tui.Model {
	t.Helper()
	m := tui.NewModel(a)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	runSyncCmd(m, m.Init())
	return m
}

// press sends one key and returns the command it produced, unrun.
func press(m *tui.Model, key string) tea.Cmd {
	next, cmd := m.Update(keyPress(key))
	*m = *next.(*tui.Model)
	return cmd
}

// pressRun sends one key and runs the command it produced to the end.
func pressRun(m *tui.Model, key string) {
	runSyncCmd(m, press(m, key))
}

// typeText types s one rune at a time into a text input. The commands a
// text input returns are its cursor blink, so they are not run.
func typeText(m *tui.Model, s string) {
	for _, r := range s {
		press(m, string(r))
	}
}

// screen is the plain text of the rendered view.
func screen(m *tui.Model) string { return ansi.Strip(m.View().Content) }

// selectSkill filters the Skills screen down to the skill named name.
func selectSkill(t *testing.T, m *tui.Model, name string) {
	t.Helper()
	pressRun(m, "/")
	typeText(m, name)
	pressRun(m, "enter")
	if !strings.Contains(screen(m), name) {
		t.Fatalf("the filter did not keep %s:\n%s", name, screen(m))
	}
}
