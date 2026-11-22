// Package doctor derives the findings of Doctor (docs/design-v2.md sections 8
// and 11) from the identity and visibility graph. It only reads the graph it
// is given: no file access, no counters, no state, and no repairs, which
// belong to the planners of W3 to W6.
package doctor

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/graph"
	"skillhub/internal/shared/domain"
)

// Kind is the kind of a finding.
type Kind string

const (
	// BrokenLink is a link whose lexical target does not exist.
	BrokenLink Kind = "broken_link"
	// LinkCycle is a link chain that loops.
	LinkCycle Kind = "link_cycle"
	// LinkOutsideRoots is a link that leaves the known roots; it is not
	// followed.
	LinkOutsideRoots Kind = "link_outside_roots"
	// IdenticalCopies is a skill with real copies of its canonical folder.
	IdenticalCopies Kind = "identical_copies"
	// NoCanonical is a skill whose identical copies all live outside the
	// store.
	NoCanonical Kind = "no_canonical"
	// UnmanagedSkill is a skill with a single folder, outside the store,
	// and no origin.
	UnmanagedSkill Kind = "unmanaged_skill"
	// LockEntryWithoutFolder is a lock entry whose folder is not there.
	LockEntryWithoutFolder Kind = "lock_entry_without_folder"
	// LockNameMismatch is a lock key that differs from the frontmatter name
	// npx skills matches it by.
	LockNameMismatch Kind = "lock_name_mismatch"
	// ParkedButVisible is a skill parked in the store's OFF/ that harnesses
	// reading subfolders still load.
	ParkedButVisible Kind = "parked_but_visible"
	// Homonyms are several skills sharing a frontmatter name. They are
	// reported, never merged.
	Homonyms Kind = "homonyms"
	// ManagedByGentleAI is a folder gentle-ai manages; it is only reported.
	ManagedByGentleAI Kind = "managed_by_gentle_ai"
)

// order is the order findings are listed in.
var order = []Kind{
	BrokenLink, LinkCycle, LinkOutsideRoots, IdenticalCopies, NoCanonical, UnmanagedSkill,
	LockEntryWithoutFolder, LockNameMismatch, ParkedButVisible, Homonyms, ManagedByGentleAI,
}

// Finding is one problem with its evidence. ID is stable: the kind, a colon
// and the skill id, the name or the home-relative path it is about.
type Finding struct {
	ID   string
	Kind Kind
	// Skill is the id of the skill it is about, if any.
	Skill   string
	Name    string
	LockKey string
	// Paths are the folders or links involved.
	Paths []string
	// Target is the lexical target of a link.
	Target string
	// Related are other paths worth showing, such as the parked folder a
	// broken link once pointed at.
	Related []string
	// Harnesses are the harnesses involved, sorted.
	Harnesses []string
	// Skills are the skills involved, sorted.
	Skills []string
}

// Detect lists every finding of g, by kind in a fixed order and then by id.
func Detect(g graph.Graph) []Finding {
	out := []Finding{}
	rel := func(p string) string {
		if r, err := filepath.Rel(g.Home, p); err == nil && filepath.IsLocal(r) {
			return filepath.ToSlash(r)
		}
		return p
	}
	entries := map[string]bool{}
	entryOf := map[string]string{}
	for _, f := range g.Folders {
		entries[f.Entry] = true
		entryOf[f.Path] = f.Entry
	}

	linkKinds := map[graph.LinkStatus]Kind{graph.LinkBroken: BrokenLink, graph.LinkCycle: LinkCycle, graph.LinkOutside: LinkOutsideRoots}
	for _, l := range g.Links {
		kind, ok := linkKinds[l.Status]
		if !ok {
			continue
		}
		f := Finding{ID: string(kind) + ":" + rel(l.Path), Kind: kind, Paths: []string{l.Path}, Target: l.Final}
		if kind == BrokenLink && filepath.Dir(l.Final) == g.Store {
			parked := filepath.Join(g.Store, "OFF", filepath.Base(l.Final))
			if entries[parked] {
				f.Related = []string{parked}
			}
		}
		out = append(out, f)
	}

	byName := map[string][]string{}
	for _, s := range g.Skills {
		if s.Name != "" {
			byName[s.Name] = append(byName[s.Name], s.ID)
		}
		about := func(kind Kind, paths []string) Finding {
			return Finding{ID: string(kind) + ":" + s.ID, Kind: kind, Skill: s.ID, Name: s.Name, LockKey: s.LockKey, Paths: paths}
		}
		manageable := s.Zone == graph.ZoneNone
		switch {
		case manageable && s.Canonical != "" && len(s.Folders) > 1:
			out = append(out, about(IdenticalCopies, slices.Clone(s.Folders)))
		case manageable && s.Canonical == "" && s.Origin == domain.OriginNone && len(s.Folders) > 1:
			out = append(out, about(NoCanonical, slices.Clone(s.Folders)))
		case manageable && s.Canonical == "" && s.Origin == domain.OriginNone && len(s.Folders) == 1:
			out = append(out, about(UnmanagedSkill, slices.Clone(s.Folders)))
		}
		if s.LockKey != "" && s.Canonical == "" {
			out = append(out, about(LockEntryWithoutFolder, nil))
		}
		if s.LockKey != "" && s.Name != "" && s.Name != s.LockKey {
			out = append(out, about(LockNameMismatch, []string{s.Canonical}))
		}
		if s.Canonical != "" && strings.HasPrefix(entryOf[s.Canonical], filepath.Join(g.Store, "OFF")+string(filepath.Separator)) {
			var hs []string
			for _, v := range s.Visibility {
				if (v.Kind == graph.Direct || v.Kind == graph.ViaLink) && !slices.Contains(hs, v.Harness) {
					hs = append(hs, v.Harness)
				}
			}
			if len(hs) > 0 {
				slices.Sort(hs)
				f := about(ParkedButVisible, []string{s.Canonical})
				f.Harnesses = hs
				out = append(out, f)
			}
		}
	}
	for name, skills := range byName {
		if len(skills) > 1 {
			slices.Sort(skills)
			out = append(out, Finding{ID: string(Homonyms) + ":" + name, Kind: Homonyms, Name: name, Skills: skills})
		}
	}
	for _, b := range g.Boundary {
		if b.Zone == graph.ZoneGentle {
			out = append(out, Finding{ID: string(ManagedByGentleAI) + ":" + rel(b.Path), Kind: ManagedByGentleAI, Paths: []string{b.Path}})
		}
	}

	slices.SortFunc(out, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(slices.Index(order, a.Kind), slices.Index(order, b.Kind)), cmp.Compare(a.ID, b.ID))
	})
	return out
}
