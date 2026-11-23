package app

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"unicode/utf8"

	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// SkillDocumentMaxBytes caps the SKILL.md bytes returned/rendered for the
// local read-only document view; FileOps.ReadFile still reads the whole file.
const SkillDocumentMaxBytes = 32 * 1024

// SkillDocument is the read-only local view of a skill's installed SKILL.md,
// its parsed frontmatter and its companion files.
type SkillDocument struct {
	SkillID       string
	SkillName     string
	Dir           string
	Frontmatter   inventory.Frontmatter
	Content       string
	Truncated     bool
	OriginalBytes int
	ShownBytes    int
	Missing       bool
	Companions    []CompanionEntry
}

// CompanionEntry is a file or directory sitting alongside SKILL.md in the
// skill's installed folder.
type CompanionEntry struct {
	Name string
	Kind ports.EntryKind
}

// SkillDocument reads the selected skill's installed frontmatter, SKILL.md
// content (capped at SkillDocumentMaxBytes in the returned view) and companion
// entries through the existing filesystem ports. It is strictly read-only,
// runs no subprocesses and touches no network.
func (a *App) SkillDocument(s domain.Skill) (SkillDocument, error) {
	doc := SkillDocument{
		SkillID:    s.ID,
		SkillName:  s.Name,
		Companions: []CompanionEntry{},
	}
	dir := skillDocumentDir(s)
	if dir == "" {
		doc.Missing = true
		return doc, nil
	}
	doc.Dir = dir

	fm, err := inventory.ReadFrontmatter(a.d.Files, dir)
	if err != nil {
		return SkillDocument{}, fmt.Errorf("read frontmatter for %s: %w", s.ID, err)
	}
	doc.Frontmatter = fm

	data, err := a.d.Files.ReadFile(filepath.Join(dir, "SKILL.md"))
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		doc.Missing = true
	case err != nil:
		return SkillDocument{}, fmt.Errorf("read SKILL.md for %s: %w", s.ID, err)
	default:
		doc.OriginalBytes = len(data)
		shown, truncated := truncateUTF8Bytes(data, SkillDocumentMaxBytes)
		doc.Content = string(shown)
		doc.ShownBytes = len(shown)
		doc.Truncated = truncated
	}

	entries, err := a.d.Dirs.Scan(dir)
	if err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return SkillDocument{}, fmt.Errorf("scan companions for %s: %w", s.ID, err)
	}
	for _, entry := range entries {
		if entry.Name == "SKILL.md" || entry.Name == ".git" {
			continue
		}
		doc.Companions = append(doc.Companions, CompanionEntry{
			Name: entry.Name,
			Kind: entry.Kind,
		})
	}
	return doc, nil
}

func skillDocumentDir(s domain.Skill) string {
	for _, loc := range s.Locations {
		if loc.AgentID == domain.StoreAgentID && loc.Kind != domain.LocationBrokenLink && loc.Path != "" {
			return loc.Path
		}
	}
	for _, loc := range s.Locations {
		if loc.Kind == domain.LocationReal && loc.Path != "" {
			return loc.Path
		}
	}
	for _, loc := range s.Locations {
		if loc.Kind != domain.LocationBrokenLink && loc.Path != "" {
			return loc.Path
		}
	}
	return ""
}

// truncateUTF8Bytes caps b at limit bytes without splitting a multi-byte UTF-8 rune.
func truncateUTF8Bytes(b []byte, limit int) ([]byte, bool) {
	if len(b) <= limit {
		return b, false
	}
	n := limit
	for n > 0 && !utf8.RuneStart(b[n]) {
		n--
	}
	return b[:n], true
}
