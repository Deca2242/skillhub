package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// ReviewDiff is what the review of a candidate shows (docs/design.md
// section 7, "Skills modificadas localmente").
type ReviewDiff struct {
	// LocallyModified is true for a Lock skill whose installed folder
	// differs from the version its lock entry records.
	LocallyModified bool
	// RegisteredAvailable is true when that registered version was read
	// from upstream by its tree SHA; User and Author then hold the review.
	RegisteredAvailable bool
	// Installed is the diff from the installed folder to upstream: the
	// whole review of an unedited skill, and of an edited one whose
	// registered version upstream no longer holds.
	Installed []FileDiff
	// User is the user's changes, from the registered version to the
	// installed folder.
	User []FileDiff
	// Author is the author's changes, from the registered version to
	// upstream.
	Author []FileDiff
}

// Split reports whether the review shows the user's and the author's changes
// as two diffs.
func (r ReviewDiff) Split() bool { return r.LocallyModified && r.RegisteredAvailable }

// errRegisteredGone reports a registered version upstream no longer holds.
var errRegisteredGone = errors.New("the registered version is no longer upstream")

// DiffForReview returns what the review of a candidate shows. For a Lock
// skill the user edited, it reads the registered version by its tree SHA
// and returns the user's and the author's changes apart; when upstream no
// longer holds that version, it returns the installed → upstream diff and
// says so. Any other candidate gets the diff Diff returns. It changes
// nothing.
func (a *App) DiffForReview(ctx context.Context, c domain.UpdateCandidate) (ReviewDiff, error) {
	name, isLock := strings.CutPrefix(c.Target, domain.LockID(""))
	if !isLock || !slices.Contains(c.Labels, domain.LabelLocallyModified) {
		diffs, err := a.Diff(ctx, c)
		if err != nil {
			return ReviewDiff{}, err
		}
		return ReviewDiff{Installed: diffs}, nil
	}

	repo, registered, err := a.registeredTree(ctx, name, c.FromSHA)
	if errors.Is(err, errRegisteredGone) {
		diffs, err := a.Diff(ctx, c)
		if err != nil {
			return ReviewDiff{}, err
		}
		return ReviewDiff{LocallyModified: true, Installed: diffs}, nil
	}
	if err != nil {
		return ReviewDiff{}, err
	}

	installed, err := a.installedFolder(c.Target)
	if err != nil {
		return ReviewDiff{}, err
	}
	installedTree, err := a.hashDir(installed)
	if err != nil {
		return ReviewDiff{}, fmt.Errorf("hash the installed folder: %w", err)
	}
	stagedTree, err := a.hashDir(c.StagedPath)
	if err != nil {
		return ReviewDiff{}, fmt.Errorf("hash the staged folder: %w", err)
	}

	blobs := map[string][]byte{}
	readRegistered := func(path, sha string) ([]byte, error) {
		if b, ok := blobs[sha]; ok {
			return b, nil
		}
		b, err := a.d.GitHub.Blob(ctx, repo, sha)
		if err != nil {
			return nil, fmt.Errorf("read the registered %s: %w", path, err)
		}
		blobs[sha] = b
		return b, nil
	}
	user, err := diffFiles(treeChanges(registered.Nodes, installedTree.Nodes), readRegistered, a.readFrom(installed, "installed"))
	if err != nil {
		return ReviewDiff{}, err
	}
	author, err := diffFiles(treeChanges(registered.Nodes, stagedTree.Nodes), readRegistered, a.readFrom(c.StagedPath, "staged"))
	if err != nil {
		return ReviewDiff{}, err
	}
	return ReviewDiff{LocallyModified: true, RegisteredAvailable: true, User: user, Author: author}, nil
}

// registeredTree reads, by its tree SHA, the version the lock entry name
// records, from the entry's repository. errRegisteredGone reports an entry
// or a tree upstream no longer has.
func (a *App) registeredTree(ctx context.Context, name, treeSHA string) (string, treehash.Tree, error) {
	lock, err := a.d.Lock.Read()
	if err != nil {
		return "", treehash.Tree{}, fmt.Errorf("read the skills lock: %w", err)
	}
	i := slices.IndexFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == name })
	if i < 0 || lock.Entries[i].SourceType != "github" {
		return "", treehash.Tree{}, errRegisteredGone
	}
	repo := lock.Entries[i].Source
	tree, err := a.d.GitHub.TreeBySHA(ctx, repo, treeSHA)
	if errors.Is(err, ports.ErrRefNotFound) || errors.Is(err, ports.ErrRepoNotFound) {
		return "", treehash.Tree{}, fmt.Errorf("%s in %s: %w", treeSHA, repo, errRegisteredGone)
	}
	if err != nil {
		return "", treehash.Tree{}, fmt.Errorf("read the registered version of %s: %w", name, err)
	}
	return repo, tree, nil
}

// hashDir hashes a folder with Deps.HashDir, or treehash.Dir when unset.
func (a *App) hashDir(dir string) (treehash.Tree, error) {
	if a.d.HashDir != nil {
		return a.d.HashDir(dir)
	}
	return treehash.Dir(dir)
}

// treeChanges lists the files that differ between two trees, by path. It
// follows the rule internal/updates uses to stage a candidate's FileChanges.
func treeChanges(oldNodes, newNodes []treehash.Node) []domain.FileChange {
	files := func(nodes []treehash.Node) map[string]treehash.Node {
		m := map[string]treehash.Node{}
		for _, n := range nodes {
			if !n.IsTree() {
				m[n.Path] = n
			}
		}
		return m
	}
	before, after := files(oldNodes), files(newNodes)
	out := []domain.FileChange{}
	for p, n := range after {
		o, ok := before[p]
		switch {
		case !ok:
			out = append(out, domain.FileChange{Path: p, Change: domain.ChangeAdded, NewMode: string(n.Mode), NewSHA: n.SHA})
		case o.SHA != n.SHA || o.Mode != n.Mode:
			out = append(out, domain.FileChange{
				Path: p, Change: domain.ChangeModified,
				OldMode: string(o.Mode), NewMode: string(n.Mode), OldSHA: o.SHA, NewSHA: n.SHA,
			})
		}
	}
	for p, o := range before {
		if _, ok := after[p]; !ok {
			out = append(out, domain.FileChange{Path: p, Change: domain.ChangeRemoved, OldMode: string(o.Mode), OldSHA: o.SHA})
		}
	}
	slices.SortFunc(out, func(a, b domain.FileChange) int { return strings.Compare(a.Path, b.Path) })
	return out
}
