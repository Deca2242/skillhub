package text

import (
	"errors"
	"fmt"
	"strings"

	"skillhub/internal/app"
	"skillhub/internal/packs"
)

// Help and messages for the packs command, in neutral Spanish (S11).
const (
	PacksShort = "Crea y consulta borradores de packs de skills"

	PacksListUse   = "list [--json]"
	PacksListShort = "Lista los borradores de packs"
	PacksListLong  = "Lista todos los borradores de packs guardados en skillhub. Solo lee: no activa ni modifica ninguna skill."

	PacksCreateUse   = "create <pack> [--description <texto>]"
	PacksCreateShort = "Crea un borrador de pack"
	PacksCreateLong  = "Crea un borrador de pack vacío con nombre y descripción opcional. No instala ni activa nada."

	PacksShowUse   = "show <pack> [--json]"
	PacksShowShort = "Muestra un borrador de pack y la salud observada de sus miembros"
	PacksShowLong  = "Muestra la descripción y los miembros de un borrador de pack junto con su estado observado en el inventario actual. Si un miembro no se puede observar, conserva su referencia como «no observada»."

	PacksAddUse   = "add <pack> <skill>"
	PacksAddShort = "Agrega una skill observada al borrador de pack"
	PacksAddLong  = "Agrega una skill observada en el inventario actual al borrador de pack, guardando su identidad exacta. No modifica carpetas de skills, enlaces ni el lock."

	PacksRemoveUse   = "remove <pack> <skill>"
	PacksRemoveShort = "Quita una skill del borrador de pack"
	PacksRemoveLong  = "Quita un miembro del borrador de pack por su identidad exacta o su nombre observado. No desinstala ni modifica ninguna skill."

	FlagPackDescription = "descripción opcional del borrador de pack"

	PacksEmpty        = "No hay borradores de packs.\n"
	PackCreated       = "Borrador de pack «%s» creado.\n"
	PackMemberAdded   = "Skill «%s» agregada al borrador de pack «%s».\n"
	PackMemberRemoved = "Skill «%s» quitada del borrador de pack «%s».\n"
)

// PackList formats all draft packs in neutral Spanish, escaping third-party text.
func PackList(list []app.PackSummary) string {
	if len(list) == 0 {
		return PacksEmpty
	}
	var b strings.Builder
	for _, p := range list {
		if p.Description != "" {
			fmt.Fprintf(&b, "- %s (%d): %s\n", SafeLine(p.Name), p.MemberCount, SafeLine(p.Description))
			continue
		}
		fmt.Fprintf(&b, "- %s (%d)\n", SafeLine(p.Name), p.MemberCount)
	}
	return b.String()
}

// PackShow formats one draft pack and each member's observed health in neutral Spanish.
func PackShow(v app.PackView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pack (borrador): %s\n", SafeLine(v.Name))
	if v.Description != "" {
		fmt.Fprintf(&b, "Descripción: %s\n", SafeLine(v.Description))
	}
	if len(v.Members) == 0 {
		b.WriteString("Sin miembros.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Miembros (%d):\n", len(v.Members))
	for _, m := range v.Members {
		if m.Presence == app.MemberNotObserved {
			fmt.Fprintf(&b, "  - %s — no observada (se conserva la referencia)\n", SafeLine(m.ID))
			continue
		}
		label := SafeLine(m.ID)
		if m.Name != "" {
			label = fmt.Sprintf("%s (%s)", SafeLine(m.Name), SafeLine(m.ID))
		}
		fmt.Fprintf(&b, "  - %s — observada · salud: %s · estado: %s", label, packMemberHealth(m.Health), State(m.State))
		if len(m.Issues) > 0 {
			fmt.Fprintf(&b, " · problemas: %s", formatShowIssues(m.Issues))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func packMemberHealth(h app.MemberHealth) string {
	switch h {
	case app.MemberHealthHealthy:
		return "sana"
	case app.MemberHealthUpdateAvailable:
		return "actualización disponible"
	case app.MemberHealthIssues:
		return "con problemas"
	case app.MemberHealthNotObserved:
		return "no observada"
	default:
		return SafeLine(string(h))
	}
}

// PackError translates pack service errors into neutral Spanish messages.
func PackError(err error) (string, bool) {
	switch {
	case errors.Is(err, packs.ErrInvalidPackName):
		return "nombre de pack no válido: usa un nombre sin espacios en los extremos, barras ni caracteres de control", true
	case errors.Is(err, packs.ErrPackExists):
		return "ese borrador de pack ya existe", true
	case errors.Is(err, packs.ErrPackNotFound):
		return "no existe ningún borrador de pack con ese nombre", true
	case errors.Is(err, packs.ErrMemberExists):
		return "esa skill ya forma parte del borrador de pack", true
	case errors.Is(err, packs.ErrMemberNotFound):
		return "esa skill no forma parte del borrador de pack", true
	case errors.Is(err, packs.ErrUnsupportedSidecarVersion):
		return "el archivo de borradores de packs tiene una versión no compatible", true
	case errors.Is(err, packs.ErrCorruptSidecar):
		return "el archivo de borradores de packs está dañado o no es válido", true
	default:
		return "", false
	}
}
