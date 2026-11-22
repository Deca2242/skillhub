package repair

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"

	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
)

// adoptOffer offers adopting the copies of a manageable skill whose folders
// are all outside every foreign zone and profile. Identical copies of a skill
// whose canonical folder is not a store folder (a clone, a folder in OFF/)
// are never adopted.
func (p *Planner) adoptOffer(g graph.Graph, f doctor.Finding) (Offer, error) {
	s, ok := skillByID(g, f.Skill)
	if !ok {
		return Offer{}, fmt.Errorf("%w: no skill %s", ErrNotOffered, f.Skill)
	}
	if s.Zone != graph.ZoneNone {
		return Offer{Reason: ReasonForeignZone}, nil
	}
	if len(s.SharedWith) > 0 {
		return Offer{Reason: ReasonAmbiguous}, nil
	}
	if f.Kind == doctor.IdenticalCopies {
		cf, ok := folderByPath(g, s.Canonical)
		if !ok || filepath.Dir(cf.Entry) != g.Store {
			return Offer{Reason: ReasonNotInStore}, nil
		}
	}
	for _, path := range s.Folders {
		fo, ok := folderByPath(g, path)
		if !ok {
			return Offer{}, fmt.Errorf("%w: no folder %s", ErrNotOffered, path)
		}
		if bad, err := p.foreign(g, fo.Entry); err != nil || bad {
			return Offer{Reason: ReasonForeignZone}, err
		}
	}
	return Offer{Repairs: []Kind{Adopt}}, nil
}

// adoption is an adoption being planned.
type adoption struct {
	key       string
	storePath string // <store>/<key>, where the canonical folder ends
	canonical graph.Folder
	// moves is true when the canonical folder moves to storePath, and
	// replaced is the store folder it replaces, if any.
	moves    bool
	replaced *graph.Folder
	copies   []graph.Folder
	links    []LinkChange
	// linked are the entries a created link takes the place of.
	linked map[string]bool
}

// adopt brings a skill's copies to one canonical folder in the store
// (docs/design-v2.md section 8): the store folder when there is one and the
// user keeps it, otherwise the copy the user chose, which moves to the store.
// Every other copy is removed, the operation's snapshot keeping it, and in a
// harness's link directory the npx link to the store takes its place; a
// harness that reads the store needs nothing. No lock entry is ever written,
// and a lock skill's folder is never replaced by a copy of unknown origin.
func (p *Planner) adopt(g graph.Graph, st domain.PersistedState, f doctor.Finding, o Options, pv Preview) (Proposal, error) {
	a, err := p.choose(g, st, f, o)
	if err != nil {
		return Proposal{}, err
	}
	if err := p.place(g, a); err != nil {
		return Proposal{}, err
	}
	if err := p.checkLinked(g, a); err != nil {
		return Proposal{}, err
	}

	var targets []string
	for _, l := range a.links {
		targets = append(targets, p.linkHarnesses(filepath.Dir(l.Path))...)
	}
	targets = sortedUnique(targets)
	b := p.d.Plans.NewBuilder(engine.Meta{Action: actionAdopt, Targets: targets, Canonical: a.canonical.Entry})
	// Build reports the first refused step.
	if !a.moves {
		// The canonical folder the copies give way to must stay exactly as
		// it was shown: an update or a removal before Apply refuses the plan.
		_ = b.Require(engine.Image{Path: a.canonical.Entry, State: engine.StateTree, TreeSHA: a.canonical.TreeSHA})
	}
	if a.moves {
		if a.replaced != nil {
			_ = b.RemoveTree(a.replaced.Entry)
		}
		_ = b.Move(a.canonical.Entry, a.storePath)
		pv.Moved = []Move{{From: a.canonical.Entry, To: a.storePath}}
	}
	for _, c := range a.copies {
		_ = b.RemoveTree(c.Entry)
	}
	for _, l := range a.links {
		_ = b.Link(l.Path, l.Target)
	}
	m := domain.ManagedSkill{Key: a.key, Targets: targets}
	if old, ok := st.Managed[a.key]; ok {
		m.Targets = sortedUnique(append(slices.Clone(old.Targets), targets...))
	}
	if m.Targets == nil {
		m.Targets = []string{}
	}
	if err := b.SetEffect(engine.Effect{SetManaged: []domain.ManagedSkill{m}}); err != nil {
		return Proposal{}, err
	}

	pv.Key, pv.Canonical, pv.CreatedLinks = a.key, a.storePath, a.links
	others := slices.Clone(a.copies)
	if a.replaced != nil {
		others = append([]graph.Folder{*a.replaced}, others...)
	}
	for _, c := range others {
		pv.RemovedCopies = append(pv.RemovedCopies, CopyChange{
			Path: c.Entry, TreeSHA: c.TreeSHA, Harnesses: seenBy(c, c.Entry),
			Replaced: a.linked[c.Entry] || (a.replaced != nil && c.Path == a.replaced.Path),
		})
		d, err := p.diff(a.canonical.Entry, c.Entry)
		if err != nil {
			return Proposal{}, err
		}
		pv.Copies = append(pv.Copies, d)
	}
	return build(b, pv)
}

// choose finds the key, the canonical folder and the copies. Identical
// copies of a store skill keep the store folder; otherwise the candidates are
// the skill's folders and the store folder of the key, if any, and the user
// must choose when there is more than one.
func (p *Planner) choose(g graph.Graph, st domain.PersistedState, f doctor.Finding, o Options) (*adoption, error) {
	s, ok := skillByID(g, f.Skill)
	if !ok {
		return nil, &StaleError{ID: f.ID}
	}
	var folders []graph.Folder
	for _, path := range s.Folders {
		fo, ok := folderByPath(g, path)
		if !ok {
			return nil, &StaleError{ID: f.ID}
		}
		folders = append(folders, fo)
	}
	if len(folders) == 0 {
		return nil, &StaleError{ID: f.ID}
	}
	a := &adoption{linked: map[string]bool{}}
	if f.Kind == doctor.IdenticalCopies {
		a.canonical, a.copies = folders[0], folders[1:]
		a.key, a.storePath = filepath.Base(a.canonical.Entry), a.canonical.Entry
		if o.Canonical != "" && o.Canonical != a.canonical.Entry && o.Canonical != a.canonical.Path {
			return nil, fmt.Errorf("%w: %s; the store folder %s is the canonical one", ErrBadCanonical, o.Canonical, a.canonical.Entry)
		}
		return a, nil
	}

	a.key = s.Name
	if !validKey(a.key) {
		a.key = filepath.Base(folders[0].Entry)
	}
	a.storePath = filepath.Join(g.Store, a.key)
	candidates := slices.Clone(folders)
	store, inStore := folderByEntry(g, a.storePath)
	if inStore {
		candidates = append(candidates, store)
	} else if taken, err := p.exists(a.storePath); err != nil {
		return nil, err
	} else if taken {
		return nil, fmt.Errorf("%w: %s", ErrKeyInUse, a.storePath)
	}
	switch {
	case o.Canonical == "" && len(candidates) == 1:
		a.canonical = candidates[0]
	case o.Canonical == "":
		return nil, fmt.Errorf("%w: %d candidates for %s", ErrNeedsCanonical, len(candidates), a.key)
	default:
		i := slices.IndexFunc(candidates, func(c graph.Folder) bool { return c.Entry == o.Canonical || c.Path == o.Canonical })
		if i < 0 {
			return nil, fmt.Errorf("%w: %s", ErrBadCanonical, o.Canonical)
		}
		a.canonical = candidates[i]
	}
	for _, fo := range folders {
		if fo.Path != a.canonical.Path {
			a.copies = append(a.copies, fo)
		}
	}
	if inStore && a.canonical.Path == store.Path {
		return a, nil
	}
	a.moves = true
	if err := p.checkKeyFree(st, a.key); err != nil {
		return nil, err
	}
	if inStore {
		cs := claims(g, store.Path)
		if slices.ContainsFunc(cs, func(c graph.Skill) bool { return c.LockKey != "" }) {
			return nil, fmt.Errorf("%w: %s", ErrLockedSkill, a.storePath)
		}
		if len(cs) != 1 || cs[0].Zone != graph.ZoneNone || len(cs[0].SharedWith) > 0 || len(cs[0].Aliases) > 0 {
			return nil, fmt.Errorf("%w: %s is shared", ErrKeyInUse, a.storePath)
		}
		a.replaced = &store
	}
	return a, nil
}

// checkKeyFree refuses moving a copy of unknown origin to a key that a lock
// entry, a parked record or a managed record holds: that record would then
// govern someone else's content.
func (p *Planner) checkKeyFree(st domain.PersistedState, key string) error {
	lock, err := p.d.Lock.Read()
	if err != nil {
		return fmt.Errorf("read the skills lock: %w", err)
	}
	var held []string
	if slices.ContainsFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == key }) {
		held = append(held, "a lock entry")
	}
	if _, ok := st.Parked[key]; ok {
		held = append(held, "a parked record")
	}
	if _, ok := st.Managed[key]; ok {
		held = append(held, "a managed record")
	}
	if len(held) > 0 {
		return fmt.Errorf("%w: %s is held by %v", ErrLockedSkill, key, held)
	}
	return nil
}

// place decides what takes the place of every folder that leaves its entry:
// the npx link to the store in a harness's link directory; nothing where only
// harnesses that read the store see it. A copy that a harness reading
// neither sees elsewhere would hide the skill from it, and is refused.
func (p *Planner) place(g graph.Graph, a *adoption) error {
	gone := []graph.Folder{}
	if a.moves {
		gone = append(gone, a.canonical)
		if a.replaced != nil {
			if err := p.checkTouch(g, a.replaced.Entry); err != nil {
				return err
			}
		}
	}
	gone = append(gone, a.copies...)
	slices.SortFunc(gone, func(x, y graph.Folder) int { return cmp.Compare(x.Entry, y.Entry) })
	freed := map[string]bool{a.storePath: a.replaced != nil}
	for _, fo := range gone {
		freed[fo.Entry] = true
	}
	created := map[string]bool{}
	for _, fo := range gone {
		if err := p.checkTouch(g, fo.Entry); err != nil {
			return err
		}
		dir := filepath.Dir(fo.Entry)
		if len(p.linkHarnesses(dir)) == 0 {
			for _, h := range seenBy(fo, fo.Entry) {
				if !p.readsStore(h) {
					return fmt.Errorf("%w: %s sees %s", ErrNoPlacement, h, fo.Entry)
				}
			}
			continue
		}
		path, target, err := toggle.StoreLink(p.resolver(), dir, a.key)
		if err != nil {
			return err
		}
		if created[path] {
			continue
		}
		if err := p.checkTouch(g, path); err != nil {
			return err
		}
		if !freed[path] {
			taken, err := p.exists(path)
			if err != nil {
				return err
			}
			if taken {
				if !a.moves && linksTo(g, path, a.canonical.Path) {
					continue // the harness already links the canonical folder
				}
				return fmt.Errorf("%w: %s", ErrPathTaken, path)
			}
		}
		created[path] = true
		a.linked[path] = true
		a.links = append(a.links, LinkChange{Path: path, Target: target, Harnesses: p.harnessesAt(path)})
	}
	slices.SortFunc(a.links, func(x, y LinkChange) int { return cmp.Compare(x.Path, y.Path) })
	return nil
}

// checkLinked refuses an adoption that would leave links broken: a link that
// leads to a folder that moves or is removed, unless a link to the store takes
// that folder's place.
func (p *Planner) checkLinked(g graph.Graph, a *adoption) error {
	gone := slices.Clone(a.copies)
	if a.moves {
		gone = append(gone, a.canonical)
	}
	for _, fo := range gone {
		if a.linked[fo.Entry] {
			continue
		}
		for _, l := range g.Links {
			if l.Status == graph.LinkOK && l.Folder == fo.Path {
				return fmt.Errorf("%w: %s leads to %s", ErrLinkedCopy, l.Path, fo.Entry)
			}
		}
	}
	return nil
}

// linksTo reports whether the link at path leads to folder.
func linksTo(g graph.Graph, path, folder string) bool {
	return slices.ContainsFunc(g.Links, func(l graph.Link) bool {
		return l.Path == path && l.Status == graph.LinkOK && l.Folder == folder
	})
}

// seenBy lists, sorted, the harnesses and profiles that see the folder at
// entry.
func seenBy(fo graph.Folder, entry string) []string {
	var out []string
	for _, v := range fo.Sightings {
		if v.Entry == entry {
			out = append(out, v.Harness)
		}
	}
	return sortedUnique(out)
}

// diff compares the copy at entry with the canonical folder, file by file.
func (p *Planner) diff(canonical, entry string) (CopyDiff, error) {
	want, err := p.d.HashDir(canonical)
	if err != nil {
		return CopyDiff{}, fmt.Errorf("hash %s: %w", canonical, err)
	}
	got, err := p.d.HashDir(entry)
	if err != nil {
		return CopyDiff{}, fmt.Errorf("hash %s: %w", entry, err)
	}
	blobs := func(nodes []treehash.Node) map[string]string {
		out := map[string]string{}
		for _, n := range nodes {
			if !n.IsTree() {
				out[n.Path] = string(n.Mode) + " " + n.SHA
			}
		}
		return out
	}
	w, c := blobs(want.Nodes), blobs(got.Nodes)
	d := CopyDiff{Path: entry, TreeSHA: got.SHA}
	for path, sha := range c {
		if old, ok := w[path]; !ok {
			d.Added = append(d.Added, path)
		} else if old != sha {
			d.Changed = append(d.Changed, path)
		}
	}
	for path := range w {
		if _, ok := c[path]; !ok {
			d.Removed = append(d.Removed, path)
		}
	}
	slices.Sort(d.Added)
	slices.Sort(d.Removed)
	slices.Sort(d.Changed)
	return d, nil
}
