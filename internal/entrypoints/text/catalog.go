package text

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"skillhub/internal/app"
)

// The skills.sh catalog (S14): commands, table and errors, in Spanish.

const (
	// SearchUse and the texts below are skillhub skills search.
	SearchUse   = "search <consulta> [--limit n] [--json] [--refresh]"
	SearchShort = "Busca skills en el catálogo de skills.sh"
	SearchLong  = "Busca skills en el catálogo público de skills.sh y muestra su nombre, su origen y\n" +
		"cuántas veces se instalaron. Solo lee: no instala nada.\n\n" +
		"Para instalar un resultado usa «skillhub skills install <origen>», que muestra la\n" +
		"revisión y el plan antes de aplicar: owner/repo@skill para GitHub, o https://sitio@skill\n" +
		"para un sitio web que publica sus skills en /.well-known/agent-skills/ (se marca como\n" +
		"«sitio web (no es GitHub)»). Un resultado que la instalación no admite se marca como no\n" +
		"instalable.\n\n" +
		"Cada búsqueda se guarda una hora en la caché; --refresh vuelve a consultar. Si\n" +
		"skills.sh no responde y hay una búsqueda guardada, se muestra con un aviso. La consulta\n" +
		"usa curl; la variable SKILLS_API_URL cambia la dirección del catálogo."
	// PopularUse and the texts below are skillhub skills popular.
	PopularUse   = "popular [--limit n] [--json] [--refresh]"
	PopularShort = "Lista las skills más instaladas de skills.sh"
	PopularLong  = "Lista las skills más instaladas de skills.sh. skills.sh no publica su ranking sin una\n" +
		"credencial, así que la lista se arma con búsquedas por tema (react, testing, python,\n" +
		"docs, design, git, typescript, security, data y agent), una tras otra, y se ordena por\n" +
		"instalaciones. Solo lee: no instala nada.\n\n" +
		"Para instalar un resultado usa «skillhub skills install <origen>»: owner/repo@skill para\n" +
		"GitHub, o https://sitio@skill para un sitio web.\n" +
		"Las búsquedas se guardan una hora en la caché; --refresh vuelve a consultar."
	// FlagCatalogLimit is --limit of search and popular.
	FlagCatalogLimit = "cantidad máxima de resultados (entre 1 y 100)"
	// FlagRefresh is --refresh of search and popular.
	FlagRefresh = "consulta skills.sh aunque la caché esté vigente"

	// CatalogNoResults is a search with no results.
	CatalogNoResults = "skills.sh no devolvió resultados para «%s».\n"
	// CatalogNoPopular is a popular view with no results.
	CatalogNoPopular = "skills.sh no devolvió resultados.\n"
	// CatalogLimitRange refuses a --limit outside 1 to 100.
	CatalogLimitRange = "--limit debe estar entre 1 y 100 (se recibió %d)"
	// CatalogCacheNotSaved warns that the cache could not be written.
	CatalogCacheNotSaved = "Aviso: no se pudo guardar la caché del catálogo: %v\n"

	catalogHeader            = "NOMBRE\tORIGEN\tINSTALACIONES\tESTADO\n"
	catalogInstalled         = "instalada"
	catalogInstalledOrigin   = "instalada (%s)"
	catalogDuplicateOrigin   = "posible duplicado (%s, de %s)"
	catalogDuplicateNoOrigin = "posible duplicado (%s, sin origen)"
	catalogWebsite           = "sitio web (no es GitHub)"
	catalogNotSupport        = "no instalable"
	catalogInstallHelp       = "Para instalar: skillhub skills install <origen>, con owner/repo@skill para GitHub o https://sitio@skill para un sitio web\n"
	catalogInstallEg         = "Por ejemplo: skillhub skills install %s\n"
	catalogStale             = "Aviso: %s. Se muestran resultados guardados del %s.\n"
)

// Installs shows a count with its thousands apart, as the RAE recommends
// from five digits on: 1204, 48 213.
func Installs(n int) string {
	s := strconv.Itoa(n)
	if n < 10000 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// CatalogMark is the state column of a catalog entry: installed (with its
// origin), possible duplicate (with matching skill and origin), a website that
// installs from its /.well-known/agent-skills/ (L84), not installable, or
// empty when it installs from GitHub.
func CatalogMark(e app.CatalogEntry) string {
	switch {
	case e.Installed:
		if e.InstalledSource != "" {
			return fmt.Sprintf(catalogInstalledOrigin, SafeLine(e.InstalledSource))
		}
		return catalogInstalled
	case e.PossibleDuplicate:
		if e.DuplicateSource != "" {
			return fmt.Sprintf(catalogDuplicateOrigin, SafeLine(e.DuplicateOf), SafeLine(e.DuplicateSource))
		}
		return fmt.Sprintf(catalogDuplicateNoOrigin, SafeLine(e.DuplicateOf))
	case e.Installable && strings.HasPrefix(e.InstallSource, "https://"):
		return catalogWebsite
	case e.Installable:
		return ""
	default:
		return catalogNotSupport
	}
}

// CatalogTable is the catalog entries as aligned columns.
func CatalogTable(entries []app.CatalogEntry) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, catalogHeader)
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", SafeLine(e.Name), SafeLine(e.Source), Installs(e.Installs), CatalogMark(e))
	}
	_ = tw.Flush() //nolint:errcheck // a strings.Builder never fails.
	return b.String()
}

// CatalogInstallHint says how to install a result, with the first one that
// can be installed and is not yet as the example.
func CatalogInstallHint(entries []app.CatalogEntry) string {
	out := catalogInstallHelp
	for _, e := range entries {
		if e.Installable && !e.Installed {
			return out + fmt.Sprintf(catalogInstallEg, SafeLine(e.InstallSource))
		}
	}
	return out
}

// CatalogError explains a failed catalog call; ok is false for any other
// error.
func CatalogError(err error) (msg string, ok bool) {
	ce, ok := errors.AsType[*app.CatalogError](err)
	if !ok {
		return "", false
	}
	switch ce.Kind {
	case app.ErrCatalogQueryTooShort:
		return "la búsqueda necesita al menos dos caracteres", true
	case app.ErrCatalogCurlMissing:
		return "no se pudo ejecutar curl, que skillhub usa para consultar skills.sh; instálalo y vuelve a intentarlo", true
	case app.ErrCatalogOffline:
		return "no se pudo conectar con skills.sh; revisa la conexión a internet", true
	case app.ErrCatalogRateLimited:
		return "skills.sh alcanzó su límite de uso para esta conexión; espera unos minutos y vuelve a intentarlo", true
	case app.ErrCatalogHTTP:
		if ce.Status != 0 {
			return fmt.Sprintf("skills.sh respondió con un error (HTTP %d); vuelve a intentarlo más tarde", ce.Status), true
		}
		return "skills.sh respondió con un error o una redirección; vuelve a intentarlo más tarde", true
	case app.ErrCatalogBadResponse:
		return "skills.sh envió una respuesta inesperada; vuelve a intentarlo más tarde", true
	default:
		return fmt.Sprintf("falló la consulta a skills.sh: %s", SafeLine(fmt.Sprint(ce.Err))), true
	}
}

// CatalogStale warns that skills.sh failed and saved results from at are
// shown instead.
func CatalogStale(err error, at time.Time) string {
	reason, ok := CatalogError(err)
	if !ok {
		reason = SafeLine(err.Error())
	}
	return fmt.Sprintf(catalogStale, reason, at.Local().Format("2006-01-02 15:04"))
}
