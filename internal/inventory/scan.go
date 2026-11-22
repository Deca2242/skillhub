package inventory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// excludedDirs are provider folders that are never listed, as home-relative
// patterns (docs/design.md section 2). A scanned directory or entry at or
// below one of them is left out.
var excludedDirs = []string{
	".cursor/skills-cursor",
	".codex/skills/.system",
	".codex/.tmp",
	".codex/plugins",
	".claude/plugins",
	".claude/skills/synced",
	".gemini/antigravity-cli/brain/*/scratch",
}

// offDirName is the folder of the store where the user parks disabled skills
// by hand (local-inventory.md section 5).
const offDirName = "OFF"

// maxCloneDepth bounds how deep below a clone's root its skills are looked
// for: skills/<name>/SKILL.md is depth 2.
const maxCloneDepth = 4

// folder is one entry of a scanned directory that may be part of a skill.
type folder struct {
	entry   ports.DirEntry
	agentID string
	// order is the position of the scanned directory: the store first,
	// then the agents table.
	order int
}

func (f folder) broken() bool { return f.entry.Kind == ports.EntryBrokenLink }

func (f folder) kind() domain.LocationKind {
	switch f.entry.Kind {
	case ports.EntryBrokenLink:
		return domain.LocationBrokenLink
	case ports.EntrySymlink:
		return domain.LocationSymlink
	default:
		return domain.LocationReal
	}
}

// rank orders folders for picking a skill's main folder: a real folder, then
// a link, then a broken link.
func (f folder) rank() int {
	switch f.kind() {
	case domain.LocationReal:
		return 0
	case domain.LocationSymlink:
		return 1
	default:
		return 2
	}
}

func compareFolders(a, b folder) int {
	return cmp.Or(cmp.Compare(a.rank(), b.rank()), cmp.Compare(a.order, b.order), cmp.Compare(a.entry.Path, b.entry.Path))
}

// unit is every folder that resolves to the same real folder; a broken link
// is a unit of its own.
type unit struct {
	folders []folder
}

func (u *unit) best() folder { return slices.MinFunc(u.folders, compareFolders) }

func (u *unit) names() []string {
	var names []string
	for _, f := range u.folders {
		names = append(names, f.entry.Name)
		if f.entry.ResolvedPath != "" {
			names = append(names, filepath.Base(f.entry.ResolvedPath))
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// building is a skill being assembled.
type building struct {
	id     string
	name   string
	origin domain.Origin
	state  domain.State
	// primary is the skill's own folder; nil when it has none.
	primary *folder
	units   []*unit
	// lockHash is compared with the primary folder of an active lock skill.
	lockHash string
	clone    *ports.CloneStatus
	// localCommits is true for a clone whose HEAD has commits its origin
	// tracking branch does not.
	localCommits bool
}

type scan struct {
	deps                Deps
	ctx                 context.Context
	gentleManagesSkills bool

	units     []*unit
	unitByKey map[string]*unit
	// taken marks units a GitHub clone took, and clones of other hosts
	// without SKILL.md at their root, which are not skills.
	taken map[*unit]bool
	// lockUnits marks units that are the store folder of a lock entry.
	lockUnits map[*unit]bool
	// storeEntries are the kept entries of the store, by path.
	storeEntries map[string]folder
	off          map[string]folder
	skills       []*building

	hashes       map[string]string
	frontmatters map[string]frontmatter
}

func (s *scan) homeRel(p string) string {
	if rel, ok := s.deps.Paths.HomeRel(p); ok {
		return rel
	}
	return filepath.ToSlash(p)
}

func (s *scan) excluded(p string) bool {
	rel, ok := s.deps.Paths.HomeRel(p)
	if !ok {
		return false
	}
	segs := strings.Split(rel, "/")
	for _, pattern := range excludedDirs {
		n := strings.Count(pattern, "/") + 1
		if len(segs) < n {
			continue
		}
		if ok, _ := path.Match(pattern, strings.Join(segs[:n], "/")); ok {
			return true
		}
	}
	return false
}

type scannedDir struct {
	path    string
	agentID string
}

func (s *scan) dirs() ([]scannedDir, error) {
	store := s.deps.Paths.StoreDir()
	out := []scannedDir{{path: store, agentID: domain.StoreAgentID}}
	seen := map[string]bool{store: true}
	for _, a := range s.deps.Agents {
		dir, err := s.deps.Paths.Join(filepath.FromSlash(a.GlobalDir))
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", a.ID, err)
		}
		if seen[dir] || s.excluded(dir) {
			continue
		}
		seen[dir] = true
		out = append(out, scannedDir{path: dir, agentID: a.ID})
	}
	return out, nil
}

// list scans dir; a missing directory has no entries.
func (s *scan) list(dir string) ([]ports.DirEntry, error) {
	entries, err := s.deps.Dirs.Scan(dir)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	return entries, nil
}

func isFolderKind(k ports.EntryKind) bool {
	return k == ports.EntryDir || k == ports.EntrySymlink
}

// collect scans every directory, groups the entries into units and turns
// GitHub clones into skills.
func (s *scan) collect() error {
	s.unitByKey = map[string]*unit{}
	s.taken = map[*unit]bool{}
	s.lockUnits = map[*unit]bool{}
	s.storeEntries = map[string]folder{}
	s.off = map[string]folder{}

	dirs, err := s.dirs()
	if err != nil {
		return err
	}
	for order, dir := range dirs {
		entries, err := s.list(dir.path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			keep := e.Kind == ports.EntryBrokenLink || (isFolderKind(e.Kind) && (e.HasSkillMD || e.HasGit))
			if !keep || s.excluded(e.Path) {
				continue
			}
			f := folder{entry: e, agentID: dir.agentID, order: order}
			if dir.agentID == domain.StoreAgentID {
				s.storeEntries[e.Path] = f
			}
			s.addToUnit(f)
		}
	}

	offEntries, err := s.list(filepath.Join(s.deps.Paths.StoreDir(), offDirName))
	if err != nil {
		return err
	}
	for _, e := range offEntries {
		if isFolderKind(e.Kind) && e.HasSkillMD {
			s.off[e.Name] = folder{entry: e, agentID: domain.StoreAgentID}
		}
	}

	for _, u := range s.units {
		if err := s.classifyClone(u); err != nil {
			return err
		}
	}
	return nil
}

func unitKey(f folder) string {
	if f.broken() {
		return "broken:" + f.entry.Path
	}
	return f.entry.ResolvedPath
}

func (s *scan) addToUnit(f folder) {
	key := unitKey(f)
	u, ok := s.unitByKey[key]
	if !ok {
		u = &unit{}
		s.unitByKey[key] = u
		s.units = append(s.units, u)
	}
	u.folders = append(u.folders, f)
}

// classifyClone turns a unit holding a .git directory into skills when its
// origin is on GitHub. Any other repository is a plain folder, listed only
// when it holds SKILL.md at its root.
func (s *scan) classifyClone(u *unit) error {
	root := u.best()
	if root.broken() || !root.entry.HasGit {
		return nil
	}
	status, err := s.deps.Git.Describe(s.ctx, root.entry.ResolvedPath)
	if err != nil {
		return fmt.Errorf("describe the clone %s: %w", root.entry.Path, err)
	}
	if !isGitHubRemote(status.Remote) {
		if !root.entry.HasSkillMD {
			s.taken[u] = true
		}
		return nil
	}
	s.taken[u] = true
	found, err := s.cloneSkillDirs(root.entry)
	if err != nil {
		return err
	}
	localCommits := s.hasLocalCommits(root.entry.ResolvedPath, status)
	for _, e := range found {
		b := &building{
			id:   domain.GitID(s.homeRel(e.Path)),
			name: e.Name,
			origin: domain.Origin{Kind: domain.OriginGitClone, GitClone: &domain.GitCloneOrigin{
				Root:     root.entry.ResolvedPath,
				Remote:   status.Remote,
				Branch:   status.Branch,
				Upstream: status.Upstream,
				Head:     status.Head,
			}},
			state:        domain.StateUnchecked,
			clone:        &status,
			localCommits: localCommits,
		}
		if e.Path == root.entry.Path {
			b.primary = &root
			b.units = []*unit{u}
		} else {
			f := folder{entry: e, agentID: root.agentID, order: root.order}
			b.primary = &f
			b.units = []*unit{{folders: []folder{f}}}
		}
		s.skills = append(s.skills, b)
	}
	return nil
}

// hasLocalCommits reports whether a clone's HEAD is not an ancestor of the
// origin branch it tracks: commits of the user's, or a history that diverged
// (docs/design.md section 7, "Skills modificadas localmente"). The scan never
// fetches, so the tracking branch stands for upstream; when git cannot tell,
// for instance because the tracking branch was never fetched, the clone is
// not marked here and the check decides.
func (s *scan) hasLocalCommits(root string, st ports.CloneStatus) bool {
	if st.Head == "" || st.Branch == "" || !strings.HasPrefix(st.Upstream, "origin/") {
		return false
	}
	ok, err := s.deps.Git.IsAncestor(s.ctx, root, st.Head, st.Upstream)
	return err == nil && !ok
}

// cloneSkillDirs finds the skill folders of a clone: its root when it holds
// SKILL.md, otherwise every folder holding one below it, without following
// links or entering .git.
func (s *scan) cloneSkillDirs(root ports.DirEntry) ([]ports.DirEntry, error) {
	if root.HasSkillMD {
		return []ports.DirEntry{root}, nil
	}
	var found []ports.DirEntry
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		entries, err := s.list(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Name == ".git" || e.Kind != ports.EntryDir {
				continue
			}
			if e.HasSkillMD {
				found = append(found, e)
			} else if depth < maxCloneDepth {
				if err := walk(e.Path, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root.Path, 1); err != nil {
		return nil, err
	}
	return found, nil
}

// isGitHubRemote reports whether a remote URL points at github.com, in URL
// form (https://, ssh://, git://) or scp form (git@github.com:owner/repo).
func isGitHubRemote(remote string) bool {
	r := strings.TrimSpace(remote)
	var host string
	if _, rest, ok := strings.Cut(r, "://"); ok {
		host, _, _ = strings.Cut(rest, "/")
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		host, _, _ = strings.Cut(host, ":")
	} else {
		before, _, ok := strings.Cut(r, ":")
		if !ok || strings.Contains(before, "/") {
			return false
		}
		host = before
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	}
	return strings.EqualFold(host, "github.com")
}

// buildLockSkills makes one skill per lock entry. Its folder is the store
// folder named after the entry, through any link; when that is absent, the
// folder parked in OFF/ makes it disabled, and with neither it is missing.
func (s *scan) buildLockSkills(entries []domain.LockEntry) {
	store := s.deps.Paths.StoreDir()
	for _, e := range entries {
		b := &building{
			id:   domain.LockID(e.Name),
			name: e.Name,
			origin: domain.Origin{Kind: domain.OriginLock, Lock: &domain.LockOrigin{
				Source:     e.Source,
				SkillPath:  e.SkillPath,
				FolderHash: e.SkillFolderHash,
				Ref:        e.Ref,
			}},
			state: domain.StateMissing,
		}
		if f, ok := s.storeEntries[filepath.Join(store, e.Name)]; ok {
			// A unit a clone took is not this entry's folder. Two lock
			// entries may share one folder through a link; each lists it.
			if u := s.unitByKey[unitKey(f)]; !s.taken[u] {
				s.lockUnits[u] = true
				b.units = append(b.units, u)
				if !f.broken() {
					b.primary = &f
					b.state = domain.StateUnchecked
					b.lockHash = e.SkillFolderHash
				}
			}
		}
		if f, ok := s.off[e.Name]; ok {
			b.units = append(b.units, &unit{folders: []folder{f}})
			if b.primary == nil {
				b.primary = &f
				b.state = domain.StateDisabled
			}
		}
		s.skills = append(s.skills, b)
	}
}

// aliases are every name of a skill's units other than its own.
func (b *building) aliases() []string {
	names := []string{}
	for _, u := range b.units {
		for _, n := range u.names() {
			if n != b.name {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// attachOriginless groups the remaining folders: a folder sharing a name
// with a lock skill joins it, otherwise one sharing a name with exactly one
// clone skill joins that; the rest group by name into skills without origin.
// gentle-ai copies are left out here, so the rule never hides a lock entry.
func (s *scan) attachOriginless() error {
	byName := map[string]*building{}
	gitNames := map[string][]*building{}
	for _, b := range s.skills {
		switch b.origin.Kind {
		case domain.OriginLock:
			for _, n := range append([]string{b.name}, b.aliases()...) {
				if _, ok := byName[n]; !ok {
					byName[n] = b
				}
			}
		case domain.OriginGitClone:
			gitNames[b.name] = append(gitNames[b.name], b)
		}
	}
	for n, bs := range gitNames {
		if _, ok := byName[n]; !ok && len(bs) == 1 {
			byName[n] = bs[0]
		}
	}

	var rest []*unit
	for _, u := range s.units {
		if s.taken[u] || s.lockUnits[u] {
			continue
		}
		best := u.best()
		if !best.broken() && s.gentleManagesSkills {
			fm, err := s.frontmatter(best.entry.ResolvedPath)
			if err != nil {
				return err
			}
			if fm.author == gentleAuthor {
				continue
			}
		}
		rest = append(rest, u)
	}
	slices.SortFunc(rest, func(a, b *unit) int { return compareFolders(a.best(), b.best()) })

	noOrigin := map[string]*building{}
	for _, u := range rest {
		names := u.names()
		target := firstMatch(names, byName)
		if target == nil {
			target = firstMatch(names, noOrigin)
		}
		if target == nil {
			best := u.best()
			target = &building{
				id:      domain.DirID(s.homeRel(best.entry.Path)),
				name:    best.entry.Name,
				origin:  domain.Origin{Kind: domain.OriginNone},
				state:   domain.StateNoOrigin,
				primary: &best,
			}
			s.skills = append(s.skills, target)
		}
		target.units = append(target.units, u)
		if target.origin.Kind == domain.OriginNone {
			for _, n := range names {
				if _, ok := noOrigin[n]; !ok {
					noOrigin[n] = target
				}
			}
		}
	}
	return nil
}

func firstMatch(names []string, index map[string]*building) *building {
	for _, n := range names {
		if b, ok := index[n]; ok {
			return b
		}
	}
	return nil
}

func (s *scan) hash(dir string) (string, error) {
	if sha, ok := s.hashes[dir]; ok {
		return sha, nil
	}
	if err := s.deps.Paths.Check(dir); err != nil {
		return "", err
	}
	tree, err := s.deps.HashDir(dir)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", dir, err)
	}
	s.hashes[dir] = tree.SHA
	return tree.SHA, nil
}

// finish turns a building into a Skill: locations with the main folder
// first, description, state and issues.
func (s *scan) finish(b *building) (domain.Skill, error) {
	var rest []folder
	seen := map[string]bool{}
	if b.primary != nil {
		seen[b.primary.entry.Path] = true
	}
	for _, u := range b.units {
		for _, f := range u.folders {
			if !seen[f.entry.Path] {
				seen[f.entry.Path] = true
				rest = append(rest, f)
			}
		}
	}
	slices.SortFunc(rest, func(x, y folder) int {
		return cmp.Or(cmp.Compare(x.order, y.order), cmp.Compare(x.entry.Path, y.entry.Path))
	})
	ordered := rest
	if b.primary != nil {
		ordered = append([]folder{*b.primary}, rest...)
	}

	skill := domain.Skill{
		ID:        b.id,
		Name:      b.name,
		Aliases:   b.aliases(),
		Origin:    b.origin,
		State:     b.state,
		Locations: []domain.Location{},
		Issues:    []domain.Issue{},
	}
	for _, f := range ordered {
		loc := domain.Location{Path: f.entry.Path, AgentID: f.agentID, Kind: f.kind()}
		if !f.broken() {
			sha, err := s.hash(f.entry.ResolvedPath)
			if err != nil {
				return domain.Skill{}, err
			}
			loc.ContentTreeSHA = sha
		}
		skill.Locations = append(skill.Locations, loc)
	}

	var primarySHA, primaryDir string
	if b.primary != nil && !b.primary.broken() {
		primarySHA = skill.Locations[0].ContentTreeSHA
		primaryDir = b.primary.entry.ResolvedPath
		fm, err := s.frontmatter(primaryDir)
		if err != nil {
			return domain.Skill{}, err
		}
		skill.Description = fm.description
	}
	skill.Issues = issues(b, ordered, skill.Locations, primaryDir, primarySHA)
	return skill, nil
}

// issues lists a skill's issues in the order the domain declares them.
func issues(b *building, ordered []folder, locs []domain.Location, primaryDir, primarySHA string) []domain.Issue {
	out := []domain.Issue{}
	locallyModified := (b.lockHash != "" && primarySHA != "" && primarySHA != b.lockHash) ||
		(b.clone != nil && (!b.clone.Clean || b.localCommits))
	if locallyModified {
		out = append(out, domain.IssueLocallyModified)
	}
	var duplicate, divergent, broken bool
	compared := map[string]bool{primaryDir: true}
	for i, f := range ordered {
		if f.broken() {
			broken = true
			continue
		}
		if primaryDir == "" || compared[f.entry.ResolvedPath] {
			continue
		}
		compared[f.entry.ResolvedPath] = true
		if locs[i].ContentTreeSHA == primarySHA {
			duplicate = true
		} else {
			divergent = true
		}
	}
	if duplicate {
		out = append(out, domain.IssueDuplicate)
	}
	if divergent {
		out = append(out, domain.IssueDivergentCopy)
	}
	if broken {
		out = append(out, domain.IssueBrokenLink)
	}
	if b.clone != nil && (b.clone.Branch == "" || !strings.HasPrefix(b.clone.Upstream, "origin/")) {
		out = append(out, domain.IssueUntrackedBranch)
	}
	return out
}
