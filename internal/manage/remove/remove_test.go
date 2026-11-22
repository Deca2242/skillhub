package remove_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// The removal of an active skill takes out every link that resolves to its
// folder, the folder and the lock entry, drops it from Managed, and undo puts
// back the home, the lock and state.json byte for byte.
func TestRemovingAnActiveSkillAndUndoingIt(t *testing.T) {
	e := machine(t)
	p := e.h.Paths()
	e.saveState(domain.PersistedState{
		Managed: map[string]domain.ManagedSkill{
			"archify":       {Key: "archify", Targets: []string{"claude-code"}},
			"agent-browser": {Key: "agent-browser", Targets: []string{"claude-code"}},
		},
		Candidates: []domain.UpdateCandidate{
			{ID: "c-open", Target: domain.LockID("archify"), Skills: []string{domain.LockID("archify")}, State: domain.CandidatePending},
			{ID: "c-done", Target: domain.LockID("archify"), Skills: []string{domain.LockID("archify")}, State: domain.CandidateApplied},
			{ID: "c-other", Target: domain.LockID("agent-browser"), Skills: []string{domain.LockID("agent-browser")}, State: domain.CandidatePending},
		},
	})
	lockBefore, err := e.lock.Read()
	must(t, err)
	before := e.everything()

	plan := e.plan("archify")

	pv := plan.Preview
	folder := filepath.Join(p.StoreDir(), "archify")
	if pv.Key != "archify" || pv.Parked || pv.Folder != folder || pv.TreeSHA == "" {
		t.Errorf("preview = key %q parked %v folder %q tree %q", pv.Key, pv.Parked, pv.Folder, pv.TreeSHA)
	}
	wantLinks := []remove.Link{{Path: e.h.Path(".claude/skills/archify"), Target: "../../.agents/skills/archify", Harnesses: []string{"claude-code", "opencode"}}}
	if !reflect.DeepEqual(pv.Links, wantLinks) {
		t.Errorf("Links = %+v, want %+v", pv.Links, wantLinks)
	}
	var raw []byte
	for _, en := range lockBefore.Entries {
		if en.Name == "archify" {
			raw = en.Raw
		}
	}
	if pv.LockEntry == nil || pv.LockEntry.Name != "archify" || string(pv.LockEntry.Raw) != string(raw) || pv.LockEntry.DeletesLock {
		t.Errorf("LockEntry = %+v, want archify with its bytes, lock kept", pv.LockEntry)
	}
	if pv.ParkedEntry != nil {
		t.Errorf("ParkedEntry = %+v, want nil", pv.ParkedEntry)
	}
	if pv.Modified.State != remove.ModifiedNo || pv.Modified.Expected != pv.TreeSHA || pv.Modified.Actual != pv.TreeSHA {
		t.Errorf("Modified = %+v, want no against %s", pv.Modified, pv.TreeSHA)
	}
	if !slices.Equal(pv.PendingCandidates, []string{"c-open"}) {
		t.Errorf("PendingCandidates = %v, want [c-open]", pv.PendingCandidates)
	}
	if !slices.Contains(pv.Targets, "claude-code") || !slices.Contains(pv.Targets, "codex") {
		t.Errorf("Targets = %v, want claude-code (link) and codex (reads the store)", pv.Targets)
	}
	if plan.Plan.ID == "" || plan.Plan.Action != "remove" {
		t.Errorf("plan = id %q action %q", plan.Plan.ID, plan.Plan.Action)
	}

	out := e.apply(plan)

	if exists(t, folder) || exists(t, e.h.Path(".claude/skills/archify")) {
		t.Error("the folder or its link is still there after the removal")
	}
	if !exists(t, e.h.Path(".claude/skills/agent-browser")) || !exists(t, filepath.Join(p.StoreDir(), "agent-browser")) {
		t.Error("the removal touched another skill")
	}
	lockAfter, err := e.lock.Read()
	must(t, err)
	if len(lockAfter.Entries) != len(lockBefore.Entries)-1 || slices.ContainsFunc(lockAfter.Entries, func(en domain.LockEntry) bool { return en.Name == "archify" }) {
		t.Errorf("lock after = %d entries, want %d without archify", len(lockAfter.Entries), len(lockBefore.Entries)-1)
	}
	st := e.loadState()
	if _, ok := st.Managed["archify"]; ok {
		t.Error("Managed still holds archify")
	}
	if _, ok := st.Managed["agent-browser"]; !ok {
		t.Error("Managed lost agent-browser")
	}

	e.undo(out)
	e.sameAs(before, "remove and undo")
}

// A parked skill's folder is in the parking zone and its entry in Parked,
// not in the lock: the removal deletes the folder and drops Parked[key],
// leaves the lock alone, and undo puts Parked[key] back.
func TestRemovingAParkedSkillAndUndoingIt(t *testing.T) {
	e := machine(t)
	p := e.h.Paths()
	e.disable("grill-me", ".claude/skills/grill-me")
	parked := e.loadState().Parked["grill-me"]
	if parked.Entry == nil {
		t.Fatal("the fixture did not park the lock entry")
	}
	before := e.everything()

	plan := e.plan("grill-me")

	pv := plan.Preview
	folder := filepath.Join(p.ParkedDir(), "grill-me")
	if !pv.Parked || pv.Folder != folder || len(pv.Links) != 0 || pv.LockEntry != nil {
		t.Errorf("preview = parked %v folder %q links %v lock %+v", pv.Parked, pv.Folder, pv.Links, pv.LockEntry)
	}
	if pv.ParkedEntry == nil || string(pv.ParkedEntry.Raw) != string(parked.Entry.Raw) {
		t.Errorf("ParkedEntry = %+v, want the parked entry", pv.ParkedEntry)
	}
	if pv.Modified.State != remove.ModifiedNo {
		t.Errorf("Modified = %+v, want no", pv.Modified)
	}
	for _, s := range plan.Plan.Steps {
		if s.Kind == engine.StepLockRemove || s.Kind == engine.StepLockPark {
			t.Errorf("the plan has a %s step for a parked skill", s.Kind)
		}
	}

	out := e.apply(plan)

	if exists(t, folder) {
		t.Error("the parked folder is still there")
	}
	if _, ok := e.loadState().Parked["grill-me"]; ok {
		t.Error("Parked still holds grill-me")
	}
	if got := fileOrAbsent(t, p.SkillsLockFile()); got != before.lock {
		t.Error("the removal of a parked skill changed the lock")
	}

	e.undo(out)
	e.sameAs(before, "remove and undo")
	if got := e.loadState().Parked["grill-me"]; got.Entry == nil || string(got.Entry.Raw) != string(parked.Entry.Raw) {
		t.Errorf("undo left Parked[grill-me] = %+v", got)
	}
}

// The last entry of the lock goes with LockRemoveLast, which deletes the
// lock; undo writes it back byte for byte.
func TestRemovingTheLastLockEntryAndUndoingIt(t *testing.T) {
	e := newEnv(t)
	p := e.h.Paths()
	e.skill("solo")
	e.h.Symlink(".claude/skills/solo", "../../.agents/skills/solo")
	sha := e.treeSHA(".agents/skills/solo")
	e.addEntry("solo", "owner/solo", "skills/solo/SKILL.md", sha)
	before := e.everything()

	plan := e.plan("solo")

	if pv := plan.Preview; pv.LockEntry == nil || !pv.LockEntry.DeletesLock {
		t.Errorf("LockEntry = %+v, want the last entry, deleting the lock", pv.LockEntry)
	}

	out := e.apply(plan)

	if exists(t, p.SkillsLockFile()) {
		t.Error("the lock is still there after removing its last entry")
	}

	e.undo(out)
	e.sameAs(before, "remove and undo")
}

// The last entry carries a ref: a lock deleted with it could only come back
// through an add, and an add never writes a ref (L32). The entry leaves the
// lock, the lock stays, and undo puts the entry back with its ref.
func TestTheLastEntryWithARefKeepsTheLock(t *testing.T) {
	e := newEnv(t)
	p := e.h.Paths()
	e.skill("pinned")
	sha := e.treeSHA(".agents/skills/pinned")
	e.h.WriteFile(".agents/.skill-lock.json", `{
  "version": 3,
  "skills": {
    "pinned": {
      "source": "owner/pinned",
      "sourceType": "github",
      "ref": "v1",
      "skillFolderHash": "`+sha+`"
    }
  },
  "dismissed": {}
}`)
	before := e.everything()

	plan := e.plan("pinned")

	if pv := plan.Preview; pv.LockEntry == nil || pv.LockEntry.DeletesLock {
		t.Errorf("LockEntry = %+v, want the entry with the lock kept", pv.LockEntry)
	}

	out := e.apply(plan)

	lock, err := e.lock.Read()
	must(t, err)
	if !exists(t, p.SkillsLockFile()) || len(lock.Entries) != 0 {
		t.Errorf("lock after = %d entries, want an empty lock", len(lock.Entries))
	}

	e.undo(out)
	e.sameAs(before, "remove and undo")
}

// A real copy with the same name in another harness, or in the read-only
// profile, is reported and never touched.
func TestAHomonymInAnotherHarnessIsReportedAndKept(t *testing.T) {
	e := machine(t)
	e.skill("humanizer")
	e.addEntry("humanizer", "owner/humanizer", "skills/humanizer/SKILL.md", e.treeSHA(".agents/skills/humanizer"))
	before := e.everything()

	plan := e.plan("humanizer")

	copyPath := e.h.Path(".claude/skills/humanizer")
	i := slices.IndexFunc(plan.Preview.Homonyms, func(h remove.Homonym) bool { return h.Path == copyPath })
	if i < 0 {
		t.Fatalf("Homonyms = %+v, want %s", plan.Preview.Homonyms, copyPath)
	}
	if h := plan.Preview.Homonyms[i]; h.Name != "humanizer" || h.Identical || !slices.Contains(h.Harnesses, "claude-code") || h.Profile != "" {
		t.Errorf("homonym = %+v", h)
	}
	if slices.Contains(linkPaths(plan.Preview.Links), copyPath) {
		t.Error("the plan removes the homonym")
	}

	e.apply(plan)

	after := e.everything()
	if !reflect.DeepEqual(subtree(before, ".claude/skills/humanizer"), subtree(after, ".claude/skills/humanizer")) {
		t.Error("the removal changed the homonym in Claude Code")
	}

	// The profile's divergent copy of orca-cli is reported with its profile.
	plan = e.plan("orca-cli")
	omni := e.h.Path(".claude-omni/skills/orca-cli")
	i = slices.IndexFunc(plan.Preview.Homonyms, func(h remove.Homonym) bool { return h.Path == omni })
	if i < 0 || plan.Preview.Homonyms[i].Profile != "claude-omni" || plan.Preview.Homonyms[i].Identical {
		t.Errorf("Homonyms = %+v, want the claude-omni copy", plan.Preview.Homonyms)
	}
}

// A link named like the skill but resolving to another folder stays.
func TestALinkToAnotherFolderIsKept(t *testing.T) {
	e := machine(t)
	e.h.Symlink(".codex/skills/archify", "../../.agents/skills/agent-browser")
	e.h.Symlink(".gemini/config/skills/archify-old", "../../../.agents/skills/grill-me")
	before := e.everything()

	plan := e.plan("archify")

	for _, path := range []string{e.h.Path(".codex/skills/archify"), e.h.Path(".gemini/config/skills/archify-old")} {
		if slices.Contains(linkPaths(plan.Preview.Links), path) {
			t.Errorf("the plan removes %s, which resolves to another folder", path)
		}
	}

	e.apply(plan)

	after := e.everything()
	for _, rel := range []string{".codex/skills/archify", ".gemini/config/skills/archify-old"} {
		if before.tree[rel] != after.tree[rel] {
			t.Errorf("%s changed: %q -> %q", rel, before.tree[rel], after.tree[rel])
		}
	}
}

// An alias in the store and a harness link through it resolve to the
// folder, so they go with it; the lock entry is the one of the skill the
// graph attributes the folder to, not one with the folder's name.
func TestAnAliasGoesWithItsFolder(t *testing.T) {
	e := machine(t)
	before := e.everything()

	plan := e.plan("orca-computer-use")

	want := []string{
		e.h.Path(".agents/skills/computer-use"),
		e.h.Path(".claude/skills/computer-use"),
	}
	if got := linkPaths(plan.Preview.Links); !slices.Equal(got, want) {
		t.Errorf("Links = %v, want %v", got, want)
	}
	if pv := plan.Preview; pv.LockEntry == nil || pv.LockEntry.Name != "computer-use" {
		t.Errorf("LockEntry = %+v, want computer-use", pv.LockEntry)
	}
	if m := plan.Preview.Modified; m.State != remove.ModifiedYes || m.Expected != "0000000000000000000000000000000000000000" {
		t.Errorf("Modified = %+v, want yes against the recorded hash", m)
	}

	out := e.apply(plan)
	e.undo(out)
	e.sameAs(before, "remove and undo")
}

// A key that names no skill, or that is not one folder name, is a typed
// error and writes nothing.
func TestAKeyWithoutASkillIsATypedError(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want error
	}{
		{"unknown skill", "does-not-exist", remove.ErrNotFound},
		{"directory without SKILL.md", "OFF", remove.ErrNotFound},
		{"skill only in a harness", "brainstorming", remove.ErrNotFound},
		{"empty key", "", remove.ErrInvalidKey},
		{"dot dot", "..", remove.ErrInvalidKey},
		{"nested path", "OFF/accessibility", remove.ErrInvalidKey},
		{"loose file", "OFF/README.md", remove.ErrInvalidKey},
		{"alias", "computer-use", remove.ErrAlias},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := machine(t)
			before := e.everything()
			_, err := e.remover().Remove(tc.key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Remove(%q) error = %v, want %v", tc.key, err, tc.want)
			}
			e.sameAs(before, "a refused removal")
		})
	}
}

// A key that is both an active skill and a parked one is refused.
func TestAnActiveAndParkedKeyIsAmbiguous(t *testing.T) {
	e := machine(t)
	e.disable("grill-me", ".claude/skills/grill-me")
	e.skill("grill-me")
	before := e.everything()

	_, err := e.remover().Remove("grill-me")

	if !errors.Is(err, remove.ErrAmbiguous) {
		t.Fatalf("Remove() error = %v, want ErrAmbiguous", err)
	}
	e.sameAs(before, "a refused removal")
}

// Foreign zones are never targets, and a removal leaves them byte for byte
// even when they hold a copy of, or a link to, the skill removed.
func TestForeignZonesAreNeverTouched(t *testing.T) {
	cases := []struct {
		name string
		// zone is the subtree that must not change.
		zone string
		// setup plants what links the zone to key; refused tells that key
		// itself is in the zone.
		setup   func(e *env)
		key     string
		refused bool
	}{
		{name: "OFF/README.md", zone: ".agents/skills/OFF/README.md", key: "archify"},
		{
			name: "synced", zone: ".claude/skills/synced", key: "pdf",
			setup: func(e *env) {
				e.skill("pdf")
				e.h.Symlink(".claude/skills/synced/2637_ab12/linked-pdf", "../../../../.agents/skills/pdf")
			},
		},
		{
			name: "plugin", zone: graphtest.KarpathyClone, key: "karpathy-guidelines",
			setup: func(e *env) {
				e.skill("karpathy-guidelines")
				e.h.Symlink(graphtest.KarpathyClone+"/skills/linked", "../../../../.agents/skills/karpathy-guidelines")
			},
		},
		{
			name: "plugin in the store", zone: ".agents/skills/vendor-plugin", key: "vendor-plugin", refused: true,
			setup: func(e *env) {
				e.skill("vendor-plugin")
				e.h.WriteFile(".agents/skills/vendor-plugin/.claude-plugin/plugin.json", `{"name":"vendor"}`)
			},
		},
		{
			name: ".system", zone: ".codex/skills/.system", key: "skill-creator",
			setup: func(e *env) {
				e.skill("skill-creator")
				e.h.Symlink(".codex/skills/.system/linked", "../../../.agents/skills/skill-creator")
			},
		},
		{name: "_shared", zone: ".agents/skills/_shared", key: "_shared", refused: true},
		{name: "gentle-ai", zone: ".agents/skills/go-testing", key: "go-testing", refused: true},
		{
			name: "claude-omni", zone: ".claude-omni", key: "find-skills",
			setup: func(e *env) {
				e.h.Symlink(".claude-omni/skills/linked-find-skills", "../../.agents/skills/find-skills")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := machine(t)
			if tc.setup != nil {
				tc.setup(e)
			}
			before := e.everything()
			zone := subtree(before, tc.zone)
			if len(zone) == 0 {
				t.Fatalf("the fixture has nothing at %s", tc.zone)
			}

			if tc.refused {
				_, err := e.remover().Remove(tc.key)
				if !errors.Is(err, remove.ErrForeignZone) {
					t.Fatalf("Remove(%q) error = %v, want ErrForeignZone", tc.key, err)
				}
				e.sameAs(before, "a refused removal")
				return
			}

			plan := e.plan(tc.key)
			for _, l := range plan.Preview.Links {
				if rel, _ := filepath.Rel(e.h.Root(), l.Path); rel == tc.zone || isBelow(rel, tc.zone) {
					t.Errorf("the plan removes %s, in the zone", l.Path)
				}
			}
			e.apply(plan)
			if after := subtree(e.everything(), tc.zone); !reflect.DeepEqual(zone, after) {
				t.Errorf("the removal changed the zone: %v", diffTrees(zone, after))
			}
		})
	}
}

// A link to the folder in the read-only profile is reported, not removed.
func TestALinkInTheProfileIsReportedNotRemoved(t *testing.T) {
	e := machine(t)
	e.h.Symlink(".claude-omni/skills/linked-find-skills", "../../.agents/skills/find-skills")

	plan := e.plan("find-skills")

	want := []remove.KeptLink{{Path: e.h.Path(".claude-omni/skills/linked-find-skills"), Target: "../../.agents/skills/find-skills", Profile: "claude-omni"}}
	if !reflect.DeepEqual(plan.Preview.KeptLinks, want) {
		t.Errorf("KeptLinks = %+v, want %+v", plan.Preview.KeptLinks, want)
	}
	if slices.Contains(plan.Preview.Targets, "claude-omni") || slices.Contains(plan.Plan.Targets, "claude-omni") {
		t.Errorf("Targets = %v, want the read-only profile left out", plan.Preview.Targets)
	}
}

func isBelow(rel, dir string) bool {
	return len(rel) > len(dir) && rel[:len(dir)] == dir && rel[len(dir)] == '/'
}

// A link that still reaches a parked folder goes with it, and one in the
// read-only profile is reported and kept.
func TestALinkStillReachingAParkedFolderGoesWithIt(t *testing.T) {
	e := machine(t)
	e.disable("grill-me", ".claude/skills/grill-me")
	e.h.Symlink(".pi/agent/skills/grill-me", "../../../.agents/.skillhub/disabled/grill-me")
	e.h.Symlink(".claude-omni/skills/grill-me-parked", "../../.agents/.skillhub/disabled/grill-me")
	before := e.everything()

	plan := e.plan("grill-me")

	if got, want := linkPaths(plan.Preview.Links), []string{e.h.Path(".pi/agent/skills/grill-me")}; !slices.Equal(got, want) {
		t.Errorf("Links = %v, want %v", got, want)
	}
	if k := plan.Preview.KeptLinks; len(k) != 1 || k[0].Path != e.h.Path(".claude-omni/skills/grill-me-parked") {
		t.Errorf("KeptLinks = %+v, want the profile's link", k)
	}

	out := e.apply(plan)

	if exists(t, e.h.Path(".pi/agent/skills/grill-me")) {
		t.Error("the link to the parked folder is still there")
	}
	if !exists(t, e.h.Path(".claude-omni/skills/grill-me-parked")) {
		t.Error("the removal touched the profile")
	}
	e.undo(out)
	e.sameAs(before, "remove and undo")
}

// A parked skill whose recorded folder is not its place in the parking zone
// is refused: the state does not describe what is on disk.
func TestAParkedSkillOutsideItsPlaceIsRefused(t *testing.T) {
	e := machine(t)
	e.disable("grill-me", ".claude/skills/grill-me")
	st := e.loadState()
	parked := st.Parked["grill-me"]
	parked.Folder = e.h.Path(".agents/skills/archify")
	st.Parked["grill-me"] = parked
	e.saveState(st)
	before := e.everything()

	_, err := e.remover().Remove("grill-me")

	if !errors.Is(err, remove.ErrChanged) {
		t.Fatalf("Remove() error = %v, want ErrChanged", err)
	}
	e.sameAs(before, "a refused removal")
}

// A folder that two skills claim, here the lock entries computer-use
// (through its alias) and orca-computer-use, is refused: removing it would
// remove both.
func TestAFolderTwoSkillsClaimIsAmbiguous(t *testing.T) {
	e := machine(t)
	e.addEntry("orca-computer-use", "stablyai/orca", "skills/orca-computer-use/SKILL.md", e.treeSHA(".agents/skills/orca-computer-use"))
	before := e.everything()

	_, err := e.remover().Remove("orca-computer-use")

	if !errors.Is(err, remove.ErrAmbiguous) {
		t.Fatalf("Remove() error = %v, want ErrAmbiguous", err)
	}
	e.sameAs(before, "a refused removal")
}

// A clone the graph places in a plugin zone is refused as a foreign zone,
// even when it sits in the store.
func TestASkillOfAForeignZoneIsRefused(t *testing.T) {
	e := machine(t)
	e.skill("cloned")
	e.h.Mkdir(".agents/skills/cloned/.git")
	e.h.WriteFile(".agents/.claude-plugin/plugin.json", `{"name":"vendor"}`)
	real, err := filepath.EvalSymlinks(e.h.Path(".agents/skills/cloned"))
	must(t, err)
	e.git.AddClone(real, ports.CloneStatus{
		Remote: "https://github.com/owner/cloned.git", Branch: "main", Upstream: "origin/main",
		Head: "2222222222222222222222222222222222222222", Clean: true,
	})
	before := e.everything()

	_, err = e.remover().Remove("cloned")

	if !errors.Is(err, remove.ErrForeignZone) {
		t.Fatalf("Remove() error = %v, want ErrForeignZone", err)
	}
	e.sameAs(before, "a refused removal")
}
