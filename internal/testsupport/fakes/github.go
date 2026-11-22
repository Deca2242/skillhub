package fakes

import (
	"context"
	"crypto/sha1" //nolint:gosec // fake commit ids only need to look like git's.
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// fakeRepo is one repository of the fake GitHub.
type fakeRepo struct {
	defaultBranch string
	branches      map[string]string
	tags          map[string]string
	commits       map[string]treehash.Tree
	blobs         map[string][]byte
}

// GitHub is an in-memory ports.GitHubPort. Its repositories are built from
// fixture folders on disk: Commit hashes a folder with treehash.Dir, so the
// trees and blob SHAs it serves are the ones git would compute.
type GitHub struct {
	Faults

	mu    sync.Mutex
	repos map[string]*fakeRepo
	seq   int
}

// NewGitHub builds a GitHub with no repositories.
func NewGitHub() *GitHub {
	return &GitHub{repos: map[string]*fakeRepo{}}
}

// AddRepo creates an empty repository owner/name with its default branch.
func (g *GitHub) AddRepo(repo, defaultBranch string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.repos[repo] = &fakeRepo{
		defaultBranch: defaultBranch,
		branches:      map[string]string{},
		tags:          map[string]string{},
		commits:       map[string]treehash.Tree{},
		blobs:         map[string][]byte{},
	}
}

// Commit records the folder dir as a new commit on branch and returns its
// SHA. The folder is read once; later changes to it do not affect the
// commit.
func (g *GitHub) Commit(tb testing.TB, repo, branch, dir string) string {
	tb.Helper()
	tree, err := treehash.Dir(dir)
	if err != nil {
		tb.Fatalf("fake GitHub: hash %s: %v", dir, err)
	}
	blobs := map[string][]byte{}
	for _, n := range tree.Nodes {
		if n.Mode == treehash.ModeTree {
			continue
		}
		abs := filepath.Join(dir, filepath.FromSlash(n.Path))
		var content []byte
		if n.Mode == treehash.ModeSymlink {
			target, err := os.Readlink(abs)
			if err != nil {
				tb.Fatalf("fake GitHub: %v", err)
			}
			content = []byte(filepath.ToSlash(target))
		} else if content, err = os.ReadFile(abs); err != nil {
			tb.Fatalf("fake GitHub: %v", err)
		}
		blobs[n.SHA] = content
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.repos[repo]
	if !ok {
		tb.Fatalf("fake GitHub: Commit to unknown repository %s; call AddRepo first", repo)
	}
	g.seq++
	sum := sha1.Sum([]byte(repo + "\x00" + branch + "\x00" + tree.SHA + "\x00" + r.branches[branch] + "\x00" + strconv.Itoa(g.seq))) //nolint:gosec
	commit := hex.EncodeToString(sum[:])
	r.commits[commit] = tree
	r.branches[branch] = commit
	for sha, content := range blobs {
		r.blobs[sha] = content
	}
	return commit
}

// Tag points tag at commit.
func (g *GitHub) Tag(repo, tag, commit string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.repos[repo].tags[tag] = commit
}

func (g *GitHub) repoLocked(repo string) (*fakeRepo, error) {
	r, ok := g.repos[repo]
	if !ok {
		return nil, fmt.Errorf("%s: %w", repo, ports.ErrRepoNotFound)
	}
	return r, nil
}

// DefaultBranch implements ports.GitHubPort.
func (g *GitHub) DefaultBranch(_ context.Context, repo string) (string, error) {
	if err := g.enter("DefaultBranch", repo); err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.repoLocked(repo)
	if err != nil {
		return "", err
	}
	return r.defaultBranch, nil
}

// ResolveCommit implements ports.GitHubPort: a branch, then a tag, then a
// commit SHA.
func (g *GitHub) ResolveCommit(_ context.Context, repo, ref string) (string, error) {
	if err := g.enter("ResolveCommit", repo, ref); err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.repoLocked(repo)
	if err != nil {
		return "", err
	}
	if c, ok := r.branches[ref]; ok {
		return c, nil
	}
	if c, ok := r.tags[ref]; ok {
		return c, nil
	}
	if _, ok := r.commits[ref]; ok {
		return ref, nil
	}
	return "", fmt.Errorf("%s@%s: %w", repo, ref, ports.ErrRefNotFound)
}

// Tree implements ports.GitHubPort.
func (g *GitHub) Tree(_ context.Context, repo, commit, path string) (treehash.Tree, error) {
	if err := g.enter("Tree", repo, commit, path); err != nil {
		return treehash.Tree{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.repoLocked(repo)
	if err != nil {
		return treehash.Tree{}, err
	}
	root, ok := r.commits[commit]
	if !ok {
		return treehash.Tree{}, fmt.Errorf("%s@%s: %w", repo, commit, ports.ErrRefNotFound)
	}
	if path == "" {
		return copyTree(root, ""), nil
	}
	for _, n := range root.Nodes {
		if n.Path == path && n.IsTree() {
			sub := treehash.Tree{SHA: n.SHA, Nodes: root.Nodes}
			return copyTree(sub, path+"/"), nil
		}
	}
	return treehash.Tree{}, fmt.Errorf("%s@%s:%s is not a tree: %w", repo, commit, path, ports.ErrRefNotFound)
}

// TreeBySHA implements ports.GitHubPort: the root tree of a recorded commit,
// or one of its subtrees, whose SHA is treeSHA.
func (g *GitHub) TreeBySHA(_ context.Context, repo, treeSHA string) (treehash.Tree, error) {
	if err := g.enter("TreeBySHA", repo, treeSHA); err != nil {
		return treehash.Tree{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.repoLocked(repo)
	if err != nil {
		return treehash.Tree{}, err
	}
	if !isHexSHA40(treeSHA) {
		return treehash.Tree{}, fmt.Errorf("tree %q is not a 40-character hex SHA: %w", treeSHA, ports.ErrRefNotFound)
	}
	treeSHA = strings.ToLower(treeSHA)
	for _, root := range r.commits {
		if root.SHA == treeSHA {
			return copyTree(root, ""), nil
		}
		for _, n := range root.Nodes {
			if n.SHA == treeSHA && n.IsTree() {
				sub := treehash.Tree{SHA: n.SHA, Nodes: root.Nodes}
				return copyTree(sub, n.Path+"/"), nil
			}
		}
	}
	return treehash.Tree{}, fmt.Errorf("%s: tree %s: %w", repo, treeSHA, ports.ErrRefNotFound)
}

func isHexSHA40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// copyTree returns the nodes below prefix, with paths relative to it.
func copyTree(t treehash.Tree, prefix string) treehash.Tree {
	out := treehash.Tree{SHA: t.SHA}
	for _, n := range t.Nodes {
		if rel, ok := strings.CutPrefix(n.Path, prefix); ok {
			n.Path = rel
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out
}

// Blob implements ports.GitHubPort.
func (g *GitHub) Blob(_ context.Context, repo, sha string) ([]byte, error) {
	if err := g.enter("Blob", repo, sha); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.repoLocked(repo)
	if err != nil {
		return nil, err
	}
	content, ok := r.blobs[sha]
	if !ok {
		return nil, fmt.Errorf("blob %s: %w", sha, ports.ErrRefNotFound)
	}
	return append([]byte(nil), content...), nil
}
