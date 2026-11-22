package data_test

import (
	"path/filepath"
	"testing"

	"skillhub/internal/data"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

func TestAgentsIsTheSkillsTable(t *testing.T) {
	agents, err := data.Agents()
	if err != nil {
		t.Fatalf("Agents() error = %v", err)
	}
	if len(agents) != 77 {
		t.Errorf("len(Agents()) = %d, want 77 (one per --agent id of skills 1.9.0, project-only left out)", len(agents))
	}
	byID := map[string]domain.AgentProfile{}
	for _, a := range agents {
		if _, dup := byID[a.ID]; dup {
			t.Errorf("agent id %q appears twice", a.ID)
		}
		byID[a.ID] = a
		if !filepath.IsLocal(a.GlobalDir) {
			t.Errorf("agent %q global_dir %q is not a path inside the home", a.ID, a.GlobalDir)
		}
		if a.ReadsUniversal != (a.GlobalDir == ".agents/skills") {
			t.Errorf("agent %q reads_universal = %v with global_dir %q", a.ID, a.ReadsUniversal, a.GlobalDir)
		}
	}

	tests := []struct {
		id   string
		want domain.AgentProfile
	}{
		{"claude-code", domain.AgentProfile{ID: "claude-code", GlobalDir: ".claude/skills"}},
		{"codex", domain.AgentProfile{ID: "codex", GlobalDir: ".agents/skills", ReadsUniversal: true}},
		{"pi", domain.AgentProfile{ID: "pi", GlobalDir: ".agents/skills", ReadsUniversal: true}},
		{"kilo", domain.AgentProfile{ID: "kilo", GlobalDir: ".kilo/skills"}},
		{"amp", domain.AgentProfile{ID: "amp", GlobalDir: ".config/agents/skills"}},
		{"antigravity-cli", domain.AgentProfile{ID: "antigravity-cli", GlobalDir: ".gemini/config/skills"}},
		{"opencode", domain.AgentProfile{ID: "opencode", GlobalDir: ".config/opencode/skills"}},
		{"cortex", domain.AgentProfile{ID: "cortex", GlobalDir: ".snowflake/cortex/skills"}},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := byID[tt.id]; got != tt.want {
				t.Errorf("agent %q = %+v, want %+v", tt.id, got, tt.want)
			}
		})
	}
	for _, projectOnly := range []string{"eve", "promptscript"} {
		if _, ok := byID[projectOnly]; ok {
			t.Errorf("project-only agent %q is in the table", projectOnly)
		}
	}
}

func TestAgentsReturnsACopy(t *testing.T) {
	first, err := data.Agents()
	if err != nil {
		t.Fatal(err)
	}
	first[0].GlobalDir = "changed"
	second, err := data.Agents()
	if err != nil {
		t.Fatal(err)
	}
	if second[0].GlobalDir == "changed" {
		t.Error("Agents() returned shared state; a caller's change leaked into the next call")
	}
}

func TestAgentsSourceNamesTheCapturedPackage(t *testing.T) {
	src := data.AgentsSource()
	if src.Package != "skills" || src.Version != "1.9.0" || src.File != "node_modules/skills/docs/agents.md" {
		t.Errorf("AgentsSource() = %+v, want skills 1.9.0 docs/agents.md", src)
	}
}

func TestAgentNamesAreTheDisplayNames(t *testing.T) {
	names, err := data.AgentNames()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"opencode": "OpenCode", "github-copilot": "GitHub Copilot", "claude-code": "Claude Code"} {
		if names[id] != want {
			t.Errorf("AgentNames()[%q] = %q, want %q", id, names[id], want)
		}
	}
}
