package text_test

import (
	"strings"
	"testing"

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
