package app

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"skillhub/internal/data"
)

// ParkedSkill is a disabled skill as the entry points list it, so the user
// can enable it from its row.
type ParkedSkill struct {
	// Key is the skill's folder name in the store: what enabling takes.
	Key string
	// Harnesses see the skill again through the links enabling recreates,
	// sorted; empty for a record with no links.
	Harnesses []string
	// Since is when the skill was disabled.
	Since time.Time
	// LockEntry reports that the skill's lock entry is parked with it.
	LockEntry bool
}

// Parked lists the disabled skills of the version 2 state, sorted by key.
// It only reads.
func (a *App) Parked() ([]ParkedSkill, error) {
	st, err := a.d.State.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	if len(st.Parked) == 0 {
		return nil, nil
	}
	harnesses, err := data.LoadHarnesses(a.d.Paths, a.d.Files)
	if err != nil {
		return nil, fmt.Errorf("load the harness table: %w", err)
	}
	m := &manager{a: a, harnesses: harnesses}
	out := make([]ParkedSkill, 0, len(st.Parked))
	for key, ps := range st.Parked {
		var seen []string
		for _, l := range ps.Links {
			seen = append(seen, m.readersOf(filepath.Dir(l.Path))...)
		}
		slices.Sort(seen)
		out = append(out, ParkedSkill{
			Key: key, Harnesses: slices.Compact(seen), Since: ps.At, LockEntry: ps.Entry != nil,
		})
	}
	slices.SortFunc(out, func(x, y ParkedSkill) int { return cmp.Compare(x.Key, y.Key) })
	return out, nil
}
