package remove_test

import (
	"context"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// parkEntry parks the lock entry of key alone, as repair's park_entry does:
// a record with the entry and no folder and no links.
func (e *env) parkEntry(key string) {
	e.t.Helper()
	b := e.eng.NewBuilder(engine.Meta{Action: "repair-park-entry"})
	must(e.t, b.LockPark(key))
	must(e.t, b.SetEffect(engine.Effect{
		DropManaged: []string{key},
		SetParked: []engine.ParkedEffect{{
			Skill: domain.ParkedSkill{Key: key, Links: []domain.LinkRecord{}}, EntryStep: 0,
		}},
	}))
	plan, err := b.Build()
	must(e.t, err)
	if _, err := e.eng.Apply(context.Background(), plan); err != nil {
		e.t.Fatalf("park the entry of %s: %v", key, err)
	}
}

// TestRemovingAParkedEntryWithoutAFolderAndUndoingIt: the record park_entry
// leaves has no folder; removing it drops the record and touches nothing on
// disk, and undo puts the record back as it was (L47).
func TestRemovingAParkedEntryWithoutAFolderAndUndoingIt(t *testing.T) {
	e := newEnv(t)
	e.addEntry("ghost", "owner/ghost", "skills/ghost/SKILL.md", "1111111111111111111111111111111111111111")
	e.addEntry("other", "owner/other", "skills/other/SKILL.md", "2222222222222222222222222222222222222222")
	e.parkEntry("ghost")
	parked := e.loadState().Parked["ghost"]
	if parked.Entry == nil || parked.Folder != "" {
		t.Fatalf("the fixture did not park the entry alone: %+v", parked)
	}
	before := e.everything()

	plan := e.plan("ghost")
	pv := plan.Preview
	if !pv.Parked || pv.Folder != "" || len(pv.Links) != 0 || pv.LockEntry != nil {
		t.Errorf("preview = parked %v folder %q links %v lock %+v", pv.Parked, pv.Folder, pv.Links, pv.LockEntry)
	}
	if pv.ParkedEntry == nil || string(pv.ParkedEntry.Raw) != string(parked.Entry.Raw) {
		t.Errorf("ParkedEntry = %+v, want the parked entry", pv.ParkedEntry)
	}
	if len(plan.Plan.Steps) != 0 {
		t.Errorf("steps = %+v, want none: nothing on disk is removed", plan.Plan.Steps)
	}

	out := e.apply(plan)
	if _, ok := e.loadState().Parked["ghost"]; ok {
		t.Error("Parked still holds ghost")
	}
	after := e.everything()
	if d := diffTrees(before.tree, after.tree); len(d) != 0 {
		t.Errorf("the removal touched the disk: %q", d)
	}
	if after.lock != before.lock {
		t.Error("the removal changed the lock")
	}

	e.undo(out)
	e.sameAs(before, "remove the parked entry and undo")
}
