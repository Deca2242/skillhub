package install

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/staging"
)

var (
	// ErrNoSkill reports a source with no installable skill, or none with
	// the selected name.
	ErrNoSkill = errors.New("no skill found at the source")
	// ErrAmbiguousSkill is the error every *AmbiguousSkillError matches.
	ErrAmbiguousSkill = errors.New("several skills found at the source")
)

// AmbiguousSkillError lists the skill folders a source holds when it must
// hold one: no name was selected, or several skills have that name.
type AmbiguousSkillError struct {
	// Folders are relative to the repository root, sorted.
	Folders []string
}

func (e *AmbiguousSkillError) Error() string {
	return fmt.Sprintf("%s: %s", ErrAmbiguousSkill, strings.Join(e.Folders, ", "))
}

// Is makes every AmbiguousSkillError match ErrAmbiguousSkill.
func (e *AmbiguousSkillError) Is(target error) bool { return target == ErrAmbiguousSkill }

// GitHub is the part of ports.GitHubPort resolving reads through.
type GitHub interface {
	DefaultBranch(ctx context.Context, repo string) (string, error)
	ResolveCommit(ctx context.Context, repo, ref string) (string, error)
	Tree(ctx context.Context, repo, commit, path string) (treehash.Tree, error)
	Blob(ctx context.Context, repo, sha string) ([]byte, error)
}

// Resolver resolves a source to one skill folder at one commit. It only
// reads.
type Resolver struct {
	GitHub GitHub
}

// Resolved is one skill folder of a GitHub repository at one commit, with
// what a lock entry of npx skills records about it.
type Resolved struct {
	// Repo is owner/name; it is the lock entry's source.
	Repo string
	// Ref is the ref the user pinned, "" when the default branch was
	// followed. A lock entry gets a ref only when it is not "".
	Ref string
	// Subpath is the folder of the repository that was searched.
	Subpath string
	// Commit is the commit the folder was read at.
	Commit string
	// Folder is the skill folder, relative to the repository root; "" is
	// the root.
	Folder string
	// SkillPath is the folder's SKILL.md relative to the repository root:
	// the lock entry's skillPath.
	SkillPath string
	// FolderSHA is the git tree SHA of the folder: the lock entry's
	// skillFolderHash and the SHA a staged copy must hash to.
	FolderSHA string
	// Nodes is the folder's tree, every path relative to it.
	Nodes []treehash.Node
	// Name is the frontmatter name: the key of the lock entry.
	Name string
	// Key is the store folder, ~/.agents/skills/<Key> (StoreKey of Name).
	Key string
	// SourceURL is the lock entry's sourceUrl.
	SourceURL string

	// Site is set for a website source (WebResolver): the lock entry's
	// sourceBaseUrl. Repo is then the site's host, the entry's source, and
	// Ref, Commit, Subpath, Folder and SkillPath are empty.
	Site string `json:",omitempty"`
	// Digest is a website skill's wellKnownDigest.
	Digest string `json:",omitempty"`
	// files are the downloaded files of a website skill by path; Prepare
	// stages them and computes FolderSHA and Nodes.
	files map[string][]byte
}

// Resolve resolves src: the pinned ref, or the head of the default branch,
// to a commit; then the one skill folder below src.Subpath, selected by
// src.Skill when it is set (discover.go says how, as npx skills does).
// No skill is ErrNoSkill and several are an *AmbiguousSkillError; GitHub
// failures keep their ports error, such as ports.ErrRefNotFound.
func (r Resolver) Resolve(ctx context.Context, src Source) (Resolved, error) {
	res := Resolved{Repo: src.Repo, Ref: src.Ref, Subpath: src.Subpath, SourceURL: "https://github.com/" + src.Repo + ".git"}
	var err error
	if res.Commit, err = r.commit(ctx, &res, src.Tree); err != nil {
		return Resolved{}, err
	}
	tree, err := r.GitHub.Tree(ctx, res.Repo, res.Commit, res.Subpath)
	if err != nil {
		return Resolved{}, fmt.Errorf("read %s at %s: %w", res.Repo, res.Commit, err)
	}
	folder, name, err := r.locate(ctx, res, tree.Nodes, src.Skill)
	if err != nil {
		return Resolved{}, err
	}
	res.Folder = joinPath(res.Subpath, folder)
	res.SkillPath = joinPath(res.Folder, skillFile)
	res.Name, res.Key = name, StoreKey(name)
	res.FolderSHA, res.Nodes = subtree(tree, folder)
	return res, nil
}

// commit resolves the commit of res.Ref, or of the default branch. A
// /tree/ path is split into a ref and a subpath here: the shortest prefix
// that names a ref wins, so a ref may hold slashes (feature/x) and a plain
// one behaves as npx skills reads it.
func (r Resolver) commit(ctx context.Context, res *Resolved, treePath string) (string, error) {
	if treePath != "" {
		parts := strings.Split(treePath, "/")
		for i := 1; i <= len(parts); i++ {
			ref := strings.Join(parts[:i], "/")
			commit, err := r.GitHub.ResolveCommit(ctx, res.Repo, ref)
			if errors.Is(err, ports.ErrRefNotFound) {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("resolve %s of %s: %w", ref, res.Repo, err)
			}
			res.Ref, res.Subpath = ref, strings.Join(parts[i:], "/")
			return commit, nil
		}
		return "", fmt.Errorf("no ref of %s starts %s: %w", res.Repo, treePath, ports.ErrRefNotFound)
	}
	ref := res.Ref
	if ref == "" {
		branch, err := r.GitHub.DefaultBranch(ctx, res.Repo)
		if err != nil {
			return "", fmt.Errorf("default branch of %s: %w", res.Repo, err)
		}
		ref = branch
	}
	commit, err := r.GitHub.ResolveCommit(ctx, res.Repo, ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s of %s: %w", ref, res.Repo, err)
	}
	return commit, nil
}

// locate picks the skill folder among the candidates of nodes and returns
// it, relative to the search root, with its frontmatter name.
func (r Resolver) locate(ctx context.Context, res Resolved, nodes []treehash.Node, filter string) (string, string, error) {
	var folders, names []string
	for _, dir := range candidates(nodes) {
		fm, err := r.readSkillMD(ctx, res.Repo, nodes, joinPath(dir, skillFile))
		if err != nil {
			return "", "", err
		}
		if fm.ok {
			folders, names = append(folders, dir), append(names, fm.name)
		}
	}
	picked := make([]int, len(folders))
	for i := range picked {
		picked[i] = i
	}
	if filter != "" {
		picked = matchesFilter(names, filter)
	}
	switch len(picked) {
	case 0:
		if filter != "" {
			return "", "", fmt.Errorf("%s has no skill named %q: %w", res.Repo, filter, ErrNoSkill)
		}
		return "", "", fmt.Errorf("%s: %w", res.Repo, ErrNoSkill)
	case 1:
		return folders[picked[0]], names[picked[0]], nil
	}
	ae := &AmbiguousSkillError{}
	for _, i := range picked {
		ae.Folders = append(ae.Folders, joinPath(res.Subpath, folders[i]))
	}
	return "", "", ae
}

// readSkillMD downloads the SKILL.md at p and parses its frontmatter. Its
// content is checked against its blob SHA first.
func (r Resolver) readSkillMD(ctx context.Context, repo string, nodes []treehash.Node, p string) (frontmatter, error) {
	for _, n := range nodes {
		if n.Path != p || n.IsTree() {
			continue
		}
		if n.Size > staging.MaxBytes {
			return frontmatter{}, nil
		}
		content, err := r.GitHub.Blob(ctx, repo, n.SHA)
		if err != nil {
			return frontmatter{}, fmt.Errorf("read %s: %w", p, err)
		}
		if treehash.BlobSHA(content) != n.SHA {
			return frontmatter{}, fmt.Errorf("read %s: %w", p, staging.ErrBlobMismatch)
		}
		return parseSkillMD(content), nil
	}
	return frontmatter{}, nil
}

// subtree returns the SHA and the nodes of dir in tree, relative to dir.
func subtree(tree treehash.Tree, dir string) (string, []treehash.Node) {
	if dir == "" {
		return tree.SHA, append([]treehash.Node(nil), tree.Nodes...)
	}
	var sha string
	var out []treehash.Node
	for _, n := range tree.Nodes {
		if n.Path == dir && n.IsTree() {
			sha = n.SHA
		}
		if rel, ok := strings.CutPrefix(n.Path, dir+"/"); ok {
			n.Path = rel
			out = append(out, n)
		}
	}
	return sha, out
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	if name == "" {
		return dir
	}
	return path.Join(dir, name)
}
