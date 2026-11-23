package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/graph"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
)

// ComparedSkill describes one skill in a homonym comparison: its identity,
// disk folder, observed paths and the agents that see it.
type ComparedSkill struct {
	ID     string
	Name   string
	Dir    string
	Paths  []string
	Agents []string
}

// SkillComparison is the read-only comparison of two skills (typically homonyms).
type SkillComparison struct {
	Name      string
	First     ComparedSkill
	Second    ComparedSkill
	Diffs     []FileDiff
	Identical bool
}

// CompareSkills compares two observed skills by their v2 graph IDs (or v1
// inventory IDs only when v2 dependencies are missing) without modifying disk
// or state: it rejects unknown identities before any comparison reads, then
// returns each skill's observed paths, visible agents, and the file diffs
// between their folders using treehash and FileOps.
func (a *App) CompareSkills(ctx context.Context, firstID, secondID string) (SkillComparison, error) {
	if firstID == "" || secondID == "" {
		return SkillComparison{}, fmt.Errorf("%w: both skill IDs are required", ErrInvalidRequest)
	}

	first, second, err := a.resolveComparedSkills(ctx, firstID, secondID)
	if err != nil {
		return SkillComparison{}, err
	}

	firstTree, err := a.hashDir(first.Dir)
	if err != nil {
		return SkillComparison{}, fmt.Errorf("hash %s: %w", first.ID, err)
	}
	secondTree, err := a.hashDir(second.Dir)
	if err != nil {
		return SkillComparison{}, fmt.Errorf("hash %s: %w", second.ID, err)
	}

	changes := treeChanges(firstTree.Nodes, secondTree.Nodes)
	diffs, err := diffFiles(changes, a.readFrom(first.Dir, first.ID), a.readFrom(second.Dir, second.ID))
	if err != nil {
		return SkillComparison{}, fmt.Errorf("diff %s and %s: %w", first.ID, second.ID, err)
	}

	name := first.Name
	if name == "" {
		name = second.Name
	}

	return SkillComparison{
		Name:      name,
		First:     first,
		Second:    second,
		Diffs:     diffs,
		Identical: len(diffs) == 0,
	}, nil
}

func (a *App) resolveComparedSkills(ctx context.Context, firstID, secondID string) (ComparedSkill, ComparedSkill, error) {
	m, err := a.v2(ctx)
	if errors.Is(err, ErrMissingDependency) {
		return a.resolveComparedSkillsV1(ctx, firstID, secondID)
	}
	if err != nil {
		return ComparedSkill{}, ComparedSkill{}, err
	}

	g, err := m.observe()
	if err != nil {
		return ComparedSkill{}, ComparedSkill{}, fmt.Errorf("observe skills graph: %w", err)
	}

	folderEntries := make(map[string]string, len(g.Folders))
	for _, f := range g.Folders {
		folderEntries[f.Path] = f.Entry
	}

	first, err := resolveGraphComparedSkill(g, folderEntries, firstID)
	if err != nil {
		return ComparedSkill{}, ComparedSkill{}, err
	}
	second, err := resolveGraphComparedSkill(g, folderEntries, secondID)
	if err != nil {
		return ComparedSkill{}, ComparedSkill{}, err
	}
	return first, second, nil
}

func (a *App) resolveComparedSkillsV1(ctx context.Context, firstID, secondID string) (ComparedSkill, ComparedSkill, error) {
	skills, err := a.Skills(ctx)
	if err != nil {
		return ComparedSkill{}, ComparedSkill{}, fmt.Errorf("read skills: %w", err)
	}

	storeDir := a.d.Paths.StoreDir()
	resolve := func(id string) (ComparedSkill, error) {
		s, ok := findSkillByID(skills, id)
		if !ok {
			return ComparedSkill{}, fmt.Errorf("%s: %w", id, ErrSkillNotFound)
		}
		dir := skillDocumentDir(s)
		if dir == "" {
			return ComparedSkill{}, fmt.Errorf("%s has no folder on disk: %w", s.ID, ErrSkillNotFound)
		}
		return buildComparedSkillV1(s, dir, storeDir), nil
	}

	first, err := resolve(firstID)
	if err != nil {
		return ComparedSkill{}, ComparedSkill{}, err
	}
	second, err := resolve(secondID)
	if err != nil {
		return ComparedSkill{}, ComparedSkill{}, err
	}
	return first, second, nil
}

func resolveGraphComparedSkill(g graph.Graph, folderEntries map[string]string, id string) (ComparedSkill, error) {
	gs, ok := findGraphSkillByID(g.Skills, id)
	if !ok {
		return ComparedSkill{}, fmt.Errorf("%s: %w", id, ErrSkillNotFound)
	}
	return buildComparedGraphSkill(g, gs, folderEntries)
}

func findSkillByID(skills []domain.Skill, id string) (domain.Skill, bool) {
	for _, s := range skills {
		if s.ID == id {
			return s, true
		}
	}
	return domain.Skill{}, false
}

func findGraphSkillByID(skills []graph.Skill, id string) (graph.Skill, bool) {
	for _, s := range skills {
		if s.ID == id {
			return s, true
		}
	}
	return graph.Skill{}, false
}

func visibleGraphSightings(g graph.Graph, gs graph.Skill, folderEntries map[string]string) []graph.Sighting {
	off := filepath.Join(g.Store, inventory.OffDirName)
	inOff := gs.Canonical != "" && strings.HasPrefix(folderEntries[gs.Canonical], off+string(filepath.Separator))
	out := make([]graph.Sighting, 0, len(gs.Visibility))
	for _, v := range gs.Visibility {
		if inOff && v.Kind != graph.Direct && v.Kind != graph.ViaLink {
			continue
		}
		if !slices.Contains(g.Harnesses, v.Harness) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func buildComparedGraphSkill(g graph.Graph, gs graph.Skill, folderEntries map[string]string) (ComparedSkill, error) {
	dir := gs.Canonical
	if dir == "" && len(gs.Folders) > 0 {
		dir = gs.Folders[0]
	}
	if dir == "" {
		return ComparedSkill{}, fmt.Errorf("%s has no folder on disk: %w", gs.ID, ErrSkillNotFound)
	}

	sightings := visibleGraphSightings(g, gs, folderEntries)

	paths := []string{dir}
	for _, p := range gs.Folders {
		if p != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}

	extraPaths := make([]string, 0, len(gs.Aliases)+len(sightings))
	for _, alias := range gs.Aliases {
		if alias != "" {
			extraPaths = append(extraPaths, alias)
		}
	}
	for _, sg := range sightings {
		if sg.Entry != "" {
			extraPaths = append(extraPaths, sg.Entry)
		}
	}
	slices.Sort(extraPaths)
	for _, p := range extraPaths {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}

	agents := make([]string, 0, len(sightings))
	for _, sg := range sightings {
		agents = append(agents, sg.Harness)
	}
	slices.Sort(agents)
	agents = slices.Compact(agents)

	name := gs.Name
	if name == "" {
		name = gs.LockKey
	}

	return ComparedSkill{
		ID:     gs.ID,
		Name:   name,
		Dir:    dir,
		Paths:  paths,
		Agents: agents,
	}, nil
}

func buildComparedSkillV1(s domain.Skill, dir, storeDir string) ComparedSkill {
	primarySHA := ""
	for _, loc := range s.Locations {
		if loc.Path == dir && loc.Kind != domain.LocationBrokenLink {
			primarySHA = loc.ContentTreeSHA
			break
		}
	}

	offPrefix := filepath.Join(storeDir, inventory.OffDirName) + string(filepath.Separator)
	dirInOff := storeDir != "" && strings.HasPrefix(dir, offPrefix)

	paths := []string{dir}
	agents := []string{}
	for _, loc := range s.Locations {
		if loc.Kind == domain.LocationBrokenLink {
			continue
		}
		if loc.Path != dir {
			if primarySHA != "" && loc.ContentTreeSHA != primarySHA {
				continue
			}
			if !dirInOff && storeDir != "" && strings.HasPrefix(loc.Path, offPrefix) {
				continue
			}
		}
		if loc.Path != "" && !slices.Contains(paths, loc.Path) {
			paths = append(paths, loc.Path)
		}
		if loc.AgentID != "" && loc.AgentID != domain.StoreAgentID && !slices.Contains(agents, loc.AgentID) {
			agents = append(agents, loc.AgentID)
		}
	}
	slices.Sort(agents)

	return ComparedSkill{
		ID:     s.ID,
		Name:   s.Name,
		Dir:    dir,
		Paths:  paths,
		Agents: agents,
	}
}
