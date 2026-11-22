package toggle

import (
	"cmp"
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"

	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// linkDir is the absolute link directory of an administrable harness.
func (p *Planner) linkDir(id string) (data.Harness, string, error) {
	h, err := p.harness(id)
	if err != nil {
		return data.Harness{}, "", err
	}
	dir, err := LinkDir(p.d.Paths, h)
	if err != nil {
		return data.Harness{}, "", err
	}
	return h, dir, nil
}

// LinkDir is the absolute link directory of h. A harness without one (a
// universal harness, which reads the store) is ErrNotAdministrable.
func LinkDir(p paths.Paths, h data.Harness) (string, error) {
	if h.LinkDir == "" {
		return "", fmt.Errorf("%w: %s", ErrNotAdministrable, h.ID)
	}
	dir, err := p.Join(filepath.FromSlash(h.LinkDir))
	if err != nil {
		return "", fmt.Errorf("harness %s: %w", h.ID, err)
	}
	return dir, nil
}

// ErrLinkedLinkDir reports a link directory below a symlinked directory of
// the home when the planner has no DirLister to read its real path: a link
// relative to the unresolved path would be broken, so none is planned.
var ErrLinkedLinkDir = errors.New("the link directory is below a symlink and its real path cannot be read")

// Resolver reads the real path of a link directory with read-only ports.
type Resolver struct {
	Paths paths.Paths
	// Dirs lists the parent of the link directory, whose entry carries the
	// directory's real path. Without it, RealDir accepts only a directory
	// with no symlink between it and the home, where the real path and the
	// lexical one give the same relative link, and refuses any other with
	// ErrLinkedLinkDir.
	Dirs DirLister
	Stat Stat
}

// RealDir is the real path of the directory dir, every symlink resolved,
// as realpath(3) gives it. A dir that is not there, or a dangling link,
// fails with fs.ErrNotExist.
func (r Resolver) RealDir(dir string) (string, error) {
	dir = filepath.Clean(dir)
	if r.Dirs == nil {
		return r.lexicalDir(dir)
	}
	entries, err := r.Dirs.Scan(filepath.Dir(dir))
	if err != nil {
		return "", fmt.Errorf("read the real path of %s: %w", dir, err)
	}
	i := slices.IndexFunc(entries, func(e ports.DirEntry) bool { return filepath.Clean(e.Path) == dir })
	if i < 0 || entries[i].ResolvedPath == "" {
		return "", fmt.Errorf("read the real path of %s: %w", dir, iofs.ErrNotExist)
	}
	return entries[i].ResolvedPath, nil
}

// lexicalDir accepts dir as its own real path when no level between it and
// the home is a symlink; levels above the home are shared with the store, so
// they do not change a link relative to dir.
func (r Resolver) lexicalDir(dir string) (string, error) {
	home := r.Paths.Home()
	for d := dir; d != home && within(d, home); d = filepath.Dir(d) {
		mode, err := r.Stat.Mode(d)
		if errors.Is(err, iofs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("look at %s: %w", d, err)
		}
		if mode&iofs.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %s is a symlink", ErrLinkedLinkDir, d)
		}
	}
	return dir, nil
}

// StoreLink is the link npx skills writes for the store folder of key in the
// link directory dir: dir/key, whose content is the store folder relative to
// the real path of dir, as createSymlink computes it:
// relative(realpath(linkDir), target) (remove.mjs:2882-2883). With a
// symlinked ancestor, such as ~/.claude managed with stow, a link relative to
// the unresolved dir would be broken. Link, install and repair create exactly
// this link.
func StoreLink(r Resolver, dir, key string) (path, target string, err error) {
	path = filepath.Join(dir, key)
	real, err := r.RealDir(dir)
	if err != nil {
		return "", "", fmt.Errorf("link %s: %w", path, err)
	}
	target, err = filepath.Rel(real, filepath.Join(r.Paths.StoreDir(), key))
	if err != nil {
		return "", "", fmt.Errorf("link %s: %w", path, err)
	}
	return path, target, nil
}

// resolver is the planner's Resolver.
func (p *Planner) resolver() Resolver {
	return Resolver{Paths: p.d.Paths, Dirs: p.d.Dirs, Stat: p.d.Stat}
}

// linksIn are the links directly in dir that resolve to folder.
func linksIn(g graph.Graph, dir, folder string) []graph.Link {
	var out []graph.Link
	for _, l := range g.Links {
		if l.Status == graph.LinkOK && l.Folder == folder && filepath.Dir(l.Path) == dir {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(x, y graph.Link) int { return cmp.Compare(x.Path, y.Path) })
	return out
}

// sightings are the ways harness sees s, leaving out the entries in skip.
func sightings(s graph.Skill, harness string, skip []string) []graph.Sighting {
	var out []graph.Sighting
	for _, v := range s.Visibility {
		if v.Harness == harness && !slices.Contains(skip, v.Entry) {
			out = append(out, v)
		}
	}
	return out
}

// targetsEffect updates Managed[key].Targets with change applied, when
// skillhub manages the skill; a skill it does not manage gets no record.
func targetsEffect(st domain.PersistedState, key string, change func([]string) []string) engine.Effect {
	m, ok := st.Managed[key]
	if !ok {
		return engine.Effect{}
	}
	m.Targets = change(slices.Clone(m.Targets))
	if m.Targets == nil {
		m.Targets = []string{}
	}
	return engine.Effect{SetManaged: []domain.ManagedSkill{m}}
}

// Unlink plans removing the links of harness to the skill of key, only where
// the harness has a link directory (decision 3). The preview tells how the
// harness still sees the skill afterwards, as the graph observed it, and
// which other harnesses read the same directory.
func (p *Planner) Unlink(g graph.Graph, key, harness string) (Proposal, error) {
	if err := validKey(key); err != nil {
		return Proposal{}, err
	}
	h, dir, err := p.linkDir(harness)
	if err != nil {
		return Proposal{}, err
	}
	a, err := p.activeSkill(g, key)
	if err != nil {
		return Proposal{}, err
	}
	links := linksIn(g, dir, a.folder)
	if len(links) == 0 {
		return Proposal{}, fmt.Errorf("%w: %s in %s", ErrNotLinked, key, harness)
	}
	st, err := p.loadState()
	if err != nil {
		return Proposal{}, err
	}

	change := SkillChange{Key: key}
	var removed []string
	b := p.d.Plans.NewBuilder(engine.Meta{Action: string(ActionUnlink), Targets: []string{harness}, Canonical: a.store})
	for _, l := range links {
		change.RemovedLinks = append(change.RemovedLinks, p.change(l.Path, l.Text, false))
		removed = append(removed, l.Path)
		_ = b.Unlink(l.Path, l.Text) // Build reports the first refused step
	}
	eff := targetsEffect(st, key, func(ts []string) []string {
		return slices.DeleteFunc(ts, func(t string) bool { return t == harness })
	})
	if err := b.SetEffect(eff); err != nil {
		return Proposal{}, err
	}
	return p.build(b, Preview{
		Action: ActionUnlink, Skills: []SkillChange{change}, Harness: harness,
		StillVisible: sightings(a.skill, harness, removed), SharesDirWith: slices.Clone(h.SharesDirWith),
	})
}

// Link plans linking the skill of key in harness, where the harness has a
// link directory and is on the machine: <link_dir>/<key>, with the relative
// target npx skills writes. The preview tells how the harness already sees
// the skill.
func (p *Planner) Link(g graph.Graph, key, harness string) (Proposal, error) {
	if err := validKey(key); err != nil {
		return Proposal{}, err
	}
	h, dir, err := p.linkDir(harness)
	if err != nil {
		return Proposal{}, err
	}
	if !slices.Contains(g.Harnesses, harness) {
		return Proposal{}, fmt.Errorf("%w: %s", ErrHarnessNotDetected, harness)
	}
	a, err := p.activeSkill(g, key)
	if err != nil {
		return Proposal{}, err
	}
	if existing := linksIn(g, dir, a.folder); len(existing) > 0 {
		return Proposal{}, fmt.Errorf("%w: %s", ErrAlreadyLinked, existing[0].Path)
	}
	path, target, err := StoreLink(p.resolver(), dir, key)
	if err != nil {
		return Proposal{}, err
	}
	taken, err := p.exists(path)
	if err != nil {
		return Proposal{}, err
	}
	if taken {
		return Proposal{}, fmt.Errorf("%w: %s", ErrPathTaken, path)
	}
	st, err := p.loadState()
	if err != nil {
		return Proposal{}, err
	}

	change := SkillChange{Key: key, CreatedLinks: []LinkChange{p.change(path, target, false)}}
	b := p.d.Plans.NewBuilder(engine.Meta{Action: string(ActionLink), Targets: []string{harness}, Canonical: a.store})
	_ = b.Link(path, target) // Build reports a refused step
	eff := targetsEffect(st, key, func(ts []string) []string {
		if !slices.Contains(ts, harness) {
			ts = append(ts, harness)
		}
		slices.Sort(ts)
		return ts
	})
	if err := b.SetEffect(eff); err != nil {
		return Proposal{}, err
	}
	return p.build(b, Preview{
		Action: ActionLink, Skills: []SkillChange{change}, Harness: harness,
		Visible: sightings(a.skill, harness, nil), SharesDirWith: slices.Clone(h.SharesDirWith),
	})
}
