package text

import (
	"fmt"
	"time"

	"skillhub/internal/app"
)

// TUIUpdatesNeverChecked is shown on the Actualizaciones tab when no
// successful check has ever completed.
const TUIUpdatesNeverChecked = "Todavía no se revisó si hay actualizaciones. Pulsa r para revisar ahora."

// TUIUpdatesNoChanges tells that no updates are pending since the last
// successful check at lastCheck, seen at now.
func TUIUpdatesNoChanges(lastCheck, now time.Time) string {
	return fmt.Sprintf("No hay cambios desde la última revisión (%s).", TUIAgo(lastCheck, now))
}

// TUIUpdatesUnchecked formats the skills that could not be checked for
// updates in plain Spanish, seen at now.
func TUIUpdatesUnchecked(items []app.UncheckedSkill, now time.Time) []string {
	if len(items) == 0 {
		return nil
	}
	lines := make([]string, 0, len(items)+1)
	lines = append(lines, "No se pudieron revisar:")
	for _, item := range items {
		lines = append(lines, uncheckedLine(item, now))
	}
	return lines
}

func uncheckedLine(item app.UncheckedSkill, now time.Time) string {
	name := SafeLine(item.Name)
	switch item.Reason {
	case app.UncheckedWebSource:
		return fmt.Sprintf("• %s — sin seguimiento (instalada desde un sitio web)", name)
	case app.UncheckedUntrackedOrigin:
		return fmt.Sprintf("• %s — sin seguimiento (origen fuera de GitHub)", name)
	case app.UncheckedNoOrigin:
		return fmt.Sprintf("• %s — no revisable (sin origen registrado)", name)
	case app.UncheckedCheckFailed:
		if !item.CheckedAt.IsZero() {
			return fmt.Sprintf("• %s — falló la revisión (%s)", name, TUIAgo(item.CheckedAt, now))
		}
		return fmt.Sprintf("• %s — falló la revisión", name)
	default:
		return fmt.Sprintf("• %s — no revisable", name)
	}
}
