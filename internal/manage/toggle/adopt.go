package toggle

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"

	"skillhub/internal/graph"
	"skillhub/internal/inventory"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// AdoptOff plans adopting the skills parked by hand in ~/.agents/skills/OFF/
// (decision 4), as one operation with a full preview: every skill folder
// directly in OFF/ moves to ParkedDir()/<key>; every link that points to it
// goes, the ones in ~/.claude/skills/OFF/ included, and so do the broken
// links towards <store>/<key>, where it was before it was moved to OFF/; its
// lock entry is parked. OFF/README.md and anything else in OFF/ that is not
// a skill are never touched.
//
// A parked record keeps, as the links enabling the skill will create, only
// the removed links whose target is <store>/<key>: the ones that lead to the
// skill once it is back in the store. The rest pointed into OFF/.
func (p *Planner) AdoptOff(g graph.Graph) (Proposal, error) {
	st, err := p.loadState()
	if err != nil {
		return Proposal{}, err
	}
	if err := p.checkParking(); err != nil {
		return Proposal{}, err
	}
	profiles, err := p.profileRoots()
	if err != nil {
		return Proposal{}, err
	}
	off := filepath.Join(p.d.Paths.StoreDir(), inventory.OffDirName)
	var folders []graph.Folder
	for _, f := range g.Folders {
		if filepath.Dir(f.Entry) == off {
			folders = append(folders, f)
		}
	}
	if len(folders) == 0 {
		return Proposal{}, fmt.Errorf("%w: %s", ErrNothingToAdopt, off)
	}
	slices.SortFunc(folders, func(x, y graph.Folder) int { return cmp.Compare(x.Entry, y.Entry) })

	pv := Preview{Action: ActionAdoptOff}
	for _, b := range g.Boundary {
		if within(b.Path, off) {
			pv.Kept = append(pv.Kept, b.Path)
		}
	}
	type adoption struct {
		unlink, restore []domain.LinkRecord
	}
	var plans []adoption
	var targets []string
	for _, f := range folders {
		key := filepath.Base(f.Entry)
		sk, err := p.adoptable(g, st, f, key)
		if err != nil {
			return Proposal{}, err
		}
		store := filepath.Join(p.d.Paths.StoreDir(), key)
		change := SkillChange{Key: key, From: f.Entry, To: filepath.Join(p.d.Paths.ParkedDir(), key), LockEntry: sk.LockKey == key,
			StillVisible: remaining(g, sk, f.Path)}
		pv.StillVisible = append(pv.StillVisible, change.StillVisible...)
		var unlink, restore []domain.LinkRecord
		for _, l := range g.Links {
			points := l.Status == graph.LinkOK && l.Folder == f.Path ||
				l.Status == graph.LinkBroken && (l.Final == store || l.Final == f.Entry)
			if !points {
				continue
			}
			if inAny(l.Path, profiles) {
				pv.Kept = append(pv.Kept, l.Path)
				continue
			}
			lc := p.change(l.Path, l.Text, l.Status == graph.LinkBroken)
			change.RemovedLinks = append(change.RemovedLinks, lc)
			targets = append(targets, lc.Harnesses...)
			rec := domain.LinkRecord{Path: l.Path, Target: l.Text}
			unlink = append(unlink, rec)
			if len(l.Hops) > 0 && l.Hops[0] == store {
				restore = append(restore, rec)
			}
		}
		change.RestoreLinks = restore
		plans = append(plans, adoption{unlink: unlink, restore: restore})
		pv.Skills = append(pv.Skills, change)
	}
	slices.Sort(pv.Kept)
	pv.Kept = slices.Compact(pv.Kept)

	b := p.d.Plans.NewBuilder(engine.Meta{Action: string(ActionAdoptOff), Targets: sortedUnique(targets)})
	s := &steps{b: b}
	var effect engine.Effect
	for i, c := range pv.Skills {
		effect.SetParked = append(effect.SetParked, park(s, c.Key, c.From, c.To, plans[i].unlink, plans[i].restore, c.LockEntry))
	}
	if err := b.SetEffect(effect); err != nil {
		return Proposal{}, err
	}
	return p.build(b, pv)
}

// adoptable checks that the OFF/ folder f is one skill's alone, outside
// every foreign zone, and that its key is free in the store and the parking
// records.
func (p *Planner) adoptable(g graph.Graph, st domain.PersistedState, f graph.Folder, key string) (graph.Skill, error) {
	if err := validKey(key); err != nil {
		return graph.Skill{}, err
	}
	var claims []graph.Skill
	for _, s := range g.Skills {
		if s.Canonical == f.Path {
			claims = append(claims, s)
		}
	}
	if len(claims) != 1 || len(claims[0].SharedWith) > 0 {
		return graph.Skill{}, fmt.Errorf("%w: %d skills claim %s", ErrAmbiguous, len(claims), f.Entry)
	}
	s := claims[0]
	if s.Zone != graph.ZoneNone {
		return graph.Skill{}, fmt.Errorf("%w: %s is %s", ErrForeignZone, f.Entry, s.Zone)
	}
	if s.LockKey != "" && s.LockKey != key {
		return graph.Skill{}, fmt.Errorf("%w: %s belongs to the lock entry %s", ErrAmbiguous, f.Entry, s.LockKey)
	}
	if _, ok := st.Parked[key]; ok {
		return graph.Skill{}, fmt.Errorf("%w: %s", ErrAlreadyParked, key)
	}
	store := filepath.Join(p.d.Paths.StoreDir(), key)
	taken, err := p.exists(store)
	if err != nil {
		return graph.Skill{}, err
	}
	if taken {
		return graph.Skill{}, fmt.Errorf("%w: %s", ErrKeyInUse, store)
	}
	return s, nil
}
