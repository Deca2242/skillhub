package text_test

import (
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// RecoveryV1 tells what recover did with a version 1 operation, in the
// words of «skillhub recover», with no English left in it.
func TestRecoveryV1TellsWhatRecoverDid(t *testing.T) {
	op := domain.Operation{ID: "op-1", Target: "lock:alpha", RestoreSnapshotID: "s1"}
	tests := []struct {
		name      string
		res       updates.RecoverResult
		leftovers bool
		want      string
	}{
		{name: "restored", res: updates.RecoverResult{Operation: op, Restored: true},
			want: "Se recuperó lock:alpha: se restauró la versión anterior desde el snapshot s1.\n"},
		{name: "nothing changed yet", res: updates.RecoverResult{Operation: op},
			want: "La operación sobre lock:alpha no llegó a cambiar nada; se descartaron sus restos.\n"},
		{name: "an apply leaves its update pending",
			res: updates.RecoverResult{Operation: domain.Operation{ID: "op-1", Target: "lock:alpha", RestoreSnapshotID: "s1", CandidateID: "c1"}, Restored: true},
			want: "Se recuperó lock:alpha: se restauró la versión anterior desde el snapshot s1.\n" +
				"La actualización sigue pendiente y puedes volver a aprobarla.\n"},
		{name: "a note of the lock, in Spanish",
			res: updates.RecoverResult{Operation: op, Restored: true, Detail: "lock entry removed by another tool; not recreated"},
			want: "Se recuperó lock:alpha: se restauró la versión anterior desde el snapshot s1.\n" +
				"Nota: otra herramienta quitó la entrada del lock; no se volvió a crear\n"},
		{name: "the install staging cleared too", res: updates.RecoverResult{Operation: op, Restored: true}, leftovers: true,
			want: "Se recuperó lock:alpha: se restauró la versión anterior desde el snapshot s1.\n" +
				"Además se eliminó la copia de una instalación sin terminar.\n"},
		{name: "one incomplete snapshot",
			res:  updates.RecoverResult{IncompleteSnapshots: 1, Detail: "removed 1 incomplete snapshots"},
			want: "No había ninguna operación interrumpida; se eliminaron restos incompletos: 1 snapshot incompleto.\n"},
		{name: "snapshots and the staging", res: updates.RecoverResult{IncompleteSnapshots: 2}, leftovers: true,
			want: "No había ninguna operación interrumpida; se eliminaron restos incompletos: 2 snapshots incompletos y la copia de una instalación sin terminar.\n"},
		{name: "only the staging", leftovers: true,
			want: "No había ninguna operación interrumpida; se eliminaron restos incompletos: la copia de una instalación sin terminar.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := text.RecoveryV1(tt.res, tt.leftovers); got != tt.want {
				t.Errorf("RecoveryV1 =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// Each note a version 1 restore leaves about the lock has a Spanish
// sentence; anything else is shown as it came.
func TestRecoverNoteIsSpanish(t *testing.T) {
	tests := map[string]string{
		"lock entry removed by another tool; not recreated":  "otra herramienta quitó la entrada del lock; no se volvió a crear",
		"lock entry ref changed by another tool; left as is": "otra herramienta cambió el ref de la entrada del lock; se dejó como estaba",
		"lock entry rewritten by another tool; left as is":   "otra herramienta reescribió la entrada del lock; se dejó como estaba",
		"something new": "something new",
	}
	for in, want := range tests {
		if got := text.RecoverNote(in); got != want {
			t.Errorf("RecoverNote(%q) = %q, want %q", in, got, want)
		}
	}
}

// Recovered dispatches on the version recover resolved.
func TestRecoveredDispatchesOnTheVersion(t *testing.T) {
	v2 := app.ManagedRecovery{Found: true, RolledBack: true, PlanID: "p1"}
	if got, want := text.Recovered(app.Recovery{Version: 2, V2: v2}), text.Recovery(v2); got != want {
		t.Errorf("version 2 = %q, want %q", got, want)
	}
	v1 := updates.RecoverResult{Operation: domain.Operation{ID: "op", Target: "lock:a", RestoreSnapshotID: "s"}, Restored: true}
	if got, want := text.Recovered(app.Recovery{Version: 1, V1: v1, Leftovers: true}), text.RecoveryV1(v1, true); got != want {
		t.Errorf("version 1 = %q, want %q", got, want)
	}
	if got := text.Recovery(app.ManagedRecovery{}); got != text.RecoverNoOp {
		t.Errorf("a version 2 recover with no record = %q, want %q", got, text.RecoverNoOp)
	}
}
