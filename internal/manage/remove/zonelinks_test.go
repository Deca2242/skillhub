package remove_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/remove"
)

// A link inside a foreign zone that resolves to the folder is never
// removed, but the removal leaves it broken, so the preview reports it with
// its zone. A zone link to another folder is not reported.
func TestLinksInForeignZonesAreReportedAndKept(t *testing.T) {
	e := machine(t)
	zoneLinks := []struct {
		rel, target string
		zone        graph.Zone
	}{
		{".agents/skills/_shared/linked", "../archify", graph.ZoneShared},
		{".claude/skills/go-testing/linked", "../../../.agents/skills/archify", graph.ZoneGentle},
		{".claude/skills/synced/2637_ab12/linked", "../../../../.agents/skills/archify", graph.ZoneProvider},
		{".codex/skills/.system/linked", "../../../.agents/skills/archify", graph.ZoneProvider},
		{graphtest.KarpathyClone + "/skills/linked", "../../../../.agents/skills/archify", graph.ZonePlugin},
		// Through the harness link: only resolving it shows the folder.
		{".codex/skills/.system/via-claude", "../../../.claude/skills/archify", graph.ZoneProvider},
	}
	var want []remove.KeptLink
	for _, l := range zoneLinks {
		e.h.Symlink(l.rel, l.target)
		want = append(want, remove.KeptLink{Path: e.h.Path(l.rel), Target: l.target, Zone: l.zone})
	}
	slices.SortFunc(want, func(x, y remove.KeptLink) int { return strings.Compare(x.Path, y.Path) })
	e.h.Symlink(".claude/skills/synced/2637_ab12/other", "../../../../.agents/skills/agent-browser")
	before := e.everything()

	plan := e.plan("archify")

	if !reflect.DeepEqual(plan.Preview.KeptLinks, want) {
		t.Errorf("KeptLinks =\n%+v\nwant\n%+v", plan.Preview.KeptLinks, want)
	}
	for _, l := range plan.Preview.Links {
		for _, k := range want {
			if l.Path == k.Path {
				t.Errorf("the plan removes %s, in a zone", l.Path)
			}
		}
	}

	e.apply(plan)

	after := e.everything()
	for _, l := range zoneLinks {
		if before.tree[l.rel] != after.tree[l.rel] {
			t.Errorf("%s changed: %q -> %q", l.rel, before.tree[l.rel], after.tree[l.rel])
		}
	}
}

// A zone link that still reaches a parked folder is reported too.
func TestALinkInAZoneToAParkedFolderIsReported(t *testing.T) {
	e := machine(t)
	e.disable("grill-me", ".claude/skills/grill-me")
	e.h.Symlink(".agents/skills/_shared/grill-me", "../../.skillhub/disabled/grill-me")

	plan := e.plan("grill-me")

	want := []remove.KeptLink{{Path: e.h.Path(".agents/skills/_shared/grill-me"), Target: "../../.skillhub/disabled/grill-me", Zone: graph.ZoneShared}}
	if !reflect.DeepEqual(plan.Preview.KeptLinks, want) {
		t.Errorf("KeptLinks = %+v, want %+v", plan.Preview.KeptLinks, want)
	}
}
