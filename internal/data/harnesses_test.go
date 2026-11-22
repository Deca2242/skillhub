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
	"skillhub/internal/testsupport"
)

func findHarness(t *testing.T, hs []data.Harness, id string) data.Harness {
	t.Helper()
	for _, h := range hs {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("harness %q not found", id)
	return data.Harness{}
}

func equalHarness(a, b data.Harness) bool {
	return a.ID == b.ID && a.GlobalDir == b.GlobalDir && a.ReadsStore == b.ReadsStore &&
		a.LinkDir == b.LinkDir && a.FollowsLinks == b.FollowsLinks &&
		slices.Equal(a.ReadRoots, b.ReadRoots) && slices.Equal(a.SharesDirWith, b.SharesDirWith)
}

// TestHarnessesDescribeTheVerifiedFacts pins the version 2 columns to the
// facts of docs/research/harnesses-v2.md; anything not verified there is
// unknown, never a guessed value.
func TestHarnessesDescribeTheVerifiedFacts(t *testing.T) {
	hs, err := data.Harnesses()
	if err != nil {
		t.Fatalf("Harnesses() error = %v", err)
	}
	unknownRoot := func(dir string) data.ReadRoot { return data.ReadRoot{Dir: dir, Depth: data.DepthUnknown} }
	tests := []data.Harness{
		{
			ID: "claude-code", GlobalDir: ".claude/skills", ReadsStore: data.No, LinkDir: ".claude/skills",
			ReadRoots:    []data.ReadRoot{{Dir: ".claude/skills", Depth: 1}},
			FollowsLinks: data.Yes, SharesDirWith: []string{"opencode"},
		},
		{
			ID: "codex", GlobalDir: ".agents/skills", ReadsStore: data.Yes,
			ReadRoots:    []data.ReadRoot{{Dir: ".agents/skills", Depth: 6}, unknownRoot(".codex/skills")},
			FollowsLinks: data.Yes,
		},
		{
			ID: "opencode", GlobalDir: ".config/opencode/skills", ReadsStore: data.Yes,
			ReadRoots: []data.ReadRoot{
				{Dir: ".config/opencode/skills", Depth: 6}, {Dir: ".claude/skills", Depth: 6}, {Dir: ".agents/skills", Depth: 6},
			},
			FollowsLinks: data.Unknown, SharesDirWith: []string{"claude-code"},
		},
		{
			ID: "pi", GlobalDir: ".agents/skills", ReadsStore: data.Yes,
			ReadRoots: []data.ReadRoot{
				{Dir: ".pi/agent/skills", Depth: data.DepthUnlimited}, {Dir: ".agents/skills", Depth: data.DepthUnlimited},
			},
			FollowsLinks: data.Yes,
		},
		{
			ID: "gemini-cli", GlobalDir: ".gemini/skills", ReadsStore: data.Yes,
			ReadRoots:    []data.ReadRoot{{Dir: ".gemini/skills", Depth: 1}, {Dir: ".agents/skills", Depth: 1}},
			FollowsLinks: data.Unknown,
		},
		// Antigravity and Antigravity CLI share one directory; npx skills
		// links there because neither reads the store.
		{
			ID: "antigravity", GlobalDir: ".gemini/config/skills", ReadsStore: data.No, LinkDir: ".gemini/config/skills",
			ReadRoots: []data.ReadRoot{unknownRoot(".gemini/config/skills")}, FollowsLinks: data.Unknown,
			SharesDirWith: []string{"antigravity-cli"},
		},
		{
			ID: "antigravity-cli", GlobalDir: ".gemini/config/skills", ReadsStore: data.No, LinkDir: ".gemini/config/skills",
			ReadRoots: []data.ReadRoot{unknownRoot(".gemini/config/skills")}, FollowsLinks: data.Unknown,
			SharesDirWith: []string{"antigravity"},
		},
		// Universal by npx skills' rule, how it reads is not verified.
		{
			ID: "kilo", GlobalDir: ".kilo/skills", ReadsStore: data.Yes,
			ReadRoots:    []data.ReadRoot{unknownRoot(".kilo/skills"), unknownRoot(".agents/skills")},
			FollowsLinks: data.Unknown,
		},
		{
			ID: "kiro-cli", GlobalDir: ".kiro/skills", ReadsStore: data.No, LinkDir: ".kiro/skills",
			ReadRoots: []data.ReadRoot{unknownRoot(".kiro/skills")}, FollowsLinks: data.Unknown,
		},
		// Its documented global directory is the store, so it reads it;
		// how deep is not verified.
		{
			ID: "dexto", GlobalDir: ".agents/skills", ReadsStore: data.Yes,
			ReadRoots: []data.ReadRoot{unknownRoot(".agents/skills")}, FollowsLinks: data.Unknown,
		},
		// Nothing verified at all.
		{
			ID: "cortex", GlobalDir: ".snowflake/cortex/skills", ReadsStore: data.Unknown,
			ReadRoots: []data.ReadRoot{unknownRoot(".snowflake/cortex/skills")}, FollowsLinks: data.Unknown,
		},
	}
	for _, want := range tests {
		t.Run(want.ID, func(t *testing.T) {
			if got := findHarness(t, hs, want.ID); !equalHarness(got, want) {
				t.Errorf("harness %q =\n %+v\nwant\n %+v", want.ID, got, want)
			}
		})
	}
}

func TestHarnessesNameEveryUniversalAndLinkedHarnessOfTheResearch(t *testing.T) {
	hs, err := data.Harnesses()
	if err != nil {
		t.Fatal(err)
	}
	// harnesses-v2.md section 1.
	for _, id := range []string{"codex", "cursor", "gemini-cli", "opencode", "pi", "kilo", "droid", "github-copilot", "cline", "zed", "warp", "amp"} {
		if h := findHarness(t, hs, id); h.ReadsStore != data.Yes || h.LinkDir != "" {
			t.Errorf("universal harness %q: reads_store = %q, link_dir = %q; want yes and none", id, h.ReadsStore, h.LinkDir)
		}
	}
	for _, id := range []string{"claude-code", "kiro-cli", "qwen-code", "junie", "devin", "windsurf", "antigravity", "antigravity-cli"} {
		if h := findHarness(t, hs, id); h.ReadsStore != data.No || h.LinkDir != h.GlobalDir {
			t.Errorf("linked harness %q: reads_store = %q, link_dir = %q; want no and %q", id, h.ReadsStore, h.LinkDir, h.GlobalDir)
		}
	}
}

func TestHarnessesKeepTheAgentsTableAndItsInvariants(t *testing.T) {
	hs, err := data.Harnesses()
	if err != nil {
		t.Fatal(err)
	}
	agents, err := data.Agents()
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != len(agents) {
		t.Fatalf("len(Harnesses()) = %d, want %d, one per agent", len(hs), len(agents))
	}
	for i, h := range hs {
		if h.ID != agents[i].ID || h.GlobalDir != agents[i].GlobalDir {
			t.Errorf("harness %d = %s %s, want the agent %s %s", i, h.ID, h.GlobalDir, agents[i].ID, agents[i].GlobalDir)
		}
		if (h.ReadsStore == data.No) != (h.LinkDir != "") {
			t.Errorf("harness %q: reads_store = %q with link_dir %q; a link dir exists exactly when it does not read the store", h.ID, h.ReadsStore, h.LinkDir)
		}
		if len(h.ReadRoots) == 0 {
			t.Errorf("harness %q has no read root", h.ID)
		}
		for _, r := range h.ReadRoots {
			if !filepath.IsLocal(r.Dir) {
				t.Errorf("harness %q read root %q is not inside the home", h.ID, r.Dir)
			}
		}
		for _, tri := range []data.Tri{h.ReadsStore, h.FollowsLinks} {
			if tri != data.Yes && tri != data.No && tri != data.Unknown {
				t.Errorf("harness %q has the value %q, want yes, no or unknown", h.ID, tri)
			}
		}
	}
}

func TestHarnessesReturnsACopy(t *testing.T) {
	first, err := data.Harnesses()
	if err != nil {
		t.Fatal(err)
	}
	first[0].ReadRoots[0].Dir = "changed"
	first[0].SharesDirWith = append(first[0].SharesDirWith, "changed")
	second, err := data.Harnesses()
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ReadRoots[0].Dir == "changed" || slices.Contains(second[0].SharesDirWith, "changed") {
		t.Error("Harnesses() returned shared state; a caller's change leaked into the next call")
	}
}

func TestDiagnosticProfilesHoldClaudeOmni(t *testing.T) {
	got, err := data.DiagnosticProfiles()
	if err != nil {
		t.Fatal(err)
	}
	want := []data.DiagnosticProfile{{
		ID: "claude-omni", Harness: "claude-code",
		ReadRoots: []data.ReadRoot{{Dir: ".claude-omni/skills", Depth: 1}},
	}}
	if len(got) != len(want) || got[0].ID != want[0].ID || got[0].Harness != want[0].Harness || !slices.Equal(got[0].ReadRoots, want[0].ReadRoots) {
		t.Errorf("DiagnosticProfiles() = %+v, want %+v", got, want)
	}
}

func TestLoadHarnessesWithoutLocalFileIsTheEmbeddedTable(t *testing.T) {
	home := testsupport.NewHome(t)
	got, err := data.LoadHarnesses(home.Paths(), fs.NewFileOps(home.Paths().Guard()))
	if err != nil {
		t.Fatalf("LoadHarnesses() error = %v", err)
	}
	want, err := data.Harnesses()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(got, want, equalHarness) {
		t.Errorf("LoadHarnesses() without agents.local.toml differs from the embedded table")
	}
}

const v2Overlay = `
# A version 1 row: its derived columns follow the new global_dir.
[[agent]]
id = "kilo"
global_dir = "~/.kilocode/skills"

# Explicit read roots stay when only global_dir changes.
[[agent]]
id = "codex"
global_dir = "~/.codex/skills"
reads_universal = true

# Every version 2 key.
[[agent]]
id = "cortex"
global_dir = "~/.snowflake/cortex/skills"
reads_store = "no"
link_dir = "~/.snowflake/links"
follows_links = "yes"

[[agent.read_roots]]
dir = "~/.snowflake/cortex/skills"
depth = 1

[[agent.read_roots]]
dir = ".snowflake/deep"
depth = "unlimited"

[[agent.read_roots]]
dir = ".snowflake/unsure"
depth = "unknown"

# A new agent that only names its directory: nothing verified.
[[agent]]
id = "newcomer"
global_dir = ".newcomer/skills"

# A new agent sharing the Antigravity directory.
[[agent]]
id = "antigravity-next"
global_dir = ".gemini/config/skills"
reads_store = "no"
`

func TestLoadHarnessesAppliesTheVersion2Keys(t *testing.T) {
	home := testsupport.NewHome(t)
	home.WriteFile(".config/skillhub/agents.local.toml", v2Overlay)
	before, err := os.ReadFile(home.Paths().AgentsLocalFile())
	if err != nil {
		t.Fatal(err)
	}

	got, err := data.LoadHarnesses(home.Paths(), fs.NewFileOps(home.Paths().Guard()))
	if err != nil {
		t.Fatalf("LoadHarnesses() error = %v", err)
	}
	unknownRoot := func(dir string) data.ReadRoot { return data.ReadRoot{Dir: dir, Depth: data.DepthUnknown} }
	tests := []data.Harness{
		{
			ID: "kilo", GlobalDir: ".kilocode/skills", ReadsStore: data.Yes,
			ReadRoots:    []data.ReadRoot{unknownRoot(".kilocode/skills"), unknownRoot(".agents/skills")},
			FollowsLinks: data.Unknown,
		},
		{
			ID: "codex", GlobalDir: ".codex/skills", ReadsStore: data.Yes,
			ReadRoots:    []data.ReadRoot{{Dir: ".agents/skills", Depth: 6}, unknownRoot(".codex/skills")},
			FollowsLinks: data.Yes,
		},
		{
			ID: "cortex", GlobalDir: ".snowflake/cortex/skills", ReadsStore: data.No, LinkDir: ".snowflake/links",
			ReadRoots: []data.ReadRoot{
				{Dir: ".snowflake/cortex/skills", Depth: 1}, {Dir: ".snowflake/deep", Depth: data.DepthUnlimited}, unknownRoot(".snowflake/unsure"),
			},
			FollowsLinks: data.Yes,
		},
		{
			ID: "newcomer", GlobalDir: ".newcomer/skills", ReadsStore: data.Unknown,
			ReadRoots: []data.ReadRoot{unknownRoot(".newcomer/skills")}, FollowsLinks: data.Unknown,
		},
		{
			ID: "antigravity", GlobalDir: ".gemini/config/skills", ReadsStore: data.No, LinkDir: ".gemini/config/skills",
			ReadRoots: []data.ReadRoot{unknownRoot(".gemini/config/skills")}, FollowsLinks: data.Unknown,
			SharesDirWith: []string{"antigravity-cli", "antigravity-next"},
		},
		{
			ID: "antigravity-next", GlobalDir: ".gemini/config/skills", ReadsStore: data.No, LinkDir: ".gemini/config/skills",
			ReadRoots: []data.ReadRoot{unknownRoot(".gemini/config/skills")}, FollowsLinks: data.Unknown,
			SharesDirWith: []string{"antigravity", "antigravity-cli"},
		},
	}
	for _, want := range tests {
		t.Run(want.ID, func(t *testing.T) {
			if h := findHarness(t, got, want.ID); !equalHarness(h, want) {
				t.Errorf("harness %q =\n %+v\nwant\n %+v", want.ID, h, want)
			}
		})
	}

	// The version 1 loader reads the same file: the new keys are accepted
	// and ignored there.
	agents, err := data.LoadAgents(home.Paths(), fs.NewFileOps(home.Paths().Guard()))
	if err != nil {
		t.Fatalf("LoadAgents() with version 2 keys error = %v", err)
	}
	if a, _ := findAgent(agents, "cortex"); a.GlobalDir != ".snowflake/cortex/skills" || a.ReadsUniversal {
		t.Errorf("LoadAgents() cortex = %+v", a)
	}

	after, err := os.ReadFile(home.Paths().AgentsLocalFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("LoadHarnesses changed agents.local.toml")
	}
}

func TestLoadHarnessesRejectsInvalidVersion2Keys(t *testing.T) {
	row := "[[agent]]\nid = \"codex\"\nglobal_dir = \".codex/skills\"\n"
	tests := []struct {
		name    string
		content string
		wantKey string
	}{
		{"reads_store outside yes, no, unknown", row + "reads_store = \"maybe\"\n", "agent.reads_store"},
		{"reads_store as a boolean", row + "reads_store = true\n", "agent.reads_store"},
		{"follows_links outside yes, no, unknown", row + "follows_links = \"sometimes\"\n", "agent.follows_links"},
		{"absolute link_dir", row + "reads_store = \"no\"\nlink_dir = \"/opt/links\"\n", "agent.link_dir"},
		{"link_dir for a harness that reads the store", row + "reads_store = \"yes\"\nlink_dir = \".codex/links\"\n", "agent.link_dir"},
		{"link_dir while reads_store is unknown", "[[agent]]\nid = \"cortex\"\nglobal_dir = \".x/skills\"\nlink_dir = \".x/skills\"\n", "agent.link_dir"},
		{"read_roots is not an array of tables", row + "read_roots = \".codex/skills\"\n", "agent.read_roots"},
		{"read root without dir", row + "[[agent.read_roots]]\ndepth = 1\n", "agent.read_roots.dir"},
		{"read root outside the home", row + "[[agent.read_roots]]\ndir = \"~/../x\"\ndepth = 1\n", "agent.read_roots.dir"},
		{"read root without depth", row + "[[agent.read_roots]]\ndir = \".x\"\n", "agent.read_roots.depth"},
		{"depth zero", row + "[[agent.read_roots]]\ndir = \".x\"\ndepth = 0\n", "agent.read_roots.depth"},
		{"unknown depth word", row + "[[agent.read_roots]]\ndir = \".x\"\ndepth = \"deep\"\n", "agent.read_roots.depth"},
		{"unknown key in a read root", row + "[[agent.read_roots]]\ndir = \".x\"\ndepth = 1\nfollow = true\n", "agent.read_roots.follow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := testsupport.NewHome(t)
			home.WriteFile(".config/skillhub/agents.local.toml", tt.content)
			files := fs.NewFileOps(home.Paths().Guard())

			got, err := data.LoadHarnesses(home.Paths(), files)
			if err == nil || got != nil {
				t.Fatalf("LoadHarnesses() = %d harnesses, %v; want none and an error", len(got), err)
			}
			var le *data.AgentsLocalError
			if !errors.As(err, &le) {
				t.Fatalf("LoadHarnesses() error = %v (%T), want a *data.AgentsLocalError", err, err)
			}
			if le.Key != tt.wantKey || !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("AgentsLocalError.Key = %q in %q, want %q", le.Key, err, tt.wantKey)
			}
			// The version 1 loader reads the same file and refuses it the
			// same way.
			agents, err := data.LoadAgents(home.Paths(), files)
			if !errors.As(err, &le) || le.Key != tt.wantKey || agents != nil {
				t.Errorf("LoadAgents() = %d agents, %v; want the same refusal on %q", len(agents), err, tt.wantKey)
			}
		})
	}
}

// TestAgentsLocalExampleDocumentsTheVersion2Keys keeps every new key
// documented in the shipped example, which must still load.
func TestAgentsLocalExampleDocumentsTheVersion2Keys(t *testing.T) {
	example, err := os.ReadFile("agents.local.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reads_store", "link_dir", "follows_links", "read_roots", "depth", "\"unlimited\"", "\"unknown\""} {
		if !strings.Contains(string(example), key) {
			t.Errorf("agents.local.example.toml does not document %s", key)
		}
	}
	home := testsupport.NewHome(t)
	home.WriteFile(".config/skillhub/agents.local.toml", string(example))
	if _, err := data.LoadHarnesses(home.Paths(), fs.NewFileOps(home.Paths().Guard())); err != nil {
		t.Errorf("LoadHarnesses(example) error = %v", err)
	}
}
