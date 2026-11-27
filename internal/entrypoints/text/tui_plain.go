package text

import (
	"fmt"
	"strings"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
)

// The plain-language texts of the TUI (S13): the Skills panel, the summary
// every confirmation starts with and the message after an operation. They
// name agents by their readable name and never use the internal words
// (lock, harness, destino, canónica, aparcada); the technical detail keeps
// them and is shown only on request.

// AgentNamer gives the readable name of a harness id.
type AgentNamer func(id string) string

// Summary is the start of a confirmation: a question and two to four
// plain lines.
type Summary struct {
	Question string
	Lines    []string
}

// The keys of every confirmation, with the details hidden and shown.
const (
	TUIConfirmKeys     = "[s] Sí  [n] No  [v] Ver detalles"
	TUIConfirmKeysHide = "[s] Sí  [n] No  [v] Ocultar detalles"
	// TUIDetailsHelp is under the technical detail of a confirmation.
	TUIDetailsHelp = "Detalle técnico: ↑/↓ desplazar • v ocultar detalles"
)

// The short states of a row of the Skills list.
const (
	TUIRowUpToDate  = "al día"
	TUIRowUnchecked = "sin revisar"
	TUIRowUpdate    = "actualizar"
	TUIRowDisabled  = "desactivada"
	TUIRowManual    = "manual"
	TUIRowProvider  = "proveedor"
	TUIRowUnmanaged = "sin gestionar"
	TUIRowIgnored   = "ignorada"
)

var rowIssues = map[domain.Issue]string{
	domain.IssueLocallyModified:   "modificada",
	domain.IssueDuplicate:         "duplicada",
	domain.IssueDivergentCopy:     "copia distinta",
	domain.IssueBrokenLink:        "enlace roto",
	domain.IssueUntrackedBranch:   "sin seguimiento",
	domain.IssueInterruptedUpdate: "a medias",
	domain.IssueRemovedUpstream:   "ya no existe",
}

// TUIRowIssue is the short state of a row with the issue: short enough to
// fit the list at 80 columns.
func TUIRowIssue(v domain.Issue) string { return lookup(rowIssues, v) }

// The lines and actions of the Skills panel.
const (
	TUIPanelNoDescription = "Sin descripción."
	TUIPanelNowhere       = "ningún agente"
	TUIPanelNoOrigin      = "copiada a mano, sin origen registrado"
	TUIPanelDisabledBy    = "desactivada con skillhub"
	TUIPanelUnmanaged     = "skillhub no la gestiona: revísala en Salud (4)."
	TUIPanelIgnored       = "Ignorada: skillhub no la muestra ni la revisa."

	TUIActionDisable        = "[d] Desactivar"
	TUIActionRemove         = "[e] Eliminar"
	TUIActionRemoveComplete = "[e] Eliminar por completo"
	TUIActionEnable         = "[a] Activar"
	TUIActionDetail         = "[enter] Ver todo"
	TUIActionAdoptOff       = "[o] Desactivarla de verdad"
	TUIActionFixLink        = "[p] Arreglar el enlace roto"
	TUIActionDoctor         = "[4] Ver en Salud"
	TUIActionIgnore         = "[x] Ignorar"
	TUIActionUnignore       = "[x] Dejar de ignorar"

	TUIKeyDisable        = "d desactivar"
	TUIKeyRemove         = "e eliminar"
	TUIKeyRemoveComplete = "e eliminar por completo"
	TUIKeyEnable         = "a activar"
	TUIKeyDetail         = "enter ver todo"
	TUIKeyIgnore         = "x ignorar"
	TUIKeyUnignore       = "x dejar de ignorar"
	TUIKeyAdoptOff       = "o desactivarla de verdad"
	TUIKeyFixLink        = "p arreglar el enlace roto"
	TUIKeyDoctor         = "4 ver en Salud"
	TUIKeyHelp           = "? ayuda"
	TUIKeyQuit           = "q salir"
)

// TUIPanelAvailable names the agents that see the skill.
func TUIPanelAvailable(agents string) string { return "Disponible en: " + SafeLine(agents) }

// TUIPanelOrigin tells where the skill comes from.
func TUIPanelOrigin(origin string) string { return "Origen: " + SafeLine(origin) }

// TUIPanelReturnsTo names the agents a disabled skill returns to when it
// is enabled.
func TUIPanelReturnsTo(agents string) string {
	return "Al activarla vuelve a estar en " + SafeLine(agents) + "."
}

// TUIPanelDisabledSince tells since when a disabled skill is off.
func TUIPanelDisabledSince(when string) string {
	return fmt.Sprintf("Desactivada desde %s. Ningún agente la ve.", SafeLine(when))
}

// TUIPanelOffHand explains a skill moved to the store's OFF/ by hand, naming
// the agents that still see it (L83).
func TUIPanelOffHand(agents []string) string {
	switch len(agents) {
	case 0:
		return "La desactivaste a mano moviéndola a OFF/, y ningún agente la ve."
	case 1:
		return fmt.Sprintf("La desactivaste a mano moviéndola a OFF/, pero %s la sigue viendo porque busca en subcarpetas.", SafeLine(agents[0]))
	}
	return fmt.Sprintf("La desactivaste a mano moviéndola a OFF/, pero %s la siguen viendo porque buscan en subcarpetas.", JoinNames(agents))
}

// TUIPanelAdoptOff tells what [o] does for the skill self: the adoption of
// OFF/ takes every skill in it, keys, at once.
func TUIPanelAdoptOff(keys []string, self string) string {
	var others []string
	for _, k := range keys {
		if k != self {
			others = append(others, k)
		}
	}
	switch len(others) {
	case 0:
		return "Desactivarla de verdad la guarda aparte, donde ningún agente la ve."
	case 1:
		return fmt.Sprintf("Desactivarla de verdad la guarda aparte, donde ningún agente la ve, junto con la otra skill de OFF/ (%s).", SafeLine(others[0]))
	}
	return fmt.Sprintf("Desactivarla de verdad la guarda aparte, donde ningún agente la ve, junto con las otras %d skills de OFF/ (%s).", len(others), JoinNames(others))
}

// TUIPanelAdoptOffLinks follows TUIPanelAdoptOff when the adoption also
// removes the skill's n broken links.
func TUIPanelAdoptOffLinks(n int) string {
	if n == 1 {
		return "También borra su enlace roto."
	}
	return fmt.Sprintf("También borra sus %d enlaces rotos.", n)
}

// TUIPanelLinksInDoctor follows the problems of a skill with broken links
// the row cannot fix in one step.
func TUIPanelLinksInDoctor(n int) string {
	if n == 1 {
		return "Su enlace roto se arregla desde Salud (4)."
	}
	return fmt.Sprintf("Tiene %d enlaces rotos: Salud (4) los muestra uno por uno.", n)
}

// What the TUI says when a key of the Skills screen does not apply to the
// selected row (L83): plain words, no command of the CLI.

// TUIOffOnly is a store action pressed on a skill moved to OFF/ by hand.
func TUIOffOnly(action app.Action) string {
	return fmt.Sprintf("Esta skill está apagada a mano en OFF/, así que no se puede %s desde aquí. Usa «Desactivarla de verdad» [o].", lookup(plainActions, string(action)))
}

// TUIOutsideStore is a store action pressed on a skill outside the store.
func TUIOutsideStore(action app.Action) string {
	return fmt.Sprintf("Esta skill no está en el almacén de skillhub, así que no se puede %s desde aquí. Mira Salud (4) para ver cómo ordenarla.", lookup(plainActions, string(action)))
}

// TUICannotNow is a store action that does not apply to the skill now.
func TUICannotNow(action app.Action) string {
	return fmt.Sprintf("Ahora no se puede %s esta skill. Mira Salud (4) para ver qué la afecta.", lookup(plainActions, string(action)))
}

const (
	// TUINotOff is [o] pressed on a skill that is not in OFF/.
	TUINotOff = "«Desactivarla de verdad» [o] solo sirve para las skills apagadas a mano en OFF/."
	// TUINoBrokenLink is [p] pressed on a skill without broken links.
	TUINoBrokenLink = "Esta skill no tiene enlaces rotos que arreglar."
	// TUIFixInDoctor is [p] pressed on a skill whose broken links are fixed
	// from Doctor, or with [o].
	TUIFixInDoctor = "Los enlaces rotos de esta skill se arreglan desde Salud (4), uno por uno."
	TUIFixWithOff  = "Los enlaces rotos de esta skill se borran con «Desactivarla de verdad» [o]."
)

var plainIssues = map[domain.Issue]string{
	domain.IssueLocallyModified:   "Tiene cambios tuyos que no están en su origen.",
	domain.IssueDuplicate:         "Hay otra copia con el mismo nombre.",
	domain.IssueDivergentCopy:     "Una de sus copias es distinta de las demás.",
	domain.IssueBrokenLink:        "Tiene un enlace que no lleva a ninguna parte.",
	domain.IssueUntrackedBranch:   "Su clon de git no sigue ninguna rama remota.",
	domain.IssueInterruptedUpdate: "Una actualización quedó a medias.",
	domain.IssueRemovedUpstream:   "Ya no existe en su repositorio de origen.",
}

var plainStates = map[domain.State]string{
	domain.StateUpdateAvailable: "Hay una actualización: mírala en Actualizar (2).",
	domain.StateCheckFailed:     "La última revisión de actualizaciones falló.",
	domain.StateNoOrigin:        "No tiene un origen registrado: skillhub no puede actualizarla.",
	domain.StateMissing:         "Falta su carpeta.",
}

// TUIPlainProblems tells a skill's state and issues as sentences; none for
// a skill with nothing to tell.
func TUIPlainProblems(state domain.State, issues []domain.Issue) []string {
	var out []string
	if s, ok := plainStates[state]; ok {
		out = append(out, s)
	}
	for _, is := range issues {
		out = append(out, lookup(plainIssues, is))
	}
	return out
}

// JoinNames joins names as a person reads them: "A", "A y B", "A, B y C".
// Every item passes through SafeLine so third-party names cannot act on the terminal.
func JoinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return SafeLine(names[0])
	}
	safe := make([]string, len(names))
	for i, n := range names {
		safe[i] = SafeLine(n)
	}
	return strings.Join(safe[:len(safe)-1], ", ") + " y " + safe[len(safe)-1]
}

func agents(ids []string, name AgentNamer) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, name(id))
	}
	return JoinNames(out)
}

const (
	undoFromHistory = "Se deshace con z enseguida, o desde el Historial (h)."
	reloadThose     = "Después, recarga las skills en esos agentes."
)

func reloadIn(ids []string, name AgentNamer) string {
	return fmt.Sprintf("Después, recarga las skills en %s.", agents(ids, name))
}

var plainRepairs = map[string]string{
	"delete_link":     "Borrar el enlace que no lleva a ninguna parte",
	"repoint_link":    "Hacer que el enlace lleve a la skill correcta",
	"enable_and_link": "Activar la skill y volver a enlazarla",
	"adopt_off":       "Desactivar con skillhub las skills de OFF/",
	"adopt":           "Pasar la skill a la gestión de skillhub",
	"park_entry":      "Desactivar el registro de una skill sin carpeta",
	"reinstall":       "Reinstalar la skill desde su origen",
}

var plainActions = map[string]string{
	string(app.ActionDisable):   "desactivar",
	string(app.ActionEnable):    "activar",
	string(app.ActionUnlink):    "quitar de un agente",
	string(app.ActionLink):      "añadir a un agente",
	string(app.ActionAdoptOff):  "desactivar las skills de OFF/",
	string(app.ActionRemove):    "eliminar",
	string(app.ActionInstall):   "instalar",
	string(app.ActionReinstall): "reinstalar",
	string(app.ActionRepair):    "reparar",
}

func providerWarningLine(provider string) string {
	return fmt.Sprintf("Aviso de proveedor (%s): borrarla solo elimina los archivos del disco, sin actualizar el estado ni el manifiesto del proveedor, por lo que el proveedor podría volver a crearla o dejar un plugin incompleto. La operación queda en el snapshot y se puede deshacer con «skillhub undo» (o con z aquí).", providerLabel(provider))
}

// TUIPlanSummary is the plain summary of a plan: what happens, naming the
// agents, what is not lost, how it is undone and what to reload.
func TUIPlanSummary(p app.Proposal, name AgentNamer) Summary {
	r := p.Request
	who := agents(p.Reload, name)
	var s Summary
	gone := func() string {
		if who == "" {
			return "Ningún agente la ve ahora."
		}
		return fmt.Sprintf("Dejará de estar disponible en %s.", who)
	}
	reload := func() {
		if who != "" {
			s.Lines = append(s.Lines, reloadThose)
		}
	}
	switch r.Action {
	case app.ActionDisable:
		s.Question = fmt.Sprintf("¿Desactivar %s?", SafeLine(r.Key))
		s.Lines = []string{gone(), "No se borra nada: la puedes volver a activar con [a]."}
		reload()
	case app.ActionEnable:
		s.Question = fmt.Sprintf("¿Activar %s?", SafeLine(r.Key))
		s.Lines = []string{"Vuelve a quedar como estaba antes de desactivarla."}
		if who != "" {
			s.Lines = []string{fmt.Sprintf("Volverá a estar disponible en %s.", who)}
		}
		s.Lines = append(s.Lines, "Se deshace con z enseguida, o desactivándola con [d].")
		reload()
	case app.ActionUnlink:
		h := SafeLine(name(r.Harness))
		s.Question = fmt.Sprintf("¿Quitar %s de %s?", SafeLine(r.Key), h)
		first := fmt.Sprintf("Dejará de estar disponible en %s.", h)
		if p.Toggle != nil && sees(p.Toggle.StillVisible, r.Harness) {
			first = fmt.Sprintf("%s la seguirá viendo por otra vía: esto solo quita una de ellas.", h)
		}
		s.Lines = []string{first, "No se borra nada: la puedes volver a añadir con [l]."}
		if len(p.Reload) > 0 {
			s.Lines = append(s.Lines, reloadIn(p.Reload, name))
		}
	case app.ActionLink:
		h := SafeLine(name(r.Harness))
		s.Question = fmt.Sprintf("¿Añadir %s a %s?", SafeLine(r.Key), h)
		s.Lines = []string{
			fmt.Sprintf("Quedará disponible en %s, sin copiarla: es la misma skill.", h),
			"Se deshace con z enseguida, o quitándola con [l].",
		}
		if len(p.Reload) > 0 {
			s.Lines = append(s.Lines, reloadIn(p.Reload, name))
		}
	case app.ActionRemove:
		if p.Remove != nil && p.Remove.Group {
			label := p.Remove.SkillName
			if label == "" {
				label = p.Remove.Key
			}
			s.Question = fmt.Sprintf("¿Eliminar por completo %s?", SafeLine(label))
			first := "Ningún agente deja de detectarla."
			if len(p.Remove.Targets) > 0 {
				first = fmt.Sprintf("Dejará de estar disponible en %s.", agents(p.Remove.Targets, name))
			}
			folderPaths := make([]string, 0, len(p.Remove.Folders))
			for _, f := range p.Remove.Folders {
				folderPaths = append(folderPaths, SafeLine(f.Path))
			}
			second := fmt.Sprintf("Copias globales: %s.", strings.Join(folderPaths, ", "))
			if len(p.Remove.Links) > 0 {
				links := make([]string, 0, len(p.Remove.Links))
				for _, l := range p.Remove.Links {
					links = append(links, SafeLine(l.Path))
				}
				second += fmt.Sprintf(" Enlaces que se quitan: %s.", strings.Join(links, ", "))
			}
			second += " Se mueven a la papelera de skillhub: se deshace todo junto con z enseguida, o desde el Historial (h)."
			s.Lines = []string{first, second}
			if len(p.Remove.KeptCopies) > 0 || len(p.Remove.Homonyms) > 0 {
				var kept []string
				for _, c := range p.Remove.KeptCopies {
					kept = append(kept, fmt.Sprintf("%s (%s)", SafeLine(c.Path), SafeLine(keptCopyReason(c))))
				}
				for _, h := range p.Remove.Homonyms {
					kept = append(kept, fmt.Sprintf("%s (copia independiente con el mismo nombre)", SafeLine(h.Path)))
				}
				s.Lines = append(s.Lines, "Se conservan sin tocar: "+strings.Join(kept, ", ")+".")
			}
			if p.Remove.Provider != "" {
				s.Lines = append(s.Lines, providerWarningLine(p.Remove.Provider))
			}
			if len(p.Reload) > 0 {
				s.Lines = append(s.Lines, reloadIn(p.Reload, name))
			}
			break
		}
		if p.Remove != nil && p.Remove.Project {
			name := SafeLine(p.Remove.SkillName)
			root := SafeLine(p.Remove.ProjectRoot)
			s.Question = fmt.Sprintf("¿Eliminar %s (%s de este proyecto)?", name, root)
			first := fmt.Sprintf("Solo se quita la copia de este proyecto en %s; las demás skills no se tocan.", root)
			second := fmt.Sprintf("Carpeta: %s. Se mueve a la papelera de skillhub: se deshace desde este mismo proyecto con z o en el Historial (h).", SafeLine(p.Remove.Folder))
			if len(p.Remove.Links) > 0 {
				links := make([]string, 0, len(p.Remove.Links))
				for _, l := range p.Remove.Links {
					links = append(links, SafeLine(l.Path))
				}
				second = fmt.Sprintf("Carpeta: %s (enlaces del proyecto que se quitan: %s). Se mueve a la papelera de skillhub: todo se restaura desde este mismo proyecto con z o en el Historial (h).",
					SafeLine(p.Remove.Folder), strings.Join(links, ", "))
			}
			s.Lines = []string{first, second}
			break
		}
		label := r.Key
		if p.Remove != nil {
			if p.Remove.Clone && p.Remove.SkillName != "" {
				label = p.Remove.SkillName
			} else if p.Remove.Key != "" {
				label = p.Remove.Key
			}
		}
		s.Question = fmt.Sprintf("¿Eliminar %s?", SafeLine(label))
		first := gone()
		switch {
		case p.Remove != nil && p.Remove.Parked:
			first = "Está desactivada: ningún agente la ve ahora."
		case p.Remove != nil && p.Remove.Off:
			if who == "" {
				first = "Está desactivada a mano en OFF/: ningún agente la ve ahora."
			} else {
				first = fmt.Sprintf("Está desactivada a mano en OFF/, pero dejará de estar disponible en %s.", who)
			}
		}
		second := "Su carpeta se mueve a la papelera de skillhub: se deshace con z enseguida, o desde el Historial (h)."
		if p.Remove != nil {
			switch {
			case p.Remove.Folder == "" && p.Remove.Parked:
				second = "Solo se descarta su registro desactivado (no tiene carpeta): se deshace con z enseguida, o desde el Historial (h)."
			case p.Remove.Clone && p.Remove.Folder != "":
				second = fmt.Sprintf("Carpeta (clon completo): %s", SafeLine(p.Remove.Folder))
				if len(p.Remove.Links) > 0 {
					links := make([]string, 0, len(p.Remove.Links))
					for _, l := range p.Remove.Links {
						links = append(links, SafeLine(l.Path))
					}
					second += fmt.Sprintf(" (enlaces que se quitan: %s)", strings.Join(links, ", "))
				}
				second += ". Se mueve a la papelera de skillhub: se deshace con z enseguida, o desde el Historial (h)."
			case p.Remove.Folder != "" && len(p.Remove.Links) > 0:
				links := make([]string, 0, len(p.Remove.Links))
				for _, l := range p.Remove.Links {
					links = append(links, SafeLine(l.Path))
				}
				second = fmt.Sprintf("Carpeta: %s (enlaces que se quitan: %s). Se mueve a la papelera de skillhub: se deshace con z enseguida, o desde el Historial (h).",
					SafeLine(p.Remove.Folder), strings.Join(links, ", "))
			case p.Remove.Folder != "":
				second = fmt.Sprintf("Carpeta: %s. Se mueve a la papelera de skillhub: se deshace con z enseguida, o desde el Historial (h).",
					SafeLine(p.Remove.Folder))
			}
		}
		s.Lines = []string{first, second}
		if p.Remove != nil && p.Remove.Clone && len(p.Remove.CloneSkills) > 0 {
			s.Lines = append(s.Lines, fmt.Sprintf("Skills del clon que se eliminan juntas: %s.", JoinNames(p.Remove.CloneSkills)))
		}
		if p.Remove != nil && p.Remove.Modified == "yes" {
			s.Lines = append(s.Lines, "Tus cambios en ella también quedan en la papelera.")
		}
		if p.Remove != nil && p.Remove.Provider != "" {
			s.Lines = append(s.Lines, providerWarningLine(p.Remove.Provider))
		}
		reload()
	case app.ActionInstall, app.ActionReinstall:
		key, from := r.Key, ""
		if p.Install != nil {
			key, from = p.Install.Key, tuiOrigin(p.Install.Source, p.Install.Site)
		}
		key = SafeLine(key)
		if r.Action == app.ActionInstall {
			s.Question = fmt.Sprintf("¿Instalar %s?", key)
			s.Lines = []string{fmt.Sprintf("Se instala desde %s.", from)}
		} else {
			s.Question = fmt.Sprintf("¿Reinstalar %s?", key)
			s.Lines = []string{fmt.Sprintf("Falta su carpeta: se descarga de nuevo desde %s.", from)}
		}
		if who != "" {
			s.Lines[0] += fmt.Sprintf(" Quedará disponible en %s.", who)
		}
		s.Lines = append(s.Lines, "No cambia ninguna otra skill. "+undoFromHistory)
		reload()
	case app.ActionRepair:
		s.Question = fmt.Sprintf("¿%s?", lookup(plainRepairs, r.Repair))
		first := "Corrige el problema que encontró Salud."
		if p.Repair != nil && p.Repair.Key != "" {
			first = fmt.Sprintf("Corrige el problema que encontró Salud en la skill %s.", SafeLine(p.Repair.Key))
		}
		s.Lines = append(s.Lines, first)
		s.Lines = append(s.Lines, "Lo que se quita queda guardado: se deshace con z enseguida, o desde el Historial (h).")
		reload()
	case app.ActionAdoptOff:
		var keys []string
		if p.Toggle != nil {
			for _, c := range p.Toggle.Skills {
				keys = append(keys, c.Key)
			}
		}
		if len(keys) == 1 {
			s.Question = fmt.Sprintf("¿Desactivar de verdad %s, la skill de OFF/?", SafeLine(keys[0]))
			s.Lines = []string{
				"skillhub la guarda aparte, donde ningún agente la ve. OFF/README.md no se toca.",
				"No se borra nada: la puedes volver a activar con [a].",
			}
		} else {
			s.Question = fmt.Sprintf("¿Desactivar de verdad las %d skills de OFF/?", len(keys))
			if len(keys) == 0 {
				s.Question = "¿Desactivar de verdad las skills de OFF/?"
			}
			s.Lines = []string{
				"skillhub las guarda aparte, donde ningún agente las ve. OFF/README.md no se toca.",
				"No se borra nada: las puedes volver a activar con [a].",
			}
			if len(keys) > 1 {
				s.Lines = append([]string{fmt.Sprintf("Se desactivan las %d juntas: %s.", len(keys), JoinNames(keys))}, s.Lines...)
			}
		}
		reload()
	case app.ActionUndo:
		of := ""
		if p.Undo != nil {
			of = lookup(plainActions, p.Undo.Of)
		}
		s.Question = fmt.Sprintf("¿Deshacer «%s»?", of)
		s.Lines = []string{
			"Todo vuelve a como estaba antes de esa operación.",
			"Un deshacer no se puede deshacer, pero puedes repetir la operación.",
		}
		if len(p.Reload) > 0 {
			s.Lines = append(s.Lines, reloadIn(p.Reload, name))
		}
	default:
		s.Question = fmt.Sprintf("¿%s?", SafeLine(string(r.Action)))
	}
	return s
}

func sees(sightings []app.Sighting, harness string) bool {
	for _, s := range sightings {
		if s.Harness == harness {
			return true
		}
	}
	return false
}

// TUIDone is what the TUI says after a plan ran: what changed, how to undo
// it and the agents to reload.
func TUIDone(res app.Result, name AgentNamer) string {
	p := res.Proposal
	r := p.Request
	var first string
	switch r.Action {
	case app.ActionDisable:
		first = fmt.Sprintf("Listo: %s está desactivada.", SafeLine(r.Key))
	case app.ActionEnable:
		first = fmt.Sprintf("Listo: %s está activada.", SafeLine(r.Key))
	case app.ActionUnlink:
		first = fmt.Sprintf("Listo: %s ya no está en %s.", SafeLine(r.Key), SafeLine(name(r.Harness)))
	case app.ActionLink:
		first = fmt.Sprintf("Listo: %s está disponible en %s.", SafeLine(r.Key), SafeLine(name(r.Harness)))
	case app.ActionRemove:
		if p.Remove != nil && p.Remove.Group {
			label := p.Remove.SkillName
			if label == "" {
				label = p.Remove.Key
			}
			first = fmt.Sprintf("Listo: todas las copias globales elegibles de %s se movieron a la papelera de skillhub.", SafeLine(label))
			break
		}
		if p.Remove != nil && p.Remove.Project {
			first = fmt.Sprintf("Listo: %s (%s) se movió a la papelera de skillhub.", SafeLine(p.Remove.SkillName), SafeLine(p.Remove.ProjectRoot))
			break
		}
		label := r.Key
		if p.Remove != nil && p.Remove.Key != "" {
			label = p.Remove.Key
		}
		first = fmt.Sprintf("Listo: %s se movió a la papelera de skillhub.", SafeLine(label))
	case app.ActionInstall, app.ActionReinstall:
		key := r.Key
		if p.Install != nil {
			key = p.Install.Key
		}
		first = fmt.Sprintf("Listo: %s está instalada.", SafeLine(key))
	case app.ActionAdoptOff:
		first = "Listo: las skills de OFF/ están desactivadas con skillhub."
	case app.ActionUndo:
		first = "Listo: la operación se deshizo."
	default:
		first = "Listo: la reparación se aplicó."
	}
	// The TUI adds the key that undoes it (TUIWithUndo).
	lines := []string{first}
	if len(res.Superseded) > 0 {
		lines = append(lines, "Sus actualizaciones pendientes ya no se ofrecen.")
	}
	if len(p.Reload) > 0 {
		lines = append(lines, fmt.Sprintf("Recarga las skills en %s.", agents(p.Reload, name)))
	}
	return strings.Join(lines, "\n")
}

// tuiOrigin names where an install comes from: the GitHub repository, or
// the website of a well-known source, from its address.
func tuiOrigin(source, site string) string {
	if site == "" {
		return SafeLine(source)
	}
	host := strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://")
	if i := strings.IndexAny(host, "/@"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		host = source
	}
	return fmt.Sprintf("el sitio web %s (no es GitHub)", SafeLine(host))
}

// TUIInstallSummary heads the review of an install, before its files: repo
// is its GitHub repository, or site its website.
func TUIInstallSummary(key, repo, site string) Summary {
	return Summary{
		Question: fmt.Sprintf("¿Instalar %s?", SafeLine(key)),
		Lines: []string{
			fmt.Sprintf("Viene de %s. Revisa abajo sus archivos; después eliges en qué agentes queda disponible.", tuiOrigin(repo, site)),
			"Nada se instala hasta que confirmes el plan.",
		},
	}
}

// TUIUpdateSummary starts the confirmation of a version 1 update.
func TUIUpdateSummary(name string, files int, locallyModified bool) Summary {
	s := Summary{
		Question: fmt.Sprintf("¿Actualizar %s?", SafeLine(name)),
		Lines:    []string{fmt.Sprintf("Se instala la versión nueva del autor (%d archivo(s) cambian).", files)},
	}
	if locallyModified {
		s.Lines = append(s.Lines, "Tus cambios se reemplazan, pero quedan guardados en una copia permanente.")
	}
	s.Lines = append(s.Lines, "Se guarda la versión actual; se deshace con z enseguida, o desde el Historial (h).")
	return s
}

// TUIUndoUpdateSummary starts the confirmation of undoing a version 1
// update whose folder changed after it was applied.
func TUIUndoUpdateSummary(name, saved string) Summary {
	return Summary{
		Question: fmt.Sprintf("¿Deshacer la actualización de %s?", SafeLine(name)),
		Lines: []string{
			"La carpeta cambió después de actualizarla.",
			fmt.Sprintf("Esos cambios no se pierden: quedan en la copia %s.", SafeLine(saved)),
		},
	}
}

// TUIUpdated is what the TUI says after a version 1 update.
func TUIUpdated(name string) string {
	return fmt.Sprintf("Listo: %s está actualizada.", SafeLine(name))
}

// TUIUpdateUndone is what the TUI says after undoing a version 1 update.
func TUIUpdateUndone(name string) string {
	return fmt.Sprintf("Listo: se deshizo la actualización de %s.", SafeLine(name))
}

// The Actualizaciones screen of S13: a row per update with an icon by risk
// and the panel of the selected one, telling in plain words what changes.
const (
	TUIUpdateShortBlocked = "bloqueada"

	TUIUpdateActionView  = "[enter] Ver cambios"
	TUIUpdateActionApply = "[a] Aplicar esta"
	TUIUpdateActionSkip  = "[o] Omitir esta versión"
	TUIUpdateActionAll   = "[t] Aplicar todas"

	TUIKeyView     = "enter ver cambios"
	TUIKeyApply    = "a aplicar esta"
	TUIKeySkip     = "o omitir esta versión"
	TUIKeyApplyAll = "t aplicar todas"
	TUIKeyCheck    = "r revisar ahora"

	// TUIUpdateModifiedNote is in the panel of an update of a skill the user
	// changed: applying it alone keeps a copy, and applying all leaves it out.
	TUIUpdateModifiedNote = "Tiene cambios tuyos: aplícala sola; al aplicarla se guardan en una copia permanente."
)

// riskShort is the short state of an update row whose labels ask for a
// look, by its first such label: short enough for the list at 100 columns.
var riskShort = map[domain.Label]string{
	domain.LabelTouchesScripts: "toca scripts",
	domain.LabelNewPermissions: "pide permisos",
	domain.LabelSuspiciousText: "revisar texto",
	domain.LabelNonTextFiles:   "otros archivos",
	domain.LabelModeChange:     "ejecutables",
}

// TUIUpdateRiskShort is the short state of an update row with the label.
func TUIUpdateRiskShort(l domain.Label) string { return lookup(riskShort, l) }

var plainLabels = map[domain.Label]string{
	domain.LabelTextOnly:        "cambia solo texto",
	domain.LabelTouchesScripts:  "agrega o cambia scripts que el agente puede ejecutar",
	domain.LabelNewPermissions:  "pide permisos nuevos para el agente",
	domain.LabelSuspiciousText:  "trae direcciones web o texto que conviene revisar",
	domain.LabelNonTextFiles:    "trae archivos que no son texto",
	domain.LabelModeChange:      "cambia qué archivos se pueden ejecutar",
	domain.LabelLocallyModified: "reemplaza cambios tuyos, que quedan guardados en una copia",
}

// TUILabelPlain tells a risk label as a person reads it.
func TUILabelPlain(l domain.Label) string { return lookup(plainLabels, l) }

// TUILabelsPlain tells the labels, one sentence.
func TUILabelsPlain(labels []domain.Label) string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, TUILabelPlain(l))
	}
	if len(out) == 0 {
		return "no se sabe qué cambia."
	}
	return JoinNames(out) + "."
}

// TUIUpdatesTitle heads the Actualizaciones list.
func TUIUpdatesTitle(n int) string { return fmt.Sprintf("Actualizaciones (%d)", n) }

// TUIUpdateVersions tells from which version to which an update goes.
func TUIUpdateVersions(from, to string) string {
	return fmt.Sprintf("De la versión %s a la %s.", SafeLine(from), SafeLine(to))
}

// TUIUpdateWhat starts the panel's line of what an update changes.
func TUIUpdateWhat(labels []domain.Label) string { return "Qué cambia: " + TUILabelsPlain(labels) }

// TUIUpdateFiles heads the files an update changes.
func TUIUpdateFiles(n int) string { return fmt.Sprintf("Archivos que cambian (%d):", n) }

// TUIUpdateBlocked tells why an update cannot be applied.
func TUIUpdateBlocked(reason string) string {
	return fmt.Sprintf("No se puede aplicar: %s.", SafeLine(reason))
}

// TUIExcluded is an update applying all leaves out, and why.
type TUIExcluded struct{ Name, Reason string }

// TUIApplyAllSummary starts the confirmation of applying all the updates:
// the ones applied, one at a time, and the ones left out.
func TUIApplyAllSummary(included []string, excluded []TUIExcluded) Summary {
	s := Summary{
		Question: "¿Aplicar todas las actualizaciones?",
		Lines:    []string{fmt.Sprintf("Se aplican de a una: %s.", JoinNames(included))},
	}
	if len(excluded) > 0 {
		out := make([]string, 0, len(excluded))
		for _, e := range excluded {
			out = append(out, fmt.Sprintf("%s (%s)", SafeLine(e.Name), SafeLine(e.Reason)))
		}
		s.Lines = append(s.Lines, fmt.Sprintf("Quedan fuera: %s.", JoinNames(out)))
	}
	s.Lines = append(s.Lines, "Antes de cada una se guarda la versión actual; se deshace cada una desde el Historial (h).")
	return s
}

// The reasons applying all leaves an update out.
const (
	TUIExcludedModified = "tiene cambios tuyos: aplícala sola"
	TUIExcludedBlocked  = "bloqueada: %s"
)

// The Historial screen of S13: each line in plain words, the identifiers in
// the detail panel only.
const (
	TUIHistoryUndoable    = "se puede deshacer [u]"
	TUIHistoryActionUndo  = "[u] Deshacer"
	TUIKeyUndo            = "u deshacer"
	TUIHistoryNoSkill     = "una skill"
	TUIHistoryRecoveredV2 = "Se recuperó una operación interrumpida"
	TUIHistoryFailedV2    = "Falló una operación"
)

// TUIHistoryTitle heads the Historial list.
func TUIHistoryTitle(n int) string { return fmt.Sprintf("Historial (%d)", n) }

var updateDid = map[domain.HistoryAction]string{
	domain.ActionApplied:   "Actualizaste %s",
	domain.ActionUndone:    "Deshiciste la actualización de %s",
	domain.ActionSkipped:   "Omitiste una versión de %s",
	domain.ActionFailed:    "Falló la actualización de %s",
	domain.ActionRecovered: "Se recuperó una actualización interrumpida de %s",
}

// TUIUpdateDid tells what a version 1 journal line did to the skill name.
func TUIUpdateDid(action domain.HistoryAction, name string) string {
	name = SafeLine(name)
	if f, ok := updateDid[action]; ok {
		return fmt.Sprintf(f, name)
	}
	return fmt.Sprintf("%s: %s", HistoryAction(action), name)
}

var operationDid = map[string]string{
	string(app.ActionDisable):   "Desactivaste %s",
	string(app.ActionEnable):    "Activaste %s",
	string(app.ActionUnlink):    "Quitaste %s de un agente",
	string(app.ActionLink):      "Añadiste %s a un agente",
	string(app.ActionRemove):    "Eliminaste %s",
	string(app.ActionInstall):   "Instalaste %s",
	string(app.ActionReinstall): "Reinstalaste %s",
}

// TUIOperationDid tells what a version 2 journal line did: what the user
// did to which skills, or what undoing it did.
func TUIOperationDid(action domain.HistoryAction, op app.OperationView) string {
	switch action {
	case domain.ActionRecovered:
		return TUIHistoryRecoveredV2
	case domain.ActionFailed:
		return TUIHistoryFailedV2
	}
	what := JoinNames(op.Keys)
	if what == "" && op.Label != "" {
		what = SafeLine(op.Label)
		if op.ProjectDir != "" {
			what = fmt.Sprintf("%s (proyecto)", what)
		}
	}
	if what == "" {
		what = TUIHistoryNoSkill
	}
	if op.Undo {
		name := lookup(plainActions, op.Action)
		if r, ok := plainRepairs[op.Action]; ok {
			name = strings.ToLower(r[:1]) + r[1:]
		}
		if len(op.Keys) == 0 && op.Label == "" {
			return fmt.Sprintf("Deshiciste «%s»", name)
		}
		return fmt.Sprintf("Deshiciste «%s» de %s", name, what)
	}
	if f, ok := operationDid[op.Action]; ok {
		return fmt.Sprintf(f, what)
	}
	if op.Action == string(app.ActionAdoptOff) || op.Action == "adopt_off" {
		return "Desactivaste con skillhub las skills de OFF/"
	}
	if r, ok := plainRepairs[op.Action]; ok {
		if len(op.Keys) == 0 {
			return fmt.Sprintf("Reparaste: %s", strings.ToLower(r[:1])+r[1:])
		}
		return fmt.Sprintf("Reparaste %s: %s", what, strings.ToLower(r[:1])+r[1:])
	}
	return fmt.Sprintf("Hiciste una operación sobre %s", what)
}

// TUIAgo tells how long ago at was, seen at now.
func TUIAgo(at, now time.Time) string {
	d := now.Sub(at)
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "hace 1 " + one
		}
		return fmt.Sprintf("hace %d %s", n, many)
	}
	switch {
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minuto", "minutos")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hora", "horas")
	case d < 30*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "día", "días")
	}
	return "el " + at.UTC().Format("2006-01-02")
}

// TUIHistoryLine is a Historial line: what was done, when, and whether it
// can be undone.
func TUIHistoryLine(did, ago string, undoable bool) string {
	line := SafeLine(did) + " — " + SafeLine(ago)
	if undoable {
		line += " — " + TUIHistoryUndoable
	}
	return line
}

// The Instalar screen of S13: its four steps, told before it starts and at
// each of them.
const (
	TUIInstallIntro = "Instala una skill desde GitHub o desde un sitio web, en cuatro pasos:\n\n" +
		"(1) De dónde la traigo: escribes su origen, por ejemplo owner/repo o https://sitio.\n" +
		"(2) Revisa lo que trae: ves sus archivos y qué hace cada cambio, antes de instalar nada.\n" +
		"(3) Elige los agentes: marcas en cuáles queda disponible.\n" +
		"(4) Confirma: lees el resumen y respondes sí o no. Se deshace con z enseguida, o desde el Historial (h).\n\n" +
		"Pulsa enter para empezar."
	TUIInstallSourceHelp = "Escribe de dónde la traigo. De GitHub: owner/repo, owner/repo@skill, owner/repo/ruta o la URL github.com/o/r/tree/ref/ruta. De un sitio web que publica skills: https://sitio, https://sitio/ruta o https://sitio@skill."
)

var installSteps = []string{"de dónde la traigo", "revisa lo que trae", "elige los agentes", "confirma"}

// TUIInstallStep names step n of the install, from 1 to 4.
func TUIInstallStep(n int) string {
	if n < 1 || n > len(installSteps) {
		return ""
	}
	return fmt.Sprintf("Paso %d de %d: %s", n, len(installSteps), installSteps[n-1])
}

// TUIInstallBrings tells what an install brings, by its labels.
func TUIInstallBrings(labels []domain.Label) string { return "Lo que trae: " + TUILabelsPlain(labels) }

// The Doctor screen of S13: the findings grouped by kind, each kind with a
// count and a plain line; gentle-ai's copies in one folded line.

// TUIGentleKind is the kind of gentle-ai's copies, folded in one line.
const TUIGentleKind = "managed_by_gentle_ai"

type findingGroup struct {
	title, one, many, what, fix string
}

var findingGroups = map[string]findingGroup{
	"broken_link": {"Enlaces rotos", "enlace roto", "enlaces rotos",
		"agentes que apuntan a una skill que ya no existe", "borrar el enlace"},
	"link_cycle": {"Enlaces en ciclo", "enlace en ciclo", "enlaces en ciclo",
		"enlaces que se apuntan entre sí y no llevan a ninguna skill", "ninguno, skillhub solo lo informa"},
	"link_outside_roots": {"Enlaces a otras carpetas", "enlace a otra carpeta", "enlaces a otras carpetas",
		"enlaces que llevan fuera de las carpetas de skills conocidas", "ninguno, skillhub no los sigue"},
	"identical_copies": {"Copias idénticas", "skill copiada igual", "skills copiadas igual",
		"la misma skill está en varios lugares, con el mismo contenido", "dejar una sola copia y enlazar las demás"},
	"no_canonical": {"Copias sin original", "skill sin original", "skills sin original",
		"están copiadas en varios agentes, pero no en el almacén de skillhub", "elegir la copia que se queda y enlazar las demás"},
	"unmanaged_skill": {"Skills sin gestionar", "skill sin gestionar", "skills sin gestionar",
		"copiadas a mano, fuera del almacén de skillhub", "pasarla a la gestión de skillhub"},
	"lock_entry_without_folder": {"Registros sin carpeta", "registro sin carpeta", "registros sin carpeta",
		"npx skills recuerda una skill cuya carpeta ya no está", "reinstalarla o desactivar su registro"},
	"lock_name_mismatch": {"Nombres distintos", "nombre distinto", "nombres distintos",
		"el nombre que guarda npx skills no coincide con el de la skill", "ninguno, skillhub solo lo informa"},
	"parked_but_visible": {"Apagadas pero visibles", "skill apagada pero visible", "skills apagadas pero visibles",
		"están en OFF/, pero algún agente todavía las ve", "desactivarlas con skillhub"},
	"homonyms": {"Nombres repetidos", "skill con nombre repetido", "skills con nombre repetido",
		"skills distintas que se llaman igual", "ninguno, skillhub nunca las une por su nombre"},
}

// TUIFindingOrder is the order of the Doctor groups: the ones a repair
// fixes first.
var TUIFindingOrder = []string{
	"broken_link", "parked_but_visible", "lock_entry_without_folder", "unmanaged_skill", "no_canonical",
	"identical_copies", "homonyms", "lock_name_mismatch", "link_cycle", "link_outside_roots",
}

// TUIFindingGroupTitle heads a Doctor group in the list.
func TUIFindingGroupTitle(kind string, n int) string {
	title := FindingKind(kind)
	if g, ok := findingGroups[kind]; ok {
		title = g.title
	}
	return fmt.Sprintf("%s (%d)", title, n)
}

// TUIFindingGroup is the plain line of a Doctor group: how many, what the
// problem is and the suggested fix.
func TUIFindingGroup(kind string, n int) string {
	g, ok := findingGroups[kind]
	if !ok {
		return fmt.Sprintf("%d hallazgos de tipo «%s».", n, FindingKind(kind))
	}
	noun := g.many
	if n == 1 {
		noun = g.one
	}
	return fmt.Sprintf("%d %s: %s. Arreglo sugerido: %s.", n, noun, g.what, g.fix)
}

// TUIGentleFolded is the one line of gentle-ai's copies.
func TUIGentleFolded(n int) string {
	if n == 1 {
		return "1 skill de gentle-ai: la gestiona gentle-ai, skillhub no la toca."
	}
	return fmt.Sprintf("%d skills de gentle-ai: las gestiona gentle-ai, skillhub no las toca.", n)
}

// TUIRepairPlain names a repair as a person reads it.
func TUIRepairPlain(kind string) string { return lookup(plainRepairs, kind) }

// TUIDoctorTitle heads the Doctor list.
func TUIDoctorTitle(n int) string { return fmt.Sprintf("Hallazgos (%d)", n) }

// The lines of the Doctor panel and of its repairs.
const (
	TUIDoctorActionRepair = "[enter] Ver cómo repararlo"
	TUIKeyRepair          = "enter ver cómo repararlo"
	TUIDoctorAgents       = "Agentes: "
	TUIDoctorWhere        = "Dónde:"
	TUIRepairsQuestion    = "¿Cómo lo reparo?"
	TUIChooseCanon        = "Elige qué copia se queda como la original; las demás se guardan."
)

// The line under each setting of Ajustes, telling what it does.
const (
	TUISettingInterval      = "Cada cuánto se buscan versiones nuevas de tus skills (mínimo 15 minutos)."
	TUISettingNotifications = "Avisa en el escritorio cuando hay versiones nuevas; nunca instala nada."
	TUISettingIgnore        = "Ignorar una skill (x en Skills) oculta la skill de skillhub; tus agentes la siguen usando. Con enter eliges cuáles dejar de ignorar."
	TUISettingBackground    = "Revisa aunque skillhub esté cerrado; nunca aplica nada. Se activa con el comando de abajo."
	TUIKeySettingsMove      = "↑/↓ elegir"
	TUIKeySettingsChange    = "enter cambiar"
)

// The keys every screen ends with, and the footer of a confirmation.
const (
	TUIKeyHelpQuit        = "? ayuda • q salir"
	TUIConfirmFooter      = "esc no • ↑/↓ desplazar el detalle"
	TUIConfirmFooterShort = "esc no"
	TUIKeysChoose         = "↑/↓ elegir • enter aceptar • esc volver"
	TUIKeysInstallRun     = "enter continuar • esc cancelar"
)

// The keys under a diff, and on Instalar before it starts.
const (
	TUIKeysDiff         = "↑/↓ desplazar • a aplicar esta • o omitir esta versión • esc volver"
	TUIKeysDiffSplit    = "↑/↓ desplazar • v cambiar vista • a aplicar esta • o omitir esta versión • esc volver"
	TUIKeysInstallStart = "enter empezar • r revisar ahora • ? ayuda • q salir"
)
