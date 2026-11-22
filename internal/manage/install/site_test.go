package install_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/inventory"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// The existing adapters and the engine satisfy the planner's consumer
// interfaces unchanged.
var (
	_ install.Plans       = (*engine.Engine)(nil)
	_ install.StateReader = (*statefile.Store)(nil)
	_ install.LockReader  = (*skillslock.Store)(nil)
	_ install.Stat        = (*fsadapter.FileOps)(nil)
	_ install.Clock       = (*fakes.Clock)(nil)
)

// now is the fake clock's time; npx skills writes it as toISOString does.
var now = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

const nowISO = "2026-11-22T10:00:00.000Z"

// site is a world (a fake home and a fake GitHub) with real adapters, an
// engine and the install planner. With the machine fixture it has the lock
// of this machine; without it, a home where npx skills never ran: a store,
// two link directories and no lock.
type site struct {
	*world
	p       paths.Paths
	git     *fakes.Git
	clock   *fakes.Clock
	harness []data.Harness
	// wrap, when set, wraps the engine's dependencies (fault injection).
	wrap func(engine.Deps) engine.Deps
}

func newSite(t *testing.T, machine bool) *site {
	t.Helper()
	w := newWorld(t)
	s := &site{world: w, p: w.home.Paths(), git: fakes.NewGit(), clock: fakes.NewClock(now)}
	if machine {
		entries := graphtest.Machine(w.home)
		writeLock(t, s.p, entries)
		clone, err := filepath.EvalSymlinks(w.home.Path(graphtest.KarpathyClone))
		must(t, err)
		s.git.AddClone(clone, ports.CloneStatus{
			Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
			Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
		})
	} else {
		w.home.Mkdir(".agents/skills")
		w.home.Mkdir(".claude/skills")
		w.home.Mkdir(".gemini/config/skills")
	}
	var err error
	s.harness, err = data.LoadHarnesses(s.p, s.files())
	must(t, err)
	return s
}

// writeLock writes entries as npx skills writes its lock: version 3, the
// entries in order, two-space indentation, no final newline.
func writeLock(t *testing.T, p paths.Paths, entries []domain.LockEntry) {
	t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"version\": 3,\n  \"skills\": {")
	for i, e := range entries {
		raw, err := json.MarshalIndent(struct {
			Source          string `json:"source"`
			SourceType      string `json:"sourceType"`
			SkillPath       string `json:"skillPath"`
			SkillFolderHash string `json:"skillFolderHash"`
		}{e.Source, e.SourceType, e.SkillPath, e.SkillFolderHash}, "    ", "  ")
		must(t, err)
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    \"" + e.Name + "\": " + string(raw))
	}
	if len(entries) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("},\n  \"dismissed\": {}\n}")
	must(t, os.MkdirAll(filepath.Dir(p.SkillsLockFile()), 0o755))
	must(t, os.WriteFile(p.SkillsLockFile(), []byte(b.String()), 0o644))
}

func (s *site) files() *fsadapter.FileOps { return fsadapter.NewFileOps(s.p.Guard()) }

func (s *site) deps() engine.Deps {
	files := s.files()
	lock := skillslock.New(s.p, files)
	d := engine.Deps{
		Paths: s.p, Files: files, Links: fsadapter.NewLinkOps(s.p.Guard()),
		Lock: lock, DropLock: lock, State: statefile.New(s.p),
		Dirs: fsadapter.NewScanner(s.p.Guard()), Clock: s.clock,
	}
	if s.wrap != nil {
		d = s.wrap(d)
	}
	return d
}

func (s *site) engine() *engine.Engine { return engine.New(s.deps()) }

// clean is an engine with nothing wrapped: what a later process sees.
func (s *site) clean() *engine.Engine {
	saved := s.wrap
	s.wrap = nil
	defer func() { s.wrap = saved }()
	return s.engine()
}

func (s *site) planner() *install.Planner {
	files := s.files()
	return install.NewPlanner(install.Deps{
		Paths: s.p, Harnesses: s.harness, Plans: s.clean(), State: statefile.New(s.p),
		Lock: skillslock.New(s.p, files), Stat: files, Clock: s.clock,
		Dirs: fsadapter.NewScanner(s.p.Guard()),
	})
}

// review parses, resolves and prepares input, as the install flow does.
func (s *site) review(input string) install.Review {
	s.t.Helper()
	rev, err := s.prepare(input)
	if err != nil {
		s.t.Fatalf("Prepare(%q): %v", input, err)
	}
	return rev
}

// graph observes the fake home as it is now.
func (s *site) graph() graph.Graph {
	s.t.Helper()
	files := s.files()
	dirs := fsadapter.NewScanner(s.p.Guard())
	agents, err := data.LoadAgents(s.p, files)
	must(s.t, err)
	profiles, err := data.DiagnosticProfiles()
	must(s.t, err)
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: s.p, Agents: agents, Dirs: dirs, Files: files,
		Lock: skillslock.New(s.p, files), Git: s.git,
	}, nil)
	must(s.t, err)
	g, err := graph.Build(graph.Deps{
		Paths: s.p, Harnesses: s.harness, Profiles: profiles, Inventory: skills,
		Dirs: dirs, Files: files, Links: osLinks{},
	})
	must(s.t, err)
	return g
}

type osLinks struct{}

func (osLinks) Readlink(path string) (string, error) { return os.Readlink(path) }

func (s *site) state() domain.PersistedState {
	s.t.Helper()
	st, err := statefile.New(s.p).Load()
	must(s.t, err)
	return st
}

func (s *site) saveState(st domain.PersistedState) {
	s.t.Helper()
	must(s.t, statefile.New(s.p).Save(st))
}

// lockBytes is the lock file, or nil when there is none.
func (s *site) lockBytes() []byte {
	s.t.Helper()
	data, err := os.ReadFile(s.p.SkillsLockFile())
	if errors.Is(err, iofs.ErrNotExist) {
		return nil
	}
	must(s.t, err)
	return data
}

// everything is the whole fake home, skillhub's own areas included (its
// staging and parking area, its state and snapshots left out: they are
// compared on their own), plus the lock: every path with its type, mode,
// content or link text.
func (s *site) everything() map[string]string {
	s.t.Helper()
	skip := []string{s.p.StateDir(), s.p.CacheDir()}
	out := map[string]string{}
	root := s.p.Home()
	err := filepath.WalkDir(root, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, sk := range skip {
			if p == sk {
				return filepath.SkipDir
			}
			if d.IsDir() && strings.HasPrefix(sk, p+string(filepath.Separator)) && p != root {
				return nil // a parent of skillhub's state or cache, which its stores create
			}
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		desc := info.Mode().String()
		switch {
		case info.Mode()&iofs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			desc += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			desc += " " + hex.EncodeToString(sum[:])
		}
		out[rel] = desc
		return nil
	})
	must(s.t, err)
	return out
}

func diff(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, "-"+k)
		} else if w != v {
			out = append(out, "~"+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "+"+k)
		}
	}
	return out
}

func sameHome(t *testing.T, what string, want, got map[string]string) {
	t.Helper()
	if d := diff(want, got); len(d) > 0 {
		t.Errorf("%s: the home changed: %v", what, d)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return false
	}
	must(t, err)
	return true
}

// indented is an entry written at the depth npx skills writes it in the
// lock.
func indented(entry string) string { return strings.ReplaceAll(entry, "\n", "\n    ") }

// npxEntry is the entry recordGlobalLock of npx skills 1.9.0 writes for a
// GitHub install with no ref (remove.mjs:6680-6691, then addSkillToLock,
// :4496-4500): written here by hand, not by the code under test.
func npxEntry(repo, commit, skillPath, folderSHA string) string {
	return fmt.Sprintf(`{
  "source": %q,
  "sourceType": "github",
  "sourceUrl": "https://github.com/%s.git",
  "commit": %q,
  "skillPath": %q,
  "skillFolderHash": %q,
  "installedAt": %q,
  "updatedAt": %q
}`, repo, repo, commit, skillPath, folderSHA, nowISO, nowISO)
}

// jsStringify is JSON.stringify(JSON.parse(data), null, 2), as npx skills
// writes its lock (remove.mjs:4481-4486), for the values a lock holds:
// keys in order, two-space indentation, strings escaped as V8 escapes them.
// It is written independently of orderedjson.
func jsStringify(t *testing.T, data []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var b strings.Builder
	var value func(depth int) error
	value = func(depth int) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		pad := func(d int) { b.WriteString("\n" + strings.Repeat("  ", d)) }
		switch v := tok.(type) {
		case json.Delim:
			closing := map[json.Delim]string{'{': "}", '[': "]"}[v]
			b.WriteString(string(v))
			n := 0
			for dec.More() {
				if n > 0 {
					b.WriteString(",")
				}
				pad(depth + 1)
				if v == '{' {
					key, err := dec.Token()
					if err != nil {
						return err
					}
					b.WriteString(jsString(key.(string)) + ": ")
				}
				if err := value(depth + 1); err != nil {
					return err
				}
				n++
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			if n > 0 {
				pad(depth)
			}
			b.WriteString(closing)
		case string:
			b.WriteString(jsString(v))
		case json.Number:
			f, err := strconv.ParseFloat(string(v), 64)
			if err != nil {
				return err
			}
			b.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case nil:
			b.WriteString("null")
		}
		return nil
	}
	if err := value(0); err != nil {
		t.Fatalf("JSON.parse: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("JSON.parse: trailing data")
	}
	return b.String()
}

func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
