package text_test

import (
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

func TestTUIActivityLines(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	if !strings.Contains(strings.ToLower(text.TUIActivityNone), "sin actividad") {
		t.Errorf("TUIActivityNone = %q, want it to say there is no activity", text.TUIActivityNone)
	}
	if !strings.Contains(text.TUIActivityHeader, "Actividad en skillhub") {
		t.Errorf("TUIActivityHeader = %q, want Actividad en skillhub", text.TUIActivityHeader)
	}

	tests := []struct {
		name      string
		entry     app.ActivityEntry
		wantParts []string
	}{
		{
			name: "v1 applied",
			entry: app.ActivityEntry{
				At:     now.Add(-10 * time.Minute),
				Action: domain.ActionApplied,
				Target: "lock:alpha",
			},
			wantParts: []string{"Actualización aplicada", "hace 10 minutos"},
		},
		{
			name: "v1 undone",
			entry: app.ActivityEntry{
				At:     now.Add(-5 * time.Minute),
				Action: domain.ActionUndone,
				Target: "lock:alpha",
			},
			wantParts: []string{"Actualización deshecha", "hace 5 minutos"},
		},
		{
			name: "v1 failed",
			entry: app.ActivityEntry{
				At:     now.Add(-2 * time.Hour),
				Action: domain.ActionFailed,
				Target: "lock:alpha",
			},
			wantParts: []string{"Falló la actualización", "hace 2 horas"},
		},
		{
			name: "v2 disable",
			entry: app.ActivityEntry{
				At:        now.Add(-1 * time.Hour),
				Action:    domain.ActionApplied,
				Target:    "v2:disable",
				V2:        true,
				Operation: app.OperationView{Action: string(app.ActionDisable), Keys: []string{"alpha"}},
			},
			wantParts: []string{"Desactivada", "hace 1 hora"},
		},
		{
			name: "v2 undo disable",
			entry: app.ActivityEntry{
				At:        now.Add(-30 * time.Second),
				Action:    domain.ActionUndone,
				Target:    "v2:undo",
				V2:        true,
				Operation: app.OperationView{Action: string(app.ActionDisable), Keys: []string{"alpha"}, Undo: true},
			},
			wantParts: []string{"Deshiciste", "desactivar", "hace un momento"},
		},
		{
			name: "v2 failed",
			entry: app.ActivityEntry{
				At:        now.Add(-3 * time.Minute),
				Action:    domain.ActionFailed,
				Target:    "v2:link",
				V2:        true,
				Operation: app.OperationView{Action: string(app.ActionLink), Keys: []string{"alpha"}},
			},
			wantParts: []string{"Falló", "hace 3 minutos"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := text.TUIActivityLine(tt.entry, now)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("TUIActivityLine = %q, want %q", got, part)
				}
			}
		})
	}
}
