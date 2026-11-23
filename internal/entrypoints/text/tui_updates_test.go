package text_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func TestTUIUpdatesNeverChecked(t *testing.T) {
	got := text.TUIUpdatesNeverChecked
	lower := strings.ToLower(got)
	if !strings.Contains(lower, "todavía no se revisó") {
		t.Errorf("TUIUpdatesNeverChecked = %q, want it to say it has not been checked yet", got)
	}
	if !strings.Contains(got, "Pulsa r") {
		t.Errorf("TUIUpdatesNeverChecked = %q, want it to tell the user to press r", got)
	}
}

func TestTUIUpdatesNoChanges(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		lastCheck time.Time
		want      string
	}{
		{
			name:      "just now",
			lastCheck: now.Add(-30 * time.Second),
			want:      "No hay cambios desde la última revisión (hace un momento).",
		},
		{
			name:      "two hours ago",
			lastCheck: now.Add(-2 * time.Hour),
			want:      "No hay cambios desde la última revisión (hace 2 horas).",
		},
		{
			name:      "three days ago",
			lastCheck: now.Add(-3 * 24 * time.Hour),
			want:      "No hay cambios desde la última revisión (hace 3 días).",
		},
		{
			name:      "older than a month",
			lastCheck: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
			want:      "No hay cambios desde la última revisión (el 2026-10-01).",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := text.TUIUpdatesNoChanges(tt.lastCheck, now); got != tt.want {
				t.Errorf("TUIUpdatesNoChanges() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTUIUpdatesUnchecked(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	items := []app.UncheckedSkill{
		{ID: "lock:ui-taste", Name: "ui-taste", Reason: app.UncheckedWebSource},
		{ID: "dir:.claude/skills/manual", Name: "manual", Reason: app.UncheckedNoOrigin},
		{ID: "git:other", Name: "other", Reason: app.UncheckedUntrackedOrigin},
		{ID: "lock:alpha", Name: "alpha", Reason: app.UncheckedCheckFailed, CheckedAt: now.Add(-2 * time.Hour)},
		{ID: "lock:beta", Name: "beta", Reason: app.UncheckedCheckFailed},
	}

	got := text.TUIUpdatesUnchecked(items, now)
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"No se pudieron revisar:",
		"ui-taste",
		"sin seguimiento",
		"manual",
		"no revisable",
		"other",
		"alpha",
		"hace 2 horas",
		"beta",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("want %q in TUIUpdatesUnchecked output:\n%s", want, joined)
		}
	}
	if slices.Contains(got, "") {
		t.Errorf("unexpected empty line in TUIUpdatesUnchecked: %v", got)
	}
}
