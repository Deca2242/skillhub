// Package data holds the data files skillhub embeds. agents.json is the
// table of harnesses and their global skills directories, read from the
// documented table of the npx skills package (docs/design.md section 4,
// skills-cli-internals.md facts 11 and 12). The user's agents.local.toml
// overlay corrects and extends it for one machine: Agents serves the
// embedded table and LoadAgents the table with the overlay applied.
// agents.local.example.toml is the overlay of the machine described in
// docs/research/local-inventory.md. Harnesses and LoadHarnesses serve the
// same table with its version 2 columns (harnesses.go).
package data

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"skillhub/internal/shared/domain"
)

//go:embed agents.json
var agentsJSON []byte

// Source records where agents.json was captured from.
type Source struct {
	Package            string   `json:"package"`
	Version            string   `json:"version"`
	File               string   `json:"file"`
	Section            string   `json:"section"`
	Captured           string   `json:"captured"`
	Method             string   `json:"method"`
	ProjectOnlySkipped []string `json:"project_only_skipped"`
	// V2Facts names where the version 2 columns come from.
	V2Facts string `json:"v2_facts"`
}

type agentsFile struct {
	Source             Source          `json:"source"`
	Agents             []agentRecord   `json:"agents"`
	DiagnosticProfiles []profileRecord `json:"diagnostic_profiles"`
}

// agentRecord is one row of agents.json. Name is the display name of the
// documented table, kept for people reading the file. The version 2 columns
// are optional; a column left out is derived (see Harness).
type agentRecord struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	GlobalDir      string       `json:"global_dir"`
	ReadsUniversal bool         `json:"reads_universal"`
	ReadsStore     *string      `json:"reads_store"`
	LinkDir        *string      `json:"link_dir"`
	ReadRoots      []rootRecord `json:"read_roots"`
	FollowsLinks   *string      `json:"follows_links"`
}

// rootRecord is a read root; Depth is a number, "unlimited" or "unknown".
type rootRecord struct {
	Dir   string `json:"dir"`
	Depth any    `json:"depth"`
}

// profileRecord is a diagnostic profile. Note explains it to people reading
// the file.
type profileRecord struct {
	ID        string       `json:"id"`
	Harness   string       `json:"harness"`
	Note      string       `json:"note"`
	ReadRoots []rootRecord `json:"read_roots"`
}

func parse() (agentsFile, error) {
	var f agentsFile
	if err := json.Unmarshal(agentsJSON, &f); err != nil {
		return agentsFile{}, fmt.Errorf("decode embedded agents.json: %w", err)
	}
	return f, nil
}

// Agents returns the embedded agents table, in the file's order. Each call
// returns a fresh slice.
func Agents() ([]domain.AgentProfile, error) {
	f, err := parse()
	if err != nil {
		return nil, err
	}
	out := make([]domain.AgentProfile, 0, len(f.Agents))
	for _, r := range f.Agents {
		out = append(out, domain.AgentProfile{ID: r.ID, GlobalDir: r.GlobalDir, ReadsUniversal: r.ReadsUniversal})
	}
	return out, nil
}

// AgentNames maps each agent id of the embedded table to its display name,
// as people know it: "opencode" is "OpenCode".
func AgentNames() (map[string]string, error) {
	f, err := parse()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(f.Agents))
	for _, r := range f.Agents {
		if r.Name != "" {
			out[r.ID] = r.Name
		}
	}
	return out, nil
}

// AgentsSource returns the provenance recorded in agents.json. It is empty
// if the embedded file does not decode, which TestAgentsIsTheSkillsTable
// rules out.
func AgentsSource() Source {
	f, err := parse()
	if err != nil {
		return Source{}
	}
	return f.Source
}
