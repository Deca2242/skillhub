package repair_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/doctor"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/repair"
	"skillhub/internal/manage/toggle"
)

// TestReportOnlyFindingsOfferNothing: gentle-ai copies, homonyms, a lock key
// that differs from its frontmatter name, link cycles and links leaving the
// known roots are only reported. Repairs offers nothing and says why, and
// Plan refuses without writing.
func TestReportOnlyFindingsOfferNothing(t *testing.T) {
	m := newMachine(t)
	m.h.Symlink(".kiro/skills/loop-a", "loop-b")
	m.h.Symlink(".kiro/skills/loop-b", "loop-a")
	m.h.Symlink(".kiro/skills/far", "/nowhere/far")
	m.h.WriteFile(".codex/skills/agent-browser/SKILL.md", graphtest.SkillMD("agent-browser", "another one"))
	want := map[doctor.Kind]repair.Reason{
		doctor.ManagedByGentleAI: repair.ReasonManagedByGentleAI,
		doctor.Homonyms:          repair.ReasonHomonyms,
		doctor.LockNameMismatch:  repair.ReasonLockNameMismatch,
		doctor.LinkCycle:         repair.ReasonLinkCycle,
		doctor.LinkOutsideRoots:  repair.ReasonLinkOutsideRoots,
	}
	g := m.graph()
	seen := map[doctor.Kind]bool{}
	before := m.snap()
	for _, f := range doctor.Detect(g) {
		reason, ok := want[f.Kind]
		if !ok {
			continue
		}
		first := !seen[f.Kind]
		seen[f.Kind] = true
		offer, err := m.planner().Repairs(g, f)
		if err != nil {
			t.Fatalf("Repairs(%s) error = %v", f.ID, err)
		}
		if len(offer.Repairs) != 0 || offer.Reason != reason {
			t.Errorf("Repairs(%s) = %+v, want nothing and %q", f.ID, offer, reason)
		}
		if !first {
			continue // Plan observes the whole home; once per kind is enough
		}
		for _, k := range []repair.Kind{repair.DeleteLink, repair.Adopt, repair.ParkEntry} {
			if _, err := m.planner().Plan(f, k, repair.Options{}); !errors.Is(err, repair.ErrNotOffered) {
				t.Errorf("Plan(%s, %s) error = %v, want ErrNotOffered", f.ID, k, err)
			}
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("the fixture has no %s finding", k)
		}
	}
	m.noWrite(before)
}

// TestTheNineteenBrokenLinksOfferOnlyAdoptOff: every real broken link points
// to a skill moved to OFF/, and AdoptOff is the only repair offered for them.
// The plan is exactly toggle's AdoptOff; applying it leaves none of the 19,
// and undoing it puts the home back byte for byte.
func TestTheNineteenBrokenLinksOfferOnlyAdoptOff(t *testing.T) {
	m := newMachine(t)
	g := m.graph()
	var broken []doctor.Finding
	for _, f := range doctor.Detect(g) {
		if f.Kind == doctor.BrokenLink {
			broken = append(broken, f)
		}
	}
	if len(broken) != len(graphtest.BrokenLinks) {
		t.Fatalf("Doctor reports %d broken links, the fixture has %d", len(broken), len(graphtest.BrokenLinks))
	}
	for _, f := range broken {
		offer, err := m.planner().Repairs(g, f)
		if err != nil {
			t.Fatalf("Repairs(%s) error = %v", f.ID, err)
		}
		if !slices.Equal(offer.Repairs, []repair.Kind{repair.AdoptOff}) {
			t.Errorf("Repairs(%s) = %v, want only adopt_off", f.ID, offer.Repairs)
		}
		if _, err := m.planner().Plan(f, repair.DeleteLink, repair.Options{}); !errors.Is(err, repair.ErrNotOffered) {
			t.Errorf("Plan(%s, delete_link) error = %v, want ErrNotOffered", f.ID, err)
		}
	}

	visible := 0
	for _, f := range doctor.Detect(g) {
		if f.Kind != doctor.ParkedButVisible {
			continue
		}
		visible++
		if offer, err := m.planner().Repairs(g, f); err != nil || !slices.Equal(offer.Repairs, []repair.Kind{repair.AdoptOff}) {
			t.Errorf("Repairs(%s) = %v, %v, want only adopt_off", f.ID, offer.Repairs, err)
		}
	}
	if visible == 0 {
		t.Error("the fixture has no parked_but_visible finding")
	}

	before := m.snap()
	pr, err := m.planner().Plan(broken[0], repair.AdoptOff, repair.Options{})
	if err != nil {
		t.Fatalf("Plan(adopt_off) error = %v", err)
	}
	want, err := m.toggle().AdoptOff(m.graph())
	must(t, err)
	if pr.Plan.ID != want.Plan.ID || pr.Preview.PlanID != want.Plan.ID {
		t.Errorf("Plan(adopt_off) = plan %s, want toggle's AdoptOff %s", pr.Plan.ID, want.Plan.ID)
	}
	if pr.Preview.Toggle == nil || !reflect.DeepEqual(*pr.Preview.Toggle, want.Preview) {
		t.Errorf("Preview.Toggle = %+v, want AdoptOff's preview", pr.Preview.Toggle)
	}
	if pr.Preview.Finding != broken[0].ID || pr.Preview.Repair != repair.AdoptOff {
		t.Errorf("Preview = %s/%s, want %s/adopt_off", pr.Preview.Finding, pr.Preview.Repair, broken[0].ID)
	}
	m.noWrite(before)

	out := m.apply(pr)
	for _, f := range m.findings() {
		if f.Kind == doctor.BrokenLink {
			t.Errorf("Doctor still reports %s", f.ID)
		}
	}
	m.undo(out)
	m.same("adopt OFF/, apply and undo", before)
}

// TestDeleteBrokenLink: a broken link with nothing to point to again is
// deleted, with its exact lexical target as the precondition.
func TestDeleteBrokenLink(t *testing.T) {
	m := newMachine(t)
	path := m.h.Symlink(".kiro/skills/ghost", "../../.agents/skills/ghost")
	f := m.finding("broken_link:.kiro/skills/ghost")
	offer, err := m.planner().Repairs(m.graph(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(offer.Repairs, []repair.Kind{repair.DeleteLink}) {
		t.Errorf("Repairs() = %v, want only delete_link", offer.Repairs)
	}

	before := m.snap()
	pr, err := m.planner().Plan(f, repair.DeleteLink, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(pr.Plan.Steps) != 1 || pr.Plan.Steps[0].Kind != engine.StepUnlink ||
		pr.Plan.Steps[0].Path != path || pr.Plan.Steps[0].Target != "../../.agents/skills/ghost" {
		t.Errorf("Plan().Steps = %+v, want one unlink of %s with its exact text", pr.Plan.Steps, path)
	}
	wantLink := repair.LinkChange{Path: path, Target: "../../.agents/skills/ghost", Harnesses: []string{"kiro-cli"}, Broken: true}
	if !reflect.DeepEqual(pr.Preview.RemovedLinks, []repair.LinkChange{wantLink}) || pr.Preview.PlanID != pr.Plan.ID {
		t.Errorf("Preview = %+v, want the removed link %+v", pr.Preview, wantLink)
	}
	m.noWrite(before)
	m.roundTrip(pr, f.ID, before)
}

// TestDeleteRefusesALinkThatChangedTarget: the link's exact text is the
// precondition; a link rewritten after planning is refused by the engine and
// nothing is written.
func TestDeleteRefusesALinkThatChangedTarget(t *testing.T) {
	m := newMachine(t)
	path := m.h.Symlink(".kiro/skills/ghost", "../../.agents/skills/ghost")
	pr, err := m.planner().Plan(m.finding("broken_link:.kiro/skills/ghost"), repair.DeleteLink, repair.Options{})
	must(t, err)
	must(t, removeAll(path))
	m.h.Symlink(".kiro/skills/ghost", "../../.agents/skills/other-ghost")
	before := m.snap()
	if _, err := m.engine().Apply(t.Context(), pr.Plan); err == nil {
		t.Fatal("Apply() deleted a link whose target changed")
	}
	m.same("refused apply", before)
}

// TestEnableAndLinkAParkedSkill: a broken link towards <store>/<key> while
// key is parked is repaired by enabling the skill, as toggle plans it, in a
// single plan; the link leads to the skill again.
func TestEnableAndLinkAParkedSkill(t *testing.T) {
	m := newMachine(t)
	dis, err := m.toggle().Disable(m.graph(), "find-skills")
	must(t, err)
	if _, err := m.engine().Apply(t.Context(), dis.Plan); err != nil {
		t.Fatal(err)
	}
	m.h.Symlink(".kiro/skills/find-skills", "../../.agents/skills/find-skills")
	f := m.finding("broken_link:.kiro/skills/find-skills")
	offer, err := m.planner().Repairs(m.graph(), f)
	must(t, err)
	if !slices.Equal(offer.Repairs, []repair.Kind{repair.DeleteLink, repair.EnableAndLink}) {
		t.Errorf("Repairs() = %v, want delete_link and enable_and_link", offer.Repairs)
	}

	before := m.snap()
	pr, err := m.planner().Plan(f, repair.EnableAndLink, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	enable, err := m.toggle().Enable("find-skills")
	must(t, err)
	if !reflect.DeepEqual(kinds(pr.Plan), kinds(enable.Plan)) || !reflect.DeepEqual(pr.Plan.Effect, enable.Plan.Effect) {
		t.Errorf("Plan() = %v %+v, want Enable's steps %v and effect %+v", kinds(pr.Plan), pr.Plan.Effect,
			kinds(enable.Plan), enable.Plan.Effect)
	}
	if pr.Preview.Toggle == nil || pr.Preview.Toggle.Action != toggle.ActionEnable || pr.Preview.Key != "find-skills" {
		t.Errorf("Preview = %+v, want Enable's preview for find-skills", pr.Preview)
	}
	m.noWrite(before)

	out := m.apply(pr)
	if got := readlink(t, m.h.Path(".kiro/skills/find-skills")); got != "../../.agents/skills/find-skills" {
		t.Errorf("the link is %q", got)
	}
	if _, ok := m.state().Parked["find-skills"]; ok {
		t.Error("find-skills is still parked")
	}
	if m.reports(f.ID) {
		t.Errorf("Doctor still reports %s", f.ID)
	}
	m.undo(out)
	m.same("enable and link, apply and undo", before)
}

// TestRepointToAnActiveSkill: a broken link whose name is the key of an
// active skill in the store is pointed at it, with the relative text npx
// writes, in place.
func TestRepointToAnActiveSkill(t *testing.T) {
	m := newMachine(t)
	path := m.h.Symlink(".kiro/skills/find-skills", "../../.agents/skills/find-skills-old")
	f := m.finding("broken_link:.kiro/skills/find-skills")
	offer, err := m.planner().Repairs(m.graph(), f)
	must(t, err)
	if !slices.Equal(offer.Repairs, []repair.Kind{repair.DeleteLink, repair.RepointLink}) {
		t.Errorf("Repairs() = %v, want delete_link and repoint_link", offer.Repairs)
	}
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.RepointLink, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := []repair.LinkChange{{Path: path, Target: "../../.agents/skills/find-skills", Harnesses: []string{"kiro-cli"}}}
	if !reflect.DeepEqual(pr.Preview.CreatedLinks, want) {
		t.Errorf("CreatedLinks = %+v, want %+v", pr.Preview.CreatedLinks, want)
	}
	m.roundTrip(pr, f.ID, before)
}

// TestBrokenLinkInADiagnosticProfileIsNeverTouched: ~/.claude-omni is a
// read-only profile; a broken link there is only reported.
func TestBrokenLinkInADiagnosticProfileIsNeverTouched(t *testing.T) {
	m := newMachine(t)
	m.h.Symlink(".claude-omni/skills/ghost", "../../.agents/skills/ghost")
	f := m.finding("broken_link:.claude-omni/skills/ghost")
	offer, err := m.planner().Repairs(m.graph(), f)
	must(t, err)
	if len(offer.Repairs) != 0 || offer.Reason != repair.ReasonDiagnosticProfile {
		t.Errorf("Repairs() = %+v, want nothing and diagnostic_profile", offer)
	}
	before := m.snap()
	if _, err := m.planner().Plan(f, repair.DeleteLink, repair.Options{}); !errors.Is(err, repair.ErrNotOffered) {
		t.Errorf("Plan() error = %v, want ErrNotOffered", err)
	}
	m.noWrite(before)
}

func readlink(t *testing.T, path string) string {
	t.Helper()
	target, err := osReadlink(path)
	must(t, err)
	return target
}

// zonePaths are the paths no repair may touch: the foreign zones of the
// fixture and the read-only profile.
func (m *machine) zonePaths() []string {
	var out []string
	for _, rel := range []string{
		".agents/skills/OFF/README.md", ".claude/skills/synced", graphtest.KarpathyClone, ".codex/skills/.system",
		".agents/skills/_shared", ".claude/skills/_shared", ".codex/skills/_shared", ".config/opencode/skills/_shared",
		".claude-omni",
	} {
		out = append(out, m.h.Path(rel))
	}
	for _, dir := range graphtest.GentleDirs {
		for _, n := range graphtest.GentleSkills {
			out = append(out, m.h.Path(filepath.Join(dir, n)))
		}
	}
	return out
}

func inside(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
