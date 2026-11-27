package remove

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

var (
	// ErrNoProject reports a project-scoped removal requested when skillhub was
	// not given an active current project directory.
	ErrNoProject = errors.New("no current project directory")
	// ErrProjectLinked reports a current-project root, skill folder, or SKILL.md
	// symlinked rather than a direct real directory.
	ErrProjectLinked = errors.New("project skill or root is a symlink")
)

// ProjectInspector inspects direct real skill directories in the current
// startup project without following symlinks and without importing fs adapters.
type ProjectInspector interface {
	WalkProject(
		projectDir string,
		onSkill func(id, root, name, path string),
		onRejected func(root, name, path, reason string),
	) error
	InspectSkillTree(projectDir, root, name string) (skillPath string, tree treehash.Tree, rejectKind string, err error)
	FindLinksToSkill(projectDir, targetRoot, targetName string, onLink func(root, name, linkPath, target string)) error
	SameDevice(projectDir, root, otherDir string) (bool, error)
}

func validProjectTarget(rest string) (root, name string, ok bool) {
	for _, r := range []engine.ProjectRoot{engine.ProjectRootAgents, engine.ProjectRootClaude} {
		prefix := string(r) + "/"
		if strings.HasPrefix(rest, prefix) {
			n := strings.TrimPrefix(rest, prefix)
			if validKey(n) && !strings.ContainsRune(n, '/') && n != ".git" {
				return string(r), n, true
			}
			return "", "", false
		}
	}
	return "", "", false
}

func (o *observation) removeProject(root, name string) (Plan, error) {
	if o.r.d.Project == nil || o.r.d.ProjectDir == "" {
		return Plan{}, fmt.Errorf("%w: project:%s/%s", ErrNoProject, root, name)
	}
	cleanProj := filepath.Clean(o.r.d.ProjectDir)
	skillPath, tree, rejectKind, err := o.r.d.Project.InspectSkillTree(cleanProj, root, name)
	switch rejectKind {
	case "":
	case "not_found":
		return Plan{}, fmt.Errorf("%w: project:%s/%s", ErrNotFound, root, name)
	case "linked":
		return Plan{}, fmt.Errorf("%w: %s", ErrProjectLinked, err)
	case "git":
		return Plan{}, fmt.Errorf("%w: %s is a git clone", engine.ErrUnsupportedTree, skillPath)
	case "not_dir", "invalid", "unsupported", "unproven":
		return Plan{}, fmt.Errorf("%w: %s (%v)", engine.ErrUnsupportedTree, skillPath, err)
	default:
		if err != nil {
			return Plan{}, fmt.Errorf("inspect project skill %s: %w", skillPath, err)
		}
		return Plan{}, fmt.Errorf("%w: %s", engine.ErrUnsupportedTree, skillPath)
	}

	sameDev, err := o.r.d.Project.SameDevice(cleanProj, root, filepath.Dir(o.r.d.Paths.ParkedDir()))
	if err != nil {
		return Plan{}, fmt.Errorf("check device for %s: %w", skillPath, err)
	}
	if !sameDev {
		return Plan{}, fmt.Errorf("move %s to %s: %w", skillPath, filepath.Dir(o.r.d.Paths.ParkedDir()), ports.ErrCrossDevice)
	}

	type projectLink struct {
		root   string
		path   string
		target string
	}
	var links []projectLink
	if err := o.r.d.Project.FindLinksToSkill(cleanProj, root, name, func(linkRoot, _, linkPath, linkTarget string) {
		links = append(links, projectLink{root: linkRoot, path: linkPath, target: linkTarget})
	}); err != nil {
		return Plan{}, fmt.Errorf("find links to %s: %w", skillPath, err)
	}

	scope := &engine.ProjectScope{
		ProjectDir: cleanProj,
		Root:       engine.ProjectRoot(root),
		SkillName:  name,
	}
	b := o.r.d.Planner.NewBuilder(engine.Meta{
		Action:          "remove",
		Targets:         []string{},
		Canonical:       skillPath,
		ReviewedTreeSHA: tree.SHA,
		Project:         scope,
	})
	pvLinks := make([]Link, 0, len(links))
	for _, l := range links {
		if err := b.Unlink(l.path, l.target); err != nil {
			return Plan{}, err
		}
		pvLinks = append(pvLinks, Link{
			Path:      l.path,
			Target:    l.target,
			Harnesses: []string{"proyecto (" + l.root + ")"},
		})
	}
	if err := b.RemoveTree(skillPath); err != nil {
		return Plan{}, err
	}
	p, err := b.Build()
	if err != nil {
		return Plan{}, err
	}

	pv := Preview{
		Key:         "project:" + root + "/" + name,
		Project:     true,
		ProjectDir:  cleanProj,
		ProjectRoot: root,
		SkillName:   name,
		Folder:      skillPath,
		TreeSHA:     tree.SHA,
		Links:       pvLinks,
		Homonyms:    o.homonyms(skillPath, name, tree.SHA, name),
	}
	return Plan{Plan: p, Preview: pv}, nil
}

type projectMatch struct {
	root string
	name string
}

func (o *observation) projectMatches(key string) []projectMatch {
	if o.r.d.Project == nil || o.r.d.ProjectDir == "" {
		return nil
	}
	cleanProj := filepath.Clean(o.r.d.ProjectDir)
	var out []projectMatch
	seen := map[string]bool{}
	_ = o.r.d.Project.WalkProject(
		cleanProj,
		func(_ string, root, name, _ string) {
			if name == key && !seen[root] {
				seen[root] = true
				out = append(out, projectMatch{root: root, name: name})
			}
		},
		func(root, name, _, _ string) {
			if name == key && !seen[root] {
				seen[root] = true
				out = append(out, projectMatch{root: root, name: name})
			}
		},
	)
	return out
}

func (o *observation) projectHomonyms(skipPath, name, treeSHA string) []Homonym {
	if o.r.d.Project == nil || o.r.d.ProjectDir == "" {
		return nil
	}
	cleanProj := filepath.Clean(o.r.d.ProjectDir)
	var out []Homonym
	_ = o.r.d.Project.WalkProject(
		cleanProj,
		func(_ string, root, skillName, skillPath string) {
			if skillName != name || skillPath == skipPath {
				return
			}
			_, tr, rej, err := o.r.d.Project.InspectSkillTree(cleanProj, root, skillName)
			sha := ""
			if err == nil && rej == "" {
				sha = tr.SHA
			}
			out = append(out, Homonym{
				Path:      skillPath,
				Name:      skillName,
				TreeSHA:   sha,
				Identical: sha != "" && sha == treeSHA,
				Harnesses: []string{"proyecto (" + root + ")"},
			})
		},
		nil,
	)
	return out
}

func (o *observation) removeLegacyWithProject(key string) (Plan, error) {
	projMatches := o.projectMatches(key)
	if len(projMatches) == 0 {
		return o.removeLegacy(key)
	}

	active, err := o.activeFolder(key)
	if err != nil {
		return Plan{}, err
	}
	_, isParked := o.st.Parked[key]
	if len(projMatches) == 1 && active == nil && !isParked {
		return o.removeProject(projMatches[0].root, projMatches[0].name)
	}

	var selectors []string
	if active != nil {
		if active.skill.ID != "" {
			selectors = append(selectors, active.skill.ID)
		} else {
			selectors = append(selectors, key)
		}
	}
	if isParked {
		selectors = append(selectors, "parked:"+key)
	}
	for _, m := range projMatches {
		selectors = append(selectors, "project:"+m.root+"/"+m.name)
	}
	slices.Sort(selectors)
	return Plan{}, fmt.Errorf("%w: %s matches several copies (%s)", ErrAmbiguous, key, strings.Join(selectors, ", "))
}
