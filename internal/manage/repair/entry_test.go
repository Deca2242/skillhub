package repair_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/doctor"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/repair"
	"skillhub/internal/shared/domain"
)

// withGhostEntry adds a lock entry whose folder is nowhere, between two
// existing entries.
func withGhostEntry(m *machine) string {
	m.t.Helper()
	entries := slices.Clone(m.entries)
	ghost := domain.LockEntry{Name: "ghost-skill", Source: "owner/ghost", SourceType: "github",
		SourceURL: "https://github.com/owner/ghost.git", SkillPath: "skills/ghost-skill/SKILL.md",
		SkillFolderHash: "1111111111111111111111111111111111111111"}
	entries = slices.Insert(entries, 2, ghost)
	m.writeLock(entries)
	for _, f := range m.findings() {
		if f.Kind == doctor.LockEntryWithoutFolder && f.LockKey == "ghost-skill" {
			return f.ID
		}
	}
	m.t.Fatal("Doctor does not report the ghost entry")
	return ""
}

// TestParkTheEntryOfAMissingFolder: the entry alone is parked, so npx skills
// update stops reinstalling it; undo puts it back in its position.
func TestParkTheEntryOfAMissingFolder(t *testing.T) {
	m := newMachine(t)
	id := withGhostEntry(m)
	f := m.finding(id)
	offer, err := m.planner().Repairs(m.graph(), f)
	must(t, err)
	if !slices.Equal(offer.Repairs, []repair.Kind{repair.ParkEntry, repair.Reinstall}) {
		t.Errorf("Repairs() = %v, want park_entry and reinstall", offer.Repairs)
	}
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.ParkEntry, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(pr.Plan.Steps) != 1 || pr.Plan.Steps[0].Kind != engine.StepLockPark || pr.Plan.Steps[0].Name != "ghost-skill" {
		t.Errorf("Steps = %+v, want only parking ghost-skill", pr.Plan.Steps)
	}
	if pr.Preview.LockEntry != "ghost-skill" || pr.Reinstall != nil {
		t.Errorf("Preview = %+v, Reinstall = %+v", pr.Preview, pr.Reinstall)
	}
	m.noWrite(before)

	out := m.apply(pr)
	if strings.Contains(string(m.lockBytes()), "ghost-skill") {
		t.Error("the entry is still in the lock")
	}
	ps, ok := m.state().Parked["ghost-skill"]
	if !ok || ps.Entry == nil || ps.Entry.Name != "ghost-skill" || ps.Folder != "" || len(ps.Links) != 0 {
		t.Errorf("Parked[ghost-skill] = %+v, want the entry alone", ps)
	}
	if m.reports(id) {
		t.Errorf("Doctor still reports %s", id)
	}
	m.undo(out)
	m.same("park the entry, apply and undo", before)
}

// TestReinstallHandsTheEntryToTheInstallFlow: reinstalling is not planned
// here; Plan returns the entry's origin, typed, and no engine plan.
func TestReinstallHandsTheEntryToTheInstallFlow(t *testing.T) {
	m := newMachine(t)
	f := m.finding(withGhostEntry(m))
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.Reinstall, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := &repair.ReinstallRef{Key: "ghost-skill", Source: "owner/ghost", SourceType: "github",
		SourceURL: "https://github.com/owner/ghost.git", SkillPath: "skills/ghost-skill/SKILL.md"}
	if !reflect.DeepEqual(pr.Reinstall, want) {
		t.Errorf("Reinstall = %+v, want %+v", pr.Reinstall, want)
	}
	if pr.Plan.ID != "" || len(pr.Plan.Steps) != 0 || pr.Preview.PlanID != "" {
		t.Errorf("Plan = %+v, want no engine plan", pr.Plan)
	}
	m.noWrite(before)
}

// TestStaleFindingsAreRefused: a finding that is gone, or that changed,
// between detecting and planning is refused with a typed error, and nothing
// is written.
func TestStaleFindingsAreRefused(t *testing.T) {
	m := newMachine(t)
	path := m.h.Symlink(".kiro/skills/ghost", "../../.agents/skills/ghost")
	gone := m.finding("broken_link:.kiro/skills/ghost")
	must(t, removeAll(path))

	m.copyTree("find-skills", ".codex/skills/find-skills")
	id := "identical_copies:" + skillID(t, m.graph(), "find-skills")
	changed := m.finding(id)
	m.copyTree("find-skills", ".gemini/config/skills/find-skills")

	before := m.snap()
	_, err := m.planner().Plan(gone, repair.DeleteLink, repair.Options{})
	var se *repair.StaleError
	if !errors.Is(err, repair.ErrStale) || !errors.As(err, &se) || se.ID != gone.ID || se.Now != nil {
		t.Errorf("Plan(gone) error = %v, want a StaleError with nothing now", err)
	}
	_, err = m.planner().Plan(changed, repair.Adopt, repair.Options{})
	if !errors.Is(err, repair.ErrStale) || !errors.As(err, &se) || se.Now == nil || len(se.Now.Paths) != 3 {
		t.Errorf("Plan(changed) error = %v, want a StaleError with the finding as it is now", err)
	}
	m.noWrite(before)
}

// TestRepairsNeverTouchAForeignZone plans every repair Doctor offers on the
// machine and checks that no step touches a foreign zone, a gentle-ai copy or
// the read-only profile.
func TestRepairsNeverTouchAForeignZone(t *testing.T) {
	m := newMachine(t)
	m.copyTree("find-skills", ".codex/skills/find-skills")
	withGhostEntry(m)
	g := m.graph()
	zones := m.zonePaths()
	planned := 0
	for _, f := range doctor.Detect(g) {
		offer, err := m.planner().Repairs(g, f)
		must(t, err)
		for _, k := range offer.Repairs {
			pr, err := m.planner().Plan(f, k, repair.Options{Canonical: firstOf(f.Paths)})
			if err != nil {
				continue // refused plans write nothing; other tests pin why
			}
			planned++
			for _, p := range stepPaths(pr.Plan) {
				for _, z := range zones {
					if inside(p, z) {
						t.Errorf("%s/%s touches %s, inside %s", f.ID, k, p, z)
					}
				}
			}
		}
	}
	if planned == 0 {
		t.Fatal("no repair was planned")
	}
	for _, n := range graphtest.GentleSkills {
		for _, f := range m.findings() {
			if f.Kind != doctor.ManagedByGentleAI || !strings.HasSuffix(f.ID, "/"+n) {
				continue
			}
			if offer, _ := m.planner().Repairs(g, f); len(offer.Repairs) != 0 {
				t.Errorf("a gentle-ai copy %s gets repairs %v", f.ID, offer.Repairs)
			}
		}
	}
}

func firstOf(ps []string) string {
	if len(ps) == 0 {
		return ""
	}
	return ps[0]
}

// TestEnableGivesBackAParkedEntryAlone: the record park_entry leaves has an
// entry and no folder; toggle's Enable plans only putting the entry back
// (unpark), so park then enable leaves the home, the lock and the state as
// they were (L47).
func TestEnableGivesBackAParkedEntryAlone(t *testing.T) {
	m := newMachine(t)
	f := m.finding(withGhostEntry(m))
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.ParkEntry, repair.Options{})
	must(t, err)
	m.apply(pr)

	en, err := m.toggle().Enable("ghost-skill")
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if got := kinds(en.Plan); !slices.Equal(got, []engine.StepKind{engine.StepLockUnpark}) {
		t.Errorf("steps = %v, want only the unpark", got)
	}
	if ch := en.Preview.Skills; len(ch) != 1 || ch[0].From != "" || ch[0].To != "" || !ch[0].LockEntry || len(ch[0].CreatedLinks) != 0 {
		t.Errorf("preview = %+v, want the entry alone", ch)
	}
	if _, err := m.engine().Apply(t.Context(), en.Plan); err != nil {
		t.Fatalf("Apply(enable) error = %v", err)
	}
	m.same("park the entry, then enable", before)
}
