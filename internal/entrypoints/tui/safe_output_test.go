package tui_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/updates"
)

func (f *fixture) installNamedLock(name string, files map[string]string) {
	f.t.Helper()
	dir := filepath.Join(f.home.Paths().StoreDir(), name)
	writeTree(f.t, dir, files)
	q := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			f.t.Fatal(err)
		}
		return string(b)
	}
	f.home.WriteFile(".agents/.skill-lock.json", fmt.Sprintf(
		"{\n  \"version\": 3,\n  \"skills\": {\n    %s: {\n      \"source\": %s,\n      \"sourceType\": \"github\",\n"+
			"      \"sourceUrl\": %s,\n      \"skillPath\": %s,\n      \"skillFolderHash\": %s,\n"+
			"      \"installedAt\": \"2026-11-20T00:00:00.000Z\",\n      \"updatedAt\": \"2026-11-20T00:00:00.000Z\"\n    }\n  }\n}\n",
		q(name), q(repo), q("https://github.com/"+repo+".git"), q("skills/"+name+"/SKILL.md"), q(hashOf(f.t, dir))))
}

func assertNoHostileRaw(t *testing.T, what, out string) {
	t.Helper()
	if strings.ContainsAny(out, "\r\x1b"+rlo) {
		t.Errorf("%s prints raw control/escape/bidi bytes:\n%q", what, out)
	}
	if !strings.Contains(out, hostileShown) {
		t.Errorf("%s lacks escaped %q:\n%s", what, hostileShown, out)
	}
}

func TestPresentSkillsAndParkedEscapeAndKeepSearchAndKeys(t *testing.T) {
	h := hostile
	skills := []domain.Skill{
		{
			ID:          "lock:evil" + h,
			Name:        "evil" + h + "\nline",
			Aliases:     []string{"alias" + h},
			Description: "desc" + h,
			Origin: domain.Origin{
				Kind: domain.OriginLock,
				Lock: &domain.LockOrigin{Source: "acme/repo" + h},
			},
			Locations: []domain.Location{
				{AgentID: "claude" + h, Kind: domain.LocationSymlink, Path: "/p/" + h},
			},
			State: domain.StateUpToDate,
		},
	}

	rows := tui.PresentSkills(skills, "desc"+h)
	if len(rows) != 1 {
		t.Fatalf("PresentSkills len = %d, want 1 (search must still match)", len(rows))
	}
	if rows[0].ID != "lock:evil"+h {
		t.Errorf("ID changed: got %q, want original", rows[0].ID)
	}
	assertNoHostileRaw(t, "PresentSkills Name", rows[0].Name)
	if strings.Contains(rows[0].Name, "\nline") {
		t.Errorf("PresentSkills Name allowed newline: %q", rows[0].Name)
	}
	assertNoHostileRaw(t, "PresentSkills Harnesses", rows[0].Harnesses)

	parked := tui.PresentParked([]app.ParkedSkill{
		{Key: "park" + h, Since: t0, Harnesses: []string{"codex" + h}, LockEntry: true},
	}, "")
	if len(parked) != 1 {
		t.Fatalf("PresentParked len = %d, want 1", len(parked))
	}
	if parked[0].ParkedKey != "park"+h {
		t.Errorf("ParkedKey changed: got %q, want original key for ActionEnable", parked[0].ParkedKey)
	}
	assertNoHostileRaw(t, "PresentParked Name", parked[0].Name)
	assertNoHostileRaw(t, "PresentParked Harnesses", parked[0].Harnesses)
}

func TestPresentSkillDetailEscapesThirdPartyText(t *testing.T) {
	h := hostile
	s := domain.Skill{
		ID:          "lock:evil" + h,
		Name:        "evil" + h,
		Aliases:     []string{"al" + h},
		Description: "desc" + h + "\nUbicaciones (0):",
		Origin: domain.Origin{
			Kind: domain.OriginLock,
			Lock: &domain.LockOrigin{
				Source:     "acme/repo" + h,
				SkillPath:  "skills/evil" + h + "/SKILL.md",
				FolderHash: "abc" + h,
				Ref:        "ref" + h,
			},
			GitClone: &domain.GitCloneOrigin{
				Remote:   "https://example.com/" + h,
				Branch:   "br" + h,
				Upstream: "up" + h,
				Head:     "def" + h,
			},
		},
		State: domain.StateUpToDate,
		Locations: []domain.Location{
			{AgentID: "codex" + h, Kind: domain.LocationSymlink, Path: "/home/u/" + h, ContentTreeSHA: "123" + h},
		},
	}
	sightings := []app.Sighting{{Harness: "codex" + h, Kind: "direct", Entry: "/entry/" + h}}
	activity := []app.ActivityEntry{{
		At:        t0.Add(-time.Hour),
		Action:    domain.ActionFailed,
		V2:        true,
		Operation: app.OperationView{Action: "act" + h},
	}}

	got := tui.PresentSkillDetailWithActivity(s, sightings, func(id string) string { return id }, activity, t0)
	assertNoHostileRaw(t, "PresentSkillDetailWithActivity", got)
	if strings.Contains(got, "\nUbicaciones (0):") {
		t.Errorf("PresentSkillDetailWithActivity allowed description newline injection:\n%s", got)
	}
	for _, want := range []string{
		"Skill: evil" + hostileShown + "\n",
		"ID: lock:evil" + hostileShown + "\n",
		"Alias: al" + hostileShown + "\n",
		"Descripción: desc" + hostileShown + `\nUbicaciones (0):` + "\n",
		"Repositorio: acme/repo" + hostileShown + "\n",
		"Ref: ref" + hostileShown + "\n",
		"Remoto: https://example.com/" + hostileShown + "\n",
		"(/entry/" + hostileShown + ")\n",
		"(/home/u/" + hostileShown + ", sha ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PresentSkillDetailWithActivity lacks %q:\n%s", want, got)
		}
	}
}

func TestPresentUpdatesApplyAllAndInterruptedEscapeAndKeepApprovals(t *testing.T) {
	h := hostile
	cands := []domain.UpdateCandidate{
		{
			ID:          "cand-ok",
			Target:      "lock:clean" + h,
			ToCommit:    "1" + h,
			ToSHA:       "aaaa11112222",
			State:       domain.CandidatePending,
			Labels:      []domain.Label{domain.LabelTextOnly},
			FileChanges: []domain.FileChange{{Path: "SKILL.md", Change: domain.ChangeModified}},
		},
		{
			ID:          "cand-blocked" + h,
			Target:      "git:.agents/skills/blocked" + h,
			ToCommit:    "2222222222",
			ToSHA:       "bbbb11112222",
			State:       domain.CandidateBlocked,
			BlockReason: domain.BlockReason("reason" + h),
		},
	}

	rows := tui.PresentUpdates(cands)
	if len(rows) != 2 {
		t.Fatalf("PresentUpdates len = %d, want 2", len(rows))
	}
	assertNoHostileRaw(t, "PresentUpdates[0].Target", rows[0].Target)
	assertNoHostileRaw(t, "PresentUpdates[0].ToCommit", rows[0].ToCommit)
	assertNoHostileRaw(t, "PresentUpdates[1].Target", rows[1].Target)
	assertNoHostileRaw(t, "PresentUpdates[1].State", rows[1].State)

	plan := tui.BuildApplyAllPlan(cands)
	aps := plan.Approvals()
	if len(aps) != 1 || aps[0].CandidateID != "cand-ok" || aps[0].ToSHA != "aaaa11112222" {
		t.Fatalf("Approvals changed: %+v", aps)
	}

	modal := tui.PresentApplyAllPlan(plan)
	assertNoHostileRaw(t, "PresentApplyAllPlan", modal)
	// Direct ApplyAllPlan with raw values must also escape.
	directModal := tui.PresentApplyAllPlan(tui.ApplyAllPlan{
		Included: []tui.UpdateRow{{Target: "lock:direct" + h, CandidateID: "c" + h, Labels: "l" + h, FileCount: 1}},
		Excluded: []tui.ExcludedCandidate{{Target: "lock:ex" + h, Reason: "why" + h}},
	})
	assertNoHostileRaw(t, "PresentApplyAllPlan direct", directModal)

	results := tui.PresentApplyAllResults([]updates.ApplyResult{
		{Target: "lock:res" + h, Outcome: updates.OutcomeApplied},
		{Target: "lock:res2" + h, Outcome: updates.Outcome("weird" + h)},
	})
	assertNoHostileRaw(t, "PresentApplyAllResults", results)

	interrupted := tui.PresentInterrupted(domain.Operation{
		Kind:              domain.OperationApply,
		Target:            "lock:op" + h,
		RestoreSnapshotID: "snap" + h,
		StartedAt:         t0,
	})
	assertNoHostileRaw(t, "PresentInterrupted", interrupted)

	sv := tui.PresentSettings(domain.Settings{CheckInterval: time.Hour, Ignore: []string{"ign" + h}}, ports.BackgroundStatus{})
	assertNoHostileRaw(t, "PresentSettings.Ignore", sv.Ignore)

	amb := tui.ExplainError(&app.AmbiguousError{Ref: "ref" + h, IDs: []string{"lock:a" + h, "lock:b" + h}})
	assertNoHostileRaw(t, "ExplainError AmbiguousError", amb)

	choices := tui.PresentChoices([]string{"item" + h + "\nforged"}, 0)
	assertNoHostileRaw(t, "PresentChoices", choices)
	if strings.Contains(choices, "\nforged") {
		t.Errorf("PresentChoices allowed newline injection: %q", choices)
	}
}

func yamlQuote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTUIUpdateConfirmationsApplyAllAndUndoEscapeAndPreserveExactTarget(t *testing.T) {
	f := newFixture(t)
	evilName := "evil" + hostile + "name"
	v1 := map[string]string{
		"SKILL.md":                "---\nname: " + yamlQuote(t, evilName) + "\ndescription: " + yamlQuote(t, "Primera línea\nSegunda "+hostile+" línea") + "\n---\nbody v1\n",
		"notas" + hostile + ".md": "v1\n",
	}
	v2 := map[string]string{
		"SKILL.md": "---\nname: " + yamlQuote(t, evilName) + "\ndescription: v2\n---\nbody v2\n",
	}
	f.installNamedLock(evilName, v1)
	wantSHA := f.publish(evilName, v2)

	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	// Also put an unchecked skill and a blocked hostile candidate in state so ApplyAll shows both included & excluded.
	f.home.WriteFile(".claude/skills/manual"+hostile+"/SKILL.md", "---\nname: manual"+hostile+"\ndescription: manual\n---\n")

	m := openSized(t, a, 100, 40)

	// 1. Skills screen: list + panel + multiline description (Safe preserves \n, escapes hostile) + full detail (SafeLine).
	skillsRaw := m.View().Content
	assertNothingActs(t, skillsRaw)
	if !strings.Contains(skillsRaw, "\x1b[") {
		t.Errorf("trusted Lip Gloss ANSI styling disappeared from Skills view")
	}
	skillsOut := screen(m)
	if !strings.Contains(skillsOut, "evil"+hostileShown+"name") {
		t.Errorf("Skills screen lacks escaped name:\n%s", skillsOut)
	}
	if !strings.Contains(skillsOut, "Primera línea") || !strings.Contains(skillsOut, "Segunda "+hostileShown+" línea") {
		t.Errorf("Skills panel did not preserve multiline description with escaped hostile bytes:\n%s", skillsOut)
	}

	// Open full skill detail view (enter).
	pressRun(m, "enter")
	detailRaw := m.View().Content
	assertNothingActs(t, detailRaw)
	if !strings.Contains(screen(m), "Skill: evil"+hostileShown+"name") {
		t.Errorf("Skill detail lacks escaped skill name:\n%s", screen(m))
	}
	pressRun(m, "esc")

	// 2. Updates screen: list, panel (with hostile file path), and unchecked list.
	pressRun(m, "2")
	updatesRaw := m.View().Content
	assertNothingActs(t, updatesRaw)
	updatesOut := screen(m)
	for _, want := range []string{
		"evil" + hostileShown + "name",
		"notas" + hostileShown + ".md",
		"manual" + hostileShown,
	} {
		if !strings.Contains(updatesOut, want) {
			t.Errorf("Updates screen lacks %q:\n%s", want, updatesOut)
		}
	}

	// 3. Apply-all modal (t) summary + technical details (v), then cancel (n).
	pressRun(m, "t")
	assertNothingActs(t, m.View().Content)
	if !strings.Contains(screen(m), "evil"+hostileShown+"name") {
		t.Errorf("ApplyAll summary lacks escaped target:\n%s", screen(m))
	}
	pressRun(m, "v")
	assertNothingActs(t, m.View().Content)
	if !strings.Contains(screen(m), "lock:evil"+hostileShown+"name") {
		t.Errorf("ApplyAll details lacks escaped target:\n%s", screen(m))
	}
	pressRun(m, "n")

	// 4. Single apply confirmation (a) summary + technical details (v), then confirm (s) -> exact target applies!
	pressRun(m, "a")
	assertNothingActs(t, m.View().Content)
	if !strings.Contains(screen(m), "¿Actualizar evil"+hostileShown+"name?") {
		t.Errorf("ConfirmApply summary lacks escaped question:\n%s", screen(m))
	}
	pressRun(m, "v")
	assertNothingActs(t, m.View().Content)
	if !strings.Contains(screen(m), "Destino:   lock:evil"+hostileShown+"name") {
		t.Errorf("ConfirmApply details lacks escaped Destino:\n%s", screen(m))
	}
	pressRun(m, "s")
	assertNothingActs(t, m.View().Content)
	if got := hashOf(t, filepath.Join(f.home.Paths().StoreDir(), evilName)); got != wantSHA {
		t.Fatalf("ConfirmApply did not apply exact target: got SHA %s, want %s", got, wantSHA)
	}

	// 5. z undo confirmation (TUIUndoUpdateAsk) escapes hostile target, then cancel.
	pressRun(m, "z")
	assertNothingActs(t, m.View().Content)
	if !strings.Contains(screen(m), "¿Deshacer la actualización de evil"+hostileShown+"name?") {
		t.Errorf("ConfirmUndo via z lacks escaped question:\n%s", screen(m))
	}
	pressRun(m, "n")

	// 6. Modify installed folder, open Historial (h), inspect panel, press u -> ConfirmUndo (changed folder) + details (v) -> confirm (s) restores v1!
	if err := os.WriteFile(filepath.Join(f.home.Paths().StoreDir(), evilName, "SKILL.md"), []byte("---\nname: "+evilName+"\n---\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pressRun(m, "h")
	assertNothingActs(t, m.View().Content)
	histOut := screen(m)
	if !strings.Contains(histOut, "Operación: lock:evil"+hostileShown+"name") {
		t.Errorf("History panel lacks escaped Operación:\n%s", histOut)
	}

	pressRun(m, "u")
	assertNothingActs(t, m.View().Content)
	pressRun(m, "v")
	assertNothingActs(t, m.View().Content)
	if !strings.Contains(screen(m), "Destino: lock:evil"+hostileShown+"name") {
		t.Errorf("ConfirmUndo details lacks escaped Destino:\n%s", screen(m))
	}
	pressRun(m, "s")
	assertNothingActs(t, m.View().Content)
	if got := hashOf(t, filepath.Join(f.home.Paths().StoreDir(), evilName)); got == wantSHA {
		t.Fatalf("ConfirmUndo did not restore previous version of exact hostile target")
	}
}

func TestTUIDoctorAndChooseEscapeThirdPartyText(t *testing.T) {
	mc := newMachine(t)
	h := hostile
	// Add an unmanaged skill with hostile directory name + a broken symlink pointing to a hostile name.
	mc.h.WriteFile(".claude/skills/unmgd"+h+"/SKILL.md", "---\nname: unmgd"+h+"\ndescription: d\n---\n")
	if err := os.Symlink("../../.agents/skills/ghost"+h, mc.h.Path(".claude/skills/broken"+h)); err != nil {
		t.Fatal(err)
	}

	m := openSized(t, mc.newApp(), 100, 40)
	pressRun(m, "4")
	assertNothingActs(t, m.View().Content)
	docOut := screen(m)
	if !strings.Contains(docOut, "broken"+hostileShown) && !strings.Contains(docOut, "unmgd"+hostileShown) {
		t.Errorf("Salud screen lacks escaped hostile finding:\n%s", docOut)
	}

	// Navigate to a hostile finding and open renderChoose (enter).
	found := false
	for range 40 {
		out := screen(m)
		assertNothingActs(t, m.View().Content)
		if strings.Contains(out, "Hallazgo: ") && strings.Contains(out, hostileShown) {
			found = true
			break
		}
		pressRun(m, "down")
	}
	if !found {
		t.Fatalf("could not select hostile finding in Salud:\n%s", screen(m))
	}

	pressRun(m, "enter")
	assertNothingActs(t, m.View().Content)
	chooseOut := screen(m)
	if !strings.Contains(chooseOut, hostileShown) {
		t.Errorf("renderChoose lacks escaped hostile name/path:\n%s", chooseOut)
	}
}

func TestTUIInterruptedRecoverScreenEscapesThirdPartyText(t *testing.T) {
	f := newFixture(t)
	st := statefile.New(f.home.Paths())
	if err := st.RecordOperation(domain.Operation{
		ID:                "op-1",
		Kind:              domain.OperationApply,
		Target:            "lock:evil" + hostile + "name",
		RestoreSnapshotID: "snap" + hostile,
		StartedAt:         t0,
	}); err != nil {
		t.Fatal(err)
	}
	m := openSized(t, f.newApp(), 100, 30)
	assertNothingActs(t, m.View().Content)
	got := screen(m)
	for _, want := range []string{
		"Destino:   lock:evil" + hostileShown + "name",
		"Snapshot:  snap" + hostileShown,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("interrupted recover screen lacks %q:\n%s", want, got)
		}
	}
}

func TestGoldenSafeOutput(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("Skills%d", width), func(t *testing.T) {
			f := newFixture(t)
			evilName := "evil" + hostile + "name"
			f.installNamedLock(evilName, map[string]string{
				"SKILL.md": "---\nname: " + yamlQuote(t, evilName) + "\ndescription: " + yamlQuote(t, "Útil"+hostile+"falsa") + "\n---\nbody\n",
			})
			m := openSized(t, f.newApp(), width, 30)
			assertNothingActs(t, m.View().Content)
			out := screen(m)
			if !strings.Contains(out, "evil"+hostileShown+"name") || !strings.Contains(out, "Útil"+hostileShown+"falsa") {
				t.Errorf("Skills golden lacks escaped text:\n%s", out)
			}
			teatest.RequireEqualOutput(t, []byte(out))
		})
		t.Run(fmt.Sprintf("ConfirmApply%d", width), func(t *testing.T) {
			f := newFixture(t)
			evilName := "evil" + hostile + "name"
			f.installNamedLock(evilName, map[string]string{
				"SKILL.md": "---\nname: " + yamlQuote(t, evilName) + "\ndescription: v1\n---\nv1\n",
			})
			f.publish(evilName, map[string]string{
				"SKILL.md": "---\nname: " + yamlQuote(t, evilName) + "\ndescription: v2\n---\nv2\n",
			})
			a := f.newApp()
			if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
				t.Fatal(err)
			}
			m := openSized(t, a, width, 30)
			pressRun(m, "2")
			pressRun(m, "a")
			pressRun(m, "v")
			assertNothingActs(t, m.View().Content)
			out := screen(m)
			if !strings.Contains(out, "lock:evil"+hostileShown+"name") {
				t.Errorf("ConfirmApply golden lacks escaped target:\n%s", out)
			}
			teatest.RequireEqualOutput(t, []byte(out))
		})
	}
}
