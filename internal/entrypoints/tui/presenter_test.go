package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/updates"
)

func TestPresentSkillsFiltersAndFormats(t *testing.T) {
	skills := []domain.Skill{
		{
			ID:      "lock:alpha",
			Name:    "alpha",
			Aliases: []string{"a-alias"},
			Origin:  domain.Origin{Kind: domain.OriginLock},
			Locations: []domain.Location{
				{AgentID: domain.StoreAgentID, Kind: domain.LocationReal},
				{AgentID: "claude-code", Kind: domain.LocationSymlink},
			},
			State:  domain.StateUpdateAvailable,
			Issues: []domain.Issue{domain.IssueLocallyModified},
		},
		{
			ID:     "dir:.claude/skills/beta",
			Name:   "beta",
			Origin: domain.Origin{Kind: domain.OriginNone},
			Locations: []domain.Location{
				{AgentID: "claude-code", Kind: domain.LocationReal},
			},
			State: domain.StateNoOrigin,
		},
	}

	all := PresentSkills(skills, "")
	if len(all) != 2 {
		t.Fatalf("PresentSkills len = %d, want 2", len(all))
	}
	if all[0].Name != "alpha (a-alias)" {
		t.Errorf("Name = %q, want alpha (a-alias)", all[0].Name)
	}
	if all[0].Origin != "lock de skills" {
		t.Errorf("Origin = %q, want lock de skills", all[0].Origin)
	}
	if all[0].Harnesses != "claude-code" {
		t.Errorf("Harnesses = %q, want claude-code", all[0].Harnesses)
	}
	if all[0].State != "actualización disponible" {
		t.Errorf("State = %q, want actualización disponible", all[0].State)
	}
	if all[0].Issues != "modificada localmente" {
		t.Errorf("Issues = %q, want modificada localmente", all[0].Issues)
	}

	filtered := PresentSkills(skills, "beta")
	if len(filtered) != 1 || filtered[0].ID != "dir:.claude/skills/beta" {
		t.Errorf("filtered = %+v, want only beta", filtered)
	}
}

func TestBuildApplyAllPlanExcludesBlockedAndLocallyModified(t *testing.T) {
	cands := []domain.UpdateCandidate{
		{
			ID:          "cand-1",
			Target:      "lock:clean",
			ToCommit:    "1234567890",
			ToSHA:       "aaaa11112222",
			State:       domain.CandidatePending,
			Labels:      []domain.Label{domain.LabelTextOnly},
			FileChanges: []domain.FileChange{{Path: "SKILL.md", Change: domain.ChangeModified}},
		},
		{
			ID:          "cand-2",
			Target:      "lock:edited",
			ToCommit:    "2234567890",
			ToSHA:       "bbbb11112222",
			State:       domain.CandidatePending,
			Labels:      []domain.Label{domain.LabelLocallyModified},
			FileChanges: []domain.FileChange{{Path: "SKILL.md", Change: domain.ChangeModified}},
		},
		{
			ID:          "cand-3",
			Target:      "git:.agents/skills/clone",
			ToCommit:    "3234567890",
			ToSHA:       "cccc11112222",
			State:       domain.CandidateBlocked,
			BlockReason: domain.BlockDirtyWorktree,
		},
	}

	plan := BuildApplyAllPlan(cands)
	if len(plan.Included) != 1 || plan.Included[0].CandidateID != "cand-1" {
		t.Fatalf("Included = %+v, want only cand-1", plan.Included)
	}
	if len(plan.Excluded) != 2 {
		t.Fatalf("Excluded len = %d, want 2", len(plan.Excluded))
	}
	aps := plan.Approvals()
	if len(aps) != 1 || aps[0].CandidateID != "cand-1" || aps[0].ToSHA != "aaaa11112222" {
		t.Errorf("Approvals = %+v, want cand-1 with exact ToSHA", aps)
	}
}

func TestPresentDiffViews(t *testing.T) {
	local := domain.UpdateCandidate{
		ID:       "cand-local",
		Target:   "lock:edited",
		FromSHA:  "111111111111",
		ToCommit: "222222222222",
		ToSHA:    "333333333333",
		State:    domain.CandidatePending,
		Labels:   []domain.Label{domain.LabelLocallyModified},
	}
	clean := local
	clean.Labels = []domain.Label{domain.LabelTextOnly}
	one := func(path, line string) []app.FileDiff {
		return []app.FileDiff{{Path: path, Change: domain.ChangeModified, Unified: "--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-old\n+" + line + "\n"}}
	}
	split := app.ReviewDiff{
		LocallyModified: true, RegisteredAvailable: true,
		User: one("SKILL.md", "user line"), Author: one("SKILL.md", "author line"),
	}
	gone := app.ReviewDiff{LocallyModified: true, Installed: one("SKILL.md", "upstream line")}
	usual := app.ReviewDiff{Installed: one("SKILL.md", "upstream line")}

	const (
		userLabel   = "Tus cambios (registrada → instalada)"
		authorLabel = "Cambios del autor (registrada → upstream)"
		singleLabel = "Vista: instalada → upstream"
		replaces    = "Esta skill tiene cambios locales: aplicar la actualización\nlos reemplaza con la versión del autor."
		goneNotice  = "La versión registrada ya no está en upstream:\nsolo se muestra el diff de instalada a upstream."
	)
	cases := []struct {
		name    string
		c       domain.UpdateCandidate
		r       app.ReviewDiff
		view    DiffView
		want    []string
		notWant []string
	}{
		{
			name: "user's changes",
			c:    local, r: split, view: DiffViewUser,
			want:    []string{"Vista: " + userLabel, "v: " + authorLabel, replaces, "+user line"},
			notWant: []string{"+author line", singleLabel, goneNotice, "todavía no está disponible"},
		},
		{
			name: "author's changes",
			c:    local, r: split, view: DiffViewAuthor,
			want:    []string{"Vista: " + authorLabel, "v: " + userLabel, replaces, "+author line"},
			notWant: []string{"+user line", singleLabel, goneNotice},
		},
		{
			name: "registered version gone",
			c:    local, r: gone, view: DiffViewUser,
			want:    []string{singleLabel, goneNotice, replaces, "+upstream line"},
			notWant: []string{userLabel, authorLabel, "todavía no está disponible"},
		},
		{
			name: "unedited skill",
			c:    clean, r: usual, view: DiffViewUser,
			want:    []string{singleLabel, "+upstream line"},
			notWant: []string{userLabel, authorLabel, goneNotice, replaces},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PresentDiff(tc.c, tc.r, tc.view)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("lacks %q:\n%s", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("has %q:\n%s", nw, got)
				}
			}
		})
	}
}

func TestPresentHistoryOrdersNewestFirstAndMarksUndoable(t *testing.T) {
	t1 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	entries := []domain.HistoryEntry{
		{At: t1, Action: domain.ActionSkipped, Target: "lock:a", ToSHA: "aaaa1111"},
		{At: t2, Action: domain.ActionApplied, Target: "lock:b", FromSHA: "11112222", ToSHA: "33334444", SnapshotID: "snap-1"},
	}

	rows := PresentHistory(entries)
	if len(rows) != 2 {
		t.Fatalf("PresentHistory len = %d, want 2", len(rows))
	}
	if rows[0].Target != "lock:b" || !rows[0].CanUndo {
		t.Errorf("rows[0] = %+v, want lock:b undoable first", rows[0])
	}
	if rows[1].Target != "lock:a" || rows[1].CanUndo {
		t.Errorf("rows[1] = %+v, want lock:a not undoable second", rows[1])
	}
}

func TestPresentSettingsIncludesBackgroundCommand(t *testing.T) {
	s := domain.Settings{
		CheckInterval: 2 * time.Hour,
		Notifications: true,
		Ignore:        []string{"foo"},
	}
	bg := ports.BackgroundStatus{Enabled: false, Installed: false, Active: false}

	view := PresentSettings(s, bg)
	if view.CheckInterval != "2h0m0s" {
		t.Errorf("CheckInterval = %q, want 2h0m0s", view.CheckInterval)
	}
	if view.Notifications != "sí" {
		t.Errorf("Notifications = %q, want sí", view.Notifications)
	}
	if view.BackgroundState != "desactivada" {
		t.Errorf("BackgroundState = %q, want desactivada", view.BackgroundState)
	}
	if !strings.Contains(view.BackgroundCommand, "skillhub background enable") {
		t.Errorf("BackgroundCommand = %q, want skillhub background enable", view.BackgroundCommand)
	}
}

func TestExplainErrorTranslatesAllKnownAndHidesRawEnglish(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantSub   string
		forbidRaw string
	}{
		{
			name:    "state locked",
			err:     ports.ErrStateLocked,
			wantSub: "Otro proceso de skillhub",
		},
		{
			name:    "interrupted",
			err:     updates.ErrInterrupted,
			wantSub: "operación interrumpida",
		},
		{
			name:    "version 2 operation pending",
			err:     fmt.Errorf("check: %w", updates.ErrV2OperationPending),
			wantSub: "Hay una operación de gestión de skills sin terminar; ejecuta «skillhub recover».",
		},
		{
			name:    "installed changed",
			err:     updates.ErrInstalledChanged,
			wantSub: "la carpeta instalada cambió",
		},
		{
			name:    "staged changed",
			err:     updates.ErrStagedChanged,
			wantSub: "la copia preparada cambió",
		},
		{
			name:    "candidate replaced",
			err:     updates.ErrNotPending,
			wantSub: "el candidato cambió durante la revisión",
		},
		{
			name:    "nothing to undo",
			err:     updates.ErrNothingToUndo,
			wantSub: "No hay ninguna aplicación",
		},
		{
			name:    "snapshot not found",
			err:     updates.ErrSnapshotNotFound,
			wantSub: "No existe ese snapshot",
		},
		{
			name:    "ambiguous",
			err:     &app.AmbiguousError{Ref: "foo", IDs: []string{"lock:foo", "dir:foo"}},
			wantSub: "corresponde a varias skills",
		},
		{
			name:      "generic unknown error hides english",
			err:       errors.New("raw english technical failure"),
			wantSub:   "Ocurrió un error",
			forbidRaw: "raw english technical failure",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainError(tc.err)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("ExplainError(%v) = %q, want substring %q", tc.err, got, tc.wantSub)
			}
			if tc.forbidRaw != "" && strings.Contains(got, tc.forbidRaw) {
				t.Errorf("ExplainError leaked raw error %q in %q", tc.forbidRaw, got)
			}
		})
	}
}
