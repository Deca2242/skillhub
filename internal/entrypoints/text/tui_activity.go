package text

import (
	"fmt"
	"strings"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
)

// Activity block headings in the Skills panel and full skill detail view.
const (
	TUIActivityHeader = "Actividad en skillhub:"
	TUIActivityNone   = "Actividad en skillhub: sin actividad registrada"
)

var v1ActivityDid = map[domain.HistoryAction]string{
	domain.ActionApplied:   "Actualización aplicada",
	domain.ActionUndone:    "Actualización deshecha",
	domain.ActionSkipped:   "Actualización omitida",
	domain.ActionFailed:    "Falló la actualización",
	domain.ActionRecovered: "Actualización recuperada",
}

var v2ActivityDid = map[string]string{
	string(app.ActionDisable):   "Desactivada",
	string(app.ActionEnable):    "Activada",
	string(app.ActionUnlink):    "Quitada de un agente",
	string(app.ActionLink):      "Añadida a un agente",
	string(app.ActionRemove):    "Eliminada",
	string(app.ActionInstall):   "Instalada",
	string(app.ActionReinstall): "Reinstalada",
	string(app.ActionAdoptOff):  "Desactivada desde OFF/",
	"adopt_off":                 "Desactivada desde OFF/",
}

// TUIActivityLine formats one skill activity entry with its relative date.
func TUIActivityLine(e app.ActivityEntry, now time.Time) string {
	return "  • " + activityWhat(e) + " — " + TUIAgo(e.At, now)
}

func activityWhat(e app.ActivityEntry) string {
	if !e.V2 {
		if s, ok := v1ActivityDid[e.Action]; ok {
			return s
		}
		return HistoryAction(e.Action)
	}
	switch e.Action {
	case domain.ActionFailed:
		if label := activityActionLabel(e.Operation.Action); label != "" {
			return fmt.Sprintf("Falló «%s»", label)
		}
		return "Falló la operación"
	case domain.ActionRecovered:
		return "Operación recuperada"
	}
	if e.Operation.Undo {
		if label := activityActionLabel(e.Operation.Action); label != "" {
			return fmt.Sprintf("Deshiciste «%s»", label)
		}
		return "Operación deshecha"
	}
	if s, ok := v2ActivityDid[e.Operation.Action]; ok {
		return s
	}
	if r, ok := plainRepairs[e.Operation.Action]; ok {
		return "Reparada: " + strings.ToLower(r[:1]) + r[1:]
	}
	return "Operación aplicada"
}

func activityActionLabel(action string) string {
	if action == "" {
		return ""
	}
	if r, ok := plainRepairs[action]; ok {
		return strings.ToLower(r[:1]) + r[1:]
	}
	if name, ok := plainActions[action]; ok {
		return name
	}
	return SafeLine(action)
}
