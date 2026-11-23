package text_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

// Every refusal kind of the app has its own Spanish sentence.
func TestEveryRefusalKindHasItsMessage(t *testing.T) {
	kinds := []error{
		app.ErrPlanChanged, app.ErrNoPlan, app.ErrInvalidRequest, app.ErrInvalidKey, app.ErrNotFound,
		app.ErrForeignZone, app.ErrAmbiguous, app.ErrAlias, app.ErrChanged, app.ErrAlreadyDisabled,
		app.ErrNotDisabled, app.ErrKeyReinstalled, app.ErrKeyInUse, app.ErrKeyExists, app.ErrUnknownHarness,
		app.ErrNotAdministrable, app.ErrHarnessNotDetected, app.ErrNotLinked, app.ErrAlreadyLinked,
		app.ErrPathTaken, app.ErrParkingVisible, app.ErrNothingToAdopt, app.ErrNeighbourParked,
		app.ErrBadParkedRecord, app.ErrNameMismatch, app.ErrPinnedRef, app.ErrMissingDir, app.ErrInvalidReview,
		app.ErrStagingChanged, app.ErrUnsupportedSource, app.ErrNoSkill, app.ErrAmbiguousSkill, app.ErrStale,
		app.ErrNotOffered, app.ErrNeedsCanonical, app.ErrBadCanonical, app.ErrLockedSkill, app.ErrLinkedCopy,
		app.ErrNoPlacement, app.ErrPrecondition, app.ErrForeignState, app.ErrOperationPending,
		app.ErrChangedSince, app.ErrNothingToUndo, app.ErrUnsupportedTree, app.ErrLockChanged,
		app.ErrNoEntryToReinstall, app.ErrEntryPinsRef, app.ErrSourceMismatch, app.ErrLinkedLinkDir,
		app.ErrCanonicalNotUsed, app.ErrRepoNotFound, app.ErrRefNotFound, app.ErrRateLimited,
		app.ErrNotAuthenticated, app.ErrGitHubUnavailable, app.ErrLockUnsupported, app.ErrLockMalformed, app.ErrStagingFailed,
	}
	seen := map[string]error{}
	req := app.Request{Action: app.ActionDisable, Key: "alpha", Harness: "codex"}
	for _, kind := range kinds {
		msg, ok := text.Refusal(&app.RefusalError{Kind: kind, Err: errors.New("cause")}, req)
		if !ok || msg == "" {
			t.Errorf("%v has no message", kind)
			continue
		}
		if prev, dup := seen[msg]; dup {
			t.Errorf("%v and %v share the message %q", kind, prev, msg)
		}
		seen[msg] = kind
		// The TUI shows these too, and it never says harness (UX2).
		if strings.Contains(strings.ToLower(msg), "harness") {
			t.Errorf("%v says harness: %q", kind, msg)
		}
	}
	for _, r := range []app.Request{{}, {Harness: "codex"}} {
		msg, _ := text.Refusal(&app.RefusalError{Kind: app.ErrUnknownHarness}, r)
		if strings.Contains(strings.ToLower(msg), "harness") || !strings.Contains(msg, "agente") {
			t.Errorf("the unknown harness refusal of %+v = %q, want agente", r, msg)
		}
	}
	if _, ok := text.Refusal(errors.New("disk full"), req); ok {
		t.Error("an error that is not a refusal got a refusal message")
	}
}

// A refusal names what the user can act on.
func TestRefusalsNameWhatToDo(t *testing.T) {
	tests := []struct {
		name    string
		kind    error
		req     app.Request
		related []string
		want    []string
	}{
		{"neighbours to enable first", app.ErrNeighbourParked, app.Request{Key: "alpha"}, []string{"beta"},
			[]string{"«alpha»", "beta", "«skillhub skills enable beta»"}},
		{"the folder that holds the key", app.ErrKeyExists,
			app.Request{Install: &app.InstallRequest{Revision: app.Revision{Key: "tidy"}}}, []string{"/h/.agents/skills/tidy"},
			[]string{"«tidy»", "/h/.agents/skills/tidy"}},
		{"the unknown harness", app.ErrUnknownHarness, app.Request{Key: "alpha", Harness: "nope"}, nil,
			[]string{"«nope» no es un agente conocido"}},
		{"the skill not found", app.ErrNotFound, app.Request{Key: "nope"}, nil,
			[]string{"no hay ninguna skill administrable «nope»"}},
		{"the repository that is not there", app.ErrRepoNotFound,
			app.Request{Action: app.ActionInstall, Install: &app.InstallRequest{Revision: app.Revision{Input: "acme/none"}}}, nil,
			[]string{"acme/none"}},
		{"the path an install needs", app.ErrPathTaken,
			app.Request{Action: app.ActionInstall, Install: &app.InstallRequest{Revision: app.Revision{Key: "tidy"}}}, nil,
			[]string{"«tidy»"}},
		{"the key of a reinstall without an entry", app.ErrNoEntryToReinstall,
			app.Request{Action: app.ActionReinstall, Install: &app.InstallRequest{Revision: app.Revision{Key: "tidy"}}}, nil,
			[]string{"«tidy»", "skillhub skills install"}},
		{"the key of a pinned entry", app.ErrEntryPinsRef,
			app.Request{Action: app.ActionReinstall, Install: &app.InstallRequest{Revision: app.Revision{Key: "grill-me"}}}, nil,
			[]string{"«grill-me»", "ref"}},
		{"the source of a reinstall", app.ErrSourceMismatch,
			app.Request{Action: app.ActionReinstall, Install: &app.InstallRequest{Revision: app.Revision{Key: "grill-me", Repo: "other/grill-me"}}}, nil,
			[]string{"«grill-me»", "other/grill-me"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := text.Refusal(&app.RefusalError{Kind: tt.kind, Err: errors.New("cause"), Related: tt.related}, tt.req)
			if !ok {
				t.Fatal("no message")
			}
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("%q lacks %q", msg, w)
				}
			}
		})
	}
}

// Every step kind, finding kind, repair and reason has a Spanish name.
func TestEveryCodeHasASpanishName(t *testing.T) {
	for _, k := range []string{"move", "link", "unlink", "lockPark", "lockUnpark", "lockAdd", "lockRemove",
		"lockRemoveLast", "lockReplace", "removeTree", "restoreTree", "require"} {
		if got := text.Step(app.Step{Kind: k, Path: "/p", Name: "n"}); strings.HasPrefix(got, k+" ") {
			t.Errorf("step %s has no Spanish text: %q", k, got)
		}
	}
	for _, k := range []string{"broken_link", "link_cycle", "link_outside_roots", "identical_copies", "no_canonical",
		"unmanaged_skill", "lock_entry_without_folder", "lock_name_mismatch", "parked_but_visible", "homonyms",
		"managed_by_gentle_ai"} {
		if text.FindingKind(k) == k {
			t.Errorf("finding %s has no Spanish name", k)
		}
	}
	for _, k := range []string{"delete_link", "repoint_link", "enable_and_link", "adopt_off", "adopt", "park_entry", "reinstall"} {
		if text.RepairKind(k) == k {
			t.Errorf("repair %s has no Spanish name", k)
		}
	}
	for _, r := range []string{"managed_by_gentle_ai", "homonyms", "lock_name_mismatch", "link_cycle",
		"link_outside_roots", "diagnostic_profile", "foreign_zone", "canonical_not_in_store", "ambiguous", "already_parked"} {
		if text.NoRepair(app.Offer{Reason: r}) == r {
			t.Errorf("reason %s has no Spanish text", r)
		}
	}
}

// The preview shows what stays visible, the links left broken, the
// homonyms, the candidates superseded, the harnesses open and the ones to
// reload.
func TestTheProposalShowsEveryWarning(t *testing.T) {
	p := app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "alpha"},
		PlanID:  "id",
		Remove: &app.RemovePreview{
			Key: "alpha", Folder: "/h/.agents/skills/alpha", TreeSHA: "abc",
			Homonyms:          []app.Homonym{{Path: "/h/.codex/skills/alpha", Harnesses: []string{"codex"}}},
			KeptLinks:         []app.KeptLink{{Path: "/h/.claude-omni/skills/alpha", Target: "../x", Profile: "claude-omni"}},
			PendingCandidates: []string{"cand-1"},
		},
		Running: []app.Process{{PID: 42, Harness: "claude-code", Command: "claude"}},
		Reload:  []string{"claude-code", "codex"},
	}
	got := text.Proposal(p)
	for _, want := range []string{"Eliminar alpha", "/h/.codex/skills/alpha", "/h/.claude-omni/skills/alpha → ../x (perfil claude-omni)",
		"cand-1", "Agentes abiertos ahora", "claude-code (proceso 42, claude)", "Después, recarga las skills en: claude-code, codex"} {
		if !strings.Contains(got, want) {
			t.Errorf("the preview lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "harness") {
		t.Errorf("the preview says harness:\n%s", got)
	}
	toggle := text.Proposal(app.Proposal{
		Request: app.Request{Action: app.ActionUnlink, Key: "alpha", Harness: "claude-code"}, PlanID: "id",
		Toggle: &app.TogglePreview{StillVisible: []app.Sighting{{Harness: "codex", Kind: "direct", Entry: "/h/.agents/skills/alpha"}}},
	})
	if !strings.Contains(toggle, "codex: lo lee directamente (/h/.agents/skills/alpha)") {
		t.Errorf("the toggle preview lacks the harness that still sees the skill:\n%s", toggle)
	}
}

// Done tells how to undo, the candidates superseded and what to reload;
// Recovery tells what recover did.
func TestResultsAndRecoveries(t *testing.T) {
	done := text.Done(app.Result{
		Proposal:   app.Proposal{Request: app.Request{Action: app.ActionRemove}, Operation: "v2:remove", Reload: []string{"codex"}},
		SnapshotID: "snap-1", Superseded: []string{"cand-1"},
	})
	for _, want := range []string{"skillhub undo v2:remove --to snap-1", "cand-1", "codex"} {
		if !strings.Contains(done, want) {
			t.Errorf("Done lacks %q:\n%s", want, done)
		}
	}
	// An undo cannot be undone, so its result offers no undo command.
	if got := text.Done(app.Result{Proposal: app.Proposal{Request: app.Request{Action: app.ActionUndo}, Operation: "v2:undo"}, SnapshotID: "s"}); strings.Contains(got, "Para deshacerlo") {
		t.Errorf("the result of an undo offers to undo it: %q", got)
	}
	// A repair is journaled with its concrete action, and undo takes that.
	repair := text.Done(app.Result{Proposal: app.Proposal{Request: app.Request{Action: app.ActionRepair}, Operation: "v2:repair-adopt"}, SnapshotID: "s"})
	if !strings.Contains(repair, "skillhub undo v2:repair-adopt --to s") {
		t.Errorf("the undo hint of a repair does not name its journal target: %q", repair)
	}
	tests := []struct {
		r    app.ManagedRecovery
		want string
	}{
		{app.ManagedRecovery{}, "No hay ninguna operación interrumpida."},
		{app.ManagedRecovery{Found: true, RolledBack: true, PlanID: "p1"}, "Se revirtió la operación de gestión interrumpida (plan p1)"},
		{app.ManagedRecovery{Found: true, Committed: true, SnapshotID: "s1"}, "ya se había completado"},
	}
	for _, tt := range tests {
		if got := text.Recovery(tt.r); !strings.Contains(got, tt.want) {
			t.Errorf("Recovery(%+v) = %q, want %q", tt.r, got, tt.want)
		}
	}
}

// The install review shows the files, the labels, the diff against nothing
// and why new_permissions is always there (L39).
func TestTheInstallReview(t *testing.T) {
	rev := app.Revision{Input: "acme/tools", Commit: "c0ffee"}
	rev.Prepared.Resolved.Key = "tidy"
	rev.Prepared.TreeSHA = "abc"
	rev.Prepared.Labels = []domain.Label{domain.LabelNewPermissions, domain.LabelTouchesScripts}
	rev.Prepared.FileChanges = []domain.FileChange{
		{Path: "SKILL.md", Change: domain.ChangeAdded, NewMode: "100644"},
		{Path: "logo.png", Change: domain.ChangeAdded, NewMode: "100644"},
	}
	got := text.InstallReview(rev, []app.FileDiff{
		{Path: "SKILL.md", Unified: "@@ -0,0 +1 @@\n+hello\n"},
		{Path: "logo.png", Binary: true},
	})
	for _, want := range []string{"tidy", "permisos nuevos, toca scripts", text.NewPermissions, "SKILL.md", "+hello",
		"logo.png\n(archivo binario: sin diff de texto)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the review lacks %q:\n%s", want, got)
		}
	}
}

// A reinstall plan names itself and shows the lock entry it replaces; a
// remove of a parked record without a folder says it has none.
func TestReinstallAndFolderlessRemovePreviews(t *testing.T) {
	got := text.Proposal(app.Proposal{
		Request: app.Request{Action: app.ActionReinstall}, PlanID: "id",
		Install: &app.InstallPreview{
			Key: "grill-me", Source: "owner/grill-me", FollowsDefaultBranch: true,
			LockEntry:     []byte(`{"source": "owner/grill-me", "skillFolderHash": "new"}`),
			ReplacedEntry: []byte(`{"source": "owner/grill-me", "skillFolderHash": "old"}`),
		},
	})
	for _, want := range []string{"Plan: Reinstalar grill-me desde owner/grill-me", "Entrada del lock que se reemplaza:",
		`"skillFolderHash": "old"`, "Entrada del lock que se añade:", `"skillFolderHash": "new"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the reinstall preview lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(text.Proposal(app.Proposal{Request: app.Request{Action: app.ActionInstall}, PlanID: "id",
		Install: &app.InstallPreview{Key: "tidy", LockEntry: []byte("{}")}}), "se reemplaza") {
		t.Error("an install preview speaks of a replaced entry")
	}

	got = text.Proposal(app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "grill-me"}, PlanID: "id",
		Remove: &app.RemovePreview{Key: "grill-me", Parked: true, ParkedEntry: &domain.ParkedEntry{}},
	})
	if !strings.Contains(got, "Sin carpeta: solo queda su registro desactivado.") || strings.Contains(got, "Carpeta (") {
		t.Errorf("the remove preview of a record without a folder:\n%s", got)
	}
}

// The reinstall offer tells it goes through the install review; an entry
// that is not GitHub's cannot be reinstalled.
func TestTheReinstallOffer(t *testing.T) {
	got := text.Reinstall(&app.Reinstall{Key: "grill-me", Input: "owner/grill-me/skills/grill-me"})
	if !strings.Contains(got, "«grill-me»") || !strings.Contains(got, "owner/grill-me/skills/grill-me") || strings.Contains(got, "skills install") {
		t.Errorf("Reinstall = %q", got)
	}
	got = text.Reinstall(&app.Reinstall{Key: "x", SourceType: "local", Source: "/src"})
	if !strings.Contains(got, "no viene de GitHub") {
		t.Errorf("Reinstall of a local entry = %q", got)
	}
}

// The warnings after an operation ran, and a recover that failed, are told
// in Spanish; the cause stays as a detail.
func TestWarningsAndRecoverFailuresInSpanish(t *testing.T) {
	got := text.SupersedeWarning(fmt.Errorf("%w: disk full", app.ErrCandidatesNotSuperseded))
	if !strings.Contains(got, "no se pudieron marcar como reemplazadas") || strings.Contains(got, "superseded") {
		t.Errorf("SupersedeWarning = %q", got)
	}
	got = text.SupersedeWarning(errors.New("release the state lock: boom"))
	if !strings.HasPrefix(got, "aviso: la operación se completó") || !strings.Contains(got, "boom") {
		t.Errorf("SupersedeWarning of another error = %q", got)
	}
	got = text.RecoverFailed(errors.New("record tampered"))
	if !strings.Contains(got, "no se pudo resolver la operación interrumpida") || !strings.Contains(got, "record tampered") {
		t.Errorf("RecoverFailed = %q", got)
	}
}

// A copy identical to the canonical folder gets no empty diff header: it
// says there are no differences; one that differs lists them (L65).
func TestTheRepairPreviewOfIdenticalCopies(t *testing.T) {
	p := app.Proposal{
		Request: app.Request{Action: app.ActionRepair, Finding: "duplicate:alpha", Repair: "replace_copy_with_link"},
		PlanID:  "id",
		Repair: &app.RepairPreview{Finding: "duplicate:alpha", Repair: "replace_copy_with_link", Key: "alpha",
			Copies: []app.CopyDiff{
				{Path: "/h/.codex/skills/alpha", TreeSHA: "abc"},
				{Path: "/h/.gemini/skills/alpha", TreeSHA: "def", Changed: []string{"SKILL.md"}},
			}},
	}
	got := text.Proposal(p)
	if strings.Contains(got, "Diferencias de /h/.codex/skills/alpha con la canónica:\n") {
		t.Errorf("an identical copy gets an empty diff header:\n%s", got)
	}
	for _, want := range []string{
		"Diferencias de /h/.codex/skills/alpha con la canónica: sin diferencias\n",
		"Diferencias de /h/.gemini/skills/alpha con la canónica:\n  ~ SKILL.md\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the preview lacks %q:\n%s", want, got)
		}
	}
}

// A website install says where it comes from, «sitio web uizze.sh (no es
// GitHub)», in its review, its plan and its title, with no commit or branch
// (S14, L84).
func TestAWebInstallNamesItsWebsite(t *testing.T) {
	const origin = "sitio web uizze.sh (no es GitHub)"
	rev := app.Revision{Input: "https://uizze.sh@ui-taste", Repo: "uizze.sh", Site: "https://uizze.sh"}
	rev.Prepared.Resolved.Key = "ui-taste"
	rev.Prepared.TreeSHA = "abc"
	review := text.InstallReview(rev, nil)
	if !strings.Contains(review, "Revisión de ui-taste ("+origin+", árbol abc)") || strings.Contains(review, "commit") {
		t.Errorf("review:\n%s", review)
	}
	p := app.Proposal{Request: app.Request{Action: app.ActionInstall}, PlanID: "id", Install: &app.InstallPreview{
		Key: "ui-taste", Source: "uizze.sh", Site: "https://uizze.sh",
		SourceURL: "https://uizze.sh/.well-known/agent-skills/ui-taste/SKILL.md", TreeSHA: "abc",
		LockEntry: []byte(`{"sourceType": "well-known"}`),
	}}
	if got := text.Title(p); got != "Instalar ui-taste desde "+origin {
		t.Errorf("Title = %q", got)
	}
	plan := text.Proposal(p)
	for _, want := range []string{"Origen: " + origin + ", https://uizze.sh/.well-known/agent-skills/ui-taste/SKILL.md",
		"origen web: la revisión de actualizaciones llegará después"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan lacks %q:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "rama por defecto") || strings.Contains(plan, "commit") {
		t.Errorf("the plan of a website speaks of a branch or a commit:\n%s", plan)
	}
	// GitHub keeps its origin line.
	gh := text.Proposal(app.Proposal{Request: app.Request{Action: app.ActionInstall}, PlanID: "id", Install: &app.InstallPreview{
		Key: "tidy", Source: "acme/tools", Commit: "c0ffee", SkillPath: "skills/tidy/SKILL.md", FollowsDefaultBranch: true,
	}})
	if !strings.Contains(gh, "Origen: acme/tools, commit c0ffee, skills/tidy/SKILL.md (sigue la rama por defecto)") {
		t.Errorf("the GitHub plan:\n%s", gh)
	}
}

// Each refusal of a website install has its own message, and an http://
// site is told to use https.
func TestTheWebRefusalsExplainThemselves(t *testing.T) {
	req := app.Request{Action: app.ActionInstall, Install: &app.InstallRequest{Revision: app.Revision{Input: "https://uizze.sh@ui-taste"}}}
	seen := map[string]error{}
	for _, kind := range []error{app.ErrSiteUnavailable, app.ErrNoWellKnown, app.ErrWebTooLarge, app.ErrUnsafeWebFile,
		app.ErrWebDigestMismatch, app.ErrWebArchive, app.ErrUnsupportedSource, app.ErrGitHubUnavailable} {
		msg, ok := text.Refusal(&app.RefusalError{Kind: kind, Err: errors.New("cause")}, req)
		if !ok || msg == "" {
			t.Errorf("%v has no message", kind)
			continue
		}
		if prev, dup := seen[msg]; dup {
			t.Errorf("%v and %v share %q", kind, prev, msg)
		}
		seen[msg] = kind
	}
	if msg, _ := text.Refusal(&app.RefusalError{Kind: app.ErrNoWellKnown}, req); !strings.Contains(msg, "/.well-known/agent-skills/") {
		t.Errorf("no index = %q", msg)
	}
	if msg, _ := text.Refusal(&app.RefusalError{Kind: app.ErrUnsupportedSource}, req); !strings.Contains(msg, "https://sitio@skill") {
		t.Errorf("unsupported source = %q, want the website form", msg)
	}
	if msg, _ := text.Refusal(&app.RefusalError{Kind: app.ErrAmbiguousSkill}, req); !strings.Contains(msg, "https://sitio@skill") {
		t.Errorf("several skills on a site = %q", msg)
	}
	gh := app.Request{Action: app.ActionInstall, Install: &app.InstallRequest{Revision: app.Revision{Input: "https://github.com/acme/tools"}}}
	if msg, _ := text.Refusal(&app.RefusalError{Kind: app.ErrAmbiguousSkill}, gh); !strings.Contains(msg, "owner/repo@skill") {
		t.Errorf("several skills on GitHub = %q", msg)
	}
	insecure := app.Request{Action: app.ActionInstall, Install: &app.InstallRequest{Revision: app.Revision{Input: "http://uizze.sh@ui-taste"}}}
	if msg, _ := text.Refusal(&app.RefusalError{Kind: app.ErrUnsupportedSource}, insecure); !strings.Contains(msg, "https") || !strings.Contains(msg, "http://") {
		t.Errorf("an http site = %q, want it told to use https", msg)
	}
}

// assertEscaped fails when out prints a character of hostile as it is, or
// lacks hostile's escaped form.
func assertEscaped(t *testing.T, what, out string) {
	t.Helper()
	if strings.ContainsAny(out, "\r\x1b"+rlo) {
		t.Errorf("%s prints third-party text as it is:\n%q", what, out)
	}
	if !strings.Contains(out, hostileShown) {
		t.Errorf("%s lacks %q:\n%s", what, hostileShown, out)
	}
}

// Every value a plan takes from a skill, a folder, a link or a process
// prints escaped (A28), so a plan cannot hide what it does; ordinary names
// print as they are.
func TestThePlansEscapeThirdPartyText(t *testing.T) {
	h := hostile
	links := []app.LinkChange{{Path: "/l/" + h, Target: "../" + h, Harnesses: []string{"codex" + h}}}
	seen := []app.Sighting{{Harness: "codex" + h, Kind: "direct", Entry: "/e/" + h}}
	toggle := &app.TogglePreview{
		Skills: []app.SkillChange{
			{Key: "uno" + h, From: "/f/" + h, To: "/t/" + h, RemovedLinks: links, CreatedLinks: links,
				RestoreLinks: []domain.LinkRecord{{Path: "/r/" + h, Target: "../" + h}}, StillVisible: seen},
			{Key: "dos" + h},
		},
		StillVisible: seen, Visible: seen, SharesDirWith: []string{"gemini" + h}, Kept: []string{"/k/" + h},
	}
	steps := []app.Step{
		{Kind: "move", Src: "/s/" + h, Dst: "/d/" + h}, {Kind: "link", Path: "/p/" + h, Target: "../" + h},
		{Kind: "lockRemove", Name: "n" + h}, {Kind: "otro" + h, Src: h, Dst: h, Path: h, Name: h},
	}
	plans := map[string]app.Proposal{
		"remove": {
			Request: app.Request{Action: app.ActionRemove, Key: "k" + h}, Steps: steps, Targets: []string{"codex" + h},
			Remove: &app.RemovePreview{
				Key: "k" + h, Folder: "/f/" + h, TreeSHA: "abc" + h, Links: links, LockEntry: &app.RemovedEntry{Name: "n" + h},
				Homonyms:          []app.Homonym{{Path: "/h/" + h, Harnesses: []string{"codex" + h}}, {Path: "/o/" + h, Profile: "omni" + h}},
				KeptLinks:         []app.KeptLink{{Path: "/kl/" + h, Target: "../" + h, Zone: "zona" + h}, {Path: "/kp/" + h, Profile: "omni" + h}},
				PendingCandidates: []string{"cand" + h},
			},
			Running: []app.Process{{PID: 42, Harness: "claude-code" + h, Command: "claude --x " + h}},
			Reload:  []string{"codex" + h},
		},
		"disable": {Request: app.Request{Action: app.ActionDisable, Key: "k" + h}, Toggle: toggle},
		"unlink": {
			Request: app.Request{Action: app.ActionUnlink, Key: "k" + h, Harness: "codex" + h},
			Toggle:  &app.TogglePreview{StillVisible: seen},
		},
		"install": {
			Request: app.Request{Action: app.ActionInstall},
			Install: &app.InstallPreview{
				Key: "k" + h, Source: "acme/x" + h, Commit: "c0ffee" + h, SkillPath: "skills/" + h, TreeSHA: "abc" + h,
				StagedPath: "/st/" + h, StorePath: "/store/" + h, Links: links,
				LockEntry: []byte(`{"source": "acme/x` + rlo + `"}`), ReplacedEntry: []byte(`{"source": "old` + rlo + `"}`),
			},
		},
		"web install": {
			Request: app.Request{Action: app.ActionInstall},
			Install: &app.InstallPreview{Key: "k" + h, Source: "sitio" + h, Site: "sitio" + h, SourceURL: "https://sitio/" + h},
		},
		"repair": {
			Request: app.Request{Action: app.ActionRepair, Finding: "f" + h, Repair: "adopt"},
			Repair: &app.RepairPreview{
				Canonical: "/c/" + h, Moved: []app.Move{{From: "/m/" + h, To: "/n/" + h}}, RemovedLinks: links, CreatedLinks: links,
				RemovedCopies: []app.CopyChange{{Path: "/rc/" + h, TreeSHA: "abc" + h}},
				Copies: []app.CopyDiff{
					{Path: "/cd/" + h, Added: []string{"a" + h}, Removed: []string{"r" + h}, Changed: []string{"c" + h}},
					{Path: "/same/" + h},
				},
				LockEntry: "n" + h, Kept: []string{"/k/" + h}, Toggle: toggle,
			},
		},
		"undo": {Request: app.Request{Action: app.ActionUndo}, Undo: &app.UndoPreview{Of: "v2:remove" + h, Snapshot: "snap" + h}},
	}
	for name, p := range plans {
		assertEscaped(t, "the "+name+" plan", text.Proposal(p))
	}
	got := text.Proposal(plans["remove"])
	for _, want := range []string{
		"  1. mover /s/" + hostileShown + " a /d/" + hostileShown + "\n",
		"Entrada del lock que se quita: «n" + hostileShown + "»\n",
		"  claude-code" + hostileShown + " (proceso 42, claude --x " + hostileShown + ")\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the remove plan lacks %q:\n%s", want, got)
		}
	}

	ordinary := text.Proposal(app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "canción-ñandú"},
		Remove:  &app.RemovePreview{Key: "canción-ñandú", Folder: "/h/.agents/skills/canción-ñandú 🎉", TreeSHA: "abc"},
	})
	for _, want := range []string{"Eliminar canción-ñandú\n", "Carpeta (activa): /h/.agents/skills/canción-ñandú 🎉 (árbol abc)\n"} {
		if !strings.Contains(ordinary, want) {
			t.Errorf("an ordinary plan lacks %q:\n%s", want, ordinary)
		}
	}
}

// The install review shows file names, frontmatter keys and diff lines
// from upstream escaped: the file list keeps its column and a file name
// stays on its header line.
func TestTheInstallReviewEscapesThirdPartyText(t *testing.T) {
	h := hostile
	rev := app.Revision{Input: "acme/x" + h, Commit: "c0ffee" + h}
	rev.Prepared.Resolved.Key = "k" + h
	rev.Prepared.TreeSHA = "abc" + h
	rev.Prepared.FileChanges = []domain.FileChange{{Path: "a" + h + ".md", Change: domain.ChangeAdded, NewMode: "100644"}}
	rev.Prepared.FrontmatterDelta.Changed = []string{"clave" + h}
	diffs := []app.FileDiff{
		{Path: "a\n" + h + ".md", Unified: "--- a/a\n" + h + ".md\n+++ b/a\n" + h + ".md\n@@ -0,0 +1 @@\n+línea" + h + "\n"},
		{Path: "b" + h + ".png", Binary: true},
	}

	got := text.InstallReview(rev, diffs)

	assertEscaped(t, "the install review", got)
	for _, want := range []string{
		"  a" + hostileShown + ".md\tañadido 100644\n",
		"Frontmatter: clave" + hostileShown + "\n",
		`--- a/a\n` + hostileShown + ".md\n" + `+++ b/a\n` + hostileShown + ".md\n",
		"+línea" + hostileShown + "\n",
		"\nb" + hostileShown + ".png\n(archivo binario",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the install review lacks %q:\n%s", want, got)
		}
	}
	rev.Site, rev.Repo = "sitio"+h, "sitio"+h
	assertEscaped(t, "the install review of a website", text.InstallReview(rev, nil))
}

// Doctor's findings, results, recoveries, warnings and refusals print the
// names, paths and identifiers they carry escaped.
func TestTheOtherManageTextsEscapeThirdPartyText(t *testing.T) {
	h := hostile
	texts := map[string]string{
		"Findings": text.Findings([]app.Finding{{ID: "f" + h, Kind: "broken_link", Skill: "s" + h,
			Paths: []string{"/p/" + h}, Target: "../" + h, Harnesses: []string{"codex" + h}}}),
		"Reinstall":                text.Reinstall(&app.Reinstall{Key: "k" + h, Input: "acme/x" + h}),
		"Reinstall without GitHub": text.Reinstall(&app.Reinstall{Key: "k" + h, SourceType: "local" + h, Source: "/s/" + h}),
		"Done": text.Done(app.Result{Proposal: app.Proposal{Operation: "v2:remove", Reload: []string{"codex" + h}},
			SnapshotID: "snap" + h, Superseded: []string{"cand" + h}}),
		"Recovery":                 text.Recovery(app.ManagedRecovery{Found: true, Committed: true, SnapshotID: "snap" + h}),
		"Recovery rolled back":     text.Recovery(app.ManagedRecovery{Found: true, RolledBack: true, PlanID: "plan" + h}),
		"PlanID":                   text.PlanID("plan" + h),
		"PlanChanged":              text.PlanChanged("plan" + h),
		"UndoLineNotFound":         text.UndoLineNotFound("v2:x"+h, "snap"+h),
		"SupersedeWarning":         text.SupersedeWarning(fmt.Errorf("%w: lock %s", app.ErrCandidatesNotSuperseded, h)),
		"another SupersedeWarning": text.SupersedeWarning(errors.New("x" + h)),
		"RecoverFailed":            text.RecoverFailed(errors.New("x" + h)),
		"Step":                     text.Step(app.Step{Kind: "otro" + h, Src: h}),
		"Sighting":                 text.Sighting(app.Sighting{Harness: "codex" + h, Kind: "link", Entry: "/e/" + h}),
	}
	for name, got := range texts {
		assertEscaped(t, name, got)
	}

	refusals := []struct {
		kind    error
		req     app.Request
		related []string
	}{
		{app.ErrNotFound, app.Request{Key: "k" + h}, nil},
		{app.ErrKeyReinstalled, app.Request{Key: "k" + h}, []string{"/p/" + h}},
		{app.ErrKeyExists, app.Request{Install: &app.InstallRequest{Revision: app.Revision{Key: "k" + h}}}, []string{"/p/" + h}},
		{app.ErrUnknownHarness, app.Request{Harness: "h" + h}, nil},
		{app.ErrNotLinked, app.Request{Key: "k" + h, Harness: "codex" + h}, nil},
		{app.ErrNeighbourParked, app.Request{Key: "k" + h}, []string{"vecina" + h}},
		{app.ErrSourceMismatch, app.Request{Install: &app.InstallRequest{Revision: app.Revision{Key: "k" + h, Repo: "o/r" + h}}}, nil},
		{app.ErrRepoNotFound, app.Request{Install: &app.InstallRequest{Revision: app.Revision{Input: "o/r" + h}}}, nil},
	}
	for _, r := range refusals {
		msg, ok := text.Refusal(&app.RefusalError{Kind: r.kind, Related: r.related}, r.req)
		if !ok {
			t.Errorf("%v has no message", r.kind)
			continue
		}
		assertEscaped(t, fmt.Sprintf("the refusal %v", r.kind), msg)
	}
}
