package app

import (
	"slices"

	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/manage/remove"
	"skillhub/internal/manage/repair"
	"skillhub/internal/manage/toggle"
)

// The planners' previews as the app's types, so the entry points never
// import the planners, the graph or Doctor.

func steps(p engine.Plan) []Step {
	out := make([]Step, 0, len(p.Steps))
	for _, s := range p.Steps {
		st := Step{Kind: string(s.Kind), Src: s.Src, Dst: s.Dst, Path: s.Path, Target: s.Target, Name: s.Name}
		switch {
		case s.Kind == engine.StepLockUnpark && s.Entry != nil:
			// lockUnpark keeps its entry in the parked entry, not in Name.
			st.Name = s.Entry.Name
		case s.Kind == engine.StepLockRemove && len(s.Post) == 1 && s.Post[0].State == engine.StateNoLock:
			// LockRemoveLast is a lockRemove that deletes the emptied lock.
			st.Kind = "lockRemoveLast"
		}
		out = append(out, st)
	}
	return out
}

func sightings(in []graph.Sighting) []Sighting {
	var out []Sighting
	for _, s := range in {
		out = append(out, Sighting{Harness: s.Harness, Kind: string(s.Kind), Entry: s.Entry})
	}
	return out
}

func toggleLinks(in []toggle.LinkChange) []LinkChange {
	var out []LinkChange
	for _, l := range in {
		out = append(out, LinkChange{Path: l.Path, Target: l.Target, Harnesses: slices.Clone(l.Harnesses), Broken: l.Broken})
	}
	return out
}

func toggleView(pv toggle.Preview) *TogglePreview {
	out := &TogglePreview{
		Harness: pv.Harness, StillVisible: sightings(pv.StillVisible), Visible: sightings(pv.Visible),
		SharesDirWith: slices.Clone(pv.SharesDirWith), Kept: slices.Clone(pv.Kept),
	}
	for _, s := range pv.Skills {
		out.Skills = append(out.Skills, SkillChange{
			Key: s.Key, From: s.From, To: s.To,
			RemovedLinks: toggleLinks(s.RemovedLinks), CreatedLinks: toggleLinks(s.CreatedLinks),
			LockEntry: s.LockEntry, RestoreLinks: slices.Clone(s.RestoreLinks), StillVisible: sightings(s.StillVisible),
		})
	}
	return out
}

func removeView(pv remove.Preview) *RemovePreview {
	out := &RemovePreview{
		Key: pv.Key, Project: pv.Project, ProjectDir: pv.ProjectDir, ProjectRoot: pv.ProjectRoot, SkillName: pv.SkillName,
		Parked: pv.Parked, Off: pv.Off, Folder: pv.Folder, TreeSHA: pv.TreeSHA, ParkedEntry: pv.ParkedEntry,
		Modified: string(pv.Modified.State), ModifiedExpected: pv.Modified.Expected, ModifiedActual: pv.Modified.Actual,
		PendingCandidates: slices.Clone(pv.PendingCandidates), Targets: slices.Clone(pv.Targets),
		Group: pv.Group, SkillID: pv.SkillID, RetainedTargets: slices.Clone(pv.RetainedTargets),
	}
	for _, f := range pv.Folders {
		out.Folders = append(out.Folders, RemovedFolder{
			Path: f.Path, TreeSHA: f.TreeSHA, Off: f.Off, InStore: f.InStore, Harnesses: slices.Clone(f.Harnesses),
		})
	}
	for _, c := range pv.KeptCopies {
		out.KeptCopies = append(out.KeptCopies, KeptCopy{
			Path: c.Path, Name: c.Name, TreeSHA: c.TreeSHA,
			Harnesses: slices.Clone(c.Harnesses), Profile: c.Profile, Reason: c.Reason,
		})
	}
	for _, l := range pv.Links {
		out.Links = append(out.Links, LinkChange{Path: l.Path, Target: l.Target, Harnesses: slices.Clone(l.Harnesses)})
	}
	if pv.LockEntry != nil {
		out.LockEntry = &RemovedEntry{Name: pv.LockEntry.Name, Raw: slices.Clone(pv.LockEntry.Raw), DeletesLock: pv.LockEntry.DeletesLock}
	}
	for _, h := range pv.Homonyms {
		out.Homonyms = append(out.Homonyms, Homonym{Path: h.Path, Name: h.Name, TreeSHA: h.TreeSHA,
			Identical: h.Identical, Harnesses: slices.Clone(h.Harnesses), Profile: h.Profile})
	}
	for _, k := range pv.KeptLinks {
		out.KeptLinks = append(out.KeptLinks, KeptLink{Path: k.Path, Target: k.Target, Profile: k.Profile, Zone: string(k.Zone)})
	}
	return out
}

func installView(pv install.Preview, rev Revision) *InstallPreview {
	out := &InstallPreview{
		Key: pv.Key, Name: pv.Name, Source: pv.Source, SourceURL: pv.SourceURL, Commit: pv.Commit,
		SkillPath: pv.SkillPath, TreeSHA: pv.TreeSHA, StagedPath: pv.StagedPath, StorePath: pv.StorePath,
		Targets: slices.Clone(pv.Targets), LockEntry: slices.Clone(pv.LockEntry), LockCreated: pv.LockCreated,
		FollowsDefaultBranch: pv.FollowsDefaultBranch, Labels: slices.Clone(rev.Prepared.Labels),
		ReplacedEntry: slices.Clone(pv.ReplacedEntry), Site: pv.Site,
	}
	for _, l := range pv.Links {
		out.Links = append(out.Links, LinkChange{Path: l.Path, Target: l.Target, Harnesses: slices.Clone(l.Harnesses)})
	}
	return out
}

func repairView(pv repair.Preview) *RepairPreview {
	out := &RepairPreview{
		Finding: pv.Finding, Repair: string(pv.Repair), Key: pv.Key, Canonical: pv.Canonical,
		Kept: slices.Clone(pv.Kept), LockEntry: pv.LockEntry,
	}
	for _, mv := range pv.Moved {
		out.Moved = append(out.Moved, Move{From: mv.From, To: mv.To})
	}
	link := func(in []repair.LinkChange) []LinkChange {
		var ls []LinkChange
		for _, l := range in {
			ls = append(ls, LinkChange{Path: l.Path, Target: l.Target, Harnesses: slices.Clone(l.Harnesses), Broken: l.Broken})
		}
		return ls
	}
	out.RemovedLinks, out.CreatedLinks = link(pv.RemovedLinks), link(pv.CreatedLinks)
	for _, c := range pv.RemovedCopies {
		out.RemovedCopies = append(out.RemovedCopies, CopyChange{Path: c.Path, TreeSHA: c.TreeSHA,
			Harnesses: slices.Clone(c.Harnesses), Replaced: c.Replaced})
	}
	for _, c := range pv.Copies {
		out.Copies = append(out.Copies, CopyDiff{Path: c.Path, TreeSHA: c.TreeSHA,
			Added: slices.Clone(c.Added), Removed: slices.Clone(c.Removed), Changed: slices.Clone(c.Changed)})
	}
	if pv.Toggle != nil {
		out.Toggle = toggleView(*pv.Toggle)
	}
	return out
}

func findingView(f doctor.Finding) Finding {
	return Finding{
		ID: f.ID, Kind: string(f.Kind), Skill: f.Skill, Name: f.Name, LockKey: f.LockKey,
		Paths: slices.Clone(f.Paths), Target: f.Target, Related: slices.Clone(f.Related),
		Harnesses: slices.Clone(f.Harnesses), Skills: slices.Clone(f.Skills),
	}
}
