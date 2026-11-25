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
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/inventory"
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
	// Harnesses are the configured harnesses whose global skill directories
	// may hold independent non-Git skills eligible for exact removal.
	Harnesses []data.Harness
	// Profiles are the read-only diagnostic profiles; a link in one of their
	// roots is reported and never removed.
	Profiles []data.DiagnosticProfile
	// Dirs and Links walk the foreign zones for links to the folder, which
	// the graph does not enter.
	Dirs  DirLister
	Links LinkReader
	// Files reads the lock's bytes and inspects paths without following
	// symlinks.
	Files FileReader
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)
	// ProjectDir and Project inspect direct skill folders in the current
	// startup project without following symlinks.
	ProjectDir string
	Project    ProjectInspector
}

// DirLister lists the direct entries of a directory.
type DirLister interface {
	Scan(dir string) ([]ports.DirEntry, error)
}

// LinkReader reads the text of a symlink.
type LinkReader interface {
	Readlink(path string) (string, error)
}

// FileReader reads files and inspects paths without following final symlinks.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
	Mode(path string) (iofs.FileMode, error)
	Exists(path string) (bool, error)
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
	// Project marks removal of an exact skill directory in the current startup
	// project (.agents/skills or .claude/skills).
	Project     bool
	ProjectDir  string
	ProjectRoot string
	SkillName   string
	// Parked tells that the skill was disabled with skillhub: its folder is in
	// the parking zone and its lock entry in skillhub's state, not in the lock.
	Parked bool
	// Off tells that the skill was disabled by hand in ~/.agents/skills/OFF/.
	Off bool
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

type targetKind int

const (
	targetLegacyKey targetKind = iota
	targetDir
	targetLock
	targetParked
	targetGit
	targetProject
)

type parsedTarget struct {
	kind  targetKind
	value string
	root  string
}

func parseTarget(raw string) (parsedTarget, error) {
	switch {
	case strings.HasPrefix(raw, "dir:"):
		rel := strings.TrimPrefix(raw, "dir:")
		if !validHomeRel(rel) {
			return parsedTarget{}, fmt.Errorf("%w: %q", ErrInvalidKey, raw)
		}
		return parsedTarget{kind: targetDir, value: rel}, nil
	case strings.HasPrefix(raw, "lock:"):
		key := strings.TrimPrefix(raw, "lock:")
		if !validKey(key) {
			return parsedTarget{}, fmt.Errorf("%w: %q", ErrInvalidKey, raw)
		}
		return parsedTarget{kind: targetLock, value: key}, nil
	case strings.HasPrefix(raw, "parked:"):
		key := strings.TrimPrefix(raw, "parked:")
		if !validKey(key) {
			return parsedTarget{}, fmt.Errorf("%w: %q", ErrInvalidKey, raw)
		}
		return parsedTarget{kind: targetParked, value: key}, nil
	case strings.HasPrefix(raw, "git:"):
		rel := strings.TrimPrefix(raw, "git:")
		if !validHomeRel(rel) {
			return parsedTarget{}, fmt.Errorf("%w: %q", ErrInvalidKey, raw)
		}
		return parsedTarget{kind: targetGit, value: rel}, nil
	case strings.HasPrefix(raw, "project:"):
		rest := strings.TrimPrefix(raw, "project:")
		root, name, ok := validProjectTarget(rest)
		if !ok {
			return parsedTarget{}, fmt.Errorf("%w: %q", ErrInvalidKey, raw)
		}
		return parsedTarget{kind: targetProject, value: name, root: root}, nil
	default:
		if !validKey(raw) {
			return parsedTarget{}, fmt.Errorf("%w: %q", ErrInvalidKey, raw)
		}
		return parsedTarget{kind: targetLegacyKey, value: raw}, nil
	}
}

// validHomeRel reports whether rel is a clean, slash-separated path relative
// to the home directory.
func validHomeRel(rel string) bool {
	if rel == "" || !utf8.ValidString(rel) || strings.Contains(rel, "\\") {
		return false
	}
	if strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return false
	}
	if path.Clean(rel) != rel || rel == "." || rel == ".." {
		return false
	}
	return filepath.IsLocal(filepath.FromSlash(rel))
}

// Remove plans the removal of a skill identified by a legacy store/parked key
// or by an exact observed ID (dir:<home-rel>, lock:<name>, parked:<key>).
// It observes the fresh graph, lock and state, enforces all ownership,
// ambiguity, symlink, provider, profile and Git boundaries, and writes nothing;
// engine.Apply runs the plan it returns.
func (r *Remover) Remove(target string) (Plan, error) {
	parsed, err := parseTarget(target)
	if err != nil {
		return Plan{}, err
	}
	st, err := r.d.State.Load()
	if err != nil {
		return Plan{}, fmt.Errorf("read the state: %w", err)
	}
	g, err := r.d.Observe()
	if err != nil {
		return Plan{}, fmt.Errorf("observe the skills: %w", err)
	}
	profiles := r.profileRoots()
	o := &observation{
		r:         r,
		g:         g,
		st:        st,
		profiles:  profiles,
		harnesses: r.harnessRoots(profiles),
	}
	switch parsed.kind {
	case targetLegacyKey:
		return o.removeLegacyWithProject(parsed.value)
	case targetParked:
		return o.removeParked(parsed.value)
	case targetLock:
		return o.removeLock(parsed.value)
	case targetDir:
		return o.removeDir(parsed.value)
	case targetGit:
		return o.removeGit(parsed.value)
	case targetProject:
		return o.removeProject(parsed.root, parsed.value)
	default:
		return Plan{}, fmt.Errorf("%w: %q", ErrInvalidKey, target)
	}
}

// validKey reports whether key is one folder name.
func validKey(key string) bool {
	return key != "" && key != "." && key != ".." && utf8.ValidString(key) && filepath.Base(key) == key
}

// observation is what one Remove call saw.
type observation struct {
	r         *Remover
	g         graph.Graph
	st        domain.PersistedState
	profiles  map[string]string // root -> profile id
	harnesses []string          // configured non-store, non-profile, non-provider global harness roots
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

// harnessRoots are the configured global harness skill directories outside the
// store, excluding provider folders and read-only diagnostic profile roots.
func (r *Remover) harnessRoots(profiles map[string]string) []string {
	store := r.d.Paths.StoreDir()
	seen := map[string]bool{store: true}
	var out []string
	add := func(rel string) {
		if rel == "" {
			return
		}
		abs, err := r.d.Paths.Join(filepath.FromSlash(rel))
		if err != nil || seen[abs] || inventory.Excluded(r.d.Paths, abs) {
			return
		}
		for root := range profiles {
			if within(abs, root) {
				return
			}
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, h := range r.d.Harnesses {
		add(h.LinkDir)
		for _, rr := range h.ReadRoots {
			add(rr.Dir)
		}
	}
	slices.Sort(out)
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

func (o *observation) offDir() string {
	return filepath.Join(o.r.d.Paths.StoreDir(), inventory.OffDirName)
}

// checkForeignBoundary rejects paths inside diagnostic profiles, provider
// excluded folders, or any graph foreign-zone boundary.
func (o *observation) checkForeignBoundary(entry string) error {
	if id := o.profileOf(entry); id != "" {
		return fmt.Errorf("%w: %s is in diagnostic profile %s", ErrForeignZone, entry, id)
	}
	if inventory.Excluded(o.r.d.Paths, entry) {
		return fmt.Errorf("%w: %s is in a provider directory", ErrForeignZone, entry)
	}
	for _, b := range o.g.Boundary {
		if b.Path == entry || (b.Zone != graph.ZoneFile && within(entry, b.Path)) {
			return fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, b.Zone)
		}
	}
	return nil
}

// enclosingGlobalRoot returns the allowed global skills root (store or a
// configured harness directory) that strictly encloses entry, or "".
func (o *observation) enclosingGlobalRoot(entry string) string {
	store := o.r.d.Paths.StoreDir()
	off := o.offDir()
	if entry == store || entry == off {
		return ""
	}
	if within(entry, store) {
		return store
	}
	for _, root := range o.harnesses {
		if entry == root {
			return ""
		}
		if within(entry, root) {
			return root
		}
	}
	return ""
}

// checkGitBoundary rejects a folder that is a Git clone, a member of a Git
// clone (even when its Git origin is unrecognized), a linked worktree, or
// contains a nested .git entry.
func (o *observation) checkGitBoundary(entry, root string) error {
	if root == "" || !within(entry, root) {
		return fmt.Errorf("%w: %s is outside %s", ErrForeignZone, entry, root)
	}
	for _, s := range o.g.Skills {
		if s.Origin != domain.OriginGitClone {
			continue
		}
		for _, f := range o.g.Folders {
			if (f.Path == s.Canonical || slices.Contains(s.Folders, f.Path)) && (f.Path == entry || slices.Contains(f.Entries, entry)) {
				return fmt.Errorf("%w: %s is a git clone", engine.ErrUnsupportedTree, entry)
			}
		}
	}
	for dir := entry; within(dir, root); dir = filepath.Dir(dir) {
		hasGit, err := o.r.d.Files.Exists(filepath.Join(dir, ".git"))
		if err != nil {
			return fmt.Errorf("look for .git in %s: %w", dir, err)
		}
		if hasGit {
			return fmt.Errorf("%w: %s is inside a git repository", engine.ErrUnsupportedTree, entry)
		}
		if dir == root {
			break
		}
	}
	return o.checkNestedGit(entry, 0)
}

func (o *observation) checkNestedGit(dir string, depth int) error {
	if depth >= maxZoneDepth {
		return fmt.Errorf("%w: %s exceeds maximum depth %d while checking for git metadata", engine.ErrUnsupportedTree, dir, maxZoneDepth)
	}
	entries, err := o.r.d.Dirs.Scan(dir)
	if err != nil {
		return fmt.Errorf("scan %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.Name == ".git" {
			return fmt.Errorf("%w: %s contains a git repository", engine.ErrUnsupportedTree, dir)
		}
		if e.Kind == ports.EntryDir {
			if err := o.checkNestedGit(e.Path, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *observation) removeLegacy(key string) (Plan, error) {
	active, err := o.activeFolder(key)
	if err != nil {
		return Plan{}, err
	}
	parked, isParked := o.st.Parked[key]
	switch {
	case active != nil && isParked:
		return Plan{}, fmt.Errorf("%w: %s is both in the store and parked", ErrAmbiguous, key)
	case active != nil:
		return o.planActive(active)
	case isParked:
		return o.planParked(key, parked)
	}
	return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, key)
}

func (o *observation) removeParked(key string) (Plan, error) {
	parked, ok := o.st.Parked[key]
	if !ok {
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return o.planParked(key, parked)
}

func (o *observation) removeLock(name string) (Plan, error) {
	id := domain.LockID(name)
	i := slices.IndexFunc(o.g.Skills, func(s graph.Skill) bool { return s.ID == id })
	if i < 0 || o.g.Skills[i].Canonical == "" {
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	skill := o.g.Skills[i]
	if skill.Zone != graph.ZoneNone {
		return Plan{}, fmt.Errorf("%w: %s is %s", ErrForeignZone, id, skill.Zone)
	}
	if len(skill.SharedWith) > 0 {
		return Plan{}, fmt.Errorf("%w: several skills claim %s", ErrAmbiguous, id)
	}
	fi := slices.IndexFunc(o.g.Folders, func(f graph.Folder) bool { return f.Path == skill.Canonical })
	if fi < 0 {
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	f := o.g.Folders[fi]
	entry := f.Entry
	if err := o.checkForeignBoundary(entry); err != nil {
		return Plan{}, err
	}
	if err := o.checkForeignBoundary(f.Path); err != nil {
		return Plan{}, err
	}
	root := o.enclosingGlobalRoot(entry)
	if root == "" {
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	physRoot := o.enclosingGlobalRoot(f.Path)
	if physRoot == "" {
		return Plan{}, fmt.Errorf("%w: %s resolves outside global skill directories", ErrForeignZone, entry)
	}
	if err := o.checkGitBoundary(entry, root); err != nil {
		return Plan{}, err
	}
	if f.Path != entry {
		if err := o.checkGitBoundary(f.Path, physRoot); err != nil {
			return Plan{}, err
		}
	}
	switch filepath.Dir(entry) {
	case o.r.d.Paths.StoreDir():
		return o.planActive(&active{key: filepath.Base(entry), path: entry, folder: f, skill: skill})
	case o.offDir():
		return o.planOff(entry, f, &skill)
	default:
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
}

func (o *observation) removeGit(rel string) (Plan, error) {
	entry, err := o.r.d.Paths.Join(filepath.FromSlash(rel))
	if err != nil {
		return Plan{}, fmt.Errorf("%w: git:%s", ErrInvalidKey, rel)
	}
	if err := o.checkForeignBoundary(entry); err != nil {
		return Plan{}, err
	}
	root := o.enclosingGlobalRoot(entry)
	if root == "" {
		return Plan{}, fmt.Errorf("%w: git:%s", ErrNotFound, rel)
	}
	exists, err := o.r.d.Files.Exists(entry)
	if err != nil {
		return Plan{}, fmt.Errorf("look at %s: %w", entry, err)
	}
	if !exists {
		return Plan{}, fmt.Errorf("%w: git:%s", ErrNotFound, rel)
	}
	return Plan{}, fmt.Errorf("%w: %s is a git clone", engine.ErrUnsupportedTree, entry)
}

func (o *observation) removeDir(rel string) (Plan, error) {
	entry, err := o.r.d.Paths.Join(filepath.FromSlash(rel))
	if err != nil {
		return Plan{}, fmt.Errorf("%w: dir:%s", ErrInvalidKey, rel)
	}
	if err := o.checkForeignBoundary(entry); err != nil {
		return Plan{}, err
	}
	root := o.enclosingGlobalRoot(entry)
	if root == "" {
		return Plan{}, fmt.Errorf("%w: dir:%s", ErrNotFound, rel)
	}
	if slices.ContainsFunc(o.g.Links, func(l graph.Link) bool { return l.Path == entry }) {
		return Plan{}, fmt.Errorf("%w: %s is a symlink", ErrAlias, entry)
	}
	mode, err := o.r.d.Files.Mode(entry)
	if errors.Is(err, iofs.ErrNotExist) {
		return Plan{}, fmt.Errorf("%w: dir:%s", ErrNotFound, rel)
	}
	if err != nil {
		return Plan{}, fmt.Errorf("look at %s: %w", entry, err)
	}
	if mode&iofs.ModeSymlink != 0 {
		return Plan{}, fmt.Errorf("%w: %s is a symlink", ErrAlias, entry)
	}
	if !mode.IsDir() {
		return Plan{}, fmt.Errorf("%w: dir:%s", ErrNotFound, rel)
	}
	phys, err := o.resolved(entry)
	if err != nil {
		return Plan{}, err
	}
	if err := o.checkForeignBoundary(phys); err != nil {
		return Plan{}, err
	}
	physRoot := o.enclosingGlobalRoot(phys)
	if physRoot == "" {
		return Plan{}, fmt.Errorf("%w: %s resolves outside global skill directories", ErrForeignZone, entry)
	}
	if err := o.checkGitBoundary(entry, root); err != nil {
		return Plan{}, err
	}
	if phys != entry {
		if err := o.checkGitBoundary(phys, physRoot); err != nil {
			return Plan{}, err
		}
	}

	parent := filepath.Dir(entry)
	physParent := filepath.Dir(phys)
	store := o.r.d.Paths.StoreDir()
	off := o.offDir()
	isStore := parent == store
	isOff := parent == off
	isHarness := slices.Contains(o.harnesses, parent)
	if !isStore && !isOff && !isHarness {
		return Plan{}, fmt.Errorf("%w: dir:%s", ErrNotFound, rel)
	}
	switch {
	case isStore && physParent != store:
		return Plan{}, fmt.Errorf("%w: %s resolves outside the store", ErrForeignZone, entry)
	case isOff && physParent != off:
		return Plan{}, fmt.Errorf("%w: %s resolves outside OFF", ErrForeignZone, entry)
	case isHarness && !slices.Contains(o.harnesses, physParent):
		return Plan{}, fmt.Errorf("%w: %s resolves outside configured harness directories", ErrForeignZone, entry)
	}

	fi := slices.IndexFunc(o.g.Folders, func(f graph.Folder) bool { return slices.Contains(f.Entries, entry) })
	if fi < 0 {
		return Plan{}, fmt.Errorf("%w: dir:%s", ErrNotFound, rel)
	}
	f := o.g.Folders[fi]
	if err := o.checkForeignBoundary(f.Path); err != nil {
		return Plan{}, err
	}
	fRoot := o.enclosingGlobalRoot(f.Path)
	if fRoot == "" {
		return Plan{}, fmt.Errorf("%w: %s resolves outside global skill directories", ErrForeignZone, entry)
	}
	if f.Path != entry && f.Path != phys {
		if err := o.checkGitBoundary(f.Path, fRoot); err != nil {
			return Plan{}, err
		}
	}

	for _, s := range o.g.Skills {
		claims := s.Canonical == f.Path || slices.Contains(s.Folders, f.Path)
		if claims && s.Zone != graph.ZoneNone && s.Zone != graph.ZoneProfile {
			return Plan{}, fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, s.Zone)
		}
	}

	var owners []graph.Skill
	for _, s := range o.g.Skills {
		if s.Canonical == f.Path {
			owners = append(owners, s)
		}
	}

	switch {
	case isStore:
		switch {
		case len(owners) == 0:
			return Plan{}, fmt.Errorf("%w: no skill owns %s", ErrNotFound, entry)
		case len(owners) > 1 || len(owners[0].SharedWith) > 0:
			return Plan{}, fmt.Errorf("%w: several skills claim %s", ErrAmbiguous, entry)
		case owners[0].Zone != graph.ZoneNone:
			return Plan{}, fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, owners[0].Zone)
		}
		return o.planActive(&active{key: filepath.Base(entry), path: entry, folder: f, skill: owners[0]})
	case isOff:
		switch {
		case len(owners) > 1 || (len(owners) == 1 && len(owners[0].SharedWith) > 0):
			return Plan{}, fmt.Errorf("%w: several skills claim %s", ErrAmbiguous, entry)
		case len(owners) == 1 && owners[0].Zone != graph.ZoneNone:
			return Plan{}, fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, owners[0].Zone)
		}
		var owner *graph.Skill
		if len(owners) == 1 {
			owner = &owners[0]
		}
		return o.planOff(entry, f, owner)
	default:
		if len(owners) > 0 {
			for _, s := range owners {
				if s.Origin == domain.OriginGitClone {
					return Plan{}, fmt.Errorf("%w: %s is a git clone", engine.ErrUnsupportedTree, entry)
				}
				if s.LockKey != "" {
					return Plan{}, fmt.Errorf("%w: %s is claimed by lock entry %s", ErrAmbiguous, entry, s.LockKey)
				}
			}
		}
		return o.planHarness(entry, f)
	}
}

// active is a skill folder in the store and the graph node that owns it.
type active struct {
	key    string
	path   string
	folder graph.Folder
	skill  graph.Skill
}

// activeFolder finds, in the graph only, the real folder listed as the store
// entry key and the one skill whose canonical folder it is. It returns nil
// when the store has no such entry.
func (o *observation) activeFolder(key string) (*active, error) {
	store := o.r.d.Paths.StoreDir()
	entry := filepath.Join(store, key)
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
	if err := o.checkForeignBoundary(entry); err != nil {
		return nil, err
	}
	if err := o.checkForeignBoundary(f.Path); err != nil {
		return nil, err
	}
	physRoot := o.enclosingGlobalRoot(f.Path)
	if physRoot == "" {
		return nil, fmt.Errorf("%w: %s resolves outside global skill directories", ErrForeignZone, entry)
	}
	if err := o.checkGitBoundary(entry, store); err != nil {
		return nil, err
	}
	if f.Path != entry {
		if err := o.checkGitBoundary(f.Path, physRoot); err != nil {
			return nil, err
		}
	}
	return &active{key: key, path: entry, folder: f, skill: owners[0]}, nil
}

// planActive removes the links to the store folder, the folder and its lock
// entry, dropping only Managed[a.key] so an unrelated parked entry is never
// touched.
func (o *observation) planActive(a *active) (Plan, error) {
	pv := Preview{Key: a.key, Folder: a.path, TreeSHA: a.folder.TreeSHA}
	var err error
	pv.Links, pv.KeptLinks, err = o.links(func(l graph.Link) bool { return l.Status == graph.LinkOK && l.Folder == a.folder.Path }, a.folder)
	if err != nil {
		return Plan{}, err
	}
	if err := o.keepZoneLinks(&pv, a.folder.Path); err != nil {
		return Plan{}, err
	}
	pv.Targets = o.harnessesList(a.folder.Sightings, "")
	pv.Homonyms = o.homonyms(a.folder.Path, a.folder.Name, a.folder.TreeSHA, a.key)
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
	return o.build(pv, engine.Effect{DropManaged: []string{a.key}})
}

// planOff removes a hand-disabled OFF/ folder, links resolving to it, and its
// lock entry when it is the canonical folder of a lock skill. It never drops
// Managed or Parked entries by basename.
func (o *observation) planOff(entry string, f graph.Folder, owner *graph.Skill) (Plan, error) {
	base := filepath.Base(entry)
	pv := Preview{Key: base, Off: true, Folder: entry, TreeSHA: f.TreeSHA}
	var err error
	pv.Links, pv.KeptLinks, err = o.links(func(l graph.Link) bool { return l.Status == graph.LinkOK && l.Folder == f.Path }, f)
	if err != nil {
		return Plan{}, err
	}
	if err := o.keepZoneLinks(&pv, f.Path); err != nil {
		return Plan{}, err
	}
	pv.Targets = o.harnessesList(f.Sightings, "")
	pv.Homonyms = o.homonyms(f.Path, f.Name, f.TreeSHA, base)
	pv.Modified = Modification{State: ModifiedUnknown, Actual: f.TreeSHA}
	if owner != nil && owner.LockKey != "" {
		lockEnt, last, err := o.lockEntry(owner.LockKey)
		if err != nil {
			return Plan{}, err
		}
		pv.LockEntry = &LockEntry{Name: lockEnt.Name, Raw: slices.Clone(lockEnt.Raw), DeletesLock: last}
		pv.Modified = modification(lockEnt.SkillFolderHash, f.TreeSHA)
		pv.PendingCandidates = o.openCandidates(owner.ID)
	}
	return o.build(pv, engine.Effect{})
}

// planHarness removes an independent non-Git folder directly inside a
// configured global harness directory and only safely owned links resolving to
// it, with no state, lock or candidate effects.
func (o *observation) planHarness(entry string, f graph.Folder) (Plan, error) {
	base := filepath.Base(entry)
	pv := Preview{Key: base, Folder: entry, TreeSHA: f.TreeSHA}
	var err error
	pv.Links, pv.KeptLinks, err = o.links(func(l graph.Link) bool { return l.Status == graph.LinkOK && l.Folder == f.Path }, f)
	if err != nil {
		return Plan{}, err
	}
	if err := o.keepZoneLinks(&pv, f.Path); err != nil {
		return Plan{}, err
	}
	pv.Targets = o.harnessesList(f.Sightings, "")
	pv.Homonyms = o.homonyms(f.Path, f.Name, f.TreeSHA, base)
	pv.Modified = Modification{State: ModifiedUnknown, Actual: f.TreeSHA}
	return o.build(pv, engine.Effect{})
}

// planParked removes a parked folder and the links that still reach it;
// its lock entry is in Parked, which the effect drops.
func (o *observation) planParked(key string, p domain.ParkedSkill) (Plan, error) {
	if p.Folder == "" && len(p.Links) == 0 && p.Entry != nil {
		return o.planParkedEntry(key, p)
	}
	folder := filepath.Join(o.r.d.Paths.ParkedDir(), key)
	if p.Folder != folder {
		return Plan{}, fmt.Errorf("%w: parked %s is at %s, not %s", ErrChanged, key, p.Folder, folder)
	}
	mode, err := o.r.d.Files.Mode(folder)
	if errors.Is(err, iofs.ErrNotExist) {
		return Plan{}, fmt.Errorf("%w: parked %s is missing", ErrChanged, key)
	}
	if err != nil {
		return Plan{}, fmt.Errorf("look at %s: %w", folder, err)
	}
	if mode&iofs.ModeSymlink != 0 {
		return Plan{}, fmt.Errorf("%w: %s is a symlink", ErrAlias, folder)
	}
	if !mode.IsDir() {
		return Plan{}, fmt.Errorf("%w: %s is not a directory", ErrChanged, folder)
	}
	phys, err := o.resolved(folder)
	if err != nil {
		return Plan{}, err
	}
	if err := o.checkForeignBoundary(folder); err != nil {
		return Plan{}, err
	}
	if phys != folder {
		if err := o.checkForeignBoundary(phys); err != nil {
			return Plan{}, err
		}
	}
	parkedRoot := o.r.d.Paths.ParkedDir()
	physParkedRoot, err := o.resolved(parkedRoot)
	if err != nil {
		return Plan{}, err
	}
	if filepath.Dir(phys) != physParkedRoot {
		return Plan{}, fmt.Errorf("%w: %s resolves outside the parking zone", ErrForeignZone, folder)
	}
	if err := o.checkGitBoundary(folder, parkedRoot); err != nil {
		return Plan{}, err
	}
	if phys != folder {
		if err := o.checkGitBoundary(phys, physParkedRoot); err != nil {
			return Plan{}, err
		}
	}
	tree, err := o.r.d.HashDir(folder)
	if err != nil {
		return Plan{}, fmt.Errorf("hash %s: %w", folder, err)
	}
	pv := Preview{Key: key, Parked: true, Folder: folder, TreeSHA: tree.SHA}
	pv.Links, pv.KeptLinks, err = o.links(func(l graph.Link) bool { return l.Final == folder }, graph.Folder{})
	if err != nil {
		return Plan{}, err
	}
	if err := o.keepZoneLinks(&pv, phys); err != nil {
		return Plan{}, err
	}
	pv.Homonyms = o.homonyms(folder, key, tree.SHA, key)
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
	return o.build(pv, engine.Effect{DropParked: []string{key}})
}

// planParkedEntry drops the record park_entry leaves (repair): the lock
// entry of a folder that was already gone, with no folder and no links
// (L47). Nothing on disk is removed; the effect drops the record, and undo
// puts it back.
func (o *observation) planParkedEntry(key string, p domain.ParkedSkill) (Plan, error) {
	if p.Key != key || p.Entry.Name != key {
		return Plan{}, fmt.Errorf("%w: the parked entry of %s names %s", ErrChanged, key, p.Entry.Name)
	}
	entry := *p.Entry
	entry.Raw = slices.Clone(entry.Raw)
	pv := Preview{Key: key, Parked: true, ParkedEntry: &entry,
		Modified:          Modification{State: ModifiedUnknown},
		PendingCandidates: o.openCandidates(domain.LockID(entry.Name))}
	return o.build(pv, engine.Effect{DropParked: []string{key}})
}

// build turns a preview into the engine plan: every link, then the folder,
// then the lock entry, plus the explicit state effect.
func (o *observation) build(pv Preview, eff engine.Effect) (Plan, error) {
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
	if !eff.IsZero() {
		if err := b.SetEffect(eff); err != nil {
			return Plan{}, err
		}
	}
	plan, err := b.Build()
	if err != nil {
		return Plan{}, err
	}
	return Plan{Plan: plan, Preview: pv}, nil
}

// links splits the graph's links that match into the ones the removal takes
// out and the ones a read-only profile or Git repository keeps, both by path.
func (o *observation) links(match func(graph.Link) bool, f graph.Folder) ([]Link, []KeptLink, error) {
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
		inGit, err := o.linkInGitRepo(l.Path)
		if err != nil {
			return nil, nil, err
		}
		if inGit {
			kept = append(kept, KeptLink{Path: l.Path, Target: l.Text, Zone: graph.Zone("git")})
			continue
		}
		out = append(out, Link{Path: l.Path, Target: l.Text, Harnesses: o.harnessesList(f.Sightings, l.Path)})
	}
	slices.SortFunc(out, func(x, y Link) int { return strings.Compare(x.Path, y.Path) })
	slices.SortFunc(kept, func(x, y KeptLink) int { return strings.Compare(x.Path, y.Path) })
	return out, kept, nil
}

func (o *observation) linkInGitRepo(linkPath string) (bool, error) {
	parent := filepath.Dir(linkPath)
	if inGit, err := o.dirInGitRepo(parent); err != nil || inGit {
		return inGit, err
	}
	physParent, err := o.resolved(parent)
	if err != nil {
		return false, err
	}
	if physParent != parent {
		return o.dirInGitRepo(physParent)
	}
	return false, nil
}

func (o *observation) dirInGitRepo(dir string) (bool, error) {
	stop := o.enclosingGlobalRoot(dir)
	if stop == "" && (dir == o.r.d.Paths.StoreDir() || slices.Contains(o.harnesses, dir)) {
		stop = dir
	}
	home := o.r.d.Paths.Home()
	for cur := dir; ; cur = filepath.Dir(cur) {
		if stop != "" {
			if !within(cur, stop) {
				break
			}
		} else if !within(cur, home) || cur == home {
			break
		}
		hasGit, err := o.r.d.Files.Exists(filepath.Join(cur, ".git"))
		if err != nil {
			return false, fmt.Errorf("look for .git in %s: %w", cur, err)
		}
		if hasGit {
			return true, nil
		}
		if cur == stop || cur == filepath.Dir(cur) {
			break
		}
	}
	return false, nil
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

// harnessesList are the harnesses, profiles left out, of the sightings; of the
// ones listing entry when it is not empty.
func (o *observation) harnessesList(sightings []graph.Sighting, entry string) []string {
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
// whose entry is named base: reported, never removed.
func (o *observation) homonyms(path, name, treeSHA, base string) []Homonym {
	var out []Homonym
	for _, f := range o.g.Folders {
		if f.Path == path || (f.Name != name && filepath.Base(f.Entry) != base) {
			continue
		}
		out = append(out, Homonym{
			Path: f.Entry, Name: f.Name, TreeSHA: f.TreeSHA, Identical: f.TreeSHA == treeSHA,
			Harnesses: o.harnessesList(f.Sightings, ""), Profile: o.profileOf(f.Entry),
		})
	}
	out = append(out, o.projectHomonyms(path, name, treeSHA)...)
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
