package inventory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"skillhub/internal/shared/ports"
)

// gentleAuthor marks gentle-ai's copies in their frontmatter
// (local-inventory.md section 8, A18).
const gentleAuthor = "gentleman-programming"

// frontmatter is what the inventory reads from a SKILL.md header.
type frontmatter struct {
	name        string
	description string
	author      string
}

// frontmatter reads the SKILL.md of the folder dir. A file without a header,
// or with one that does not parse, has an empty frontmatter.
func (s *scan) frontmatter(dir string) (frontmatter, error) {
	if s.frontmatters == nil {
		s.frontmatters = map[string]frontmatter{}
	}
	if fm, ok := s.frontmatters[dir]; ok {
		return fm, nil
	}
	data, err := s.deps.Files.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return frontmatter{}, fmt.Errorf("read the SKILL.md of %s: %w", dir, err)
	}
	fm := parseFrontmatter(data)
	s.frontmatters[dir] = fm
	return fm, nil
}

func parseFrontmatter(data []byte) frontmatter {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return frontmatter{}
	}
	header, _, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return frontmatter{}
	}
	var fields map[string]any
	if err := yaml.Unmarshal(header, &fields); err != nil {
		return frontmatter{}
	}
	var fm frontmatter
	fm.name, _ = fields["name"].(string)
	fm.description, _ = fields["description"].(string)
	if metadata, ok := fields["metadata"].(map[string]any); ok {
		fm.author, _ = metadata["author"].(string)
	}
	return fm
}

// gentleManagesSkills reports whether gentle-ai's state lists its skills
// component. Without that, a gentle-ai author in a frontmatter hides nothing.
// A state file that does not parse counts as not listing it, so nothing is
// hidden by mistake.
func gentleManagesSkills(files ports.FileOps, stateFile string) (bool, error) {
	data, err := files.ReadFile(stateFile)
	if errors.Is(err, iofs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read gentle-ai's state: %w", err)
	}
	var state struct {
		Components []json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return false, nil
	}
	for _, raw := range state.Components {
		var name string
		if json.Unmarshal(raw, &name) == nil && name == "skills" {
			return true, nil
		}
	}
	return false, nil
}
