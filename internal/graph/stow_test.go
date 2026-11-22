package graph_test

import (
	"slices"
	"testing"

	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
)

func stowed(t *testing.T) (*testsupport.Home, graph.Graph) {
	t.Helper()
	h := testsupport.NewHome(t)
	graphtest.Stowed(h)
	return h, build(t, h, nil, nil)
}

// TestLinkUnderALinkedAncestorResolvesFromTheRealDirectory: with ~/.claude a
// symlink to ~/dotfiles/a/claude, a link relative to the real link directory,
// as npx skills writes it, reaches the store folder, as the kernel resolves
// it. Its hops and final target are shown in the form of the known roots.
func TestLinkUnderALinkedAncestorResolvesFromTheRealDirectory(t *testing.T) {
	h, g := stowed(t)
	l := graphtest.StowedLinks
	store := h.Path(".agents/skills/find-skills")

	good := linkAt(t, g, h.Path(l.Good))
	if good.Status != graph.LinkOK || good.Text != l.GoodText || good.Final != store ||
		!slices.Equal(good.Hops, []string{store}) || good.Folder != real(t, h, ".agents/skills/find-skills") {
		t.Errorf("good link = %+v, want ok towards %s", good, store)
	}
	s := skillByID(t, g, domain.DirID(".agents/skills/find-skills"))
	if got := seen(s); !slices.Equal(got["claude-code"], []graph.Kind{graph.ViaLink}) {
		t.Errorf("claude-code sees find-skills as %v, want through a link", got["claude-code"])
	}
}

// TestBrokenLinkUnderALinkedAncestorIsBroken: a link of the real directory
// whose target, resolved from there, does not exist is broken, towards the
// store path it names.
func TestBrokenLinkUnderALinkedAncestorIsBroken(t *testing.T) {
	h, g := stowed(t)
	l := graphtest.StowedLinks
	want := h.Path(".agents/skills/gone")
	broken := linkAt(t, g, h.Path(l.Broken))
	if broken.Status != graph.LinkBroken || broken.Text != l.BrokenText || broken.Final != want || broken.Folder != "" {
		t.Errorf("broken link = %+v, want broken towards %s", broken, want)
	}
}

// TestLinkChainUnderALinkedAncestorIsFollowedHopByHop: every hop is resolved
// from the real directory of the link it leaves, including a link to a
// sibling in the real link directory.
func TestLinkChainUnderALinkedAncestorIsFollowedHopByHop(t *testing.T) {
	h, g := stowed(t)
	l := graphtest.StowedLinks
	chain := linkAt(t, g, h.Path(l.Chain))
	want := []string{h.Path(".agents/skills/computer-use"), h.Path(".agents/skills/orca-computer-use")}
	if chain.Status != graph.LinkOK || !slices.Equal(chain.Hops, want) || chain.Folder != real(t, h, ".agents/skills/orca-computer-use") {
		t.Errorf("chain = %+v, want ok through %v", chain, want)
	}
	sibling := linkAt(t, g, h.Path(l.Sibling))
	want = []string{h.Path(l.Good), h.Path(".agents/skills/find-skills")}
	if sibling.Status != graph.LinkOK || !slices.Equal(sibling.Hops, want) || sibling.Folder != real(t, h, ".agents/skills/find-skills") {
		t.Errorf("sibling = %+v, want ok through %v", sibling, want)
	}
}

// TestLinkThroughTheRealPathOfARootStaysInside: a store link written towards
// the real path of ~/.claude/skills is inside that root, and is shown below
// it, even when the store is walked before any link of ~/.claude/skills.
func TestLinkThroughTheRealPathOfARootStaysInside(t *testing.T) {
	h := testsupport.NewHome(t)
	graphtest.Stowed(h)
	h.WriteFile("dotfiles/a/claude/skills/mine/SKILL.md", graphtest.SkillMD("mine", "kept in dotfiles"))
	h.Symlink(".agents/skills/mine", "../../dotfiles/a/claude/skills/mine")
	g := build(t, h, nil, nil)

	l := linkAt(t, g, h.Path(".agents/skills/mine"))
	want := []string{h.Path(".claude/skills/mine")}
	if l.Status != graph.LinkOK || !slices.Equal(l.Hops, want) || l.Folder != real(t, h, ".claude/skills/mine") {
		t.Errorf("link = %+v, want ok through %v", l, want)
	}
}

// TestLinkUnderALinkedAncestorOutsideTheRootsIsNotFollowed: resolved from the
// real directory, a target outside the known roots stays outside and is not
// followed.
func TestLinkUnderALinkedAncestorOutsideTheRootsIsNotFollowed(t *testing.T) {
	h, g := stowed(t)
	out := linkAt(t, g, h.Path(graphtest.StowedLinks.Outside))
	if out.Status != graph.LinkOutside || out.Folder != "" || out.Final != h.Path("projects/elsewhere") {
		t.Errorf("outside link = %+v, want outside towards %s, unfollowed", out, h.Path("projects/elsewhere"))
	}
	if len(skillsNamed(g, "elsewhere")) != 0 {
		t.Errorf("a folder outside the known roots became a skill")
	}
}
