package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"skillhub/internal/shared/paths"
)

const projectSkillIDPrefix = "project:"

var (
	// ErrProjectDirLinked reports that the startup project directory or one of
	// its ancestors is a symlink and cannot be accepted without following links.
	ErrProjectDirLinked = errors.New("project directory is a symlink")
	// ErrProjectDirNotDir reports that the startup project directory or one of
	// its ancestors is not a directory.
	ErrProjectDirNotDir = errors.New("project directory is not a directory")
	// ErrProjectScopeUnproven reports that physical containment inside the
	// startup project directory could not be verified.
	ErrProjectScopeUnproven = errors.New("project scope could not be proved")
)

// ProjectRootKind identifies an in-scope project skills directory relative to
// the startup project directory.
type ProjectRootKind string

const (
	ProjectRootAgents ProjectRootKind = ".agents/skills"
	ProjectRootClaude ProjectRootKind = ".claude/skills"
)

type projectRootSpec struct {
	kind  ProjectRootKind
	agent string
	leaf  string
}

var projectRootSpecs = []projectRootSpec{
	{kind: ProjectRootAgents, agent: ".agents", leaf: "skills"},
	{kind: ProjectRootClaude, agent: ".claude", leaf: "skills"},
}

// ProjectRootStatus reports the observation outcome of a project skills root.
type ProjectRootStatus string

const (
	ProjectRootReady    ProjectRootStatus = "ready"
	ProjectRootMissing  ProjectRootStatus = "missing"
	ProjectRootRejected ProjectRootStatus = "rejected"
)

// ProjectRejectReason explains why a project skills root or candidate entry was
// left ineligible.
type ProjectRejectReason string

const (
	ProjectRejectLinked            ProjectRejectReason = "linked"
	ProjectRejectNotDir            ProjectRejectReason = "not_dir"
	ProjectRejectMissingSkillMD    ProjectRejectReason = "missing_skill_md"
	ProjectRejectLinkedSkillMD     ProjectRejectReason = "linked_skill_md"
	ProjectRejectNonRegularSkillMD ProjectRejectReason = "non_regular_skill_md"
	ProjectRejectUnreadable        ProjectRejectReason = "unreadable"
	ProjectRejectUnprovenScope     ProjectRejectReason = "unproven_scope"
)

// ProjectSkillID returns the exact in-memory identity for a direct real skill
// directory under root inside the startup project directory.
func ProjectSkillID(root ProjectRootKind, name string) string {
	return projectSkillIDPrefix + string(root) + "/" + name
}

// ProjectSkill is one eligible direct real skill directory under a project's
// .agents/skills or .claude/skills root. It represents read-only identity, not
// deletion permission.
type ProjectSkill struct {
	ID          string
	ProjectDir  string
	Root        ProjectRootKind
	Name        string
	Path        string
	SkillMDPath string
}

// ProjectRejectedEntry describes an ineligible candidate observed directly under
// a ready project skills root.
type ProjectRejectedEntry struct {
	Name   string
	Path   string
	Reason ProjectRejectReason
	Err    error
}

// ProjectSkillRoot describes the observation result for one in-scope project
// skills root (.agents/skills or .claude/skills).
type ProjectSkillRoot struct {
	Kind     ProjectRootKind
	Path     string
	Status   ProjectRootStatus
	Reason   ProjectRejectReason
	Err      error
	Rejected []ProjectRejectedEntry
}

// ProjectSkills holds the read-only enumeration of direct real skill
// directories for one explicit startup project directory.
type ProjectSkills struct {
	projectDir string
	roots      []ProjectSkillRoot
	skills     []ProjectSkill
	byID       map[string]ProjectSkill
}

// ProjectDir returns the cleaned startup project directory that was scanned.
func (p ProjectSkills) ProjectDir() string { return p.projectDir }

// Roots returns a defensive copy of the observed roots in deterministic order
// (.agents/skills, then .claude/skills).
func (p ProjectSkills) Roots() []ProjectSkillRoot {
	out := make([]ProjectSkillRoot, len(p.roots))
	for i, root := range p.roots {
		root.Rejected = slices.Clone(root.Rejected)
		if root.Rejected == nil {
			root.Rejected = []ProjectRejectedEntry{}
		}
		out[i] = root
	}
	return out
}

// Skills returns a defensive copy of all eligible direct real skill directories,
// preserving distinct same-name copies across roots.
func (p ProjectSkills) Skills() []ProjectSkill {
	if len(p.skills) == 0 {
		return []ProjectSkill{}
	}
	return slices.Clone(p.skills)
}

// Lookup matches id strictly against the identities enumerated in memory. An
// unrecognized ID returns zero and false without inspecting the filesystem.
func (p ProjectSkills) Lookup(id string) (ProjectSkill, bool) {
	if id == "" || len(p.byID) == 0 {
		return ProjectSkill{}, false
	}
	skill, ok := p.byID[id]
	return skill, ok
}

// ProjectSkillScanner performs bounded no-follow observation of direct real
// skill directories under an explicit startup project directory.
type ProjectSkillScanner struct {
	guard paths.Guard
}

// NewProjectSkillScanner builds a ProjectSkillScanner that enforces guard
// against relative and forbidden paths.
func NewProjectSkillScanner(guard paths.Guard) *ProjectSkillScanner {
	return &ProjectSkillScanner{guard: guard}
}

// Scan enumerates exact direct real skill directories in projectDir's
// .agents/skills and .claude/skills roots without following symlinks or reading
// SKILL.md contents. Missing roots are normal; errors or unproven scope never
// produce eligible entries.
func (s *ProjectSkillScanner) Scan(projectDir string) (ProjectSkills, error) {
	cleanProjectDir, err := s.validateProjectDir(projectDir)
	if err != nil {
		return ProjectSkills{}, err
	}

	projFD, err := s.openStartupDirNoFollow(cleanProjectDir)
	if err != nil {
		return ProjectSkills{}, err
	}
	defer closeFD(projFD)

	realProjDir, err := s.verifyFDScope(projFD, cleanProjectDir)
	if err != nil {
		return ProjectSkills{}, fmt.Errorf("scan project skills %s: %w", cleanProjectDir, err)
	}

	roots := make([]ProjectSkillRoot, 0, len(projectRootSpecs))
	skills := []ProjectSkill{}
	byID := map[string]ProjectSkill{}

	for _, spec := range projectRootSpecs {
		root, rootSkills := s.scanRoot(
			projFD,
			cleanProjectDir,
			realProjDir,
			spec,
		)
		roots = append(roots, root)
		for _, skill := range rootSkills {
			skills = append(skills, skill)
			byID[skill.ID] = skill
		}
	}

	return ProjectSkills{
		projectDir: cleanProjectDir,
		roots:      roots,
		skills:     skills,
		byID:       maps.Clone(byID),
	}, nil
}

func (s *ProjectSkillScanner) validateProjectDir(projectDir string) (string, error) {
	if projectDir == "" || !filepath.IsAbs(projectDir) {
		return "", fmt.Errorf("scan project skills: %w: %q is not absolute", paths.ErrInvalidPath, projectDir)
	}
	if hasDotDotSegment(projectDir) {
		return "", fmt.Errorf("scan project skills: %w: %q contains parent traversal", paths.ErrInvalidPath, projectDir)
	}
	clean := filepath.Clean(projectDir)
	if err := s.guard.CheckLexical(clean); err != nil {
		return "", fmt.Errorf("scan project skills: %w", err)
	}
	return clean, nil
}

func (s *ProjectSkillScanner) openStartupDirNoFollow(cleanProjectDir string) (int, error) {
	if err := s.guard.CheckLexical("/"); err != nil {
		return -1, fmt.Errorf("scan project skills %s: %w", cleanProjectDir, err)
	}
	curFD, err := openNoFollowDir("/")
	if err != nil {
		return -1, mapProjectDirOpenError(cleanProjectDir, err)
	}
	if cleanProjectDir == "/" {
		return curFD, nil
	}

	curPath := "/"
	for _, part := range strings.Split(strings.TrimPrefix(cleanProjectDir, "/"), "/") {
		if part == "" {
			continue
		}
		curPath = filepath.Join(curPath, part)
		if err := s.guard.CheckLexical(curPath); err != nil {
			closeFD(curFD)
			return -1, fmt.Errorf("scan project skills %s: %w", cleanProjectDir, err)
		}
		var st unix.Stat_t
		if err := fstatatNoFollow(curFD, part, &st); err != nil {
			closeFD(curFD)
			return -1, &iofs.PathError{Op: "lstat", Path: curPath, Err: err}
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			closeFD(curFD)
			return -1, fmt.Errorf(
				"scan project skills %s: %w: %s is a symlink",
				cleanProjectDir,
				ErrProjectDirLinked,
				curPath,
			)
		case unix.S_IFDIR:
		default:
			closeFD(curFD)
			return -1, fmt.Errorf(
				"scan project skills %s: %w: %s is not a directory",
				cleanProjectDir,
				ErrProjectDirNotDir,
				curPath,
			)
		}
		nextFD, err := openatNoFollowDir(curFD, part)
		closeFD(curFD)
		if err != nil {
			return -1, mapProjectDirOpenError(curPath, err)
		}
		curFD = nextFD
	}
	return curFD, nil
}

func (s *ProjectSkillScanner) verifyFDScope(fd int, wantPath string) (string, error) {
	realPath, err := fdRealPath(fd)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProjectScopeUnproven, err)
	}
	if err := s.guard.CheckLexical(realPath); err != nil {
		return "", err
	}
	if realPath != wantPath {
		return "", fmt.Errorf("%w: resolved to %s, want %s", ErrProjectScopeUnproven, realPath, wantPath)
	}
	return realPath, nil
}

func hasDotDotSegment(path string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..")
}

func mapProjectDirOpenError(projectDir string, err error) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return fmt.Errorf("scan project skills %s: %w", projectDir, ErrProjectDirLinked)
	case errors.Is(err, unix.ENOTDIR):
		return fmt.Errorf("scan project skills %s: %w", projectDir, ErrProjectDirNotDir)
	default:
		return &iofs.PathError{Op: "open", Path: projectDir, Err: err}
	}
}

func (s *ProjectSkillScanner) scanRoot(
	projFD int,
	cleanProjectDir string,
	realProjDir string,
	spec projectRootSpec,
) (ProjectSkillRoot, []ProjectSkill) {
	rootPath := filepath.Join(cleanProjectDir, spec.agent, spec.leaf)
	root := ProjectSkillRoot{
		Kind:     spec.kind,
		Path:     rootPath,
		Status:   ProjectRootReady,
		Rejected: []ProjectRejectedEntry{},
	}

	agentPath := filepath.Join(cleanProjectDir, spec.agent)
	if err := s.guard.CheckLexical(agentPath); err != nil {
		return rejectRoot(root, ProjectRejectUnprovenScope, err), nil
	}
	agentFD, rejected := openChildDirNoFollow(projFD, spec.agent, agentPath, root)
	if rejected != nil {
		return *rejected, nil
	}
	defer closeFD(agentFD)

	if _, err := s.verifyFDScope(agentFD, filepath.Join(realProjDir, spec.agent)); err != nil {
		return rejectRoot(root, ProjectRejectUnprovenScope, err), nil
	}

	if err := s.guard.CheckLexical(rootPath); err != nil {
		return rejectRoot(root, ProjectRejectUnprovenScope, err), nil
	}
	skillsFD, rejected := openChildDirNoFollow(agentFD, spec.leaf, rootPath, root)
	if rejected != nil {
		return *rejected, nil
	}
	// os.NewFile takes ownership of skillsFD; closing dirFile closes skillsFD.
	dirFile := os.NewFile(uintptr(skillsFD), rootPath)
	defer func() { _ = dirFile.Close() }()

	wantRealRoot := filepath.Join(realProjDir, spec.agent, spec.leaf)
	realSkillsPath, err := s.verifyFDScope(int(dirFile.Fd()), wantRealRoot)
	if err != nil {
		return rejectRoot(root, ProjectRejectUnprovenScope, err), nil
	}

	names, err := dirFile.Readdirnames(-1)
	if err != nil {
		return rejectRoot(root, ProjectRejectUnreadable, err), nil
	}
	slices.Sort(names)

	skills := make([]ProjectSkill, 0, len(names))
	rawFD := int(dirFile.Fd())
	for _, name := range names {
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
			continue
		}
		skill, rejected := s.inspectProjectCandidate(
			rawFD,
			cleanProjectDir,
			realSkillsPath,
			rootPath,
			spec.kind,
			name,
		)
		if rejected != nil {
			root.Rejected = append(root.Rejected, *rejected)
			continue
		}
		skills = append(skills, skill)
	}
	return root, skills
}

func openChildDirNoFollow(
	parentFD int,
	name string,
	componentPath string,
	root ProjectSkillRoot,
) (int, *ProjectSkillRoot) {
	var st unix.Stat_t
	if err := fstatatNoFollow(parentFD, name, &st); err != nil {
		res := classifyRootOpenError(root, componentPath, err)
		return -1, &res
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		res := rejectRoot(root, ProjectRejectLinked, fmt.Errorf("%s is a symlink", componentPath))
		return -1, &res
	case unix.S_IFDIR:
	default:
		res := rejectRoot(root, ProjectRejectNotDir, fmt.Errorf("%s is not a directory", componentPath))
		return -1, &res
	}
	fd, err := openatNoFollowDir(parentFD, name)
	if err != nil {
		res := classifyRootOpenError(root, componentPath, err)
		return -1, &res
	}
	return fd, nil
}

func classifyRootOpenError(root ProjectSkillRoot, componentPath string, err error) ProjectSkillRoot {
	switch {
	case errors.Is(err, unix.ENOENT):
		root.Status = ProjectRootMissing
		return root
	case errors.Is(err, unix.ELOOP):
		return rejectRoot(root, ProjectRejectLinked, fmt.Errorf("%s is a symlink", componentPath))
	case errors.Is(err, unix.ENOTDIR):
		return rejectRoot(root, ProjectRejectNotDir, fmt.Errorf("%s is not a directory", componentPath))
	default:
		return rejectRoot(root, ProjectRejectUnreadable, &iofs.PathError{Op: "open", Path: componentPath, Err: err})
	}
}

func rejectRoot(root ProjectSkillRoot, reason ProjectRejectReason, err error) ProjectSkillRoot {
	root.Status = ProjectRootRejected
	root.Reason = reason
	root.Err = err
	root.Rejected = []ProjectRejectedEntry{}
	return root
}

func (s *ProjectSkillScanner) inspectProjectCandidate(
	skillsFD int,
	cleanProjectDir string,
	realSkillsPath string,
	rootPath string,
	kind ProjectRootKind,
	name string,
) (ProjectSkill, *ProjectRejectedEntry) {
	candPath := filepath.Join(rootPath, name)
	if err := s.guard.CheckLexical(candPath); err != nil {
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectUnprovenScope,
			Err:    err,
		}
	}
	var st unix.Stat_t
	if err := fstatatNoFollow(skillsFD, name, &st); err != nil {
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectUnreadable,
			Err:    &iofs.PathError{Op: "lstat", Path: candPath, Err: err},
		}
	}

	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectLinked,
		}
	case unix.S_IFDIR:
	default:
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectNotDir,
		}
	}

	candFD, err := openatNoFollowDir(skillsFD, name)
	if err != nil {
		return ProjectSkill{}, classifyCandidateOpenError(name, candPath, err)
	}
	defer closeFD(candFD)

	if _, err := s.verifyFDScope(candFD, filepath.Join(realSkillsPath, name)); err != nil {
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectUnprovenScope,
			Err:    err,
		}
	}

	skillMDPath := filepath.Join(candPath, "SKILL.md")
	if err := s.guard.CheckLexical(skillMDPath); err != nil {
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectUnprovenScope,
			Err:    err,
		}
	}
	var skillSt unix.Stat_t
	if err := fstatatNoFollow(candFD, "SKILL.md", &skillSt); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ProjectSkill{}, &ProjectRejectedEntry{
				Name:   name,
				Path:   candPath,
				Reason: ProjectRejectMissingSkillMD,
			}
		}
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectUnreadable,
			Err:    &iofs.PathError{Op: "lstat", Path: skillMDPath, Err: err},
		}
	}

	switch skillSt.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return ProjectSkill{
			ID:          ProjectSkillID(kind, name),
			ProjectDir:  cleanProjectDir,
			Root:        kind,
			Name:        name,
			Path:        candPath,
			SkillMDPath: skillMDPath,
		}, nil
	case unix.S_IFLNK:
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectLinkedSkillMD,
		}
	default:
		return ProjectSkill{}, &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectNonRegularSkillMD,
		}
	}
}

func classifyCandidateOpenError(name, candPath string, err error) *ProjectRejectedEntry {
	switch {
	case errors.Is(err, unix.ELOOP):
		return &ProjectRejectedEntry{Name: name, Path: candPath, Reason: ProjectRejectLinked}
	case errors.Is(err, unix.ENOTDIR):
		return &ProjectRejectedEntry{Name: name, Path: candPath, Reason: ProjectRejectNotDir}
	default:
		return &ProjectRejectedEntry{
			Name:   name,
			Path:   candPath,
			Reason: ProjectRejectUnreadable,
			Err:    &iofs.PathError{Op: "open", Path: candPath, Err: err},
		}
	}
}

func openNoFollowDir(path string) (int, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	for {
		fd, err := unix.Open(path, flags, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

func openatNoFollowDir(dirFD int, name string) (int, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	for {
		fd, err := unix.Openat(dirFD, name, flags, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

func fstatatNoFollow(dirFD int, name string, st *unix.Stat_t) error {
	for {
		err := unix.Fstatat(dirFD, name, st, unix.AT_SYMLINK_NOFOLLOW)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func fdRealPath(fd int) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}

func closeFD(fd int) {
	_ = unix.Close(fd)
}
