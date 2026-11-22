package toggle

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"

	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// Disable plans disabling the skill of key in every managed target
// (docs/design-v2.md section 5, decision 3): remove every link outside the
// store that resolves to its folder, and only those, as the graph saw them;
// move the folder to ParkedDir()/<key>; park its lock entry, if it has one.
// The plan's effect records Parked[key] with the exact links and drops
// Managed[key]. A link in a read-only profile is kept and listed.
func (p *Planner) Disable(g graph.Graph, key string) (Proposal, error) {
	if err := validKey(key); err != nil {
		return Proposal{}, err
	}
	st, err := p.loadState()
	if err != nil {
		return Proposal{}, err
	}
	if _, ok := st.Parked[key]; ok {
		return Proposal{}, fmt.Errorf("%w: %s", ErrAlreadyParked, key)
	}
	if err := p.checkParking(); err != nil {
		return Proposal{}, err
	}
	a, err := p.activeSkill(g, key)
	if err != nil {
		return Proposal{}, err
	}
	links, kept, err := p.linksTo(g, a.folder)
	if err != nil {
		return Proposal{}, err
	}
	slices.SortFunc(links, func(x, y graph.Link) int { return cmp.Compare(x.Path, y.Path) })

	parked := filepath.Join(p.d.Paths.ParkedDir(), key)
	change := SkillChange{Key: key, From: a.store, To: parked, LockEntry: a.skill.LockKey == key,
		StillVisible: remaining(g, a.skill, a.folder)}
	var records []domain.LinkRecord
	var targets []string
	for _, l := range links {
		lc := p.change(l.Path, l.Text, false)
		change.RemovedLinks = append(change.RemovedLinks, lc)
		records = append(records, domain.LinkRecord{Path: l.Path, Target: l.Text})
		targets = append(targets, lc.Harnesses...)
	}
	change.RestoreLinks = records

	b := p.d.Plans.NewBuilder(engine.Meta{Action: string(ActionDisable), Targets: sortedUnique(targets), Canonical: a.store})
	pe := park(&steps{b: b}, key, a.store, parked, records, records, change.LockEntry)
	if err := b.SetEffect(engine.Effect{DropManaged: []string{key}, SetParked: []engine.ParkedEffect{pe}}); err != nil {
		return Proposal{}, err
	}
	return p.build(b, Preview{Action: ActionDisable, Skills: []SkillChange{change}, Kept: kept,
		StillVisible: slices.Clone(change.StillVisible)})
}

// steps counts the steps added to a builder, so an effect can name the step
// that parks an entry. A refused step stays in the builder, which fails every
// later call and Build with it, so the count only matters for a plan that
// builds.
type steps struct {
	b *engine.Builder
	n int
}

func (s *steps) add(err error) {
	if err == nil {
		s.n++
	}
}

// park adds the steps that disable one skill: remove every link of unlink,
// move from to parked and park the lock entry of key when lockEntry. It
// returns the parked record for the effect, which keeps restore as the links
// enabling it will create.
func park(s *steps, key, from, parked string, unlink, restore []domain.LinkRecord, lockEntry bool) engine.ParkedEffect {
	for _, l := range unlink {
		s.add(s.b.Unlink(l.Path, l.Target))
	}
	s.add(s.b.Move(from, parked))
	links := slices.Clone(restore)
	if links == nil {
		links = []domain.LinkRecord{}
	}
	pe := engine.ParkedEffect{Skill: domain.ParkedSkill{Key: key, Folder: parked, Links: links}, EntryStep: -1}
	if lockEntry {
		pe.EntryStep = s.n
		s.add(s.b.LockPark(key))
	}
	return pe
}

// Enable plans enabling a parked skill: the exact inverse of Disable. The
// folder goes back to <store>/<key>, the same links are created and the lock
// entry returns to its position; Managed[key] comes back as the disable
// operation's snapshot recorded it. When a skill with the same key was
// installed meanwhile, in the store or in the lock, it refuses with a
// KeyReinstalledError and the user chooses. A record with the entry alone,
// which park_entry leaves, gives back only the entry (unpark).
func (p *Planner) Enable(key string) (Proposal, error) {
	if err := validKey(key); err != nil {
		return Proposal{}, err
	}
	st, err := p.loadState()
	if err != nil {
		return Proposal{}, err
	}
	ps, ok := st.Parked[key]
	if !ok {
		return Proposal{}, fmt.Errorf("%w: %s", ErrNotParked, key)
	}
	parked := filepath.Join(p.d.Paths.ParkedDir(), key)
	// entryOnly is the record park_entry leaves (repair): the lock entry of a
	// folder that was already gone, with no folder and no links (L47).
	entryOnly := ps.Folder == "" && len(ps.Links) == 0 && ps.Entry != nil
	if (ps.Folder != parked && !entryOnly) || ps.Key != key || (ps.Entry != nil && ps.Entry.Name != key) {
		return Proposal{}, fmt.Errorf("%w: %s is parked at %s, not %s", ErrBadParkedRecord, key, ps.Folder, parked)
	}
	store := filepath.Join(p.d.Paths.StoreDir(), key)
	lock, err := p.d.Lock.Read()
	if err != nil {
		return Proposal{}, fmt.Errorf("read the skills lock: %w", err)
	}
	if err := p.checkReinstalled(key, store, lock); err != nil {
		return Proposal{}, err
	}
	if err := checkNeighbours(st, ps, lock); err != nil {
		return Proposal{}, err
	}
	var managed []domain.ManagedSkill
	if ps.SnapshotID != "" {
		snap, err := p.d.Plans.ReadSnapshot(ps.SnapshotID)
		if err != nil {
			return Proposal{}, fmt.Errorf("read the snapshot %s of %s: %w", ps.SnapshotID, key, err)
		}
		if m, ok := snap.State.Managed[key]; ok {
			managed = append(managed, m)
		}
	}

	change := SkillChange{Key: key, From: parked, To: store, LockEntry: ps.Entry != nil}
	if entryOnly {
		change.From, change.To = "", ""
	}
	var targets []string
	for _, l := range ps.Links {
		lc := p.change(l.Path, l.Target, false)
		change.CreatedLinks = append(change.CreatedLinks, lc)
		targets = append(targets, lc.Harnesses...)
	}
	b := p.d.Plans.NewBuilder(engine.Meta{Action: string(ActionEnable), Targets: sortedUnique(targets), Canonical: change.To})
	if !entryOnly {
		_ = b.Move(parked, store) // Build reports the first refused step
	}
	for _, l := range ps.Links {
		_ = b.Link(l.Path, l.Target)
	}
	if ps.Entry != nil {
		_ = b.LockUnpark(*ps.Entry)
	}
	if err := b.SetEffect(engine.Effect{SetManaged: managed, DropParked: []string{key}}); err != nil {
		return Proposal{}, err
	}
	return p.build(b, Preview{Action: ActionEnable, Skills: []SkillChange{change}})
}

// checkReinstalled refuses a key that exists again in the store or the lock.
func (p *Planner) checkReinstalled(key, store string, lock domain.SkillsLock) error {
	taken, err := p.exists(store)
	if err != nil {
		return err
	}
	inLock := slices.ContainsFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == key })
	if !taken && !inLock {
		return nil
	}
	re := &KeyReinstalledError{Key: key, LockEntry: inLock}
	if taken {
		re.Folder = store
	}
	return re
}

// checkNeighbours refuses, before any plan, a parked entry whose recorded
// neighbours are not in the lock: the lock store would refuse to put it
// back (WL), and the operation would fail at that step.
func checkNeighbours(st domain.PersistedState, ps domain.ParkedSkill, lock domain.SkillsLock) error {
	if ps.Entry == nil {
		return nil
	}
	ne := &NeighbourParkedError{Key: ps.Key}
	for _, n := range []string{ps.Entry.Before, ps.Entry.After} {
		if n == "" || slices.ContainsFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == n }) {
			continue
		}
		if q, ok := st.Parked[n]; ok && q.Entry != nil && q.Entry.Name == n {
			ne.Parked = append(ne.Parked, n)
		} else {
			ne.Gone = append(ne.Gone, n)
		}
	}
	if len(ne.Parked) == 0 && len(ne.Gone) == 0 {
		return nil
	}
	return ne
}

// build finishes the plan and fills the preview's identifier.
func (p *Planner) build(b *engine.Builder, pv Preview) (Proposal, error) {
	plan, err := b.Build()
	if err != nil {
		return Proposal{}, fmt.Errorf("plan %s: %w", pv.Action, err)
	}
	pv.PlanID = plan.ID
	return Proposal{Plan: plan, Preview: pv}, nil
}
