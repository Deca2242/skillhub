package inventory

import (
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// Version 2 exports (docs/design-v2.md section 11, items 3 and 7): the
// identity and visibility graph reads frontmatters and applies the S1
// exclusions exactly as the scan does, through these wrappers over the
// scan's own code.

// OffDirName is the folder of the store where the user parked skills by hand.
const OffDirName = offDirName

// Frontmatter is what a SKILL.md header declares.
type Frontmatter struct {
	Name        string
	Description string
	// Author is metadata.author.
	Author string
}

// ReadFrontmatter reads the SKILL.md of the folder dir through files. A
// missing file, or a header that does not parse, gives an empty Frontmatter.
func ReadFrontmatter(files ports.FileOps, dir string) (Frontmatter, error) {
	s := &scan{deps: Deps{Files: files}}
	fm, err := s.frontmatter(dir)
	if err != nil {
		return Frontmatter{}, err
	}
	return Frontmatter{Name: fm.name, Description: fm.description, Author: fm.author}, nil
}

// IsGentleAuthor reports whether fm marks a copy managed by gentle-ai (A18).
// It only hides a skill when GentleManagesSkills is true.
func IsGentleAuthor(fm Frontmatter) bool { return fm.Author == gentleAuthor }

// GentleManagesSkills reports whether gentle-ai's state file lists its skills
// component, as the scan decides it.
func GentleManagesSkills(files ports.FileOps, stateFile string) (bool, error) {
	return gentleManagesSkills(files, stateFile)
}

// Excluded reports whether path is at or below a provider zone that S1 leaves
// out of the inventory (docs/design.md section 2).
func Excluded(p paths.Paths, path string) bool {
	s := &scan{deps: Deps{Paths: p}}
	return s.excluded(path)
}
