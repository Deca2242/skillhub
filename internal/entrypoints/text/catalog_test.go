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

func TestCatalogInstallHintOmitsCatalogOriginExamples(t *testing.T) {
	const hint = "Para instalar: skillhub skills install <origen>, con owner/repo@skill para GitHub o https://sitio@skill para un sitio web\n"
	for _, tc := range []struct {
		name    string
		entries []app.CatalogEntry
	}{
		{"github first", catalogEntries},
		{"website first", catalogEntries[1:]},
		{"no installable candidates", catalogEntries[1:2]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := text.CatalogInstallHint(tc.entries)
			if got != hint {
				t.Errorf("CatalogInstallHint =\n%q\nwant\n%q", got, hint)
			}
			if strings.Contains(got, "Por ejemplo:") {
				t.Errorf("CatalogInstallHint embedded an example command: %q", got)
			}
			for _, e := range tc.entries {
				if e.InstallSource != "" && strings.Contains(got, e.InstallSource) {
					t.Errorf("CatalogInstallHint embedded catalog origin %q: %q", e.InstallSource, got)
				}
			}
		})
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

func TestCatalogTableShowsOriginAndPossibleDuplicates(t *testing.T) {
	entries := []app.CatalogEntry{
		{ID: "acme/skills/alpha", Source: "acme/skills", SkillID: "alpha", Name: "alpha", Installs: 900,
			Installable: true, InstallSource: "acme/skills@alpha", Installed: true, InstalledSource: "acme/skills"},
		{ID: "other/repo/beta", Source: "other/repo", SkillID: "beta", Name: "beta", Installs: 800,
			Installable: true, InstallSource: "other/repo@beta", PossibleDuplicate: true, DuplicateOf: "beta", DuplicateSource: "acme/skills"},
		{ID: "other/repo/gamma", Source: "other/repo", SkillID: "gamma", Name: "gamma", Installs: 700,
			Installable: true, InstallSource: "other/repo@gamma", PossibleDuplicate: true, DuplicateOf: "gamma"},
		{ID: "vercel/skills/react-best", Source: "vercel/skills", SkillID: "react-best", Name: "react-best", Installs: 600,
			Installable: true, InstallSource: "vercel/skills@react-best"},
	}
	got := text.CatalogTable(entries)
	want := "" +
		"NOMBRE      ORIGEN         INSTALACIONES  ESTADO\n" +
		"alpha       acme/skills    900            instalada (acme/skills)\n" +
		"beta        other/repo     800            posible duplicado (beta, de acme/skills)\n" +
		"gamma       other/repo     700            posible duplicado (gamma, sin origen)\n" +
		"react-best  vercel/skills  600            \n"
	if got != want {
		t.Errorf("CatalogTable =\n%s\nwant\n%s", got, want)
	}
}
