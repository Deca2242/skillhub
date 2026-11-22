package repair

import (
	"fmt"
	"slices"

	"skillhub/internal/doctor"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// lockEntry reads the lock entry of a lock_entry_without_folder finding.
func (p *Planner) lockEntry(f doctor.Finding) (domain.LockEntry, error) {
	lock, err := p.d.Lock.Read()
	if err != nil {
		return domain.LockEntry{}, fmt.Errorf("read the skills lock: %w", err)
	}
	i := slices.IndexFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == f.LockKey })
	if f.LockKey == "" || i < 0 {
		return domain.LockEntry{}, &StaleError{ID: f.ID}
	}
	return lock.Entries[i], nil
}

// parkEntry parks the lock entry alone, so npx skills update stops
// reinstalling a folder that is gone (docs/design-v2.md section 2, fact 4).
// The parked record has no folder and no links; undo puts the entry back in
// its position.
func (p *Planner) parkEntry(f doctor.Finding, pv Preview) (Proposal, error) {
	e, err := p.lockEntry(f)
	if err != nil {
		return Proposal{}, err
	}
	pv.Key, pv.LockEntry = e.Name, e.Name
	b := p.d.Plans.NewBuilder(engine.Meta{Action: actionParkEntry})
	_ = b.LockPark(e.Name) // Build reports a refused step
	eff := engine.Effect{
		DropManaged: []string{e.Name},
		SetParked: []engine.ParkedEffect{{
			Skill: domain.ParkedSkill{Key: e.Name, Links: []domain.LinkRecord{}}, EntryStep: 0,
		}},
	}
	if err := b.SetEffect(eff); err != nil {
		return Proposal{}, err
	}
	return build(b, pv)
}

// reinstall plans nothing: it hands the entry's origin to the install flow,
// which the application runs: install.Planner.Reinstall plans it.
func (p *Planner) reinstall(f doctor.Finding, pv Preview) (Proposal, error) {
	e, err := p.lockEntry(f)
	if err != nil {
		return Proposal{}, err
	}
	pv.Key = e.Name
	return Proposal{Preview: pv, Reinstall: &ReinstallRef{
		Key: e.Name, Source: e.Source, SourceType: e.SourceType, SourceURL: e.SourceURL, SkillPath: e.SkillPath, Ref: e.Ref,
	}}, nil
}
