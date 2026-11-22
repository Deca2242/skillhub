package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// testAgents is a small agents table for the fixture. Besides real harness
// directories it lists the provider directories of docs/design.md section 2
// as if an overlay had added them, to prove they stay out even then.
var testAgents = []domain.AgentProfile{
	{ID: "claude-code", GlobalDir: ".claude/skills"},
	{ID: "codex", GlobalDir: ".codex/skills"},
	{ID: "pi", GlobalDir: ".pi/agent/skills"},
	{ID: "cline", GlobalDir: ".agents/skills", ReadsUniversal: true},
	{ID: "kilo", GlobalDir: ".kilo/skills"},
	{ID: "cursor-vendor", GlobalDir: ".cursor/skills-cursor"},
	{ID: "codex-tmp", GlobalDir: ".codex/.tmp"},
	{ID: "codex-plugins", GlobalDir: ".codex/plugins"},
	{ID: "claude-plugins", GlobalDir: ".claude/plugins"},
	{ID: "antigravity-brain", GlobalDir: ".gemini/antigravity-cli/brain/abc/scratch"},
}

const zeroSHA = "0000000000000000000000000000000000000000"

func skillMD(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n", name, description, name)
}

func gentleSkillMD(name string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: gentle\nmetadata:\n  author: gentleman-programming\n  version: \"1.0\"\n---\n", name)
}

// fixture is a fake home holding every case of docs/design.md section 2.
type fixture struct {
	home *testsupport.Home
	lock *fakes.SkillsLockStore
	git  *fakes.Git
}

func (f *fixture) hash(t *testing.T, rel string) string {
	t.Helper()
	tree, err := treehash.Dir(f.home.Path(rel))
	if err != nil {
		t.Fatalf("hash %s: %v", rel, err)
	}
	return tree.SHA
}

func (f *fixture) realPath(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(f.home.Path(rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func lockEntry(name, source, folderHash string) domain.LockEntry {
	return domain.LockEntry{
		Name:            name,
		Source:          source,
		SourceType:      "github",
		SkillPath:       "skills/" + name + "/SKILL.md",
		SkillFolderHash: folderHash,
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := testsupport.NewHome(t)
	f := &fixture{home: h, git: fakes.NewGit()}

	// Lock skill, linked from Claude Code.
	h.WriteFile(".agents/skills/find-skills/SKILL.md", skillMD("find-skills", "Find skills"))
	h.Symlink(".claude/skills/find-skills", "../../.agents/skills/find-skills")
	// Lock skill with an identical real copy in another harness: duplicate.
	h.WriteFile(".agents/skills/grill-me/SKILL.md", skillMD("grill-me", "Grill me"))
	h.WriteFile(".codex/skills/grill-me/SKILL.md", skillMD("grill-me", "Grill me"))
	// Lock skill rewritten locally, with a different copy elsewhere:
	// locally modified and divergent copy.
	h.WriteFile(".agents/skills/orca-cli/SKILL.md", skillMD("orca-cli", "Orca"))
	h.WriteFile(".claude/skills/orca-cli/SKILL.md", skillMD("orca-cli", "Orca, older"))
	// Store alias: computer-use is a link to its sibling orca-computer-use.
	h.WriteFile(".agents/skills/orca-computer-use/SKILL.md", skillMD("orca-computer-use", "Computer use"))
	h.Symlink(".agents/skills/computer-use", "orca-computer-use")
	// Pinned lock entry.
	h.WriteFile(".agents/skills/pinned/SKILL.md", skillMD("pinned", "Pinned"))
	// Disabled by hand, with a link left dangling in another harness.
	h.WriteFile(".agents/skills/OFF/accessibility/SKILL.md", skillMD("accessibility", "A11y"))
	h.WriteFile(".agents/skills/OFF/README.md", "parked skills")
	h.Symlink(".pi/agent/skills/accessibility", "../../../.agents/skills/accessibility")
	// Active folder next to a copy in OFF/: same content, and different.
	h.WriteFile(".agents/skills/twin/SKILL.md", skillMD("twin", "Twin"))
	h.WriteFile(".agents/skills/OFF/twin/SKILL.md", skillMD("twin", "Twin"))
	h.WriteFile(".agents/skills/rival/SKILL.md", skillMD("rival", "Rival"))
	h.WriteFile(".agents/skills/OFF/rival/SKILL.md", skillMD("rival", "Rival, parked"))
	// A folder in OFF/ that the lock does not name is not a skill.
	h.WriteFile(".agents/skills/OFF/parked-manual/SKILL.md", skillMD("parked-manual", "Parked"))
	// Gentle-ai copies, and one gentle-ai folder the lock names.
	h.WriteFile(".agents/skills/go-testing/SKILL.md", gentleSkillMD("go-testing"))
	h.WriteFile(".claude/skills/go-testing/SKILL.md", gentleSkillMD("go-testing"))
	h.WriteFile(".agents/skills/work-unit-commits/SKILL.md", gentleSkillMD("work-unit-commits"))
	h.WriteFile(".gentle-ai/state.json", `{"installed_agents":["claude-code"],"components":["engram","skills","persona"]}`)
	// Folders without origin.
	h.WriteFile(".agents/skills/manual/SKILL.md", skillMD("manual", "Manual"))
	h.WriteFile(".agents/skills/_shared/notes.md", "no SKILL.md here")
	h.WriteFile(".agents/skills/secret-notes/SKILL.md", skillMD("secret-notes", "Ignored by name"))
	h.WriteFile(".codex/skills/scratchpad/SKILL.md", skillMD("scratchpad", "Ignored by id"))
	h.Symlink(".pi/agent/skills/orphan", "../../../nowhere/orphan")
	// Same name, two origins: a lock entry and a skill inside a clone.
	h.WriteFile(".agents/skills/planner/SKILL.md", skillMD("planner", "Planner from the lock"))
	h.WriteFile(".claude/skills/planner-repo/skills/planner/SKILL.md", skillMD("planner", "Planner from the clone"))
	h.Mkdir(".claude/skills/planner-repo/.git")
	// A clean GitHub clone with its skill nested, like andrej-karpathy-skills.
	h.WriteFile(".claude/skills/karpathy/README.md", "readme")
	h.WriteFile(".claude/skills/karpathy/skills/karpathy-guidelines/SKILL.md", skillMD("karpathy-guidelines", "Guidelines"))
	h.Mkdir(".claude/skills/karpathy/.git/objects")
	// A dirty clone on a detached HEAD, skill at its root.
	h.WriteFile(".codex/skills/dirty-clone/SKILL.md", skillMD("dirty-clone", "Dirty"))
	h.Mkdir(".codex/skills/dirty-clone/.git")
	// A clone whose origin is not GitHub has no origin.
	h.WriteFile(".codex/skills/gitlab-skill/SKILL.md", skillMD("gitlab-skill", "GitLab"))
	h.Mkdir(".codex/skills/gitlab-skill/.git")
	// Excluded: provider folders, Claude plugins, project skills.
	h.WriteFile(".codex/skills/.system/SKILL.md", skillMD("system", "Provider"))
	h.WriteFile(".claude/skills/synced/SKILL.md", skillMD("synced", "claude.ai"))
	h.WriteFile(".cursor/skills-cursor/cursor-skill/SKILL.md", skillMD("cursor-skill", "Provider"))
	h.WriteFile(".codex/.tmp/tmp-skill/SKILL.md", skillMD("tmp-skill", "Provider"))
	h.WriteFile(".codex/plugins/plugin-skill/SKILL.md", skillMD("plugin-skill", "Provider"))
	h.WriteFile(".claude/plugins/claude-plugin-skill/SKILL.md", skillMD("claude-plugin-skill", "Plugin"))
	h.WriteFile(".claude/plugins/cache/m/p/1.0/skills/cached/SKILL.md", skillMD("cached", "Plugin"))
	h.WriteFile(".gemini/antigravity-cli/brain/abc/scratch/brain-skill/SKILL.md", skillMD("brain-skill", "Noise"))
	h.WriteFile("proj/.claude/skills/project-skill/SKILL.md", skillMD("project-skill", "Project"))
	h.WriteFile("proj/.agents/skills/project-store-skill/SKILL.md", skillMD("project-store-skill", "Project"))

	pinned := lockEntry("pinned", "owner/pinned", f.hash(t, ".agents/skills/pinned"))
	pinned.Ref = "v1.2.0"
	f.lock = fakes.NewSkillsLockStore(
		lockEntry("find-skills", "vercel-labs/skills", f.hash(t, ".agents/skills/find-skills")),
		lockEntry("grill-me", "mattpocock/skills", f.hash(t, ".agents/skills/grill-me")),
		lockEntry("orca-cli", "stablyai/orca", zeroSHA),
		lockEntry("computer-use", "stablyai/orca", f.hash(t, ".agents/skills/orca-computer-use")),
		pinned,
		lockEntry("accessibility", "addyosmani/web-quality-skills", f.hash(t, ".agents/skills/OFF/accessibility")),
		lockEntry("twin", "owner/twin", f.hash(t, ".agents/skills/twin")),
		lockEntry("rival", "owner/rival", f.hash(t, ".agents/skills/rival")),
		lockEntry("work-unit-commits", "owner/gentle", f.hash(t, ".agents/skills/work-unit-commits")),
		lockEntry("planner", "owner/planner", f.hash(t, ".agents/skills/planner")),
		lockEntry("ghost", "owner/ghost", zeroSHA),
	)

	f.git.AddClone(f.realPath(t, ".claude/skills/karpathy"), ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	f.git.AddClone(f.realPath(t, ".claude/skills/planner-repo"), ports.CloneStatus{
		Remote: "git@github.com:owner/planner-repo.git", Branch: "main",
		Upstream: "origin/main", Head: "2222222222222222222222222222222222222222", Clean: true,
	})
	f.git.AddClone(f.realPath(t, ".codex/skills/dirty-clone"), ports.CloneStatus{
		Remote: "ssh://git@github.com/owner/dirty-clone.git", Head: "3333333333333333333333333333333333333333",
	})
	f.git.AddClone(f.realPath(t, ".codex/skills/gitlab-skill"), ports.CloneStatus{
		Remote: "https://gitlab.com/owner/gitlab-skill.git", Branch: "main",
		Upstream: "origin/main", Head: "4444444444444444444444444444444444444444", Clean: true,
	})
	return f
}

func (f *fixture) deps() inventory.Deps {
	guard := f.home.Paths().Guard()
	return inventory.Deps{
		Paths:  f.home.Paths(),
		Agents: testAgents,
		Dirs:   fsadapter.NewScanner(guard),
		Files:  fsadapter.NewFileOps(guard),
		Lock:   f.lock,
		Git:    f.git,
	}
}

// expectedSkill is what the fixture test checks for one row. Locations are
// "<agent> <kind> <home-relative path>", primary first.
type expectedSkill struct {
	name        string
	aliases     []string
	description string
	origin      domain.OriginKind
	ref         string
	cloneRoot   string
	state       domain.State
	issues      []domain.Issue
	locations   []string
}

func TestScanReproducesEveryCaseOfTheDesign(t *testing.T) {
	f := newFixture(t)
	before, err := treehash.Dir(f.home.Root())
	if err != nil {
		t.Fatal(err)
	}

	skills, err := inventory.Scan(context.Background(), f.deps(), []string{"secret-notes", "dir:.codex/skills/scratchpad"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := map[string]expectedSkill{
		"lock:find-skills": {
			name: "find-skills", description: "Find skills", origin: domain.OriginLock, state: domain.StateUnchecked,
			locations: []string{"store real .agents/skills/find-skills", "claude-code symlink .claude/skills/find-skills"},
		},
		"lock:grill-me": {
			name: "grill-me", description: "Grill me", origin: domain.OriginLock, state: domain.StateUnchecked,
			issues:    []domain.Issue{domain.IssueDuplicate},
			locations: []string{"store real .agents/skills/grill-me", "codex real .codex/skills/grill-me"},
		},
		"lock:orca-cli": {
			name: "orca-cli", description: "Orca", origin: domain.OriginLock, state: domain.StateUnchecked,
			issues:    []domain.Issue{domain.IssueLocallyModified, domain.IssueDivergentCopy},
			locations: []string{"store real .agents/skills/orca-cli", "claude-code real .claude/skills/orca-cli"},
		},
		"lock:computer-use": {
			name: "computer-use", aliases: []string{"orca-computer-use"}, description: "Computer use",
			origin: domain.OriginLock, state: domain.StateUnchecked,
			locations: []string{"store symlink .agents/skills/computer-use", "store real .agents/skills/orca-computer-use"},
		},
		"lock:pinned": {
			name: "pinned", description: "Pinned", origin: domain.OriginLock, ref: "v1.2.0", state: domain.StateUnchecked,
			locations: []string{"store real .agents/skills/pinned"},
		},
		"lock:accessibility": {
			name: "accessibility", description: "A11y", origin: domain.OriginLock, state: domain.StateDisabled,
			issues:    []domain.Issue{domain.IssueBrokenLink},
			locations: []string{"store real .agents/skills/OFF/accessibility", "pi broken_link .pi/agent/skills/accessibility"},
		},
		"lock:twin": {
			name: "twin", description: "Twin", origin: domain.OriginLock, state: domain.StateUnchecked,
			issues:    []domain.Issue{domain.IssueDuplicate},
			locations: []string{"store real .agents/skills/twin", "store real .agents/skills/OFF/twin"},
		},
		"lock:rival": {
			name: "rival", description: "Rival", origin: domain.OriginLock, state: domain.StateUnchecked,
			issues:    []domain.Issue{domain.IssueDivergentCopy},
			locations: []string{"store real .agents/skills/rival", "store real .agents/skills/OFF/rival"},
		},
		"lock:work-unit-commits": {
			name: "work-unit-commits", description: "gentle", origin: domain.OriginLock, state: domain.StateUnchecked,
			locations: []string{"store real .agents/skills/work-unit-commits"},
		},
		"lock:planner": {
			name: "planner", description: "Planner from the lock", origin: domain.OriginLock, state: domain.StateUnchecked,
			locations: []string{"store real .agents/skills/planner"},
		},
		"lock:ghost": {
			name: "ghost", origin: domain.OriginLock, state: domain.StateMissing,
		},
		"git:.claude/skills/planner-repo/skills/planner": {
			name: "planner", description: "Planner from the clone", origin: domain.OriginGitClone,
			cloneRoot: ".claude/skills/planner-repo", state: domain.StateUnchecked,
			locations: []string{"claude-code real .claude/skills/planner-repo/skills/planner"},
		},
		"git:.claude/skills/karpathy/skills/karpathy-guidelines": {
			name: "karpathy-guidelines", description: "Guidelines", origin: domain.OriginGitClone,
			cloneRoot: ".claude/skills/karpathy", state: domain.StateUnchecked,
			locations: []string{"claude-code real .claude/skills/karpathy/skills/karpathy-guidelines"},
		},
		"git:.codex/skills/dirty-clone": {
			name: "dirty-clone", description: "Dirty", origin: domain.OriginGitClone,
			cloneRoot: ".codex/skills/dirty-clone", state: domain.StateUnchecked,
			issues:    []domain.Issue{domain.IssueLocallyModified, domain.IssueUntrackedBranch},
			locations: []string{"codex real .codex/skills/dirty-clone"},
		},
		"dir:.codex/skills/gitlab-skill": {
			name: "gitlab-skill", description: "GitLab", origin: domain.OriginNone, state: domain.StateNoOrigin,
			locations: []string{"codex real .codex/skills/gitlab-skill"},
		},
		"dir:.agents/skills/manual": {
			name: "manual", description: "Manual", origin: domain.OriginNone, state: domain.StateNoOrigin,
			locations: []string{"store real .agents/skills/manual"},
		},
		"dir:.pi/agent/skills/orphan": {
			name: "orphan", origin: domain.OriginNone, state: domain.StateNoOrigin,
			issues:    []domain.Issue{domain.IssueBrokenLink},
			locations: []string{"pi broken_link .pi/agent/skills/orphan"},
		},
	}

	var gotIDs []string
	for _, s := range skills {
		gotIDs = append(gotIDs, s.ID)
	}
	var wantIDs []string
	for id := range want {
		wantIDs = append(wantIDs, id)
	}
	slices.Sort(wantIDs)
	if !slices.Equal(sortedCopy(gotIDs), wantIDs) {
		t.Errorf("skill ids:\n got %v\nwant %v", gotIDs, wantIDs)
	}
	if !slices.IsSortedFunc(skills, func(a, b domain.Skill) int {
		if c := compareStrings(a.Name, b.Name); c != 0 {
			return c
		}
		return compareStrings(a.ID, b.ID)
	}) {
		t.Errorf("skills are not sorted by name then id: %v", gotIDs)
	}

	for _, s := range skills {
		w, ok := want[s.ID]
		if !ok {
			continue
		}
		t.Run(s.ID, func(t *testing.T) { checkSkill(t, f, s, w) })
	}

	after, err := treehash.Dir(f.home.Root())
	if err != nil {
		t.Fatal(err)
	}
	if before.SHA != after.SHA {
		t.Error("Scan changed the fake home; the scan must only read")
	}
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

func checkSkill(t *testing.T, f *fixture, s domain.Skill, w expectedSkill) {
	t.Helper()
	if s.Name != w.name {
		t.Errorf("name = %q, want %q", s.Name, w.name)
	}
	if !slices.Equal(s.Aliases, w.aliases) && !(len(s.Aliases) == 0 && len(w.aliases) == 0) {
		t.Errorf("aliases = %v, want %v", s.Aliases, w.aliases)
	}
	if s.Aliases == nil || s.Issues == nil || s.Locations == nil {
		t.Errorf("nil slice in %+v; JSON output must carry [] not null", s)
	}
	if s.Description != w.description {
		t.Errorf("description = %q, want %q", s.Description, w.description)
	}
	if s.Origin.Kind != w.origin {
		t.Errorf("origin = %q, want %q", s.Origin.Kind, w.origin)
	}
	switch w.origin {
	case domain.OriginLock:
		if s.Origin.Lock == nil || s.Origin.GitClone != nil {
			t.Fatalf("lock origin = %+v", s.Origin)
		}
		if s.Origin.Lock.Ref != w.ref {
			t.Errorf("ref = %q, want %q", s.Origin.Lock.Ref, w.ref)
		}
		if s.Origin.Lock.Source == "" || s.Origin.Lock.SkillPath != "skills/"+s.Name+"/SKILL.md" {
			t.Errorf("lock origin = %+v", *s.Origin.Lock)
		}
	case domain.OriginGitClone:
		if s.Origin.GitClone == nil || s.Origin.Lock != nil {
			t.Fatalf("git origin = %+v", s.Origin)
		}
		if root := f.realPath(t, w.cloneRoot); s.Origin.GitClone.Root != root {
			t.Errorf("clone root = %q, want %q", s.Origin.GitClone.Root, root)
		}
		if s.Origin.GitClone.Remote == "" || s.Origin.GitClone.Head == "" {
			t.Errorf("git origin = %+v", *s.Origin.GitClone)
		}
	default:
		if s.Origin.Lock != nil || s.Origin.GitClone != nil {
			t.Errorf("origin none carries data: %+v", s.Origin)
		}
	}
	if s.State != w.state {
		t.Errorf("state = %q, want %q", s.State, w.state)
	}
	if !slices.Equal(s.Issues, w.issues) && !(len(s.Issues) == 0 && len(w.issues) == 0) {
		t.Errorf("issues = %v, want %v", s.Issues, w.issues)
	}
	var locs []string
	for _, l := range s.Locations {
		rel, ok := f.home.Paths().HomeRel(l.Path)
		if !ok {
			t.Errorf("location %q is outside the home", l.Path)
		}
		locs = append(locs, fmt.Sprintf("%s %s %s", l.AgentID, l.Kind, rel))
		switch l.Kind {
		case domain.LocationBrokenLink:
			if l.ContentTreeSHA != "" {
				t.Errorf("broken link %s has a tree SHA", rel)
			}
		default:
			tree, err := treehash.Dir(l.Path)
			if err != nil {
				t.Fatal(err)
			}
			if l.ContentTreeSHA != tree.SHA {
				t.Errorf("location %s tree SHA = %q, want %q", rel, l.ContentTreeSHA, tree.SHA)
			}
		}
	}
	if !slices.Equal(locs, w.locations) {
		t.Errorf("locations:\n got %v\nwant %v", locs, w.locations)
	}
}

func TestScanListsGentleCopiesWhenGentleAIDoesNotManageSkills(t *testing.T) {
	tests := []struct {
		name  string
		state string // "" leaves state.json out
	}{
		{name: "no gentle-ai state", state: ""},
		{name: "skills component absent", state: `{"components":["engram","persona"]}`},
		{name: "unreadable state", state: `{not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := testsupport.NewHome(t)
			h.WriteFile(".agents/skills/go-testing/SKILL.md", gentleSkillMD("go-testing"))
			if tt.state != "" {
				h.WriteFile(".gentle-ai/state.json", tt.state)
			}
			guard := h.Paths().Guard()
			skills, err := inventory.Scan(context.Background(), inventory.Deps{
				Paths: h.Paths(), Agents: testAgents, Dirs: fsadapter.NewScanner(guard),
				Files: fsadapter.NewFileOps(guard), Lock: fakes.NewSkillsLockStore(), Git: fakes.NewGit(),
			}, nil)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(skills) != 1 || skills[0].ID != "dir:.agents/skills/go-testing" {
				t.Errorf("skills = %+v, want only dir:.agents/skills/go-testing", skills)
			}
		})
	}
}

func TestScanWithAnEmptyHome(t *testing.T) {
	h := testsupport.NewHome(t)
	guard := h.Paths().Guard()
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: h.Paths(), Agents: testAgents, Dirs: fsadapter.NewScanner(guard),
		Files: fsadapter.NewFileOps(guard), Lock: fakes.NewSkillsLockStore(), Git: fakes.NewGit(),
	}, nil)
	if err != nil || skills == nil || len(skills) != 0 {
		t.Errorf("Scan of an empty home = %v, %v; want an empty, non-nil list", skills, err)
	}
}

func TestScanReportsFailures(t *testing.T) {
	injected := errors.New("injected")
	tests := []struct {
		name      string
		breakDeps func(d *inventory.Deps, f *fixture)
		want      error
	}{
		{
			name: "lock read fails",
			breakDeps: func(d *inventory.Deps, f *fixture) {
				f.lock.Fail = func(fakes.Call) error { return injected }
			},
			want: injected,
		},
		{
			name: "describing a clone fails",
			breakDeps: func(d *inventory.Deps, f *fixture) {
				f.git.Fail = func(c fakes.Call) error { return injected }
			},
			want: injected,
		},
		{
			name:      "missing dependency",
			breakDeps: func(d *inventory.Deps, f *fixture) { d.Git = nil },
			want:      inventory.ErrMissingDependency,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			d := f.deps()
			tt.breakDeps(&d, f)
			skills, err := inventory.Scan(context.Background(), d, nil)
			if !errors.Is(err, tt.want) || skills != nil {
				t.Errorf("Scan = %d skills, %v; want nil, %v", len(skills), err, tt.want)
			}
		})
	}
}
