package toggle_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
)

// offLinks are, for each OFF/ skill of the fixture, every link that points
// to it: its link in ~/.claude/skills/OFF/ and the broken ones towards the
// store folder it was moved out of.
func offLinks(m *machine) map[string][]string {
	out := map[string][]string{}
	for _, n := range graphtest.OffSkills {
		out[n] = append(out[n], m.h.Path(".claude/skills/OFF/"+n))
	}
	for link, target := range graphtest.BrokenLinks {
		n := filepath.Base(target)
		out[n] = append(out[n], m.h.Path(link))
	}
	for n := range out {
		slices.Sort(out[n])
	}
	return out
}

// TestAdoptOffParksTheFourSkillsAndTheirLinks: one operation, previewed in
// full, moves the four OFF/ skills to the parking area, removes every link to
// them (the four of ~/.claude/skills/OFF/ and the 19 broken ones), parks
// their lock entries and never touches OFF/README.md; undo puts everything
// back byte for byte.
func TestAdoptOffParksTheFourSkillsAndTheirLinks(t *testing.T) {
	m := newMachine(t)
	world0, lock0, state0 := m.world(), readFile(t, m.p.SkillsLockFile()), m.state()
	readme := m.h.Path(".agents/skills/OFF/README.md")
	readme0 := readFile(t, readme)
	want := offLinks(m)

	pr, err := m.planner().AdoptOff(m.graph())
	if err != nil {
		t.Fatalf("AdoptOff() error = %v", err)
	}
	pv := pr.Preview
	if pv.Action != toggle.ActionAdoptOff || pv.PlanID != pr.Plan.ID {
		t.Fatalf("preview = %+v", pv)
	}
	var keys []string
	removed := 0
	for _, c := range pv.Skills {
		keys = append(keys, c.Key)
		if c.From != m.h.Path(".agents/skills/OFF/"+c.Key) || c.To != filepath.Join(m.p.ParkedDir(), c.Key) || !c.LockEntry {
			t.Errorf("change = %+v", c)
		}
		if got := removedPaths(c); !reflect.DeepEqual(got, want[c.Key]) {
			t.Errorf("%s: removed links = %v, want %v", c.Key, got, want[c.Key])
		}
		removed += len(c.RemovedLinks)
	}
	if !reflect.DeepEqual(keys, graphtest.OffSkills) {
		t.Errorf("skills = %v, want %v", keys, graphtest.OffSkills)
	}
	if removed != 4+len(graphtest.BrokenLinks) || len(graphtest.BrokenLinks) != 19 {
		t.Errorf("%d links removed, want 4 + 19", removed)
	}
	if !slices.Contains(pv.Kept, readme) {
		t.Errorf("kept = %v, want OFF/README.md listed", pv.Kept)
	}
	for _, s := range pr.Plan.Steps {
		for _, p := range []string{s.Path, s.Src, s.Dst} {
			if p == readme {
				t.Errorf("a %s step touches OFF/README.md", s.Kind)
			}
		}
	}

	out := m.apply(pr)
	entries, err := os.ReadDir(m.h.Path(".agents/skills/OFF"))
	must(t, err)
	if len(entries) != 1 || entries[0].Name() != "README.md" || !bytes.Equal(readFile(t, readme), readme0) {
		t.Errorf("OFF/ holds %v after adopting, want only the untouched README.md", entries)
	}
	for n, links := range want {
		if !exists(t, filepath.Join(m.p.ParkedDir(), n, "SKILL.md")) {
			t.Errorf("%s was not parked", n)
		}
		for _, l := range links {
			if exists(t, l) {
				t.Errorf("link %s is still there", l)
			}
		}
	}
	for _, k := range m.lockKeys() {
		if slices.Contains(graphtest.OffSkills, k) {
			t.Errorf("the lock still holds %s", k)
		}
	}
	st := m.state()
	for _, n := range graphtest.OffSkills {
		ps, ok := st.Parked[n]
		if !ok || ps.Entry == nil || ps.Entry.Name != n || ps.SnapshotID != out.SnapshotID {
			t.Errorf("parked %s = %+v", n, ps)
			continue
		}
		// Enabling it later recreates only the links that lead to the store
		// folder <store>/<key>: the broken ones, exactly as they were.
		var wantRestore []domain.LinkRecord
		for link, target := range graphtest.BrokenLinks {
			if filepath.Base(target) == n {
				wantRestore = append(wantRestore, domain.LinkRecord{Path: m.h.Path(link), Target: target})
			}
		}
		slices.SortFunc(wantRestore, func(x, y domain.LinkRecord) int { return compare(x.Path, y.Path) })
		if !reflect.DeepEqual(ps.Links, wantRestore) {
			t.Errorf("%s restores %v, want %v", n, ps.Links, wantRestore)
		}
	}

	if _, err := m.engine().Undo(t.Context(), out.SnapshotID); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	sameWorld(t, "adopt then undo", world0, m.world())
	if !bytes.Equal(readFile(t, m.p.SkillsLockFile()), lock0) {
		t.Error("the lock is not identical after undo")
	}
	if got := m.state(); !reflect.DeepEqual(got.Parked, state0.Parked) || !reflect.DeepEqual(got.Managed, state0.Managed) {
		t.Errorf("state after undo: parked %v managed %v", got.Parked, got.Managed)
	}
}

// TestAdoptOffWarnsWhenAnOwnCopyKeepsASkillVisible: an own copy of an
// adopted skill stays, untouched, and the preview says who still sees it.
func TestAdoptOffWarnsWhenAnOwnCopyKeepsASkillVisible(t *testing.T) {
	m := newMachine(t)
	skillMD := readFile(t, m.h.Path(".agents/skills/OFF/accessibility/SKILL.md"))
	m.h.WriteFile(".pi/agent/skills/acc-copy/SKILL.md", string(skillMD))
	copyMD := m.h.Path(".pi/agent/skills/acc-copy/SKILL.md")
	want := []graph.Sighting{{Harness: "pi", Kind: graph.OwnCopy, Entry: m.h.Path(".pi/agent/skills/acc-copy")}}

	pr, err := m.planner().AdoptOff(m.graph())
	must(t, err)
	for _, c := range pr.Preview.Skills {
		var w []graph.Sighting
		if c.Key == "accessibility" {
			w = want
		}
		if !reflect.DeepEqual(c.StillVisible, w) {
			t.Errorf("%s still visible = %+v, want %+v", c.Key, c.StillVisible, w)
		}
	}
	if !reflect.DeepEqual(pr.Preview.StillVisible, want) {
		t.Errorf("preview still visible = %+v, want %+v", pr.Preview.StillVisible, want)
	}
	m.apply(pr)
	if !bytes.Equal(readFile(t, copyMD), skillMD) {
		t.Error("the own copy changed")
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

// TestAnAdoptedSkillCanBeEnabled: enabling an adopted skill puts it in the
// store and recreates the links that now lead to it. The lock store puts an
// entry back only between the neighbours it had, so while a neighbour is
// still parked Enable refuses up front and names it; enabling all four in
// the reverse order gives back the lock byte for byte.
func TestAnAdoptedSkillCanBeEnabled(t *testing.T) {
	m := newMachine(t)
	lock0 := readFile(t, m.p.SkillsLockFile())
	pr, err := m.planner().AdoptOff(m.graph())
	must(t, err)
	m.apply(pr)

	world, st := m.world(), m.state()
	_, err = m.planner().Enable("accessibility")
	var ne *toggle.NeighbourParkedError
	if !errors.Is(err, toggle.ErrNeighbourParked) || !errors.As(err, &ne) ||
		ne.Key != "accessibility" || !reflect.DeepEqual(ne.Parked, []string{"domain-driven-design"}) {
		t.Fatalf("Enable(accessibility) error = %v, want ErrNeighbourParked naming domain-driven-design", err)
	}
	noWrite(t, m, world, st)

	for _, n := range []string{"web-quality-audit", "git-guardrails-claude-code", "domain-driven-design", "accessibility"} {
		en, err := m.planner().Enable(n)
		if err != nil {
			t.Fatalf("Enable(%s) error = %v", n, err)
		}
		m.apply(en)
	}
	if !bytes.Equal(readFile(t, m.p.SkillsLockFile()), lock0) {
		t.Error("the lock is not the original one after enabling the four")
	}
	if !exists(t, m.h.Path(".agents/skills/accessibility/SKILL.md")) {
		t.Fatal("accessibility is not in the store")
	}
	for link, target := range graphtest.BrokenLinks {
		if filepath.Base(target) != "accessibility" {
			continue
		}
		if _, err := os.Stat(m.h.Path(link)); err != nil {
			t.Errorf("%s does not lead to the skill: %v", link, err)
		}
	}
	if !slices.Contains(m.lockKeys(), "accessibility") {
		t.Error("the lock entry did not come back")
	}
}

func TestAdoptOffRefusals(t *testing.T) {
	t.Run("nothing to adopt", func(t *testing.T) {
		m := newMachine(t)
		pr, err := m.planner().AdoptOff(m.graph())
		must(t, err)
		m.apply(pr)
		world, st := m.world(), m.state()
		if _, err := m.planner().AdoptOff(m.graph()); !errors.Is(err, toggle.ErrNothingToAdopt) {
			t.Fatalf("second AdoptOff() error = %v, want ErrNothingToAdopt", err)
		}
		noWrite(t, m, world, st)
	})
	t.Run("a key taken in the store", func(t *testing.T) {
		m := newMachine(t)
		m.h.WriteFile(".agents/skills/web-quality-audit/SKILL.md", graphtest.SkillMD("web-quality-audit", "another one"))
		world, st := m.world(), m.state()
		if _, err := m.planner().AdoptOff(m.graph()); !errors.Is(err, toggle.ErrKeyInUse) {
			t.Fatalf("AdoptOff() error = %v, want ErrKeyInUse", err)
		}
		noWrite(t, m, world, st)
	})
	t.Run("a key already parked", func(t *testing.T) {
		m := newMachine(t)
		m.saveState(domain.PersistedState{Parked: map[string]domain.ParkedSkill{
			"accessibility": {Key: "accessibility", Folder: filepath.Join(m.p.ParkedDir(), "accessibility")}}})
		if _, err := m.planner().AdoptOff(m.graph()); !errors.Is(err, toggle.ErrAlreadyParked) {
			t.Fatalf("AdoptOff() error = %v, want ErrAlreadyParked", err)
		}
	})
}
