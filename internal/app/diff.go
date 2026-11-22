package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	udiff "github.com/aymanbagabas/go-udiff"

	"skillhub/internal/shared/domain"
)

// FileDiff is how one file of a candidate changes, from the installed folder
// to the staged one.
type FileDiff struct {
	Path   string
	Change domain.FileChangeKind
	// Unified is the unified diff of a text file; empty for a mode-only
	// change and for a binary file.
	Unified string
	// Binary is true when either side is not UTF-8 text.
	Binary bool
}

// Diff returns the diff of a candidate, file by file in the candidate's
// order: what the user approves. It reads the installed folder and the
// staged copy and changes nothing.
func (a *App) Diff(_ context.Context, c domain.UpdateCandidate) ([]FileDiff, error) {
	installed, err := a.installedFolder(c.Target)
	if err != nil {
		return nil, err
	}
	return diffFiles(c.FileChanges, a.readFrom(installed, "installed"), a.readFrom(c.StagedPath, "staged"))
}

// RevisionDiff returns the diff of a prepared install or reinstall
// revision against nothing, file by file in its order: the diff of
// rev.Prepared, the reviewed tree its plan identifier covers (A2 of L51),
// whatever the store holds under its key. It reads only the staged copy.
func (a *App) RevisionDiff(_ context.Context, rev Revision) ([]FileDiff, error) {
	nothing := func(path, _ string) ([]byte, error) {
		return nil, fmt.Errorf("%w: %s has an old side, and a revision is diffed against nothing", ErrInvalidRequest, path)
	}
	return diffFiles(rev.Prepared.FileChanges, nothing, a.readFrom(rev.Prepared.StagedPath, "staged"))
}

// readSide reads one side of a file change: its slash-separated path and
// its blob SHA.
type readSide func(path, sha string) ([]byte, error)

// readFrom reads a side from the folder root on disk; side names it in
// errors.
func (a *App) readFrom(root, side string) readSide {
	return func(path, _ string) ([]byte, error) {
		b, err := a.d.Files.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read the %s %s: %w", side, path, err)
		}
		return b, nil
	}
}

// diffFiles formats each change, reading its old side with readOld and its
// new side with readNew. A symlink's target is not read, and a binary side
// gives no text: every diff of a review follows these rules.
func diffFiles(changes []domain.FileChange, readOld, readNew readSide) ([]FileDiff, error) {
	out := make([]FileDiff, 0, len(changes))
	for _, fc := range changes {
		d := FileDiff{Path: fc.Path, Change: fc.Change}
		var before, after []byte
		var err error
		if fc.OldSHA != "" && fc.OldMode != symlinkMode {
			if before, err = readOld(fc.Path, fc.OldSHA); err != nil {
				return nil, err
			}
		}
		if fc.NewSHA != "" && fc.NewMode != symlinkMode {
			if after, err = readNew(fc.Path, fc.NewSHA); err != nil {
				return nil, err
			}
		}
		if isBinary(before) || isBinary(after) {
			d.Binary = true
		} else {
			d.Unified = udiff.Unified("a/"+fc.Path, "b/"+fc.Path, string(before), string(after))
		}
		out = append(out, d)
	}
	return out, nil
}

// symlinkMode is git's mode of a symbolic link.
const symlinkMode = "120000"

// isBinary reports content that is not UTF-8 text or holds a NUL byte.
func isBinary(b []byte) bool {
	return !utf8.Valid(b) || strings.ContainsRune(string(b), 0)
}

// installedFolder is the folder a target's update replaces.
func (a *App) installedFolder(target string) (string, error) {
	if name, ok := strings.CutPrefix(target, domain.LockID("")); ok {
		return filepath.Join(a.d.Paths.StoreDir(), name), nil
	}
	if rel, ok := strings.CutPrefix(target, domain.GitID("")); ok {
		return a.d.Paths.Join(filepath.FromSlash(rel))
	}
	return "", fmt.Errorf("%s: %w", target, ErrSkillNotFound)
}
