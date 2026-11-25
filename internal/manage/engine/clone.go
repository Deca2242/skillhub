package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"skillhub/internal/shared/paths"
)

const cloneDigestDomain = "skillhub clone tree v1\n"

// maxCloneSymlinkHops bounds in-memory symlink target verification inside a
// clone, matching path resolution limits.
const maxCloneSymlinkHops = 40

type cloneNodeKind uint8

const (
	cloneNodeDir cloneNodeKind = iota + 1
	cloneNodeFile
	cloneNodeSymlink
)

type cloneNode struct {
	rel    string
	kind   cloneNodeKind
	perm   fs.FileMode
	size   int64
	sha256 [32]byte
	target string
}

type cloneManifest struct {
	SHA string
}

// unsupportedGitMarkers are entries under .git that identify layouts this
// engine refuses to snapshot or remove (linked worktrees, split gitdirs,
// submodules, and shared object alternates).
var unsupportedGitMarkers = []string{
	"commondir",
	"gitdir",
	"worktrees",
	"modules",
	filepath.Join("objects", "info", "alternates"),
}

// inspectClone walks dir as a standalone Git clone, refusing unsupported clone
// layouts, unsafe symlinks, special files, special mode bits, and incomplete
// checks, and returns a deterministic digest covering file bytes, .git
// metadata, empty directories, exact permission modes, and symlink targets.
func (e *Engine) inspectClone(dir string) (cloneManifest, error) {
	dr, ok := e.d.Files.(DirReader)
	if !ok {
		return cloneManifest{}, fmt.Errorf("%w: %s: directory listing unavailable for clone verification", ErrUnsupportedTree, dir)
	}
	mr, ok := e.d.Files.(EntryMetadataReader)
	if !ok {
		return cloneManifest{}, fmt.Errorf("%w: %s: metadata inspection unavailable for clone verification", ErrUnsupportedTree, dir)
	}
	if e.d.Links == nil {
		return cloneManifest{}, fmt.Errorf("%w: %s: link reader unavailable for clone verification", ErrUnsupportedTree, dir)
	}
	rootMode, err := e.d.Files.Mode(dir)
	if err != nil {
		return cloneManifest{}, fmt.Errorf("look at %s: %w", dir, err)
	}
	if !rootMode.IsDir() {
		return cloneManifest{}, fmt.Errorf("%w: %s is not a real directory", ErrUnsupportedTree, dir)
	}
	if rootMode&specialBits != 0 {
		return cloneManifest{}, fmt.Errorf("%w: %s has mode %v; setuid, setgid and sticky bits are not kept", ErrUnsupportedTree, dir, rootMode)
	}
	if err := checkCloneMetadata(mr, dir, rootMode); err != nil {
		return cloneManifest{}, err
	}
	if err := e.checkCloneGitLayout(dir); err != nil {
		return cloneManifest{}, err
	}

	var nodes []cloneNode
	symlinks := map[string]string{}
	if err := e.walkCloneDir(dr, mr, dir, "", &nodes, symlinks); err != nil {
		return cloneManifest{}, err
	}
	if err := verifyCloneSymlinks(dir, symlinks); err != nil {
		return cloneManifest{}, err
	}
	slices.SortFunc(nodes, func(a, b cloneNode) int {
		return strings.Compare(a.rel, b.rel)
	})

	h := sha256.New()
	_, _ = h.Write([]byte(cloneDigestDomain))
	_, _ = fmt.Fprintf(h, "root %04o\n", uint32(rootMode.Perm()))
	for _, n := range nodes {
		switch n.kind {
		case cloneNodeDir:
			_, _ = fmt.Fprintf(h, "D %04o %d %s\n", uint32(n.perm), len(n.rel), n.rel)
		case cloneNodeFile:
			_, _ = fmt.Fprintf(h, "F %04o %d %s %d %s\n", uint32(n.perm), n.size, hex.EncodeToString(n.sha256[:]), len(n.rel), n.rel)
		case cloneNodeSymlink:
			_, _ = fmt.Fprintf(h, "L %04o %d %s -> %d %s\n", uint32(n.perm), len(n.rel), n.rel, len(n.target), n.target)
		}
	}
	return cloneManifest{SHA: hex.EncodeToString(h.Sum(nil))}, nil
}

func (e *Engine) checkCloneGitLayout(dir string) error {
	gitDir := filepath.Join(dir, ".git")
	gitMode, err := e.d.Files.Mode(gitDir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s has no .git directory", ErrUnsupportedTree, dir)
	}
	if err != nil {
		return fmt.Errorf("look at %s: %w", gitDir, err)
	}
	if !gitMode.IsDir() {
		return fmt.Errorf("%w: %s has an unsupported .git layout", ErrUnsupportedTree, dir)
	}
	if gitMode&specialBits != 0 {
		return fmt.Errorf("%w: %s has mode %v; setuid, setgid and sticky bits are not kept", ErrUnsupportedTree, gitDir, gitMode)
	}
	for _, marker := range unsupportedGitMarkers {
		p := filepath.Join(gitDir, marker)
		exists, err := e.d.Files.Exists(p)
		if errors.Is(err, syscall.ENOTDIR) {
			return fmt.Errorf("%w: %s has an unsupported .git layout at %s", ErrUnsupportedTree, dir, marker)
		}
		if err != nil {
			return fmt.Errorf("look at %s: %w", p, err)
		}
		if exists {
			return fmt.Errorf("%w: %s has unsupported git metadata (%s)", ErrUnsupportedTree, dir, filepath.ToSlash(marker))
		}
	}
	return nil
}

func (e *Engine) walkCloneDir(
	dr DirReader,
	mr EntryMetadataReader,
	absDir, relDir string,
	nodes *[]cloneNode,
	symlinks map[string]string,
) error {
	entries, err := dr.ReadDir(absDir)
	if err != nil {
		return fmt.Errorf("list %s: %w", absDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsRune(name, '/') || strings.ContainsRune(name, 0) {
			return fmt.Errorf("%w: %s contains an unsupported entry name %q", ErrUnsupportedTree, absDir, name)
		}
		relSlash := path.Join(relDir, name)
		if name == ".git" && relDir != "" {
			return fmt.Errorf("%w: %s contains nested .git at %s", ErrUnsupportedTree, absDir, relSlash)
		}
		absPath := filepath.Join(absDir, name)
		mode, err := e.d.Files.Mode(absPath)
		if err != nil {
			return fmt.Errorf("look at %s: %w", absPath, err)
		}
		if entry.Type() != mode.Type() {
			return fmt.Errorf("%w: %s changed type while inspected", ErrUnsupportedTree, absPath)
		}
		if mode&specialBits != 0 {
			return fmt.Errorf("%w: %s has mode %v; setuid, setgid and sticky bits are not kept", ErrUnsupportedTree, absPath, mode)
		}
		if err := checkCloneMetadata(mr, absPath, mode); err != nil {
			return err
		}
		inGit := relSlash == ".git" || strings.HasPrefix(relSlash, ".git/")

		switch {
		case mode&fs.ModeSymlink != 0:
			if inGit {
				return fmt.Errorf("%w: %s contains a symlink inside .git", ErrUnsupportedTree, absPath)
			}
			target, err := e.d.Links.Readlink(absPath)
			if err != nil {
				return fmt.Errorf("read link %s: %w", absPath, err)
			}
			if target == "" || !utf8.ValidString(target) || strings.ContainsRune(target, 0) {
				return fmt.Errorf("%w: %s has an unsupported symlink target %q", ErrUnsupportedTree, absPath, target)
			}
			if filepath.IsAbs(target) {
				return fmt.Errorf("%w: %s is an absolute symlink to %q", ErrUnsupportedTree, absPath, target)
			}
			cleaned := path.Clean(path.Join(relDir, filepath.ToSlash(target)))
			if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
				return fmt.Errorf("%w: %s escapes the clone root (%q)", ErrUnsupportedTree, absPath, target)
			}
			if cleaned == ".git" || strings.HasPrefix(cleaned, ".git/") {
				return fmt.Errorf("%w: %s points into .git (%q)", ErrUnsupportedTree, absPath, target)
			}
			symlinks[relSlash] = target
			*nodes = append(*nodes, cloneNode{rel: relSlash, kind: cloneNodeSymlink, perm: mode.Perm(), target: target})
		case mode.IsDir():
			*nodes = append(*nodes, cloneNode{rel: relSlash, kind: cloneNodeDir, perm: mode.Perm()})
			if err := e.walkCloneDir(dr, mr, absPath, relSlash, nodes, symlinks); err != nil {
				return err
			}
		case mode.IsRegular():
			data, err := e.d.Files.ReadFile(absPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", absPath, err)
			}
			modeAfter, err := e.d.Files.Mode(absPath)
			if err != nil {
				return fmt.Errorf("look at %s: %w", absPath, err)
			}
			if modeAfter != mode {
				return fmt.Errorf("%w: %s changed mode while inspected", ErrUnsupportedTree, absPath)
			}
			if err := checkCloneMetadata(mr, absPath, modeAfter); err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			*nodes = append(*nodes, cloneNode{
				rel:    relSlash,
				kind:   cloneNodeFile,
				perm:   mode.Perm(),
				size:   int64(len(data)),
				sha256: sum,
			})
		default:
			return fmt.Errorf("%w: %s has unsupported file type %s", ErrUnsupportedTree, absPath, mode.Type())
		}
	}
	return nil
}

func checkCloneMetadata(mr EntryMetadataReader, absPath string, mode fs.FileMode) error {
	nlink, hasXattrs, hasACLs, err := mr.EntryMetadata(absPath)
	if err != nil {
		if errors.Is(err, paths.ErrForbidden) || errors.Is(err, paths.ErrInvalidPath) {
			return fmt.Errorf("inspect metadata of %s: %w", absPath, err)
		}
		return fmt.Errorf("%w: inspect metadata of %s: %w", ErrUnsupportedTree, absPath, err)
	}
	if !mode.IsDir() && nlink != 1 {
		return fmt.Errorf("%w: %s has %d hard links", ErrUnsupportedTree, absPath, nlink)
	}
	if mode.IsDir() && nlink == 0 {
		return fmt.Errorf("%w: %s has link count 0", ErrUnsupportedTree, absPath)
	}
	if hasXattrs {
		return fmt.Errorf("%w: %s has unsupported extended attributes", ErrUnsupportedTree, absPath)
	}
	if hasACLs {
		return fmt.Errorf("%w: %s has unsupported ACLs", ErrUnsupportedTree, absPath)
	}
	return nil
}

// verifyCloneSymlinks resolves every symlink path component-by-component purely
// in memory so multi-hop symlink escapes, .git targets, and loops are rejected
// without following symlinks on disk.
func verifyCloneSymlinks(root string, symlinks map[string]string) error {
	for linkRel, target := range symlinks {
		parent := path.Dir(linkRel)
		var cur []string
		if parent != "." {
			cur = strings.Split(parent, "/")
		}
		queue := strings.Split(filepath.ToSlash(target), "/")
		hops := 0
		for len(queue) > 0 {
			seg := queue[0]
			queue = queue[1:]
			switch seg {
			case "", ".":
				continue
			case "..":
				if len(cur) == 0 {
					return fmt.Errorf("%w: %s escapes the clone root via %q", ErrUnsupportedTree, filepath.Join(root, filepath.FromSlash(linkRel)), target)
				}
				cur = cur[:len(cur)-1]
			default:
				cur = append(cur, seg)
				joined := strings.Join(cur, "/")
				if joined == ".git" || strings.HasPrefix(joined, ".git/") {
					return fmt.Errorf("%w: %s resolves into .git via %q", ErrUnsupportedTree, filepath.Join(root, filepath.FromSlash(linkRel)), target)
				}
				if next, isLink := symlinks[joined]; isLink {
					hops++
					if hops > maxCloneSymlinkHops {
						return fmt.Errorf("%w: %s is part of a symlink loop", ErrUnsupportedTree, filepath.Join(root, filepath.FromSlash(linkRel)))
					}
					cur = cur[:len(cur)-1]
					queue = append(strings.Split(filepath.ToSlash(next), "/"), queue...)
				}
			}
		}
	}
	return nil
}

// verifyCloneCopy verifies that copyPath matches wantCloneSHA and agrees with
// src on the complete whole-clone digest.
func (e *Engine) verifyCloneCopy(src, copyPath, wantCloneSHA string) error {
	copyManifest, err := e.inspectClone(copyPath)
	if err != nil {
		return fmt.Errorf("%w: the copy of %s failed verification: %w", ErrSnapshotMismatch, src, err)
	}
	if copyManifest.SHA != wantCloneSHA {
		return fmt.Errorf("%w: the copy of %s is %s, want %s", ErrSnapshotMismatch, src, copyManifest.SHA, wantCloneSHA)
	}
	srcManifest, err := e.inspectClone(src)
	if err != nil {
		return fmt.Errorf("%w: %s failed verification: %w", ErrSnapshotMismatch, src, err)
	}
	if srcManifest.SHA != wantCloneSHA {
		return fmt.Errorf("%w: %s is %s, want %s", ErrSnapshotMismatch, src, srcManifest.SHA, wantCloneSHA)
	}
	return nil
}

// verifyCloneSnapshotCopy verifies a stored clone copy inside a snapshot
// against wantCloneSHA.
func (e *Engine) verifyCloneSnapshotCopy(origPath, copyPath, wantCloneSHA string) error {
	copyManifest, err := e.inspectClone(copyPath)
	if err != nil {
		return fmt.Errorf("%w: the copy of %s failed verification: %w", ErrSnapshotMismatch, origPath, err)
	}
	if copyManifest.SHA != wantCloneSHA {
		return fmt.Errorf("%w: the copy of %s is %s, want %s", ErrSnapshotMismatch, origPath, copyManifest.SHA, wantCloneSHA)
	}
	return nil
}
