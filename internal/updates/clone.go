package updates

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// This file is the git clone origin (docs/design.md section 7, "Clones de
// git"). A clone is checked against the head of the branch it tracks on
// GitHub, staged from GitHub like any skill, and applied only by a
// fast-forward of a clean clone to the exact commit the user approved.

// isCloneTarget reports whether target names a clone root.
func isCloneTarget(target string) bool { return strings.HasPrefix(target, domain.GitID("")) }

// cloneTarget returns the target id of a clone root: its path relative to
// the home.
func (s *Service) cloneTarget(root string) (string, error) {
	rel, ok := s.d.Paths.HomeRel(root)
	if !ok || rel == "." {
		return "", fmt.Errorf("%s: %w: not inside the home", root, ErrNotUpdatable)
	}
	return domain.GitID(rel), nil
}

// cloneRoot returns the clone root a target id names.
func (s *Service) cloneRoot(target string) (string, error) {
	rel, ok := strings.CutPrefix(target, domain.GitID(""))
	if !ok {
		return "", fmt.Errorf("%s: %w: not a clone", target, ErrNotUpdatable)
	}
	root, err := s.d.Paths.Join(filepath.FromSlash(rel))
	if err != nil {
		return "", fmt.Errorf("%s: %w: %w", target, ErrNotUpdatable, err)
	}
	return root, nil
}

// checkCloneRoot accepts a clone skillhub may snapshot and swap: a real
// folder, not a link, holding a .git directory. A .git file points at a git
// directory elsewhere, which a copy of the folder would not hold.
func (s *Service) checkCloneRoot(root string) error {
	for _, p := range []string{root, filepath.Join(root, ".git")} {
		mode, err := s.d.Files.Mode(p)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w: missing", p, ErrNotUpdatable)
		}
		if err != nil {
			return fmt.Errorf("look at %s: %w", p, err)
		}
		if !mode.IsDir() {
			return fmt.Errorf("%s: %w: not a directory", p, ErrNotUpdatable)
		}
	}
	return nil
}

// cloneSHA identifies a clone's whole content: its work tree, ignored files
// included, and its .git directory. A snapshot of a clone must hash to it,
// and a restored clone too.
func (s *Service) cloneSHA(root string) (string, error) {
	work, err := s.d.HashDir(root)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", root, err)
	}
	gitDir, err := s.d.HashDir(filepath.Join(root, ".git"))
	if err != nil {
		return "", fmt.Errorf("hash %s/.git: %w", root, err)
	}
	return work.SHA + "+" + gitDir.SHA, nil
}

// trackedBranch returns the branch of origin that HEAD's branch tracks.
func trackedBranch(st ports.CloneStatus) (string, bool) {
	branch, ok := strings.CutPrefix(st.Upstream, "origin/")
	return branch, ok && st.Branch != "" && branch != ""
}

// githubRepo returns owner/name of a GitHub remote URL, in URL form
// (https://, ssh://, git://) or scp form (git@github.com:owner/name).
func githubRepo(remote string) (string, bool) {
	r := strings.TrimSpace(remote)
	var host, rest string
	if _, after, ok := strings.Cut(r, "://"); ok {
		host, rest, _ = strings.Cut(after, "/")
		host, _, _ = strings.Cut(host[strings.LastIndex(host, "@")+1:], ":")
	} else {
		var ok bool
		if host, rest, ok = strings.Cut(r, ":"); !ok || strings.Contains(host, "/") {
			return "", false
		}
		host = host[strings.LastIndex(host, "@")+1:]
	}
	if !strings.EqualFold(host, "github.com") {
		return "", false
	}
	repo := strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
	return repo, validRepo(repo)
}

// listClones returns the clones to check, by target, without duplicates.
func (s *Service) listClones(ctx context.Context) ([]Clone, error) {
	if s.d.Clones == nil {
		return nil, nil
	}
	clones, err := s.d.Clones(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the git clones: %w", err)
	}
	out := []Clone{}
	for _, cl := range clones {
		if !slices.ContainsFunc(out, func(o Clone) bool { return o.Root == cl.Root }) {
			out = append(out, cl)
		}
	}
	return out, nil
}

// cloneMeta is what a clone's staging records about the clone, next to the
// staged folder: the apply revalidates it under the state lock.
type cloneMeta struct {
	Head     string `json:"head"`
	Branch   string `json:"branch"`
	Upstream string `json:"upstream"`
	Remote   string `json:"remote"`
}

func metaOf(st ports.CloneStatus) cloneMeta {
	return cloneMeta{Head: st.Head, Branch: st.Branch, Upstream: st.Upstream, Remote: st.Remote}
}

// checkClone compares one clone with the head of the branch it tracks and
// stages a candidate when needed. It reads the clone and never changes it:
// no fetch, no merge.
func (r *checkRun) checkClone(tgt string, cl Clone) TargetCheck {
	s, ctx := r.s, r.ctx
	fail := func(err error) TargetCheck { return TargetCheck{Target: tgt, Outcome: CheckFailed, Err: err} }
	notChecked := func(err error) TargetCheck { return TargetCheck{Target: tgt, Outcome: CheckNotChecked, Err: err} }

	root, err := s.cloneRoot(tgt)
	if err != nil || root != cl.Root {
		return notChecked(fmt.Errorf("%s: %w: its id does not name its folder", cl.Root, ErrNotUpdatable))
	}
	if err := s.checkCloneRoot(root); errors.Is(err, ErrNotUpdatable) {
		return notChecked(err)
	} else if err != nil {
		return fail(err)
	}
	status, err := s.d.Git.Describe(ctx, root)
	if err != nil {
		return fail(fmt.Errorf("describe %s: %w", root, err))
	}
	branch, ok := trackedBranch(status)
	if !ok || status.Head == "" {
		return notChecked(fmt.Errorf("%s: %w", root, ErrUntrackedBranch))
	}
	repo, ok := githubRepo(status.Remote)
	if !ok {
		return notChecked(fmt.Errorf("%s: %w: origin is not a GitHub repository", root, ErrNotUpdatable))
	}
	commit, err := s.d.GitHub.ResolveCommit(ctx, repo, branch)
	if err != nil {
		return fail(fmt.Errorf("resolve %s@%s: %w", repo, branch, err))
	}
	if commit == status.Head {
		r.supersede(tgt)
		return TargetCheck{Target: tgt, Outcome: CheckUpToDate}
	}
	installed, err := s.d.Git.TreeSHA(ctx, root, status.Head)
	if err != nil {
		return fail(fmt.Errorf("tree of HEAD in %s: %w", root, err))
	}
	reason, guessed, err := s.cloneBlockReason(ctx, root, status, commit)
	if err != nil {
		return fail(err)
	}
	upstream, err := s.d.GitHub.Tree(ctx, repo, commit, "")
	if err != nil {
		return fail(fmt.Errorf("tree of %s@%s: %w", repo, commit, err))
	}
	var head treehash.Tree
	if reason == "" {
		if head, err = s.d.GitHub.Tree(ctx, repo, status.Head, ""); err != nil {
			return fail(fmt.Errorf("tree of %s@%s: %w", repo, status.Head, err))
		}
		transforms, err := s.cloneAttributes(ctx, repo, root, head, upstream)
		if err != nil {
			return fail(err)
		}
		if transforms {
			reason = domain.BlockGitAttributes
		}
	}
	if slices.ContainsFunc(r.st.Skips, func(sk domain.Skip) bool { return sk.Target == tgt && sk.ToSHA == upstream.SHA }) {
		r.supersede(tgt)
		return TargetCheck{Target: tgt, Outcome: CheckSkipped}
	}
	meta := metaOf(status)
	for _, c := range r.st.Candidates {
		if c.Target != tgt || !isOpen(c) || c.ToCommit != commit || c.ToSHA != upstream.SHA || c.InstalledSHAAtStage != installed {
			continue
		}
		// A failed fast-forward proved local commits that the clone, put back
		// as it was, can no longer show: keep that verdict.
		if c.State == domain.CandidateBlocked && (c.BlockReason == reason || guessed && c.BlockReason == domain.BlockLocalCommits) {
			return TargetCheck{Target: tgt, Outcome: CheckBlocked, CandidateID: c.ID}
		}
		if c.State == domain.CandidatePending && reason == "" {
			if m, err := s.stagedMeta(c); err != nil || m.Clone != meta {
				continue
			}
			if staged, err := s.d.HashDir(c.StagedPath); err == nil && staged.SHA == c.ToSHA {
				return TargetCheck{Target: tgt, Outcome: CheckUpdateAvailable, CandidateID: c.ID}
			}
		}
	}

	c := domain.UpdateCandidate{
		ID:                  r.newCandidateID(tgt, upstream.SHA, installed),
		Target:              tgt,
		Skills:              slices.Clone(cl.Skills),
		FromSHA:             installed,
		InstalledSHAAtStage: installed,
		ToCommit:            commit,
		ToSHA:               upstream.SHA,
		FirstSeenAt:         r.now,
		State:               domain.CandidatePending,
		FileChanges:         []domain.FileChange{},
		Labels:              []domain.Label{},
	}
	if c.Skills == nil {
		c.Skills = []string{}
	}
	if reason != "" {
		// A blocked clone is not staged: nothing in it may be applied.
		c.State, c.BlockReason = domain.CandidateBlocked, reason
	} else {
		if head.SHA != installed {
			return fail(fmt.Errorf("%s@%s has tree %s on GitHub and %s in %s", repo, status.Head, head.SHA, installed, root))
		}
		st := stager{s: s, ctx: ctx, repo: repo, folder: root, installed: head, clone: true}
		if err := st.stage(&c, upstream.Nodes); err != nil {
			return fail(err)
		}
		if c.State == domain.CandidatePending {
			if err := s.saveStagedMeta(c, stagedMeta{Clone: meta}); err != nil {
				s.discardStaging(c)
				return fail(err)
			}
		}
	}
	r.supersede(tgt)
	r.st.Candidates = append(r.st.Candidates, c)
	r.created = append(r.created, c.ID)
	if c.State == domain.CandidateBlocked {
		return TargetCheck{Target: tgt, Outcome: CheckBlocked, CandidateID: c.ID}
	}
	return TargetCheck{Target: tgt, Outcome: CheckUpdateAvailable, CandidateID: c.ID}
}

// cloneBlockReason says why a clone cannot fast-forward to commit, or "":
// a dirty work tree, or a HEAD that is not an ancestor of upstream (local
// commits, or a divergent history). A check never fetches, so when the clone
// does not have commit yet, its origin tracking branch stands for upstream
// and guessed is true; the apply checks the ancestry again after its fetch.
func (s *Service) cloneBlockReason(ctx context.Context, root string, st ports.CloneStatus, commit string) (reason domain.BlockReason, guessed bool, err error) {
	if !st.Clean {
		return domain.BlockDirtyWorktree, false, nil
	}
	base := commit
	if _, err := s.d.Git.TreeSHA(ctx, root, commit); err != nil {
		base, guessed = st.Upstream, true
	}
	ok, err := s.d.Git.IsAncestor(ctx, root, st.Head, base)
	if err != nil {
		return "", false, fmt.Errorf("ancestry of HEAD in %s: %w", root, err)
	}
	if !ok {
		return domain.BlockLocalCommits, guessed, nil
	}
	return "", guessed, nil
}

// clonePlan is a revalidated apply of a clone.
type clonePlan struct {
	s         *Service
	ctx       context.Context
	candidate domain.UpdateCandidate
	root      string
	meta      cloneMeta
}

// revalidateClone checks, under the state lock and before anything changes,
// that the clone is still the one the candidate was staged from: same head,
// branch, upstream and origin, still clean, and that the staged copy is
// whole (docs/design.md section 7, step 6).
func (s *Service) revalidateClone(ctx context.Context, c domain.UpdateCandidate, a Approval) (clonePlan, error) {
	if err := checkApproval(c, a); err != nil {
		return clonePlan{}, err
	}
	root, err := s.cloneRoot(c.Target)
	if err != nil {
		return clonePlan{}, rejected(err, "%s", c.Target)
	}
	if err := s.checkCloneRoot(root); errors.Is(err, ErrNotUpdatable) {
		return clonePlan{}, rejected(err, "%s", c.Target)
	} else if err != nil {
		return clonePlan{}, err
	}
	meta, err := s.stagedMeta(c)
	if errors.Is(err, errNoStagedRef) {
		return clonePlan{}, rejected(ErrStagedChanged, "%s: %v", c.ID, err)
	}
	if err != nil {
		return clonePlan{}, err
	}
	status, err := s.d.Git.Describe(ctx, root)
	if err != nil {
		return clonePlan{}, fmt.Errorf("describe %s: %w", root, err)
	}
	if !status.Clean {
		return clonePlan{}, rejected(ErrInstalledChanged, "%s is no longer clean", root)
	}
	if got := metaOf(status); got != meta.Clone {
		return clonePlan{}, rejected(ErrInstalledChanged, "%s is %+v, staged at %+v", root, got, meta.Clone)
	}
	tree, err := s.d.Git.TreeSHA(ctx, root, status.Head)
	if err != nil {
		return clonePlan{}, fmt.Errorf("tree of HEAD in %s: %w", root, err)
	}
	if tree != c.InstalledSHAAtStage {
		return clonePlan{}, rejected(ErrInstalledChanged, "%s is %s, staged at %s", root, tree, c.InstalledSHAAtStage)
	}
	if err := s.checkStaged(c); err != nil {
		return clonePlan{}, err
	}
	if err := s.checkStagedAttributes(c, root); err != nil {
		return clonePlan{}, err
	}
	return clonePlan{s: s, ctx: ctx, candidate: c, root: root, meta: meta.Clone}, nil
}

func (p clonePlan) target() domain.UpdateCandidate { return p.candidate }

// snapshot copies the whole clone, .git included.
func (p clonePlan) snapshot(id string, now time.Time) (snapshotRecord, error) {
	sha, err := p.s.cloneSHA(p.root)
	if err != nil {
		return snapshotRecord{}, err
	}
	return p.s.takeSnapshot(snapshotRecord{
		Snapshot:   domain.Snapshot{ID: id, Target: p.candidate.Target, TakenAt: now, Cause: domain.SnapshotBeforeApply},
		FolderPath: p.root, FolderSHA: sha, TreeSHA: p.candidate.InstalledSHAAtStage,
	})
}

// change fetches the approved commit, checks that HEAD is its ancestor and
// fast-forwards (step 8), then verifies the clone (step 9).
func (p clonePlan) change(time.Time) error {
	git, c := p.s.d.Git, p.candidate
	if err := git.Fetch(p.ctx, p.root, c.ToCommit); err != nil {
		return fmt.Errorf("fetch %s: %w", c.ToCommit, err)
	}
	// The fetched commit must be the reviewed tree, whose attributes the
	// revalidation checked; and the clone must still carry no attribute of
	// its own that transforms a checkout.
	fetched, err := git.TreeSHA(p.ctx, p.root, c.ToCommit)
	if err != nil {
		return fmt.Errorf("tree of %s: %w", c.ToCommit, err)
	}
	if fetched != c.ToSHA {
		return fmt.Errorf("%w: fetched %s has tree %s, reviewed %s", ErrVerifyFailed, c.ToCommit, fetched, c.ToSHA)
	}
	if err := p.s.checkInfoAttributes(p.root); err != nil {
		return err
	}
	ok, err := git.IsAncestor(p.ctx, p.root, p.meta.Head, c.ToCommit)
	if err != nil {
		return fmt.Errorf("ancestry of %s: %w", c.ToCommit, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s is not an ancestor of %s", ErrNotFastForward, p.meta.Head, c.ToCommit)
	}
	if err := git.FastForward(p.ctx, p.root, c.ToCommit); err != nil {
		return fmt.Errorf("fast-forward to %s: %w", c.ToCommit, err)
	}
	status, err := git.Describe(p.ctx, p.root)
	if err != nil {
		return fmt.Errorf("%w: describe %s: %w", ErrVerifyFailed, p.root, err)
	}
	if status.Head != c.ToCommit || !status.Clean || status.Branch != p.meta.Branch {
		return fmt.Errorf("%w: %s is %+v, want a clean %s at %s", ErrVerifyFailed, p.root, status, p.meta.Branch, c.ToCommit)
	}
	tree, err := git.TreeSHA(p.ctx, p.root, status.Head)
	if err != nil {
		return fmt.Errorf("%w: tree of HEAD: %w", ErrVerifyFailed, err)
	}
	if tree != c.ToSHA {
		return fmt.Errorf("%w: HEAD has tree %s, want %s", ErrVerifyFailed, tree, c.ToSHA)
	}
	return p.verifyFiles()
}

// verifyFiles checks the files git wrote, not only HEAD: every file of the
// reviewed tree must be in the work tree with the reviewed content and mode.
// This is the safety net against anything that transforms a checkout. The
// work tree is compared file by file rather than by its tree SHA, because it
// may also hold the user's ignored files, which the commit does not have and
// which stay; git status already proved that no other file is there.
func (p clonePlan) verifyFiles() error {
	c := p.candidate
	staged, err := p.s.d.HashDir(c.StagedPath)
	if err != nil || staged.SHA != c.ToSHA {
		return fmt.Errorf("%w: the staged copy of %s is gone or changed", ErrVerifyFailed, c.ToSHA)
	}
	work, err := p.s.d.HashDir(p.root)
	if err != nil {
		return fmt.Errorf("%w: hash %s: %w", ErrVerifyFailed, p.root, err)
	}
	installed := map[string]treehash.Node{}
	for _, n := range work.Nodes {
		if !n.IsTree() {
			installed[n.Path] = n
		}
	}
	for _, n := range staged.Nodes {
		if n.IsTree() {
			continue
		}
		got, ok := installed[n.Path]
		if !ok || got.SHA != n.SHA || got.Mode != n.Mode {
			return fmt.Errorf("%w: %s is not the reviewed file", ErrVerifyFailed, n.Path)
		}
	}
	return nil
}

// checkoutAttributes are the git attributes that make a checkout write
// something other than the blob: a filter driver runs a command and writes
// its output; eol, text, crlf and working-tree-encoding convert the
// content; ident rewrites $Id$. binary is a macro for -diff -merge -text and
// blocks too: -text is itself a checkout attribute, and a clone is updated
// only when git writes every blob exactly as it was reviewed.
var checkoutAttributes = map[string]bool{
	"filter": true, "eol": true, "text": true, "crlf": true,
	"working-tree-encoding": true, "ident": true, "binary": true,
}

// transformsCheckout reports whether a gitattributes file sets, unsets or
// defines in a macro any checkout attribute. Attributes are matched as whole
// names, with any -, ! or =value. A pattern that happens to be one of these
// names counts too: blocking more is the safe side.
func transformsCheckout(content []byte) bool {
	for line := range strings.Lines(string(content)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, field := range strings.Fields(line) {
			name := strings.TrimLeft(strings.TrimPrefix(field, "[attr]"), "-!")
			name, _, _ = strings.Cut(name, "=")
			if checkoutAttributes[name] {
				return true
			}
		}
	}
	return false
}

// cloneAttributes reports whether any .gitattributes of the given GitHub
// trees, or the clone's .git/info/attributes, transforms a checkout. Blobs
// are read through GitHub and checked against their SHA.
func (s *Service) cloneAttributes(ctx context.Context, repo, root string, trees ...treehash.Tree) (bool, error) {
	for _, tree := range trees {
		for _, n := range tree.Nodes {
			if n.IsTree() || path.Base(n.Path) != ".gitattributes" {
				continue
			}
			content, err := s.d.GitHub.Blob(ctx, repo, n.SHA)
			if err != nil {
				return false, fmt.Errorf("blob %s of %s: %w", n.SHA, n.Path, err)
			}
			if treehash.BlobSHA(content) != n.SHA {
				return false, fmt.Errorf("blob %s of %s: %w", n.SHA, n.Path, errBlobMismatch)
			}
			if transformsCheckout(content) {
				return true, nil
			}
		}
	}
	if err := s.checkInfoAttributes(root); errors.Is(err, ErrGitAttributes) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}

// checkInfoAttributes refuses a clone whose .git/info/attributes transforms
// a checkout. That file is the user's own, but it applies to the files the
// update writes.
func (s *Service) checkInfoAttributes(root string) error {
	content, err := s.d.Files.ReadFile(filepath.Join(root, ".git", "info", "attributes"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the attributes of %s: %w", root, err)
	}
	if transformsCheckout(content) {
		return fmt.Errorf("%w: %s/.git/info/attributes", ErrGitAttributes, root)
	}
	return nil
}

// checkStagedAttributes applies the rule of the check to what the apply
// would install: the staged copy, which is the reviewed commit, and the
// clone's .git/info/attributes.
func (s *Service) checkStagedAttributes(c domain.UpdateCandidate, root string) error {
	staged, err := s.d.HashDir(c.StagedPath)
	if err != nil {
		return rejected(ErrStagedChanged, "%s: %v", c.StagedPath, err)
	}
	for _, n := range staged.Nodes {
		if n.IsTree() || path.Base(n.Path) != ".gitattributes" {
			continue
		}
		content, err := s.d.Files.ReadFile(filepath.Join(c.StagedPath, filepath.FromSlash(n.Path)))
		if err != nil {
			return fmt.Errorf("read the staged %s: %w", n.Path, err)
		}
		if transformsCheckout(content) {
			return rejected(ErrGitAttributes, "%s", n.Path)
		}
	}
	if err := s.checkInfoAttributes(root); err != nil {
		if errors.Is(err, ErrGitAttributes) {
			return rejected(err, "%s", root)
		}
		return err
	}
	return nil
}

// cloneUndoState reads what an undo of a clone finds: the tree of HEAD, and
// whether the clone changed after the apply that left applied. A clone git
// cannot describe counts as changed, so its copy is kept.
func (s *Service) cloneUndoState(ctx context.Context, root, appliedSHA string) (tree string, changed bool) {
	status, err := s.d.Git.Describe(ctx, root)
	if err != nil || status.Head == "" {
		return "", true
	}
	tree, err = s.d.Git.TreeSHA(ctx, root, status.Head)
	if err != nil {
		return "", true
	}
	return tree, !status.Clean || tree != appliedSHA
}
