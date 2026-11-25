package text_test

import (
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func TestTUIAgentFilterItem(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		count int
		want  string
	}{
		{name: "zero skills", agent: "Kiro", count: 0, want: "Kiro (0 · sin skills)"},
		{name: "positive skills", agent: "Claude Code", count: 12, want: "Claude Code (12)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := text.TUIAgentFilterItem(tt.agent, tt.count); got != tt.want {
				t.Errorf("TUIAgentFilterItem(%q, %d) = %q, want %q", tt.agent, tt.count, got, tt.want)
			}
		})
	}
}

func TestTUIAgentFilterUndetected(t *testing.T) {
	tests := []struct {
		name      string
		count     int
		wantEmpty bool
		wantParts []string
	}{
		{name: "zero", count: 0, wantEmpty: true},
		{name: "one", count: 1, wantParts: []string{"1", "no detectado"}},
		{name: "many", count: 64, wantParts: []string{"64", "no detectado"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := text.TUIAgentFilterUndetected(tt.count)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("TUIAgentFilterUndetected(0) = %q, want empty", got)
				}
				return
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("TUIAgentFilterUndetected(%d) = %q, lacks %q", tt.count, got, part)
				}
			}
		})
	}
}

func TestTUIGuideProvider(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		wantParts   []string
		forbidParts []string
	}{
		{
			name:     "claude plugin distinguishes plugin from single skill",
			provider: app.ProviderClaudePlugin,
			wantParts: []string{
				"Gestionada por un plugin de Claude Code",
				"skillhub no puede eliminar",
				"enabledPlugins",
				"claude plugin list",
				"claude plugin uninstall <plugin>",
				"plugin completo",
			},
		},
		{
			name:     "gentle-ai avoids uninstall and sync and points to docs/config",
			provider: app.ProviderGentleAI,
			wantParts: []string{
				"Gestionada por gentle-ai",
				"skillhub no puede eliminarla",
				"gentle-ai no ofrece una acción verificada para quitar una sola skill",
				"documentación",
				"state.json",
				"conjunto",
			},
			forbidParts: []string{
				"uninstall",
				"sync",
			},
		},
		{
			name:     "claude synced points to Claude Code / claude.ai surface",
			provider: app.ProviderClaudeSynced,
			wantParts: []string{
				"Gestionada por Claude Code",
				"skillhub no puede eliminarla",
				"interfaz o la configuración de Claude Code / claude.ai",
			},
		},
		{
			name:     "cursor points to Cursor settings or docs",
			provider: app.ProviderCursor,
			wantParts: []string{
				"Gestionada por Cursor",
				"skillhub no puede eliminarla",
				"ajustes o la documentación de Cursor",
			},
		},
		{
			name:     "codex names config.toml or plugins surface",
			provider: app.ProviderCodex,
			wantParts: []string{
				"Gestionada por Codex",
				"skillhub no puede eliminarla",
				"config.toml",
				"[[skills.config]]",
				"plugins",
			},
		},
		{
			name:     "unknown provider fallback",
			provider: "other",
			wantParts: []string{
				"Gestionada por otro gestor",
				"skillhub no puede eliminarla",
				"interfaz o la documentación",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := text.TUIGuideProvider(tt.provider)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("TUIGuideProvider(%q) = %q, lacks %q", tt.provider, got, part)
				}
			}
			for _, forbid := range tt.forbidParts {
				if strings.Contains(strings.ToLower(got), strings.ToLower(forbid)) {
					t.Errorf("TUIGuideProvider(%q) = %q, must not contain %q", tt.provider, got, forbid)
				}
			}
		})
	}
}
