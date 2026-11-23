package text

import (
	"fmt"
	"strings"

	"skillhub/internal/app"
	"skillhub/internal/updates"
)

// The texts of recover, shared by the CLI and the TUI: both tell the user
// the same thing about an interrupted operation (S11).
const (
	RecoverNoOp = "No hay ninguna operación interrumpida.\n"
	// RecoverPending follows the recover of an interrupted apply: its
	// candidate is pending again.
	RecoverPending = "La actualización sigue pendiente y puedes volver a aprobarla.\n"
	// RecoverAlsoCleared follows a recovered operation when recover also
	// removed an orphan install staging.
	RecoverAlsoCleared = "Además se eliminó la copia de una instalación sin terminar.\n"

	fmtRecovered      = "Se recuperó %s: se restauró la versión anterior desde el snapshot %s.\n"
	fmtRecoveredEmpty = "La operación sobre %s no llegó a cambiar nada; se descartaron sus restos.\n"
	fmtRecoverLeft    = "No había ninguna operación interrumpida; se eliminaron restos incompletos: %s.\n"
	fmtRecoverNote    = "Nota: %s\n"
	msgLeftSnapshot   = "1 snapshot incompleto"
	fmtLeftSnapshots  = "%d snapshots incompletos"
	msgLeftStaging    = "la copia de una instalación sin terminar"
)

// recoverNotes are the Spanish sentences of the notes a version 1 restore
// leaves about the lock (internal/updates/snapshot.go), keyed by the note.
var recoverNotes = map[string]string{
	"lock entry removed by another tool; not recreated":  "otra herramienta quitó la entrada del lock; no se volvió a crear",
	"lock entry ref changed by another tool; left as is": "otra herramienta cambió el ref de la entrada del lock; se dejó como estaba",
	"lock entry rewritten by another tool; left as is":   "otra herramienta reescribió la entrada del lock; se dejó como estaba",
}

// RecoverNote is the Spanish sentence of a note a version 1 restore left
// about the lock; a note it does not know is shown escaped with SafeLine.
func RecoverNote(detail string) string {
	if s, ok := recoverNotes[detail]; ok {
		return s
	}
	return SafeLine(detail)
}

// RecoverLeftovers names, in a sentence, what a recover with no operation
// removed: incomplete snapshots, an orphan install staging, or both.
func RecoverLeftovers(snapshots int, staging bool) string {
	var parts []string
	switch {
	case snapshots == 1:
		parts = append(parts, msgLeftSnapshot)
	case snapshots > 1:
		parts = append(parts, fmt.Sprintf(fmtLeftSnapshots, snapshots))
	}
	if staging {
		parts = append(parts, msgLeftStaging)
	}
	return fmt.Sprintf(fmtRecoverLeft, strings.Join(parts, " y "))
}

// RecoveryV1 tells what a successful recover did with a version 1
// operation; leftovers is whether it also removed an orphan install staging.
func RecoveryV1(res updates.RecoverResult, leftovers bool) string {
	op := res.Operation
	if op.ID == "" {
		// No operation was recorded: only leftovers were removed, and
		// nothing was restored. Detail repeats the count in English.
		return RecoverLeftovers(res.IncompleteSnapshots, leftovers)
	}
	var b strings.Builder
	if res.Restored {
		fmt.Fprintf(&b, fmtRecovered, SafeLine(op.Target), SafeLine(op.RestoreSnapshotID))
	} else {
		fmt.Fprintf(&b, fmtRecoveredEmpty, SafeLine(op.Target))
	}
	if res.Detail != "" {
		fmt.Fprintf(&b, fmtRecoverNote, RecoverNote(res.Detail))
	}
	if op.CandidateID != "" {
		b.WriteString(RecoverPending)
	}
	if leftovers {
		b.WriteString(RecoverAlsoCleared)
	}
	return b.String()
}

// Recovered tells what a successful RecoverOperation did, in the version it
// resolved.
func Recovered(rec app.Recovery) string {
	if rec.Version == 2 {
		return Recovery(rec.V2)
	}
	return RecoveryV1(rec.V1, rec.Leftovers)
}
