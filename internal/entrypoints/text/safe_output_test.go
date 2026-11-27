package text_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

func TestUnknownDomainCodesEscapeThirdPartyText(t *testing.T) {
	h := hostile
	cases := map[string]string{
		"State":          text.State(domain.State("custom" + h)),
		"Issue":          text.Issue(domain.Issue("custom" + h)),
		"Label":          text.Label(domain.Label("custom" + h)),
		"CandidateState": text.CandidateState(domain.CandidateState("custom" + h)),
		"OriginKind":     text.OriginKind(domain.OriginKind("custom" + h)),
		"LocationKind":   text.LocationKind(domain.LocationKind("custom" + h)),
		"OperationKind":  text.OperationKind(domain.OperationKind("custom" + h)),
		"HistoryAction":  text.HistoryAction(domain.HistoryAction("custom" + h)),
		"SnapshotCause":  text.SnapshotCause(domain.SnapshotCause("custom" + h)),
		"BlockReason":    text.BlockReason(domain.BlockReason("custom" + h)),
		"FileChangeKind": text.FileChangeKind(domain.FileChangeKind("custom" + h)),
		"FindingKind":    text.FindingKind("custom" + h),
		"RepairKind":     text.RepairKind("custom" + h),
		"TUIRowIssue":    text.TUIRowIssue(domain.Issue("custom" + h)),
		"TUILabelPlain":  text.TUILabelPlain(domain.Label("custom" + h)),
		"TUIRepairPlain": text.TUIRepairPlain("custom" + h),
		"TUIHealthRow":   text.TUIHealthRow("custom" + h),
		"TUIHealthOne":   text.TUIHealthFindings("custom"+h, 1),
		"TUIHealthMany":  text.TUIHealthFindings("custom"+h, 2),
		"TUIHealthNoSk":  text.TUIHealthNoSkill("custom" + h),
	}
	for name, got := range cases {
		assertEscaped(t, name, got)
	}
	// Regression: known codes stay unchanged in neutral Spanish.
	if got := text.State(domain.StateUpToDate); got != "al día" {
		t.Errorf("State(up_to_date) = %q, want al día", got)
	}
}

func TestRecoverTextsEscapeThirdPartyText(t *testing.T) {
	h := hostile
	op := domain.Operation{
		ID:                "op-1",
		Target:            "lock:evil" + h + "name",
		RestoreSnapshotID: "snap-" + h,
	}
	cases := map[string]string{
		"RecoveryV1 restored": text.RecoveryV1(updates.RecoverResult{
			Operation: op,
			Restored:  true,
			Detail:    "nota" + h + "\nforjada",
		}, false),
		"RecoveryV1 empty": text.RecoveryV1(updates.RecoverResult{
			Operation: op,
			Restored:  false,
		}, false),
		"RecoverNote unknown": text.RecoverNote("detail" + h + "\tcol"),
		"Recovered v1": text.Recovered(app.Recovery{
			Version: 1,
			V1:      updates.RecoverResult{Operation: op, Restored: true},
		}),
	}
	for name, got := range cases {
		assertEscaped(t, name, got)
	}
	if strings.Contains(cases["RecoveryV1 restored"], "\nforjada") {
		t.Errorf("RecoverNote allowed a newline to split lines: %q", cases["RecoveryV1 restored"])
	}
	// Regression: known Spanish lock note stays unchanged.
	if got := text.RecoverNote("lock entry removed by another tool; not recreated"); got != "otra herramienta quitó la entrada del lock; no se volvió a crear" {
		t.Errorf("RecoverNote known = %q", got)
	}
}

func TestCatalogTextsEscapeThirdPartyText(t *testing.T) {
	h := hostile
	entries := []app.CatalogEntry{
		{
			ID:                "acme/skills/evil",
			Source:            "acme/skills" + h,
			SkillID:           "evil" + h,
			Name:              "evil\t" + h + "\nfake",
			Installs:          42,
			Installable:       true,
			InstallSource:     "acme/skills" + h + "@evil" + h,
			Installed:         false,
			PossibleDuplicate: true,
			DuplicateOf:       "dup" + h,
			DuplicateSource:   "other/repo" + h,
		},
		{
			ID:              "acme/skills/inst",
			Source:          "acme/skills",
			SkillID:         "inst",
			Name:            "inst",
			Installs:        10,
			Installable:     true,
			InstallSource:   "acme/skills@inst",
			Installed:       true,
			InstalledSource: "acme/inst" + h,
		},
		{
			ID:                "acme/skills/dup2",
			Source:            "acme/skills",
			SkillID:           "dup2",
			Name:              "dup2",
			Installs:          5,
			PossibleDuplicate: true,
			DuplicateOf:       "dup-no-origin" + h,
		},
	}

	table := text.CatalogTable(entries)
	assertEscaped(t, "CatalogTable", table)
	if strings.Contains(table, "\nfake") {
		t.Errorf("CatalogTable allowed newline injection:\n%q", table)
	}
	hint := text.CatalogInstallHint(entries)
	if hint != text.CatalogInstallHint(nil) {
		t.Errorf("CatalogInstallHint(entries) = %q, want generic hint %q", hint, text.CatalogInstallHint(nil))
	}
	for _, e := range entries {
		if e.InstallSource != "" && strings.Contains(hint, e.InstallSource) {
			t.Errorf("CatalogInstallHint embedded InstallSource %q: %q", e.InstallSource, hint)
		}
	}

	errMsg, ok := text.CatalogError(&app.CatalogError{Kind: app.ErrCatalogFailed, Err: errors.New("boom" + h)})
	if !ok {
		t.Fatal("CatalogError returned ok=false")
	}
	assertEscaped(t, "CatalogError", errMsg)

	stale := text.CatalogStale(errors.New("stale"+h), time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC))
	assertEscaped(t, "CatalogStale", stale)

	for name, got := range map[string]string{
		"query":             text.TUICatalogHeading("find"+h, 2),
		"name":              text.TUICatalogName(entries[0]),
		"fallback name":     text.TUICatalogName(app.CatalogEntry{SkillID: "evil" + h}),
		"result details":    strings.Join(text.TUICatalogDetails(entries[0]), " "),
		"installed details": strings.Join(text.TUICatalogDetails(entries[1]), " "),
		"installed refusal": text.TUICatalogAlreadyInstalled(entries[1]),
		"error":             text.TUICatalogError(errors.New("read" + h)),
	} {
		assertEscaped(t, name, got)
	}
}

func TestTUIUpdatesUncheckedAndActivityEscapeThirdPartyText(t *testing.T) {
	h := hostile
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	items := []app.UncheckedSkill{
		{ID: "lock:a", Name: "web" + h, Reason: app.UncheckedWebSource},
		{ID: "git:b", Name: "git" + h, Reason: app.UncheckedUntrackedOrigin},
		{ID: "dir:c", Name: "dir" + h, Reason: app.UncheckedNoOrigin},
		{ID: "lock:d", Name: "fail1" + h, Reason: app.UncheckedCheckFailed, CheckedAt: now.Add(-time.Hour)},
		{ID: "lock:e", Name: "fail2" + h, Reason: app.UncheckedCheckFailed},
		{ID: "lock:f", Name: "other" + h, Reason: app.UncheckedReason("unknown")},
	}
	lines := text.TUIUpdatesUnchecked(items, now)
	for i, l := range lines[1:] {
		assertEscaped(t, "TUIUpdatesUnchecked line", l)
		if strings.ContainsAny(l, "\n\t") {
			t.Errorf("unchecked line %d has raw newline/tab: %q", i, l)
		}
	}

	actCases := map[string]app.ActivityEntry{
		"v1 unknown action": {
			At:     now.Add(-time.Minute),
			Action: domain.HistoryAction("weird" + h),
		},
		"v2 failed unknown op": {
			At:        now.Add(-time.Minute),
			Action:    domain.ActionFailed,
			V2:        true,
			Operation: app.OperationView{Action: "custom" + h},
		},
		"v2 undo unknown op": {
			At:        now.Add(-time.Minute),
			Action:    domain.ActionUndone,
			V2:        true,
			Operation: app.OperationView{Action: "custom" + h, Undo: true},
		},
	}
	for name, entry := range actCases {
		assertEscaped(t, "TUIActivityLine "+name, text.TUIActivityLine(entry, now))
	}
}

func TestTUIManageAndRefusalEscapeThirdPartyText(t *testing.T) {
	h := hostile
	refusals := []struct {
		name    string
		kind    error
		req     app.Request
		related []string
	}{
		{"unknown harness", app.ErrUnknownHarness, app.Request{Harness: "codex" + h}, nil},
		{"neighbour parked empty", app.ErrNeighbourParked, app.Request{Key: "k" + h}, nil},
		{"neighbour parked related", app.ErrNeighbourParked, app.Request{Key: "k" + h}, []string{"vecina" + h}},
		{"no entry to reinstall", app.ErrNoEntryToReinstall, app.Request{Install: &app.InstallRequest{Revision: app.Revision{Key: "k" + h}}}, nil},
		{"not found unknown action", app.ErrNotFound, app.Request{Action: app.Action("act" + h)}, nil},
		{"fallback to Refusal", app.ErrAlreadyDisabled, app.Request{Key: "k" + h}, nil},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := text.TUIRefusal(&app.RefusalError{Kind: tc.kind, Related: tc.related}, tc.req)
			if !ok {
				t.Fatalf("TUIRefusal(%v) returned ok=false", tc.kind)
			}
			assertEscaped(t, "TUIRefusal "+tc.name, msg)
		})
	}

	other := map[string]string{
		"TUIPlanID":       text.TUIPlanID("plan" + h),
		"TUIParkedSince":  text.TUIParkedSince("2026" + h),
		"TUIHealthAlsoIn": text.TUIHealthAlsoIn([]string{"a" + h, "b" + h}),
		"TUIHealthAt":     text.TUIHealthAt("skill"+h, "~/dir/"+h),
	}
	for name, got := range other {
		assertEscaped(t, name, got)
	}
}

func TestTUIPlainTextsEscapeThirdPartyText(t *testing.T) {
	h := hostile
	namer := func(id string) string { return id }

	plainCases := map[string]string{
		"JoinNames one":         text.JoinNames([]string{"a" + h}),
		"JoinNames many":        text.JoinNames([]string{"a" + h, "b" + h, "c" + h}),
		"TUIPanelAvailable":     text.TUIPanelAvailable("Claude" + h),
		"TUIPanelOrigin":        text.TUIPanelOrigin("acme/repo" + h),
		"TUIPanelReturnsTo":     text.TUIPanelReturnsTo("Codex" + h),
		"TUIPanelDisabledSince": text.TUIPanelDisabledSince("2026" + h),
		"TUIPanelOffHand one":   text.TUIPanelOffHand([]string{"Codex" + h}),
		"TUIPanelOffHand many":  text.TUIPanelOffHand([]string{"Codex" + h, "Pi" + h}),
		"TUIPanelAdoptOff one":  text.TUIPanelAdoptOff([]string{"self" + h, "other" + h}, "self"+h),
		"TUIPanelAdoptOff many": text.TUIPanelAdoptOff([]string{"self" + h, "o1" + h, "o2" + h}, "self"+h),
		"TUIUpdated":            text.TUIUpdated("alpha" + h),
		"TUIUpdateUndone":       text.TUIUpdateUndone("alpha" + h),
		"TUIUpdateVersions":     text.TUIUpdateVersions("111"+h, "222"+h),
		"TUIUpdateBlocked":      text.TUIUpdateBlocked("reason" + h),
		"TUIUpdateDid":          text.TUIUpdateDid(domain.ActionApplied, "alpha"+h),
		"TUIUpdateDid unknown":  text.TUIUpdateDid(domain.HistoryAction("x"+h), "alpha"+h),
		"TUIOperationDid":       text.TUIOperationDid(domain.ActionApplied, app.OperationView{Action: string(app.ActionDisable), Keys: []string{"k" + h}}),
		"TUIOperationDid undo":  text.TUIOperationDid(domain.ActionUndone, app.OperationView{Action: "custom" + h, Keys: []string{"k" + h}, Undo: true}),
		"TUIHistoryLine":        text.TUIHistoryLine("did"+h, "ago"+h, true),
		"TUIDone disable":       text.TUIDone(app.Result{Proposal: app.Proposal{Request: app.Request{Action: app.ActionDisable, Key: "k" + h}, Reload: []string{"codex" + h}}}, namer),
		"TUIDone unlink":        text.TUIDone(app.Result{Proposal: app.Proposal{Request: app.Request{Action: app.ActionUnlink, Key: "k" + h, Harness: "codex" + h}}}, namer),
		"TUIDone install":       text.TUIDone(app.Result{Proposal: app.Proposal{Request: app.Request{Action: app.ActionInstall}, Install: &app.InstallPreview{Key: "k" + h}}}, namer),
	}
	for name, got := range plainCases {
		assertEscaped(t, name, got)
	}

	// TUIPanelAdoptOff must still exclude self when self itself contains hostile chars.
	if got := text.TUIPanelAdoptOff([]string{"self" + h}, "self"+h); strings.Contains(got, "self") {
		t.Errorf("TUIPanelAdoptOff did not exclude hostile self: %q", got)
	}

	summaries := map[string]text.Summary{
		"TUIInstallSummary github": text.TUIInstallSummary("k"+h, "acme/repo"+h, ""),
		"TUIInstallSummary web":    text.TUIInstallSummary("k"+h, "site"+h, "https://site"+h+"/s"),
		"TUIUpdateSummary":         text.TUIUpdateSummary("k"+h, 2, true),
		"TUIUndoUpdateSummary":     text.TUIUndoUpdateSummary("k"+h, "snap"+h),
		"TUIApplyAllSummary": text.TUIApplyAllSummary(
			[]string{"inc" + h},
			[]text.TUIExcluded{{Name: "exc" + h, Reason: "why" + h}},
		),
		"TUIPlanSummary disable": text.TUIPlanSummary(app.Proposal{
			Request: app.Request{Action: app.ActionDisable, Key: "k" + h},
			Reload:  []string{"codex" + h},
		}, namer),
		"TUIPlanSummary unlink": text.TUIPlanSummary(app.Proposal{
			Request: app.Request{Action: app.ActionUnlink, Key: "k" + h, Harness: "codex" + h},
			Reload:  []string{"codex" + h},
		}, namer),
		"TUIPlanSummary install": text.TUIPlanSummary(app.Proposal{
			Request: app.Request{Action: app.ActionInstall},
			Install: &app.InstallPreview{Key: "k" + h, Source: "acme/repo" + h},
		}, namer),
		"TUIPlanSummary repair": text.TUIPlanSummary(app.Proposal{
			Request: app.Request{Action: app.ActionRepair, Repair: "rep" + h},
			Repair:  &app.RepairPreview{Key: "k" + h},
		}, namer),
		"TUIPlanSummary adopt-off one": text.TUIPlanSummary(app.Proposal{
			Request: app.Request{Action: app.ActionAdoptOff},
			Toggle:  &app.TogglePreview{Skills: []app.SkillChange{{Key: "k" + h}}},
		}, namer),
		"TUIPlanSummary adopt-off many": text.TUIPlanSummary(app.Proposal{
			Request: app.Request{Action: app.ActionAdoptOff},
			Toggle:  &app.TogglePreview{Skills: []app.SkillChange{{Key: "k1" + h}, {Key: "k2" + h}}},
		}, namer),
		"TUIPlanSummary unknown": text.TUIPlanSummary(app.Proposal{
			Request: app.Request{Action: app.Action("weird" + h)},
		}, namer),
	}
	for name, s := range summaries {
		joined := s.Question + "\n" + strings.Join(s.Lines, "\n")
		assertEscaped(t, name, joined)
	}
}
