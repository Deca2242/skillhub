package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestCompareSkillsShowsPathsAgentsAndDiffs(t *testing.T) {
	f := newFixture(t)
	f.installLock(map[string]map[string]string{
		"alpha": {
			"SKILL.md":  "---\nname: alpha\ndescription: Store copy\n---\n# Alpha\nline one\n",
			"only-a.md": "only in first\n",
			"icon.bin":  "\x00\x01\x02",
		},
	})
	claudeDir := f.home.Path(".claude/skills/alpha-copy")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{
		"SKILL.md":  "---\nname: alpha\ndescription: Claude copy\n---\n# Alpha\nline two\n",
		"only-b.md": "only in second\n",
		"icon.bin":  "\x00\xff\x02",
	} {
		if err := os.WriteFile(filepath.Join(claudeDir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	a := f.app
	const firstID = "lock:alpha"
	const secondID = "dir:.claude/skills/alpha-copy"

	t.Run("differing homonyms", func(t *testing.T) {
		got, err := a.CompareSkills(t.Context(), firstID, secondID)
		if err != nil {
			t.Fatalf("CompareSkills: %v", err)
		}
		if got.Identical {
			t.Fatalf("expected Identical=false, got true")
		}
		if got.Name != "alpha" {
			t.Errorf("Name = %q, want alpha", got.Name)
		}
		if got.First.ID != firstID || got.First.Dir == "" || len(got.First.Paths) == 0 {
			t.Errorf("First = %+v, want ID/Dir/Paths", got.First)
		}
		if got.Second.ID != secondID || got.Second.Dir != claudeDir || !slices.Contains(got.Second.Paths, claudeDir) {
			t.Errorf("Second = %+v, want Dir=%q", got.Second, claudeDir)
		}
		if !slices.Contains(got.Second.Agents, "claude-code") {
			t.Errorf("Second.Agents = %v, want claude-code", got.Second.Agents)
		}

		byPath := make(map[string]app.FileDiff, len(got.Diffs))
		for _, d := range got.Diffs {
			byPath[d.Path] = d
		}
		if d, ok := byPath["SKILL.md"]; !ok || d.Change != domain.ChangeModified || !strings.Contains(d.Unified, "-line one") || !strings.Contains(d.Unified, "+line two") {
			t.Errorf("SKILL.md diff = %+v (ok=%v)", d, ok)
		}
		if d, ok := byPath["only-a.md"]; !ok || d.Change != domain.ChangeRemoved {
			t.Errorf("only-a.md diff = %+v (ok=%v)", d, ok)
		}
		if d, ok := byPath["only-b.md"]; !ok || d.Change != domain.ChangeAdded {
			t.Errorf("only-b.md diff = %+v (ok=%v)", d, ok)
		}
		if d, ok := byPath["icon.bin"]; !ok || !d.Binary || d.Unified != "" {
			t.Errorf("icon.bin diff = %+v (ok=%v), want Binary=true", d, ok)
		}
	})

	t.Run("identical skill with itself", func(t *testing.T) {
		got, err := a.CompareSkills(t.Context(), firstID, firstID)
		if err != nil {
			t.Fatalf("CompareSkills: %v", err)
		}
		if !got.Identical || len(got.Diffs) != 0 {
			t.Errorf("expected Identical=true and 0 diffs, got Identical=%v len=%d", got.Identical, len(got.Diffs))
		}
	})

	t.Run("invalid, missing, and non-observed dir ids", func(t *testing.T) {
		outsideDir := f.home.Path("notes/not-a-skill")
		if err := os.MkdirAll(outsideDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outsideDir, "SKILL.md"), []byte("---\nname: alpha\n---\noutside\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, err := a.CompareSkills(t.Context(), "", secondID); !errors.Is(err, app.ErrInvalidRequest) {
			t.Errorf("empty id err = %v, want ErrInvalidRequest", err)
		}
		if _, err := a.CompareSkills(t.Context(), firstID, "dir:missing"); !errors.Is(err, app.ErrSkillNotFound) {
			t.Errorf("missing id err = %v, want ErrSkillNotFound", err)
		}
		if _, err := a.CompareSkills(t.Context(), firstID, "dir:notes/not-a-skill"); !errors.Is(err, app.ErrSkillNotFound) {
			t.Errorf("non-observed dir id err = %v, want ErrSkillNotFound", err)
		}
	})
}

type countingScanner struct {
	inner     ports.SkillDirScanner
	storeDir  string
	storeHits int
}

func (c *countingScanner) Scan(dir string) ([]ports.DirEntry, error) {
	if dir == c.storeDir {
		c.storeHits++
	}
	return c.inner.Scan(dir)
}

func newCompareV2Fixture(t *testing.T, writeLayout func(h *testsupport.Home)) (*testsupport.Home, *app.App, *countingScanner) {
	t.Helper()
	h := testsupport.NewHome(t)
	writeLayout(h)

	p := h.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	agents, err := data.LoadAgents(p, files)
	if err != nil {
		t.Fatalf("data.LoadAgents: %v", err)
	}
	lock := skillslock.New(p, files)
	scanner := &countingScanner{
		inner:    fsadapter.NewScanner(p.Guard()),
		storeDir: p.StoreDir(),
	}
	a, err := app.New(app.Deps{
		Paths:       p,
		Agents:      agents,
		Files:       files,
		Dirs:        scanner,
		Lock:        lock,
		ManagedLock: lock,
		Links:       fsadapter.NewLinkOps(p.Guard()),
		State:       statefile.New(p),
		Settings:    settings.New(p),
		GitHub:      fakes.NewGitHub(),
		Git:         fakes.NewGit(),
		Notifier:    fakes.NewNotifier(),
		Background:  fakes.NewBackgroundService(),
		Clock:       fakes.NewClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)),
		ProcDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return h, a, scanner
}

func TestCompareSkillsV2ResolvesSingleGraphPerIdentity(t *testing.T) {
	h, a, scanner := newCompareV2Fixture(t, func(h *testsupport.Home) {
		storeAlpha := h.Path(".agents/skills/alpha")
		writeTree(t, storeAlpha, map[string]string{
			"SKILL.md":      "---\nname: alpha\ndescription: Store alpha\n---\n# Alpha\nfrom store\n",
			"only-store.md": "store only\n",
		})
		lockJSON := "{\n  \"version\": 3,\n  \"skills\": {\n    \"alpha\": {\n      \"source\": \"acme/skills\",\n      \"sourceType\": \"github\",\n      \"skillPath\": \"skills/alpha/SKILL.md\",\n      \"skillFolderHash\": \"" + hashOf(t, storeAlpha) + "\"\n    }\n  },\n  \"dismissed\": {}\n}\n"
		h.WriteFile(".agents/.skill-lock.json", lockJSON)

		// Live store alias and broken store alias for lock:alpha.
		h.Symlink(".agents/skills/alpha-alias", "alpha")
		h.Symlink(".agents/skills/alpha-broken", "missing-store")

		// Codex reads the store (sees lock:alpha and its live store alias), plus holds a broken same-basename link.
		h.Mkdir(".codex/skills")
		h.Symlink(".codex/skills/alpha", "../../.agents/skills/missing-codex")

		// Devin does not read the store and only holds a broken same-basename link.
		h.Mkdir(".config/devin/skills")
		h.Symlink(".config/devin/skills/alpha", "../../../.agents/skills/missing-devin")

		// Claude Code holds a divergent folder with the exact same basename, its own live alias, and a broken alias.
		writeTree(t, h.Path(".claude/skills/alpha"), map[string]string{
			"SKILL.md":       "---\nname: alpha\ndescription: Claude alpha\n---\n# Alpha\nfrom claude\n",
			"only-claude.md": "claude only\n",
		})
		h.Symlink(".claude/skills/alpha-claude-alias", "alpha")
		h.Symlink(".claude/skills/alpha-broken", "missing-claude")

		// Unobserved directory outside any read root.
		writeTree(t, h.Path("notes/not-a-skill"), map[string]string{
			"SKILL.md": "---\nname: alpha\n---\noutside\n",
		})
	})

	const firstID = "lock:alpha"
	const secondID = "dir:.claude/skills/alpha"

	t.Run("distinct paths and agents per identity from one graph observation", func(t *testing.T) {
		scanner.storeHits = 0
		got, err := a.CompareSkills(t.Context(), firstID, secondID)
		if err != nil {
			t.Fatalf("CompareSkills: %v", err)
		}
		// One observe() scans the store once in inventory.Scan and once in graph.Build (2 total).
		if scanner.storeHits != 2 {
			t.Errorf("store scans = %d, want 2 (single v2 observe)", scanner.storeHits)
		}
		if got.Identical {
			t.Fatalf("Identical = true, want false")
		}
		if got.Name != "alpha" {
			t.Errorf("Name = %q, want alpha", got.Name)
		}

		wantFirstDir := h.Path(".agents/skills/alpha")
		wantFirstPaths := []string{
			h.Path(".agents/skills/alpha"),
			h.Path(".agents/skills/alpha-alias"),
		}
		// Codex reads the store; OpenCode is also detected via .claude/skills and reads the store.
		wantFirstAgents := []string{"codex", "opencode"}
		if got.First.ID != firstID || got.First.Dir != wantFirstDir {
			t.Errorf("First ID/Dir = (%q, %q), want (%q, %q)", got.First.ID, got.First.Dir, firstID, wantFirstDir)
		}
		if !slices.Equal(got.First.Paths, wantFirstPaths) {
			t.Errorf("First.Paths = %v, want %v", got.First.Paths, wantFirstPaths)
		}
		if !slices.Equal(got.First.Agents, wantFirstAgents) {
			t.Errorf("First.Agents = %v, want %v", got.First.Agents, wantFirstAgents)
		}

		wantSecondDir := h.Path(".claude/skills/alpha")
		wantSecondPaths := []string{
			h.Path(".claude/skills/alpha"),
			h.Path(".claude/skills/alpha-claude-alias"),
		}
		// Claude Code and OpenCode both read .claude/skills; Codex and Devin do not see this folder.
		wantSecondAgents := []string{"claude-code", "opencode"}
		if got.Second.ID != secondID || got.Second.Dir != wantSecondDir {
			t.Errorf("Second ID/Dir = (%q, %q), want (%q, %q)", got.Second.ID, got.Second.Dir, secondID, wantSecondDir)
		}
		if !slices.Equal(got.Second.Paths, wantSecondPaths) {
			t.Errorf("Second.Paths = %v, want %v", got.Second.Paths, wantSecondPaths)
		}
		if !slices.Equal(got.Second.Agents, wantSecondAgents) {
			t.Errorf("Second.Agents = %v, want %v", got.Second.Agents, wantSecondAgents)
		}

		byPath := make(map[string]app.FileDiff, len(got.Diffs))
		for _, d := range got.Diffs {
			byPath[d.Path] = d
		}
		if d, ok := byPath["SKILL.md"]; !ok || d.Change != domain.ChangeModified || !strings.Contains(d.Unified, "-from store") || !strings.Contains(d.Unified, "+from claude") {
			t.Errorf("SKILL.md diff = %+v (ok=%v)", d, ok)
		}
		if d, ok := byPath["only-store.md"]; !ok || d.Change != domain.ChangeRemoved {
			t.Errorf("only-store.md diff = %+v (ok=%v)", d, ok)
		}
		if d, ok := byPath["only-claude.md"]; !ok || d.Change != domain.ChangeAdded {
			t.Errorf("only-claude.md diff = %+v (ok=%v)", d, ok)
		}
	})

	t.Run("rejects unknown and non-observed ids without inference", func(t *testing.T) {
		for _, badID := range []string{"dir:missing", "dir:notes/not-a-skill", "lock:missing"} {
			if _, err := a.CompareSkills(t.Context(), firstID, badID); !errors.Is(err, app.ErrSkillNotFound) {
				t.Errorf("CompareSkills(%q, %q) err = %v, want ErrSkillNotFound", firstID, badID, err)
			}
		}
	})

	t.Run("v2 config error does not fall back to v1", func(t *testing.T) {
		badHome, badApp, _ := newCompareV2Fixture(t, func(h *testsupport.Home) {
			storeAlpha := h.Path(".agents/skills/alpha")
			writeTree(t, storeAlpha, map[string]string{
				"SKILL.md": "---\nname: alpha\n---\nstore\n",
			})
		})
		badHome.WriteFile(".config/skillhub/agents.local.toml", "not valid toml = [\n")
		if _, err := badApp.CompareSkills(t.Context(), "dir:.agents/skills/alpha", "dir:.agents/skills/alpha"); err == nil {
			t.Fatal("expected error for malformed agents.local.toml, got nil")
		}
	})
}

func TestCompareSkillsV1FallbackExcludesBrokenLinksAndUnrelatedCopies(t *testing.T) {
	home := testsupport.NewHome(t)
	p := home.Paths()
	files := fsadapter.NewFileOps(p.Guard())
	a, err := app.New(app.Deps{
		Paths: p,
		Agents: []domain.AgentProfile{
			{ID: "claude-code", GlobalDir: ".claude/skills"},
			{ID: "codex", GlobalDir: ".codex/skills"},
			{ID: "gemini-cli", GlobalDir: ".gemini/skills"},
		},
		Files:      files,
		Dirs:       fsadapter.NewScanner(p.Guard()),
		Lock:       skillslock.New(p, files),
		State:      statefile.New(p),
		Settings:   settings.New(p),
		GitHub:     fakes.NewGitHub(),
		Git:        fakes.NewGit(),
		Notifier:   fakes.NewNotifier(),
		Background: fakes.NewBackgroundService(),
		Clock:      fakes.NewClock(t0),
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	storeAlpha := home.Path(".agents/skills/alpha")
	writeTree(t, storeAlpha, map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nstore copy\n",
	})
	lockJSON := "{\n  \"version\": 3,\n  \"skills\": {\n    \"alpha\": {\n      \"source\": \"acme/skills\",\n      \"sourceType\": \"github\",\n      \"skillPath\": \"skills/alpha/SKILL.md\",\n      \"skillFolderHash\": \"" + hashOf(t, storeAlpha) + "\"\n    }\n  },\n  \"dismissed\": {}\n}\n"
	home.WriteFile(".agents/.skill-lock.json", lockJSON)

	// Unrelated divergent copy with same basename in claude-code (merged by name in v1 inventory).
	writeTree(t, home.Path(".claude/skills/alpha"), map[string]string{
		"SKILL.md": "---\nname: alpha\n---\ndivergent claude copy\n",
	})
	// Broken same-basename link in codex (merged by name in v1 inventory).
	home.Mkdir(".codex/skills")
	home.Symlink(".codex/skills/alpha", "../../.agents/skills/missing")
	// Live symlink alias to store alpha in gemini-cli.
	home.Mkdir(".gemini/skills")
	home.Symlink(".gemini/skills/alpha-live", "../../.agents/skills/alpha")
	// Separate observed skill to compare against lock:alpha in v1.
	otherDir := home.Path(".claude/skills/alpha-copy")
	writeTree(t, otherDir, map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nsecond copy\n",
	})

	got, err := a.CompareSkills(t.Context(), "lock:alpha", "dir:.claude/skills/alpha-copy")
	if err != nil {
		t.Fatalf("CompareSkills: %v", err)
	}

	wantFirstPaths := []string{
		storeAlpha,
		home.Path(".gemini/skills/alpha-live"),
	}
	wantFirstAgents := []string{"gemini-cli"}
	if !slices.Equal(got.First.Paths, wantFirstPaths) {
		t.Errorf("First.Paths = %v, want %v", got.First.Paths, wantFirstPaths)
	}
	if !slices.Equal(got.First.Agents, wantFirstAgents) {
		t.Errorf("First.Agents = %v, want %v", got.First.Agents, wantFirstAgents)
	}
	if !slices.Equal(got.Second.Paths, []string{otherDir}) {
		t.Errorf("Second.Paths = %v, want [%s]", got.Second.Paths, otherDir)
	}
	if !slices.Equal(got.Second.Agents, []string{"claude-code"}) {
		t.Errorf("Second.Agents = %v, want [claude-code]", got.Second.Agents)
	}
}
