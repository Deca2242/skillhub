package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

const specialTreeBits = iofs.ModeSetuid | iofs.ModeSetgid | iofs.ModeSticky

type renameat2Func func(olddirfd int, oldpath string, newdirfd int, newpath string, flags uint) error

// ProjectOps executes descriptor-confined inspection, backup copying, removal to
// trash, and staging restoration for exact skill directories in the current
// startup project without following symlinks.
type ProjectOps struct {
	guard   paths.Guard
	scanner *ProjectSkillScanner
	trees   *ProjectTreeAdapter
	rename  renameat2Func
}

// NewProjectOps returns a ProjectOps enforcing guard against relative and
// forbidden paths.
func NewProjectOps(guard paths.Guard) *ProjectOps {
	return &ProjectOps{
		guard:   guard,
		scanner: NewProjectSkillScanner(guard),
		trees:   NewProjectTreeAdapter(guard),
		rename:  unix.Renameat2,
	}
}

// Scan discovers eligible direct skill directories and rejected entries under
// the current startup project directory without following symlinks.
func (p *ProjectOps) Scan(projectDir string) (ProjectSkills, error) {
	if p == nil || p.scanner == nil {
		return ProjectSkills{}, fmt.Errorf("scan project skills: %w: uninitialized ops", ErrProjectSkillInvalid)
	}
	return p.scanner.Scan(projectDir)
}

// OpenTree opens a read-only descriptor-confined ProjectTree for a skill
// emitted by scanned.
func (p *ProjectOps) OpenTree(scanned ProjectSkills, skill ProjectSkill) (ProjectTree, error) {
	if p == nil || p.trees == nil {
		return nil, fmt.Errorf("open project tree: %w: uninitialized ops", ErrProjectSkillInvalid)
	}
	return p.trees.Open(scanned, skill)
}

// WalkProject scans projectDir without following symlinks and invokes onSkill for
// each eligible direct skill directory and onRejected for rejected roots or entries.
func (p *ProjectOps) WalkProject(
	projectDir string,
	onSkill func(id, root, name, path string),
	onRejected func(root, name, path, reason string),
) error {
	scanned, err := p.Scan(projectDir)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) ||
			errors.Is(err, ErrProjectDirLinked) ||
			errors.Is(err, ErrProjectDirNotDir) ||
			errors.Is(err, ErrProjectScopeUnproven) ||
			errors.Is(err, paths.ErrForbidden) {
			return nil
		}
		return err
	}
	if onSkill != nil {
		for _, sk := range scanned.Skills() {
			onSkill(sk.ID, string(sk.Root), sk.Name, sk.Path)
		}
	}
	if onRejected != nil {
		for _, r := range scanned.Roots() {
			if r.Status == ProjectRootRejected {
				onRejected(string(r.Kind), "", r.Path, string(r.Reason))
			}
			for _, rej := range r.Rejected {
				onRejected(string(r.Kind), rej.Name, rej.Path, string(rej.Reason))
			}
		}
	}
	return nil
}

// InspectSkillTree inspects one exact copy under projectDir/root/name and returns
// its canonical path, verified treehash (when eligible), and a machine-readable
// rejection kind ("" when eligible; otherwise "not_found", "linked", "git",
// "not_dir", "invalid", "unsupported", or "unproven").
func (p *ProjectOps) InspectSkillTree(projectDir, root, name string) (skillPath string, tree treehash.Tree, rejectKind string, err error) {
	spec, cleanProj, err := p.validateArgs("inspect", projectDir, root, name)
	if err != nil {
		switch {
		case errors.Is(err, paths.ErrForbidden):
			return "", treehash.Tree{}, "unproven", err
		default:
			return "", treehash.Tree{}, "invalid", err
		}
	}
	skillPath = filepath.Join(cleanProj, spec.agent, spec.leaf, name)
	exists, foreign, tr, obsErr := p.ObserveSkill(cleanProj, string(spec.kind), name)
	if obsErr != nil {
		switch {
		case errors.Is(obsErr, iofs.ErrNotExist):
			return skillPath, treehash.Tree{}, "not_found", obsErr
		case errors.Is(obsErr, ErrProjectDirLinked), errors.Is(obsErr, ErrProjectTreeLinked):
			return skillPath, treehash.Tree{}, "linked", obsErr
		case errors.Is(obsErr, ErrProjectTreeGit):
			return skillPath, treehash.Tree{}, "git", obsErr
		case errors.Is(obsErr, ErrProjectDirNotDir), errors.Is(obsErr, ErrProjectTreeNotDir):
			return skillPath, treehash.Tree{}, "not_dir", obsErr
		case errors.Is(obsErr, ErrProjectTreeUnsupported):
			return skillPath, treehash.Tree{}, "unsupported", obsErr
		case errors.Is(obsErr, ErrProjectScopeUnproven), errors.Is(obsErr, paths.ErrForbidden):
			return skillPath, treehash.Tree{}, "unproven", obsErr
		default:
			return skillPath, treehash.Tree{}, "error", obsErr
		}
	}
	if !exists {
		return skillPath, treehash.Tree{}, "not_found", nil
	}
	if foreign {
		if _, clsErr := p.classifyMissingObserved(cleanProj, spec.kind, name); clsErr != nil {
			switch {
			case errors.Is(clsErr, ErrProjectTreeLinked):
				return skillPath, treehash.Tree{}, "linked", clsErr
			case errors.Is(clsErr, ErrProjectTreeNotDir):
				return skillPath, treehash.Tree{}, "not_dir", clsErr
			case errors.Is(clsErr, ErrProjectTreeUnsupported):
				return skillPath, treehash.Tree{}, "unsupported", clsErr
			case errors.Is(clsErr, iofs.ErrNotExist):
				return skillPath, treehash.Tree{}, "invalid", clsErr
			}
		}
		return skillPath, treehash.Tree{}, "invalid", nil
	}
	return skillPath, tr, "", nil
}

// ObserveSkill inspects one exact slot under projectDir/root/name without
// following symlinks. Absent skill or missing root returns exists=false;
// non-directory or missing/non-regular SKILL.md occupants return foreign=true;
// eligible directories with special mode bits, .git, FIFO, or symlink
// descendants fail explicitly.
func (p *ProjectOps) ObserveSkill(projectDir, root, name string) (exists bool, foreign bool, tree treehash.Tree, err error) {
	spec, cleanProj, err := p.validateArgs("observe", projectDir, root, name)
	if err != nil {
		return false, false, treehash.Tree{}, err
	}
	skillPath := filepath.Join(cleanProj, spec.agent, spec.leaf, name)
	if err := p.guard.CheckLexical(skillPath); err != nil {
		return false, false, treehash.Tree{}, fmt.Errorf("observe %s: %w", skillPath, err)
	}

	projFD, err := p.scanner.openStartupDirNoFollow(cleanProj)
	if err != nil {
		return false, false, treehash.Tree{}, err
	}
	realProjDir, err := p.scanner.verifyFDScope(projFD, cleanProj)
	if err != nil {
		closeFD(projFD)
		return false, false, treehash.Tree{}, fmt.Errorf("observe %s: %w", skillPath, err)
	}

	agentPath := filepath.Join(cleanProj, spec.agent)
	realAgentDir := filepath.Join(realProjDir, spec.agent)
	agentFD, err := openVerifiedDirStep(p.scanner, "observe", projFD, spec.agent, agentPath, realAgentDir, agentPath)
	closeFD(projFD)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, false, treehash.Tree{}, nil
		}
		return false, false, treehash.Tree{}, err
	}

	rootPath := filepath.Join(agentPath, spec.leaf)
	realRootDir := filepath.Join(realAgentDir, spec.leaf)
	skillsFD, err := openVerifiedDirStep(p.scanner, "observe", agentFD, spec.leaf, rootPath, realRootDir, rootPath)
	closeFD(agentFD)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, false, treehash.Tree{}, nil
		}
		return false, false, treehash.Tree{}, err
	}

	var st unix.Stat_t
	err = fstatatNoFollow(skillsFD, name, &st)
	if errors.Is(err, unix.ENOENT) {
		closeFD(skillsFD)
		return false, false, treehash.Tree{}, nil
	}
	if err != nil {
		closeFD(skillsFD)
		return false, false, treehash.Tree{}, &iofs.PathError{Op: "lstat", Path: skillPath, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		closeFD(skillsFD)
		return true, true, treehash.Tree{}, nil
	}

	skillFD, err := openVerifiedDirStep(p.scanner, "observe", skillsFD, name, skillPath, filepath.Join(realRootDir, name), skillPath)
	closeFD(skillsFD)
	if err != nil {
		return false, false, treehash.Tree{}, err
	}
	var skillMDStat unix.Stat_t
	err = fstatatNoFollow(skillFD, "SKILL.md", &skillMDStat)
	closeFD(skillFD)
	if errors.Is(err, unix.ENOENT) || (err == nil && skillMDStat.Mode&unix.S_IFMT != unix.S_IFREG) {
		return true, true, treehash.Tree{}, nil
	}
	if err != nil {
		return false, false, treehash.Tree{}, &iofs.PathError{Op: "lstat", Path: filepath.Join(skillPath, "SKILL.md"), Err: err}
	}

	pt, err := p.openObservedTree(cleanProj, spec.kind, name)
	if err != nil {
		return false, false, treehash.Tree{}, err
	}
	defer func() { _ = pt.Close() }()

	if err := checkProjectTreeClean(pt, skillPath); err != nil {
		return false, false, treehash.Tree{}, err
	}
	tr, err := treehash.Hash(pt)
	if err != nil {
		return false, false, treehash.Tree{}, err
	}
	return true, false, tr, nil
}

// SameDevice reports whether the deepest existing verified component of
// projectDir/root shares a filesystem device with otherDir (or its deepest
// existing ancestor).
func (p *ProjectOps) SameDevice(projectDir, root, otherDir string) (bool, error) {
	if p == nil || p.scanner == nil {
		return false, fmt.Errorf("same device: %w: uninitialized ops", ErrProjectSkillInvalid)
	}
	spec, ok := projectRootSpecByKind(ProjectRootKind(root))
	if !ok {
		return false, fmt.Errorf("same device: %w: %q", ErrProjectRootUnsupported, root)
	}
	cleanProj, err := p.scanner.validateProjectDir(projectDir)
	if err != nil {
		return false, err
	}
	cleanOther := filepath.Clean(otherDir)
	if !filepath.IsAbs(cleanOther) {
		return false, fmt.Errorf("same device: %w: %q is not absolute", paths.ErrInvalidPath, otherDir)
	}
	if err := p.guard.Check(cleanOther); err != nil {
		return false, err
	}

	projFD, err := p.scanner.openStartupDirNoFollow(cleanProj)
	if err != nil {
		return false, err
	}
	realProjDir, err := p.scanner.verifyFDScope(projFD, cleanProj)
	if err != nil {
		closeFD(projFD)
		return false, err
	}
	devFD := projFD

	agentPath := filepath.Join(cleanProj, spec.agent)
	realAgentDir := filepath.Join(realProjDir, spec.agent)
	agentFD, err := openVerifiedDirStep(p.scanner, "same-device", projFD, spec.agent, agentPath, realAgentDir, agentPath)
	if err != nil {
		if !errors.Is(err, iofs.ErrNotExist) {
			closeFD(projFD)
			return false, err
		}
	} else {
		closeFD(devFD)
		devFD = agentFD
		rootPath := filepath.Join(agentPath, spec.leaf)
		realRootDir := filepath.Join(realAgentDir, spec.leaf)
		skillsFD, err := openVerifiedDirStep(p.scanner, "same-device", agentFD, spec.leaf, rootPath, realRootDir, rootPath)
		if err != nil {
			if !errors.Is(err, iofs.ErrNotExist) {
				closeFD(devFD)
				return false, err
			}
		} else {
			closeFD(devFD)
			devFD = skillsFD
		}
	}

	var projStat unix.Stat_t
	fstatErr := unix.Fstat(devFD, &projStat)
	closeFD(devFD)
	if fstatErr != nil {
		return false, &iofs.PathError{Op: "fstat", Path: cleanProj, Err: fstatErr}
	}

	otherStat, err := p.statDeepestAncestor(cleanOther)
	if err != nil {
		return false, err
	}
	return uint64(projStat.Dev) == otherStat.Dev, nil
}

// CopySkill copies an exact scanner-observed project skill through ProjectTree
// to dstDir (which must not exist), preserving modes and verifying the copy's
// treehash matches the source.
func (p *ProjectOps) CopySkill(projectDir, root, name, dstDir string) (treehash.Tree, error) {
	spec, cleanProj, err := p.validateArgs("copy", projectDir, root, name)
	if err != nil {
		return treehash.Tree{}, err
	}
	cleanDst := filepath.Clean(dstDir)
	if !filepath.IsAbs(cleanDst) {
		return treehash.Tree{}, fmt.Errorf("copy project skill: %w: %q is not absolute", paths.ErrInvalidPath, dstDir)
	}
	if err := p.guard.Check(cleanDst); err != nil {
		return treehash.Tree{}, err
	}
	if _, err := os.Lstat(cleanDst); err == nil {
		return treehash.Tree{}, &iofs.PathError{Op: "copy", Path: cleanDst, Err: iofs.ErrExist}
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return treehash.Tree{}, &iofs.PathError{Op: "lstat", Path: cleanDst, Err: err}
	}

	pt, err := p.openObservedTree(cleanProj, spec.kind, name)
	if err != nil {
		return treehash.Tree{}, err
	}
	defer func() { _ = pt.Close() }()

	if err := checkProjectTreeClean(pt, pt.Skill().Path); err != nil {
		return treehash.Tree{}, err
	}
	srcHash, err := treehash.Hash(pt)
	if err != nil {
		return treehash.Tree{}, err
	}

	var dirs []dirPermRecord
	if err := copyProjectTreeRecursive(pt, ".", cleanDst, &dirs); err != nil {
		_ = os.RemoveAll(cleanDst)
		return treehash.Tree{}, err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].perm); err != nil {
			_ = os.RemoveAll(cleanDst)
			return treehash.Tree{}, err
		}
		if err := fsyncDirPath(dirs[i].path); err != nil {
			_ = os.RemoveAll(cleanDst)
			return treehash.Tree{}, err
		}
	}
	if err := fsyncDirPath(filepath.Dir(cleanDst)); err != nil {
		_ = os.RemoveAll(cleanDst)
		return treehash.Tree{}, err
	}

	dstHash, err := treehash.Dir(cleanDst)
	if err != nil {
		_ = os.RemoveAll(cleanDst)
		return treehash.Tree{}, err
	}
	if dstHash.SHA != srcHash.SHA || !slices.Equal(dstHash.Nodes, srcHash.Nodes) {
		_ = os.RemoveAll(cleanDst)
		return treehash.Tree{}, fmt.Errorf("copy %s: hash mismatch %s vs %s", pt.Skill().Path, dstHash.SHA, srcHash.SHA)
	}
	return dstHash, nil
}

// RemoveSkillToTrash verifies the project skill via ProjectTree, closes all
// descendant descriptors, and atomically moves the exact folder to trashDest
// using RENAME_NOREPLACE without overwriting existing entries.
func (p *ProjectOps) RemoveSkillToTrash(projectDir, root, name, trashDest string) error {
	spec, cleanProj, err := p.validateArgs("remove", projectDir, root, name)
	if err != nil {
		return err
	}
	cleanTrash := filepath.Clean(trashDest)
	if !filepath.IsAbs(cleanTrash) {
		return fmt.Errorf("remove project skill: %w: %q is not absolute", paths.ErrInvalidPath, trashDest)
	}
	if err := p.guard.Check(cleanTrash); err != nil {
		return err
	}
	if _, err := os.Lstat(cleanTrash); err == nil {
		return &iofs.PathError{Op: "remove", Path: cleanTrash, Err: iofs.ErrExist}
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return &iofs.PathError{Op: "lstat", Path: cleanTrash, Err: err}
	}

	// Verify readability, no .git, no symlink descendants, and no special bits
	// before moving anything out of the project directory.
	pt, err := p.openObservedTree(cleanProj, spec.kind, name)
	if err != nil {
		return err
	}
	verifyErr := checkProjectTreeClean(pt, pt.Skill().Path)
	if verifyErr == nil {
		_, verifyErr = treehash.Hash(pt)
	}
	if closeErr := pt.Close(); verifyErr != nil || closeErr != nil {
		return errors.Join(verifyErr, closeErr)
	}

	skillsFD, skillPath, err := p.openVerifiedSkillsRoot(cleanProj, spec, "remove")
	if err != nil {
		return err
	}
	defer closeFD(skillsFD)

	var st unix.Stat_t
	targetPath := filepath.Join(skillPath, name)
	if err := fstatatNoFollow(skillsFD, name, &st); err != nil {
		return &iofs.PathError{Op: "lstat", Path: targetPath, Err: err}
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return &iofs.PathError{Op: "remove", Path: targetPath, Err: ErrProjectTreeLinked}
	case unix.S_IFDIR:
	default:
		return &iofs.PathError{Op: "remove", Path: targetPath, Err: ErrProjectTreeNotDir}
	}

	trashParentFD, err := openGuardedExistingDir(p.guard, "remove", filepath.Dir(cleanTrash))
	if err != nil {
		return err
	}
	defer closeFD(trashParentFD)

	if err := p.rename(skillsFD, name, trashParentFD, filepath.Base(cleanTrash), unix.RENAME_NOREPLACE); err != nil {
		return mapRenameError("remove", targetPath, cleanTrash, err)
	}
	return errors.Join(
		syncFD("remove", skillPath, skillsFD),
		syncFD("remove", filepath.Dir(cleanTrash), trashParentFD),
	)
}

// RestoreSkillFromStaging validates stagingSrc recursively and atomically
// restores it to projectDir/root/name using RENAME_NOREPLACE, recreating missing
// .agents/.claude/skills directories without following symlinks.
func (p *ProjectOps) RestoreSkillFromStaging(stagingSrc, projectDir, root, name string) error {
	spec, cleanProj, err := p.validateArgs("restore", projectDir, root, name)
	if err != nil {
		return err
	}
	cleanStaging := filepath.Clean(stagingSrc)
	if !filepath.IsAbs(cleanStaging) {
		return fmt.Errorf("restore project skill: %w: %q is not absolute", paths.ErrInvalidPath, stagingSrc)
	}
	if err := p.guard.Check(cleanStaging); err != nil {
		return err
	}
	if err := verifyStagingDirClean(cleanStaging); err != nil {
		return err
	}

	stagingParentFD, err := openGuardedExistingDir(p.guard, "restore", filepath.Dir(cleanStaging))
	if err != nil {
		return err
	}
	defer closeFD(stagingParentFD)

	projFD, err := p.scanner.openStartupDirNoFollow(cleanProj)
	if err != nil {
		return err
	}
	realProjDir, err := p.scanner.verifyFDScope(projFD, cleanProj)
	if err != nil {
		closeFD(projFD)
		return fmt.Errorf("restore %s: %w", filepath.Join(cleanProj, spec.agent, spec.leaf, name), err)
	}

	agentPath := filepath.Join(cleanProj, spec.agent)
	realAgentDir := filepath.Join(realProjDir, spec.agent)
	agentFD, err := ensureVerifiedDirStep(p.scanner, "restore", projFD, spec.agent, agentPath, realAgentDir)
	closeFD(projFD)
	if err != nil {
		return err
	}

	rootPath := filepath.Join(agentPath, spec.leaf)
	realRootDir := filepath.Join(realAgentDir, spec.leaf)
	skillsFD, err := ensureVerifiedDirStep(p.scanner, "restore", agentFD, spec.leaf, rootPath, realRootDir)
	closeFD(agentFD)
	if err != nil {
		return err
	}
	defer closeFD(skillsFD)

	targetPath := filepath.Join(rootPath, name)
	var st unix.Stat_t
	err = fstatatNoFollow(skillsFD, name, &st)
	if err == nil {
		return &iofs.PathError{Op: "restore", Path: targetPath, Err: iofs.ErrExist}
	}
	if !errors.Is(err, unix.ENOENT) {
		return &iofs.PathError{Op: "lstat", Path: targetPath, Err: err}
	}

	if err := p.rename(stagingParentFD, filepath.Base(cleanStaging), skillsFD, name, unix.RENAME_NOREPLACE); err != nil {
		return mapRenameError("restore", cleanStaging, targetPath, err)
	}
	return errors.Join(
		syncFD("restore", rootPath, skillsFD),
		syncFD("restore", filepath.Dir(cleanStaging), stagingParentFD),
	)
}

func (p *ProjectOps) validateArgs(op, projectDir, root, name string) (projectRootSpec, string, error) {
	if p == nil || p.scanner == nil || p.trees == nil || p.rename == nil {
		return projectRootSpec{}, "", fmt.Errorf("%s project skill: %w: uninitialized ops", op, ErrProjectSkillInvalid)
	}
	spec, ok := projectRootSpecByKind(ProjectRootKind(root))
	if !ok {
		return projectRootSpec{}, "", fmt.Errorf("%s project skill: %w: %q", op, ErrProjectRootUnsupported, root)
	}
	if !validProjectChildName(name) {
		return projectRootSpec{}, "", fmt.Errorf("%s project skill: %w: invalid skill name %q", op, ErrProjectSkillInvalid, name)
	}
	cleanProj, err := p.scanner.validateProjectDir(projectDir)
	if err != nil {
		return projectRootSpec{}, "", err
	}
	if projectDir != cleanProj {
		return projectRootSpec{}, "", fmt.Errorf("%s project skill: %w: project dir %q does not match canonical %q", op, ErrProjectSkillInvalid, projectDir, cleanProj)
	}
	return spec, cleanProj, nil
}

func (p *ProjectOps) openObservedTree(cleanProj string, root ProjectRootKind, name string) (ProjectTree, error) {
	scanned, err := p.scanner.Scan(cleanProj)
	if err != nil {
		return nil, err
	}
	id := ProjectSkillID(root, name)
	bound, ok := scanned.Lookup(id)
	if !ok {
		return p.classifyMissingObserved(cleanProj, root, name)
	}
	return p.trees.Open(scanned, bound)
}

// classifyMissingObserved diagnoses why an existing project skill slot was not
// emitted by Scan (symlink root/entry, non-dir root, missing SKILL.md, etc.).
func (p *ProjectOps) classifyMissingObserved(cleanProj string, root ProjectRootKind, name string) (ProjectTree, error) {
	spec, _ := projectRootSpecByKind(root)
	skillPath := filepath.Join(cleanProj, spec.agent, spec.leaf, name)
	skillsFD, _, err := p.openVerifiedSkillsRoot(cleanProj, spec, "open")
	if err != nil {
		return nil, err
	}
	defer closeFD(skillsFD)

	var st unix.Stat_t
	if err := fstatatNoFollow(skillsFD, name, &st); err != nil {
		return nil, &iofs.PathError{Op: "lstat", Path: skillPath, Err: err}
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return nil, &iofs.PathError{Op: "open", Path: skillPath, Err: ErrProjectTreeLinked}
	case unix.S_IFDIR:
	default:
		return nil, &iofs.PathError{Op: "open", Path: skillPath, Err: ErrProjectTreeNotDir}
	}

	skillFD, err := openVerifiedDirStep(p.scanner, "open", skillsFD, name, skillPath, filepath.Join(cleanProj, spec.agent, spec.leaf, name), skillPath)
	if err != nil {
		return nil, err
	}
	defer closeFD(skillFD)

	var skillMD unix.Stat_t
	if err := fstatatNoFollow(skillFD, "SKILL.md", &skillMD); err != nil {
		return nil, &iofs.PathError{Op: "lstat", Path: filepath.Join(skillPath, "SKILL.md"), Err: err}
	}
	switch skillMD.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return nil, &iofs.PathError{Op: "open", Path: filepath.Join(skillPath, "SKILL.md"), Err: ErrProjectTreeLinked}
	case unix.S_IFREG:
		return nil, fmt.Errorf("open project tree %s: %w", skillPath, ErrProjectSkillInvalid)
	default:
		return nil, &iofs.PathError{Op: "open", Path: filepath.Join(skillPath, "SKILL.md"), Err: ErrProjectTreeUnsupported}
	}
}

func (p *ProjectOps) openVerifiedSkillsRoot(cleanProj string, spec projectRootSpec, op string) (int, string, error) {
	projFD, err := p.scanner.openStartupDirNoFollow(cleanProj)
	if err != nil {
		return -1, "", err
	}
	realProjDir, err := p.scanner.verifyFDScope(projFD, cleanProj)
	if err != nil {
		closeFD(projFD)
		return -1, "", fmt.Errorf("%s %s: %w", op, cleanProj, err)
	}

	agentPath := filepath.Join(cleanProj, spec.agent)
	realAgentDir := filepath.Join(realProjDir, spec.agent)
	agentFD, err := openVerifiedDirStep(p.scanner, op, projFD, spec.agent, agentPath, realAgentDir, agentPath)
	closeFD(projFD)
	if err != nil {
		return -1, "", err
	}

	rootPath := filepath.Join(agentPath, spec.leaf)
	realRootDir := filepath.Join(realAgentDir, spec.leaf)
	skillsFD, err := openVerifiedDirStep(p.scanner, op, agentFD, spec.leaf, rootPath, realRootDir, rootPath)
	closeFD(agentFD)
	if err != nil {
		return -1, "", err
	}
	return skillsFD, rootPath, nil
}

func ensureVerifiedDirStep(
	scanner *ProjectSkillScanner,
	op string,
	parentFD int,
	name string,
	lexicalPath string,
	wantRealPath string,
) (int, error) {
	fd, err := openVerifiedDirStep(scanner, op, parentFD, name, lexicalPath, wantRealPath, lexicalPath)
	if !errors.Is(err, iofs.ErrNotExist) {
		return fd, err
	}
	if err := scanner.guard.CheckLexical(lexicalPath); err != nil {
		return -1, fmt.Errorf("%s %s: %w", op, lexicalPath, err)
	}
	if mkErr := unix.Mkdirat(parentFD, name, 0o755); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
		return -1, &iofs.PathError{Op: "mkdir", Path: lexicalPath, Err: mkErr}
	}
	if err := syncFD(op, lexicalPath, parentFD); err != nil {
		return -1, err
	}
	return openVerifiedDirStep(scanner, op, parentFD, name, lexicalPath, wantRealPath, lexicalPath)
}

func openGuardedExistingDir(guard paths.Guard, op, dir string) (int, error) {
	cleanDir := filepath.Clean(dir)
	if err := guard.Check(cleanDir); err != nil {
		return -1, err
	}
	fd, err := unix.Open(cleanDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &iofs.PathError{Op: op, Path: cleanDir, Err: err}
	}
	real, err := fdRealPath(fd)
	if err == nil {
		err = guard.Check(real)
	}
	if err != nil {
		closeFD(fd)
		return -1, err
	}
	return fd, nil
}

func (p *ProjectOps) statDeepestAncestor(abs string) (*syscall.Stat_t, error) {
	cur := filepath.Clean(abs)
	for {
		if err := p.guard.Check(cur); err != nil {
			return nil, err
		}
		info, err := os.Stat(cur)
		if err == nil {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return nil, fmt.Errorf("stat %s: missing device identity", cur)
			}
			return st, nil
		}
		if !errors.Is(err, iofs.ErrNotExist) {
			return nil, &iofs.PathError{Op: "stat", Path: cur, Err: err}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, &iofs.PathError{Op: "stat", Path: abs, Err: iofs.ErrNotExist}
		}
		cur = parent
	}
}

func checkProjectTreeClean(pt ProjectTree, absRoot string) error {
	return walkTreeClean(pt, ".", absRoot)
}

func walkTreeClean(pt ProjectTree, rel, absRoot string) error {
	info, err := pt.Stat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&specialTreeBits != 0 {
		return &iofs.PathError{Op: "stat", Path: absFromRel(absRoot, rel), Err: ErrProjectTreeUnsupported}
	}
	if info.Mode()&iofs.ModeSymlink != 0 {
		return &iofs.PathError{Op: "lstat", Path: absFromRel(absRoot, rel), Err: ErrProjectTreeLinked}
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return &iofs.PathError{Op: "stat", Path: absFromRel(absRoot, rel), Err: ErrProjectTreeUnsupported}
		}
		return nil
	}
	entries, err := pt.ReadDir(rel)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			return &iofs.PathError{Op: "readdir", Path: absFromRel(absRoot, path.Join(rel, name)), Err: ErrProjectTreeGit}
		}
		childRel := name
		if rel != "." {
			childRel = path.Join(rel, name)
		}
		if err := walkTreeClean(pt, childRel, absRoot); err != nil {
			return err
		}
	}
	return nil
}

type dirPermRecord struct {
	path string
	perm iofs.FileMode
}

func copyProjectTreeRecursive(pt ProjectTree, rel, dst string, dirs *[]dirPermRecord) error {
	info, err := pt.Stat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&specialTreeBits != 0 {
		return &iofs.PathError{Op: "copy", Path: dst, Err: ErrProjectTreeUnsupported}
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return &iofs.PathError{Op: "copy", Path: dst, Err: ErrProjectTreeUnsupported}
		}
		data, err := pt.ReadFile(rel)
		if err != nil {
			return err
		}
		return writeNewFileSynced(dst, data, info.Mode().Perm())
	}

	if err := os.Mkdir(dst, 0o700); err != nil {
		return &iofs.PathError{Op: "mkdir", Path: dst, Err: err}
	}
	*dirs = append(*dirs, dirPermRecord{path: dst, perm: info.Mode().Perm()})

	entries, err := pt.ReadDir(rel)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			return &iofs.PathError{Op: "copy", Path: filepath.Join(dst, name), Err: ErrProjectTreeGit}
		}
		childRel := name
		if rel != "." {
			childRel = path.Join(rel, name)
		}
		if err := copyProjectTreeRecursive(pt, childRel, filepath.Join(dst, name), dirs); err != nil {
			return err
		}
	}
	return nil
}

func writeNewFileSynced(dst string, data []byte, perm iofs.FileMode) (err error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	for len(data) > 0 {
		n, werr := f.Write(data)
		if werr != nil {
			return werr
		}
		data = data[n:]
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	return f.Sync()
}

func fsyncDirPath(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return &iofs.PathError{Op: "open", Path: dir, Err: err}
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return &iofs.PathError{Op: "fsync", Path: dir, Err: err}
	}
	return nil
}

func verifyStagingDirClean(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&iofs.ModeSymlink != 0 {
		return &iofs.PathError{Op: "lstat", Path: dir, Err: ErrProjectTreeLinked}
	}
	if !info.IsDir() {
		return &iofs.PathError{Op: "lstat", Path: dir, Err: ErrProjectTreeNotDir}
	}
	if info.Mode()&specialTreeBits != 0 {
		return &iofs.PathError{Op: "lstat", Path: dir, Err: ErrProjectTreeUnsupported}
	}
	return verifyStagingChildrenClean(dir)
}

func verifyStagingChildrenClean(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		full := filepath.Join(dir, name)
		if name == ".git" {
			return &iofs.PathError{Op: "readdir", Path: full, Err: ErrProjectTreeGit}
		}
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if info.Mode()&specialTreeBits != 0 {
			return &iofs.PathError{Op: "lstat", Path: full, Err: ErrProjectTreeUnsupported}
		}
		switch {
		case info.Mode()&iofs.ModeSymlink != 0:
			return &iofs.PathError{Op: "lstat", Path: full, Err: ErrProjectTreeLinked}
		case info.IsDir():
			if err := verifyStagingChildrenClean(full); err != nil {
				return err
			}
		case info.Mode().IsRegular():
		default:
			return &iofs.PathError{Op: "lstat", Path: full, Err: ErrProjectTreeUnsupported}
		}
	}
	return nil
}

func mapRenameError(op, from, to string, err error) error {
	switch {
	case errors.Is(err, unix.EXDEV):
		return &os.LinkError{Op: op, Old: from, New: to, Err: ports.ErrCrossDevice}
	case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY):
		return &os.LinkError{Op: op, Old: from, New: to, Err: iofs.ErrExist}
	default:
		return &os.LinkError{Op: op, Old: from, New: to, Err: err}
	}
}

func syncFD(op, dir string, fd int) error {
	if err := unix.Fsync(fd); err != nil {
		return &iofs.PathError{Op: op + " fsync", Path: dir, Err: err}
	}
	return nil
}

func absFromRel(absRoot, rel string) string {
	if rel == "." || rel == "" {
		return absRoot
	}
	return filepath.Join(absRoot, filepath.FromSlash(rel))
}
