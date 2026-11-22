package repair_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/repair"
	"skillhub/internal/shared/domain"
)

// TestAdoptUnmanagedSkillFromAUniversalDir: an own skill of a harness that
// reads the store moves to the store, with no lock entry; the harness keeps
// seeing it through the store.
func TestAdoptUnmanagedSkillFromAUniversalDir(t *testing.T) {
	m := newMachine(t)
	id := "unmanaged_skill:" + string(domain.DirID(".codex/skills/brainstorming"))
	f := m.finding(id)
	offer, err := m.planner().Repairs(m.graph(), f)
	must(t, err)
	if !slices.Equal(offer.Repairs, []repair.Kind{repair.Adopt}) {
		t.Errorf("Repairs() = %v, want adopt", offer.Repairs)
	}
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.Adopt, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	from, to := m.h.Path(".codex/skills/brainstorming"), m.h.Path(".agents/skills/brainstorming")
	if len(pr.Plan.Steps) != 1 || pr.Plan.Steps[0].Kind != engine.StepMove || pr.Plan.Steps[0].Src != from ||
		pr.Plan.Steps[0].Dst != to {
		t.Errorf("Steps = %+v, want only the move %s -> %s", pr.Plan.Steps, from, to)
	}
	if pr.Plan.TouchesLock {
		t.Error("adopting an unmanaged skill touches the lock")
	}
	wantEffect := engine.Effect{SetManaged: []domain.ManagedSkill{{Key: "brainstorming", Targets: []string{}}}}
	if !reflect.DeepEqual(pr.Plan.Effect, wantEffect) {
		t.Errorf("Effect = %+v, want %+v", pr.Plan.Effect, wantEffect)
	}
	if !reflect.DeepEqual(pr.Preview.Moved, []repair.Move{{From: from, To: to}}) || pr.Preview.Key != "brainstorming" ||
		pr.Preview.Canonical != to {
		t.Errorf("Preview = %+v", pr.Preview)
	}
	m.noWrite(before)

	out := m.apply(pr)
	if string(m.lockBytes()) != string(before.lock) {
		t.Error("adopting wrote the lock")
	}
	if _, ok := m.state().Managed["brainstorming"]; !ok {
		t.Error("the adopted skill is not managed")
	}
	if m.reports(id) {
		t.Errorf("Doctor still reports %s", id)
	}
	m.undo(out)
	m.same("adopt, apply and undo", before)
}

// TestAdoptUnmanagedSkillFromALinkDir: the copy of a harness that links is
// replaced by the npx link to its new store folder, at the same path.
func TestAdoptUnmanagedSkillFromALinkDir(t *testing.T) {
	m := newMachine(t)
	id := "unmanaged_skill:" + string(domain.DirID(".claude/skills/humanizer"))
	f := m.finding(id)
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.Adopt, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	m.roundTrip(pr, id, before)
}

// TestIdenticalCopiesInAUniversalDirAreRemoved: the store copy is the
// canonical one; a copy in a dir only store readers see is deleted, and the
// snapshot keeps it.
func TestIdenticalCopiesInAUniversalDirAreRemoved(t *testing.T) {
	m := newMachine(t)
	m.copyTree("find-skills", ".codex/skills/find-skills")
	g := m.graph()
	id := "identical_copies:" + skillID(t, g, "find-skills")
	f := m.finding(id)
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.Adopt, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	copyPath := m.h.Path(".codex/skills/find-skills")
	if !slices.Equal(kinds(pr.Plan), []engine.StepKind{engine.StepRequire, engine.StepRemoveTree}) ||
		pr.Plan.Steps[0].Path != m.h.Path(".agents/skills/find-skills") || pr.Plan.Steps[1].Path != copyPath {
		t.Errorf("Steps = %+v, want requiring the store folder and removing %s", pr.Plan.Steps, copyPath)
	}
	if len(pr.Preview.RemovedCopies) != 1 || pr.Preview.RemovedCopies[0].Path != copyPath ||
		pr.Preview.RemovedCopies[0].Replaced || pr.Preview.Canonical != m.h.Path(".agents/skills/find-skills") {
		t.Errorf("Preview = %+v", pr.Preview)
	}
	m.noWrite(before)
	m.roundTrip(pr, id, before)
}

// TestIdenticalCopiesInALinkDirBecomeLinks: a copy in a harness's link dir
// is replaced by the npx link to the store.
func TestIdenticalCopiesInALinkDirBecomeLinks(t *testing.T) {
	m := newMachine(t)
	m.copyTree("find-skills", ".kiro/skills/find-skills")
	id := "identical_copies:" + skillID(t, m.graph(), "find-skills")
	f := m.finding(id)
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.Adopt, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	m.roundTrip(pr, id, before)
}

// TestNoCanonicalNeedsTheExactCanonical: identical copies all outside the
// store; adopting needs the user's canonical, which moves to the store, while
// the others are removed and, in a link dir (Kiro's), replaced by the npx
// link.
func TestNoCanonicalNeedsTheExactCanonical(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".codex/skills/notes/SKILL.md", graphtest.SkillMD("notes", "notes"))
	m.h.WriteFile(".kiro/skills/notes-copy/SKILL.md", graphtest.SkillMD("notes", "notes"))
	id := "no_canonical:" + string(domain.DirID(".codex/skills/notes"))
	f := m.finding(id)
	before := m.snap()
	if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{}); !errors.Is(err, repair.ErrNeedsCanonical) {
		t.Errorf("Plan() without a canonical error = %v, want ErrNeedsCanonical", err)
	}
	if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{Canonical: m.h.Path(".gemini/skills/notes")}); !errors.Is(err, repair.ErrBadCanonical) {
		t.Errorf("Plan() with a foreign canonical error = %v, want ErrBadCanonical", err)
	}
	m.noWrite(before)

	canonical := m.h.Path(".codex/skills/notes")
	pr, err := m.planner().Plan(f, repair.Adopt, repair.Options{Canonical: canonical})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	copyPath, store := m.h.Path(".kiro/skills/notes-copy"), m.h.Path(".agents/skills/notes")
	wantKinds := []engine.StepKind{engine.StepMove, engine.StepRemoveTree, engine.StepLink}
	if !slices.Equal(kinds(pr.Plan), wantKinds) {
		t.Fatalf("Steps = %v, want %v", kinds(pr.Plan), wantKinds)
	}
	if s := pr.Plan.Steps[2]; s.Path != m.h.Path(".kiro/skills/notes") || s.Target != "../../.agents/skills/notes" {
		t.Errorf("link step = %+v, want the npx link in .kiro/skills", s)
	}
	if pr.Plan.Meta.Canonical != canonical || pr.Preview.Canonical != store {
		t.Errorf("Meta.Canonical = %q, Preview.Canonical = %q", pr.Plan.Meta.Canonical, pr.Preview.Canonical)
	}
	if len(pr.Preview.Copies) != 1 || pr.Preview.Copies[0].Path != copyPath || !pr.Preview.Copies[0].Same() {
		t.Errorf("Copies = %+v, want the identical copy %s", pr.Preview.Copies, copyPath)
	}
	wantEffect := engine.Effect{SetManaged: []domain.ManagedSkill{{Key: "notes", Targets: []string{"kiro-cli"}}}}
	if !reflect.DeepEqual(pr.Plan.Effect, wantEffect) {
		t.Errorf("Effect = %+v, want %+v", pr.Plan.Effect, wantEffect)
	}
	m.noWrite(before)
	m.roundTrip(pr, id, before)
}

// TestDivergentCopyShowsTheDiffAndKeepsItInTheSnapshot: a copy whose content
// differs from the store skill of the same key; the user chooses the store
// one, the preview shows the diff and the copy is removed, recoverable by
// undo. A lock skill is never replaced by a copy of unknown origin.
func TestDivergentCopyShowsTheDiffAndKeepsItInTheSnapshot(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".codex/skills/find-skills/SKILL.md", graphtest.SkillMD("find-skills", "edited by hand"))
	m.h.WriteFile(".codex/skills/find-skills/extra.md", "extra")
	id := "unmanaged_skill:" + string(domain.DirID(".codex/skills/find-skills"))
	f := m.finding(id)
	before := m.snap()
	if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{}); !errors.Is(err, repair.ErrNeedsCanonical) {
		t.Errorf("Plan() without a canonical error = %v, want ErrNeedsCanonical", err)
	}
	copyPath, store := m.h.Path(".codex/skills/find-skills"), m.h.Path(".agents/skills/find-skills")
	if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{Canonical: copyPath}); !errors.Is(err, repair.ErrLockedSkill) {
		t.Errorf("Plan() with the copy over a lock skill error = %v, want ErrLockedSkill", err)
	}
	m.noWrite(before)

	pr, err := m.planner().Plan(f, repair.Adopt, repair.Options{Canonical: store})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !slices.Equal(kinds(pr.Plan), []engine.StepKind{engine.StepRequire, engine.StepRemoveTree}) ||
		pr.Plan.Steps[0].Path != m.h.Path(".agents/skills/find-skills") || pr.Plan.Steps[1].Path != copyPath {
		t.Errorf("Steps = %+v, want requiring the store folder and removing the copy", pr.Plan.Steps)
	}
	want := repair.CopyDiff{Path: copyPath, Added: []string{"extra.md"}, Changed: []string{"SKILL.md"}}
	if len(pr.Preview.Copies) != 1 {
		t.Fatalf("Copies = %+v", pr.Preview.Copies)
	}
	got := pr.Preview.Copies[0]
	got.TreeSHA = ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Copies[0] = %+v, want %+v", got, want)
	}
	m.roundTrip(pr, id, before)
}

// skillID is the graph id of the skill whose canonical folder is the store
// folder of key.
func skillID(t *testing.T, g graph.Graph, key string) string {
	t.Helper()
	entry := filepath.Join(g.Store, key)
	i := slices.IndexFunc(g.Folders, func(f graph.Folder) bool { return f.Entry == entry })
	if i < 0 {
		t.Fatalf("no store folder %s", entry)
	}
	for _, s := range g.Skills {
		if s.Canonical == g.Folders[i].Path {
			return s.ID
		}
	}
	t.Fatalf("no skill claims %s", entry)
	return ""
}

// TestAdoptNeverHidesASkillFromAHarness: a copy that a harness reads outside
// any link directory, and that harness is not known to read the store (Trae),
// is never removed or moved: the harness would lose the skill.
func TestAdoptNeverHidesASkillFromAHarness(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".trae/skills/trae-only/SKILL.md", graphtest.SkillMD("trae-only", "trae"))
	f := m.finding("unmanaged_skill:" + string(domain.DirID(".trae/skills/trae-only")))
	before := m.snap()
	if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{}); !errors.Is(err, repair.ErrNoPlacement) {
		t.Errorf("Plan() error = %v, want ErrNoPlacement", err)
	}
	m.noWrite(before)
}

// TestAdoptNeverLeavesALinkBroken: a link that leads to a copy the adoption
// removes, with nothing taking the copy's place, refuses the adoption.
func TestAdoptNeverLeavesALinkBroken(t *testing.T) {
	m := newMachine(t)
	m.copyTree("find-skills", ".codex/skills/find-skills")
	m.h.Symlink(".gemini/config/skills/find-skills-codex", "../../../.codex/skills/find-skills")
	f := m.finding("identical_copies:" + skillID(t, m.graph(), "find-skills"))
	before := m.snap()
	if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{}); !errors.Is(err, repair.ErrLinkedCopy) {
		t.Errorf("Plan() error = %v, want ErrLinkedCopy", err)
	}
	m.noWrite(before)
}

// TestAdoptNeverLinksOverAForeignZone: the npx link of the adopted key would
// land on the plugin clone in ~/.claude/skills; the adoption is refused as a
// foreign zone and nothing is written.
func TestAdoptNeverLinksOverAForeignZone(t *testing.T) {
	m := newMachine(t)
	key := filepath.Base(graphtest.KarpathyClone)
	m.h.WriteFile(".codex/skills/"+key+"/SKILL.md", graphtest.SkillMD(key, "copy"))
	m.h.WriteFile(".claude/skills/akp-copy/SKILL.md", graphtest.SkillMD(key, "copy"))
	f := m.finding("no_canonical:" + string(domain.DirID(".claude/skills/akp-copy")))
	before := m.snap()
	_, err := m.planner().Plan(f, repair.Adopt, repair.Options{Canonical: m.h.Path(".codex/skills/" + key)})
	if !errors.Is(err, repair.ErrForeignZone) {
		t.Errorf("Plan() error = %v, want ErrForeignZone", err)
	}
	m.noWrite(before)
}
