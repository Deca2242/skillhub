package text_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func TestInstallsGroupsThousands(t *testing.T) {
	for n, want := range map[int]string{
		0: "0", 7: "7", 999: "999", 1204: "1204", 9999: "9999",
		10000: "10 000", 48213: "48 213", 1234567: "1 234 567",
	} {
		if got := text.Installs(n); got != want {
			t.Errorf("Installs(%d) = %q, want %q", n, got, want)
		}
	}
}

var catalogEntries = []app.CatalogEntry{
	{ID: "vercel-labs/agent-skills/react-best", Source: "vercel-labs/agent-skills", SkillID: "react-best",
		Name: "react-best", Installs: 48213, Installable: true, InstallSource: "vercel-labs/agent-skills@react-best"},
	{ID: "acme/skills/alpha", Source: "acme/skills", SkillID: "alpha", Name: "Alpha", Installs: 1204,
		Installable: true, InstallSource: "acme/skills@alpha", Installed: true},
	// A website installs from its /.well-known/agent-skills/ (L84); one
	// whose skill id install does not accept is not installable.
	{ID: "mintlify.com/mintlify", Source: "mintlify.com", SkillID: "mintlify", Name: "Mintlify", Installs: 5,
		Installable: true, InstallSource: "https://mintlify.com@mintlify"},
	{ID: "docs.example.com/bad_name", Source: "docs.example.com", SkillID: "bad_name", Name: "Bad", Installs: 3,
		InstallSource: "https://docs.example.com@bad_name"},
}

func TestCatalogTable(t *testing.T) {
	got := text.CatalogTable(catalogEntries)
	want := "" +
		"NOMBRE      ORIGEN                    INSTALACIONES  ESTADO\n" +
		"react-best  vercel-labs/agent-skills  48 213         \n" +
		"Alpha       acme/skills               1204           instalada\n" +
		"Mintlify    mintlify.com              5              sitio web (no es GitHub)\n" +
		"Bad         docs.example.com          3              no instalable\n"
	if got != want {
		t.Errorf("CatalogTable =\n%s\nwant\n%s", got, want)
	}
}

func TestCatalogInstallHintNamesTheFirstInstallableSkill(t *testing.T) {
	const hint = "Para instalar: skillhub skills install <origen>, con owner/repo@skill para GitHub o https://sitio@skill para un sitio web\n"
	got := text.CatalogInstallHint(catalogEntries)
	want := hint + "Por ejemplo: skillhub skills install vercel-labs/agent-skills@react-best\n"
	if got != want {
		t.Errorf("CatalogInstallHint =\n%q\nwant\n%q", got, want)
	}
	// A website is an example too.
	if got := text.CatalogInstallHint(catalogEntries[1:]); got != hint+"Por ejemplo: skillhub skills install https://mintlify.com@mintlify\n" {
		t.Errorf("CatalogInstallHint with a website first = %q", got)
	}
	// With nothing left to install, only the general form.
	if got := text.CatalogInstallHint(catalogEntries[1:2]); got != hint {
		t.Errorf("CatalogInstallHint without candidates = %q", got)
	}
}

func TestCatalogErrorExplainsEachFailure(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&app.CatalogError{Kind: app.ErrCatalogQueryTooShort}, "al menos dos caracteres"},
		{&app.CatalogError{Kind: app.ErrCatalogCurlMissing}, "no se pudo ejecutar curl"},
		{&app.CatalogError{Kind: app.ErrCatalogOffline}, "no se pudo conectar con skills.sh"},
		{&app.CatalogError{Kind: app.ErrCatalogRateLimited, Status: 429}, "límite de uso"},
		{&app.CatalogError{Kind: app.ErrCatalogHTTP, Status: 503}, "HTTP 503"},
		{&app.CatalogError{Kind: app.ErrCatalogHTTP}, "skills.sh respondió con un error"},
		{&app.CatalogError{Kind: app.ErrCatalogBadResponse}, "respuesta inesperada"},
		{&app.CatalogError{Kind: app.ErrCatalogFailed, Err: errors.New("curl exited with 97")}, "curl exited with 97"},
		{fmt.Errorf("wrapped: %w", &app.CatalogError{Kind: app.ErrCatalogOffline}), "no se pudo conectar"},
	} {
		got, ok := text.CatalogError(tc.err)
		if !ok || !strings.Contains(got, tc.want) {
			t.Errorf("CatalogError(%v) = %q, %v; want it to contain %q", tc.err, got, ok, tc.want)
		}
	}
	if _, ok := text.CatalogError(errors.New("other")); ok {
		t.Error("CatalogError explained an error that is not a catalog error")
	}
}

func TestCatalogStaleWarning(t *testing.T) {
	at := time.Date(2026, 11, 22, 9, 30, 0, 0, time.Local)
	got := text.CatalogStale(&app.CatalogError{Kind: app.ErrCatalogOffline}, at)
	if !strings.HasPrefix(got, "Aviso: ") || !strings.Contains(got, "no se pudo conectar con skills.sh") ||
		!strings.Contains(got, "2026-11-22 09:30") || !strings.HasSuffix(got, "\n") {
		t.Errorf("CatalogStale = %q", got)
	}
}
