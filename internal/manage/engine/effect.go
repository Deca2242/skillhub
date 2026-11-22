package engine

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"skillhub/internal/shared/domain"
)

// Effect is the change to skillhub's own state that an operation makes when
// it closes, in the same close as the journal line: which skills are managed
// and which are parked (docs/design-v2.md section 14). Applying it twice
// gives the same state, so a close that a crash interrupted can be finished.
type Effect struct {
	SetManaged  []domain.ManagedSkill `json:"set_managed,omitempty"`
	DropManaged []string              `json:"drop_managed,omitempty"`
	SetParked   []ParkedEffect        `json:"set_parked,omitempty"`
	DropParked  []string              `json:"drop_parked,omitempty"`
}

// ParkedEffect records a parked skill. When EntryStep is a step index, the
// entry that lockPark or lockRemove step took out of the lock becomes
// Skill.Entry, with the neighbours the lock store reported. A zero Skill.At
// becomes the time of the snapshot and an empty Skill.SnapshotID the
// operation's snapshot.
type ParkedEffect struct {
	Skill     domain.ParkedSkill `json:"skill"`
	EntryStep int                `json:"entry_step"`
}

// IsZero reports whether the effect changes nothing.
func (f Effect) IsZero() bool {
	return len(f.SetManaged) == 0 && len(f.DropManaged) == 0 && len(f.SetParked) == 0 && len(f.DropParked) == 0
}

// managedKeys and parkedKeys are the keys the effect touches, in order.
func (f Effect) managedKeys() []string {
	out := slices.Clone(f.DropManaged)
	for _, m := range f.SetManaged {
		out = append(out, m.Key)
	}
	return out
}

func (f Effect) parkedKeys() []string {
	out := slices.Clone(f.DropParked)
	for _, p := range f.SetParked {
		out = append(out, p.Skill.Key)
	}
	return out
}

// validate checks the effect against the plan's steps: every key valid and
// touched once per map, and every EntryStep a step that takes an entry out.
func (f Effect) validate(steps []Step) error {
	for _, keys := range [][]string{f.managedKeys(), f.parkedKeys()} {
		seen := map[string]bool{}
		for _, k := range keys {
			if !validText(k) || seen[k] {
				return fmt.Errorf("%w: effect key %q is empty, not UTF-8 or repeated", ErrInvalidStep, k)
			}
			seen[k] = true
		}
	}
	for _, p := range f.SetParked {
		if p.EntryStep == -1 {
			continue
		}
		if p.EntryStep < 0 || p.EntryStep >= len(steps) || p.Skill.Entry != nil {
			return fmt.Errorf("%w: parked %s takes its entry from step %d", ErrInvalidStep, p.Skill.Key, p.EntryStep)
		}
		if k := steps[p.EntryStep].Kind; k != StepLockPark && k != StepLockRemove {
			return fmt.Errorf("%w: parked %s takes its entry from a %s step", ErrInvalidStep, p.Skill.Key, k)
		}
	}
	return nil
}

// StateImage is the part of skillhub's state an effect touches, as it was
// before the operation: the values of its keys that existed.
type StateImage struct {
	Managed map[string]domain.ManagedSkill `json:"managed"`
	Parked  map[string]domain.ParkedSkill  `json:"parked"`
}

// stateImage keeps the values of the keys f touches.
func stateImage(st domain.PersistedState, f Effect) StateImage {
	img := StateImage{Managed: map[string]domain.ManagedSkill{}, Parked: map[string]domain.ParkedSkill{}}
	for _, k := range f.managedKeys() {
		if v, ok := st.Managed[k]; ok {
			img.Managed[k] = v
		}
	}
	for _, k := range f.parkedKeys() {
		if v, ok := st.Parked[k]; ok {
			img.Parked[k] = v
		}
	}
	return img
}

// parkedValue is the value SetParked p writes: the step's entry, the
// snapshot's time and id filled in.
func parkedValue(p ParkedEffect, snap Snapshot, entries map[int]domain.ParkedEntry) (domain.ParkedSkill, error) {
	v := p.Skill
	v.Links = slices.Clone(v.Links)
	if v.At.IsZero() {
		v.At = snap.TakenAt
	}
	if v.SnapshotID == "" {
		v.SnapshotID = snap.SnapshotID
	}
	if p.EntryStep >= 0 {
		entry, ok := entries[p.EntryStep]
		if !ok {
			return domain.ParkedSkill{}, fmt.Errorf("%w: no entry recorded for step %d", ErrUnsupportedRecord, p.EntryStep)
		}
		entry.Raw = slices.Clone(entry.Raw)
		v.Entry = &entry
	}
	return v, nil
}

// apply changes st as f says.
func (f Effect) apply(st *domain.PersistedState, snap Snapshot, entries map[int]domain.ParkedEntry) error {
	if st.Managed == nil {
		st.Managed = map[string]domain.ManagedSkill{}
	}
	if st.Parked == nil {
		st.Parked = map[string]domain.ParkedSkill{}
	}
	for _, k := range f.DropManaged {
		delete(st.Managed, k)
	}
	for _, m := range f.SetManaged {
		m.Targets = slices.Clone(m.Targets)
		st.Managed[m.Key] = m
	}
	for _, k := range f.DropParked {
		delete(st.Parked, k)
	}
	for _, p := range f.SetParked {
		v, err := parkedValue(p, snap, entries)
		if err != nil {
			return err
		}
		st.Parked[p.Skill.Key] = v
	}
	if len(st.Managed) == 0 {
		st.Managed = nil
	}
	if len(st.Parked) == 0 {
		st.Parked = nil
	}
	return nil
}

// inverse is the effect that puts back the values img kept for the keys f
// touches.
func (f Effect) inverse(img StateImage) Effect {
	var inv Effect
	for _, k := range f.managedKeys() {
		if v, ok := img.Managed[k]; ok {
			inv.SetManaged = append(inv.SetManaged, v)
		} else {
			inv.DropManaged = append(inv.DropManaged, k)
		}
	}
	for _, k := range f.parkedKeys() {
		if v, ok := img.Parked[k]; ok {
			inv.SetParked = append(inv.SetParked, ParkedEffect{Skill: v, EntryStep: -1})
		} else {
			inv.DropParked = append(inv.DropParked, k)
		}
	}
	return inv
}

// still reports whether st still holds what f wrote when the operation of
// plan p and snapshot snap closed; the neighbours of a parked entry, which
// only the lock store knew, are not compared.
func (f Effect) still(st domain.PersistedState, p Plan, snap Snapshot) error {
	for _, k := range f.DropManaged {
		if _, ok := st.Managed[k]; ok {
			return fmt.Errorf("%w: skill %s is managed again", ErrChangedSince, k)
		}
	}
	for _, m := range f.SetManaged {
		if got, ok := st.Managed[m.Key]; !ok || !reflect.DeepEqual(got, m) {
			return fmt.Errorf("%w: the managed skill %s changed", ErrChangedSince, m.Key)
		}
	}
	for _, k := range f.DropParked {
		if _, ok := st.Parked[k]; ok {
			return fmt.Errorf("%w: skill %s is parked again", ErrChangedSince, k)
		}
	}
	for _, pe := range f.SetParked {
		got, ok := st.Parked[pe.Skill.Key]
		want, err := parkedValue(pe, snap, stepEntries(p))
		if err != nil {
			return err
		}
		if ok && pe.EntryStep >= 0 && got.Entry != nil {
			// Compare the entry's name and bytes only.
			e := *got.Entry
			e.Before, e.After, e.LockSHA = want.Entry.Before, want.Entry.After, want.Entry.LockSHA
			got.Entry = &e
		}
		if !ok || !reflect.DeepEqual(got, want) {
			return fmt.Errorf("%w: the parked skill %s changed", ErrChangedSince, pe.Skill.Key)
		}
	}
	return nil
}

// stepEntries is, for every step that takes an entry out, the entry as its
// preimage saw it, without neighbours.
func stepEntries(p Plan) map[int]domain.ParkedEntry {
	out := map[int]domain.ParkedEntry{}
	for i, s := range p.Steps {
		if s.Kind == StepLockPark || s.Kind == StepLockRemove {
			out[i] = domain.ParkedEntry{Name: s.Pre[0].Entry, Raw: slices.Clone(s.Pre[0].Raw)}
		}
	}
	return out
}

// cloneEntries copies a recorded entries map.
func cloneEntries(m map[int]domain.ParkedEntry) map[int]domain.ParkedEntry {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}
