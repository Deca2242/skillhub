package text

import (
	"errors"
	"fmt"
	"strings"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
)

// The texts of the version 2 operations (docs/design-v2.md sections 9 and
// 10): plans, refusals, results and help, in neutral Spanish (S11). The CLI
// prints them as they are; the TUI reuses the same functions, so both entry
// points tell the user the same thing. Every value they interpolate (a
// name, a path, a link target, a process, an identifier, a diff) goes
// through SafeLine, Safe or SafeDiff (A28): a plan cannot hide what it does.

// Help of the version 2 commands.
const (
	SkillsShort   = "Instala, elimina, activa, desactiva y enlaza skills administradas"
	InstallUse    = "install <origen> [--to <harness>...] [--dry-run] [--yes --plan <id>]"
	InstallShort  = "Instala una skill desde GitHub o desde un sitio web tras revisarla"
	InstallLong   = "Resuelve el origen, de GitHub (owner/repo, owner/repo@skill, owner/repo/ruta o la URL github.com/o/r/tree/ref/ruta) o de un sitio web que publica sus skills en /.well-known/agent-skills/ (https://sitio@skill, por ejemplo https://uizze.sh@ui-taste; sin @skill, el sitio debe publicar una sola), prepara la skill y muestra su revisión completa: archivos, etiquetas de riesgo y diff contra una carpeta vacía. Después muestra el plan (mover la copia revisada al almacén, crear los enlaces y añadir la entrada del lock) y pide confirmación. Sin --to, los destinos son los harness detectados que se proponen; es una propuesta que apruebas con el plan, nunca un permiso automático. Un sitio web se lee solo por https, con curl y con límites de tamaño; las actualizaciones de sus skills todavía no se revisan."
	RemoveUse     = "remove <skill> [--allow-provider] [--dry-run] [--yes --plan <id>]"
	RemoveShort   = "Elimina una skill independiente (global o del proyecto actual)"
	RemoveLong    = "Quita los enlaces que apuntan a la carpeta elegida, borra esa carpeta y, cuando corresponde, quita su entrada del lock. Acepta una clave del almacén o aparcada (<clave>), o un identificador exacto: dir:<ruta> (carpeta global directa en el almacén, en OFF/ o en el directorio de un agente), lock:<nombre>, parked:<clave>, project:.agents/skills/<nombre> o project:.claude/skills/<nombre>. Por defecto rechaza zonas de proveedor; con --allow-provider permite planificar la eliminación reversible de skills en zonas de proveedor confirmadas (gentle-ai, plugins o synced), mostrando un aviso explícito de que solo borra archivos del disco sin actualizar el estado ni el manifiesto del proveedor. Al eliminar una carpeta real del proyecto actual (project:.agents/skills/<nombre> o project:.claude/skills/<nombre>), también quita los enlaces simbólicos directos del mismo proyecto que apuntan a esa carpeta para no dejar enlaces rotos; elegir directamente un enlace sigue rechazándose. Nunca elimina varias copias por nombre ni toca clones de git, enlaces simbólicos elegidos directamente, otros proyectos, _shared/ ni perfiles. Todo queda en un snapshot y se deshace con «skillhub undo» (una skill de proyecto requiere ejecutarlo en ese mismo proyecto y restaura tanto la carpeta como esos enlaces). Las copias con el mismo nombre en otros ámbitos o agentes no se tocan: se informan. Una actualización pendiente de la skill pasa a reemplazada."
	DisableUse    = "disable <skill> [--dry-run] [--yes --plan <id>]"
	DisableShort  = "Desactiva una skill en todos sus destinos gestionados"
	DisableLong   = "Quita sus enlaces, aparca su carpeta en ~/.agents/.skillhub/disabled y aparca su entrada del lock, para que «npx skills update» no la reinstale. Un harness que tiene una copia propia la sigue viendo, y el plan lo avisa."
	EnableUse     = "enable <skill> [--dry-run] [--yes --plan <id>]"
	EnableShort   = "Activa una skill desactivada, con los enlaces que tenía"
	UnlinkUse     = "unlink <skill> --from <harness> [--dry-run] [--yes --plan <id>]"
	UnlinkShort   = "Quita el enlace de una skill en un harness"
	UnlinkLong    = "Solo funciona donde la ruta es administrable. Si el harness sigue viendo la skill (por ejemplo, porque lee el almacén directamente), el plan lo avisa."
	LinkUse       = "link <skill> --to <harness> [--dry-run] [--yes --plan <id>]"
	LinkShort     = "Enlaza una skill en un harness"
	AdoptOffUse   = "adopt-off [--dry-run] [--yes --plan <id>]"
	AdoptOffShort = "Adopta como desactivadas las skills de ~/.agents/skills/OFF/"
	AdoptOffLong  = "Las aparca fuera de todo directorio que lea un harness, quita los enlaces que llevan a ellas y aparca sus entradas del lock. OFF/README.md no se toca."
	DoctorUse     = "doctor"
	DoctorShort   = "Lista los enlaces rotos, los duplicados y las copias divergentes"
	DoctorLong    = "Lista los hallazgos: enlaces rotos, copias idénticas o divergentes, skills fuera del almacén y entradas del lock sin carpeta. Solo lee: no cambia nada. Repara un hallazgo con «skillhub doctor fix <hallazgo>», que muestra su plan o explica por qué no tiene reparación."
	FixUse        = "fix <hallazgo> [--repair <tipo>] [--canonical <ruta>] [--to <harness>] [--dry-run] [--yes --plan <id>]"
	FixShort      = "Repara un hallazgo de Doctor tras tu confirmación"
	FixLong       = "Sin --repair, aplica la única reparación que admite el hallazgo; si admite varias, indica una con --repair. Adoptar copias divergentes necesita que elijas la canónica con --canonical. Reinstalar una entrada del lock sin carpeta (--repair reinstall) muestra primero la revisión de su origen en GitHub, como «skills install», y después el plan que reemplaza la entrada; --to elige sus destinos."

	FlagYes           = "aprueba sin preguntar el plan cuyo identificador indica --plan"
	FlagPlan          = "identificador (`id`) del plan que viste y apruebas; se usa con --yes"
	FlagDryRun        = "muestra el plan sin cambiar nada; se niega, como la ejecución, si hay una operación interrumpida"
	FlagAllowProvider = "permite planificar la eliminación reversible de una skill en zona de proveedor (avisa el riesgo antes de confirmar)"
	FlagTargets       = "`harness` de destino; se puede repetir o separar con comas (por defecto, los detectados que se proponen)"
	FlagFrom          = "`harness` del que se quita el enlace"
	FlagLinkTo        = "`harness` donde se crea el enlace"
	FlagRepair        = "`tipo` de reparación que se aplica, si el hallazgo admite varias"
	FlagCanonical     = "`ruta` de la carpeta canónica; solo con la reparación adopt"
	FlagFixTo         = "`harness` de destino de una reinstalación; se puede repetir o separar con comas (por defecto, los detectados que se proponen)"
	UndoLong          = "Deshace una actualización aplicada: <skill> es su nombre o su id, y --to elige el snapshot. Para deshacer una operación de gestión (instalar, eliminar, activar, desactivar, enlazar, quitar un enlace o reparar), <skill> es su línea del historial, por ejemplo v2:disable, y --to su snapshot; sin --to, la última operación elegible. Deshacer una operación de gestión muestra su plan y pide confirmación, o se aprueba con --yes --plan <id>."
	FlagUndoPlan      = "identificador (`id`) del plan de deshacer que viste (operaciones de gestión, con --yes)"
	FlagUndoYes       = "aprueba sin preguntar el deshacer cuyo identificador indica --plan (operaciones de gestión)"
)

// Messages of the approval flow.
const (
	DryRun           = "Simulación: no se cambió nada.\n"
	ConfirmPlan      = "¿Aplicar este plan?"
	YesNeedsPlan     = "--yes necesita --plan <id>, el identificador de un plan que viste: ejecuta el mismo comando con --dry-run para ver el plan y su identificador, y vuelve a ejecutarlo con --yes --plan <id>; no se cambió nada"
	PlanWithoutYes   = "--plan solo se usa junto con --yes"
	ToNeedsReinstall = "--to solo se usa con la reparación reinstall; no se cambió nada"
	NeedsTerminal    = "sin una terminal, aprueba el plan con --yes --plan <id>; no se cambió nada"
	NotConfirmed     = "no se confirmó; no se cambió nada"
	NoFindings       = "Doctor no encontró nada que informar.\n"
	FixHint          = "Repara uno con «skillhub doctor fix <hallazgo>»: muestra su plan y pide confirmación, o explica por qué no tiene reparación.\n"
	NoPlanToApply    = "esta acción no tiene un plan que aplicar; no se cambió nada"
	// V1StillPending follows a version 2 recover when a version 1
	// operation is interrupted too: the next recover resolves it.
	V1StillPending = "Queda además una operación interrumpida de una actualización: vuelve a ejecutar «skillhub recover».\n"
	// UndoFlagsV2Only refuses --yes and --plan on the undo of an update:
	// they approve only the undo plans of version 2 operations.
	UndoFlagsV2Only = "--yes y --plan solo se usan para deshacer operaciones de gestión (v2:...); no se cambió nada"
	// ManagedPendingPlan is what recover --dry-run says while a version 2
	// operation record is pending (A5 of L51).
	ManagedPendingPlan = "Hay una operación de gestión sin terminar; «skillhub recover» la resuelve: la revierte si no llegó a completarse o termina de cerrarla si ya se había completado.\n"
	// NewPermissions explains why every install carries new_permissions
	// (L39).
	NewPermissions = "Toda instalación lleva la etiqueta «permisos nuevos»: la skill entera es nueva en esta máquina, así que cada archivo cuenta como un permiso que no tenía.\n"
)

// Texts of the multi-skill install picker in the TUI (S11).
const (
	TUIInstallPickTitle  = "El origen contiene varias skills"
	TUIInstallPickPrompt = "Elige la skill que quieres revisar antes de instalar:"
	TUIKeysInstallPick   = "↑/↓ elegir · enter revisar · esc cancelar"
)

// PlanChanged explains engine and app ErrPlanChanged: the approved plan is
// not the one planning gives now, and the new one never runs by itself.
func PlanChanged(now string) string {
	return fmt.Sprintf("el plan cambió desde que lo viste (ahora es %s); no se ejecutó nada. Revisa el plan nuevo y apruébalo si sigue siendo lo que quieres", SafeLine(now))
}

// NeedsHarness refuses unlink or link without the harness flag.
func NeedsHarness(command, flag string) string {
	return fmt.Sprintf("«%s» necesita --%s <harness>", command, flag)
}

// UndoLineNotFound is the refusal of undo for a version 2 line that is not
// in the journal.
func UndoLineNotFound(target, snapshot string) string {
	if snapshot == "" {
		return fmt.Sprintf("no hay ninguna operación %s en el historial", SafeLine(target))
	}
	return fmt.Sprintf("no hay ninguna operación %s con el snapshot «%s»", SafeLine(target), SafeLine(snapshot))
}

// SupersedeWarning is the warning of A4 of L51: the operation ran and is
// undoable, but what came after it failed. The cause stays as a detail.
func SupersedeWarning(err error) string {
	if errors.Is(err, app.ErrCandidatesNotSuperseded) {
		cause := strings.TrimPrefix(err.Error(), app.ErrCandidatesNotSuperseded.Error()+": ")
		return fmt.Sprintf("aviso: la skill se eliminó y se puede deshacer, pero sus actualizaciones pendientes no se pudieron marcar como reemplazadas; siguen listadas como pendientes (detalle: %s)\n", Safe(cause))
	}
	return fmt.Sprintf("aviso: la operación se completó y se puede deshacer, pero falló lo que venía después (detalle: %s)\n", Safe(err.Error()))
}

// ClosePending explains an operation whose engine close did not finish,
// distinguishing a confirmed journal commit from an unreadable journal where
// commit could not be confirmed.
func ClosePending(committed bool, snapshotID string, err error) string {
	detail := ""
	if err != nil {
		detail = SafeLine(err.Error())
	}
	suffix := ""
	if detail != "" {
		suffix = fmt.Sprintf(" (detalle: %s)", detail)
	}
	if committed {
		if snapshotID != "" {
			return fmt.Sprintf("la operación ya quedó confirmada en el diario (snapshot %s), pero su cierre quedó pendiente; ejecuta «skillhub recover» para terminarlo%s", SafeLine(snapshotID), suffix)
		}
		return fmt.Sprintf("la operación ya quedó confirmada en el diario, pero su cierre quedó pendiente; ejecuta «skillhub recover» para terminarlo%s", suffix)
	}
	return fmt.Sprintf("no se pudo confirmar si la operación quedó registrada en el diario y su cierre quedó pendiente; no se revirtió nada: ejecuta «skillhub recover» para comprobarlo y cerrarla%s", suffix)
}

// RecoverFailed is what recover says when it could not resolve the
// interrupted operation and the cause is no refusal; the cause stays as a
// detail.
func RecoverFailed(err error) string {
	return fmt.Sprintf("no se pudo resolver la operación interrumpida; vuelve a ejecutar «skillhub recover» y, si se repite, revisa el registro de la operación (detalle: %s)", Safe(err.Error()))
}

var stepKinds = map[string]string{
	"move":           "mover %[1]s a %[2]s",
	"link":           "crear el enlace %[3]s → %[4]s",
	"unlink":         "quitar el enlace %[3]s (→ %[4]s)",
	"lockPark":       "aparcar la entrada «%[5]s» del lock",
	"lockUnpark":     "devolver la entrada «%[5]s» al lock, en su posición",
	"lockAdd":        "añadir la entrada «%[5]s» al lock",
	"lockRemove":     "quitar la entrada «%[5]s» del lock",
	"lockReplace":    "reemplazar la entrada «%[5]s» del lock, en su posición, por la revisada",
	"lockRemoveLast": "quitar la entrada «%[5]s», la última, y borrar el lock que queda vacío",
	"removeTree":     "borrar %[3]s (queda en el snapshot)",
	"restoreTree":    "restaurar %[3]s desde el snapshot",
	"removeClone":    "borrar el clon %[3]s (queda en el snapshot)",
	"restoreClone":   "restaurar el clon %[3]s desde el snapshot",
	"require":        "comprobar que %[3]s sigue igual (no se toca)",
}

// Step describes one plan step.
func Step(s app.Step) string {
	src, dst, path, target, name := SafeLine(s.Src), SafeLine(s.Dst), SafeLine(s.Path), SafeLine(s.Target), SafeLine(s.Name)
	f, ok := stepKinds[s.Kind]
	if !ok {
		return fmt.Sprintf("%s %s%s%s%s", SafeLine(s.Kind), src, dst, path, name)
	}
	return fmt.Sprintf(f, src, dst, path, target, name)
}

var sightingKinds = map[string]string{
	"direct":        "lo lee directamente",
	"link":          "por un enlace",
	"copy":          "por una copia propia",
	"indeterminate": "quizá: no se verificó cómo lee esa carpeta",
}

// Sighting describes how a harness sees a skill.
func Sighting(s app.Sighting) string {
	return fmt.Sprintf("%s: %s (%s)", SafeLine(s.Harness), lookup(sightingKinds, s.Kind), SafeLine(s.Entry))
}

var findingKinds = map[string]string{
	"broken_link":               "enlace roto",
	"link_cycle":                "enlace en ciclo",
	"link_outside_roots":        "enlace fuera de las raíces conocidas",
	"identical_copies":          "copias idénticas",
	"no_canonical":              "copias sin canónica en el almacén",
	"unmanaged_skill":           "skill no administrada",
	"lock_entry_without_folder": "entrada del lock sin carpeta",
	"lock_name_mismatch":        "clave del lock distinta del nombre",
	"parked_but_visible":        "desactivada en OFF/ pero visible",
	"homonyms":                  "homónimos",
	"managed_by_gentle_ai":      "administrada por gentle-ai",
}

// FindingKind is the Spanish name of a Doctor finding kind.
func FindingKind(k string) string { return lookup(findingKinds, k) }

var repairKinds = map[string]string{
	"delete_link":     "borrar el enlace",
	"repoint_link":    "reapuntar el enlace",
	"enable_and_link": "activar y enlazar",
	"adopt_off":       "adoptar las desactivadas de OFF/",
	"adopt":           "adoptar al almacén",
	"park_entry":      "aparcar la entrada del lock",
	"reinstall":       "reinstalar desde el origen",
}

// RepairKind is the Spanish name of a repair.
func RepairKind(k string) string { return lookup(repairKinds, k) }

var offerReasons = map[string]string{
	"managed_by_gentle_ai":   "gentle-ai administra esta copia: «gentle-ai sync» la volvería a crear, así que solo se informa",
	"homonyms":               "skillhub nunca une skills por su nombre; solo se informa",
	"lock_name_mismatch":     "la clave es la de npx skills; cambiarla no es una reparación",
	"link_cycle":             "skillhub no sigue enlaces en ciclo; solo se informa",
	"link_outside_roots":     "el enlace sale de las raíces conocidas; skillhub no lo sigue",
	"diagnostic_profile":     "la ruta está en un perfil de diagnóstico de solo lectura",
	"foreign_zone":           "la ruta está en una zona que skillhub nunca toca",
	"canonical_not_in_store": "la carpeta canónica no está en el almacén (es un clon o está en OFF/); nunca se adopta",
	"ambiguous":              "la carpeta la comparte un alias u otra skill",
	"already_parked":         "la clave ya está desactivada",
}

// NoRepair explains why a finding gets no repair.
func NoRepair(o app.Offer) string {
	if o.Reason == "" {
		return "no hay ninguna reparación para este hallazgo"
	}
	return lookup(offerReasons, o.Reason)
}

// ChooseRepair is the refusal of doctor fix without --repair when the
// finding takes several.
func ChooseRepair(o app.Offer) string {
	return fmt.Sprintf("el hallazgo admite varias reparaciones; indica una con --repair: %s", repairList(o.Repairs))
}

func repairList(kinds []string) string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, fmt.Sprintf("%s (%s)", k, RepairKind(k)))
	}
	return strings.Join(out, ", ")
}

// Findings lists Doctor's findings. Listing them only reads; the repairs
// a finding takes are told by doctor fix, which reads it again.
func Findings(findings []app.Finding) string {
	if len(findings) == 0 {
		return NoFindings
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hallazgos: %d\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "\n%s\n  %s", SafeLine(f.ID), FindingKind(f.Kind))
		if f.Skill != "" {
			fmt.Fprintf(&b, " · %s", SafeLine(f.Skill))
		}
		b.WriteString("\n")
		for _, p := range f.Paths {
			fmt.Fprintf(&b, "  %s\n", SafeLine(p))
		}
		if f.Target != "" {
			fmt.Fprintf(&b, "  lleva a %s\n", SafeLine(f.Target))
		}
		if len(f.Harnesses) > 0 {
			fmt.Fprintf(&b, "  harness: %s\n", joinSafe(f.Harnesses))
		}
	}
	b.WriteString("\n" + FixHint)
	return b.String()
}

// Title is the first line of a plan.
func Title(p app.Proposal) string {
	r := p.Request
	key, harness := SafeLine(r.Key), SafeLine(r.Harness)
	switch r.Action {
	case app.ActionDisable:
		return fmt.Sprintf("Desactivar %s en todos sus destinos gestionados", key)
	case app.ActionEnable:
		return fmt.Sprintf("Activar %s", key)
	case app.ActionUnlink:
		return fmt.Sprintf("Quitar el enlace de %s en %s", key, harness)
	case app.ActionLink:
		return fmt.Sprintf("Enlazar %s en %s", key, harness)
	case app.ActionAdoptOff:
		return "Adoptar como desactivadas las skills de OFF/"
	case app.ActionRemove:
		if p.Remove != nil && p.Remove.Group {
			label := p.Remove.SkillName
			if label == "" {
				label = p.Remove.Key
			}
			return fmt.Sprintf("Eliminar por completo %s", SafeLine(label))
		}
		if p.Remove != nil && p.Remove.Key != "" {
			return fmt.Sprintf("Eliminar %s", SafeLine(p.Remove.Key))
		}
		return fmt.Sprintf("Eliminar %s", key)
	case app.ActionInstall:
		if p.Install != nil {
			return fmt.Sprintf("Instalar %s desde %s", SafeLine(p.Install.Key), installOrigin(p.Install.Source, p.Install.Site))
		}
		return "Instalar"
	case app.ActionReinstall:
		if p.Install != nil {
			return fmt.Sprintf("Reinstalar %s desde %s", SafeLine(p.Install.Key), installOrigin(p.Install.Source, p.Install.Site))
		}
		return "Reinstalar"
	case app.ActionRepair:
		return fmt.Sprintf("Reparar %s: %s", SafeLine(r.Finding), SafeLine(RepairKind(r.Repair)))
	case app.ActionUndo:
		if p.Undo != nil {
			return fmt.Sprintf("Se desharía la operación %s del snapshot %s", SafeLine(p.Undo.Of), SafeLine(p.Undo.Snapshot))
		}
		return "Deshacer"
	}
	return SafeLine(string(r.Action))
}

// Proposal is a plan as the user approves it: what it does, step by step,
// what it leaves visible or broken, the agents open now and the ones to
// reload, and the identifier the approval binds.
func Proposal(p app.Proposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan: %s\n", Title(p))
	switch {
	case p.Toggle != nil:
		writeToggle(&b, p.Toggle, p.Request.Action == app.ActionEnable)
	case p.Remove != nil:
		writeRemove(&b, p.Remove)
	case p.Install != nil:
		writeInstall(&b, p.Install)
	case p.Repair != nil:
		writeRepair(&b, p.Repair)
	}
	if len(p.Targets) > 0 {
		fmt.Fprintf(&b, "Destinos: %s\n", joinSafe(p.Targets))
	}
	if len(p.Steps) > 0 {
		b.WriteString("Pasos:\n")
		for i, s := range p.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, Step(s))
		}
	}
	if len(p.Running) > 0 {
		b.WriteString("Agentes abiertos ahora (pueden tener la skill cargada; no se bloquea nada):\n")
		for _, r := range p.Running {
			fmt.Fprintf(&b, "  %s (proceso %d, %s)\n", SafeLine(r.Harness), r.PID, SafeLine(r.Command))
		}
	}
	if len(p.Reload) > 0 {
		fmt.Fprintf(&b, "Después, recarga las skills en: %s\n", joinSafe(p.Reload))
	}
	return b.String()
}

// PlanID tells the identifier the approval binds and how to approve it
// without a question.
func PlanID(id string) string {
	id = SafeLine(id)
	return fmt.Sprintf("Identificador del plan: %s\nPara aprobarlo sin preguntar, vuelve a ejecutar el comando con --yes --plan %s\n", id, id)
}

func writeLinks(b *strings.Builder, title string, links []app.LinkChange) {
	if len(links) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, l := range links {
		fmt.Fprintf(b, "  %s → %s", SafeLine(l.Path), SafeLine(l.Target))
		if len(l.Harnesses) > 0 {
			fmt.Fprintf(b, " (%s)", joinSafe(l.Harnesses))
		}
		if l.Broken {
			b.WriteString(" [roto]")
		}
		b.WriteString("\n")
	}
}

func writeSightings(b *strings.Builder, title string, ss []app.Sighting) {
	if len(ss) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, s := range ss {
		fmt.Fprintf(b, "  %s\n", Sighting(s))
	}
}

func writeList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, it := range items {
		fmt.Fprintf(b, "  %s\n", SafeLine(it))
	}
}

func writeToggle(b *strings.Builder, t *app.TogglePreview, enabling bool) {
	for _, s := range t.Skills {
		if len(t.Skills) > 1 {
			fmt.Fprintf(b, "Skill %s:\n", SafeLine(s.Key))
		}
		if s.From != "" {
			fmt.Fprintf(b, "Carpeta: %s → %s\n", SafeLine(s.From), SafeLine(s.To))
		}
		writeLinks(b, "Enlaces que se quitan", s.RemovedLinks)
		writeLinks(b, "Enlaces que se crean", s.CreatedLinks)
		switch {
		case s.LockEntry && enabling:
			b.WriteString("Su entrada del lock vuelve al lock, en la posición que tenía.\n")
		case s.LockEntry:
			b.WriteString("Su entrada del lock se aparca junto con la carpeta, para que «npx skills update» no la reinstale.\n")
		}
		if len(s.RestoreLinks) > 0 {
			b.WriteString("Enlaces que se recrearán al activarla:\n")
			for _, l := range s.RestoreLinks {
				fmt.Fprintf(b, "  %s → %s\n", SafeLine(l.Path), SafeLine(l.Target))
			}
		}
		if len(t.Skills) > 1 {
			writeSightings(b, "Siguen viendo la skill", s.StillVisible)
		}
	}
	if len(t.Skills) <= 1 {
		writeSightings(b, "Siguen viendo la skill (el plan no lo cambia)", t.StillVisible)
	}
	writeSightings(b, "Ya la ven antes de enlazar", t.Visible)
	if len(t.SharesDirWith) > 0 {
		fmt.Fprintf(b, "Comparten ese directorio, y también ven el cambio: %s\n", joinSafe(t.SharesDirWith))
	}
	writeList(b, "No se tocan", t.Kept)
}

var modified = map[string]string{
	"no":      "no",
	"yes":     "sí: la carpeta difiere de lo que registra el lock; tus cambios quedan en el snapshot",
	"unknown": "no se sabe: no hay un hash registrado con qué comparar",
}

func keptCopyReason(c app.KeptCopy) string {
	switch c.Reason {
	case "diagnostic_profile":
		if c.Profile != "" {
			return "perfil " + c.Profile + " (solo lectura)"
		}
		return "perfil de diagnóstico (solo lectura)"
	case "git":
		return "clon o repositorio de git (protegido)"
	case "foreign_zone":
		return "zona protegida que skillhub no toca"
	case "outside_global_root":
		return "fuera de las carpetas globales de skills"
	case "ambiguous":
		return "reclamada por otra skill o entrada"
	case "alias":
		return "enlace simbólico, no carpeta real"
	default:
		if c.Reason != "" {
			return c.Reason
		}
		return "excluida por seguridad"
	}
}

func providerLabel(provider string) string {
	switch provider {
	case app.ProviderClaudePlugin:
		return "Claude Code (plugin)"
	case app.ProviderGentleAI:
		return "gentle-ai"
	case app.ProviderClaudeSynced:
		return "Claude Code (skills sincronizadas)"
	case app.ProviderCursor:
		return "Cursor"
	case app.ProviderCodex:
		return "Codex"
	default:
		return SafeLine(provider)
	}
}

func writeRemove(b *strings.Builder, r *app.RemovePreview) {
	if r.Provider != "" {
		fmt.Fprintf(b, "Aviso de proveedor (%s): borrarla solo elimina los archivos del disco, sin actualizar el estado ni el manifiesto del proveedor, por lo que el proveedor podría volver a crearla o dejar un plugin incompleto. La operación queda en el snapshot y se puede deshacer con «skillhub undo».\n", providerLabel(r.Provider))
	}
	if r.Group {
		for _, f := range r.Folders {
			state := "activa"
			if f.Off {
				state = "desactivada a mano en OFF/"
			}
			fmt.Fprintf(b, "Carpeta (%s): %s (árbol %s)\n", state, SafeLine(f.Path), SafeLine(short(f.TreeSHA)))
		}
		if len(r.Targets) > 0 {
			fmt.Fprintf(b, "Agentes que dejan de detectarla: %s\n", joinSafe(r.Targets))
		} else {
			b.WriteString("Agentes que dejan de detectarla: ninguno\n")
		}
	} else {
		state := "activa"
		switch {
		case r.Project:
			state = "proyecto (" + SafeLine(r.ProjectRoot) + ")"
		case r.Parked:
			state = "desactivada"
		case r.Off:
			state = "desactivada a mano en OFF/"
		}
		if r.Folder == "" {
			// A parked record whose folder was already gone (park_entry).
			b.WriteString("Sin carpeta: solo queda su registro desactivado.\n")
		} else if r.Clone {
			fmt.Fprintf(b, "Carpeta (clon completo): %s (clon %s)\n", SafeLine(r.Folder), SafeLine(short(r.TreeSHA)))
			if len(r.CloneSkills) > 0 {
				fmt.Fprintf(b, "Skills del clon que se eliminan juntas: %s\n", joinSafe(r.CloneSkills))
			}
		} else {
			fmt.Fprintf(b, "Carpeta (%s): %s (árbol %s)\n", state, SafeLine(r.Folder), SafeLine(short(r.TreeSHA)))
		}
	}
	if r.Modified != "" {
		fmt.Fprintf(b, "Modificada localmente: %s\n", SafeLine(lookup(modified, r.Modified)))
	}
	writeLinks(b, "Enlaces que se quitan", r.Links)
	if r.Project && len(r.Links) > 0 {
		b.WriteString("También se quitan los enlaces simbólicos del proyecto que apuntan a esa carpeta para no dejar enlaces rotos; todo se restaura con «skillhub undo».\n")
	}
	switch {
	case r.LockEntry != nil && r.LockEntry.DeletesLock:
		fmt.Fprintf(b, "Entrada del lock que se quita: «%s» (era la última: se borra el lock vacío)\n", SafeLine(r.LockEntry.Name))
	case r.LockEntry != nil:
		fmt.Fprintf(b, "Entrada del lock que se quita: «%s»\n", SafeLine(r.LockEntry.Name))
	case r.ParkedEntry != nil:
		b.WriteString("Su entrada del lock aparcada se descarta (queda en el snapshot).\n")
	}
	if len(r.KeptCopies) > 0 {
		b.WriteString("Copias protegidas o excluidas que se conservan:\n")
		for _, c := range r.KeptCopies {
			fmt.Fprintf(b, "  %s (%s)\n", SafeLine(c.Path), SafeLine(keptCopyReason(c)))
		}
	}
	if len(r.Homonyms) > 0 {
		b.WriteString("Otras copias con el mismo nombre (no se tocan; se resuelven con «skillhub doctor»):\n")
		for _, h := range r.Homonyms {
			same := "distinta"
			if h.Identical {
				same = "idéntica"
			}
			where := joinSafe(h.Harnesses)
			if h.Profile != "" {
				where = "perfil " + SafeLine(h.Profile)
			}
			fmt.Fprintf(b, "  %s (%s, %s)\n", SafeLine(h.Path), same, where)
		}
	}
	if len(r.KeptLinks) > 0 {
		b.WriteString("Enlaces de zonas que skillhub no toca y que quedarán rotos:\n")
		for _, k := range r.KeptLinks {
			where := SafeLine(k.Zone)
			if k.Profile != "" {
				where = "perfil " + SafeLine(k.Profile)
			}
			fmt.Fprintf(b, "  %s → %s (%s)\n", SafeLine(k.Path), SafeLine(k.Target), where)
		}
	}
	writeList(b, "Actualizaciones pendientes que pasarán a reemplazadas", r.PendingCandidates)
}

func writeInstall(b *strings.Builder, in *app.InstallPreview) {
	if in.Site != "" {
		fmt.Fprintf(b, "Origen: %s, %s\n", installOrigin(in.Source, in.Site), SafeLine(in.SourceURL))
		b.WriteString(WebUpdatesLater + "\n")
	} else {
		ref := "sigue la rama por defecto"
		if !in.FollowsDefaultBranch {
			ref = "ref fijado"
		}
		fmt.Fprintf(b, "Origen: %s, commit %s, %s (%s)\n", SafeLine(in.Source), SafeLine(short(in.Commit)), SafeLine(in.SkillPath), ref)
	}
	fmt.Fprintf(b, "Copia revisada: %s (árbol %s) → %s\n", SafeLine(in.StagedPath), SafeLine(short(in.TreeSHA)), SafeLine(in.StorePath))
	writeLinks(b, "Enlaces que se crean", in.Links)
	if in.LockCreated {
		b.WriteString("El lock de skills no existe: se crea con esta entrada.\n")
	}
	if len(in.ReplacedEntry) > 0 {
		fmt.Fprintf(b, "Entrada del lock que se reemplaza:\n%s\n", Safe(indent(string(in.ReplacedEntry))))
	}
	if len(in.LockEntry) > 0 {
		fmt.Fprintf(b, "Entrada del lock que se añade:\n%s\n", Safe(indent(string(in.LockEntry))))
	}
}

func writeRepair(b *strings.Builder, r *app.RepairPreview) {
	if r.Canonical != "" {
		fmt.Fprintf(b, "Carpeta canónica: %s\n", SafeLine(r.Canonical))
	}
	for _, m := range r.Moved {
		fmt.Fprintf(b, "Se mueve: %s → %s\n", SafeLine(m.From), SafeLine(m.To))
	}
	writeLinks(b, "Enlaces que se quitan", r.RemovedLinks)
	writeLinks(b, "Enlaces que se crean", r.CreatedLinks)
	if len(r.RemovedCopies) > 0 {
		b.WriteString("Copias que se quitan (quedan en el snapshot):\n")
		for _, c := range r.RemovedCopies {
			how := "se borra"
			if c.Replaced {
				how = "se reemplaza por un enlace"
			}
			fmt.Fprintf(b, "  %s (árbol %s, %s)\n", SafeLine(c.Path), SafeLine(short(c.TreeSHA)), how)
		}
	}
	for _, c := range r.Copies {
		if len(c.Added)+len(c.Removed)+len(c.Changed) == 0 {
			fmt.Fprintf(b, "Diferencias de %s con la canónica: sin diferencias\n", SafeLine(c.Path))
			continue
		}
		fmt.Fprintf(b, "Diferencias de %s con la canónica:\n", SafeLine(c.Path))
		for _, f := range c.Added {
			fmt.Fprintf(b, "  + %s\n", SafeLine(f))
		}
		for _, f := range c.Removed {
			fmt.Fprintf(b, "  - %s\n", SafeLine(f))
		}
		for _, f := range c.Changed {
			fmt.Fprintf(b, "  ~ %s\n", SafeLine(f))
		}
	}
	if r.LockEntry != "" {
		fmt.Fprintf(b, "Entrada del lock: «%s»\n", SafeLine(r.LockEntry))
	}
	writeList(b, "No se tocan", r.Kept)
	if r.Toggle != nil {
		writeToggle(b, r.Toggle, r.Repair == "enable_and_link")
	}
}

// WebUpdatesLater says that the updates of a website skill are not checked
// yet (S14, L84); check shows it as the outcome of such a skill.
const WebUpdatesLater = "origen web: la revisión de actualizaciones llegará después"

// isSiteInput reports whether an install source is a website rather than
// GitHub.
func isSiteInput(input string) bool {
	in := strings.ToLower(strings.TrimSpace(input))
	return strings.HasPrefix(in, "https://") && !strings.HasPrefix(in, "https://github.com/") &&
		!strings.HasPrefix(in, "https://www.github.com/")
}

// installOrigin names where an install comes from: the owner/repo of
// GitHub, or «sitio web <host> (no es GitHub)» for a website.
func installOrigin(source, site string) string {
	if site == "" {
		return SafeLine(source)
	}
	return fmt.Sprintf("sitio web %s (no es GitHub)", SafeLine(source))
}

// Reinstall is what doctor fix says for the reinstall repair before its
// review: the repair has no plan of its own and goes through the install
// review of the entry's source, then a plan that replaces the entry.
func Reinstall(r *app.Reinstall) string {
	if r.Input == "" {
		return fmt.Sprintf("La entrada «%s» no viene de GitHub (%s %s): skillhub no puede reinstalarla. No se cambió nada.\n",
			SafeLine(r.Key), SafeLine(r.SourceType), SafeLine(r.Source))
	}
	return fmt.Sprintf("Reinstalar «%s» es instalarla de nuevo desde su origen, %s: primero se revisa la revisión que se instalaría y después el plan que reemplaza su entrada del lock.\n",
		SafeLine(r.Key), SafeLine(r.Input))
}

// InstallReview is the complete review of a prepared install: the files,
// the risk labels and the diff of each file against nothing. It is what
// the plan identifier covers (A2 of L51).
func InstallReview(rev app.Revision, diffs []app.FileDiff) string {
	var b strings.Builder
	key, tree := SafeLine(rev.Prepared.Resolved.Key), SafeLine(short(rev.Prepared.TreeSHA))
	if rev.Site != "" {
		fmt.Fprintf(&b, "Revisión de %s (%s, árbol %s)\n", key, installOrigin(rev.Repo, rev.Site), tree)
	} else {
		fmt.Fprintf(&b, "Revisión de %s (%s, commit %s, árbol %s)\n", key, SafeLine(rev.Input), SafeLine(short(rev.Commit)), tree)
	}
	fmt.Fprintf(&b, "Etiquetas: %s\n", labelList(rev.Prepared.Labels))
	b.WriteString(NewPermissions)
	b.WriteString("Archivos:\n")
	for _, fc := range rev.Prepared.FileChanges {
		fmt.Fprintf(&b, "  %s\t%s %s\n", SafeLine(fc.Path), FileChangeKind(fc.Change), SafeLine(fc.NewMode))
	}
	if len(rev.Prepared.FrontmatterDelta.Changed) > 0 {
		fmt.Fprintf(&b, "Frontmatter: %s\n", joinSafe(rev.Prepared.FrontmatterDelta.Changed))
	}
	for _, d := range diffs {
		fmt.Fprintf(&b, "\n%s\n", SafeLine(d.Path))
		switch {
		case d.Binary:
			b.WriteString("(archivo binario: sin diff de texto)\n")
		case d.Unified == "":
			b.WriteString("(sin contenido de texto)\n")
		default:
			b.WriteString(SafeDiff(d.Path, d.Unified))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func labelList(labels []domain.Label) string {
	if len(labels) == 0 {
		return "ninguna"
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, Label(l))
	}
	return strings.Join(out, ", ")
}

// Done is what a plan that ran says: how to undo it, the candidates it
// superseded and the harnesses to reload.
func Done(res app.Result) string {
	var b strings.Builder
	b.WriteString("Hecho.\n")
	if len(res.Superseded) > 0 {
		fmt.Fprintf(&b, "Actualizaciones pendientes marcadas como reemplazadas: %s\n", joinSafe(res.Superseded))
	}
	if len(res.Proposal.Reload) > 0 {
		fmt.Fprintf(&b, "Recarga las skills en: %s (/reload-skills, o reinicia Gemini CLI y Qwen Code)\n", joinSafe(res.Proposal.Reload))
	}
	// An undo cannot itself be undone, so it offers no undo command.
	if res.SnapshotID != "" && res.Proposal.Operation != "v2:undo" {
		fmt.Fprintf(&b, "Para deshacerlo: skillhub undo %s --to %s\n", SafeLine(UndoTarget(res.Proposal)), SafeLine(res.SnapshotID))
	}
	return b.String()
}

// UndoTarget is the journal target of a plan's operation: what undo takes.
func UndoTarget(p app.Proposal) string { return p.Operation }

// Recovery tells what recover did with a version 2 operation.
func Recovery(r app.ManagedRecovery) string {
	switch {
	case !r.Found:
		return RecoverNoOp
	case r.Committed && r.Undo:
		return "La operación de deshacer interrumpida ya se había completado: se terminó de cerrar.\n"
	case r.Committed:
		return fmt.Sprintf("La operación de gestión interrumpida ya se había completado: se terminó de cerrar. Se deshace con su snapshot %s (búscala en «skillhub history»).\n", SafeLine(r.SnapshotID))
	default:
		return fmt.Sprintf("Se revirtió la operación de gestión interrumpida (plan %s): todo quedó como estaba antes de empezar.\n", SafeLine(r.PlanID))
	}
}

// Refusal explains a refusal of a version 2 operation, with its cause, for
// the request that got it. ok is false when err is not a refusal. Every
// refusal is one line, so the whole sentence is SafeLine: the keys, paths
// and harnesses it names print escaped.
func Refusal(err error, req app.Request) (msg string, ok bool) {
	var re *app.RefusalError
	if !errors.As(err, &re) {
		return "", false
	}
	for _, r := range refusalTexts {
		if errors.Is(re.Kind, r.kind) {
			msg := SafeLine(r.text(req, re.Related))
			if errors.Is(re.Kind, app.ErrAmbiguousSkill) {
				if choices := app.InstallChoices(err); len(choices) > 0 {
					var b strings.Builder
					b.WriteString(msg)
					for _, c := range choices {
						fmt.Fprintf(&b, "\n  - %s (%s)", SafeLine(c.Name), SafeLine(c.Source))
					}
					return b.String(), true
				}
			}
			return msg, true
		}
	}
	return "", false
}

type refusalText struct {
	kind error
	text func(req app.Request, related []string) string
}

func fixed(s string) func(app.Request, []string) string {
	return func(app.Request, []string) string { return s }
}

func withKey(f string) func(app.Request, []string) string {
	return func(req app.Request, _ []string) string { return fmt.Sprintf(f, req.Key) }
}

var refusalTexts = []refusalText{
	{app.ErrPlanChanged, fixed("el plan cambió desde que lo viste; no se ejecutó nada")},
	{app.ErrNoPlan, fixed("esta reparación no tiene un plan propio que aplicar")},
	{app.ErrInvalidRequest, fixed("la solicitud no es válida")},
	{app.ErrInvalidKey, withKey("«%s» no es un nombre de skill válido: es el nombre de su carpeta en el almacén, sin barras")},
	{app.ErrNotFound, withKey("no hay ninguna skill administrable «%s»; «skillhub scan» lista las skills y «skillhub doctor», las que están fuera del almacén")},
	{app.ErrForeignZone, fixed("la skill está en una zona que skillhub nunca toca (synced/, un plugin, .system, _shared/ o gentle-ai); solo se informa")},
	{app.ErrAmbiguous, withKey("«%s» corresponde a más de una skill; skillhub nunca elige una por su nombre")},
	{app.ErrAlias, withKey("«%s» es un alias de otra carpeta de skill; usa el nombre de la carpeta real")},
	{app.ErrChanged, fixed("la skill cambió mientras se observaba; vuelve a intentarlo")},
	{app.ErrAlreadyDisabled, withKey("«%s» ya está desactivada")},
	{app.ErrNotDisabled, withKey("«%s» no está desactivada")},
	{app.ErrKeyReinstalled, func(req app.Request, related []string) string {
		return fmt.Sprintf("mientras «%s» estaba desactivada se volvió a instalar una skill con la misma clave%s; elige cuál conservar: elimina primero la instalada, o conserva la instalada y descarta la desactivada", req.Key, inParens(related))
	}},
	{app.ErrKeyInUse, fixed("la clave ya está ocupada en el almacén")},
	{app.ErrKeyExists, func(req app.Request, related []string) string {
		return fmt.Sprintf("ya existe una skill con la clave «%s»%s; instalar nunca reemplaza una skill", installKey(req), inParens(related))
	}},
	{app.ErrUnknownHarness, func(req app.Request, _ []string) string {
		if req.Harness == "" {
			return "uno de los agentes indicados no es conocido; «skillhub scan» muestra los agentes"
		}
		return fmt.Sprintf("«%s» no es un agente conocido", req.Harness)
	}},
	{app.ErrNotAdministrable, fixed("ese agente no tiene un directorio de enlaces que skillhub pueda gestionar: lee el almacén directamente o no admite enlaces")},
	{app.ErrHarnessNotDetected, fixed("ese agente no está detectado en esta máquina")},
	{app.ErrNotLinked, func(req app.Request, _ []string) string {
		return fmt.Sprintf("%s no tiene un enlace a «%s»", req.Harness, req.Key)
	}},
	{app.ErrAlreadyLinked, func(req app.Request, _ []string) string {
		return fmt.Sprintf("%s ya enlaza «%s»", req.Harness, req.Key)
	}},
	{app.ErrPathTaken, func(req app.Request, _ []string) string {
		if k := installKey(req); k != "" {
			return fmt.Sprintf("ya existe algo en una ruta que el plan de «%s» necesita; skillhub no lo reemplaza", k)
		}
		return "ya existe algo en una ruta que el plan necesita; skillhub no lo reemplaza"
	}},
	{app.ErrParkingVisible, fixed("la zona de aparcamiento queda dentro de un directorio que lee un agente; desactivar no ocultaría la skill")},
	{app.ErrNothingToAdopt, fixed("no hay ninguna skill que adoptar en OFF/")},
	{app.ErrNeighbourParked, func(req app.Request, related []string) string {
		if len(related) == 0 {
			return fmt.Sprintf("«%s» no puede volver al lock: falta una entrada vecina que skillhub no guarda", req.Key)
		}
		cmds := make([]string, 0, len(related))
		for _, k := range related {
			cmds = append(cmds, "«skillhub skills enable "+k+"»")
		}
		return fmt.Sprintf("«%s» no puede volver todavía a su posición en el lock: su vecina %s también está desactivada, y las entradas vuelven en el orden inverso al que salieron. Activa primero %s con %s",
			req.Key, strings.Join(related, ", "), strings.Join(related, ", "), strings.Join(cmds, " y "))
	}},
	{app.ErrBadParkedRecord, fixed("el registro de la skill desactivada no coincide con su carpeta aparcada; no se toca nada")},
	{app.ErrNameMismatch, fixed("el name del frontmatter no coincide con la clave de la skill en el almacén; skillhub no instala una skill cuyo nombre y carpeta difieren")},
	{app.ErrPinnedRef, fixed("el origen fija un ref y una instalación nunca escribe un ref en el lock; indica el origen sin ref")},
	{app.ErrMissingDir, fixed("falta un directorio que la instalación necesita (~/.agents/skills o el directorio de enlaces de un destino)")},
	{app.ErrInvalidReview, fixed("la revisión no es una instalación preparada; vuelve a ejecutar el comando")},
	{app.ErrStagingChanged, fixed("la copia preparada cambió desde que se revisó; vuelve a ejecutar el comando para revisarla de nuevo")},
	{app.ErrUnsupportedSource, func(req app.Request, _ []string) string {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(installInput(req))), "http://") {
			return "un sitio web se lee solo por https: escribe https:// en lugar de http://"
		}
		return "origen no admitido: instalar acepta GitHub (owner/repo, owner/repo@skill, owner/repo/ruta o github.com/o/r/tree/ref/ruta) o un sitio web que publica /.well-known/agent-skills/ (https://sitio@skill)"
	}},
	{app.ErrNoSkill, fixed("no se encontró ninguna skill (SKILL.md) en ese origen")},
	{app.ErrAmbiguousSkill, func(req app.Request, _ []string) string {
		if isSiteInput(installInput(req)) {
			return "el sitio web publica varias skills; indica una con https://sitio@skill"
		}
		return "el origen contiene varias skills; indica una con owner/repo@skill o con su ruta"
	}},
	{app.ErrStale, fixed("el hallazgo cambió desde que se detectó o ya no existe; vuelve a ejecutar «skillhub doctor»")},
	{app.ErrNotOffered, fixed("esa reparación no se aplica a este hallazgo; «skillhub doctor» lista las que sí")},
	{app.ErrNeedsCanonical, fixed("esta reparación necesita que elijas la carpeta canónica con --canonical <ruta>")},
	{app.ErrBadCanonical, fixed("la carpeta indicada con --canonical no es una de las carpetas de la skill")},
	{app.ErrLockedSkill, fixed("la carpeta de una skill del lock nunca se reemplaza por una copia de origen desconocido")},
	{app.ErrLinkedCopy, fixed("hay enlaces que llevan a una carpeta que la reparación movería o borraría")},
	{app.ErrNoPlacement, fixed("un agente que no lee el almacén perdería la skill")},
	{app.ErrPrecondition, fixed("algo que el plan esperaba cambió; no se cambió nada. Vuelve a ver el plan")},
	{app.ErrForeignState, fixed("otro programa cambió algo durante la operación; se conservaron los dos estados y no se sobrescribió nada. Ejecuta «skillhub recover» para ver el diagnóstico")},
	{app.ErrOperationPending, fixed("hay una operación sin terminar; ejecuta «skillhub recover» antes de continuar")},
	{app.ErrChangedSince, fixed("algo cambió después de la operación; deshacer se niega para no pisar ese cambio")},
	{app.ErrNothingToUndo, fixed("no hay nada que deshacer")},
	{app.ErrUnsupportedTree, fixed("la carpeta no se puede guardar en un snapshot (por ejemplo, es un clon de git o pertenece a uno, o tiene archivos especiales o bits setuid)")},
	{app.ErrNoEntryToReinstall, func(req app.Request, _ []string) string {
		return fmt.Sprintf("el lock no tiene ninguna entrada «%s» que reinstalar; para una skill nueva, usa «skillhub skills install»", installKey(req))
	}},
	{app.ErrEntryPinsRef, func(req app.Request, _ []string) string {
		return fmt.Sprintf("la entrada «%s» del lock fija un ref, y skillhub nunca quita el ref de una entrada; no se reinstala", installKey(req))
	}},
	{app.ErrSourceMismatch, func(req app.Request, _ []string) string {
		repo := ""
		if req.Install != nil {
			repo = req.Install.Revision.Repo
		}
		return fmt.Sprintf("la entrada «%s» del lock registra otro origen que %s; reinstalar solo usa el origen que registra la entrada", installKey(req), repo)
	}},
	{app.ErrLinkedLinkDir, fixed("el directorio de enlaces está bajo un enlace simbólico cuya ruta real no se pudo leer; no se crea un enlace que quedaría roto")},
	{app.ErrCanonicalNotUsed, fixed("--canonical solo se usa con la reparación adopt, que elige la carpeta canónica; no se cambió nada")},
	{app.ErrRepoNotFound, func(req app.Request, _ []string) string {
		return fmt.Sprintf("el repositorio %s no existe en GitHub o no es visible con tu sesión de gh", installInput(req))
	}},
	{app.ErrRefNotFound, fixed("el ref o la ruta indicados no existen en el repositorio de GitHub")},
	{app.ErrRateLimited, fixed("se agotó el límite de uso de GitHub; vuelve a intentarlo más tarde")},
	{app.ErrNotAuthenticated, fixed("gh no tiene una sesión iniciada; ejecuta «gh auth login» y vuelve a intentarlo")},
	{app.ErrGitHubUnavailable, fixed("no se pudo consultar GitHub (¿sin conexión a la red, o gh falló?); no se cambió nada")},
	{app.ErrSiteUnavailable, fixed("no se pudo leer el sitio web (¿sin conexión a la red, sin curl, o el sitio respondió con un error?); no se cambió nada")},
	{app.ErrNoWellKnown, fixed("el sitio web no publica skills en /.well-known/agent-skills/ ni en /.well-known/skills/ (si indicaste una ruta, se busca solo bajo esa ruta); no se cambió nada")},
	{app.ErrWebTooLarge, fixed("la skill del sitio web supera los límites de descarga (cantidad de archivos o tamaño); no se instaló nada")},
	{app.ErrUnsafeWebFile, fixed("el índice del sitio web nombra un archivo que skillhub no descarga: una ruta no segura, otro sitio o algo fuera de la dirección indicada; no se instaló nada")},
	{app.ErrWebDigestMismatch, fixed("la skill descargada no coincide con el digest que publica el índice del sitio web; no se instaló nada")},
	{app.ErrWebArchive, fixed("el sitio web publica la skill como un archivo comprimido, que skillhub todavía no instala; no se cambió nada")},
	{app.ErrLockUnsupported, fixed("el lock de skills tiene una versión que skillhub no escribe (solo la 3, la de npx skills 1.9.0); no se cambió nada")},
	{app.ErrStagingFailed, fixed("no se pudo leer ni vaciar la copia preparada para instalar en ~/.agents/.skillhub/staging; revisa los permisos de esa carpeta y vuelve a intentarlo")},
	{app.ErrLockMalformed, fixed("el lock de skills está mal formado; skillhub no lo toca. Corrígelo o vuelve a generarlo con npx skills")},
	{app.ErrLockChanged, fixed("el lock de skills cambió mientras tanto (quizá por «npx skills»); no se cambió nada. Vuelve a ver el plan")},
	{app.ErrNoProject, fixed("no hay un proyecto actual válido desde el que eliminar una skill de proyecto")},
	{app.ErrProjectLinked, fixed("la raíz o la carpeta de la skill en el proyecto es un enlace simbólico; skillhub solo opera sobre carpetas reales del proyecto actual")},
	{app.ErrWrongProject, func(_ app.Request, related []string) string {
		if len(related) > 0 {
			return fmt.Sprintf("esta operación pertenece al proyecto %s; vuelve a abrir skillhub desde esa carpeta para continuarla o deshacerla", SafeLine(related[0]))
		}
		return "esta operación pertenece a otro proyecto; vuelve a abrir skillhub desde esa carpeta para continuarla o deshacerla"
	}},
	{app.ErrCrossDevice, fixed("la carpeta de la skill y la papelera de skillhub están en sistemas de archivos distintos; no se movió nada")},
}

// installKey is the skill key of an install or reinstall request.
func installKey(req app.Request) string {
	if req.Install != nil {
		return req.Install.Revision.Key
	}
	return req.Key
}

// installInput is the source the user gave an install or reinstall.
func installInput(req app.Request) string {
	if req.Install != nil {
		return req.Install.Revision.Input
	}
	return ""
}

func inParens(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return " (" + strings.Join(items, ", ") + ")"
}

// joinSafe joins the SafeLine of each item with commas.
func joinSafe(items []string) string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, SafeLine(it))
	}
	return strings.Join(out, ", ")
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
