package repair

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/data"
	"skillhub/internal/graph"
)

// validKey accepts a single folder name.
func validKey(key string) bool {
	return key != "" && key != "." && key != ".." && !strings.ContainsRune(key, filepath.Separator) &&
		!strings.ContainsRune(key, '/') && filepath.Base(key) == key
}

// within reports whether p is root or below it.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

func sortedUnique(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// abs is a home-relative directory of the agents table, made absolute.
func (p *Planner) abs(rel string) (string, error) {
	return p.d.Paths.Join(filepath.FromSlash(rel))
}

// harnessesAt lists the harnesses whose link directory or a read root is the
// directory holding path.
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
			if abs, err := p.abs(rel); err == nil && abs == dir {
				out = append(out, h.ID)
				break
			}
		}
	}
	return sortedUnique(out)
}

// linkHarnesses lists the harnesses whose link directory is dir.
func (p *Planner) linkHarnesses(dir string) []string {
	var out []string
	for _, h := range p.d.Harnesses {
		if h.LinkDir == "" {
			continue
		}
		if abs, err := p.abs(h.LinkDir); err == nil && abs == dir {
			out = append(out, h.ID)
		}
	}
	return sortedUnique(out)
}

// readsStore reports whether harness id is known to read the store.
func (p *Planner) readsStore(id string) bool {
	i := slices.IndexFunc(p.d.Harnesses, func(h data.Harness) bool { return h.ID == id })
	return i >= 0 && p.d.Harnesses[i].ReadsStore == data.Yes
}

// profileRoots are the read roots of the read-only diagnostic profiles.
func (p *Planner) profileRoots() ([]string, error) {
	var out []string
	for _, pr := range p.d.Profiles {
		for _, r := range pr.ReadRoots {
			abs, err := p.abs(r.Dir)
			if err != nil {
				return nil, fmt.Errorf("profile %s: %w", pr.ID, err)
			}
			out = append(out, abs)
		}
	}
	return out, nil
}

// inProfile reports whether path is in a read-only profile.
func (p *Planner) inProfile(path string) (bool, error) {
	roots, err := p.profileRoots()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(roots, func(r string) bool { return within(path, r) }), nil
}

// foreign reports whether path is, holds or lies in something the graph met
// as a boundary (a provider folder, a plugin, _shared, a gentle-ai copy, a
// loose file), or lies in a read-only profile.
func (p *Planner) foreign(g graph.Graph, path string) (bool, error) {
	for _, b := range g.Boundary {
		if within(path, b.Path) || within(b.Path, path) {
			return true, nil
		}
	}
	return p.inProfile(path)
}

// checkTouch refuses touching path when it is foreign.
func (p *Planner) checkTouch(g graph.Graph, path string) error {
	bad, err := p.foreign(g, path)
	if err != nil {
		return err
	}
	if bad {
		return fmt.Errorf("%w: %s", ErrForeignZone, path)
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

func skillByID(g graph.Graph, id string) (graph.Skill, bool) {
	i := slices.IndexFunc(g.Skills, func(s graph.Skill) bool { return s.ID == id })
	if i < 0 {
		return graph.Skill{}, false
	}
	return g.Skills[i], true
}

func folderByPath(g graph.Graph, path string) (graph.Folder, bool) {
	i := slices.IndexFunc(g.Folders, func(f graph.Folder) bool { return f.Path == path })
	if i < 0 {
		return graph.Folder{}, false
	}
	return g.Folders[i], true
}

func folderByEntry(g graph.Graph, entry string) (graph.Folder, bool) {
	i := slices.IndexFunc(g.Folders, func(f graph.Folder) bool { return f.Entry == entry })
	if i < 0 {
		return graph.Folder{}, false
	}
	return g.Folders[i], true
}

// claims lists the skills whose canonical folder is folder.
func claims(g graph.Graph, folder string) []graph.Skill {
	var out []graph.Skill
	for _, s := range g.Skills {
		if s.Canonical == folder {
			out = append(out, s)
		}
	}
	return out
}

// activeStore finds the active skill of key in the store: a real entry
// <store>/<key> that one manageable skill alone claims.
func activeStore(g graph.Graph, key string) (graph.Folder, graph.Skill, bool) {
	f, ok := folderByEntry(g, filepath.Join(g.Store, key))
	if !ok {
		return graph.Folder{}, graph.Skill{}, false
	}
	cs := claims(g, f.Path)
	if len(cs) != 1 || cs[0].Zone != graph.ZoneNone || len(cs[0].SharedWith) > 0 {
		return graph.Folder{}, graph.Skill{}, false
	}
	return f, cs[0], true
}
