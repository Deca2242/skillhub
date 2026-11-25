package fs

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"skillhub/internal/shared/paths"
)

var (
	// ErrProjectRootUnsupported reports a project root kind outside the approved
	// .agents/skills and .claude/skills roots.
	ErrProjectRootUnsupported = errors.New("unsupported project skills root")
	// ErrProjectSkillInvalid reports an invalid or inconsistent project skill
	// binding.
	ErrProjectSkillInvalid = errors.New("invalid project skill identity")
	// ErrProjectTreeLinked reports a symlink component or descendant inside an
	// approved project root or skill directory.
	ErrProjectTreeLinked = errors.New("symlink is not allowed in project skill tree")
	// ErrProjectTreeNotDir reports a non-directory where a directory was
	// expected inside a project skill tree.
	ErrProjectTreeNotDir = errors.New("expected directory in project skill tree")
	// ErrProjectTreeGit reports a .git entry inside a project skill tree.
	ErrProjectTreeGit = errors.New(".git entry is not allowed in project skill tree")
	// ErrProjectTreeUnsupported reports an unsupported entry type or invalid
	// entry name inside a project skill tree.
	ErrProjectTreeUnsupported = errors.New("unsupported entry in project skill tree")
	// ErrProjectTreeClosed reports an operation attempted after Close.
	ErrProjectTreeClosed = errors.New("project skill tree is closed")
)

// ProjectTree provides scoped, read-only, descriptor-relative one-shot
// traversal and file access for a single observed project skill directory.
type ProjectTree interface {
	ReadDir(name string) ([]iofs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	Stat(name string) (iofs.FileInfo, error)
	Close() error
	// Skill returns the canonical observed project skill bound to this tree.
	Skill() ProjectSkill
}

// ProjectTreeOpener opens a scoped, read-only ProjectTree bound to an exact
// observed project skill emitted by scanned.
type ProjectTreeOpener interface {
	Open(scanned ProjectSkills, skill ProjectSkill) (ProjectTree, error)
}

// ProjectTreeAdapter opens read-only, descriptor-relative project skill trees
// without following symlinks or accepting arbitrary path identities.
type ProjectTreeAdapter struct {
	scanner *ProjectSkillScanner
}

var _ ProjectTreeOpener = (*ProjectTreeAdapter)(nil)

// NewProjectTreeAdapter builds a ProjectTreeAdapter enforcing guard against
// relative and forbidden paths.
func NewProjectTreeAdapter(guard paths.Guard) *ProjectTreeAdapter {
	return &ProjectTreeAdapter{
		scanner: NewProjectSkillScanner(guard),
	}
}

// Open binds to the exact scanner-observed project skill identified by skill's
// canonical fields and confirmed to have been emitted in scanned for the same
// project directory. It opens every component without following symlinks, checks
// forbidden lexical prefixes, and verifies physical containment before
// returning a read-only one-shot ProjectTree.
func (a *ProjectTreeAdapter) Open(scanned ProjectSkills, skill ProjectSkill) (ProjectTree, error) {
	if a == nil || a.scanner == nil {
		return nil, fmt.Errorf("open project tree: %w: uninitialized adapter", ErrProjectSkillInvalid)
	}
	spec, ok := projectRootSpecByKind(skill.Root)
	if !ok {
		return nil, fmt.Errorf("open project tree: %w: %q", ErrProjectRootUnsupported, skill.Root)
	}
	cleanProjectDir, err := a.scanner.validateProjectDir(skill.ProjectDir)
	if err != nil {
		return nil, err
	}
	if skill.ProjectDir != cleanProjectDir {
		return nil, fmt.Errorf(
			"open project tree: %w: project dir %q does not match canonical %q",
			ErrProjectSkillInvalid,
			skill.ProjectDir,
			cleanProjectDir,
		)
	}
	if !validProjectChildName(skill.Name) {
		return nil, fmt.Errorf("open project tree: %w: invalid skill name %q", ErrProjectSkillInvalid, skill.Name)
	}

	bound, err := validateObservedBinding(cleanProjectDir, spec, skill)
	if err != nil {
		return nil, err
	}
	if err := a.scanner.guard.CheckLexical(bound.Path); err != nil {
		return nil, fmt.Errorf("open project tree %s: %w", bound.Path, err)
	}
	if err := a.scanner.guard.CheckLexical(bound.SkillMDPath); err != nil {
		return nil, fmt.Errorf("open project tree %s: %w", bound.SkillMDPath, err)
	}
	if err := verifyScannerProvenance(scanned, cleanProjectDir, bound); err != nil {
		return nil, err
	}

	projFD, err := a.scanner.openStartupDirNoFollow(cleanProjectDir)
	if err != nil {
		return nil, err
	}
	defer closeFD(projFD)

	realProjDir, err := a.scanner.verifyFDScope(projFD, cleanProjectDir)
	if err != nil {
		return nil, fmt.Errorf("open project tree %s: %w", bound.Path, err)
	}

	agentPath := filepath.Join(cleanProjectDir, spec.agent)
	realAgentDir := filepath.Join(realProjDir, spec.agent)
	agentFD, err := openVerifiedDirStep(
		a.scanner,
		"open",
		projFD,
		spec.agent,
		agentPath,
		realAgentDir,
		agentPath,
	)
	if err != nil {
		return nil, err
	}
	defer closeFD(agentFD)

	rootPath := filepath.Join(agentPath, spec.leaf)
	realRootDir := filepath.Join(realAgentDir, spec.leaf)
	skillsFD, err := openVerifiedDirStep(
		a.scanner,
		"open",
		agentFD,
		spec.leaf,
		rootPath,
		realRootDir,
		rootPath,
	)
	if err != nil {
		return nil, err
	}
	defer closeFD(skillsFD)

	realSkillDir := filepath.Join(realRootDir, bound.Name)
	skillFD, err := openVerifiedDirStep(
		a.scanner,
		"open",
		skillsFD,
		bound.Name,
		bound.Path,
		realSkillDir,
		bound.Path,
	)
	if err != nil {
		return nil, err
	}
	keepSkillFD := false
	defer func() {
		if !keepSkillFD {
			closeFD(skillFD)
		}
	}()

	if err := a.verifySkillMD(skillFD, bound.SkillMDPath); err != nil {
		return nil, err
	}

	keepSkillFD = true
	return &scopedProjectTree{
		scanner:       a.scanner,
		skill:         bound,
		realSkillPath: realSkillDir,
		rootFD:        skillFD,
	}, nil
}

func (a *ProjectTreeAdapter) verifySkillMD(skillFD int, skillMDPath string) error {
	st, err := statVerifiedEntry(a.scanner.guard, "open", skillFD, "SKILL.md", skillMDPath, skillMDPath)
	if err != nil {
		return err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return nil
	case unix.S_IFLNK:
		return fmt.Errorf("open project tree: %w: %s is a symlink", ErrProjectTreeLinked, skillMDPath)
	default:
		return fmt.Errorf("open project tree: %w: %s is not a regular file", ErrProjectTreeUnsupported, skillMDPath)
	}
}

func projectRootSpecByKind(kind ProjectRootKind) (projectRootSpec, bool) {
	for _, spec := range projectRootSpecs {
		if spec.kind == kind {
			return spec, true
		}
	}
	return projectRootSpec{}, false
}

func validProjectChildName(name string) bool {
	if name == "" || name == "." || name == ".." || name == ".git" || !utf8.ValidString(name) {
		return false
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, 0) {
		return false
	}
	return true
}

func validateObservedBinding(cleanProjectDir string, spec projectRootSpec, skill ProjectSkill) (ProjectSkill, error) {
	wantID := ProjectSkillID(spec.kind, skill.Name)
	wantPath := filepath.Join(cleanProjectDir, spec.agent, spec.leaf, skill.Name)
	wantSkillMD := filepath.Join(wantPath, "SKILL.md")

	if skill.ID != wantID {
		return ProjectSkill{}, fmt.Errorf("open project tree: %w: id %q does not match %q", ErrProjectSkillInvalid, skill.ID, wantID)
	}
	if skill.Path != wantPath {
		return ProjectSkill{}, fmt.Errorf("open project tree: %w: path %q does not match %q", ErrProjectSkillInvalid, skill.Path, wantPath)
	}
	if skill.SkillMDPath != wantSkillMD {
		return ProjectSkill{}, fmt.Errorf("open project tree: %w: SKILL.md path %q does not match %q", ErrProjectSkillInvalid, skill.SkillMDPath, wantSkillMD)
	}
	return ProjectSkill{
		ID:          wantID,
		ProjectDir:  cleanProjectDir,
		Root:        spec.kind,
		Name:        skill.Name,
		Path:        wantPath,
		SkillMDPath: wantSkillMD,
	}, nil
}

func verifyScannerProvenance(scanned ProjectSkills, cleanProjectDir string, bound ProjectSkill) error {
	if scanned.projectDir == "" || scanned.projectDir != cleanProjectDir {
		return fmt.Errorf(
			"open project tree: %w: selection %s does not belong to scanned project %q",
			ErrProjectSkillInvalid,
			bound.ID,
			cleanProjectDir,
		)
	}
	observed, ok := scanned.byID[bound.ID]
	if !ok || observed != bound {
		return fmt.Errorf(
			"open project tree: %w: selection %s was not emitted by scanner",
			ErrProjectSkillInvalid,
			bound.ID,
		)
	}
	return nil
}

type scopedProjectTree struct {
	mu            sync.Mutex
	scanner       *ProjectSkillScanner
	skill         ProjectSkill
	realSkillPath string
	rootFD        int
	closed        bool
}

var _ ProjectTree = (*scopedProjectTree)(nil)

func (t *scopedProjectTree) Skill() ProjectSkill {
	return t.skill
}

func (t *scopedProjectTree) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	fd := t.rootFD
	t.rootFD = -1
	if fd < 0 {
		return nil
	}
	return unix.Close(fd)
}

func (t *scopedProjectTree) Stat(name string) (iofs.FileInfo, error) {
	if err := validateRelPath("stat", name); err != nil {
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.ensureOpenLocked("stat", name); err != nil {
		return nil, err
	}
	if name == "." {
		var st unix.Stat_t
		if err := fstatFD(t.rootFD, &st); err != nil {
			return nil, mapTreeStatError("stat", name, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, &iofs.PathError{Op: "stat", Path: name, Err: ErrProjectTreeNotDir}
		}
		return fileInfoFromStat(t.skill.Name, st), nil
	}

	parentFD, leaf, leafLexical, _, err := t.openParentLocked("stat", name)
	if err != nil {
		return nil, err
	}
	defer t.closeTempFD(parentFD)

	st, err := statVerifiedEntry(t.scanner.guard, "stat", parentFD, leaf, leafLexical, name)
	if err != nil {
		return nil, err
	}
	if err := checkSupportedStat("stat", name, st); err != nil {
		return nil, err
	}
	return fileInfoFromStat(leaf, st), nil
}

func (t *scopedProjectTree) ReadDir(name string) ([]iofs.DirEntry, error) {
	if err := validateRelPath("readdir", name); err != nil {
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.ensureOpenLocked("readdir", name); err != nil {
		return nil, err
	}
	dirFD, dirLexical, dirReal, err := t.openRelDirLocked("readdir", name)
	if err != nil {
		return nil, err
	}
	dirFile := os.NewFile(uintptr(dirFD), dirLexical)
	defer func() { _ = dirFile.Close() }()

	entries, err := listVerifiedDirFile(t.scanner, dirFile, dirLexical, dirReal, name)
	if err != nil {
		return nil, err
	}
	if err := t.ensureOpenLocked("readdir", name); err != nil {
		return nil, err
	}
	return entries, nil
}

func (t *scopedProjectTree) ReadFile(name string) ([]byte, error) {
	if err := validateRelPath("read", name); err != nil {
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.ensureOpenLocked("read", name); err != nil {
		return nil, err
	}
	if name == "." {
		return nil, &iofs.PathError{Op: "read", Path: name, Err: unix.EISDIR}
	}

	parentFD, leaf, leafLexical, leafReal, err := t.openParentLocked("read", name)
	if err != nil {
		return nil, err
	}
	defer t.closeTempFD(parentFD)

	st, err := statVerifiedEntry(t.scanner.guard, "read", parentFD, leaf, leafLexical, name)
	if err != nil {
		return nil, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return nil, &iofs.PathError{Op: "read", Path: name, Err: ErrProjectTreeLinked}
	case unix.S_IFDIR:
		return nil, &iofs.PathError{Op: "read", Path: name, Err: unix.EISDIR}
	case unix.S_IFREG:
	default:
		return nil, &iofs.PathError{Op: "read", Path: name, Err: ErrProjectTreeUnsupported}
	}

	return t.readVerifiedFileLocked(parentFD, leaf, name, st, leafLexical, leafReal)
}

func (t *scopedProjectTree) readVerifiedFileLocked(
	parentFD int,
	leaf string,
	relPath string,
	st unix.Stat_t,
	lexicalPath string,
	wantRealPath string,
) ([]byte, error) {
	fileFD, err := openatNoFollowFile(parentFD, leaf)
	if err != nil {
		return nil, mapTreeStatError("read", relPath, err)
	}
	if err := verifyOpenedChild(t.scanner, fileFD, st, lexicalPath, wantRealPath); err != nil {
		closeFD(fileFD)
		return nil, err
	}

	file := os.NewFile(uintptr(fileFD), lexicalPath)
	data, readErr := io.ReadAll(file)
	scopeErr := t.ensureOpenLocked("read", relPath)
	if scopeErr == nil {
		if _, err := t.scanner.verifyFDScope(int(file.Fd()), wantRealPath); err != nil {
			scopeErr = fmt.Errorf("read %s: %w", relPath, err)
		}
	}
	closeErr := file.Close()

	if readErr != nil {
		return nil, readErr
	}
	if scopeErr != nil {
		return nil, scopeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

func (t *scopedProjectTree) ensureOpenLocked(op, relPath string) error {
	if t.closed || t.rootFD < 0 {
		return &iofs.PathError{Op: op, Path: relPath, Err: ErrProjectTreeClosed}
	}
	if err := t.scanner.guard.CheckLexical(t.skill.Path); err != nil {
		return fmt.Errorf("%s %s: %w", op, relPath, err)
	}
	if _, err := t.scanner.verifyFDScope(t.rootFD, t.realSkillPath); err != nil {
		return fmt.Errorf("%s %s: %w", op, relPath, err)
	}
	return nil
}

func (t *scopedProjectTree) closeTempFD(fd int) {
	if fd >= 0 && fd != t.rootFD {
		closeFD(fd)
	}
}

func (t *scopedProjectTree) openParentLocked(op, relPath string) (int, string, string, string, error) {
	parts := strings.Split(relPath, "/")
	curFD := t.rootFD
	curLexical := t.skill.Path
	curReal := t.realSkillPath

	for _, seg := range parts[:len(parts)-1] {
		nextLexical := filepath.Join(curLexical, seg)
		nextReal := filepath.Join(curReal, seg)
		nextFD, err := openVerifiedDirStep(t.scanner, op, curFD, seg, nextLexical, nextReal, nextLexical)
		t.closeTempFD(curFD)
		if err != nil {
			return -1, "", "", "", err
		}
		curFD = nextFD
		curLexical = nextLexical
		curReal = nextReal
	}

	leaf := parts[len(parts)-1]
	leafLexical := filepath.Join(curLexical, leaf)
	if err := t.scanner.guard.CheckLexical(leafLexical); err != nil {
		t.closeTempFD(curFD)
		return -1, "", "", "", fmt.Errorf("%s %s: %w", op, leafLexical, err)
	}
	return curFD, leaf, leafLexical, filepath.Join(curReal, leaf), nil
}

func (t *scopedProjectTree) openRelDirLocked(op, name string) (int, string, string, error) {
	if name == "." {
		var rootSt unix.Stat_t
		if err := fstatFD(t.rootFD, &rootSt); err != nil {
			return -1, "", "", mapTreeStatError(op, name, err)
		}
		if rootSt.Mode&unix.S_IFMT != unix.S_IFDIR {
			return -1, "", "", &iofs.PathError{Op: op, Path: name, Err: ErrProjectTreeNotDir}
		}
		dirFD, err := openVerifiedDirAt(
			t.scanner,
			op,
			t.rootFD,
			".",
			rootSt,
			t.skill.Path,
			t.realSkillPath,
		)
		if err != nil {
			return -1, "", "", err
		}
		return dirFD, t.skill.Path, t.realSkillPath, nil
	}

	parentFD, leaf, leafLexical, leafReal, err := t.openParentLocked(op, name)
	if err != nil {
		return -1, "", "", err
	}
	defer t.closeTempFD(parentFD)

	dirFD, err := openVerifiedDirStep(t.scanner, op, parentFD, leaf, leafLexical, leafReal, name)
	if err != nil {
		return -1, "", "", err
	}
	return dirFD, leafLexical, leafReal, nil
}

func validateRelPath(op, name string) error {
	if !iofs.ValidPath(name) || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return &iofs.PathError{Op: op, Path: name, Err: iofs.ErrInvalid}
	}
	if name == "." {
		return nil
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".git" {
			return &iofs.PathError{Op: op, Path: name, Err: ErrProjectTreeGit}
		}
	}
	return nil
}

func listVerifiedDirFile(
	scanner *ProjectSkillScanner,
	dirFile *os.File,
	dirLexical string,
	dirReal string,
	relPath string,
) ([]iofs.DirEntry, error) {
	if err := scanner.guard.CheckLexical(dirLexical); err != nil {
		return nil, fmt.Errorf("readdir %s: %w", relPath, err)
	}
	rawFD := int(dirFile.Fd())
	if _, err := scanner.verifyFDScope(rawFD, dirReal); err != nil {
		return nil, fmt.Errorf("readdir %s: %w", relPath, err)
	}
	names, err := dirFile.Readdirnames(-1)
	if err != nil {
		return nil, &iofs.PathError{Op: "readdir", Path: relPath, Err: err}
	}
	slices.Sort(names)

	entries := make([]iofs.DirEntry, 0, len(names))
	for _, entryName := range names {
		entryLexical := filepath.Join(dirLexical, entryName)
		if entryName == ".git" {
			return nil, &iofs.PathError{Op: "readdir", Path: entryLexical, Err: ErrProjectTreeGit}
		}
		if !validProjectChildName(entryName) {
			return nil, &iofs.PathError{Op: "readdir", Path: entryLexical, Err: ErrProjectTreeUnsupported}
		}
		st, err := statVerifiedEntry(scanner.guard, "readdir", rawFD, entryName, entryLexical, entryLexical)
		if err != nil {
			return nil, err
		}
		if err := checkSupportedStat("readdir", entryLexical, st); err != nil {
			return nil, err
		}
		entries = append(entries, projectDirEntry{
			info: fileInfoFromStat(entryName, st),
		})
	}
	if _, err := scanner.verifyFDScope(rawFD, dirReal); err != nil {
		return nil, fmt.Errorf("readdir %s: %w", relPath, err)
	}
	return entries, nil
}

func statVerifiedEntry(
	guard paths.Guard,
	op string,
	parentFD int,
	name string,
	lexicalPath string,
	errPath string,
) (unix.Stat_t, error) {
	if err := guard.CheckLexical(lexicalPath); err != nil {
		return unix.Stat_t{}, fmt.Errorf("%s %s: %w", op, errPath, err)
	}
	var st unix.Stat_t
	if err := fstatatNoFollow(parentFD, name, &st); err != nil {
		return unix.Stat_t{}, mapTreeStatError(op, errPath, err)
	}
	return st, nil
}

func openVerifiedDirStep(
	scanner *ProjectSkillScanner,
	op string,
	parentFD int,
	name string,
	lexicalPath string,
	wantRealPath string,
	errPath string,
) (int, error) {
	st, err := statVerifiedEntry(scanner.guard, op, parentFD, name, lexicalPath, errPath)
	if err != nil {
		return -1, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return -1, &iofs.PathError{Op: op, Path: errPath, Err: ErrProjectTreeLinked}
	case unix.S_IFDIR:
	default:
		return -1, &iofs.PathError{Op: op, Path: errPath, Err: ErrProjectTreeNotDir}
	}
	return openVerifiedDirAt(scanner, op, parentFD, name, st, lexicalPath, wantRealPath)
}

func checkSupportedStat(op, path string, st unix.Stat_t) error {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return &iofs.PathError{Op: op, Path: path, Err: ErrProjectTreeLinked}
	case unix.S_IFDIR, unix.S_IFREG:
		return nil
	default:
		return &iofs.PathError{Op: op, Path: path, Err: ErrProjectTreeUnsupported}
	}
}

func verifyOpenedChild(
	scanner *ProjectSkillScanner,
	fd int,
	wantStat unix.Stat_t,
	lexicalPath string,
	wantRealPath string,
) error {
	if err := verifySameInode(fd, wantStat, lexicalPath); err != nil {
		return err
	}
	if _, err := scanner.verifyFDScope(fd, wantRealPath); err != nil {
		return fmt.Errorf("verify scope %s: %w", lexicalPath, err)
	}
	return nil
}

func verifySameInode(fd int, want unix.Stat_t, path string) error {
	var got unix.Stat_t
	if err := fstatFD(fd, &got); err != nil {
		return mapTreeStatError("fstat", path, err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino || (got.Mode&unix.S_IFMT) != (want.Mode&unix.S_IFMT) {
		return fmt.Errorf("%w: %s changed while opening", ErrProjectScopeUnproven, path)
	}
	return nil
}

func openVerifiedDirAt(
	scanner *ProjectSkillScanner,
	op string,
	parentFD int,
	name string,
	st unix.Stat_t,
	lexicalPath string,
	wantRealPath string,
) (int, error) {
	dirFD, err := openatNoFollowDir(parentFD, name)
	if err != nil {
		return -1, mapTreeStatError(op, lexicalPath, err)
	}
	if err := verifyOpenedChild(scanner, dirFD, st, lexicalPath, wantRealPath); err != nil {
		closeFD(dirFD)
		return -1, err
	}
	return dirFD, nil
}

func openatNoFollowFile(dirFD int, name string) (int, error) {
	const flags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	for {
		fd, err := unix.Openat(dirFD, name, flags, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

func fstatFD(fd int, st *unix.Stat_t) error {
	for {
		err := unix.Fstat(fd, st)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func mapTreeStatError(op, path string, err error) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return &iofs.PathError{Op: op, Path: path, Err: ErrProjectTreeLinked}
	case errors.Is(err, unix.ENOTDIR):
		return &iofs.PathError{Op: op, Path: path, Err: ErrProjectTreeNotDir}
	default:
		return &iofs.PathError{Op: op, Path: path, Err: err}
	}
}

func fileInfoFromStat(name string, st unix.Stat_t) projectFileInfo {
	mode := iofs.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= iofs.ModeDir
	case unix.S_IFLNK:
		mode |= iofs.ModeSymlink
	case unix.S_IFIFO:
		mode |= iofs.ModeNamedPipe
	case unix.S_IFSOCK:
		mode |= iofs.ModeSocket
	case unix.S_IFCHR:
		mode |= iofs.ModeDevice | iofs.ModeCharDevice
	case unix.S_IFBLK:
		mode |= iofs.ModeDevice
	}
	if st.Mode&unix.S_ISUID != 0 {
		mode |= iofs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		mode |= iofs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		mode |= iofs.ModeSticky
	}
	var size int64
	if st.Mode&unix.S_IFMT == unix.S_IFREG {
		size = st.Size
	}
	return projectFileInfo{
		name:    name,
		size:    size,
		mode:    mode,
		modTime: time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)),
	}
}

type projectFileInfo struct {
	name    string
	size    int64
	mode    iofs.FileMode
	modTime time.Time
}

func (fi projectFileInfo) Name() string        { return fi.name }
func (fi projectFileInfo) Size() int64         { return fi.size }
func (fi projectFileInfo) Mode() iofs.FileMode { return fi.mode }
func (fi projectFileInfo) ModTime() time.Time  { return fi.modTime }
func (fi projectFileInfo) IsDir() bool         { return fi.mode.IsDir() }
func (fi projectFileInfo) Sys() any            { return nil }

type projectDirEntry struct {
	info projectFileInfo
}

func (de projectDirEntry) Name() string                 { return de.info.name }
func (de projectDirEntry) IsDir() bool                  { return de.info.IsDir() }
func (de projectDirEntry) Type() iofs.FileMode          { return de.info.mode.Type() }
func (de projectDirEntry) Info() (iofs.FileInfo, error) { return de.info, nil }
