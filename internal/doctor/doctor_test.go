package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/data"
	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

type osLinks struct{}

func (osLinks) Readlink(path string) (string, error) { return os.Readlink(path) }

func machineGraph(t *testing.T) (*testsupport.Home, graph.Graph) {
	t.Helper()
	h := testsupport.NewHome(t)
	lock := graphtest.Machine(h)
	clone, err := filepath.EvalSymlinks(h.Path(graphtest.KarpathyClone))
	if err != nil {
		t.Fatal(err)
	}
	git := fakes.NewGit()
	git.AddClone(clone, ports.CloneStatus{Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main", Upstream: "origin/main", Clean: true})
	files := fsadapter.NewFileOps(h.Paths().Guard())
	dirs := fsadapter.NewScanner(h.Paths().Guard())
	agents, err := data.LoadAgents(h.Paths(), files)
	if err != nil {
		t.Fatal(err)
	}
	harnesses, err := data.LoadHarnesses(h.Paths(), files)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := data.DiagnosticProfiles()
	if err != nil {
		t.Fatal(err)
	}
	skills, err := inventory.Scan(context.Background(), inventory.Deps{
		Paths: h.Paths(), Agents: agents, Dirs: dirs, Files: files, Lock: fakes.NewSkillsLockStore(lock...), Git: git,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Build(graph.Deps{Paths: h.Paths(), Harnesses: harnesses, Profiles: profiles, Inventory: skills, Dirs: dirs, Files: files, Links: osLinks{}})
	if err != nil {
		t.Fatal(err)
	}
	return h, g
}

func count(fs []doctor.Finding) map[doctor.Kind]int {
	out := map[doctor.Kind]int{}
	for _, f := range fs {
		out[f.Kind]++
	}
	return out
}

func byID(t *testing.T, fs []doctor.Finding, id string) doctor.Finding {
	t.Helper()
	for _, f := range fs {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no finding %q", id)
	return doctor.Finding{}
}

func TestDetectOnTheMachine(t *testing.T) {
	h, g := machineGraph(t)
	got := doctor.Detect(g)

	want := map[doctor.Kind]int{
		doctor.BrokenLink:        19,
		doctor.ParkedButVisible:  4,
		doctor.LockNameMismatch:  1,
		doctor.NoCanonical:       1,
		doctor.UnmanagedSkill:    2,
		doctor.Homonyms:          9,
		doctor.ManagedByGentleAI: 12,
	}
	if c := count(got); !reflect.DeepEqual(c, want) {
		t.Errorf("findings by kind = %v, want %v", c, want)
	}

	f := byID(t, got, "broken_link:.kiro/skills/accessibility")
	if f.Target != h.Path(".agents/skills/accessibility") || !slices.Equal(f.Related, []string{h.Path(".agents/skills/OFF/accessibility")}) {
		t.Errorf("broken link evidence = %+v, want its lexical target and the parked folder", f)
	}
	f = byID(t, got, "parked_but_visible:lock:accessibility")
	if !slices.Equal(f.Harnesses, []string{"codex", "opencode", "pi"}) {
		t.Errorf("parked accessibility visible in %v, want codex, opencode and pi", f.Harnesses)
	}
	f = byID(t, got, "lock_name_mismatch:lock:computer-use")
	if f.Name != "orca-computer-use" || f.LockKey != "computer-use" {
		t.Errorf("name mismatch = %+v", f)
	}
	f = byID(t, got, "no_canonical:dir:.copilot/skills/requirements-elicitation")
	if len(f.Paths) != 5 {
		t.Errorf("no canonical evidence = %v, want the 5 copies", f.Paths)
	}
	byID(t, got, "unmanaged_skill:dir:.claude/skills/humanizer")
	byID(t, got, "unmanaged_skill:dir:.codex/skills/brainstorming")
	f = byID(t, got, "homonyms:orca-cli")
	if !slices.Equal(f.Skills, []string{"dir:.claude-omni/skills/orca-cli", "lock:orca-cli"}) {
		t.Errorf("orca-cli homonyms = %v", f.Skills)
	}
	byID(t, got, "managed_by_gentle_ai:.gemini/config/skills/go-testing")

	// Stable: the same graph gives the same findings, ids and order.
	if again := doctor.Detect(g); !reflect.DeepEqual(again, got) {
		t.Error("Detect is not deterministic")
	}
	ids := map[string]bool{}
	for _, f := range got {
		if ids[f.ID] {
			t.Errorf("id %q repeats", f.ID)
		}
		ids[f.ID] = true
		if !strings.HasPrefix(f.ID, string(f.Kind)+":") {
			t.Errorf("id %q does not start with its kind %q", f.ID, f.Kind)
		}
	}
}

func TestDetectEachRule(t *testing.T) {
	home := "/h"
	link := func(path string, status graph.LinkStatus, final string) graph.Link {
		return graph.Link{Path: home + path, Status: status, Final: home + final}
	}
	tests := []struct {
		name string
		g    graph.Graph
		want []doctor.Finding
	}{
		{
			name: "cycle and outside links",
			g: graph.Graph{Home: home, Links: []graph.Link{
				link("/.claude/skills/a", graph.LinkCycle, "/.claude/skills/b"),
				link("/.claude/skills/c", graph.LinkOutside, "/projects/c"),
				link("/.claude/skills/ok", graph.LinkOK, "/.agents/skills/ok"),
			}},
			want: []doctor.Finding{
				{ID: "link_cycle:.claude/skills/a", Kind: doctor.LinkCycle, Paths: []string{"/h/.claude/skills/a"}, Target: "/h/.claude/skills/b"},
				{ID: "link_outside_roots:.claude/skills/c", Kind: doctor.LinkOutsideRoots, Paths: []string{"/h/.claude/skills/c"}, Target: "/h/projects/c"},
			},
		},
		{
			name: "identical copies and a lock entry without folder",
			g: graph.Graph{Home: home, Skills: []graph.Skill{
				{ID: "lock:a", LockKey: "a", Name: "a", Origin: domain.OriginLock, Canonical: "/h/.agents/skills/a", Folders: []string{"/h/.agents/skills/a", "/h/.claude/skills/a"}},
				{ID: "lock:gone", LockKey: "gone", Origin: domain.OriginLock},
			}},
			want: []doctor.Finding{
				{ID: "identical_copies:lock:a", Kind: doctor.IdenticalCopies, Skill: "lock:a", Name: "a", LockKey: "a", Paths: []string{"/h/.agents/skills/a", "/h/.claude/skills/a"}},
				{ID: "lock_entry_without_folder:lock:gone", Kind: doctor.LockEntryWithoutFolder, Skill: "lock:gone", LockKey: "gone"},
			},
		},
		{
			name: "a clone or a foreign zone is never unmanaged",
			g: graph.Graph{Home: home, Skills: []graph.Skill{
				{ID: "git:x", Name: "x", Origin: domain.OriginGitClone, Canonical: "/h/.claude/skills/x", Folders: []string{"/h/.claude/skills/x"}},
				{ID: "dir:p", Name: "p", Origin: domain.OriginNone, Zone: graph.ZoneProfile, Folders: []string{"/h/.claude-omni/skills/p"}},
			}},
			want: []doctor.Finding{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doctor.Detect(tt.g); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Detect() =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

func TestDetectLeavesTheGraphUntouched(t *testing.T) {
	_, g := machineGraph(t)
	before := cloneGraph(g)
	doctor.Detect(g)
	if !reflect.DeepEqual(g, before) {
		t.Error("Detect changed the graph it read")
	}
}

func cloneGraph(g graph.Graph) graph.Graph {
	c := g
	c.Skills = slices.Clone(g.Skills)
	for i := range c.Skills {
		c.Skills[i].Folders = slices.Clone(g.Skills[i].Folders)
		c.Skills[i].Visibility = slices.Clone(g.Skills[i].Visibility)
		c.Skills[i].Aliases = slices.Clone(g.Skills[i].Aliases)
	}
	c.Links = slices.Clone(g.Links)
	c.Boundary = slices.Clone(g.Boundary)
	c.Folders = slices.Clone(g.Folders)
	return c
}
