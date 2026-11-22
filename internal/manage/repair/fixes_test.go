package repair_test

import (
	"errors"
	"slices"
	"testing"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/repair"
	"skillhub/internal/shared/domain"
)

// disableFindSkills disables find-skills, whose links in .claude/skills and
// .pi/agent/skills the parked record keeps.
func (m *machine) disableFindSkills() {
	m.t.Helper()
	dis, err := m.toggle().Disable(m.graph(), "find-skills")
	must(m.t, err)
	if _, err := m.engine().Apply(m.t.Context(), dis.Plan); err != nil {
		m.t.Fatal(err)
	}
}

// TestEnableAndLinkKeepsALinkEnableWouldRecreate: the broken link sits where
// enabling recreates a link, with the very text npx writes. One plan enables
// the skill and keeps that link, now a precondition, leading to it.
func TestEnableAndLinkKeepsALinkEnableWouldRecreate(t *testing.T) {
	m := newMachine(t)
	m.disableFindSkills()
	path := m.h.Symlink(".claude/skills/find-skills", "../../.agents/skills/find-skills")
	f := m.finding("broken_link:.claude/skills/find-skills")
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.EnableAndLink, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	var onPath []engine.StepKind
	for _, s := range pr.Plan.Steps {
		if s.Path == path {
			onPath = append(onPath, s.Kind)
		}
	}
	if !slices.Equal(onPath, []engine.StepKind{engine.StepRequire}) {
		t.Errorf("steps on %s = %v, want only a require", path, onPath)
	}
	m.noWrite(before)
	out := m.apply(pr)
	if got := readlink(t, path); got != "../../.agents/skills/find-skills" {
		t.Errorf("the link is %q", got)
	}
	if got := readlink(t, m.h.Path(".pi/agent/skills/find-skills")); got != "../../../.agents/skills/find-skills" {
		t.Errorf("the Pi link is %q", got)
	}
	if m.reports(f.ID) {
		t.Errorf("Doctor still reports %s", f.ID)
	}
	m.undo(out)
	m.same("enable and keep the link, apply and undo", before)
}

// TestEnableAndLinkReplacesAStaleLinkEnableWouldRecreate: the broken link
// sits where enabling recreates a link, but leads elsewhere; it is replaced
// in place by the link enabling records.
func TestEnableAndLinkReplacesAStaleLinkEnableWouldRecreate(t *testing.T) {
	m := newMachine(t)
	m.disableFindSkills()
	path := m.h.Symlink(".claude/skills/find-skills", "../../.agents/skills/find-skills-old")
	f := m.finding("broken_link:.claude/skills/find-skills")
	before := m.snap()
	pr, err := m.planner().Plan(f, repair.EnableAndLink, repair.Options{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	m.noWrite(before)
	out := m.apply(pr)
	if got := readlink(t, path); got != "../../.agents/skills/find-skills" {
		t.Errorf("the link is %q", got)
	}
	if m.reports(f.ID) {
		t.Errorf("Doctor still reports %s", f.ID)
	}
	m.undo(out)
	m.same("enable and replace the link, apply and undo", before)
}

// TestAdoptPinsTheCanonicalFolder: the store folder adoption keeps is a
// precondition: if npx updates or removes it between planning and applying,
// Apply refuses and nothing changes.
func TestAdoptPinsTheCanonicalFolder(t *testing.T) {
	changes := map[string]func(m *machine){
		"updated": func(m *machine) {
			m.h.WriteFile(".agents/skills/find-skills/SKILL.md", graphtest.SkillMD("find-skills", "updated"))
		},
		"removed": func(m *machine) { must(m.t, removeAll(m.h.Path(".agents/skills/find-skills"))) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			m.copyTree("find-skills", ".kiro/skills/find-skills")
			f := m.finding("identical_copies:" + skillID(t, m.graph(), "find-skills"))
			pr, err := m.planner().Plan(f, repair.Adopt, repair.Options{})
			must(t, err)
			change(m)
			before := m.snap()
			if _, err := m.engine().Apply(t.Context(), pr.Plan); !errors.Is(err, engine.ErrPrecondition) {
				t.Fatalf("Apply() error = %v, want ErrPrecondition", err)
			}
			m.noWrite(before)
		})
	}
}

// TestRepointPinsTheTargetSkill: the active skill the link will lead to is a
// precondition of re-pointing.
func TestRepointPinsTheTargetSkill(t *testing.T) {
	m := newMachine(t)
	m.h.Symlink(".kiro/skills/find-skills", "../../.agents/skills/find-skills-old")
	pr, err := m.planner().Plan(m.finding("broken_link:.kiro/skills/find-skills"), repair.RepointLink, repair.Options{})
	must(t, err)
	must(t, removeAll(m.h.Path(".agents/skills/find-skills")))
	before := m.snap()
	if _, err := m.engine().Apply(t.Context(), pr.Plan); !errors.Is(err, engine.ErrPrecondition) {
		t.Fatalf("Apply() error = %v, want ErrPrecondition", err)
	}
	m.noWrite(before)
}

// TestAdoptRefusesAKeyTheLockOrTheStateHolds: a copy of unknown origin never
// takes a key that a lock entry, a parked record or a managed record holds,
// even when the store folder is not there.
func TestAdoptRefusesAKeyTheLockOrTheStateHolds(t *testing.T) {
	t.Run("lock entry", func(t *testing.T) {
		m := newMachine(t)
		withGhostEntry(m)
		m.h.WriteFile(".codex/skills/ghost-skill/SKILL.md", graphtest.SkillMD("ghost-skill", "someone else's"))
		f := m.finding("unmanaged_skill:" + string(domain.DirID(".codex/skills/ghost-skill")))
		before := m.snap()
		if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{}); !errors.Is(err, repair.ErrLockedSkill) {
			t.Errorf("Plan() error = %v, want ErrLockedSkill", err)
		}
		m.noWrite(before)
	})
	records := map[string]func(st *domain.PersistedState){
		"parked record": func(st *domain.PersistedState) {
			st.Parked = map[string]domain.ParkedSkill{"notes": {Key: "notes", Links: []domain.LinkRecord{}}}
		},
		"managed record": func(st *domain.PersistedState) {
			st.Managed = map[string]domain.ManagedSkill{"notes": {Key: "notes", Targets: []string{}}}
		},
	}
	for name, set := range records {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			st := m.state()
			set(&st)
			must(t, statefile.New(m.p).Save(st))
			m.h.WriteFile(".codex/skills/notes/SKILL.md", graphtest.SkillMD("notes", "notes"))
			f := m.finding("unmanaged_skill:" + string(domain.DirID(".codex/skills/notes")))
			before := m.snap()
			if _, err := m.planner().Plan(f, repair.Adopt, repair.Options{}); !errors.Is(err, repair.ErrLockedSkill) {
				t.Errorf("Plan() error = %v, want ErrLockedSkill", err)
			}
			m.noWrite(before)
		})
	}
}
