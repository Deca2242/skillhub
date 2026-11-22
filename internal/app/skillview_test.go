package app_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/domain"
)

// Visibility tells, per skill, the detected harnesses that see it: the
// ones it is linked in and the ones that read the store. It only reads.
func TestVisibilityNamesTheHarnessesThatSeeEachSkill(t *testing.T) {
	m := newMachine(t)
	before := m.hash()

	got, err := m.app.Visibility(bg)
	must(t, err)
	seen := got["lock:agent-browser"]
	// claude-code sees it through its link, codex through the store.
	for _, want := range []string{"claude-code", "codex"} {
		if !slices.Contains(seen, want) {
			t.Errorf("Visibility[lock:agent-browser] = %v, lacks %q", seen, want)
		}
	}
	if !slices.IsSorted(seen) || len(slices.Compact(slices.Clone(seen))) != len(seen) {
		t.Errorf("Visibility[lock:agent-browser] = %v, want sorted without repeats", seen)
	}
	if m.hash() != before {
		t.Fatal("Visibility wrote something")
	}
}

// A skill parked by hand in OFF/ is available only where it is really
// seen: the harnesses Doctor's parked_but_visible finding names (codex,
// opencode and pi), not every harness that reads the store (L83).
func TestVisibilityOfAnOFFSkillIsWhoStillSeesIt(t *testing.T) {
	m := newMachine(t)
	vis, err := m.app.Visibility(bg)
	must(t, err)
	findings, err := m.app.Doctor(bg)
	must(t, err)
	for _, name := range graphtest.OffSkills {
		id := "lock:" + name
		var want []string
		for _, f := range findings {
			if f.Kind == "parked_but_visible" && f.Skill == id {
				want = f.Harnesses
			}
		}
		if len(want) == 0 {
			t.Fatalf("Doctor has no parked_but_visible finding for %s", id)
		}
		if got := vis[id]; !slices.Equal(got, want) {
			t.Errorf("Visibility[%s] = %v, want %v", id, got, want)
		}
	}
}

// Without the version 2 ports Visibility refuses, so the caller keeps the
// inventory's locations.
func TestVisibilityNeedsTheVersion2Ports(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.Visibility(bg); !errors.Is(err, app.ErrMissingDependency) {
		t.Fatalf("Visibility error = %v, want ErrMissingDependency", err)
	}
}

func TestAgentNameIsReadable(t *testing.T) {
	tests := []struct{ id, want string }{
		{"claude-code", "Claude Code"},
		{"gemini-cli", "Gemini CLI"},
		{"codex", "Codex"},
		{"opencode", "OpenCode"},
		{"github-copilot", "GitHub Copilot"},
		{"my-own-agent", "My Own Agent"},
		{"", ""},
	}
	var a app.App
	for _, tt := range tests {
		if got := a.AgentName(tt.id); got != tt.want {
			t.Errorf("AgentName(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

// Operation tells what a version 2 journal line did, by its snapshot: the
// action and the skills it touched; for an undo, the action it undid. It
// only reads.
func TestOperationTellsWhatAJournalLineDid(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.Plan(bg, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	must(t, err)
	m.apply(p)
	disable := m.lastV2()
	u, err := m.app.Plan(bg, app.Request{Action: app.ActionUndo, Snapshot: disable.SnapshotID})
	must(t, err)
	m.apply(u)
	undo := m.lastV2()
	before := m.hash()

	got, err := m.app.Operation(bg, disable.SnapshotID)
	must(t, err)
	if got.Action != "disable" || got.Undo || !slices.Equal(got.Keys, []string{"agent-browser"}) {
		t.Errorf("Operation(disable) = %+v", got)
	}
	got, err = m.app.Operation(bg, undo.SnapshotID)
	must(t, err)
	if got.Action != "disable" || !got.Undo || !slices.Equal(got.Keys, []string{"agent-browser"}) {
		t.Errorf("Operation(undo) = %+v, want the undone disable of agent-browser", got)
	}
	if _, err := m.app.Operation(bg, "nope"); err == nil {
		t.Error("Operation of an unknown snapshot did not fail")
	}
	if m.hash() != before {
		t.Fatal("Operation wrote something")
	}
}

// Now is the app's clock, which the TUI tells times against.
func TestNowIsTheAppsClock(t *testing.T) {
	m := newMachine(t)
	if got := m.app.Now(); !got.Equal(m.clock.Now()) {
		t.Errorf("Now = %v, want %v", got, m.clock.Now())
	}
}

// SkillActions offers, per skill, only what its planner plans now: every
// offered action plans without a refusal, and every action a planner would
// plan for the skill's key is offered. It only reads.
func TestSkillActionsAreWhatThePlannersPlan(t *testing.T) {
	m := newMachine(t)
	before := m.hash()
	skills, err := m.app.Skills(bg)
	must(t, err)
	got, err := m.app.SkillActions(bg)
	must(t, err)
	if m.hash() != before {
		t.Fatal("SkillActions wrote something")
	}
	plans := func(req app.Request) bool {
		_, err := m.app.Plan(bg, req)
		return err == nil
	}
	for _, s := range skills {
		acts := got[s.ID]
		key := acts.Key
		if key == "" {
			key = s.Name
		}
		for _, a := range []struct {
			action  app.Action
			offered bool
		}{{app.ActionDisable, acts.Disable}, {app.ActionRemove, acts.Remove}} {
			if ok := plans(app.Request{Action: a.action, Key: key}); ok != a.offered {
				t.Errorf("%s: %s offered = %v, but planning it succeeds = %v", s.ID, a.action, a.offered, ok)
			}
		}
		for _, h := range acts.Unlink {
			if !plans(app.Request{Action: app.ActionUnlink, Key: key, Harness: h}) {
				t.Errorf("%s: unlink from %s offered, but its planning is refused", s.ID, h)
			}
		}
		if h := acts.LinkIn; h != "" && !plans(app.Request{Action: app.ActionLink, Key: key, Harness: h}) {
			t.Errorf("%s: link in %s offered, but its planning is refused", s.ID, h)
		}
		if acts.Off && !plans(app.Request{Action: app.ActionAdoptOff}) {
			t.Errorf("%s: adopting OFF/ offered, but its planning is refused", s.ID)
		}
		for _, l := range acts.BrokenLinks {
			if l.Repair != "" && !plans(app.Request{Action: app.ActionRepair, Finding: l.Finding.ID, Repair: l.Repair}) {
				t.Errorf("%s: repair %s of %s offered, but its planning is refused", s.ID, l.Repair, l.Finding.ID)
			}
		}
	}
	if m.hash() != before {
		t.Fatal("planning wrote something")
	}
}

// The user's case (L83): a skill in OFF/ that Codex, OpenCode and Pi still
// see, with broken links in Kiro and others. Disabling and removing do not
// apply; adopting OFF/ does, for the four skills in it, and Doctor's repair
// of its broken links is that same adoption.
func TestSkillActionsOfAnOFFSkill(t *testing.T) {
	m := newMachine(t)
	got, err := m.app.SkillActions(bg)
	must(t, err)
	acts := got["lock:git-guardrails-claude-code"]
	if acts.Key != "" || acts.Disable || acts.Remove || len(acts.Unlink) > 0 || acts.LinkIn != "" {
		t.Errorf("OFF skill actions = %+v, want no store action", acts)
	}
	want := slices.Sorted(slices.Values(graphtest.OffSkills))
	if !acts.Off || acts.OffKey != "git-guardrails-claude-code" || !slices.Equal(acts.OffKeys, want) {
		t.Errorf("Off = %v, OffKeys = %v; want true, %v", acts.Off, acts.OffKeys, want)
	}
	var kiro *app.LinkFix
	for i, l := range acts.BrokenLinks {
		if l.Finding.ID == "broken_link:.kiro/skills/git-guardrails-claude-code" {
			kiro = &acts.BrokenLinks[i]
		}
	}
	if kiro == nil || kiro.Repair != "adopt_off" {
		t.Errorf("BrokenLinks = %+v, want the Kiro link repaired by adopt_off", acts.BrokenLinks)
	}
	// Junie, Kilocode and Kiro hold its three broken links.
	if len(acts.BrokenLinks) != 3 {
		t.Errorf("BrokenLinks = %d, want 3", len(acts.BrokenLinks))
	}

	ab := got["lock:agent-browser"]
	if ab.Key != "agent-browser" || !ab.Disable || !ab.Remove || !slices.Contains(ab.Unlink, "claude-code") || ab.Off {
		t.Errorf("agent-browser actions = %+v, want key, disable, remove and unlink from claude-code", ab)
	}
}

// A broken link named after an active store skill is repaired by pointing it
// at that skill again: the row offers that repair.
func TestSkillActionsRepointABrokenLinkOfAnActiveSkill(t *testing.T) {
	m := newMachine(t)
	m.h.Symlink(".kiro/skills/agent-browser", "../../.agents/skills/old-agent-browser")
	got, err := m.app.SkillActions(bg)
	must(t, err)
	links := got["lock:agent-browser"].BrokenLinks
	if len(links) != 1 || links[0].Finding.ID != "broken_link:.kiro/skills/agent-browser" || links[0].Repair != "repoint_link" {
		t.Fatalf("BrokenLinks = %+v, want the Kiro link repaired by repoint_link", links)
	}
	if _, err := m.app.Plan(bg, app.Request{Action: app.ActionRepair, Finding: links[0].Finding.ID, Repair: links[0].Repair}); err != nil {
		t.Fatalf("plan the offered repair: %v", err)
	}
	// The link names the agents whose folder holds it (UX3).
	if !slices.Equal(links[0].Harnesses, []string{"kiro-cli"}) {
		t.Errorf("Harnesses = %v, want [kiro-cli]", links[0].Harnesses)
	}
}

// Without the version 2 ports SkillActions refuses, and nothing is offered.
func TestSkillActionsNeedTheVersion2Ports(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.SkillActions(bg); !errors.Is(err, app.ErrMissingDependency) {
		t.Fatalf("SkillActions error = %v, want ErrMissingDependency", err)
	}
}

// Overview is what SkillActions and Doctor tell, read on one observation,
// and which findings Doctor offers a repair for now (UX3: the TUI reads it
// once per load for the panel and the Salud counter). It only reads.
func TestOverviewIsTheActionsAndTheFindingsOfOneLoad(t *testing.T) {
	m := newMachine(t)
	before := m.hash()
	ov, err := m.app.Overview(bg)
	must(t, err)
	if m.hash() != before {
		t.Fatal("Overview wrote something")
	}
	acts, err := m.app.SkillActions(bg)
	must(t, err)
	if !reflect.DeepEqual(ov.Actions, acts) {
		t.Errorf("Overview.Actions differ from SkillActions:\n%+v\n%+v", ov.Actions, acts)
	}
	findings, err := m.app.Doctor(bg)
	must(t, err)
	if !reflect.DeepEqual(ov.Findings, findings) {
		t.Errorf("Overview.Findings differ from Doctor:\n%+v\n%+v", ov.Findings, findings)
	}
	repairable := 0
	for _, f := range findings {
		offer, err := m.app.Repairs(bg, f.ID)
		must(t, err)
		if got, want := ov.Repairable[f.ID], len(offer.Repairs) > 0; got != want {
			t.Errorf("Repairable[%s] = %v, want %v (%+v)", f.ID, got, want, offer)
		}
		if len(offer.Repairs) > 0 {
			repairable++
		}
	}
	if repairable == 0 || repairable == len(findings) {
		t.Fatalf("the fixture should have repairable and other findings: %d of %d", repairable, len(findings))
	}
	if m.hash() != before {
		t.Fatal("reading the findings wrote something")
	}
}

// Without the version 2 ports Overview refuses, as SkillActions does.
func TestOverviewNeedsTheVersion2Ports(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.Overview(bg); !errors.Is(err, app.ErrMissingDependency) {
		t.Fatalf("Overview error = %v, want ErrMissingDependency", err)
	}
}

// AgentChoices lists, for a store skill, the detected agents it can be
// linked in (the ones with a link folder), whether it is linked there and
// whether the app can change that now; and, apart, the detected agents that
// read the store and need no link (UX3). Each changeable choice plans. It
// only reads.
func TestAgentChoicesOfAStoreSkill(t *testing.T) {
	m := newMachine(t)
	before := m.hash()
	ch, err := m.app.AgentChoices(bg, "agent-browser")
	must(t, err)
	if m.hash() != before {
		t.Fatal("AgentChoices wrote something")
	}
	if ch.Key != "agent-browser" {
		t.Errorf("Key = %q", ch.Key)
	}
	for _, c := range ch.Agents {
		if c.Harness == "codex" {
			t.Errorf("codex reads the store: it is no link choice: %+v", ch.Agents)
		}
	}
	claude := slices.IndexFunc(ch.Agents, func(c app.AgentChoice) bool { return c.Harness == "claude-code" })
	if claude < 0 || !ch.Agents[claude].Linked || !ch.Agents[claude].Changeable {
		t.Fatalf("claude-code should be linked and changeable: %+v", ch.Agents)
	}
	unlinked := 0
	for _, c := range ch.Agents {
		if !c.Changeable {
			continue
		}
		req := app.Request{Action: app.ActionLink, Key: "agent-browser", Harness: c.Harness}
		if c.Linked {
			req.Action = app.ActionUnlink
		} else {
			unlinked++
		}
		if _, err := m.app.Plan(bg, req); err != nil {
			t.Errorf("%s offered for %s, but its planning is refused: %v", req.Action, c.Harness, err)
		}
	}
	if unlinked == 0 {
		t.Errorf("no agent to link agent-browser in: %+v", ch.Agents)
	}
	for _, want := range []string{"codex", "opencode", "pi"} {
		if !slices.Contains(ch.ReadStore, want) {
			t.Errorf("ReadStore = %v, lacks %s", ch.ReadStore, want)
		}
	}
	if slices.Contains(ch.ReadStore, "claude-code") {
		t.Errorf("ReadStore = %v: claude-code needs a link", ch.ReadStore)
	}
	if m.hash() != before {
		t.Fatal("planning wrote something")
	}
}

// With no key, AgentChoices tells the install's choices: the agents with a
// link folder that are detected, none linked, and the ones that read the
// store.
func TestAgentChoicesForAnInstall(t *testing.T) {
	m := newMachine(t)
	ch, err := m.app.AgentChoices(bg, "")
	must(t, err)
	targets, err := m.app.ProposeTargets(bg)
	must(t, err)
	var ids []string
	for _, c := range ch.Agents {
		ids = append(ids, c.Harness)
		if c.Linked || !c.Changeable {
			t.Errorf("an install choice is unlinked and changeable: %+v", c)
		}
	}
	for _, id := range ids {
		if !slices.Contains(targets, id) {
			t.Errorf("choice %s is not a proposed target %v", id, targets)
		}
	}
	if len(ids) == 0 || !slices.Contains(ch.ReadStore, "codex") {
		t.Errorf("choices = %v, readers = %v", ids, ch.ReadStore)
	}
}

// Ignored lists the skills the ignore setting leaves out, each with the
// entries that name it, as the inventory finds them without the setting
// (UX4). An entry that names no skill names nothing here. It only reads,
// and the inventory keeps leaving those skills out.
func TestIgnoredListsTheSkillsTheSettingLeavesOut(t *testing.T) {
	m := newMachine(t)
	none, err := m.app.Ignored(bg)
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("Ignored without the setting = %+v, want none", none)
	}

	_, err = m.app.SetSetting(app.KeyIgnore, "grill-with-docs,gone-skill,lock:grill-with-docs", false)
	must(t, err)
	before := m.hash()

	got, err := m.app.Ignored(bg)
	must(t, err)
	if len(got) != 1 {
		t.Fatalf("Ignored = %+v, want only grill-with-docs", got)
	}
	if got[0].Skill.Name != "grill-with-docs" ||
		!slices.Equal(got[0].Entries, []string{"grill-with-docs", "lock:grill-with-docs"}) {
		t.Errorf("Ignored[0] = %s %v, want grill-with-docs named by both entries", got[0].Skill.Name, got[0].Entries)
	}
	skills, err := m.app.Skills(bg)
	must(t, err)
	if slices.ContainsFunc(skills, func(s domain.Skill) bool { return s.Name == "grill-with-docs" }) {
		t.Error("Skills lists grill-with-docs, which the setting ignores")
	}
	if m.hash() != before {
		t.Fatal("Ignored wrote something")
	}
}
