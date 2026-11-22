package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

var _ ports.SkillDirScanner = (*Scanner)(nil)

// Scanner is the real ports.SkillDirScanner. It only reads: it lists, stats
// and resolves links, and never writes.
type Scanner struct {
	guard paths.Guard
}

// NewScanner builds a Scanner that refuses every path guard refuses: the
// directory itself, and any entry whose link resolves under a forbidden
// prefix.
func NewScanner(guard paths.Guard) *Scanner {
	return &Scanner{guard: guard}
}

// Scan implements ports.SkillDirScanner.
func (s *Scanner) Scan(dir string) ([]ports.DirEntry, error) {
	if err := s.guard.Check(dir); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	entries, err := os.ReadDir(dir) // sorted by name
	if err != nil {
		return nil, err
	}
	out := make([]ports.DirEntry, 0, len(entries))
	for _, entry := range entries {
		e, err := s.describe(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Scanner) describe(path string) (ports.DirEntry, error) {
	if err := s.guard.Check(path); err != nil {
		return ports.DirEntry{}, fmt.Errorf("scan: %w", err)
	}
	e := ports.DirEntry{Name: filepath.Base(path), Path: path}
	info, err := os.Lstat(path)
	if err != nil {
		return ports.DirEntry{}, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if info.Mode()&iofs.ModeSymlink != 0 {
			// A dangling link, a link loop or a target that cannot be
			// reached: either way the link leads nowhere usable.
			e.Kind = ports.EntryBrokenLink
			return e, nil
		}
		return ports.DirEntry{}, err
	}
	e.ResolvedPath = resolved
	target, err := os.Stat(resolved)
	if err != nil {
		return ports.DirEntry{}, err
	}
	switch mode := info.Mode(); {
	case mode&iofs.ModeSymlink != 0:
		e.Kind = ports.EntrySymlink
	case mode.IsDir():
		e.Kind = ports.EntryDir
	case mode.IsRegular():
		e.Kind = ports.EntryFile
	default:
		e.Kind = ports.EntryOther
	}
	if target.IsDir() {
		if e.HasSkillMD, err = isRegularFile(filepath.Join(resolved, "SKILL.md")); err != nil {
			return ports.DirEntry{}, err
		}
		if e.HasGit, err = isDir(filepath.Join(resolved, ".git")); err != nil {
			return ports.DirEntry{}, err
		}
	}
	return e, nil
}

func isRegularFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// isDir reports whether path is a directory itself, not a link to one: a
// clone is recognized by a .git directory (docs/design.md section 2).
func isDir(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}
