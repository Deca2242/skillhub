package app_test

import (
	"slices"
	"testing"
)

// Parked lists nothing, and writes nothing, while no skill is disabled.
func TestParkedIsEmptyWithoutDisabledSkills(t *testing.T) {
	m := newMachine(t)
	before := m.hash()

	got, err := m.app.Parked()
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("Parked = %+v, want none", got)
	}
	if m.hash() != before {
		t.Fatal("Parked wrote something")
	}
}

// Parked lists each disabled skill from the version 2 state, sorted by key:
// its key, the harnesses whose links enabling recreates, since when it is
// disabled and whether its lock entry is parked. It only reads.
func TestParkedListsTheDisabledSkills(t *testing.T) {
	m := newMachine(t)
	since := m.clock.Now().UTC()
	for _, key := range []string{"i-have-adhd", "agent-browser"} {
		p, err := m.app.PlanDisable(bg, key)
		must(t, err)
		m.apply(p)
	}
	before := m.hash()

	got, err := m.app.Parked()
	must(t, err)
	if m.hash() != before {
		t.Fatal("Parked wrote something")
	}
	keys := make([]string, 0, len(got))
	for _, s := range got {
		keys = append(keys, s.Key)
	}
	if !slices.Equal(keys, []string{"agent-browser", "i-have-adhd"}) {
		t.Fatalf("keys = %v, want agent-browser and i-have-adhd, sorted", keys)
	}

	browser, adhd := got[0], got[1]
	if !browser.LockEntry {
		t.Error("agent-browser came from the lock, but its entry is not reported parked")
	}
	if adhd.LockEntry {
		t.Error("i-have-adhd has no lock entry, but one is reported parked")
	}
	if !slices.Contains(browser.Harnesses, "claude-code") {
		t.Errorf("agent-browser harnesses = %v, want claude-code among them", browser.Harnesses)
	}
	for _, h := range []string{"claude-code", "gemini-cli"} {
		if !slices.Contains(adhd.Harnesses, h) {
			t.Errorf("i-have-adhd harnesses = %v, want %s among them", adhd.Harnesses, h)
		}
	}
	if !slices.IsSorted(adhd.Harnesses) || len(slices.Compact(slices.Clone(adhd.Harnesses))) != len(adhd.Harnesses) {
		t.Errorf("harnesses = %v, want them sorted and unique", adhd.Harnesses)
	}
	for _, s := range got {
		if !s.Since.Equal(since) {
			t.Errorf("%s since %v, want %v", s.Key, s.Since, since)
		}
	}
}

// A skill enabled again leaves the list.
func TestParkedForgetsAnEnabledSkill(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	m.apply(p)
	p, err = m.app.PlanEnable(bg, "agent-browser")
	must(t, err)
	m.apply(p)

	got, err := m.app.Parked()
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("Parked = %+v, want none after enabling", got)
	}
}
