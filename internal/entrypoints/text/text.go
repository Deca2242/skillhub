// Package text holds the Spanish names of the states, issues, labels and
// other enum values of the domain, shared by the CLI and the TUI (S11,
// docs/design.md section 9). The codes stay in English in JSON output; these
// names are what a person reads.
package text

import "skillhub/internal/shared/domain"

// V2OperationPending explains updates.ErrV2OperationPending: a version 2
// operation record stops every version 1 flow until recover resolves it.
// The CLI prints it as is and the TUI as a sentence.
const V2OperationPending = "hay una operación de gestión de skills sin terminar; ejecuta «skillhub recover»"

// lookup returns the name of v, or its code escaped with SafeLine when v is
// unknown, so a value added later is still shown without acting on the terminal.
func lookup[T ~string](table map[T]string, v T) string {
	if name, ok := table[v]; ok {
		return name
	}
	return SafeLine(string(v))
}

var states = map[domain.State]string{
	domain.StateUpToDate:        "al día",
	domain.StateUpdateAvailable: "actualización disponible",
	domain.StateUnchecked:       "sin revisar",
	domain.StateCheckFailed:     "revisión fallida",
	domain.StateNoOrigin:        "sin origen",
	domain.StateDisabled:        "desactivada",
	domain.StateMissing:         "faltante",
}

// State is the Spanish name of a skill state.
func State(v domain.State) string { return lookup(states, v) }

var issues = map[domain.Issue]string{
	domain.IssueLocallyModified:   "modificada localmente",
	domain.IssueDuplicate:         "duplicada",
	domain.IssueDivergentCopy:     "copia divergente",
	domain.IssueBrokenLink:        "enlace roto",
	domain.IssueUntrackedBranch:   "rama sin seguimiento",
	domain.IssueInterruptedUpdate: "actualización interrumpida",
	domain.IssueRemovedUpstream:   "eliminada del repositorio de origen",
}

// Issue is the Spanish name of an inventory issue.
func Issue(v domain.Issue) string { return lookup(issues, v) }

var labels = map[domain.Label]string{
	domain.LabelNonTextFiles:    "archivos que no son texto",
	domain.LabelTouchesScripts:  "toca scripts",
	domain.LabelModeChange:      "cambio de permisos de archivo",
	domain.LabelNewPermissions:  "permisos nuevos",
	domain.LabelSuspiciousText:  "texto sospechoso",
	domain.LabelLocallyModified: "modificada localmente",
	domain.LabelTextOnly:        "solo texto",
}

// Label is the Spanish name of a risk label.
func Label(v domain.Label) string { return lookup(labels, v) }

var candidateStates = map[domain.CandidateState]string{
	domain.CandidatePending:    "pendiente",
	domain.CandidateApplied:    "aplicada",
	domain.CandidateSkipped:    "omitida",
	domain.CandidateSuperseded: "reemplazada",
	domain.CandidateBlocked:    "bloqueada",
	domain.CandidateFailed:     "fallida",
}

// CandidateState is the Spanish name of an update candidate's state.
func CandidateState(v domain.CandidateState) string { return lookup(candidateStates, v) }

var originKinds = map[domain.OriginKind]string{
	domain.OriginLock:     "lock de skills",
	domain.OriginGitClone: "clon de git",
	domain.OriginNone:     "sin origen",
}

// OriginKind is the Spanish name of a skill's origin.
func OriginKind(v domain.OriginKind) string { return lookup(originKinds, v) }

var locationKinds = map[domain.LocationKind]string{
	domain.LocationReal:       "carpeta",
	domain.LocationSymlink:    "enlace",
	domain.LocationBrokenLink: "enlace roto",
}

// LocationKind is the Spanish name of a location's kind.
func LocationKind(v domain.LocationKind) string { return lookup(locationKinds, v) }

var operationKinds = map[domain.OperationKind]string{
	domain.OperationApply: "aplicación",
	domain.OperationUndo:  "deshacer",
}

// OperationKind is the Spanish name of an operation's kind.
func OperationKind(v domain.OperationKind) string { return lookup(operationKinds, v) }

var historyActions = map[domain.HistoryAction]string{
	domain.ActionApplied:   "aplicada",
	domain.ActionUndone:    "deshecha",
	domain.ActionSkipped:   "omitida",
	domain.ActionFailed:    "fallida",
	domain.ActionRecovered: "recuperada",
}

// HistoryAction is the Spanish name of a journal action.
func HistoryAction(v domain.HistoryAction) string { return lookup(historyActions, v) }

var snapshotCauses = map[domain.SnapshotCause]string{
	domain.SnapshotBeforeApply: "antes de aplicar",
	domain.SnapshotBeforeUndo:  "antes de deshacer",
}

// SnapshotCause is the Spanish name of a snapshot's cause.
func SnapshotCause(v domain.SnapshotCause) string { return lookup(snapshotCauses, v) }

var blockReasons = map[domain.BlockReason]string{
	domain.BlockDirtyWorktree:    "el clon tiene archivos modificados o sin seguimiento",
	domain.BlockLocalCommits:     "el clon tiene commits propios o una historia divergente",
	domain.BlockHashMismatch:     "el contenido preparado no coincide con el hash de upstream",
	domain.BlockUnsupportedEntry: "upstream contiene un enlace simbólico o un submódulo",
	domain.BlockLimitsExceeded:   "upstream supera los límites de profundidad, número de archivos o tamaño",
	domain.BlockGitAttributes:    "el clon de upstream trae atributos de git que transformarían los archivos al instalarlos",
}

// BlockReason is the Spanish explanation of why a candidate is blocked.
func BlockReason(v domain.BlockReason) string { return lookup(blockReasons, v) }

var fileChangeKinds = map[domain.FileChangeKind]string{
	domain.ChangeAdded:    "añadido",
	domain.ChangeModified: "modificado",
	domain.ChangeRemoved:  "eliminado",
}

// FileChangeKind is the Spanish name of how a file changed.
func FileChangeKind(v domain.FileChangeKind) string { return lookup(fileChangeKinds, v) }
