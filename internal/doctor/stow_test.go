package doctor_test

import (
	"context"
	"slices"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/data"
	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/inventory"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func stowedGraph(t *testing.T) (*testsupport.Home, graph.Graph) {
	t.Helper()
	h := testsupport.NewHome(t)
	graphtest.Stowed(h)
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
		Paths: h.Paths(), Agents: agents, Dirs: dirs, Files: files, Lock: fakes.NewSkillsLockStore(), Git: fakes.NewGit(),
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

// TestDetectUnderALinkedAncestor: with ~/.claude a symlink to
// ~/dotfiles/a/claude, links resolved from the real directory are classified
// as the kernel sees them: the good link, the chain and the sibling link are
// no finding, the dangling one is a broken link next to its parked folder,
// and only the link that leaves the roots is outside them.
func TestDetectUnderALinkedAncestor(t *testing.T) {
	h, g := stowedGraph(t)
	got := doctor.Detect(g)
	l := graphtest.StowedLinks

	var links []string
	for _, f := range got {
		if f.Kind == doctor.BrokenLink || f.Kind == doctor.LinkCycle || f.Kind == doctor.LinkOutsideRoots {
			links = append(links, f.ID)
		}
	}
	want := []string{"broken_link:" + l.Broken, "link_outside_roots:" + l.Outside}
	if !slices.Equal(links, want) {
		t.Fatalf("link findings = %v, want %v", links, want)
	}

	broken := byID(t, got, "broken_link:"+l.Broken)
	if !slices.Equal(broken.Paths, []string{h.Path(l.Broken)}) || broken.Target != h.Path(".agents/skills/gone") ||
		!slices.Equal(broken.Related, []string{h.Path(".agents/skills/OFF/gone")}) {
		t.Errorf("broken = %+v, want towards the store path gone, related to OFF/gone", broken)
	}
	outside := byID(t, got, "link_outside_roots:"+l.Outside)
	if !slices.Equal(outside.Paths, []string{h.Path(l.Outside)}) || outside.Target != h.Path("projects/elsewhere") {
		t.Errorf("outside = %+v, want towards ~/projects/elsewhere", outside)
	}
}
