package app_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/graph/graphtest"
)

func TestSightingsReportsKindAndEntryPerHarness(t *testing.T) {
	m := newMachine(t)
	before := m.hash()

	got, err := m.app.Sightings(bg)
	must(t, err)

	// agent-browser: linked in claude-code, direct through the store in codex.
	browser := got["lock:agent-browser"]
	if !hasSighting(browser, "claude-code", app.SightingViaLink, ".claude/skills/agent-browser") {
		t.Errorf("Sightings[lock:agent-browser] lacks claude-code ViaLink: %+v", browser)
	}
	if !hasSighting(browser, "codex", app.SightingDirect, ".agents/skills/agent-browser") {
		t.Errorf("Sightings[lock:agent-browser] lacks codex Direct: %+v", browser)
	}

	// humanizer: own copy in claude-code.
	humanizer := findSkillSightings(got, "humanizer")
	if !hasSighting(humanizer, "claude-code", app.SightingOwnCopy, ".claude/skills/humanizer") {
		t.Errorf("Sightings for humanizer lacks claude-code OwnCopy: %+v", humanizer)
	}

	// OFF/ skill: keeps only Direct and ViaLink sightings, matching Visibility.
	vis, err := m.app.Visibility(bg)
	must(t, err)
	for _, name := range graphtest.OffSkills {
		id := "lock:" + name
		sgs := got[id]
		var harnesses []string
		for _, sg := range sgs {
			if sg.Kind != app.SightingDirect && sg.Kind != app.SightingViaLink {
				t.Errorf("Sightings[%s] includes non-direct/link sighting %+v", id, sg)
			}
			if !slices.Contains(harnesses, sg.Harness) {
				harnesses = append(harnesses, sg.Harness)
			}
		}
		slices.Sort(harnesses)
		if !slices.Equal(harnesses, vis[id]) {
			t.Errorf("Sightings[%s] harnesses = %v, want %v", id, harnesses, vis[id])
		}
	}

	if m.hash() != before {
		t.Fatal("Sightings wrote something")
	}
}

func TestSightingsNeedsTheVersion2Ports(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.Sightings(bg); !errors.Is(err, app.ErrMissingDependency) {
		t.Fatalf("Sightings error = %v, want ErrMissingDependency", err)
	}
}

func hasSighting(sgs []app.Sighting, harness, kind, relSuffix string) bool {
	wantSuffix := filepath.FromSlash(relSuffix)
	for _, sg := range sgs {
		if sg.Harness == harness && sg.Kind == kind && len(sg.Entry) >= len(wantSuffix) && sg.Entry[len(sg.Entry)-len(wantSuffix):] == wantSuffix {
			return true
		}
	}
	return false
}

func findSkillSightings(all map[string][]app.Sighting, name string) []app.Sighting {
	for id, sgs := range all {
		if id == name || strings.HasSuffix(id, ":"+name) || strings.HasSuffix(id, "/"+name) {
			return sgs
		}
	}
	return nil
}
