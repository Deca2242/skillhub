package text_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"skillhub/internal/entrypoints/text"
)

func TestTUISkillsVisibilitySummary(t *testing.T) {
	tests := []struct {
		name string
		c    text.SkillVisibilityCounts
		want string
	}{
		{
			name: "all visible",
			c:    text.SkillVisibilityCounts{Visible: 10, Hidden: 0},
			want: "Visibles: 10 · sin agente: 0",
		},
		{
			name: "disabled still visible singular",
			c:    text.SkillVisibilityCounts{Visible: 5, Hidden: 1, DisabledVisible: 1},
			want: "Visibles: 5 · sin agente: 1 (1 desactivada visible)",
		},
		{
			name: "disabled still visible plural and active hidden singular",
			c:    text.SkillVisibilityCounts{Visible: 12, Hidden: 2, ActiveHidden: 1, DisabledVisible: 3},
			want: "Visibles: 12 · sin agente: 2 (3 desactivadas visibles, 1 activa sin agente)",
		},
		{
			name: "active hidden plural",
			c:    text.SkillVisibilityCounts{Visible: 0, Hidden: 2, ActiveHidden: 2},
			want: "Visibles: 0 · sin agente: 2 (2 activas sin agente)",
		},
		{
			name: "drops notes if over 80 columns",
			c:    text.SkillVisibilityCounts{Visible: 1234, Hidden: 5678, ActiveHidden: 123, DisabledVisible: 456},
			want: "Visibles: 1234 · sin agente: 5678",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := text.TUISkillsVisibilitySummary(tt.c)
			if got != tt.want {
				t.Errorf("TUISkillsVisibilitySummary(%+v) = %q, want %q", tt.c, got, tt.want)
			}
			if w := utf8.RuneCountInString(got); w > 80 {
				t.Errorf("summary width = %d, want <= 80: %q", w, got)
			}
			if w := internal.FindString(got); w != "" {
				t.Errorf("summary uses internal word %q: %q", w, got)
			}
		})
	}
}

func TestTUIPanelSkillhubStateAndExclusions(t *testing.T) {
	for _, state := range []string{
		text.TUIStateActive,
		text.TUIStateDisabled,
		text.TUIStateDisabledHand,
		text.TUIStateIgnored,
	} {
		got := text.TUIPanelSkillhubState(state)
		if !strings.HasPrefix(got, "En skillhub: ") || !strings.Contains(got, state) {
			t.Errorf("TUIPanelSkillhubState(%q) = %q", state, got)
		}
	}

	for _, want := range []string{"proveedor", "plugins de Claude Code", "gentle-ai", "proyecto"} {
		if !strings.Contains(text.TUISkillsExclusions, want) {
			t.Errorf("TUISkillsExclusions lacks %q: %q", want, text.TUISkillsExclusions)
		}
	}
	if w := internal.FindString(text.TUISkillsExclusions); w != "" {
		t.Errorf("TUISkillsExclusions uses internal word %q: %q", w, text.TUISkillsExclusions)
	}
}

func TestTUISightingReasonAndPanelWhyVisible(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"direct", "directamente, sin enlace"},
		{"link", "por un enlace"},
		{"copy", "por una copia propia"},
		{"indeterminate", "indeterminado"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			reason := text.TUISightingReason(tt.kind)
			if !strings.Contains(reason, tt.want) {
				t.Errorf("TUISightingReason(%q) = %q, want substring %q", tt.kind, reason, tt.want)
			}
			if w := internal.FindString(reason); w != "" {
				t.Errorf("TUISightingReason(%q) uses internal word %q: %q", tt.kind, w, reason)
			}
			if strings.Contains(strings.ToLower(reason), "cargó") || strings.Contains(strings.ToLower(reason), "sesión") {
				t.Errorf("TUISightingReason(%q) claims session loaded skill: %q", tt.kind, reason)
			}
		})
	}

	line := text.TUIPanelWhyVisible("Codex y OpenCode", text.TUISightingReason("link"))
	if line != "  Codex y OpenCode: por un enlace" {
		t.Errorf("TUIPanelWhyVisible = %q, want %q", line, "  Codex y OpenCode: por un enlace")
	}
}
