// Package staging prepares a verifiable copy of an upstream tree at a
// location the caller chooses (docs/design.md section 7, step 2;
// docs/design-v2.md section 15, point 6). It enforces the limits, refuses
// entries it cannot write safely, checks every blob against its SHA and the
// written tree against the expected tree SHA. What the staged tree means (a
// diff, risk labels, an approval) is the caller's policy, not this
// package's.
//
// It imports neither internal/updates nor internal/manage/engine: both use
// it, and updates already imports engine.
package staging

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"skillhub/internal/shared/treehash"
)

// Staging limits. A skill is a handful of text files; anything beyond these
// is refused rather than downloaded.
const (
	MaxFiles = 2000
	MaxBytes = 64 << 20
	MaxDepth = 24
)

var (
	// ErrUnsupportedEntry reports an upstream tree holding a symlink, a
	// submodule or a path that is not a plain relative path.
	ErrUnsupportedEntry = errors.New("upstream tree holds an unsupported entry")
	// ErrLimitsExceeded reports an upstream tree beyond MaxFiles, MaxBytes or
	// MaxDepth.
	ErrLimitsExceeded = errors.New("upstream tree exceeds the staging limits")
	// ErrBlobMismatch reports a blob whose content does not hash to its SHA.
	ErrBlobMismatch = errors.New("blob content does not match its sha")
	// ErrTreeMismatch reports a staged tree whose SHA is not the expected
	// one.
	ErrTreeMismatch = errors.New("staged tree does not match its sha")
)

// Files is the part of ports.FileOps staging writes through.
type Files interface {
	ReadFile(path string) ([]byte, error)
	WriteFileAtomic(path string, data []byte, perm fs.FileMode) error
	RemoveTree(path string) error
	MkdirAll(path string, perm fs.FileMode) error
}

// Blobs downloads the content of a blob; ports.GitHubPort satisfies it.
type Blobs interface {
	Blob(ctx context.Context, repo, sha string) ([]byte, error)
}

// Stager writes verified trees.
type Stager struct {
	Files Files
	Blobs Blobs
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
}

// Request is one tree to stage.
type Request struct {
	// Repo is the owner/name the blobs are downloaded from.
	Repo string
	// Dir is where the tree is written. Whatever is there is removed first.
	Dir string
	// Nodes is the upstream tree, every path relative to its root.
	Nodes []treehash.Node
	// SHA is the tree SHA the written folder must hash to.
	SHA string
	// Reuse, when set, is a folder whose files supply blobs by SHA instead
	// of a download; ReuseNodes is its tree, relative to it. A reused
	// content is hashed again before it is written.
	Reuse      string
	ReuseNodes []treehash.Node
}

// Stage writes req.Nodes at req.Dir and returns the tree of what it wrote,
// whose SHA is req.SHA.
//
// A tree that fails the entry or limit checks is ErrUnsupportedEntry or
// ErrLimitsExceeded and nothing is touched: req.Dir is neither removed nor
// created. Any later failure removes req.Dir: a blob or the tree that does
// not match its SHA is ErrBlobMismatch or ErrTreeMismatch; anything else is
// a failure worth retrying, such as a download error.
func (s Stager) Stage(ctx context.Context, req Request) (treehash.Tree, error) {
	if err := CheckNodes(req.Nodes); err != nil {
		return treehash.Tree{}, err
	}
	hashDir := s.HashDir
	if hashDir == nil {
		hashDir = treehash.Dir
	}
	discard := func() { _ = s.Files.RemoveTree(req.Dir) } //nolint:errcheck // staging is a disposable cache.
	if err := s.Files.RemoveTree(req.Dir); err != nil {
		return treehash.Tree{}, fmt.Errorf("clear staging: %w", err)
	}
	if err := s.Files.MkdirAll(req.Dir, 0o755); err != nil {
		return treehash.Tree{}, fmt.Errorf("create staging: %w", err)
	}
	if err := s.fill(ctx, req); err != nil {
		discard()
		return treehash.Tree{}, err
	}
	staged, err := hashDir(req.Dir)
	if err != nil {
		discard()
		return treehash.Tree{}, fmt.Errorf("hash staging: %w", err)
	}
	if staged.SHA != req.SHA {
		discard()
		return treehash.Tree{}, fmt.Errorf("staged %s, want %s: %w", staged.SHA, req.SHA, ErrTreeMismatch)
	}
	return staged, nil
}

// CheckNodes returns ErrUnsupportedEntry or ErrLimitsExceeded when nodes
// cannot be staged, or nil.
func CheckNodes(nodes []treehash.Node) error {
	var count int
	var size int64
	for _, n := range nodes {
		switch n.Mode {
		case treehash.ModeTree:
		case treehash.ModeFile, treehash.ModeExecutable:
			count++
			size += n.Size
		default: // a symlink or a submodule
			return fmt.Errorf("%s: %w", n.Path, ErrUnsupportedEntry)
		}
		if !validTreePath(n.Path) {
			return fmt.Errorf("%q: %w", n.Path, ErrUnsupportedEntry)
		}
		if strings.Count(n.Path, "/")+1 > MaxDepth {
			return fmt.Errorf("%s is too deep: %w", n.Path, ErrLimitsExceeded)
		}
	}
	if count > MaxFiles || size > MaxBytes {
		return fmt.Errorf("%d files, %d bytes: %w", count, size, ErrLimitsExceeded)
	}
	return nil
}

// validTreePath accepts a relative slash path whose every element is a
// plain name: no "", ".", "..", ".git", backslash or NUL. Staging writes only
// regular files below its fresh root, so such a path cannot leave it.
func validTreePath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for elem := range strings.SplitSeq(p, "/") {
		if elem == "" || elem == "." || elem == ".." || strings.EqualFold(elem, ".git") {
			return false
		}
	}
	return filepath.IsLocal(filepath.FromSlash(p))
}

// fill writes every blob of req.Nodes below req.Dir: from req.Reuse when a
// file there has the same blob SHA, otherwise from GitHub. Every content is
// hashed again before it is written.
func (s Stager) fill(ctx context.Context, req Request) error {
	local := map[string]string{}
	for _, n := range req.ReuseNodes {
		if n.Mode == treehash.ModeFile || n.Mode == treehash.ModeExecutable {
			local[n.SHA] = n.Path
		}
	}
	for _, n := range req.Nodes {
		if n.IsTree() {
			continue
		}
		content, err := s.blob(ctx, req, n.SHA, local)
		if err != nil {
			return err
		}
		perm := fs.FileMode(0o644)
		if n.Mode == treehash.ModeExecutable {
			perm = 0o755
		}
		if err := s.Files.WriteFileAtomic(filepath.Join(req.Dir, filepath.FromSlash(n.Path)), content, perm); err != nil {
			return fmt.Errorf("stage %s: %w", n.Path, err)
		}
	}
	return nil
}

func (s Stager) blob(ctx context.Context, req Request, sha string, local map[string]string) ([]byte, error) {
	if rel, ok := local[sha]; ok && req.Reuse != "" {
		content, err := s.Files.ReadFile(filepath.Join(req.Reuse, filepath.FromSlash(rel)))
		if err == nil && treehash.BlobSHA(content) == sha {
			return content, nil
		}
	}
	content, err := s.Blobs.Blob(ctx, req.Repo, sha)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", sha, err)
	}
	if treehash.BlobSHA(content) != sha {
		return nil, fmt.Errorf("blob %s: %w", sha, ErrBlobMismatch)
	}
	return content, nil
}
