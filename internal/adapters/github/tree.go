package github

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

type rawTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type rawTreeResponse struct {
	SHA       string         `json:"sha"`
	Tree      []rawTreeEntry `json:"tree"`
	Truncated bool           `json:"truncated"`
}

func normalizeTreePath(p string) (string, error) {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" || trimmed == "." {
		return "", nil
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("invalid tree path %q: %w", p, ports.ErrRefNotFound)
	}
	return cleaned, nil
}

func buildTreeFromRecursive(resp rawTreeResponse, rootTreeSHA, subPath string) (treehash.Tree, error) {
	rootSHA := rootTreeSHA
	var filtered []rawTreeEntry
	if subPath == "" {
		filtered = resp.Tree
	} else {
		foundTree := false
		for _, e := range resp.Tree {
			if e.Path == subPath {
				if e.Type != "tree" {
					return treehash.Tree{}, fmt.Errorf("path %q is %s, not a tree: %w", subPath, e.Type, ports.ErrRefNotFound)
				}
				rootSHA = e.SHA
				foundTree = true
				break
			}
		}
		if !foundTree {
			return treehash.Tree{}, fmt.Errorf("path %q not found: %w", subPath, ports.ErrRefNotFound)
		}
		prefix := subPath + "/"
		for _, e := range resp.Tree {
			if rel, ok := strings.CutPrefix(e.Path, prefix); ok {
				e.Path = rel
				filtered = append(filtered, e)
			}
		}
	}

	nodes := sortAndFlatten(filtered)
	return treehash.Tree{SHA: rootSHA, Nodes: nodes}, nil
}

func (c *Client) walkTruncatedTree(ctx context.Context, repo, rootSHA, subPath string) (treehash.Tree, error) {
	targetSHA := rootSHA
	if subPath != "" {
		var err error
		targetSHA, err = c.resolveSubtreeSHA(ctx, repo, rootSHA, subPath)
		if err != nil {
			return treehash.Tree{}, err
		}
	}
	var all []rawTreeEntry
	if err := c.collectSubtree(ctx, repo, targetSHA, "", &all); err != nil {
		return treehash.Tree{}, err
	}
	return treehash.Tree{
		SHA:   targetSHA,
		Nodes: sortAndFlatten(all),
	}, nil
}

func (c *Client) resolveSubtreeSHA(ctx context.Context, repo, rootSHA, subPath string) (string, error) {
	curSHA := rootSHA
	for _, seg := range strings.Split(subPath, "/") {
		resp, err := c.fetchSingleTree(ctx, repo, curSHA)
		if err != nil {
			return "", err
		}
		var next *rawTreeEntry
		for i := range resp.Tree {
			if resp.Tree[i].Path == seg {
				next = &resp.Tree[i]
				break
			}
		}
		if next == nil || next.Type != "tree" {
			return "", fmt.Errorf("path %q not found: %w", subPath, ports.ErrRefNotFound)
		}
		curSHA = next.SHA
	}
	return curSHA, nil
}

func (c *Client) collectSubtree(ctx context.Context, repo, treeSHA, prefix string, out *[]rawTreeEntry) error {
	resp, err := c.fetchSingleTree(ctx, repo, treeSHA)
	if err != nil {
		return err
	}
	if resp.Truncated {
		return fmt.Errorf("github: non-recursive tree %s@%s was truncated", repo, treeSHA)
	}
	for _, e := range resp.Tree {
		rel := e.Path
		if prefix != "" {
			rel = prefix + "/" + e.Path
		}
		entry := e
		entry.Path = rel
		*out = append(*out, entry)
		if e.Type == "tree" {
			if err := c.collectSubtree(ctx, repo, e.SHA, rel, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) fetchSingleTree(ctx context.Context, repo, treeSHA string) (rawTreeResponse, error) {
	endpoint := fmt.Sprintf("repos/%s/git/trees/%s", repo, treeSHA)
	body, err := c.get(ctx, endpoint, "", false)
	if err != nil {
		return rawTreeResponse{}, c.disambiguateNotFound(ctx, repo, err)
	}
	var resp rawTreeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return rawTreeResponse{}, fmt.Errorf("github: decode tree %s@%s: %w", repo, treeSHA, err)
	}
	return resp, nil
}

type dirItem struct {
	name     string
	node     treehash.Node
	children []*dirItem
}

func sortAndFlatten(entries []rawTreeEntry) []treehash.Node {
	root := &dirItem{}
	dirs := map[string]*dirItem{"": root}

	// Ensure every directory item exists first.
	for _, e := range entries {
		if e.Type == "tree" {
			dirs[e.Path] = &dirItem{
				name: path.Base(e.Path),
				node: toNode(e),
			}
		}
	}
	for _, e := range entries {
		parentPath := path.Dir(e.Path)
		if parentPath == "." {
			parentPath = ""
		}
		parent := dirs[parentPath]
		if parent == nil {
			continue
		}
		if e.Type == "tree" {
			parent.children = append(parent.children, dirs[e.Path])
		} else {
			parent.children = append(parent.children, &dirItem{
				name: path.Base(e.Path),
				node: toNode(e),
			})
		}
	}

	var out []treehash.Node
	var visit func(d *dirItem)
	visit = func(d *dirItem) {
		slices.SortFunc(d.children, func(a, b *dirItem) int {
			return strings.Compare(gitSortKey(a), gitSortKey(b))
		})
		for _, ch := range d.children {
			out = append(out, ch.node)
			if ch.node.IsTree() {
				visit(ch)
			}
		}
	}
	visit(root)
	return out
}

func gitSortKey(it *dirItem) string {
	if it.node.IsTree() {
		return it.name + "/"
	}
	return it.name
}

func toNode(e rawTreeEntry) treehash.Node {
	size := e.Size
	if e.Type == "tree" || e.Type == "commit" {
		size = 0
	}
	return treehash.Node{
		Path: e.Path,
		Mode: treehash.Mode(e.Mode),
		SHA:  e.SHA,
		Size: size,
	}
}
