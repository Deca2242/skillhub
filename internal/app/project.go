package app

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/paths"
)

// ProjectPort inspects and operates on direct real skill directories in the
// current startup project without following symlinks.
type ProjectPort interface {
	engine.ProjectFS
	remove.ProjectInspector
}

// ProjectSkillView describes one entry found under .agents/skills or
// .claude/skills of the startup project directory.
type ProjectSkillView struct {
	ID         string
	Name       string
	ProjectDir string
	Root       string
	Path       string
	TreeSHA    string
	Removable  bool
	Linked     bool
	HasGit     bool
	Reason     string
}

func normalizeProjectDir(p paths.Paths, raw string) string {
	if raw == "" || !filepath.IsAbs(raw) {
		return ""
	}
	if slices.Contains(strings.Split(filepath.ToSlash(raw), "/"), "..") {
		return ""
	}
	clean := filepath.Clean(raw)
	if p.Guard().CheckLexical(clean) != nil {
		return ""
	}
	home := p.Home()
	if clean == home {
		return ""
	}
	excluded := []string{
		filepath.Join(home, ".agents"),
		filepath.Join(home, ".claude"),
		p.ConfigDir(),
		p.CacheDir(),
		p.StateDir(),
	}
	for _, dir := range excluded {
		if dir == "" {
			continue
		}
		if clean == dir || strings.HasPrefix(clean, dir+string(filepath.Separator)) {
			return ""
		}
	}
	return clean
}

// ProjectDir returns the normalized startup project directory, or "" when no
// project scope is active.
func (a *App) ProjectDir() string { return a.projectDir }

// ProjectSkills returns the direct skill entries under .agents/skills and
// .claude/skills in the startup project directory without traversing parent or
// sibling directories.
func (a *App) ProjectSkills() ([]ProjectSkillView, error) {
	if a.projectDir == "" || a.d.Project == nil {
		return nil, nil
	}
	byID := map[string]ProjectSkillView{}
	err := a.d.Project.WalkProject(
		a.projectDir,
		func(id, root, name, skillPath string) {
			inspectedPath, tr, rej, err := a.d.Project.InspectSkillTree(a.projectDir, root, name)
			row := ProjectSkillView{
				ID:         id,
				Name:       name,
				ProjectDir: a.projectDir,
				Root:       root,
				Path:       skillPath,
				TreeSHA:    tr.SHA,
				Removable:  err == nil && rej == "",
				Linked:     rej == "linked",
				HasGit:     rej == "git",
				Reason:     rej,
			}
			if inspectedPath != "" {
				row.Path = inspectedPath
			}
			byID[id] = row
		},
		func(root, name, path, reason string) {
			id := "project:" + root
			labelName := root
			if name != "" {
				id = "project:" + root + "/" + name
				labelName = name
			}
			byID[id] = ProjectSkillView{
				ID:         id,
				Name:       labelName,
				ProjectDir: a.projectDir,
				Root:       root,
				Path:       path,
				Removable:  false,
				Linked:     reason == "linked" || reason == "linked_skill_md",
				HasGit:     reason == "git",
				Reason:     reason,
			}
		},
	)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(byID) == 0 {
		return nil, nil
	}
	out := make([]ProjectSkillView, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	slices.SortFunc(out, func(x, y ProjectSkillView) int {
		if c := strings.Compare(x.Name, y.Name); c != 0 {
			return c
		}
		if c := strings.Compare(x.Root, y.Root); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out, nil
}
