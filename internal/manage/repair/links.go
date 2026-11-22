package repair

import (
	"fmt"
	"path/filepath"
	"slices"

	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
)

// Actions of the plans this package builds; the journal names them.
const (
	actionDeleteLink    = "repair-delete-link"
	actionRepointLink   = "repair-repoint-link"
	actionEnableAndLink = "repair-enable-and-link"
	actionAdopt         = "repair-adopt"
	actionParkEntry     = "repair-park-entry"
)

// brokenLink is the graph's link of a broken_link finding.
func brokenLink(g graph.Graph, f doctor.Finding) (graph.Link, error) {
	if len(f.Paths) != 1 {
		return graph.Link{}, fmt.Errorf("%w: %s lists %d paths", ErrNotOffered, f.ID, len(f.Paths))
	}
	i := slices.IndexFunc(g.Links, func(l graph.Link) bool { return l.Path == f.Paths[0] })
	if i < 0 || g.Links[i].Status != graph.LinkBroken {
		return graph.Link{}, fmt.Errorf("%w: %s is not a broken link", ErrNotOffered, f.Paths[0])
	}
	return g.Links[i], nil
}

// linkOffer: a broken link towards a skill moved to OFF/ goes with adopting
// OFF/, and nothing else is offered for it (toggle's AdoptOff removes it).
// Any other one can be deleted, re-pointed at the active store skill of its
// key, or followed by enabling the parked skill of its key. The key is the
// link's name, which is where npx skills links <store>/<key>.
func (p *Planner) linkOffer(g graph.Graph, st domain.PersistedState, f doctor.Finding) (Offer, error) {
	l, err := brokenLink(g, f)
	if err != nil {
		return Offer{}, err
	}
	if len(f.Related) > 0 {
		return Offer{Repairs: []Kind{AdoptOff}}, nil
	}
	prof, err := p.inProfile(l.Path)
	if err != nil {
		return Offer{}, err
	}
	if prof {
		return Offer{Reason: ReasonDiagnosticProfile}, nil
	}
	if bad, err := p.foreign(g, l.Path); err != nil || bad {
		return Offer{Reason: ReasonForeignZone}, err
	}
	out := []Kind{DeleteLink}
	key := filepath.Base(l.Path)
	if validKey(key) {
		if _, ok := st.Parked[key]; ok {
			out = append(out, EnableAndLink)
		} else if _, _, ok := activeStore(g, key); ok {
			out = append(out, RepointLink)
		}
	}
	return Offer{Repairs: out}, nil
}

// deleteLink removes the broken link, whose exact text is the precondition.
func (p *Planner) deleteLink(g graph.Graph, f doctor.Finding, pv Preview) (Proposal, error) {
	l, err := brokenLink(g, f)
	if err != nil {
		return Proposal{}, err
	}
	lc := LinkChange{Path: l.Path, Target: l.Text, Harnesses: p.harnessesAt(l.Path), Broken: true}
	pv.RemovedLinks = []LinkChange{lc}
	b := p.d.Plans.NewBuilder(engine.Meta{Action: actionDeleteLink, Targets: lc.Harnesses})
	_ = b.Unlink(l.Path, l.Text) // Build reports a refused step
	return build(b, pv)
}

// resolver reads the real path of a link directory.
func (p *Planner) resolver() toggle.Resolver {
	return toggle.Resolver{Paths: p.d.Paths, Dirs: p.d.Dirs, Stat: p.d.Stat}
}

// relink adds the steps that replace the broken link l by the link npx
// skills writes for <store>/<key> in the same directory, and returns both
// changes.
func (p *Planner) relink(b *engine.Builder, l graph.Link, key string) (removed, created LinkChange, err error) {
	path, target, err := toggle.StoreLink(p.resolver(), filepath.Dir(l.Path), key)
	if err != nil {
		return LinkChange{}, LinkChange{}, err
	}
	removed = LinkChange{Path: l.Path, Target: l.Text, Harnesses: p.harnessesAt(l.Path), Broken: true}
	created = LinkChange{Path: path, Target: target, Harnesses: p.harnessesAt(path)}
	_ = b.Unlink(l.Path, l.Text) // Build reports the first refused step
	_ = b.Link(path, target)
	return removed, created, nil
}

// repoint points the broken link at the active store skill of its key, with
// the relative text npx writes. When skillhub manages the skill, the link's
// harnesses join its targets.
func (p *Planner) repoint(g graph.Graph, st domain.PersistedState, f doctor.Finding, pv Preview) (Proposal, error) {
	l, err := brokenLink(g, f)
	if err != nil {
		return Proposal{}, err
	}
	key := filepath.Base(l.Path)
	store := filepath.Join(g.Store, key)
	pv.Key, pv.Canonical = key, store
	targets := p.linkHarnesses(filepath.Dir(l.Path))
	folder, _, ok := activeStore(g, key)
	if !ok {
		return Proposal{}, &StaleError{ID: f.ID}
	}
	b := p.d.Plans.NewBuilder(engine.Meta{Action: actionRepointLink, Targets: targets, Canonical: store})
	// The skill the link will lead to must stay exactly as it was shown.
	_ = b.Require(engine.Image{Path: store, State: engine.StateTree, TreeSHA: folder.TreeSHA})
	removed, created, err := p.relink(b, l, key)
	if err != nil {
		return Proposal{}, err
	}
	pv.RemovedLinks, pv.CreatedLinks = []LinkChange{removed}, []LinkChange{created}
	if m, ok := st.Managed[key]; ok {
		m.Targets = sortedUnique(append(slices.Clone(m.Targets), targets...))
		if m.Targets == nil {
			m.Targets = []string{}
		}
		if err := b.SetEffect(engine.Effect{SetManaged: []domain.ManagedSkill{m}}); err != nil {
			return Proposal{}, err
		}
	}
	return build(b, pv)
}

// enableAndLink enables the parked skill of the link's key, as toggle's
// Enable plans it, and leaves the link leading to it, in one plan
// (docs/design-v2.md section 11, item 4).
//
// When enabling recreates a link at the broken link's path, Enable is
// planned with that path freed first: a link with the very text enabling
// writes stays, as a precondition, and any other one is replaced in place.
// Otherwise a link whose text already leads to <store>/<key> needs nothing
// more, and any other one is replaced by the npx link.
func (p *Planner) enableAndLink(g graph.Graph, st domain.PersistedState, f doctor.Finding, pv Preview) (Proposal, error) {
	l, err := brokenLink(g, f)
	if err != nil {
		return Proposal{}, err
	}
	key := filepath.Base(l.Path)
	ri := slices.IndexFunc(st.Parked[key].Links, func(r domain.LinkRecord) bool { return r.Path == l.Path })
	recreated := ri >= 0
	keep := recreated && st.Parked[key].Links[ri].Target == l.Text
	var enable toggle.Proposal
	if recreated {
		enable, err = p.seeded(func(b *engine.Builder) error { return b.Unlink(l.Path, l.Text) }).Enable(key)
	} else {
		enable, err = p.d.Toggle.Enable(key)
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("enable %s: %w", key, err)
	}
	if slices.ContainsFunc(enable.Plan.Effect.SetParked, func(e engine.ParkedEffect) bool { return e.EntryStep >= 0 }) {
		// Enable's effect names no step; a composed plan could not keep one.
		return Proposal{}, fmt.Errorf("%w: enable %s names a step in its effect", engine.ErrInvalidStep, key)
	}
	store := filepath.Join(g.Store, key)
	meta := engine.Meta{Action: actionEnableAndLink, Targets: slices.Clone(enable.Plan.Targets), Canonical: enable.Plan.Canonical}
	relinked := !recreated && l.Final != store
	if relinked {
		meta.Targets = sortedUnique(append(meta.Targets, p.linkHarnesses(filepath.Dir(l.Path))...))
	}
	b := p.d.Plans.NewBuilder(meta)
	steps := enable.Plan.Steps
	switch {
	case keep:
		steps = steps[1:] // the seed unlink: the link stays
		pv.Kept = []string{l.Path}
	case recreated:
		r := st.Parked[key].Links[ri]
		pv.RemovedLinks = []LinkChange{{Path: l.Path, Target: l.Text, Harnesses: p.harnessesAt(l.Path), Broken: true}}
		pv.CreatedLinks = []LinkChange{{Path: r.Path, Target: r.Target, Harnesses: p.harnessesAt(r.Path)}}
	}
	err = replaySteps(b, enable.Plan.Action, steps, func(s engine.Step) (bool, error) {
		if keep && s.Kind == engine.StepLink && s.Path == l.Path {
			return true, b.Require(engine.Image{Path: l.Path, State: engine.StateLink, Target: l.Text})
		}
		return false, nil
	})
	if err != nil {
		return Proposal{}, err
	}
	if relinked {
		removed, created, err := p.relink(b, l, key)
		if err != nil {
			return Proposal{}, err
		}
		pv.RemovedLinks, pv.CreatedLinks = []LinkChange{removed}, []LinkChange{created}
	}
	if err := b.SetEffect(enable.Plan.Effect); err != nil {
		return Proposal{}, err
	}
	tp := enable.Preview
	tp.PlanID = "" // the plan run is this one, in Preview.PlanID
	pv.Key, pv.Canonical, pv.Toggle = key, store, &tp
	return build(b, pv)
}

// seeded is toggle's planner over builders that start with seed's steps, so
// a planner can plan around a path the repair frees first. A seed that is
// refused makes the planner's Build fail with it.
func (p *Planner) seeded(seed func(*engine.Builder) error) *toggle.Planner {
	return toggle.New(toggle.Deps{
		Paths: p.d.Paths, Harnesses: p.d.Harnesses, Profiles: p.d.Profiles,
		Plans: seededPlans{Plans: p.d.Plans, seed: seed}, State: p.d.State, Lock: p.d.Lock, Stat: p.d.Stat,
	})
}

type seededPlans struct {
	Plans
	seed func(*engine.Builder) error
}

func (s seededPlans) NewBuilder(meta engine.Meta) *engine.Builder {
	b := s.Plans.NewBuilder(meta)
	_ = s.seed(b) // a refused seed fails every later call and Build
	return b
}

// adoptOff is toggle's AdoptOff, unchanged.
func (p *Planner) adoptOff(g graph.Graph, pv Preview) (Proposal, error) {
	pr, err := p.d.Toggle.AdoptOff(g)
	if err != nil {
		return Proposal{}, fmt.Errorf("adopt OFF/: %w", err)
	}
	pv.PlanID = pr.Plan.ID
	tp := pr.Preview
	pv.Toggle = &tp
	return Proposal{Plan: pr.Plan, Preview: pv}, nil
}
