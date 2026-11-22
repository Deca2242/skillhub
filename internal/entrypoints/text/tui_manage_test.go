package text_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

// A repair offered in the TUI is told in plain words (UX2); its kind stays
// in the plan's technical detail.
func TestTUIRepairPlainNamesTheRepair(t *testing.T) {
	if got, want := text.TUIRepairPlain("delete_link"), "Borrar el enlace que no lleva a ninguna parte"; got != want {
		t.Errorf("TUIRepairPlain = %q, want %q", got, want)
	}
}

// The confirmation names the identifier it approves, without the CLI's
// --yes --plan advice.
func TestTUIPlanIDNamesOnlyTheIdentifier(t *testing.T) {
	got := text.TUIPlanID("abc")
	if got != "Identificador del plan: abc\n" || strings.Contains(got, "--yes") {
		t.Errorf("TUIPlanID = %q", got)
	}
}

// The global help names the four tabs, the shortcuts and z, and every key
// of the Skills screen (UX3).
func TestTUIHelpNamesTheVersion2Keys(t *testing.T) {
	if strings.Contains(text.TUIHelpSkills+text.TUIHelpSkills2, "harness") {
		t.Errorf("TUIHelpSkills says harness: %q", text.TUIHelpSkills)
	}
	for _, want := range []string{"1-4 pestañas", "tab siguiente", "h historial", "c ajustes", "z deshacer lo último"} {
		if !strings.Contains(text.TUIHelpGlobal, want) {
			t.Errorf("TUIHelpGlobal lacks %q", want)
		}
	}
	for _, want := range []string{"d desactivar", "a activar", "e eliminar", "l elegir agentes", "f filtrar", "g ver por agente"} {
		if !strings.Contains(text.TUIHelpSkills+text.TUIHelpSkills2, want) {
			t.Errorf("TUIHelpSkills lacks %q", want)
		}
	}
}

// refusalKinds are every refusal kind app.RefusalError carries, by name:
// TestEveryAppRefusalIsListed keeps it complete.
var refusalKinds = map[string]error{
	"ErrPlanChanged":        app.ErrPlanChanged,
	"ErrNoPlan":             app.ErrNoPlan,
	"ErrInvalidRequest":     app.ErrInvalidRequest,
	"ErrInvalidKey":         app.ErrInvalidKey,
	"ErrNotFound":           app.ErrNotFound,
	"ErrForeignZone":        app.ErrForeignZone,
	"ErrAmbiguous":          app.ErrAmbiguous,
	"ErrAlias":              app.ErrAlias,
	"ErrChanged":            app.ErrChanged,
	"ErrAlreadyDisabled":    app.ErrAlreadyDisabled,
	"ErrNotDisabled":        app.ErrNotDisabled,
	"ErrKeyReinstalled":     app.ErrKeyReinstalled,
	"ErrKeyInUse":           app.ErrKeyInUse,
	"ErrKeyExists":          app.ErrKeyExists,
	"ErrUnknownHarness":     app.ErrUnknownHarness,
	"ErrNotAdministrable":   app.ErrNotAdministrable,
	"ErrHarnessNotDetected": app.ErrHarnessNotDetected,
	"ErrNotLinked":          app.ErrNotLinked,
	"ErrAlreadyLinked":      app.ErrAlreadyLinked,
	"ErrPathTaken":          app.ErrPathTaken,
	"ErrParkingVisible":     app.ErrParkingVisible,
	"ErrNothingToAdopt":     app.ErrNothingToAdopt,
	"ErrNeighbourParked":    app.ErrNeighbourParked,
	"ErrBadParkedRecord":    app.ErrBadParkedRecord,
	"ErrNameMismatch":       app.ErrNameMismatch,
	"ErrPinnedRef":          app.ErrPinnedRef,
	"ErrMissingDir":         app.ErrMissingDir,
	"ErrInvalidReview":      app.ErrInvalidReview,
	"ErrStagingChanged":     app.ErrStagingChanged,
	"ErrUnsupportedSource":  app.ErrUnsupportedSource,
	"ErrNoSkill":            app.ErrNoSkill,
	"ErrAmbiguousSkill":     app.ErrAmbiguousSkill,
	"ErrStale":              app.ErrStale,
	"ErrNotOffered":         app.ErrNotOffered,
	"ErrNeedsCanonical":     app.ErrNeedsCanonical,
	"ErrBadCanonical":       app.ErrBadCanonical,
	"ErrLockedSkill":        app.ErrLockedSkill,
	"ErrLinkedCopy":         app.ErrLinkedCopy,
	"ErrNoPlacement":        app.ErrNoPlacement,
	"ErrPrecondition":       app.ErrPrecondition,
	"ErrForeignState":       app.ErrForeignState,
	"ErrOperationPending":   app.ErrOperationPending,
	"ErrChangedSince":       app.ErrChangedSince,
	"ErrNothingToUndo":      app.ErrNothingToUndo,
	"ErrUnsupportedTree":    app.ErrUnsupportedTree,
	"ErrLockChanged":        app.ErrLockChanged,
	"ErrNoEntryToReinstall": app.ErrNoEntryToReinstall,
	"ErrEntryPinsRef":       app.ErrEntryPinsRef,
	"ErrSourceMismatch":     app.ErrSourceMismatch,
	"ErrLinkedLinkDir":      app.ErrLinkedLinkDir,
	"ErrCanonicalNotUsed":   app.ErrCanonicalNotUsed,
	"ErrRepoNotFound":       app.ErrRepoNotFound,
	"ErrRefNotFound":        app.ErrRefNotFound,
	"ErrRateLimited":        app.ErrRateLimited,
	"ErrNotAuthenticated":   app.ErrNotAuthenticated,
	"ErrGitHubUnavailable":  app.ErrGitHubUnavailable,
	"ErrLockUnsupported":    app.ErrLockUnsupported,
	"ErrLockMalformed":      app.ErrLockMalformed,
	"ErrStagingFailed":      app.ErrStagingFailed,
	"ErrSiteUnavailable":    app.ErrSiteUnavailable,
	"ErrNoWellKnown":        app.ErrNoWellKnown,
	"ErrWebTooLarge":        app.ErrWebTooLarge,
	"ErrUnsafeWebFile":      app.ErrUnsafeWebFile,
	"ErrWebDigestMismatch":  app.ErrWebDigestMismatch,
	"ErrWebArchive":         app.ErrWebArchive,
}

// cliWords are what only the CLI user can act on: a skillhub command, a
// flag, or "run the command again".
var cliWords = regexp.MustCompile(`«skillhub|skillhub (scan|doctor|recover|skills|history|undo)\b|--[a-z]|el comando`)

// The TUI tells every refusal in plain words, without the CLI's commands or
// flags (L83).
func TestTUIRefusalNamesNoCommand(t *testing.T) {
	for _, req := range []app.Request{
		{Action: app.ActionDisable, Key: "x"},
		{Action: app.ActionEnable, Key: "x"},
		{Action: app.ActionRepair, Finding: "broken_link:a", Repair: "adopt"},
		{Action: app.ActionUnlink, Key: "x"},
	} {
		for _, k := range refusalKinds {
			err := &app.RefusalError{Kind: k, Err: k, Related: []string{"y"}}
			msg, ok := text.TUIRefusal(err, req)
			if !ok || msg == "" {
				t.Errorf("TUIRefusal(%v, %s) = %q, %v; want a message", k, req.Action, msg, ok)
				continue
			}
			if w := cliWords.FindString(msg); w != "" {
				t.Errorf("TUIRefusal(%v, %s) = %q names %q", k, req.Action, msg, w)
			}
		}
	}
	if _, ok := text.TUIRefusal(app.ErrNotFound, app.Request{}); ok {
		t.Error("TUIRefusal told an error that is no refusal")
	}
}

// A skill the planners do not find is not in skillhub's store: the TUI says
// so and points to Doctor.
func TestTUIRefusalOfASkillOutsideTheStore(t *testing.T) {
	err := &app.RefusalError{Kind: app.ErrNotFound, Err: app.ErrNotFound}
	got, _ := text.TUIRefusal(err, app.Request{Action: app.ActionDisable, Key: "humanizer"})
	want := "esta skill no está en el almacén de skillhub, así que no se puede desactivar desde aquí. Mira Salud (4) para ver cómo ordenarla"
	if got != want {
		t.Errorf("TUIRefusal = %q, want %q", got, want)
	}
}

// The help names the keys the Skills panel offers for a skill in OFF/ and
// for a broken link.
func TestTUIHelpNamesTheRowFixes(t *testing.T) {
	for _, want := range []string{"o desactivarla de verdad", "p arreglar el enlace roto"} {
		if !strings.Contains(text.TUIHelpSkills+text.TUIHelpSkills2, want) {
			t.Errorf("the Skills help lacks %q", want)
		}
	}
}

// Every refusal kind the app declares is in refusalKinds, so that
// TestTUIRefusalNamesNoCommand gives each one a plain text: the kinds are
// the Err variables of the var blocks of app's errors.go and install.go.
func TestEveryAppRefusalIsListed(t *testing.T) {
	fset := token.NewFileSet()
	var declared []string
	for _, name := range []string{"errors.go", "install.go"} {
		f, err := parser.ParseFile(fset, filepath.Join("..", "..", "app", name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR || !g.Lparen.IsValid() {
				continue
			}
			for _, spec := range g.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					if strings.HasPrefix(id.Name, "Err") {
						declared = append(declared, id.Name)
					}
				}
			}
		}
	}
	if len(declared) < len(refusalKinds) {
		t.Fatalf("found only %d refusal kinds: %v", len(declared), declared)
	}
	for _, name := range declared {
		if _, ok := refusalKinds[name]; !ok {
			t.Errorf("app.%s has no entry in refusalKinds: add it and give it a plain text", name)
		}
	}
}

// The refusals of an install from a website are told without its
// technical words.
func TestTUIRefusalsOfAWebsiteArePlain(t *testing.T) {
	jargon := regexp.MustCompile(`(?i)digest|índice|well-known|curl|límites de descarga`)
	for _, k := range []error{app.ErrSiteUnavailable, app.ErrNoWellKnown, app.ErrWebTooLarge,
		app.ErrUnsafeWebFile, app.ErrWebDigestMismatch, app.ErrWebArchive} {
		msg, ok := text.TUIRefusal(&app.RefusalError{Kind: k, Err: k}, app.Request{Action: app.ActionInstall})
		cli, _ := text.Refusal(&app.RefusalError{Kind: k, Err: k}, app.Request{Action: app.ActionInstall})
		if !ok || msg == cli || jargon.MatchString(msg) || !strings.Contains(msg, "sitio web") {
			t.Errorf("TUIRefusal(%v) = %q, want its own plain text about the website", k, msg)
		}
	}
}
