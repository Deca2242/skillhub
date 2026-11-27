package updates

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"go.yaml.in/yaml/v3"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/staging"
	"skillhub/internal/updates/risk"
)

// errBlobMismatch reports a blob whose content does not hash to its SHA.
var errBlobMismatch = staging.ErrBlobMismatch

// unparsedFrontmatter is the delta key of a SKILL.md header that does not
// parse. It is not a known safe key, so it raises new_permissions.
const unparsedFrontmatter = "(unparsed frontmatter)"

// stager stages one candidate.
type stager struct {
	s    *Service
	ctx  context.Context
	repo string
	// folder is the installed skill folder, or the clone root; installed is
	// its tree, with paths relative to it.
	folder    string
	installed treehash.Tree
	// clone is true for a clone: its labels are computed skill folder by
	// skill folder.
	clone bool
}

// stage builds the staged folder of c from the upstream nodes, labels it,
// and fills c. An upstream tree skillhub must not install blocks c and
// leaves no staging; err is only for failures worth retrying.
func (st stager) stage(c *domain.UpdateCandidate, nodes []treehash.Node) error {
	c.FileChanges, c.Labels = []domain.FileChange{}, []domain.Label{}
	files := st.s.d.Files
	dir := filepath.Join(st.s.d.Paths.StagingDir(), c.ID)
	discard := func() { _ = files.RemoveTree(dir) } //nolint:errcheck // staging is a disposable cache.
	stager := staging.Stager{Files: files, Blobs: st.s.d.GitHub, HashDir: st.s.d.HashDir}
	staged, err := stager.Stage(st.ctx, staging.Request{
		Repo: st.repo, Dir: dir, Nodes: nodes, SHA: c.ToSHA,
		Reuse: st.folder, ReuseNodes: st.installed.Nodes,
	})
	if reason := blockReason(err); reason != "" {
		c.State, c.BlockReason = domain.CandidateBlocked, reason
		return nil
	}
	if err != nil {
		return err
	}
	c.StagedPath = dir
	c.FileChanges = fileChanges(st.installed.Nodes, staged.Nodes)
	if err := st.label(c, dir, staged.Nodes); err != nil {
		discard()
		c.StagedPath = ""
		return err
	}
	c.RulesetVersion = risk.RulesetVersion
	return nil
}

// label sets the candidate's labels and frontmatter delta with
// risk.Classify. A lock skill is one skill folder. A clone is labeled skill
// folder by skill folder, every path relative to its skill folder, and the
// labels and delta are merged; files outside every skill folder are labeled
// relative to the clone root.
func (st stager) label(c *domain.UpdateCandidate, dir string, staged []treehash.Node) error {
	if !st.clone {
		in, err := st.riskInput(dir, st.folder, c.FileChanges, stagedFiles(staged, ""))
		if err != nil {
			return err
		}
		in.LocallyModified = c.InstalledSHAAtStage != c.FromSHA
		c.FrontmatterDelta = in.FrontmatterDelta
		c.Labels = risk.Classify(in)
		return nil
	}
	skills := skillFolders(st.installed.Nodes, staged)
	groups := map[string][]domain.FileChange{}
	for _, ch := range c.FileChanges {
		g := folderOf(ch.Path, skills)
		ch.Path = relTo(ch.Path, g)
		groups[g] = append(groups[g], ch)
	}
	var sets [][]domain.Label
	changed := []string{}
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		in, err := st.riskInput(filepath.Join(dir, filepath.FromSlash(g)), filepath.Join(st.folder, filepath.FromSlash(g)), groups[g], stagedFiles(staged, g))
		if err != nil {
			return err
		}
		changed = append(changed, in.FrontmatterDelta.Changed...)
		sets = append(sets, risk.Classify(in))
	}
	slices.Sort(changed)
	c.FrontmatterDelta = domain.FrontmatterDelta{Changed: slices.Compact(changed)}
	c.Labels = mergeLabels(sets)
	return nil
}

// skillFolders lists the folders holding a SKILL.md in either tree, "" for
// the root, deepest first.
func skillFolders(trees ...[]treehash.Node) []string {
	var out []string
	for _, nodes := range trees {
		for _, n := range nodes {
			if n.IsTree() || path.Base(n.Path) != "SKILL.md" {
				continue
			}
			if dir := path.Dir(n.Path); dir != "." {
				out = append(out, dir)
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		return cmp.Or(cmp.Compare(strings.Count(b, "/"), strings.Count(a, "/")), strings.Compare(a, b))
	})
	return slices.Compact(out)
}

// folderOf returns the deepest skill folder holding p, or "" for the root.
func folderOf(p string, skills []string) string {
	for _, dir := range skills {
		if strings.HasPrefix(p, dir+"/") {
			return dir
		}
	}
	return ""
}

// relTo returns p relative to dir; dir "" is the root.
func relTo(p, dir string) string {
	if dir == "" {
		return p
	}
	return strings.TrimPrefix(p, dir+"/")
}

// stagedFiles lists the files of staged below dir, relative to it.
func stagedFiles(staged []treehash.Node, dir string) []string {
	out := []string{}
	for _, n := range staged {
		if !n.IsTree() && (dir == "" || strings.HasPrefix(n.Path, dir+"/")) {
			out = append(out, relTo(n.Path, dir))
		}
	}
	return out
}

// mergeLabels merges the labels of several skill folders in the order of
// domain.Labels(); text_only stays only when no folder has another label.
func mergeLabels(sets [][]domain.Label) []domain.Label {
	out := []domain.Label{}
	for _, l := range domain.Labels() {
		if l != domain.LabelTextOnly && slices.ContainsFunc(sets, func(set []domain.Label) bool { return slices.Contains(set, l) }) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return []domain.Label{domain.LabelTextOnly}
	}
	return out
}

// blockReason maps a staging refusal to the reason that blocks a
// candidate, or "" for no error or a failure worth retrying.
func blockReason(err error) domain.BlockReason {
	switch {
	case errors.Is(err, staging.ErrUnsupportedEntry):
		return domain.BlockUnsupportedEntry
	case errors.Is(err, staging.ErrLimitsExceeded):
		return domain.BlockLimitsExceeded
	case errors.Is(err, staging.ErrBlobMismatch), errors.Is(err, staging.ErrTreeMismatch):
		return domain.BlockHashMismatch
	}
	return ""
}

// fileChanges lists the files that differ between two trees, by path.
func fileChanges(oldNodes, newNodes []treehash.Node) []domain.FileChange {
	blobs := func(nodes []treehash.Node) map[string]treehash.Node {
		m := map[string]treehash.Node{}
		for _, n := range nodes {
			if !n.IsTree() {
				m[n.Path] = n
			}
		}
		return m
	}
	before, after := blobs(oldNodes), blobs(newNodes)
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

// riskInput builds the input of risk.Classify for one skill folder, staged
// at dir and installed at folder, with every path relative to it.
func (st stager) riskInput(dir, folder string, changes []domain.FileChange, staged []string) (risk.Input, error) {
	in := risk.Input{FileChanges: changes, StagedFiles: staged, AddedLines: []risk.AddedFileLines{}}
	readExpected := func(root, rel string) ([]byte, error) {
		data, err := st.s.d.Files.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		return data, nil
	}
	for _, ch := range changes {
		if ch.Change == domain.ChangeRemoved || ch.OldSHA == ch.NewSHA {
			continue
		}
		after, err := readExpected(dir, ch.Path)
		if err != nil {
			return risk.Input{}, err
		}
		var before []byte
		beforePresent := false
		if ch.Change == domain.ChangeModified {
			beforePresent = true
			if before, err = readExpected(folder, ch.Path); err != nil {
				return risk.Input{}, err
			}
		}
		if !risk.IsAnalyzableText(after) || (beforePresent && !risk.IsAnalyzableText(before)) {
			in.NonTextFiles = append(in.NonTextFiles, ch.Path)
		}
		if lines := addedLines(before, beforePresent, after); len(lines) > 0 {
			in.AddedLines = append(in.AddedLines, risk.AddedFileLines{Path: ch.Path, Lines: lines})
		}
	}
	newPresent := slices.Contains(staged, "SKILL.md")
	oldPresent := newPresent
	for _, ch := range changes {
		if ch.Path != "SKILL.md" {
			continue
		}
		switch ch.Change {
		case domain.ChangeAdded:
			oldPresent = false
		case domain.ChangeModified:
			oldPresent, newPresent = true, true
		case domain.ChangeRemoved:
			oldPresent, newPresent = true, false
		}
		break
	}
	var oldMD, newMD []byte
	if oldPresent {
		var err error
		if oldMD, err = readExpected(folder, "SKILL.md"); err != nil {
			return risk.Input{}, err
		}
	}
	if newPresent {
		var err error
		if newMD, err = readExpected(dir, "SKILL.md"); err != nil {
			return risk.Input{}, err
		}
	}
	in.FrontmatterDelta = frontmatterDelta(oldMD, oldPresent, newMD, newPresent)
	return in, nil
}

// addedLines returns the lines after adds to before. When after is not
// analyzable UTF-8 text without NUL, it returns nil. When before is present
// and not analyzable, all lines of after are returned so text warnings on
// new content are preserved.
func addedLines(before []byte, beforePresent bool, after []byte) []string {
	if !risk.IsAnalyzableText(after) {
		return nil
	}
	diffBefore := ""
	if beforePresent && risk.IsAnalyzableText(before) {
		diffBefore = string(before)
	}
	var out []string
	for _, edit := range udiff.Lines(diffBefore, string(after)) {
		added := strings.TrimSuffix(edit.New, "\n")
		if edit.New == "" {
			continue
		}
		out = append(out, strings.Split(added, "\n")...)
	}
	return out
}

// frontmatterDelta lists the frontmatter keys of SKILL.md whose value was
// added, removed or changed, nested keys joined by dots, sorted.
func frontmatterDelta(before []byte, oldPresent bool, after []byte, newPresent bool) domain.FrontmatterDelta {
	oldKeys, oldOK := map[string]string{}, true
	newKeys, newOK := map[string]string{}, true
	switch {
	case !newPresent, oldPresent && bytes.Equal(before, after):
		if oldPresent {
			oldKeys, oldOK = frontmatterKeys(before)
		}
		if newPresent {
			newKeys, newOK = frontmatterKeys(after)
		}
	default:
		if oldPresent {
			if !risk.IsAnalyzableText(before) {
				oldOK = false
			} else {
				oldKeys, oldOK = frontmatterKeys(before)
			}
		}
		if !risk.IsAnalyzableText(after) {
			newOK = false
		} else {
			newKeys, newOK = frontmatterKeys(after)
		}
	}
	changed := []string{}
	if !newOK && (!oldPresent || !bytes.Equal(before, after)) || !oldOK && newOK {
		changed = append(changed, unparsedFrontmatter)
	}
	for k, v := range newKeys {
		if old, ok := oldKeys[k]; !ok || old != v {
			changed = append(changed, k)
		}
	}
	for k := range oldKeys {
		if _, ok := newKeys[k]; !ok {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	return domain.FrontmatterDelta{Changed: slices.Compact(changed)}
}

// frontmatterKeys flattens the YAML header of a SKILL.md into dotted keys
// and printed values. ok is false when a header is present but does not
// parse; a file without a header has no keys and ok true.
func frontmatterKeys(data []byte) (map[string]string, bool) {
	keys := map[string]string{}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, found := bytes.CutPrefix(data, []byte("---\n"))
	if !found {
		return keys, true
	}
	header, _, found := bytes.Cut(rest, []byte("\n---"))
	if !found {
		return keys, false
	}
	var fields map[string]any
	if err := yaml.Unmarshal(header, &fields); err != nil {
		return keys, false
	}
	flatten("", fields, keys)
	return keys, true
}

func flatten(prefix string, fields map[string]any, out map[string]string) {
	for k, v := range fields {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
			flatten(key, nested, out)
			continue
		}
		out[key] = fmt.Sprintf("%#v", v)
	}
}
