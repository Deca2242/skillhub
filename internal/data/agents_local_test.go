package data_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/adapters/fs"
	"skillhub/internal/data"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func findAgent(agents []domain.AgentProfile, id string) (domain.AgentProfile, int) {
	for i, a := range agents {
		if a.ID == id {
			return a, i
		}
	}
	return domain.AgentProfile{}, -1
}

func TestLoadAgentsWithoutLocalFileIsTheEmbeddedTable(t *testing.T) {
	home := testsupport.NewHome(t)
	files := fs.NewFileOps(home.Paths().Guard())

	got, err := data.LoadAgents(home.Paths(), files)
	if err != nil {
		t.Fatalf("LoadAgents() error = %v", err)
	}
	want, err := data.Agents()
	if err != nil {
		t.Fatalf("Agents() error = %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("LoadAgents() without agents.local.toml differs from the embedded table:\n got %v\nwant %v", got, want)
	}
	if exists, _ := files.Exists(home.Paths().AgentsLocalFile()); exists {
		t.Errorf("LoadAgents created %s; it must only read", home.Paths().AgentsLocalFile())
	}
}

func TestLoadAgentsCorrectsByIDAndExtends(t *testing.T) {
	home := testsupport.NewHome(t)
	home.WriteFile(".config/skillhub/agents.local.toml", `
[[agent]]
id = "kilo"
global_dir = "~/.kilocode/skills"

[[agent]]
id = "codex"
global_dir = ".codex/skills"
reads_universal = false

[[agent]]
id = "claude-omni"
global_dir = "~/.claude-omni/skills"

[[agent]]
id = "cline"
global_dir = ".cline/skills"
`)
	before, err := os.ReadFile(home.Path(".config/skillhub/agents.local.toml"))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := data.Agents()
	if err != nil {
		t.Fatal(err)
	}

	got, err := data.LoadAgents(home.Paths(), fs.NewFileOps(home.Paths().Guard()))
	if err != nil {
		t.Fatalf("LoadAgents() error = %v", err)
	}

	if len(got) != len(embedded)+1 {
		t.Fatalf("len(LoadAgents()) = %d, want %d (embedded plus one new agent)", len(got), len(embedded)+1)
	}
	tests := []struct {
		id      string
		want    domain.AgentProfile
		wantIdx int
	}{
		// A corrected agent keeps its place; reads_universal left out keeps
		// the embedded value.
		{"kilo", domain.AgentProfile{ID: "kilo", GlobalDir: ".kilocode/skills", ReadsUniversal: false}, idx(t, embedded, "kilo")},
		{"codex", domain.AgentProfile{ID: "codex", GlobalDir: ".codex/skills", ReadsUniversal: false}, idx(t, embedded, "codex")},
		{"cline", domain.AgentProfile{ID: "cline", GlobalDir: ".cline/skills", ReadsUniversal: true}, idx(t, embedded, "cline")},
		// A new agent goes after the embedded table.
		{"claude-omni", domain.AgentProfile{ID: "claude-omni", GlobalDir: ".claude-omni/skills"}, len(embedded)},
		// An agent the file does not name is untouched.
		{"pi", domain.AgentProfile{ID: "pi", GlobalDir: ".agents/skills", ReadsUniversal: true}, idx(t, embedded, "pi")},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			a, i := findAgent(got, tt.id)
			if a != tt.want || i != tt.wantIdx {
				t.Errorf("agent %q = %+v at %d, want %+v at %d", tt.id, a, i, tt.want, tt.wantIdx)
			}
		})
	}

	after, err := os.ReadFile(home.Path(".config/skillhub/agents.local.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("LoadAgents changed agents.local.toml")
	}
}

func TestLoadAgentsKeepsReadsUniversalWhenCorrectingOnlyTheFlag(t *testing.T) {
	home := testsupport.NewHome(t)
	home.WriteFile(".config/skillhub/agents.local.toml", "[[agent]]\nid = \"claude-code\"\nglobal_dir = \".claude/skills\"\nreads_universal = true\n")

	got, err := data.LoadAgents(home.Paths(), fs.NewFileOps(home.Paths().Guard()))
	if err != nil {
		t.Fatalf("LoadAgents() error = %v", err)
	}
	a, _ := findAgent(got, "claude-code")
	if want := (domain.AgentProfile{ID: "claude-code", GlobalDir: ".claude/skills", ReadsUniversal: true}); a != want {
		t.Errorf("claude-code = %+v, want %+v", a, want)
	}
}

func idx(t *testing.T, agents []domain.AgentProfile, id string) int {
	t.Helper()
	_, i := findAgent(agents, id)
	if i < 0 {
		t.Fatalf("embedded table has no agent %q", id)
	}
	return i
}

func TestLoadAgentsRejectsAnInvalidFileNamingTheKey(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantKey   string
		wantInErr string
	}{
		{"unknown key in an agent", "[[agent]]\nid = \"codex\"\nglobal_dir = \".codex/skills\"\nglobal_dirs = \"x\"\n", "agent.global_dirs", ""},
		{"unknown top-level key", "agents = 1\n", "agents", ""},
		{"unknown top-level table", "[agentes]\nid = \"x\"\n", "agentes", ""},
		{"wrong type for global_dir", "[[agent]]\nid = \"codex\"\nglobal_dir = 3\n", "agent.global_dir", "must be a string"},
		{"wrong type for reads_universal", "[[agent]]\nid = \"codex\"\nglobal_dir = \".codex/skills\"\nreads_universal = \"yes\"\n", "agent.reads_universal", ""},
		{"wrong type for id", "[[agent]]\nid = 7\nglobal_dir = \".codex/skills\"\n", "agent.id", "must be a string"},
		{"agent is not an array of tables", "agent = \"codex\"\n", "agent", ""},
		{"missing id", "[[agent]]\nglobal_dir = \".codex/skills\"\n", "agent.id", ""},
		{"empty id", "[[agent]]\nid = \"\"\nglobal_dir = \".codex/skills\"\n", "agent.id", ""},
		{"missing global_dir", "[[agent]]\nid = \"codex\"\n", "agent.global_dir", ""},
		{"absolute global_dir", "[[agent]]\nid = \"codex\"\nglobal_dir = \"/opt/skills\"\n", "agent.global_dir", ""},
		{"global_dir climbs out of the home", "[[agent]]\nid = \"codex\"\nglobal_dir = \"~/../other/skills\"\n", "agent.global_dir", ""},
		{"global_dir is the home", "[[agent]]\nid = \"codex\"\nglobal_dir = \"~\"\n", "agent.global_dir", ""},
		{"duplicate id", "[[agent]]\nid = \"codex\"\nglobal_dir = \".codex/skills\"\n[[agent]]\nid = \"codex\"\nglobal_dir = \".codex/other\"\n", "agent.id", ""},
		{"invalid TOML", "[[agent]\nid = \"codex\"\n", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := testsupport.NewHome(t)
			home.WriteFile(".config/skillhub/agents.local.toml", tt.content)

			got, err := data.LoadAgents(home.Paths(), fs.NewFileOps(home.Paths().Guard()))
			if err == nil {
				t.Fatalf("LoadAgents() = %d agents, want an error", len(got))
			}
			if got != nil {
				t.Errorf("LoadAgents() returned %d agents along with the error, want none", len(got))
			}
			var le *data.AgentsLocalError
			if !errors.As(err, &le) {
				t.Fatalf("LoadAgents() error = %v (%T), want a *data.AgentsLocalError", err, err)
			}
			if le.Path != home.Paths().AgentsLocalFile() {
				t.Errorf("AgentsLocalError.Path = %q, want %q", le.Path, home.Paths().AgentsLocalFile())
			}
			if le.Key != tt.wantKey {
				t.Errorf("AgentsLocalError.Key = %q, want %q (error: %v)", le.Key, tt.wantKey, err)
			}
			if tt.wantKey != "" && !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("error %q does not name the key %q", err, tt.wantKey)
			}
			if !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("error %q does not say %q", err, tt.wantInErr)
			}
			if !strings.Contains(err.Error(), "agents.local.toml") {
				t.Errorf("error %q does not name the file", err)
			}
		})
	}
}

func TestLoadAgentsReportsAnUnreadableFile(t *testing.T) {
	home := testsupport.NewHome(t)
	files := fakes.NewFileOps(home.Paths().Guard())
	files.AddFile(home.Paths().AgentsLocalFile(), "", 0o644)
	boom := errors.New("disk on fire")
	files.Fail = func(c fakes.Call) error {
		if c.Op == "ReadFile" {
			return boom
		}
		return nil
	}

	got, err := data.LoadAgents(home.Paths(), files)
	if !errors.Is(err, boom) || got != nil {
		t.Fatalf("LoadAgents() = %v, %v; want nil and an error wrapping %v", got, err, boom)
	}
}

// TestAgentsLocalExample keeps the shipped example loadable and pins the
// corrections it makes for the machine in docs/research/local-inventory.md.
func TestAgentsLocalExample(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("agents.local.example.toml"))
	if err != nil {
		t.Fatalf("read the example: %v", err)
	}
	home := testsupport.NewHome(t)
	home.WriteFile(".config/skillhub/agents.local.toml", string(example))

	got, err := data.LoadAgents(home.Paths(), fs.NewFileOps(home.Paths().Guard()))
	if err != nil {
		t.Fatalf("LoadAgents(example) error = %v", err)
	}
	embedded, err := data.Agents()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(embedded) {
		t.Errorf("the example adds %d agents, want 0: it only corrects", len(got)-len(embedded))
	}
	for _, want := range []domain.AgentProfile{
		{ID: "codex", GlobalDir: ".codex/skills", ReadsUniversal: true},
		{ID: "pi", GlobalDir: ".pi/agent/skills", ReadsUniversal: false},
		{ID: "kilo", GlobalDir: ".kilocode/skills", ReadsUniversal: false},
	} {
		if a, _ := findAgent(got, want.ID); a != want {
			t.Errorf("example agent %q = %+v, want %+v", want.ID, a, want)
		}
	}
}
