package graph_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// osLinks reads links straight from the disk, for tests only: the graph
// receives any value with Readlink.
type osLinks struct{}

func (osLinks) Readlink(path string) (string, error) { return os.Readlink(path) }

// build scans the fake home with the inventory and builds the graph from it.
func build(t *testing.T, h *testsupport.Home, lock []domain.LockEntry, git *fakes.Git) graph.Graph {
	t.Helper()
	files := fsadapter.NewFileOps(h.Paths().Guard())
	dirs := fsadapter.NewScanner(h.Paths().Guard())
	agents, err := data.LoadAgents(h.Paths(), files)
	if err != nil {
		t.Fatalf("LoadAgents() error = %v", err)
	}
	harnesses, err := data.LoadHarnesses(h.Paths(), files)
	if err != nil {
		t.Fatalf("LoadHarnesses() error = %v", err)
	}
	profiles, err := data.DiagnosticProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if git == nil {
		git = fakes.NewGit()
	}
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: h.Paths(), Agents: agents, Dirs: dirs, Files: files,
		Lock: fakes.NewSkillsLockStore(lock...), Git: git,
	}, nil)
	if err != nil {
		t.Fatalf("inventory.Scan() error = %v", err)
	}
	g, err := graph.Build(graph.Deps{
		Paths: h.Paths(), Harnesses: harnesses, Profiles: profiles, Inventory: skills,
		Dirs: dirs, Files: files, Links: osLinks{},
	})
	if err != nil {
		t.Fatalf("graph.Build() error = %v", err)
	}
	return g
}

func real(t *testing.T, h *testsupport.Home, rel string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(h.Path(rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func skillByID(t *testing.T, g graph.Graph, id string) graph.Skill {
	t.Helper()
	for _, s := range g.Skills {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("skill %q not in the graph; skills: %v", id, ids(g))
	return graph.Skill{}
}

func ids(g graph.Graph) []string {
	var out []string
	for _, s := range g.Skills {
		out = append(out, s.ID)
	}
	return out
}

func skillsNamed(g graph.Graph, name string) []graph.Skill {
	var out []graph.Skill
	for _, s := range g.Skills {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// seen summarizes a skill's visibility as harness -> kinds.
func seen(s graph.Skill) map[string][]graph.Kind {
	out := map[string][]graph.Kind{}
	for _, v := range s.Visibility {
		if !slices.Contains(out[v.Harness], v.Kind) {
			out[v.Harness] = append(out[v.Harness], v.Kind)
		}
	}
	for _, k := range out {
		slices.Sort(k)
	}
	return out
}

func machine(t *testing.T) (*testsupport.Home, graph.Graph) {
	t.Helper()
	h := testsupport.NewHome(t)
	lock := graphtest.Machine(h)
	git := fakes.NewGit()
	git.AddClone(real(t, h, graphtest.KarpathyClone), ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	return h, build(t, h, lock, git)
}

func TestNestedOFFIsVisibleInTheHarnessesThatReadSubfolders(t *testing.T) {
	h, g := machine(t)
	for _, name := range graphtest.OffSkills {
		s := skillByID(t, g, domain.LockID(name))
		if s.LockKey != name || s.Canonical != real(t, h, ".agents/skills/OFF/"+name) || s.State != domain.StateDisabled {
			t.Errorf("%s: key %q canonical %q state %q", name, s.LockKey, s.Canonical, s.State)
		}
		got := seen(s)
		for _, id := range []string{"codex", "pi"} {
			if !slices.Equal(got[id], []graph.Kind{graph.Direct}) {
				t.Errorf("%s in %s: %v, want direct through the store", name, id, got[id])
			}
		}
		// OpenCode reads the store and also ~/.claude/skills, whose OFF/
		// links it may or may not follow.
		if !slices.Equal(got["opencode"], []graph.Kind{graph.Direct, graph.Indeterminate}) {
			t.Errorf("%s in opencode: %v, want direct and indeterminate", name, got["opencode"])
		}
		// One level only: neither OFF/ of the store nor the OFF/ links of
		// ~/.claude/skills reach them.
		for _, id := range []string{"claude-code", "gemini-cli"} {
			if k, ok := got[id]; ok {
				t.Errorf("%s in %s: %v, want not visible", name, id, k)
			}
		}
		// The store's depth is not verified for Cursor.
		if !slices.Equal(got["cursor"], []graph.Kind{graph.Indeterminate}) {
			t.Errorf("%s in cursor: %v, want indeterminate", name, got["cursor"])
		}
	}
}

func TestAliasAndFrontmatterNameAreKeptApart(t *testing.T) {
	h, g := machine(t)
	s := skillByID(t, g, domain.LockID("computer-use"))
	if s.LockKey != "computer-use" || s.Name != "orca-computer-use" {
		t.Errorf("computer-use: key %q name %q, want computer-use and orca-computer-use", s.LockKey, s.Name)
	}
	if s.Canonical != real(t, h, ".agents/skills/orca-computer-use") {
		t.Errorf("canonical = %q, want the real folder orca-computer-use", s.Canonical)
	}
	if !slices.Equal(s.Aliases, []string{h.Path(".agents/skills/computer-use")}) {
		t.Errorf("aliases = %v, want the store link computer-use", s.Aliases)
	}
	if got := seen(s); !slices.Equal(got["claude-code"], []graph.Kind{graph.ViaLink}) {
		t.Errorf("claude-code sees computer-use as %v, want through a link", got["claude-code"])
	}
	// The real folder is one skill, not a second one beside the alias.
	for _, other := range g.Skills {
		if other.ID != s.ID && other.Canonical == s.Canonical {
			t.Errorf("skill %q shares the canonical folder %q", other.ID, s.Canonical)
		}
	}
	// The link chain is recorded hop by hop.
	link := linkAt(t, g, h.Path(".claude/skills/computer-use"))
	want := []string{h.Path(".agents/skills/computer-use"), h.Path(".agents/skills/orca-computer-use")}
	if link.Status != graph.LinkOK || !slices.Equal(link.Hops, want) || link.Folder != s.Canonical {
		t.Errorf("link = %+v, want ok through %v", link, want)
	}
}

func linkAt(t *testing.T, g graph.Graph, path string) graph.Link {
	t.Helper()
	for _, l := range g.Links {
		if l.Path == path {
			return l
		}
	}
	t.Fatalf("no link at %s", path)
	return graph.Link{}
}

func TestBrokenLinksKeepTheirLexicalTarget(t *testing.T) {
	h, g := machine(t)
	var broken []graph.Link
	for _, l := range g.Links {
		if l.Status == graph.LinkBroken {
			broken = append(broken, l)
		}
	}
	if len(broken) != len(graphtest.BrokenLinks) {
		t.Fatalf("%d broken links, want %d", len(broken), len(graphtest.BrokenLinks))
	}
	for rel, text := range graphtest.BrokenLinks {
		l := linkAt(t, g, h.Path(rel))
		want := filepath.Join(filepath.Dir(h.Path(rel)), text)
		if l.Status != graph.LinkBroken || l.Text != text || l.Final != want || l.Folder != "" {
			t.Errorf("link %s = %+v, want broken towards %s", rel, l, want)
		}
	}
}

func TestSameNameIsNeverTheSameSkill(t *testing.T) {
	h, g := machine(t)
	// requirements-elicitation: identical real copies and none in the
	// store, so one skill without a canonical folder.
	req := skillsNamed(g, "requirements-elicitation")
	if len(req) != 1 || req[0].Canonical != "" || len(req[0].Folders) != 5 || req[0].Origin != domain.OriginNone {
		t.Fatalf("requirements-elicitation = %+v, want one skill without canonical and its 5 observed copies", req)
	}
	if got := seen(req[0]); !slices.Equal(got["gemini-cli"], []graph.Kind{graph.OwnCopy}) {
		t.Errorf("gemini-cli sees requirements-elicitation as %v, want its own copy", got["gemini-cli"])
	}
	// orca-cli of the store and of the read-only profile share a name and
	// differ: two skills, nothing merged.
	orca := skillsNamed(g, "orca-cli")
	if len(orca) != 2 {
		t.Fatalf("orca-cli = %d skills, want 2", len(orca))
	}
	for _, f := range orca[0].Folders {
		if slices.Contains(orca[1].Folders, f) {
			t.Errorf("orca-cli homonyms share the folder %s", f)
		}
	}
	if orca[0].Zone != graph.ZoneProfile || orca[1].Canonical != real(t, h, ".agents/skills/orca-cli") {
		t.Errorf("orca-cli = %+v, want the profile's skill and the store's", orca)
	}
}

func TestHomonymsWithoutAnyLockStayApart(t *testing.T) {
	h := testsupport.NewHome(t)
	// Two folders with the same frontmatter name and different content, in
	// two harnesses, neither in the lock.
	h.WriteFile(".claude/skills/planner/SKILL.md", graphtest.SkillMD("planner", "one"))
	h.WriteFile(".codex/skills/planner/SKILL.md", graphtest.SkillMD("planner", "two"))
	g := build(t, h, nil, nil)
	got := skillsNamed(g, "planner")
	if len(got) != 2 {
		t.Fatalf("planner = %d skills, want 2 apart: %+v", len(got), got)
	}
	for _, s := range got {
		if len(s.Folders) != 1 || s.Canonical != "" {
			t.Errorf("planner skill %+v, want one folder and no canonical (outside the store)", s)
		}
	}
}

func TestAnIdenticalCopyIsSeenAsAnOwnCopy(t *testing.T) {
	h := testsupport.NewHome(t)
	h.WriteFile(".agents/skills/grill-me/SKILL.md", graphtest.SkillMD("grill-me", "same"))
	h.WriteFile(".claude/skills/grill-me/SKILL.md", graphtest.SkillMD("grill-me", "same"))
	h.Mkdir(".codex/skills") // so Codex is detected
	tree, err := treehash.Dir(h.Path(".agents/skills/grill-me"))
	if err != nil {
		t.Fatal(err)
	}
	g := build(t, h, []domain.LockEntry{{Name: "grill-me", Source: "o/r", SourceType: "github", SkillPath: "skills/grill-me/SKILL.md", SkillFolderHash: tree.SHA}}, nil)
	s := skillByID(t, g, domain.LockID("grill-me"))
	want := []string{real(t, h, ".agents/skills/grill-me"), real(t, h, ".claude/skills/grill-me")}
	if !slices.Equal(s.Folders, want) {
		t.Errorf("folders = %v, want the canonical then the copy %v", s.Folders, want)
	}
	got := seen(s)
	if !slices.Equal(got["claude-code"], []graph.Kind{graph.OwnCopy}) || !slices.Equal(got["codex"], []graph.Kind{graph.Direct}) ||
		!slices.Equal(got["opencode"], []graph.Kind{graph.OwnCopy, graph.Direct}) {
		t.Errorf("visibility = %v, want claude-code by its own copy, codex direct, opencode both", got)
	}
	if len(g.Skills) != 1 {
		t.Errorf("skills = %v, want only grill-me", ids(g))
	}
}

func TestForeignZonesAreBoundariesNotSkills(t *testing.T) {
	h, g := machine(t)
	zones := map[string]graph.Zone{}
	for _, b := range g.Boundary {
		zones[b.Path] = b.Zone
	}
	want := map[string]graph.Zone{
		".claude/skills/synced":            graph.ZoneProvider,
		".codex/skills/.system":            graph.ZoneProvider,
		graphtest.KarpathyClone:            graph.ZonePlugin,
		".agents/skills/_shared":           graph.ZoneShared,
		".agents/skills/OFF/README.md":     graph.ZoneFile,
		".agents/skills/go-testing":        graph.ZoneGentle,
		".gemini/config/skills/go-testing": graph.ZoneGentle,
	}
	for rel, zone := range want {
		if zones[h.Path(rel)] != zone {
			t.Errorf("boundary %s = %q, want %q", rel, zones[h.Path(rel)], zone)
		}
	}
	for _, s := range g.Skills {
		if s.Name == "go-testing" || s.Name == "pdf" || s.Name == "skill-creator" {
			t.Errorf("foreign folder became the skill %+v", s)
		}
	}
	// The plugin's skill comes from the inventory as a clone, and is not
	// manageable.
	k := skillByID(t, g, domain.GitID(graphtest.KarpathySkill))
	if k.Zone != graph.ZonePlugin || k.Canonical != real(t, h, graphtest.KarpathySkill) {
		t.Errorf("karpathy = zone %q canonical %q, want plugin", k.Zone, k.Canonical)
	}
}

func TestLinksAreNeverFollowedOutsideTheKnownRoots(t *testing.T) {
	h := testsupport.NewHome(t)
	h.WriteFile("projects/elsewhere/SKILL.md", graphtest.SkillMD("elsewhere", "foreign folder"))
	h.Symlink(".claude/skills/elsewhere", "../../projects/elsewhere")
	h.Symlink(".claude/skills/loop-a", "loop-b")
	h.Symlink(".claude/skills/loop-b", "loop-a")
	g := build(t, h, nil, nil)

	out := linkAt(t, g, h.Path(".claude/skills/elsewhere"))
	if out.Status != graph.LinkOutside || out.Folder != "" || out.Final != h.Path("projects/elsewhere") {
		t.Errorf("outside link = %+v, want outside, unfollowed", out)
	}
	if len(skillsNamed(g, "elsewhere")) != 0 {
		t.Errorf("a folder outside the known roots became a skill")
	}
	for _, rel := range []string{".claude/skills/loop-a", ".claude/skills/loop-b"} {
		if l := linkAt(t, g, h.Path(rel)); l.Status != graph.LinkCycle {
			t.Errorf("%s = %+v, want a cycle", rel, l)
		}
	}
}

func TestTheReadOnlyProfileIsDiagnostic(t *testing.T) {
	h, g := machine(t)
	s := skillByID(t, g, domain.DirID(".claude-omni/skills/find-skills"))
	if s.Profile != "claude-omni" || s.Zone != graph.ZoneProfile || s.Canonical != "" {
		t.Errorf("profile skill = %+v, want profile claude-omni, zone diagnostic, no canonical", s)
	}
	if got := seen(s); !slices.Equal(got["claude-omni"], []graph.Kind{graph.Direct}) || len(got) != 1 {
		t.Errorf("profile visibility = %v, want only the profile", got)
	}
	// Its content equals the store's find-skills, and still it is not a
	// copy of it.
	store := skillByID(t, g, domain.LockID("find-skills"))
	if slices.Contains(store.Folders, real(t, h, ".claude-omni/skills/find-skills")) {
		t.Errorf("the profile's folder joined the store skill: %v", store.Folders)
	}
}

func TestPresentHarnessesAndOrdering(t *testing.T) {
	_, g := machine(t)
	for _, id := range []string{"claude-code", "codex", "opencode", "pi", "gemini-cli", "antigravity", "antigravity-cli", "kilo", "kiro-cli", "droid", "qwen-code", "junie", "devin", "cursor", "github-copilot"} {
		if !slices.Contains(g.Harnesses, id) {
			t.Errorf("harness %q not detected", id)
		}
	}
	// Harnesses whose only root is the store cannot be detected.
	for _, id := range []string{"cline", "dexto", "zed"} {
		if slices.Contains(g.Harnesses, id) {
			t.Errorf("harness %q detected without a directory of its own", id)
		}
	}
	if !slices.IsSortedFunc(g.Skills, func(a, b graph.Skill) int { return compare(a.ID, b.ID) }) {
		t.Error("skills are not sorted by id")
	}
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestBuildingTheGraphWritesNothing(t *testing.T) {
	h := testsupport.NewHome(t)
	lock := graphtest.Machine(h)
	git := fakes.NewGit()
	git.AddClone(real(t, h, graphtest.KarpathyClone), ports.CloneStatus{Remote: "https://github.com/o/r.git", Branch: "main", Upstream: "origin/main", Clean: true})
	before, err := treehash.Dir(h.Root())
	if err != nil {
		t.Fatal(err)
	}
	build(t, h, lock, git)
	after, err := treehash.Dir(h.Root())
	if err != nil {
		t.Fatal(err)
	}
	if before.SHA != after.SHA {
		t.Error("building the graph changed the home")
	}
}

func TestACopyIsNeverAttachedToAForeignZone(t *testing.T) {
	h := testsupport.NewHome(t)
	// A plugin clone, and a real folder elsewhere with the same content as
	// its skill.
	h.WriteFile(graphtest.KarpathyClone+"/.claude-plugin/plugin.json", "{}")
	h.WriteFile(graphtest.KarpathySkill+"/SKILL.md", graphtest.SkillMD("karpathy-guidelines", "plugin"))
	h.Mkdir(graphtest.KarpathyClone + "/.git")
	h.WriteFile(".codex/skills/karpathy-guidelines/SKILL.md", graphtest.SkillMD("karpathy-guidelines", "plugin"))
	git := fakes.NewGit()
	git.AddClone(real(t, h, graphtest.KarpathyClone), ports.CloneStatus{Remote: "https://github.com/o/r.git", Branch: "main", Upstream: "origin/main", Clean: true})
	g := build(t, h, nil, git)

	plugin := skillByID(t, g, domain.GitID(graphtest.KarpathySkill))
	if plugin.Zone != graph.ZonePlugin || len(plugin.Folders) != 1 {
		t.Errorf("plugin skill = %+v, want only its own folder", plugin)
	}
	own := skillByID(t, g, domain.DirID(".codex/skills/karpathy-guidelines"))
	if own.Zone != graph.ZoneNone || !slices.Equal(own.Folders, []string{real(t, h, ".codex/skills/karpathy-guidelines")}) {
		t.Errorf("codex folder = %+v, want a skill of its own", own)
	}
}

type failingGitDirs struct {
	inner   graph.DirLister
	failDir string
	err     error
}

func (f failingGitDirs) Scan(dir string) ([]ports.DirEntry, error) {
	if dir == f.failDir {
		return nil, f.err
	}
	return f.inner.Scan(dir)
}

func folderByEntry(t *testing.T, g graph.Graph, entry string) graph.Folder {
	t.Helper()
	for _, f := range g.Folders {
		if f.Entry == entry || slices.Contains(f.Entries, entry) {
			return f
		}
	}
	t.Fatalf("folder %q not in graph", entry)
	return graph.Folder{}
}

func TestFolderGitEvidence(t *testing.T) {
	if (graph.Folder{}).Git != graph.GitUnknown {
		t.Fatalf("zero-value Folder.Git = %v, want GitUnknown", (graph.Folder{}).Git)
	}

	t.Run("clean manual folder is GitNone", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".claude/skills/humanizer/SKILL.md", graphtest.SkillMD("humanizer", "manual"))
		g := build(t, h, nil, nil)
		f := folderByEntry(t, g, h.Path(".claude/skills/humanizer"))
		if f.Git != graph.GitNone {
			t.Errorf("Git = %v, want GitNone", f.Git)
		}
	})

	t.Run(".git marker file at folder is GitPresent", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".claude/skills/wt-skill/SKILL.md", graphtest.SkillMD("wt-skill", "worktree"))
		h.WriteFile(".claude/skills/wt-skill/.git", "gitdir: /some/repo/.git/worktrees/wt-skill\n")
		g := build(t, h, nil, nil)
		f := folderByEntry(t, g, h.Path(".claude/skills/wt-skill"))
		if f.Git != graph.GitPresent {
			t.Errorf("Git = %v, want GitPresent", f.Git)
		}
	})

	t.Run(".git marker at configured root is GitPresent", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".claude/skills/root-skill/SKILL.md", graphtest.SkillMD("root-skill", "under git root"))
		h.WriteFile(".claude/skills/.git", "gitdir: ../../.git/modules/skills\n")
		g := build(t, h, nil, nil)
		f := folderByEntry(t, g, h.Path(".claude/skills/root-skill"))
		if f.Git != graph.GitPresent {
			t.Errorf("Git = %v, want GitPresent", f.Git)
		}
	})

	t.Run(".git at resolved ancestor of symlinked root is GitPresent", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile("dotfiles/claude/skills/linked-skill/SKILL.md", graphtest.SkillMD("linked-skill", "in dotfiles"))
		h.Mkdir("dotfiles/.git/objects")
		h.Symlink(".claude/skills", h.Path("dotfiles/claude/skills"))
		g := build(t, h, nil, nil)
		f := folderByEntry(t, g, h.Path(".claude/skills/linked-skill"))
		if f.Git != graph.GitPresent {
			t.Errorf("Git = %v, want GitPresent", f.Git)
		}
	})

	t.Run("nested .git inside folder is GitPresent", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".claude/skills/nested-skill/SKILL.md", graphtest.SkillMD("nested-skill", "nested git"))
		h.Mkdir(".claude/skills/nested-skill/vendor/sub/.git/objects")
		g := build(t, h, nil, nil)
		f := folderByEntry(t, g, h.Path(".claude/skills/nested-skill"))
		if f.Git != graph.GitPresent {
			t.Errorf("Git = %v, want GitPresent", f.Git)
		}
	})

	t.Run("incomplete Exists check yields GitUnknown", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".claude/skills/manual-skill/SKILL.md", graphtest.SkillMD("manual-skill", "manual"))
		realFiles := fsadapter.NewFileOps(h.Paths().Guard())
		files := &fakes.FailingFileOps{
			Inner: realFiles,
			Fail: func(c fakes.Call) error {
				if c.Op == "Exists" && len(c.Args) > 0 && filepath.Base(c.Args[0]) == ".git" {
					return errors.New("permission denied")
				}
				return nil
			},
		}
		dirs := fsadapter.NewScanner(h.Paths().Guard())
		harnesses, err := data.LoadHarnesses(h.Paths(), realFiles)
		if err != nil {
			t.Fatal(err)
		}
		profiles, err := data.DiagnosticProfiles()
		if err != nil {
			t.Fatal(err)
		}
		g, err := graph.Build(graph.Deps{
			Paths: h.Paths(), Harnesses: harnesses, Profiles: profiles,
			Dirs: dirs, Files: files, Links: osLinks{},
		})
		if err != nil {
			t.Fatalf("graph.Build() error = %v", err)
		}
		f := folderByEntry(t, g, h.Path(".claude/skills/manual-skill"))
		if f.Git != graph.GitUnknown {
			t.Errorf("Git = %v, want GitUnknown", f.Git)
		}
	})

	t.Run("incomplete nested Scan yields GitUnknown", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".claude/skills/manual-skill/SKILL.md", graphtest.SkillMD("manual-skill", "manual"))
		h.Mkdir(".claude/skills/manual-skill/sub")
		files := fsadapter.NewFileOps(h.Paths().Guard())
		realDirs := fsadapter.NewScanner(h.Paths().Guard())
		dirs := failingGitDirs{
			inner:   realDirs,
			failDir: real(t, h, ".claude/skills/manual-skill/sub"),
			err:     errors.New("unreadable"),
		}
		harnesses, err := data.LoadHarnesses(h.Paths(), files)
		if err != nil {
			t.Fatal(err)
		}
		profiles, err := data.DiagnosticProfiles()
		if err != nil {
			t.Fatal(err)
		}
		g, err := graph.Build(graph.Deps{
			Paths: h.Paths(), Harnesses: harnesses, Profiles: profiles,
			Dirs: dirs, Files: files, Links: osLinks{},
		})
		if err != nil {
			t.Fatalf("graph.Build() error = %v", err)
		}
		f := folderByEntry(t, g, h.Path(".claude/skills/manual-skill"))
		if f.Git != graph.GitUnknown {
			t.Errorf("Git = %v, want GitUnknown", f.Git)
		}
	})

	t.Run("nested tree deeper than maxWalk yields GitUnknown", func(t *testing.T) {
		h := testsupport.NewHome(t)
		h.WriteFile(".claude/skills/deep-skill/SKILL.md", graphtest.SkillMD("deep-skill", "deep"))
		deep := ".claude/skills/deep-skill"
		for range 17 {
			deep = filepath.Join(deep, "d")
		}
		h.Mkdir(deep)
		g := build(t, h, nil, nil)
		f := folderByEntry(t, g, h.Path(".claude/skills/deep-skill"))
		if f.Git != graph.GitUnknown {
			t.Errorf("Git = %v, want GitUnknown", f.Git)
		}
	})
}
