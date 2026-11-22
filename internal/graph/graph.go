// Package graph builds the read-only identity and visibility graph of version
// 2 (docs/design-v2.md section 11, item 3): which folder is a skill's
// canonical one, its lock key, aliases, frontmatter name and profile, and
// which harnesses see it and how. Identity data (lock key, origin, state)
// comes from the inventory; every relation (folder, link, target) comes from
// what is observed in each harness's read roots. Two skills are never the
// same because of their name. It only reads.
package graph

import (
	"cmp"
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/data"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// DirLister lists the direct entries of a directory; ports.SkillDirScanner
// satisfies it.
type DirLister interface {
	Scan(dir string) ([]ports.DirEntry, error)
}

// FileReader reads files and modes; the FileOps adapter satisfies it.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
	// Mode does not follow a final symlink.
	Mode(path string) (iofs.FileMode, error)
	Exists(path string) (bool, error)
}

// LinkReader reads the text of a symlink; the LinkOps adapter satisfies it.
type LinkReader interface {
	Readlink(path string) (string, error)
}

// ErrMissingDependency reports Deps without something Build needs.
var ErrMissingDependency = errors.New("graph: missing dependency")

// Deps are what Build reads.
type Deps struct {
	Paths     paths.Paths
	Harnesses []data.Harness
	Profiles  []data.DiagnosticProfile
	// Inventory is the result of inventory.Scan, without an ignore list.
	Inventory []domain.Skill
	Dirs      DirLister
	Files     FileReader
	Links     LinkReader
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
}

func (d Deps) validate() error {
	var missing []string
	if d.Paths.Home() == "" {
		missing = append(missing, "Paths")
	}
	if d.Dirs == nil {
		missing = append(missing, "Dirs")
	}
	if d.Files == nil {
		missing = append(missing, "Files")
	}
	if d.Links == nil {
		missing = append(missing, "Links")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %v", ErrMissingDependency, missing)
	}
	return nil
}

// Kind is how a harness sees a skill.
type Kind string

const (
	// Direct: the harness reads the canonical folder where it lives.
	Direct Kind = "direct"
	// ViaLink: through a symlink, in a harness known to follow links.
	ViaLink Kind = "link"
	// OwnCopy: a real folder of the skill other than the canonical one.
	OwnCopy Kind = "copy"
	// Indeterminate: the folder is in a read root whose depth, or whose
	// handling of links, is not verified.
	Indeterminate Kind = "indeterminate"
)

// Sighting is one way a harness, or a diagnostic profile, reaches a skill.
type Sighting struct {
	Harness string
	Kind    Kind
	// Entry is the path the harness finds, as listed.
	Entry string
}

// LinkStatus is where a symlink leads.
type LinkStatus string

const (
	LinkOK     LinkStatus = "ok"
	LinkBroken LinkStatus = "broken"
	LinkCycle  LinkStatus = "cycle"
	// LinkOutside: a hop leaves the known roots and is not followed.
	LinkOutside LinkStatus = "outside"
)

// Link is a symlink found in a read root.
type Link struct {
	Path string
	// Text is the content of the link, as written.
	Text string
	// Hops are the absolute lexical targets, one per link of the chain. A
	// relative text is resolved from the real directory holding its link, as
	// the kernel does, and a target below the real path of a known root is
	// written below that root, the way the roots are entered.
	Hops []string
	// Final is the last hop: the lexical target of a broken link.
	Final  string
	Status LinkStatus
	// Folder is the physical skill folder it reaches; empty when none.
	Folder string
}

// Zone is why something is not a manageable skill.
type Zone string

const (
	ZoneNone Zone = ""
	// ZoneProvider is a folder S1 leaves out (inventory.Excluded).
	ZoneProvider Zone = "provider"
	// ZonePlugin is a folder holding .claude-plugin.
	ZonePlugin Zone = "plugin"
	// ZoneShared is gentle-ai's _shared folder.
	ZoneShared Zone = "shared"
	// ZoneGentle is a skill folder gentle-ai manages.
	ZoneGentle Zone = "gentle_ai"
	// ZoneFile is a loose file in a skills directory, such as OFF/README.md.
	ZoneFile Zone = "file"
	// ZoneProfile is a skill of a read-only diagnostic profile.
	ZoneProfile Zone = "diagnostic_profile"
)

// Boundary is something met while walking that is never a manageable skill.
type Boundary struct {
	Path string
	Zone Zone
}

// Folder is a physical skill folder, however many entries reach it.
type Folder struct {
	// Path is the folder with every link resolved.
	Path string
	// Entry is the first real (not linked) entry listing it, or the final
	// hop of a link when none was walked.
	Entry   string
	Name    string
	TreeSHA string
	// Profile is the diagnostic profile whose root holds it; empty otherwise.
	Profile string
	// Entries are every listed path that reaches it.
	Entries   []string
	Sightings []Sighting
}

// Skill is a node of the graph.
type Skill struct {
	ID string
	// LockKey is the key of its lock entry; empty without one.
	LockKey string
	Origin  domain.OriginKind
	State   domain.State
	// Canonical is the physical folder of the skill: the store folder of
	// its lock key or of its own, or a clone's folder. Empty when there is
	// none, as for copies found only outside the store.
	Canonical string
	// Folders are the canonical folder first, then every real folder with
	// identical content that is attributed to it.
	Folders []string
	// Aliases are the store entries, other than the canonical folder's own,
	// that are links resolving to it.
	Aliases []string
	// Name is the frontmatter name.
	Name    string
	Profile string
	Zone    Zone
	// SharedWith lists other skills claiming the same canonical folder.
	SharedWith []string
	Visibility []Sighting
}

// Graph is the whole read-only graph.
type Graph struct {
	// Home and Store are the home and the store, as lexical paths.
	Home  string
	Store string
	// Harnesses are the detected harnesses, in table order.
	Harnesses []string
	Skills    []Skill
	Folders   []Folder
	Links     []Link
	Boundary  []Boundary
}

// maxWalk bounds every walk below a read root; only real directories are
// entered, so the walk itself cannot loop.
const maxWalk = 16

// maxHops bounds a link chain.
const maxHops = 40

type rawKind int

const (
	rawReal rawKind = iota
	rawLink
)

// observed is a skill folder entry found in a root.
type observed struct {
	entry  ports.DirEntry
	level  int
	kind   rawKind
	folder string
}

type builder struct {
	d          Deps
	ro         ports.FileOps
	store      string
	roots      []string
	gentle     bool
	walks      map[string][]observed
	folders    map[string]*Folder
	links      map[string]*Link
	boundary   map[string]Zone
	zoneByPath map[string]Zone
	// realDirs caches the real path of each directory resolved, the known
	// roots among them.
	realDirs map[string]string
}

// Build builds the graph.
func Build(d Deps) (Graph, error) {
	if err := d.validate(); err != nil {
		return Graph{}, err
	}
	if d.HashDir == nil {
		d.HashDir = treehash.Dir
	}
	b := &builder{
		d: d, ro: readOnly{d.Files}, store: d.Paths.StoreDir(),
		walks: map[string][]observed{}, folders: map[string]*Folder{}, links: map[string]*Link{}, realDirs: map[string]string{},
		boundary: map[string]Zone{}, zoneByPath: map[string]Zone{},
	}
	var err error
	if b.gentle, err = inventory.GentleManagesSkills(b.ro, d.Paths.GentleAIStateFile()); err != nil {
		return Graph{}, err
	}
	if err := b.collectRoots(); err != nil {
		return Graph{}, err
	}
	for _, r := range b.roots {
		if err := b.walk(r, r, 1); err != nil {
			return Graph{}, err
		}
	}
	g := Graph{Home: d.Paths.Home(), Store: b.store}
	if g.Harnesses, err = b.sight(); err != nil {
		return Graph{}, err
	}
	if g.Skills, err = b.skills(); err != nil {
		return Graph{}, err
	}
	for _, f := range b.folders {
		g.Folders = append(g.Folders, *f)
	}
	slices.SortFunc(g.Folders, func(x, y Folder) int { return cmp.Compare(x.Path, y.Path) })
	for _, l := range b.links {
		g.Links = append(g.Links, *l)
	}
	slices.SortFunc(g.Links, func(x, y Link) int { return cmp.Compare(x.Path, y.Path) })
	for p, z := range b.boundary {
		g.Boundary = append(g.Boundary, Boundary{Path: p, Zone: z})
	}
	slices.SortFunc(g.Boundary, func(x, y Boundary) int { return cmp.Compare(x.Path, y.Path) })
	return g, nil
}

func (b *builder) abs(rel string) (string, error) {
	return b.d.Paths.Join(filepath.FromSlash(rel))
}

// collectRoots gathers every read root, link dir and profile root: the known
// roots links may be followed into.
func (b *builder) collectRoots() error {
	set := map[string]bool{b.store: true}
	add := func(rel string) error {
		p, err := b.abs(rel)
		if err != nil {
			return err
		}
		set[p] = true
		return nil
	}
	for _, h := range b.d.Harnesses {
		for _, r := range h.ReadRoots {
			if err := add(r.Dir); err != nil {
				return fmt.Errorf("harness %s: %w", h.ID, err)
			}
		}
		if h.LinkDir != "" {
			if err := add(h.LinkDir); err != nil {
				return fmt.Errorf("harness %s: %w", h.ID, err)
			}
		}
	}
	for _, p := range b.d.Profiles {
		for _, r := range p.ReadRoots {
			if err := add(r.Dir); err != nil {
				return fmt.Errorf("profile %s: %w", p.ID, err)
			}
		}
	}
	for p := range set {
		b.roots = append(b.roots, p)
		if _, err := b.realDir(p); err != nil {
			return err
		}
	}
	slices.Sort(b.roots)
	return nil
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

func (b *builder) known(p string) bool {
	return slices.ContainsFunc(b.roots, func(r string) bool { return within(p, r) })
}

func (b *builder) list(dir string) ([]ports.DirEntry, error) {
	entries, err := b.d.Dirs.Scan(dir)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	return entries, nil
}

// walk records every skill folder, link and boundary below dir, a level
// below root.
func (b *builder) walk(root, dir string, level int) error {
	entries, err := b.list(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if inventory.Excluded(b.d.Paths, e.Path) {
			b.boundary[e.Path] = ZoneProvider
			continue
		}
		switch e.Kind {
		case ports.EntryFile, ports.EntryOther:
			b.boundary[e.Path] = ZoneFile
		case ports.EntryBrokenLink, ports.EntrySymlink:
			l, err := b.link(e.Path)
			if err != nil {
				return err
			}
			if l.Status != LinkOK || !e.HasSkillMD {
				continue
			}
			zone, err := b.zone(e.ResolvedPath, filepath.Base(l.Final))
			if err != nil {
				return err
			}
			if zone != ZoneNone || inventory.Excluded(b.d.Paths, l.Final) {
				continue
			}
			if err := b.observe(root, e, level, rawLink, l.Final); err != nil {
				return err
			}
			l.Folder = e.ResolvedPath
		case ports.EntryDir:
			if e.Name == ".git" {
				continue
			}
			zone, err := b.zone(e.Path, e.Name)
			if err != nil {
				return err
			}
			if zone != ZoneNone {
				b.boundary[e.Path] = zone
				continue
			}
			if e.HasSkillMD {
				if err := b.observe(root, e, level, rawReal, e.Path); err != nil {
					return err
				}
			} else if level < maxWalk {
				if err := b.walk(root, e.Path, level+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// zone tells whether the directory dir, named name, is a foreign zone.
func (b *builder) zone(dir, name string) (Zone, error) {
	if z, ok := b.zoneByPath[dir]; ok {
		return z, nil
	}
	z := ZoneNone
	switch {
	case name == "_shared":
		z = ZoneShared
	default:
		plugin, err := b.d.Files.Exists(filepath.Join(dir, ".claude-plugin"))
		if err != nil {
			return "", fmt.Errorf("look for .claude-plugin in %s: %w", dir, err)
		}
		if plugin {
			z = ZonePlugin
			break
		}
		if b.gentle {
			fm, err := inventory.ReadFrontmatter(b.ro, dir)
			if err != nil {
				return "", err
			}
			if inventory.IsGentleAuthor(fm) {
				z = ZoneGentle
			}
		}
	}
	b.zoneByPath[dir] = z
	return z, nil
}

// observe records a skill folder entry of root.
func (b *builder) observe(root string, e ports.DirEntry, level int, kind rawKind, lexical string) error {
	f, ok := b.folders[e.ResolvedPath]
	if !ok {
		if err := b.d.Paths.Check(e.ResolvedPath); err != nil {
			return err
		}
		tree, err := b.d.HashDir(e.ResolvedPath)
		if err != nil {
			return fmt.Errorf("hash %s: %w", e.ResolvedPath, err)
		}
		fm, err := inventory.ReadFrontmatter(b.ro, e.ResolvedPath)
		if err != nil {
			return err
		}
		f = &Folder{Path: e.ResolvedPath, Entry: lexical, Name: fm.Name, TreeSHA: tree.SHA}
		b.folders[e.ResolvedPath] = f
	}
	if kind == rawReal && f.Entry != e.Path && !b.isRealEntry(f) {
		f.Entry = e.Path
	}
	if !slices.Contains(f.Entries, e.Path) {
		f.Entries = append(f.Entries, e.Path)
		slices.Sort(f.Entries)
	}
	for _, p := range b.d.Profiles {
		for _, r := range p.ReadRoots {
			if pr, err := b.abs(r.Dir); err == nil && pr == root {
				f.Profile = p.ID
			}
		}
	}
	b.walks[root] = append(b.walks[root], observed{entry: e, level: level, kind: kind, folder: e.ResolvedPath})
	return nil
}

// isRealEntry reports whether the folder's Entry was set from a real entry.
func (b *builder) isRealEntry(f *Folder) bool {
	for _, obs := range b.walks {
		for _, o := range obs {
			if o.kind == rawReal && o.entry.Path == f.Entry {
				return true
			}
		}
	}
	return false
}

// realDir is the real path of the directory dir, every symlink resolved, as
// the scanner reports it for dir's entry in its parent. A dir the parent does
// not list is returned as it is.
func (b *builder) realDir(dir string) (string, error) {
	if r, ok := b.realDirs[dir]; ok {
		return r, nil
	}
	entries, err := b.list(filepath.Dir(dir))
	if err != nil {
		return "", err
	}
	r := dir
	i := slices.IndexFunc(entries, func(e ports.DirEntry) bool { return filepath.Clean(e.Path) == dir })
	if i >= 0 && entries[i].ResolvedPath != "" {
		r = filepath.Clean(entries[i].ResolvedPath)
	}
	b.realDirs[dir] = r
	return r, nil
}

// underRoot writes a path found below the real path of a known root, such as
// ~/dotfiles/a/claude/skills/x when ~/.claude is a symlink to
// ~/dotfiles/a/claude, below that root (~/.claude/skills/x), so containment
// and the targets shown use the paths the roots are entered by. Any other path
// is returned as it is.
func (b *builder) underRoot(p string) string {
	if b.known(p) {
		return p
	}
	best := ""
	for _, root := range b.roots {
		r := b.realDirs[root]
		if r == "" || r == root || !within(p, r) || len(r) <= len(b.realDirs[best]) {
			continue
		}
		best = root
	}
	if best == "" {
		return p
	}
	rel, err := filepath.Rel(b.realDirs[best], p)
	if err != nil {
		return p
	}
	return filepath.Join(best, rel)
}

// link resolves the symlink at path hop by hop, as the kernel does, never
// past the known roots.
func (b *builder) link(path string) (*Link, error) {
	if l, ok := b.links[path]; ok {
		return l, nil
	}
	l := &Link{Path: path, Status: LinkCycle}
	b.links[path] = l
	seen := map[string]bool{path: true}
	cur := path
	for range maxHops {
		text, err := b.d.Links.Readlink(cur)
		if err != nil {
			return nil, fmt.Errorf("read the link %s: %w", cur, err)
		}
		if cur == path {
			l.Text = text
		}
		next := filepath.FromSlash(text)
		if !filepath.IsAbs(next) {
			// The kernel resolves a relative text from the real directory
			// holding the link, not from the path it was reached by.
			dir, err := b.realDir(filepath.Dir(cur))
			if err != nil {
				return nil, err
			}
			next = filepath.Join(dir, next)
		}
		next = b.underRoot(filepath.Clean(next))
		l.Hops = append(l.Hops, next)
		l.Final = next
		if !b.known(next) {
			l.Status = LinkOutside
			return l, nil
		}
		if seen[next] {
			l.Status = LinkCycle
			return l, nil
		}
		seen[next] = true
		mode, err := b.d.Files.Mode(next)
		if errors.Is(err, iofs.ErrNotExist) {
			l.Status = LinkBroken
			return l, nil
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", next, err)
		}
		if mode&iofs.ModeSymlink == 0 {
			l.Status = LinkOK
			return l, nil
		}
		cur = next
	}
	return l, nil
}

// sight turns the walks into sightings for every detected harness and
// profile, and returns the detected harnesses.
func (b *builder) sight() ([]string, error) {
	var detected []string
	for _, h := range b.d.Harnesses {
		present, err := b.present(h)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		detected = append(detected, h.ID)
		for _, r := range h.ReadRoots {
			if err := b.sightRoot(h.ID, r, h.FollowsLinks); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range b.d.Profiles {
		for _, r := range p.ReadRoots {
			if err := b.sightRoot(p.ID, r, data.Yes); err != nil {
				return nil, err
			}
		}
	}
	for _, f := range b.folders {
		slices.SortFunc(f.Sightings, func(x, y Sighting) int {
			return cmp.Or(cmp.Compare(x.Harness, y.Harness), cmp.Compare(x.Entry, y.Entry))
		})
		f.Sightings = slices.Compact(f.Sightings)
	}
	return detected, nil
}

// present reports whether a directory of the harness other than the store
// exists. A harness whose only directory is the store cannot be detected.
func (b *builder) present(h data.Harness) (bool, error) {
	dirs := []string{h.LinkDir}
	for _, r := range h.ReadRoots {
		dirs = append(dirs, r.Dir)
	}
	for _, rel := range dirs {
		if rel == "" {
			continue
		}
		p, err := b.abs(rel)
		if err != nil {
			return false, err
		}
		if p == b.store {
			continue
		}
		ok, err := b.d.Files.Exists(p)
		if err != nil {
			return false, fmt.Errorf("look for %s: %w", p, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// sightRoot adds who's sightings of the folders observed in root. The kind
// is provisional: Direct stands for a real entry until skills() knows the
// canonical folder.
func (b *builder) sightRoot(who string, r data.ReadRoot, follows data.Tri) error {
	root, err := b.abs(r.Dir)
	if err != nil {
		return err
	}
	for _, o := range b.walks[root] {
		if r.Depth > 0 && o.level > int(r.Depth) {
			continue
		}
		kind := Direct
		switch {
		case r.Depth == data.DepthUnknown:
			kind = Indeterminate
		case o.kind == rawLink && follows == data.Yes:
			kind = ViaLink
		case o.kind == rawLink && follows == data.No:
			continue
		case o.kind == rawLink:
			kind = Indeterminate
		}
		f := b.folders[o.folder]
		f.Sightings = append(f.Sightings, Sighting{Harness: who, Kind: kind, Entry: o.entry.Path})
	}
	return nil
}

// skills builds the nodes: inventory skills with a lock entry or a clone,
// then every folder no such skill claims, never grouped by name.
func (b *builder) skills() ([]Skill, error) {
	var out []*Skill
	claimed := map[string]bool{}
	for _, inv := range b.d.Inventory {
		s := &Skill{ID: inv.ID, Origin: inv.Origin.Kind, State: inv.State}
		switch inv.Origin.Kind {
		case domain.OriginLock:
			s.LockKey = inv.Name
			s.Canonical = b.lockFolder(inv.Name)
		case domain.OriginGitClone:
			if len(inv.Locations) == 0 {
				continue
			}
			canonical, zone, err := b.cloneFolder(inv.Locations[0].Path)
			if err != nil {
				return nil, err
			}
			s.Canonical, s.Zone = canonical, zone
		default:
			continue
		}
		if s.Canonical != "" {
			claimed[s.Canonical] = true
			s.Folders = []string{s.Canonical}
		}
		out = append(out, s)
	}

	var rest []*Folder
	for _, f := range b.sortedFolders() {
		if claimed[f.Path] {
			continue
		}
		switch {
		case f.Profile != "":
			out = append(out, &Skill{
				ID: domain.DirID(b.homeRel(f.Entry)), Origin: domain.OriginNone, State: domain.StateNoOrigin,
				Folders: []string{f.Path}, Profile: f.Profile, Zone: ZoneProfile,
			})
		case within(f.Entry, b.store):
			out = append(out, &Skill{
				ID: domain.DirID(b.homeRel(f.Entry)), Origin: domain.OriginNone, State: domain.StateNoOrigin,
				Canonical: f.Path, Folders: []string{f.Path},
			})
		default:
			rest = append(rest, f)
		}
	}

	// A folder outside the store with the content of exactly one canonical
	// folder is that skill's own copy; the others group by identical
	// content into skills without a canonical folder.
	bySHA := map[string][]*Skill{}
	for _, s := range out {
		if s.Canonical != "" && s.Zone == ZoneNone {
			if f, ok := b.folders[s.Canonical]; ok {
				bySHA[f.TreeSHA] = append(bySHA[f.TreeSHA], s)
			}
		}
	}
	groups := map[string]*Skill{}
	for _, f := range rest {
		if owners := bySHA[f.TreeSHA]; len(owners) == 1 {
			owners[0].Folders = append(owners[0].Folders, f.Path)
			continue
		}
		g, ok := groups[f.TreeSHA]
		if !ok {
			g = &Skill{ID: domain.DirID(b.homeRel(f.Entry)), Origin: domain.OriginNone, State: domain.StateNoOrigin}
			groups[f.TreeSHA] = g
			out = append(out, g)
		}
		g.Folders = append(g.Folders, f.Path)
	}

	byCanonical := map[string][]string{}
	for _, s := range out {
		if s.Canonical != "" {
			byCanonical[s.Canonical] = append(byCanonical[s.Canonical], s.ID)
		}
	}
	result := make([]Skill, 0, len(out))
	for _, s := range out {
		b.finish(s, byCanonical)
		result = append(result, *s)
	}
	slices.SortFunc(result, func(x, y Skill) int { return cmp.Compare(x.ID, y.ID) })
	return result, nil
}

func (b *builder) sortedFolders() []*Folder {
	out := make([]*Folder, 0, len(b.folders))
	for _, f := range b.folders {
		out = append(out, f)
	}
	slices.SortFunc(out, func(x, y *Folder) int { return cmp.Compare(x.Entry, y.Entry) })
	return out
}

func (b *builder) homeRel(p string) string {
	if rel, ok := b.d.Paths.HomeRel(p); ok {
		return rel
	}
	return filepath.ToSlash(p)
}

// lockFolder is the physical folder of a lock key: the store entry of that
// name, through any link, or else the folder parked in OFF/.
func (b *builder) lockFolder(key string) string {
	for _, rel := range []string{key, filepath.Join(inventory.OffDirName, key)} {
		want := filepath.Join(b.store, rel)
		for _, o := range b.walks[b.store] {
			if o.entry.Path == want {
				return o.folder
			}
		}
	}
	return ""
}

// cloneFolder resolves a clone skill's folder, which the inventory found
// through git, and tells whether it lies in a foreign zone.
func (b *builder) cloneFolder(path string) (string, Zone, error) {
	entries, err := b.list(filepath.Dir(path))
	if err != nil {
		return "", ZoneNone, err
	}
	i := slices.IndexFunc(entries, func(e ports.DirEntry) bool { return e.Path == path })
	if i < 0 || entries[i].ResolvedPath == "" {
		return "", ZoneNone, nil
	}
	resolved := entries[i].ResolvedPath
	if _, ok := b.folders[resolved]; !ok {
		tree, err := b.d.HashDir(resolved)
		if err != nil {
			return "", ZoneNone, fmt.Errorf("hash %s: %w", resolved, err)
		}
		fm, err := inventory.ReadFrontmatter(b.ro, resolved)
		if err != nil {
			return "", ZoneNone, err
		}
		b.folders[resolved] = &Folder{Path: resolved, Entry: path, Name: fm.Name, TreeSHA: tree.SHA, Entries: []string{path}}
	}
	if inventory.Excluded(b.d.Paths, path) {
		return resolved, ZoneProvider, nil
	}
	for dir := filepath.Dir(path); within(dir, b.d.Paths.Home()) && dir != b.d.Paths.Home(); dir = filepath.Dir(dir) {
		zone, err := b.zone(dir, filepath.Base(dir))
		if err != nil {
			return "", ZoneNone, err
		}
		if zone == ZonePlugin || zone == ZoneShared {
			return resolved, zone, nil
		}
	}
	return resolved, ZoneNone, nil
}

// finish fills the name, aliases, shared claims and visibility of s.
func (b *builder) finish(s *Skill, byCanonical map[string][]string) {
	if len(s.Folders) > 0 {
		s.Name = b.folders[s.Folders[0]].Name
	}
	if s.Canonical != "" {
		for _, o := range b.walks[b.store] {
			if o.level == 1 && o.kind == rawLink && o.folder == s.Canonical {
				s.Aliases = append(s.Aliases, o.entry.Path)
			}
		}
		slices.Sort(s.Aliases)
		for _, id := range byCanonical[s.Canonical] {
			if id != s.ID {
				s.SharedWith = append(s.SharedWith, id)
			}
		}
	}
	for _, path := range s.Folders {
		for _, v := range b.folders[path].Sightings {
			if v.Kind == Direct && path != s.Canonical && s.Zone != ZoneProfile {
				v.Kind = OwnCopy
			}
			s.Visibility = append(s.Visibility, v)
		}
	}
	slices.SortFunc(s.Visibility, func(x, y Sighting) int {
		return cmp.Or(cmp.Compare(x.Harness, y.Harness), cmp.Compare(x.Entry, y.Entry))
	})
}

// errReadOnly is what readOnly answers to any write.
var errReadOnly = errors.New("graph: read-only access")

// readOnly lends a FileReader to the inventory helpers, which take a
// ports.FileOps: every write method fails without touching anything.
type readOnly struct{ FileReader }

func (readOnly) WriteFileAtomic(string, []byte, iofs.FileMode) error { return errReadOnly }
func (readOnly) Rename(string, string) error                         { return errReadOnly }
func (readOnly) CopyTree(string, string) error                       { return errReadOnly }
func (readOnly) RemoveTree(string) error                             { return errReadOnly }
func (readOnly) MkdirAll(string, iofs.FileMode) error                { return errReadOnly }
func (readOnly) Chmod(string, iofs.FileMode) error                   { return errReadOnly }
