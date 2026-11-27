package remove

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
)

// RemovedFolder is one eligible global folder removed by RemoveGroup.
type RemovedFolder struct {
	Path      string
	TreeSHA   string
	Off       bool
	InStore   bool
	Harnesses []string
}

// KeptCopy is a copy attributed to a group that RemoveGroup leaves untouched
// because it is protected or unproven, with the reason it was excluded.
type KeptCopy struct {
	Path      string
	Name      string
	TreeSHA   string
	Harnesses []string
	Profile   string
	Reason    string
}

// RemoveGroup plans the atomic removal of all eligible global folders and
// safely owned links attributed to the graph skill skillID, while preserving
// protected/unproven copies, independent same-name homonyms, projects and
// providers.
func (r *Remover) RemoveGroup(skillID string) (Plan, error) {
	return r.RemoveGroupWithOptions(skillID, Options{})
}

// RemoveGroupWithOptions plans a grouped removal with explicit options.
func (r *Remover) RemoveGroupWithOptions(skillID string, opts Options) (Plan, error) {
	if skillID == "" {
		return Plan{}, fmt.Errorf("%w: empty skill id", ErrInvalidKey)
	}
	st, err := r.d.State.Load()
	if err != nil {
		return Plan{}, fmt.Errorf("read the state: %w", err)
	}
	g, err := r.d.Observe()
	if err != nil {
		return Plan{}, fmt.Errorf("observe the skills: %w", err)
	}
	profiles := r.profileRoots()
	o := &observation{
		r:             r,
		g:             g,
		st:            st,
		profiles:      profiles,
		harnesses:     r.harnessRoots(profiles),
		allowProvider: opts.AllowProvider,
	}
	return o.removeGroup(skillID)
}

func (o *observation) removeGroup(skillID string) (Plan, error) {
	idx := slices.IndexFunc(o.g.Skills, func(s graph.Skill) bool { return s.ID == skillID })
	if idx < 0 {
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, skillID)
	}
	skill := o.g.Skills[idx]
	if len(skill.SharedWith) > 0 {
		return Plan{}, fmt.Errorf("%w: several skills claim %s", ErrAmbiguous, skillID)
	}

	candidates := groupCandidatePaths(skill)
	if len(candidates) == 0 {
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, skillID)
	}

	store := o.r.d.Paths.StoreDir()
	off := o.offDir()

	var eligible []graph.Folder
	var keptCopies []KeptCopy
	var firstRefusal error
	setRefusal := func(err error) {
		if firstRefusal == nil {
			firstRefusal = err
		}
	}

	seenFolders := map[string]bool{}
	for _, folderPath := range candidates {
		fi := slices.IndexFunc(o.g.Folders, func(f graph.Folder) bool { return f.Path == folderPath })
		if fi < 0 {
			continue
		}
		f := o.g.Folders[fi]
		if seenFolders[f.Path] {
			continue
		}
		seenFolders[f.Path] = true

		reason, err := o.checkGroupFolderEligibility(skill, f)
		if err != nil {
			setRefusal(err)
			keptCopies = append(keptCopies, KeptCopy{
				Path:      f.Entry,
				Name:      f.Name,
				TreeSHA:   f.TreeSHA,
				Harnesses: o.harnessesList(f.Sightings, ""),
				Profile:   o.profileOf(f.Entry),
				Reason:    reason,
			})
			continue
		}
		eligible = append(eligible, f)
	}

	if len(eligible) == 0 {
		if firstRefusal != nil {
			return Plan{}, firstRefusal
		}
		return Plan{}, fmt.Errorf("%w: %s", ErrNotFound, skillID)
	}

	// For a lock-backed skill, any non-canonical harness copy is eligible only
	// when the canonical folder itself is eligible and lock ownership is proven.
	if skill.LockKey != "" {
		canonicalEligible := skill.Canonical != "" && slices.ContainsFunc(eligible, func(f graph.Folder) bool { return f.Path == skill.Canonical })
		var lockProven bool
		if canonicalEligible {
			if _, _, lerr := o.lockEntry(skill.LockKey); lerr != nil {
				setRefusal(lerr)
			} else {
				lockProven = true
			}
		}
		if !canonicalEligible || !lockProven {
			var filtered []graph.Folder
			for _, f := range eligible {
				if f.Path != skill.Canonical {
					keptCopies = append(keptCopies, KeptCopy{
						Path:      f.Entry,
						Name:      f.Name,
						TreeSHA:   f.TreeSHA,
						Harnesses: o.harnessesList(f.Sightings, ""),
						Profile:   o.profileOf(f.Entry),
						Reason:    "ambiguous",
					})
					setRefusal(fmt.Errorf("%w: %s is claimed by lock entry %s without an eligible canonical folder", ErrAmbiguous, f.Entry, skill.LockKey))
					continue
				}
				filtered = append(filtered, f)
			}
			eligible = filtered
			if len(eligible) == 0 {
				return Plan{}, firstRefusal
			}
		}
	}

	slices.SortFunc(eligible, func(a, b graph.Folder) int {
		return strings.Compare(a.Entry, b.Entry)
	})
	slices.SortFunc(keptCopies, func(a, b KeptCopy) int {
		return strings.Compare(a.Path, b.Path)
	})

	keyName := skill.Name
	if keyName == "" {
		keyName = filepath.Base(eligible[0].Entry)
	}
	pv := Preview{
		Key:        keyName,
		SkillName:  keyName,
		Group:      true,
		SkillID:    skillID,
		KeptCopies: keptCopies,
		Modified:   Modification{State: ModifiedUnknown, Actual: eligible[0].TreeSHA},
	}
	if o.allowProvider {
		for _, f := range eligible {
			pz, pe := o.providerInfo(f.Entry, f.Path, []graph.Skill{skill})
			if isAllowedProviderZone(pz) {
				pv.ProviderZone = pz
				pv.ProviderEntry = pe
				break
			}
		}
	}

	var eff engine.Effect
	seenLinks := map[string]Link{}
	seenKeptLinks := map[string]KeptLink{}
	removedPaths := map[string]bool{}
	var removedHarnesses []string

	for _, f := range eligible {
		removedPaths[f.Path] = true
		removedPaths[f.Entry] = true
		parent := filepath.Dir(f.Entry)
		inStore := parent == store
		isOff := parent == off
		fh := o.harnessesList(f.Sightings, "")
		pv.Folders = append(pv.Folders, RemovedFolder{
			Path:      f.Entry,
			TreeSHA:   f.TreeSHA,
			Off:       isOff,
			InStore:   inStore,
			Harnesses: fh,
		})
		for _, h := range fh {
			if !slices.Contains(removedHarnesses, h) {
				removedHarnesses = append(removedHarnesses, h)
			}
		}

		links, kept, err := o.links(func(l graph.Link) bool { return l.Status == graph.LinkOK && l.Folder == f.Path }, f)
		if err != nil {
			return Plan{}, err
		}
		for _, l := range links {
			if prev, ok := seenLinks[l.Path]; ok {
				for _, h := range l.Harnesses {
					if !slices.Contains(prev.Harnesses, h) {
						prev.Harnesses = append(prev.Harnesses, h)
					}
				}
				slices.Sort(prev.Harnesses)
				seenLinks[l.Path] = prev
			} else {
				seenLinks[l.Path] = l
			}
			for _, h := range l.Harnesses {
				if !slices.Contains(removedHarnesses, h) {
					removedHarnesses = append(removedHarnesses, h)
				}
			}
		}
		for _, k := range kept {
			seenKeptLinks[k.Path] = k
		}
		var tmp Preview
		if err := o.keepZoneLinks(&tmp, f.Path); err != nil {
			return Plan{}, err
		}
		for _, k := range tmp.KeptLinks {
			seenKeptLinks[k.Path] = k
		}

		if inStore {
			base := filepath.Base(f.Entry)
			if !slices.Contains(eff.DropManaged, base) {
				eff.DropManaged = append(eff.DropManaged, base)
			}
		}
	}

	for _, l := range seenLinks {
		pv.Links = append(pv.Links, l)
	}
	slices.SortFunc(pv.Links, func(a, b Link) int { return strings.Compare(a.Path, b.Path) })
	for _, k := range seenKeptLinks {
		pv.KeptLinks = append(pv.KeptLinks, k)
	}
	slices.SortFunc(pv.KeptLinks, func(a, b KeptLink) int { return strings.Compare(a.Path, b.Path) })

	// Lock entry and open update candidates apply only when the canonical folder
	// itself is among the eligible removed folders.
	if skill.LockKey != "" && skill.Canonical != "" && removedPaths[skill.Canonical] {
		lockEnt, last, err := o.lockEntry(skill.LockKey)
		if err != nil {
			return Plan{}, err
		}
		pv.LockEntry = &LockEntry{Name: lockEnt.Name, Raw: slices.Clone(lockEnt.Raw), DeletesLock: last}
		for _, f := range eligible {
			if f.Path == skill.Canonical {
				pv.Modified = modification(lockEnt.SkillFolderHash, f.TreeSHA)
				break
			}
		}
		pv.PendingCandidates = o.openCandidates(skill.ID)
	}

	// Collect independent same-name homonyms that were not attributed to this group.
	seenHomonyms := map[string]bool{}
	for _, f := range eligible {
		for _, h := range o.homonyms(f.Path, f.Name, f.TreeSHA, filepath.Base(f.Entry)) {
			if seenFolders[h.Path] || removedPaths[h.Path] || seenHomonyms[h.Path] {
				continue
			}
			// Also skip if h.Path is the entry of any folder in seenFolders.
			if slices.ContainsFunc(o.g.Folders, func(gf graph.Folder) bool { return seenFolders[gf.Path] && gf.Entry == h.Path }) {
				continue
			}
			seenHomonyms[h.Path] = true
			pv.Homonyms = append(pv.Homonyms, h)
		}
	}
	slices.SortFunc(pv.Homonyms, func(a, b Homonym) int { return strings.Compare(a.Path, b.Path) })

	// Harnesses that still see the skill via a retained copy or global homonym
	// do not lose access.
	var retained []string
	for _, c := range pv.KeptCopies {
		for _, h := range c.Harnesses {
			if !slices.Contains(retained, h) {
				retained = append(retained, h)
			}
		}
	}
	for _, h := range pv.Homonyms {
		for _, agent := range h.Harnesses {
			if !slices.Contains(retained, agent) {
				retained = append(retained, agent)
			}
		}
	}
	slices.Sort(retained)
	pv.RetainedTargets = retained

	var lost []string
	for _, h := range removedHarnesses {
		if !slices.Contains(retained, h) {
			lost = append(lost, h)
		}
	}
	slices.Sort(lost)
	pv.Targets = lost

	return o.buildGroup(pv, eff)
}

func groupCandidatePaths(skill graph.Skill) []string {
	var out []string
	if skill.Canonical != "" {
		out = append(out, skill.Canonical)
	}
	for _, p := range skill.Folders {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// checkGroupFolderEligibility enforces the exact-folder policy, ownership,
// symlink, foreign zone, and git boundaries for a candidate folder in a group.
func (o *observation) checkGroupFolderEligibility(skill graph.Skill, f graph.Folder) (string, error) {
	entry := f.Entry
	if id := o.profileOf(entry); id != "" {
		return "diagnostic_profile", fmt.Errorf("%w: %s is in diagnostic profile %s", ErrForeignZone, entry, id)
	}
	if id := o.profileOf(f.Path); id != "" {
		return "diagnostic_profile", fmt.Errorf("%w: %s is in diagnostic profile %s", ErrForeignZone, f.Path, id)
	}
	if err := o.checkForeignBoundary(entry); err != nil {
		return "foreign_zone", err
	}
	if err := o.checkForeignBoundary(f.Path); err != nil {
		return "foreign_zone", err
	}
	if skill.Zone != graph.ZoneNone && skill.Zone != graph.ZoneProfile && !(o.allowProvider && isAllowedProviderZone(skill.Zone)) {
		return "foreign_zone", fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, skill.Zone)
	}

	root := o.enclosingRoot(entry)
	if root == "" {
		return "outside_global_root", fmt.Errorf("%w: %s", ErrNotFound, entry)
	}
	if slices.ContainsFunc(o.g.Links, func(l graph.Link) bool { return l.Path == entry }) {
		return "alias", fmt.Errorf("%w: %s is a symlink", ErrAlias, entry)
	}
	mode, err := o.r.d.Files.Mode(entry)
	if errors.Is(err, iofs.ErrNotExist) {
		return "not_found", fmt.Errorf("%w: %s", ErrNotFound, entry)
	}
	if err != nil {
		return "unproven", fmt.Errorf("look at %s: %w", entry, err)
	}
	if mode&iofs.ModeSymlink != 0 {
		return "alias", fmt.Errorf("%w: %s is a symlink", ErrAlias, entry)
	}
	if !mode.IsDir() {
		return "not_found", fmt.Errorf("%w: %s", ErrNotFound, entry)
	}

	phys, err := o.resolved(entry)
	if err != nil {
		return "unproven", err
	}
	if err := o.checkForeignBoundary(phys); err != nil {
		return "foreign_zone", err
	}
	physRoot := o.enclosingRoot(phys)
	if physRoot == "" {
		return "outside_global_root", fmt.Errorf("%w: %s resolves outside global skill directories", ErrForeignZone, entry)
	}
	fRoot := o.enclosingRoot(f.Path)
	if fRoot == "" {
		return "outside_global_root", fmt.Errorf("%w: %s resolves outside global skill directories", ErrForeignZone, entry)
	}

	if f.Git != graph.GitNone {
		return "git", fmt.Errorf("%w: %s has git metadata or incomplete observation", engine.ErrUnsupportedTree, entry)
	}
	if err := o.checkGitBoundary(entry, root); err != nil {
		return "git", err
	}
	if phys != entry {
		if err := o.checkGitBoundary(phys, physRoot); err != nil {
			return "git", err
		}
	}
	if f.Path != entry && f.Path != phys {
		if err := o.checkGitBoundary(f.Path, fRoot); err != nil {
			return "git", err
		}
	}

	parent := filepath.Dir(entry)
	physParent := filepath.Dir(phys)
	store := o.r.d.Paths.StoreDir()
	off := o.offDir()
	isStore := parent == store
	isOff := parent == off
	isHarness := slices.Contains(o.harnesses, parent)
	isProvider := o.allowProvider && o.enclosingProviderRoot(entry) != ""
	if !isStore && !isOff && !isHarness && !isProvider {
		return "outside_global_root", fmt.Errorf("%w: %s", ErrNotFound, entry)
	}
	switch {
	case isStore && physParent != store:
		return "outside_global_root", fmt.Errorf("%w: %s resolves outside the store", ErrForeignZone, entry)
	case isOff && physParent != off:
		return "outside_global_root", fmt.Errorf("%w: %s resolves outside OFF", ErrForeignZone, entry)
	case isHarness && !slices.Contains(o.harnesses, physParent) && (!o.allowProvider || o.enclosingProviderRoot(phys) == ""):
		return "outside_global_root", fmt.Errorf("%w: %s resolves outside configured harness directories", ErrForeignZone, entry)
	case isProvider && o.enclosingProviderRoot(phys) == "":
		return "outside_global_root", fmt.Errorf("%w: %s resolves outside provider directories", ErrForeignZone, entry)
	}

	if isStore {
		base := filepath.Base(entry)
		if _, isParked := o.st.Parked[base]; isParked {
			return "ambiguous", fmt.Errorf("%w: %s is both in the store and parked", ErrAmbiguous, base)
		}
	}

	for _, other := range o.g.Skills {
		claims := other.Canonical == f.Path || slices.Contains(other.Folders, f.Path)
		if !claims {
			continue
		}
		if other.Zone != graph.ZoneNone && other.Zone != graph.ZoneProfile && !(o.allowProvider && isAllowedProviderZone(other.Zone)) {
			return "foreign_zone", fmt.Errorf("%w: %s is %s", ErrForeignZone, entry, other.Zone)
		}
		if other.ID != skill.ID {
			return "ambiguous", fmt.Errorf("%w: several skills claim %s", ErrAmbiguous, entry)
		}
		if isHarness && other.LockKey != "" {
			if skill.Canonical == "" || f.Path == skill.Canonical || other.LockKey != skill.LockKey {
				return "ambiguous", fmt.Errorf("%w: %s is claimed by lock entry %s", ErrAmbiguous, entry, other.LockKey)
			}
		}
	}

	return "", nil
}

func (o *observation) buildGroup(pv Preview, eff engine.Effect) (Plan, error) {
	b := o.r.d.Planner.NewBuilder(engine.Meta{Action: "remove", Targets: pv.Targets})
	for _, l := range pv.Links {
		if err := b.Unlink(l.Path, l.Target); err != nil {
			return Plan{}, err
		}
	}
	for _, f := range pv.Folders {
		if err := b.RemoveTree(f.Path); err != nil {
			return Plan{}, err
		}
	}
	if e := pv.LockEntry; e != nil {
		removeLock := b.LockRemove
		if e.DeletesLock {
			removeLock = b.LockRemoveLast
		}
		if err := removeLock(e.Name, e.Raw); err != nil {
			return Plan{}, err
		}
	}
	if !eff.IsZero() {
		if err := b.SetEffect(eff); err != nil {
			return Plan{}, err
		}
	}
	plan, err := b.Build()
	if err != nil {
		return Plan{}, err
	}
	return Plan{Plan: plan, Preview: pv}, nil
}
