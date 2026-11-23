package text

import (
	"errors"
	"fmt"
	"strings"

	"skillhub/internal/app"
)

// The texts only the TUI shows for the version 2 operations, in neutral
// Spanish (S11). The plans, refusals and results are the CLI's own texts
// (manage.go), so both entry points tell the user the same thing.

// Tabs, help and prompts of the version 2 screens.
const (
	TUIHelpGlobal  = "En todas: 1-4 pestañas • tab siguiente • h historial • c ajustes • z deshacer lo último • r revisar ahora • ? ayuda • q salir"
	TUIHelpLists   = "Listas: ↑/↓ mover • PgUp/PgDn una página • home/end inicio y fin"
	TUIHelpSkills  = "1 Skills: / buscar • f filtrar • g ver por agente • l elegir agentes • d desactivar • a activar • e eliminar"
	TUIHelpSkills2 = "1 Skills: o desactivarla de verdad (OFF/) • p arreglar el enlace roto • x ignorar o dejar de ignorar (filtro ignoradas) • enter ver todo"
	TUIHelpUpdates = "2 Actualizar: enter ver cambios • a aplicar esta • o omitir esta versión • t aplicar todas"
	TUIHelpDiff    = "Cambios: ↑/↓ desplazar • v cambiar vista (si la modificaste) • esc volver"
	TUIHelpInstall = "3 Instalar: enter empezar o continuar • espacio marcar un agente • esc cancelar"
	TUIHelpDoctor  = "4 Salud: ↑/↓ elegir • enter ver cómo repararlo"
	TUIHelpHistory = "Historial (h): u deshacer la operación elegida • esc volver"
	TUIHelpSetting = "Ajustes (c): ↑/↓ elegir • enter cambiar • esc volver"
	TUIHelpPlan    = "Confirmación: s sí • n no • v ver u ocultar detalles • esc no"
	TUIHelpChoose  = "↑/↓ elegir • enter aceptar • esc volver"

	TUIConfirmPlan = "¿Aplicar este plan? (s/n)"
	TUIPlanning    = "Preparando el plan..."
	TUIApplying    = "Aplicando el plan..."
	TUIResolving   = "Resolviendo y preparando el origen..."
	TUISearching   = "Buscando hallazgos..."
	TUIReadRepairs = "Leyendo las reparaciones del hallazgo..."
	// TUIReadingActions answers a write key pressed before the app told
	// what each skill admits.
	TUIReadingActions = "Leyendo qué se puede hacer con cada skill; vuelve a intentarlo en un momento."

	TUIPromptEnable = "Clave de la skill desactivada: "
	TUIPromptSource = "Origen (GitHub o sitio web): "
	TUIPromptHint   = "enter para continuar, esc para cancelar"

	TUIInstallReview = "enter elegir los agentes • ↑/↓ desplazar la revisión • esc cancelar"

	TUINoFindings   = "Salud no encontró nada que informar."
	TUIFindingsHint = "Buscar no cambia nada: solo se repara lo que elijas y confirmes."

	TUIColFinding = "Hallazgo"

	// TUIStaleResult replaces a plan, a review, a list of repairs or a
	// confirmation that arrived after the user left the screen or the mode
	// that asked for it: it is dropped, never shown in silence.
	TUIStaleResult = "Llegó un resultado cuando ya estabas en otra pantalla o en otro modo; se descartó y no se aplicó nada. Vuelve a pedirlo si todavía lo quieres."
	// TUIDiscardFailed follows a cancelled install whose prepared copy
	// could not be removed.
	TUIDiscardFailed = "Aviso: no se pudo borrar la copia preparada de la instalación en ~/.agents/.skillhub/staging; no se instaló nada."
	// TUIHelpScroll is shown under a help that does not fit the terminal.
	TUIHelpScroll = "↑/↓ desplazar la ayuda"

	// TUIManagedPending and TUIManagedRecoverPrompt are the opening screen
	// while a version 2 operation record is pending: recover is offered and
	// runs only when the user asks.
	TUIManagedPending = "Hay una operación de gestión de skills sin terminar.\n" +
		"Recuperarla la revierte si no llegó a completarse,\n" +
		"o termina de cerrarla si ya se había completado.\n" +
		"La TUI nunca la resuelve sola."
	TUIManagedRecoverPrompt = "Pulsa r o enter para recuperarla, o q para salir."

	// TUIParkedEntry is the note of a disabled skill's row when its lock
	// entry is parked with it: short, so it fits the issues column.
	TUIParkedEntry = "entrada aparcada"
	// TUIParkedOrigin is the origin column of a disabled skill's row.
	TUIParkedOrigin = "aparcada"

	// TUIUndoHint follows the result of an operation: the TUI undoes it
	// with z right after, or from Historial.
	TUIUndoHint = "En la TUI se deshace con z enseguida, o con u en el Historial (h)."
)

// TUIPlanID is the identifier the confirmation approves.
func TUIPlanID(id string) string {
	return fmt.Sprintf("Identificador del plan: %s\n", SafeLine(id))
}

// TUIParkedSince is the state of a disabled skill's row on the Skills
// screen: since when skillhub keeps it parked.
func TUIParkedSince(when string) string {
	return fmt.Sprintf("desactivada desde %s", SafeLine(when))
}

// TUIRefusal explains a refusal of a version 2 operation as Refusal does,
// but in the TUI's words: the refusals whose CLI text names a skillhub
// command, a flag or "run the command again" tell instead what to do in the
// TUI (L83). ok is false when err is not a refusal.
func TUIRefusal(err error, req app.Request) (msg string, ok bool) {
	var re *app.RefusalError
	if !errors.As(err, &re) {
		return "", false
	}
	for _, r := range tuiRefusalTexts {
		if errors.Is(re.Kind, r.kind) {
			return SafeLine(r.text(req, re.Related)), true
		}
	}
	return Refusal(err, req)
}

var tuiRefusalTexts = []refusalText{
	{app.ErrNotFound, func(req app.Request, _ []string) string {
		return fmt.Sprintf("esta skill no está en el almacén de skillhub, así que no se puede %s desde aquí. Mira Salud (4) para ver cómo ordenarla",
			lookup(plainActions, string(req.Action)))
	}},
	{app.ErrUnknownHarness, func(req app.Request, _ []string) string {
		if req.Harness == "" {
			return "uno de los agentes indicados no es conocido; escribe su id, por ejemplo claude-code"
		}
		return fmt.Sprintf("«%s» no es un agente conocido", req.Harness)
	}},
	{app.ErrNeighbourParked, func(req app.Request, related []string) string {
		if len(related) == 0 {
			return fmt.Sprintf("«%s» no puede volver todavía: falta una skill vecina que skillhub no guarda", req.Key)
		}
		return fmt.Sprintf("«%s» no puede volver todavía a su lugar: antes hay que activar %s, que se desactivó después. Elige esa fila y pulsa [a]",
			req.Key, strings.Join(related, ", "))
	}},
	{app.ErrInvalidReview, fixed("la revisión no es una instalación preparada; vuelve a empezar desde Instalar (3)")},
	{app.ErrStagingChanged, fixed("la copia preparada cambió desde que se revisó; vuelve a empezar desde Instalar (3) para revisarla de nuevo")},
	{app.ErrStale, fixed("el problema cambió desde que Doctor lo encontró o ya no existe; pulsa r para volver a revisar")},
	{app.ErrNotOffered, fixed("esa reparación no se aplica a este problema; Salud (4) muestra las que sí")},
	{app.ErrNeedsCanonical, fixed("esta reparación necesita que elijas qué carpeta se conserva; hazlo desde Salud (4)")},
	{app.ErrBadCanonical, fixed("la carpeta elegida no es una de las carpetas de la skill")},
	{app.ErrCanonicalNotUsed, fixed("esta reparación no elige qué carpeta se conserva; no se cambió nada")},
	{app.ErrForeignState, fixed("otro programa cambió algo durante la operación; se conservaron los dos estados y no se sobrescribió nada. Recupérala con r para ver el diagnóstico")},
	{app.ErrOperationPending, fixed("hay una operación sin terminar; recupérala con r antes de continuar")},
	{app.ErrSiteUnavailable, fixed("no se pudo leer el sitio web: revisa tu conexión y la dirección, y vuelve a intentarlo. No se cambió nada")},
	{app.ErrNoWellKnown, fixed("ese sitio web no publica skills que se puedan instalar; revisa la dirección. No se cambió nada")},
	{app.ErrWebTooLarge, fixed("la skill de ese sitio web es demasiado grande para descargarla. No se instaló nada")},
	{app.ErrUnsafeWebFile, fixed("la skill de ese sitio web trae un archivo de otro lugar o con una ruta peligrosa, así que no se descarga. No se instaló nada")},
	{app.ErrWebDigestMismatch, fixed("lo que se descargó no coincide con lo que anuncia el sitio web: puede estar a medio publicarse o alterado. No se instaló nada")},
	{app.ErrWebArchive, fixed("ese sitio web entrega la skill comprimida, y skillhub todavía no la instala así. No se cambió nada")},
	{app.ErrNoEntryToReinstall, func(req app.Request, _ []string) string {
		return fmt.Sprintf("no hay ningún registro de «%s» que reinstalar; para una skill nueva, usa Instalar (3)", installKey(req))
	}},
}

// The Salud list grouped by skill (INV10, S15 #9): each affected skill is
// one line, open with its findings under it while the cursor is in it; the
// findings that name no skill keep a group of their kind.
const (
	// TUIHealthOpen marks the group the cursor is in, shown with its
	// findings; TUIHealthClosed marks the others.
	TUIHealthOpen   = "▾"
	TUIHealthClosed = "▸"
)

// TUIHealthTitle heads the Salud list: the skills affected, then the
// findings, each counted once. Without a skill, only the findings.
func TUIHealthTitle(skills, findings int) string {
	if skills == 0 {
		return TUIDoctorTitle(findings)
	}
	affected := fmt.Sprintf("%d skills afectadas", skills)
	if skills == 1 {
		affected = "1 skill afectada"
	}
	return affected + " · " + TUIHealthCount(findings)
}

// TUIHealthCount counts the findings of a group, on its line.
func TUIHealthCount(n int) string {
	if n == 1 {
		return "1 hallazgo"
	}
	return fmt.Sprintf("%d hallazgos", n)
}

// healthRows are the words of a finding on its row, under the skill it
// affects: short, since the skill is named on the line above.
var healthRows = map[string]string{
	"broken_link":               "enlace roto",
	"link_cycle":                "enlace en ciclo",
	"link_outside_roots":        "enlace a otra carpeta",
	"identical_copies":          "copias idénticas",
	"no_canonical":              "copias sin original",
	"unmanaged_skill":           "sin gestionar",
	"lock_entry_without_folder": "registro sin carpeta",
	"lock_name_mismatch":        "nombre distinto",
	"parked_but_visible":        "apagada pero visible",
	"homonyms":                  "nombre repetido",
}

// TUIHealthRow names a finding of kind on its row under its skill.
func TUIHealthRow(kind string) string {
	if s, ok := healthRows[kind]; ok {
		return s
	}
	return FindingKind(kind)
}

// healthOne tells one finding of each kind in the singular, as findingGroups
// tells a group of them: what the problem is and the suggested fix.
var healthOne = map[string]struct{ what, fix string }{
	"broken_link":               {"apunta a una skill que ya no existe", "borrar el enlace"},
	"link_cycle":                {"forma un ciclo y no lleva a ninguna skill", "ninguno, skillhub solo lo informa"},
	"link_outside_roots":        {"lleva fuera de las carpetas de skills conocidas", "ninguno, skillhub no lo sigue"},
	"identical_copies":          {"la misma skill está en varios lugares, con el mismo contenido", "dejar una sola copia y enlazar las demás"},
	"no_canonical":              {"está copiada en varios agentes, pero no en el almacén de skillhub", "elegir la copia que se queda y enlazar las demás"},
	"unmanaged_skill":           {"está copiada a mano, fuera del almacén de skillhub", "pasarla a la gestión de skillhub"},
	"lock_entry_without_folder": {"npx skills recuerda una skill cuya carpeta ya no está", "reinstalarla o desactivar su registro"},
	"lock_name_mismatch":        {"el nombre que guarda npx skills no coincide con el de la skill", "ninguno, skillhub solo lo informa"},
	"parked_but_visible":        {"está en OFF/, pero algún agente todavía la ve", "desactivarla con skillhub"},
	"homonyms":                  {"hay otra skill distinta que se llama igual", "ninguno, skillhub nunca las une por su nombre"},
}

// TUIHealthFindings is the plain line of n findings of kind: how many, what
// the problem is and the suggested fix, as TUIFindingGroup tells a group,
// but in the singular when n is 1.
func TUIHealthFindings(kind string, n int) string {
	if n != 1 {
		return TUIFindingGroup(kind, n)
	}
	g, ok := findingGroups[kind]
	one, known := healthOne[kind]
	if !ok || !known {
		return fmt.Sprintf("1 hallazgo de tipo «%s».", FindingKind(kind))
	}
	return fmt.Sprintf("1 %s: %s. Arreglo sugerido: %s.", g.one, one.what, one.fix)
}

// TUIHealthNoSkill heads the group of the findings of kind that name no
// skill.
func TUIHealthNoSkill(kind string) string {
	title := FindingKind(kind)
	if g, ok := findingGroups[kind]; ok {
		title = g.title
	}
	return title + " sin skill"
}

// TUIHealthAlsoIn names the other skills a finding is listed under (A31):
// it is one finding, whose repair is one plan.
func TUIHealthAlsoIn(names []string) string {
	return "También aparece en " + JoinNames(names) + ": es el mismo hallazgo, no uno por cada skill."
}

// TUIHealthAt tells apart a skill whose name another affected skill shares,
// by where it is.
func TUIHealthAt(name, where string) string {
	return fmt.Sprintf("%s (%s)", SafeLine(name), SafeLine(where))
}
