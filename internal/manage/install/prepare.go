package install

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"go.yaml.in/yaml/v3"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/staging"
	"skillhub/internal/updates/risk"
)

// unparsedFrontmatter is the delta key of a SKILL.md header that does not
// parse, as in an update; it raises new_permissions.
const unparsedFrontmatter = "(unparsed frontmatter)"

// StagingRoot is where installs are prepared: ~/.agents/.skillhub/staging,
// beside the store and on its mount, so publishing is a rename
// (docs/design-v2.md section 11, point 6), and outside every directory a
// harness reads, like the parking area (Paths.ParkedDir).
func StagingRoot(p paths.Paths) string { return p.InstallStagingDir() }

// Preparer stages a resolved skill and reviews it. It writes only below
// StagingRoot.
type Preparer struct {
	Paths  paths.Paths
	Files  staging.Files
	GitHub staging.Blobs
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
}

// Review is a staged skill and what the user approves: its files, labels
// and frontmatter, all against an empty folder, as an update reviews a
// change (docs/design-v2.md section 7, step 2). The approval binds TreeSHA.
type Review struct {
	Resolved Resolved
	// StagedPath is StagingRoot/<key>-<tree SHA>.
	StagedPath string
	// TreeSHA is the SHA of the staged tree, which is Resolved.FolderSHA.
	TreeSHA          string
	FileChanges      []domain.FileChange
	FrontmatterDelta domain.FrontmatterDelta
	Labels           []domain.Label
	RulesetVersion   int
}

// Prepare stages r at StagingRoot/<key>-<FolderSHA>, replacing an earlier
// staging of the same tree, and reviews it. A website skill (r.Site) has no
// published tree: its downloaded files are hashed first (hashWeb), and the
// same stager writes and verifies them. A tree staging refuses is the
// staging error (staging.ErrUnsupportedEntry, ErrLimitsExceeded,
// ErrBlobMismatch or ErrTreeMismatch) and leaves no staging behind.
func (p Preparer) Prepare(ctx context.Context, r Resolved) (Review, error) {
	if r.Key == "" || r.Key != StoreKey(r.Key) {
		return Review{}, fmt.Errorf("prepare: store key %q is not a sanitized name", r.Key)
	}
	var blobs staging.Blobs = p.GitHub
	if r.Site != "" {
		var err error
		if r, blobs, err = p.hashWeb(r); err != nil {
			return Review{}, fmt.Errorf("prepare %s: %w", r.Key, err)
		}
	}
	dir := filepath.Join(StagingRoot(p.Paths), r.Key+"-"+r.FolderSHA)
	stager := staging.Stager{Files: p.Files, Blobs: blobs, HashDir: p.HashDir}
	staged, err := stager.Stage(ctx, staging.Request{Repo: r.Repo, Dir: dir, Nodes: r.Nodes, SHA: r.FolderSHA})
	if err != nil {
		return Review{}, fmt.Errorf("prepare %s: %w", r.Key, err)
	}
	rev := Review{Resolved: r, StagedPath: dir, TreeSHA: staged.SHA, FileChanges: []domain.FileChange{}}
	in := risk.Input{StagedFiles: []string{}, AddedLines: []risk.AddedFileLines{}}
	for _, n := range staged.Nodes {
		if n.IsTree() {
			continue
		}
		rev.FileChanges = append(rev.FileChanges, domain.FileChange{
			Path: n.Path, Change: domain.ChangeAdded, NewMode: string(n.Mode), NewSHA: n.SHA,
		})
		in.StagedFiles = append(in.StagedFiles, n.Path)
		data, err := p.Files.ReadFile(filepath.Join(dir, filepath.FromSlash(n.Path)))
		if err != nil {
			_ = p.Files.RemoveTree(dir) //nolint:errcheck // staging is a disposable cache.
			return Review{}, fmt.Errorf("prepare %s: read %s: %w", r.Key, n.Path, err)
		}
		if !risk.IsAnalyzableText(data) {
			in.NonTextFiles = append(in.NonTextFiles, n.Path)
		} else if lines := addedLines(data); len(lines) > 0 {
			in.AddedLines = append(in.AddedLines, risk.AddedFileLines{Path: n.Path, Lines: lines})
		}
		if n.Path == skillFile {
			rev.FrontmatterDelta = frontmatterDelta(data)
		}
	}
	slices.SortFunc(rev.FileChanges, func(a, b domain.FileChange) int { return strings.Compare(a.Path, b.Path) })
	if rev.FrontmatterDelta.Changed == nil {
		rev.FrontmatterDelta.Changed = []string{}
	}
	in.FileChanges, in.FrontmatterDelta = rev.FileChanges, rev.FrontmatterDelta
	rev.Labels = risk.Classify(in)
	rev.RulesetVersion = risk.RulesetVersion
	return rev, nil
}

// addedLines returns the lines of a file added to an empty folder, as an
// update computes them; a file that is not UTF-8 text has none.
func addedLines(after []byte) []string {
	if !risk.IsAnalyzableText(after) {
		return nil
	}
	var out []string
	for _, edit := range udiff.Lines("", string(after)) {
		if edit.New == "" {
			continue
		}
		out = append(out, strings.Split(strings.TrimSuffix(edit.New, "\n"), "\n")...)
	}
	return out
}

// frontmatterDelta lists every frontmatter key of a SKILL.md, nested keys
// joined by dots, sorted: against an empty folder each one is new. A header
// that does not parse or a non-analyzable file is unparsedFrontmatter.
func frontmatterDelta(data []byte) domain.FrontmatterDelta {
	if !risk.IsAnalyzableText(data) {
		return domain.FrontmatterDelta{Changed: []string{unparsedFrontmatter}}
	}
	changed := []string{}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, found := bytes.CutPrefix(data, []byte("---\n"))
	if !found {
		return domain.FrontmatterDelta{Changed: changed}
	}
	header, _, found := bytes.Cut(rest, []byte("\n---"))
	var fields map[string]any
	if !found || yaml.Unmarshal(header, &fields) != nil {
		return domain.FrontmatterDelta{Changed: []string{unparsedFrontmatter}}
	}
	changed = flattenKeys("", fields, changed)
	slices.Sort(changed)
	return domain.FrontmatterDelta{Changed: slices.Compact(changed)}
}

func flattenKeys(prefix string, fields map[string]any, out []string) []string {
	for k, v := range fields {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
			out = flattenKeys(key, nested, out)
			continue
		}
		out = append(out, key)
	}
	return out
}
