package toggle

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"

	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/shared/domain"
)

// active is a skill whose real folder is <store>/<key>, as the graph sees it.
type active struct {
	key   string
	store string // <store>/<key>, lexical
	skill graph.Skill
	// folder is the physical folder; links are matched against it.
	folder string
}

// activeSkill finds the skill of key in the store. The folder must be a real
// entry <store>/<key> and the skill node must claim it alone, with no link
// in the store resolving to it: acting on a shared folder would break the
// other claimant.
func (p *Planner) activeSkill(g graph.Graph, key string) (active, error) {
	store := filepath.Join(p.d.Paths.StoreDir(), key)
	for _, b := range g.Boundary {
		if b.Path == store && b.Zone != graph.ZoneFile {
			return active{}, fmt.Errorf("%w: %s is %s", ErrForeignZone, store, b.Zone)
		}
	}
	i := slices.IndexFunc(g.Folders, func(f graph.Folder) bool { return f.Entry == store })
	if i < 0 {
		return active{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	folder := g.Folders[i].Path
	var claims []graph.Skill
	for _, s := range g.Skills {
		if s.Canonical == folder {
			claims = append(claims, s)
		}
	}
	if len(claims) != 1 {
		return active{}, fmt.Errorf("%w: %d skills claim %s", ErrAmbiguous, len(claims), store)
	}
	s := claims[0]
	if s.Zone != graph.ZoneNone {
		return active{}, fmt.Errorf("%w: %s is %s", ErrForeignZone, store, s.Zone)
	}
	if s.LockKey != "" && s.LockKey != key {
		return active{}, fmt.Errorf("%w: %s belongs to the lock entry %s", ErrAmbiguous, store, s.LockKey)
	}
	// Any link in the store that resolves to the folder is an alias (the
	// graph's Aliases are the first level of it); claims above already
	// cover SharedWith.
	for _, l := range g.Links {
		if l.Status == graph.LinkOK && l.Folder == folder && within(l.Path, g.Store) {
			return active{}, fmt.Errorf("%w: the link %s in the store resolves to %s", ErrAmbiguous, l.Path, store)
		}
	}
	return active{key: key, store: store, skill: s, folder: folder}, nil
}

// remaining are the sightings of s that survive parking its canonical folder:
// every entry that does not reach that folder, such as an own copy. The
// folder's own entry and every link to it either leave with the plan or
// lead nowhere afterwards.
func remaining(g graph.Graph, s graph.Skill, folder string) []graph.Sighting {
	var reach []string
	if i := slices.IndexFunc(g.Folders, func(f graph.Folder) bool { return f.Path == folder }); i >= 0 {
		reach = g.Folders[i].Entries
	}
	var out []graph.Sighting
	for _, v := range s.Visibility {
		if !slices.Contains(reach, v.Entry) {
			out = append(out, v)
		}
	}
	return out
}

// profileRoots are the read roots of the read-only diagnostic profiles.
func (p *Planner) profileRoots() ([]string, error) {
	var out []string
	for _, pr := range p.d.Profiles {
		for _, r := range pr.ReadRoots {
			abs, err := p.d.Paths.Join(filepath.FromSlash(r.Dir))
			if err != nil {
				return nil, fmt.Errorf("profile %s: %w", pr.ID, err)
			}
			out = append(out, abs)
		}
	}
	return out, nil
}

func inAny(path string, roots []string) bool {
	return slices.ContainsFunc(roots, func(r string) bool { return within(path, r) })
}

// linksTo splits the links outside the store that resolve to folder into
// those the plan removes and those it keeps because a read-only profile
// holds them.
func (p *Planner) linksTo(g graph.Graph, folder string) (remove []graph.Link, keep []string, err error) {
	profiles, err := p.profileRoots()
	if err != nil {
		return nil, nil, err
	}
	for _, l := range g.Links {
		if l.Status != graph.LinkOK || l.Folder != folder || within(l.Path, g.Store) {
			continue
		}
		if inAny(l.Path, profiles) {
			keep = append(keep, l.Path)
			continue
		}
		remove = append(remove, l)
	}
	return remove, keep, nil
}

// harnessesAt lists the harnesses whose link directory or a read root is
// the directory holding path, or holds it: a link in ~/.claude/skills/OFF/
// is Claude Code's to reload as much as one in ~/.claude/skills (L65).
func (p *Planner) harnessesAt(path string) []string {
	dir := filepath.Dir(path)
	var out []string
	for _, h := range p.d.Harnesses {
		dirs := []string{h.LinkDir}
		for _, r := range h.ReadRoots {
			dirs = append(dirs, r.Dir)
		}
		for _, rel := range dirs {
			if rel == "" {
				continue
			}
			if abs, err := p.d.Paths.Join(filepath.FromSlash(rel)); err == nil && within(dir, abs) {
				out = append(out, h.ID)
				break
			}
		}
	}
	return sortedUnique(out)
}

// change describes a link the plan removes or creates.
func (p *Planner) change(path, target string, broken bool) LinkChange {
	return LinkChange{Path: path, Target: target, Harnesses: p.harnessesAt(path), Broken: broken}
}

// checkParking refuses a parking area that a harness or a profile reads:
// a parked skill there would stay visible (docs/design-v2.md section 11,
// item 4).
func (p *Planner) checkParking() error {
	parked := p.d.Paths.ParkedDir()
	var roots []string
	for _, h := range p.d.Harnesses {
		for _, r := range h.ReadRoots {
			roots = append(roots, r.Dir)
		}
		if h.LinkDir != "" {
			roots = append(roots, h.LinkDir)
		}
	}
	for _, pr := range p.d.Profiles {
		for _, r := range pr.ReadRoots {
			roots = append(roots, r.Dir)
		}
	}
	for _, rel := range roots {
		abs, err := p.d.Paths.Join(filepath.FromSlash(rel))
		if err != nil {
			return err
		}
		if within(parked, abs) {
			return fmt.Errorf("%w: %s is inside %s", ErrParkingVisible, parked, abs)
		}
	}
	return nil
}

// exists tells whether something, a dangling link included, is at path.
func (p *Planner) exists(path string) (bool, error) {
	_, err := p.d.Stat.Mode(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	return true, nil
}

// harness finds a harness by id.
func (p *Planner) harness(id string) (data.Harness, error) {
	i := slices.IndexFunc(p.d.Harnesses, func(h data.Harness) bool { return h.ID == id })
	if i < 0 {
		return data.Harness{}, fmt.Errorf("%w: %s", ErrUnknownHarness, id)
	}
	return p.d.Harnesses[i], nil
}

// loadState reads skillhub's state.
func (p *Planner) loadState() (domain.PersistedState, error) {
	st, err := p.d.State.Load()
	if err != nil {
		return domain.PersistedState{}, fmt.Errorf("load the state: %w", err)
	}
	return st, nil
}
