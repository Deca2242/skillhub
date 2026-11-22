// Package remove plans the removal of a skill (docs/design-v2.md sections 6,
// 11 and 15): every link of a harness that resolves to its folder, and only
// those, then its folder in the store or in the parking zone, then its lock
// entry. It observes through the read-only graph, never by grouping names,
// and writes nothing: the plan it returns is run by engine.Apply, which takes
// the snapshot that undo restores byte for byte.
package remove

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// Planner starts a plan; *engine.Engine satisfies it.
type Planner interface {
	NewBuilder(meta engine.Meta) *engine.Builder
}

// LockReader reads the npx skills global lock.
type LockReader interface {
	Read() (domain.SkillsLock, error)
}

// StateReader reads skillhub's persisted state.
type StateReader interface {
	Load() (domain.PersistedState, error)
}

// Deps are what the planner reads. None of them can write.
type Deps struct {
	Paths   paths.Paths
	Planner Planner
	Lock    LockReader
	State   StateReader
	// Observe builds a fresh identity and visibility graph.
	Observe func() (graph.Graph, error)
	// Profiles are the read-only diagnostic profiles; a link in one of their
	// roots is reported and never removed.
	Profiles []data.DiagnosticProfile
	// Dirs and Links walk the foreign zones for links to the folder, which
	// the graph does not enter.
	Dirs  DirLister
	Links LinkReader
	// Files reads the lock's bytes, to tell whether removing its last entry
	// may delete it.
	Files FileReader
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
}

// DirLister lists the direct entries of a directory.
type DirLister interface {
	Scan(dir string) ([]ports.DirEntry, error)
}

// LinkReader reads the text of a symlink.
type LinkReader interface {
	Readlink(path string) (string, error)
}

// FileReader reads a file.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

var (
	// ErrInvalidKey reports a key that is not one folder name.
	ErrInvalidKey = errors.New("invalid skill key")
	// ErrNotFound reports a key with no skill folder in the store and no
	// parked skill.
	ErrNotFound = errors.New("skill not found")
	// ErrForeignZone reports a key in a zone skillhub never touches.
	ErrForeignZone = errors.New("the skill is in a zone skillhub does not manage")
	// ErrAlias reports a key whose store entry is a link to another folder.
	ErrAlias = errors.New("the key is an alias of another skill folder")
	// ErrAmbiguous reports a key that names two things: an active and a
	// parked skill, or a folder several skills claim.
	ErrAmbiguous = errors.New("the key names more than one skill")
	// ErrChanged reports a skill that changed while it was being observed.
	ErrChanged = errors.New("the skill changed while it was observed")
)

// Remover plans removals.
type Remover struct {
	d Deps
}

// New builds a Remover.
func New(d Deps) *Remover {
	if d.HashDir == nil {
		d.HashDir = treehash.Dir
	}
	return &Remover{d: d}
}

// Plan is a removal ready to approve: the engine plan, whose ID the approval
// binds to, and its typed preview.
type Plan struct {
	Plan    engine.Plan
	Preview Preview
}

// Preview is what a removal does and what it leaves, as data; the entry
// points turn it into text.
type Preview struct {
	Key string
	// Parked tells that the skill was disabled: its folder is in the parking
	// zone and its lock entry in skillhub's state, not in the lock.
	Parked bool
	// Folder is the real folder removed, with its tree SHA.
	Folder  string
	TreeSHA string
	// Links are the links removed: every link of a harness, or alias in the
	// store, that resolves to Folder.
	Links []Link
	// LockEntry is the lock entry removed; nil when the lock holds none.
	LockEntry *LockEntry
	// ParkedEntry is the lock entry a parked skill kept in skillhub's state,
	// dropped with it; undo puts it back there.
	ParkedEntry *domain.ParkedEntry
	// Modified tells whether the folder differs from what its lock entry
	// recorded.
	Modified Modification
	// Homonyms are real folders elsewhere with the same name or key: they are
	// reported and never removed (section 8 resolves them).
	Homonyms []Homonym
	// KeptLinks are links to Folder in a read-only diagnostic profile: they
	// are reported and never removed, and the removal leaves them broken.
	KeptLinks []KeptLink
	// PendingCandidates are the ids of the update candidates of the skill
	// that are still open; the caller marks them superseded once the removal
	// is applied.
	PendingCandidates []string
	// Targets are the harnesses that see the skill today and stop seeing it.
	Targets []string
}

// Link is a symlink the removal takes out: its path, exact content and the
// harnesses that list it.
type Link struct {
	Path      string
	Target    string
	Harnesses []string
}

// LockEntry is the lock entry the removal takes out, with its exact bytes.
// DeletesLock tells that it is the last entry and the lock file goes too.
type LockEntry struct {
	Name        string
	Raw         []byte
	DeletesLock bool
}

// ModifiedState tells whether a folder is what its origin recorded.
type ModifiedState string

const (
	ModifiedNo      ModifiedState = "no"
	ModifiedYes     ModifiedState = "yes"
	ModifiedUnknown ModifiedState = "unknown"
)

// Modification compares the folder with the skillFolderHash of its lock
// entry: Expected is the recorded hash, Actual the folder's.
type Modification struct {
	State    ModifiedState
	Expected string
	Actual   string
}

// Homonym is a real folder, outside the one removed, with the same name or
// key.
type Homonym struct {
	Path    string
	Name    string
	TreeSHA string
	// Identical tells that its content is the removed folder's.
	Identical bool
	Harnesses []string
	// Profile is the read-only diagnostic profile holding it, if any.
	Profile string
}

// KeptLink is a link to the removed folder that the removal leaves, broken:
// one in a read-only diagnostic profile (Profile) or in a foreign zone
// (Zone).
type KeptLink struct {
	Path    string
	Target  string
	Profile string
	Zone    graph.Zone
}

// Remove plans the removal of the skill whose folder is the store entry key,
// or of the parked skill key. It observes the graph, the lock and the state,
// and writes nothing; engine.Apply runs the plan it returns.
func (r *Remover) Remove(key string) (Plan, error) {
	if !validKey(key) {
		return Plan{}, fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	st, err := r.d.State.Load()
	if err != nil {
		return Plan{}, fmt.Errorf("read the state: %w", err)
	}
	g, err := r.d.Observe()
	if err != nil {
		return Plan{}, fmt.Errorf("observe the skills: %w", err)
	}
	o := &observation{r: r, g: g, st: st, key: key, profiles: r.profileRoots()}
	active, err := o.activeFolder()
	if err != nil {
		return Plan{}, err
	}
	parked, isParked := st.Parked[key]
	switch {
	case active != nil && isParked:
		return Plan{}, fmt.Errorf("%w: %s is both in the store and parked", ErrAmbiguous, key)
	case active != nil:
		return o.planActive(active)
	case isParked:
		return o.planParked(parked)
	}
	return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, key)
}

// validKey reports whether key is one folder name.
func validKey(key string) bool {
	return key != "." && key != ".." && utf8.ValidString(key) && filepath.Base(key) == key
}

// observation is what one Remove call saw.
type observation struct {
	r        *Remover
	g        graph.Graph
	st       domain.PersistedState
	key      string
	profiles map[string]string // root -> profile id
}

// profileRoots are the read roots of the diagnostic profiles.
func (r *Remover) profileRoots() map[string]string {
	out := map[string]string{}
	for _, p := range r.d.Profiles {
		for _, root := range p.ReadRoots {
			if abs, err := r.d.Paths.Join(filepath.FromSlash(root.Dir)); err == nil {
				out[abs] = p.ID
			}
		}
	}
	return out
}

// profileOf is the profile whose root holds path, or "".
func (o *observation) profileOf(path string) string {
	for root, id := range o.profiles {
		if within(path, root) {
			return id
		}
	}
	return ""
}

func (o *observation) isProfile(id string) bool {
	for _, p := range o.r.d.Profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// active is a skill folder in the store and the graph node that owns it.
type active struct {
	path   string
	folder graph.Folder
	skill  graph.Skill
}

// activeFolder finds, in the graph only, the real folder listed as the store
// entry key and the one skill whose canonical folder it is. It returns nil
// when the store has no such entry.
func (o *observation) activeFolder() (*active, error) {
	entry := filepath.Join(o.r.d.Paths.StoreDir(), o.key)
	for _, b := range o.g.Boundary {
		if b.Path == entry {
			return nil, fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, b.Zone)
		}
	}
	i := slices.IndexFunc(o.g.Folders, func(f graph.Folder) bool { return slices.Contains(f.Entries, entry) })
	if i < 0 {
		return nil, nil
	}
	f := o.g.Folders[i]
	if slices.ContainsFunc(o.g.Links, func(l graph.Link) bool { return l.Path == entry }) {
		return nil, fmt.Errorf("%w: %s resolves to %s", ErrAlias, entry, f.Path)
	}
	// The entry is in the store, never in a profile root; f.Profile is also
	// set when a profile only links to the folder, so it is not checked.
	var owners []graph.Skill
	for _, s := range o.g.Skills {
		if s.Canonical == f.Path {
			owners = append(owners, s)
		}
	}
	switch {
	case len(owners) == 0:
		return nil, fmt.Errorf("%w: no skill owns %s", ErrNotFound, entry)
	case len(owners) > 1 || len(owners[0].SharedWith) > 0:
		return nil, fmt.Errorf("%w: several skills claim %s", ErrAmbiguous, entry)
	case owners[0].Zone != graph.ZoneNone:
		return nil, fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, owners[0].Zone)
	}
	return &active{path: entry, folder: f, skill: owners[0]}, nil
}

// planActive removes the links to the folder, the folder and its lock entry.
func (o *observation) planActive(a *active) (Plan, error) {
	pv := Preview{Key: o.key, Folder: a.path, TreeSHA: a.folder.TreeSHA}
	pv.Links, pv.KeptLinks = o.links(func(l graph.Link) bool { return l.Status == graph.LinkOK && l.Folder == a.folder.Path }, a.folder)
	if err := o.keepZoneLinks(&pv, a.folder.Path); err != nil {
		return Plan{}, err
	}
	pv.Targets = o.harnesses(a.folder.Sightings, "")
	pv.Homonyms = o.homonyms(a.folder.Path, a.folder.Name, a.folder.TreeSHA)
	pv.PendingCandidates = o.openCandidates(a.skill.ID)
	pv.Modified = Modification{State: ModifiedUnknown, Actual: a.folder.TreeSHA}
	if a.skill.LockKey != "" {
		entry, last, err := o.lockEntry(a.skill.LockKey)
		if err != nil {
			return Plan{}, err
		}
		pv.LockEntry = &LockEntry{Name: entry.Name, Raw: slices.Clone(entry.Raw), DeletesLock: last}
		pv.Modified = modification(entry.SkillFolderHash, a.folder.TreeSHA)
	}
	return o.build(pv)
}

// planParked removes a parked folder and the links that still reach it;
// its lock entry is in Parked, which the effect drops.
func (o *observation) planParked(p domain.ParkedSkill) (Plan, error) {
	if p.Folder == "" && len(p.Links) == 0 && p.Entry != nil {
		return o.planParkedEntry(p)
	}
	folder := filepath.Join(o.r.d.Paths.ParkedDir(), o.key)
	if p.Folder != folder {
		return Plan{}, fmt.Errorf("%w: parked %s is at %s, not %s", ErrChanged, o.key, p.Folder, folder)
	}
	tree, err := o.r.d.HashDir(folder)
	if err != nil {
		return Plan{}, fmt.Errorf("hash %s: %w", folder, err)
	}
	pv := Preview{Key: o.key, Parked: true, Folder: folder, TreeSHA: tree.SHA}
	pv.Links, pv.KeptLinks = o.links(func(l graph.Link) bool { return l.Final == folder }, graph.Folder{})
	resolved, err := o.resolved(folder)
	if err != nil {
		return Plan{}, err
	}
	if err := o.keepZoneLinks(&pv, resolved); err != nil {
		return Plan{}, err
	}
	pv.Homonyms = o.homonyms(folder, o.key, tree.SHA)
	pv.Modified = Modification{State: ModifiedUnknown, Actual: tree.SHA}
	if p.Entry != nil {
		entry := *p.Entry
		entry.Raw = slices.Clone(entry.Raw)
		pv.ParkedEntry = &entry
		pv.PendingCandidates = o.openCandidates(domain.LockID(entry.Name))
		var fields struct {
			SkillFolderHash string `json:"skillFolderHash"`
		}
		if err := json.Unmarshal(entry.Raw, &fields); err == nil {
			pv.Modified = modification(fields.SkillFolderHash, tree.SHA)
		}
	}
	return o.build(pv)
}

// planParkedEntry drops the record park_entry leaves (repair): the lock
// entry of a folder that was already gone, with no folder and no links
// (L47). Nothing on disk is removed; the effect drops the record, and undo
// puts it back.
func (o *observation) planParkedEntry(p domain.ParkedSkill) (Plan, error) {
	if p.Key != o.key || p.Entry.Name != o.key {
		return Plan{}, fmt.Errorf("%w: the parked entry of %s names %s", ErrChanged, o.key, p.Entry.Name)
	}
	entry := *p.Entry
	entry.Raw = slices.Clone(entry.Raw)
	pv := Preview{Key: o.key, Parked: true, ParkedEntry: &entry,
		Modified:          Modification{State: ModifiedUnknown},
		PendingCandidates: o.openCandidates(domain.LockID(entry.Name))}
	return o.build(pv)
}

// build turns a preview into the engine plan: every link, then the folder,
// then the lock entry; its effect drops the key from Managed and Parked. A
// parked entry without a folder has no step at all.
func (o *observation) build(pv Preview) (Plan, error) {
	b := o.r.d.Planner.NewBuilder(engine.Meta{Action: "remove", Targets: pv.Targets})
	for _, l := range pv.Links {
		if err := b.Unlink(l.Path, l.Target); err != nil {
			return Plan{}, err
		}
	}
	if pv.Folder != "" {
		if err := b.RemoveTree(pv.Folder); err != nil {
			return Plan{}, err
		}
	}
	if e := pv.LockEntry; e != nil {
		remove := b.LockRemove
		if e.DeletesLock {
			remove = b.LockRemoveLast
		}
		if err := remove(e.Name, e.Raw); err != nil {
			return Plan{}, err
		}
	}
	if err := b.SetEffect(engine.Effect{DropManaged: []string{o.key}, DropParked: []string{o.key}}); err != nil {
		return Plan{}, err
	}
	plan, err := b.Build()
	if err != nil {
		return Plan{}, err
	}
	return Plan{Plan: plan, Preview: pv}, nil
}

// links splits the graph's links that match into the ones the removal takes
// out and the ones a read-only profile keeps, both by path.
func (o *observation) links(match func(graph.Link) bool, f graph.Folder) ([]Link, []KeptLink) {
	var out []Link
	var kept []KeptLink
	for _, l := range o.g.Links {
		if !match(l) {
			continue
		}
		if id := o.profileOf(l.Path); id != "" {
			kept = append(kept, KeptLink{Path: l.Path, Target: l.Text, Profile: id})
			continue
		}
		out = append(out, Link{Path: l.Path, Target: l.Text, Harnesses: o.harnesses(f.Sightings, l.Path)})
	}
	slices.SortFunc(out, func(x, y Link) int { return strings.Compare(x.Path, y.Path) })
	slices.SortFunc(kept, func(x, y KeptLink) int { return strings.Compare(x.Path, y.Path) })
	return out, kept
}

// maxZoneDepth bounds the walk below a foreign zone; only real directories
// are entered, so the walk cannot loop.
const maxZoneDepth = 16

// keepZoneLinks adds to the kept links every symlink at or below a foreign
// zone (the graph's boundaries, loose files left out) that resolves to the
// folder, whose real path is resolved. The
// graph never enters those zones and skillhub never touches them, so the
// removal leaves these links broken and the preview says so.
func (o *observation) keepZoneLinks(pv *Preview, resolved string) error {
	for _, b := range o.g.Boundary {
		entries, err := o.scan(filepath.Dir(b.Path))
		if err != nil {
			return err
		}
		i := slices.IndexFunc(entries, func(e ports.DirEntry) bool { return e.Path == b.Path })
		if i < 0 {
			continue
		}
		if err := o.zoneWalk(pv, entries[i], b.Zone, resolved, 0); err != nil {
			return err
		}
	}
	slices.SortFunc(pv.KeptLinks, func(x, y KeptLink) int { return strings.Compare(x.Path, y.Path) })
	return nil
}

func (o *observation) zoneWalk(pv *Preview, e ports.DirEntry, zone graph.Zone, resolved string, depth int) error {
	switch e.Kind {
	case ports.EntrySymlink:
		if e.ResolvedPath != resolved {
			return nil
		}
		text, err := o.r.d.Links.Readlink(e.Path)
		if err != nil {
			return fmt.Errorf("read the link %s: %w", e.Path, err)
		}
		pv.KeptLinks = append(pv.KeptLinks, KeptLink{Path: e.Path, Target: text, Zone: zone})
	case ports.EntryDir:
		if e.Name == ".git" || depth >= maxZoneDepth {
			return nil
		}
		children, err := o.scan(e.Path)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := o.zoneWalk(pv, c, zone, resolved, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// scan lists dir; a missing one is empty.
func (o *observation) scan(dir string) ([]ports.DirEntry, error) {
	entries, err := o.r.d.Dirs.Scan(dir)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	return entries, nil
}

// resolved is path with every symlink resolved, as its parent lists it.
func (o *observation) resolved(path string) (string, error) {
	entries, err := o.scan(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Path == path && e.ResolvedPath != "" {
			return e.ResolvedPath, nil
		}
	}
	return path, nil
}

// harnesses are the harnesses, profiles left out, of the sightings; of the
// ones listing entry when it is not empty.
func (o *observation) harnesses(sightings []graph.Sighting, entry string) []string {
	var out []string
	for _, s := range sightings {
		if (entry == "" || s.Entry == entry) && !o.isProfile(s.Harness) && !slices.Contains(out, s.Harness) {
			out = append(out, s.Harness)
		}
	}
	slices.Sort(out)
	return out
}

// homonyms are the other real folders whose frontmatter name is name or
// whose entry is named key: reported, never removed.
func (o *observation) homonyms(path, name, treeSHA string) []Homonym {
	var out []Homonym
	for _, f := range o.g.Folders {
		if f.Path == path || (f.Name != name && filepath.Base(f.Entry) != o.key) {
			continue
		}
		out = append(out, Homonym{
			Path: f.Entry, Name: f.Name, TreeSHA: f.TreeSHA, Identical: f.TreeSHA == treeSHA,
			Harnesses: o.harnesses(f.Sightings, ""), Profile: o.profileOf(f.Entry),
		})
	}
	slices.SortFunc(out, func(x, y Homonym) int { return strings.Compare(x.Path, y.Path) })
	return out
}

// openCandidates are the ids of the update candidates of skill id that are
// still open.
func (o *observation) openCandidates(id string) []string {
	var out []string
	for _, c := range o.st.Candidates {
		switch c.State {
		case domain.CandidateApplied, domain.CandidateSkipped, domain.CandidateSuperseded:
			continue
		}
		if c.Target == id || slices.Contains(c.Skills, id) {
			out = append(out, c.ID)
		}
	}
	return out
}

// lockEntry reads the entry name from the lock and tells whether removing
// it may delete the lock: only when the lock left would be, byte for byte,
// the empty lock that an add on a missing lock and then a remove leave
// (emptyLock), and the entry carries no ref, since the lock could only come
// back through an add, which never writes a ref (L32). Any other lock keeps
// its other keys and an empty "skills", as npx skills remove leaves it.
func (o *observation) lockEntry(name string) (domain.LockEntry, bool, error) {
	data, err := o.r.d.Files.ReadFile(o.r.d.Paths.SkillsLockFile())
	if err != nil {
		return domain.LockEntry{}, false, fmt.Errorf("read the skills lock: %w", err)
	}
	lock, err := o.r.d.Lock.Read()
	if err != nil {
		return domain.LockEntry{}, false, fmt.Errorf("read the skills lock: %w", err)
	}
	i := slices.IndexFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == name })
	if i < 0 {
		return domain.LockEntry{}, false, fmt.Errorf("%w: lock entry %s is gone", ErrChanged, name)
	}
	entry := lock.Entries[i]
	left, err := lockWithout(data, name, entry.Raw)
	if err != nil {
		return domain.LockEntry{}, false, err
	}
	return entry, len(lock.Entries) == 1 && entry.Ref == "" && string(left) == emptyLock, nil
}

// emptyLock is the lock npx skills 1.9.0 creates when it has none, and so
// the only lock the lock store's DropEmpty deletes. The test of the last
// entry of a lock that an add created pins it to the store's behaviour.
const emptyLock = "{\n  \"version\": 3,\n  \"skills\": {},\n  \"dismissed\": {}\n}"

// lockWithout is the lock data with entry name taken out, as the lock store
// rewrites it. The entry must be raw, or the lock changed between the reads.
func lockWithout(data []byte, name string, raw []byte) ([]byte, error) {
	doc, err := orderedjson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse the skills lock: %w", err)
	}
	skills, ok := doc.Root().Get("skills")
	var got *orderedjson.Value
	if ok && skills.Kind() == orderedjson.KindObject {
		got, _ = skills.Get(name)
	}
	if got == nil || !bytes.Equal(got.Bytes(), raw) {
		return nil, fmt.Errorf("%w: lock entry %s changed between two reads", ErrChanged, name)
	}
	skills.Delete(name)
	return doc.Bytes(), nil
}

// modification compares a folder's tree SHA with the hash its lock entry
// recorded.
func modification(expected, actual string) Modification {
	m := Modification{State: ModifiedUnknown, Expected: expected, Actual: actual}
	switch {
	case expected == "" || actual == "":
	case expected == actual:
		m.State = ModifiedNo
	default:
		m.State = ModifiedYes
	}
	return m
}
