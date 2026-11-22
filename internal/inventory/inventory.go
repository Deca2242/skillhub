// Package inventory builds the list of skills the user installed
// (docs/design.md sections 2 and 4): it scans the store and the global
// directory of every harness, groups folders into skills with a stable id,
// and reports each skill's origin, state and issues. It only reads; it never
// writes to a skills folder.
package inventory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// ErrMissingDependency reports Deps without a port Scan needs.
var ErrMissingDependency = errors.New("inventory: missing dependency")

// Deps are what Scan reads through.
type Deps struct {
	Paths paths.Paths
	// Agents is the agents table: the embedded one plus the user's overlay.
	Agents []domain.AgentProfile
	Dirs   ports.SkillDirScanner
	// Files reads SKILL.md files and gentle-ai's state; Scan never writes.
	Files ports.FileOps
	Lock  ports.SkillsLockStore
	// Git describes the clones found in skills directories.
	Git ports.GitPort
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
}

func (d Deps) validate() error {
	var missing []string
	if d.Paths.Home() == "" {
		missing = append(missing, "Paths")
	}
	if d.Dirs == nil {
		missing = append(missing, "Dirs")
	}
	if d.Files == nil {
		missing = append(missing, "Files")
	}
	if d.Lock == nil {
		missing = append(missing, "Lock")
	}
	if d.Git == nil {
		missing = append(missing, "Git")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %v", ErrMissingDependency, missing)
	}
	return nil
}

// Scan lists every skill the user installed, sorted by name and then id.
// A skill whose id, name or alias is in ignore is left out (the ignore list
// of the settings).
func Scan(ctx context.Context, d Deps, ignore []string) ([]domain.Skill, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	if d.HashDir == nil {
		d.HashDir = treehash.Dir
	}
	lock, err := d.Lock.Read()
	if err != nil {
		return nil, fmt.Errorf("read the skills lock: %w", err)
	}
	s := &scan{deps: d, ctx: ctx, hashes: map[string]string{}}
	if s.gentleManagesSkills, err = gentleManagesSkills(d.Files, d.Paths.GentleAIStateFile()); err != nil {
		return nil, err
	}
	if err := s.collect(); err != nil {
		return nil, err
	}
	s.buildLockSkills(lock.Entries)
	if err := s.attachOriginless(); err != nil {
		return nil, err
	}

	ignored := map[string]bool{}
	for _, name := range ignore {
		ignored[name] = true
	}
	out := []domain.Skill{}
	for _, b := range s.skills {
		skill, err := s.finish(b)
		if err != nil {
			return nil, err
		}
		if ignored[skill.ID] || ignored[skill.Name] || slices.ContainsFunc(skill.Aliases, func(a string) bool { return ignored[a] }) {
			continue
		}
		out = append(out, skill)
	}
	slices.SortFunc(out, func(a, b domain.Skill) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
