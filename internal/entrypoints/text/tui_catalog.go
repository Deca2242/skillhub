package text

import (
	"errors"
	"fmt"
	"strings"

	"skillhub/internal/app"
)

const (
	catalogSearchLabel  = "buscar"
	catalogOriginLabel  = "escribir origen"
	catalogReloadLabel  = "recargar catálogo"
	catalogInstallLabel = "instalar"

	TUICatalogPrompt      = "Buscar en skills.sh: "
	TUICatalogSearchKeys  = "enter buscar • esc cancelar"
	catalogBrowseKeys     = "/ " + catalogSearchLabel + " • o " + catalogOriginLabel + " • u " + catalogReloadLabel
	TUICatalogKeys        = catalogBrowseKeys + " • enter " + catalogInstallLabel
	TUIHelpInstallCatalog = "3 Instalar: " + TUICatalogKeys + " • esc cancelar"

	catalogFooterTail       = " • esc cancelar • ? ayuda • q salir"
	TUICatalogFooter        = catalogBrowseKeys + catalogFooterTail
	TUICatalogFooterInstall = TUICatalogKeys + catalogFooterTail

	TUICatalogActionSearch  = "[/] Buscar"
	TUICatalogActionOrigin  = "[o] Escribir origen"
	TUICatalogActionReload  = "[u] Recargar catálogo"
	TUICatalogActionInstall = "[enter] Instalar"
	TUICatalogActionBack    = "[esc] Cancelar"

	TUICatalogResults        = "Resultados"
	TUICatalogLoading        = "Consultando skills.sh…"
	TUICatalogEmpty          = "skills.sh no devolvió resultados."
	TUICatalogUnsupported    = "El origen de este resultado no se puede instalar con skillhub."
	TUICatalogInstalled      = catalogInstalled
	TUICatalogNotInstallable = catalogNotSupport
)

// TUICatalogHeading describes the current results, not the draft search.
func TUICatalogHeading(query string, count int) string {
	if query == "" {
		return fmt.Sprintf("Populares en skills.sh (%d)", count)
	}
	return fmt.Sprintf("Búsqueda «%s» en skills.sh (%d)", SafeLine(query), count)
}

// TUICatalogName uses the skill ID if the catalog did not give a name.
func TUICatalogName(e app.CatalogEntry) string {
	if e.Name != "" {
		return SafeLine(e.Name)
	}
	return SafeLine(e.SkillID)
}

// TUICatalogDetails gives the evidence behind the selected row's status.
func TUICatalogDetails(e app.CatalogEntry) []string {
	var lines []string
	switch {
	case e.Installed:
		lines = append(lines, CatalogMark(e))
	case !e.Installable:
		lines = append(lines, TUICatalogUnsupported)
	case e.PossibleDuplicate:
		lines = append(lines, CatalogMark(e))
	}
	if e.Installable && strings.HasPrefix(e.InstallSource, "https://") {
		lines = append(lines, fmt.Sprintf("sitio web %s (no es GitHub)", SafeLine(e.Source)))
	}
	lines = append(lines, "Origen: "+SafeLine(e.Source), "Instalaciones: "+Installs(e.Installs))
	if e.Installable {
		lines = append(lines, "Para instalar: "+SafeLine(e.InstallSource))
	}
	return lines
}

// TUICatalogAlreadyInstalled explains the refusal and where to go instead.
func TUICatalogAlreadyInstalled(e app.CatalogEntry) string {
	origin := ""
	if e.InstalledSource != "" {
		origin = " desde " + SafeLine(e.InstalledSource)
	}
	return "Ya está instalada" + origin + ". Ve a Skills (1) o Actualizar (2)."
}

// TUICatalogError explains a catalog failure without requiring a command.
func TUICatalogError(err error) string {
	if errors.Is(err, app.ErrCatalogCurlMissing) {
		return "No se pudo consultar skills.sh en este equipo."
	}
	if msg, ok := CatalogError(err); ok {
		return msg
	}
	return "No se pudo consultar el catálogo: " + SafeLine(err.Error())
}
