package fs_test

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
)

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	mustNoErr(t, "mkdir "+path, os.MkdirAll(path, 0o755))
}

func mustWriteFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	mustNoErr(t, "write "+path, os.WriteFile(path, []byte(content), mode))
	mustNoErr(t, "chmod "+path, os.Chmod(path, mode))
}

func mustSymlink(t *testing.T, target, linkPath string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(linkPath))
	mustNoErr(t, "symlink "+linkPath, os.Symlink(target, linkPath))
}

func TestProjectSkillScannerEnumeratesDirectRealSkills(t *testing.T) {
	base := t.TempDir()
	parentRoot := filepath.Join(base, "workspace")
	projectDir := filepath.Join(parentRoot, "project")
	siblingDir := filepath.Join(parentRoot, "project-sibling")

	// Parent and sibling out-of-scope skills must never be discovered.
	mustWriteFileMode(t, filepath.Join(parentRoot, ".agents", "skills", "from-parent", "SKILL.md"), "parent", 0o644)
	mustWriteFileMode(t, filepath.Join(siblingDir, ".agents", "skills", "from-sibling", "SKILL.md"), "sibling", 0o644)

	// Out-of-scope directories and nested structures inside the project.
	mustWriteFileMode(t, filepath.Join(projectDir, ".codex", "skills", "codex-only", "SKILL.md"), "codex", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".cursor", "skills", "cursor-only", "SKILL.md"), "cursor", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "other", "not-skills", "SKILL.md"), "other", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "group", "nested", "SKILL.md"), "nested", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "README.md"), "file", 0o644)

	// In-scope direct real skill directories, including two distinct same-name copies.
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "alpha", "SKILL.md"), "alpha", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "shared", "SKILL.md"), "shared-agents", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".claude", "skills", "beta", "SKILL.md"), "beta", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".claude", "skills", "shared", "SKILL.md"), "shared-claude", 0o644)

	if os.Geteuid() != 0 {
		// Unreadable regular SKILL.md must still be admitted without reading contents.
		mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "unreadable-content", "SKILL.md"), "secret", 0o000)
		t.Cleanup(func() {
			_ = os.Chmod(filepath.Join(projectDir, ".agents", "skills", "unreadable-content", "SKILL.md"), 0o644)
		})
	}

	scanner := fsadapter.NewProjectSkillScanner(paths.Guard{})
	got, err := scanner.Scan(projectDir)
	mustNoErr(t, "Scan", err)

	if got.ProjectDir() != projectDir {
		t.Fatalf("ProjectDir() = %q, want %q", got.ProjectDir(), projectDir)
	}

	roots := got.Roots()
	if len(roots) != 2 {
		t.Fatalf("len(Roots()) = %d, want 2: %+v", len(roots), roots)
	}
	for _, r := range roots {
		if r.Status != fsadapter.ProjectRootReady {
			t.Errorf("root %s status = %q (reason=%q, err=%v), want ready", r.Kind, r.Status, r.Reason, r.Err)
		}
	}

	want := []fsadapter.ProjectSkill{
		{
			ID:          fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "alpha"),
			ProjectDir:  projectDir,
			Root:        fsadapter.ProjectRootAgents,
			Name:        "alpha",
			Path:        filepath.Join(projectDir, ".agents", "skills", "alpha"),
			SkillMDPath: filepath.Join(projectDir, ".agents", "skills", "alpha", "SKILL.md"),
		},
		{
			ID:          fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "shared"),
			ProjectDir:  projectDir,
			Root:        fsadapter.ProjectRootAgents,
			Name:        "shared",
			Path:        filepath.Join(projectDir, ".agents", "skills", "shared"),
			SkillMDPath: filepath.Join(projectDir, ".agents", "skills", "shared", "SKILL.md"),
		},
	}
	if os.Geteuid() != 0 {
		want = append(want, fsadapter.ProjectSkill{
			ID:          fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "unreadable-content"),
			ProjectDir:  projectDir,
			Root:        fsadapter.ProjectRootAgents,
			Name:        "unreadable-content",
			Path:        filepath.Join(projectDir, ".agents", "skills", "unreadable-content"),
			SkillMDPath: filepath.Join(projectDir, ".agents", "skills", "unreadable-content", "SKILL.md"),
		})
	}
	want = append(want,
		fsadapter.ProjectSkill{
			ID:          fsadapter.ProjectSkillID(fsadapter.ProjectRootClaude, "beta"),
			ProjectDir:  projectDir,
			Root:        fsadapter.ProjectRootClaude,
			Name:        "beta",
			Path:        filepath.Join(projectDir, ".claude", "skills", "beta"),
			SkillMDPath: filepath.Join(projectDir, ".claude", "skills", "beta", "SKILL.md"),
		},
		fsadapter.ProjectSkill{
			ID:          fsadapter.ProjectSkillID(fsadapter.ProjectRootClaude, "shared"),
			ProjectDir:  projectDir,
			Root:        fsadapter.ProjectRootClaude,
			Name:        "shared",
			Path:        filepath.Join(projectDir, ".claude", "skills", "shared"),
			SkillMDPath: filepath.Join(projectDir, ".claude", "skills", "shared", "SKILL.md"),
		},
	)

	skills := got.Skills()
	if !slices.Equal(skills, want) {
		t.Fatalf("Skills() =\n%+v\nwant:\n%+v", skills, want)
	}
}

func TestProjectSkillScannerMissingRootsAreNormal(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, projectDir string)
		wantAgents fsadapter.ProjectRootStatus
		wantClaude fsadapter.ProjectRootStatus
		wantSkills int
	}{
		{
			name:       "neither root exists",
			setup:      func(t *testing.T, projectDir string) {},
			wantAgents: fsadapter.ProjectRootMissing,
			wantClaude: fsadapter.ProjectRootMissing,
			wantSkills: 0,
		},
		{
			name: "agent dir exists without skills subdir",
			setup: func(t *testing.T, projectDir string) {
				mustMkdirAll(t, filepath.Join(projectDir, ".agents"))
			},
			wantAgents: fsadapter.ProjectRootMissing,
			wantClaude: fsadapter.ProjectRootMissing,
			wantSkills: 0,
		},
		{
			name: "only claude skills root exists",
			setup: func(t *testing.T, projectDir string) {
				mustWriteFileMode(t, filepath.Join(projectDir, ".claude", "skills", "one", "SKILL.md"), "one", 0o644)
			},
			wantAgents: fsadapter.ProjectRootMissing,
			wantClaude: fsadapter.ProjectRootReady,
			wantSkills: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "project")
			mustMkdirAll(t, projectDir)
			tt.setup(t, projectDir)

			got, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(projectDir)
			mustNoErr(t, "Scan", err)

			roots := got.Roots()
			if len(roots) != 2 {
				t.Fatalf("len(Roots()) = %d, want 2", len(roots))
			}
			if roots[0].Kind != fsadapter.ProjectRootAgents || roots[0].Status != tt.wantAgents || roots[0].Reason != "" || roots[0].Err != nil {
				t.Errorf("agents root = %+v, want status %q without reason/err", roots[0], tt.wantAgents)
			}
			if roots[1].Kind != fsadapter.ProjectRootClaude || roots[1].Status != tt.wantClaude || roots[1].Reason != "" || roots[1].Err != nil {
				t.Errorf("claude root = %+v, want status %q without reason/err", roots[1], tt.wantClaude)
			}
			if len(got.Skills()) != tt.wantSkills {
				t.Errorf("len(Skills()) = %d, want %d", len(got.Skills()), tt.wantSkills)
			}
		})
	}
}

func TestProjectSkillScannerRejectsLinkedAndNonDirRootsWithoutInspectingTarget(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(t *testing.T, base, forbidden, projectDir string)
		wantAgentsStatus fsadapter.ProjectRootStatus
		wantAgentsReason fsadapter.ProjectRejectReason
		wantClaudeStatus fsadapter.ProjectRootStatus
		wantClaudeReason fsadapter.ProjectRejectReason
		wantSkillNames   []string
	}{
		{
			name: "linked .agents directory leaves target uninspected while claude works",
			setup: func(t *testing.T, base, forbidden, projectDir string) {
				mustSymlink(t, forbidden, filepath.Join(projectDir, ".agents"))
				mustWriteFileMode(t, filepath.Join(projectDir, ".claude", "skills", "ok", "SKILL.md"), "ok", 0o644)
			},
			wantAgentsStatus: fsadapter.ProjectRootRejected,
			wantAgentsReason: fsadapter.ProjectRejectLinked,
			wantClaudeStatus: fsadapter.ProjectRootReady,
			wantSkillNames:   []string{"ok"},
		},
		{
			name: "linked .agents/skills directory",
			setup: func(t *testing.T, base, forbidden, projectDir string) {
				mustMkdirAll(t, filepath.Join(projectDir, ".agents"))
				mustSymlink(t, filepath.Join(forbidden, "skills"), filepath.Join(projectDir, ".agents", "skills"))
			},
			wantAgentsStatus: fsadapter.ProjectRootRejected,
			wantAgentsReason: fsadapter.ProjectRejectLinked,
			wantClaudeStatus: fsadapter.ProjectRootMissing,
		},
		{
			name: "linked .claude directory",
			setup: func(t *testing.T, base, forbidden, projectDir string) {
				mustSymlink(t, filepath.Join(base, "elsewhere"), filepath.Join(projectDir, ".claude"))
			},
			wantAgentsStatus: fsadapter.ProjectRootMissing,
			wantClaudeStatus: fsadapter.ProjectRootRejected,
			wantClaudeReason: fsadapter.ProjectRejectLinked,
		},
		{
			name: "linked .claude/skills directory",
			setup: func(t *testing.T, base, forbidden, projectDir string) {
				mustMkdirAll(t, filepath.Join(projectDir, ".claude"))
				mustSymlink(t, filepath.Join(base, "elsewhere", "skills"), filepath.Join(projectDir, ".claude", "skills"))
			},
			wantAgentsStatus: fsadapter.ProjectRootMissing,
			wantClaudeStatus: fsadapter.ProjectRootRejected,
			wantClaudeReason: fsadapter.ProjectRejectLinked,
		},
		{
			name: "regular file at .agents",
			setup: func(t *testing.T, base, forbidden, projectDir string) {
				mustWriteFileMode(t, filepath.Join(projectDir, ".agents"), "not a dir", 0o644)
			},
			wantAgentsStatus: fsadapter.ProjectRootRejected,
			wantAgentsReason: fsadapter.ProjectRejectNotDir,
			wantClaudeStatus: fsadapter.ProjectRootMissing,
		},
		{
			name: "regular file at .claude/skills",
			setup: func(t *testing.T, base, forbidden, projectDir string) {
				mustWriteFileMode(t, filepath.Join(projectDir, ".claude", "skills"), "not a dir", 0o644)
			},
			wantAgentsStatus: fsadapter.ProjectRootMissing,
			wantClaudeStatus: fsadapter.ProjectRootRejected,
			wantClaudeReason: fsadapter.ProjectRejectNotDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			forbidden := filepath.Join(base, "forbidden")
			mustWriteFileMode(t, filepath.Join(forbidden, "skills", "secret", "SKILL.md"), "secret", 0o644)
			mustWriteFileMode(t, filepath.Join(base, "elsewhere", "skills", "other", "SKILL.md"), "other", 0o644)
			before, err := treehash.Dir(forbidden)
			mustNoErr(t, "hash forbidden before", err)

			projectDir := filepath.Join(base, "project")
			mustMkdirAll(t, projectDir)
			tt.setup(t, base, forbidden, projectDir)

			got, err := fsadapter.NewProjectSkillScanner(paths.NewGuard(forbidden)).Scan(projectDir)
			mustNoErr(t, "Scan", err)

			roots := got.Roots()
			if roots[0].Status != tt.wantAgentsStatus || roots[0].Reason != tt.wantAgentsReason {
				t.Errorf("agents root = %+v, want status=%q reason=%q", roots[0], tt.wantAgentsStatus, tt.wantAgentsReason)
			}
			if roots[1].Status != tt.wantClaudeStatus || roots[1].Reason != tt.wantClaudeReason {
				t.Errorf("claude root = %+v, want status=%q reason=%q", roots[1], tt.wantClaudeStatus, tt.wantClaudeReason)
			}

			var names []string
			for _, sk := range got.Skills() {
				names = append(names, sk.Name)
			}
			if !slices.Equal(names, tt.wantSkillNames) {
				t.Errorf("skill names = %v, want %v", names, tt.wantSkillNames)
			}

			after, err := treehash.Dir(forbidden)
			mustNoErr(t, "hash forbidden after", err)
			if before.SHA != after.SHA {
				t.Errorf("forbidden tree changed: %s -> %s", before.SHA, after.SHA)
			}
		})
	}

	if os.Geteuid() != 0 {
		t.Run("unreadable .agents/skills root is rejected not missing", func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "project")
			skillsRoot := filepath.Join(projectDir, ".agents", "skills")
			mustWriteFileMode(t, filepath.Join(skillsRoot, "hidden", "SKILL.md"), "hidden", 0o644)
			mustNoErr(t, "chmod 0000", os.Chmod(skillsRoot, 0o000))
			t.Cleanup(func() { _ = os.Chmod(skillsRoot, 0o755) })

			got, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(projectDir)
			mustNoErr(t, "Scan", err)
			roots := got.Roots()
			if roots[0].Status != fsadapter.ProjectRootRejected || roots[0].Reason != fsadapter.ProjectRejectUnreadable || roots[0].Err == nil {
				t.Errorf("agents root = %+v, want rejected/unreadable with err", roots[0])
			}
			if len(got.Skills()) != 0 {
				t.Errorf("got %d eligible skills from unreadable root, want 0", len(got.Skills()))
			}
		})
	}
}

func TestProjectSkillScannerRejectsLinkedProjectDirAndUnprovenScope(t *testing.T) {
	base := t.TempDir()
	realProj := filepath.Join(base, "real-project")
	mustWriteFileMode(t, filepath.Join(realProj, ".agents", "skills", "alpha", "SKILL.md"), "alpha", 0o644)
	linkedProj := filepath.Join(base, "linked-project")
	mustSymlink(t, realProj, linkedProj)

	scanner := fsadapter.NewProjectSkillScanner(paths.Guard{})

	got, err := scanner.Scan(linkedProj)
	if !errors.Is(err, fsadapter.ErrProjectDirLinked) {
		t.Fatalf("Scan(linkedProj) error = %v, want ErrProjectDirLinked", err)
	}
	if len(got.Skills()) != 0 {
		t.Fatalf("Scan(linkedProj) produced %d eligible skills, want 0", len(got.Skills()))
	}

	// Trailing slash must not bypass O_NOFOLLOW on a linked projectDir.
	gotSlash, err := scanner.Scan(linkedProj + string(filepath.Separator))
	if !errors.Is(err, fsadapter.ErrProjectDirLinked) {
		t.Fatalf("Scan(linkedProj/) error = %v, want ErrProjectDirLinked", err)
	}
	if len(gotSlash.Skills()) != 0 {
		t.Fatalf("Scan(linkedProj/) produced %d eligible skills, want 0", len(gotSlash.Skills()))
	}

	fileProj := filepath.Join(base, "file-project")
	mustWriteFileMode(t, fileProj, "not a dir", 0o644)
	if res, err := scanner.Scan(fileProj); !errors.Is(err, fsadapter.ErrProjectDirNotDir) || len(res.Skills()) != 0 {
		t.Fatalf("Scan(fileProj) = (%d skills, %v), want 0 skills and ErrProjectDirNotDir", len(res.Skills()), err)
	}

	missingProj := filepath.Join(base, "missing-project")
	if res, err := scanner.Scan(missingProj); !errors.Is(err, iofs.ErrNotExist) || len(res.Skills()) != 0 {
		t.Fatalf("Scan(missingProj) = (%d skills, %v), want 0 skills and ErrNotExist", len(res.Skills()), err)
	}

	// Linked ancestor of the startup directory must be rejected without following links.
	linkedAncestor := filepath.Join(base, "linked-ancestor")
	mustSymlink(t, realProj, linkedAncestor)
	mustWriteFileMode(t, filepath.Join(realProj, "nested", ".agents", "skills", "beta", "SKILL.md"), "beta", 0o644)
	if res, err := scanner.Scan(filepath.Join(linkedAncestor, "nested")); !errors.Is(err, fsadapter.ErrProjectDirLinked) || len(res.Skills()) != 0 {
		t.Fatalf("Scan(linkedAncestor/nested) = (%d skills, %v), want 0 skills and ErrProjectDirLinked", len(res.Skills()), err)
	}

	// Non-directory and missing ancestors must be rejected appropriately.
	if res, err := scanner.Scan(filepath.Join(fileProj, "nested")); !errors.Is(err, fsadapter.ErrProjectDirNotDir) || len(res.Skills()) != 0 {
		t.Fatalf("Scan(fileProj/nested) = (%d skills, %v), want 0 skills and ErrProjectDirNotDir", len(res.Skills()), err)
	}
	if res, err := scanner.Scan(filepath.Join(missingProj, "nested")); !errors.Is(err, iofs.ErrNotExist) || len(res.Skills()) != 0 {
		t.Fatalf("Scan(missingProj/nested) = (%d skills, %v), want 0 skills and ErrNotExist", len(res.Skills()), err)
	}
}

func TestProjectSkillScannerRejectsLinkedEntriesAndNonRegularSkillMD(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	mustWriteFileMode(t, filepath.Join(forbidden, "secret-skill", "SKILL.md"), "secret", 0o644)
	mustWriteFileMode(t, filepath.Join(forbidden, "secret.md"), "secret", 0o644)
	before, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden before", err)

	projectDir := filepath.Join(base, "project")
	agentsSkills := filepath.Join(projectDir, ".agents", "skills")

	// Valid sibling must still be enumerated.
	mustWriteFileMode(t, filepath.Join(agentsSkills, "valid", "SKILL.md"), "valid", 0o644)

	// Linked candidate entries.
	mustSymlink(t, filepath.Join(agentsSkills, "valid"), filepath.Join(agentsSkills, "alias-valid"))
	mustSymlink(t, filepath.Join(forbidden, "secret-skill"), filepath.Join(agentsSkills, "alias-forbidden"))
	mustSymlink(t, filepath.Join(base, "does-not-exist"), filepath.Join(agentsSkills, "alias-dangling"))

	// Candidate dir with linked SKILL.md.
	mustMkdirAll(t, filepath.Join(agentsSkills, "linked-skillmd"))
	mustSymlink(t, filepath.Join(agentsSkills, "valid", "SKILL.md"), filepath.Join(agentsSkills, "linked-skillmd", "SKILL.md"))

	mustMkdirAll(t, filepath.Join(agentsSkills, "linked-forbidden-skillmd"))
	mustSymlink(t, filepath.Join(forbidden, "secret.md"), filepath.Join(agentsSkills, "linked-forbidden-skillmd", "SKILL.md"))

	// Candidate dir with directory SKILL.md and FIFO SKILL.md.
	mustMkdirAll(t, filepath.Join(agentsSkills, "dir-skillmd", "SKILL.md"))
	mustMkdirAll(t, filepath.Join(agentsSkills, "fifo-skillmd"))
	mustNoErr(t, "mkfifo", syscall.Mkfifo(filepath.Join(agentsSkills, "fifo-skillmd", "SKILL.md"), 0o644))

	// Candidate dir without SKILL.md and regular file entry.
	mustMkdirAll(t, filepath.Join(agentsSkills, "no-skillmd"))
	mustWriteFileMode(t, filepath.Join(agentsSkills, "plain-file"), "x", 0o644)

	if os.Geteuid() != 0 {
		locked := filepath.Join(agentsSkills, "locked-dir")
		mustWriteFileMode(t, filepath.Join(locked, "SKILL.md"), "x", 0o644)
		mustNoErr(t, "chmod 0000", os.Chmod(locked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	}

	got, err := fsadapter.NewProjectSkillScanner(paths.NewGuard(forbidden)).Scan(projectDir)
	mustNoErr(t, "Scan", err)

	skills := got.Skills()
	if len(skills) != 1 || skills[0].Name != "valid" {
		t.Fatalf("Skills() = %+v, want only [valid]", skills)
	}

	reasons := map[string]fsadapter.ProjectRejectReason{}
	for _, r := range got.Roots()[0].Rejected {
		reasons[r.Name] = r.Reason
	}
	wantReasons := map[string]fsadapter.ProjectRejectReason{
		"alias-valid":              fsadapter.ProjectRejectLinked,
		"alias-forbidden":          fsadapter.ProjectRejectLinked,
		"alias-dangling":           fsadapter.ProjectRejectLinked,
		"linked-skillmd":           fsadapter.ProjectRejectLinkedSkillMD,
		"linked-forbidden-skillmd": fsadapter.ProjectRejectLinkedSkillMD,
		"dir-skillmd":              fsadapter.ProjectRejectNonRegularSkillMD,
		"fifo-skillmd":             fsadapter.ProjectRejectNonRegularSkillMD,
		"no-skillmd":               fsadapter.ProjectRejectMissingSkillMD,
		"plain-file":               fsadapter.ProjectRejectNotDir,
	}
	if os.Geteuid() != 0 {
		wantReasons["locked-dir"] = fsadapter.ProjectRejectUnreadable
	}
	for name, wantReason := range wantReasons {
		if reasons[name] != wantReason {
			t.Errorf("rejected[%q] = %q, want %q", name, reasons[name], wantReason)
		}
	}

	after, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden after", err)
	if before.SHA != after.SHA {
		t.Errorf("forbidden tree changed: %s -> %s", before.SHA, after.SHA)
	}
}

func TestProjectSkillScannerGuardAndInvalidPaths(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	mustWriteFileMode(t, filepath.Join(forbidden, "proj", ".agents", "skills", "x", "SKILL.md"), "x", 0o644)
	before, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden before", err)

	escapeLink := filepath.Join(base, "escape")
	mustSymlink(t, filepath.Join(forbidden, "proj"), escapeLink)
	mustWriteFileMode(t, filepath.Join(forbidden, "proj", "nested", ".agents", "skills", "y", "SKILL.md"), "y", 0o644)
	// Recompute baseline hash after adding the nested fixture inside forbidden.
	before, err = treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden before", err)

	prefixes := filepath.SplitList(testsupport.NewHome(t).Env().ForbidPrefix)
	if len(prefixes) == 0 {
		t.Fatal("testsupport.Main set no forbidden prefix")
	}
	realHome := prefixes[0]

	scanner := fsadapter.NewProjectSkillScanner(paths.NewGuard(forbidden + string(os.PathListSeparator) + realHome))

	tests := []struct {
		name    string
		dir     string
		wantErr error
	}{
		{"empty path", "", paths.ErrInvalidPath},
		{"relative path", "relative/project", paths.ErrInvalidPath},
		{"parent traversal", base + string(filepath.Separator) + "other" + string(filepath.Separator) + ".." + string(filepath.Separator) + "proj", paths.ErrInvalidPath},
		{"forbidden prefix", forbidden, paths.ErrForbidden},
		{"forbidden path", filepath.Join(forbidden, "proj"), paths.ErrForbidden},
		{"symlink to forbidden path rejected as linked without following", escapeLink, fsadapter.ErrProjectDirLinked},
		{"linked ancestor to forbidden path rejected as linked without following", filepath.Join(escapeLink, "nested"), fsadapter.ErrProjectDirLinked},
		{"real home", realHome, paths.ErrForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := scanner.Scan(tt.dir)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Scan(%q) error = %v, want %v", tt.dir, err, tt.wantErr)
			}
			if len(res.Skills()) != 0 {
				t.Fatalf("Scan(%q) produced %d eligible skills, want 0", tt.dir, len(res.Skills()))
			}
		})
	}

	// Forbidden skill-root and candidate prefixes must be rejected lexically before inspection.
	t.Run("forbidden skill root and candidate prefixes rejected before inspection", func(t *testing.T) {
		proj := filepath.Join(base, "guarded-proj")
		mustSymlink(t, filepath.Join(forbidden, "proj"), filepath.Join(proj, ".agents"))
		mustWriteFileMode(t, filepath.Join(proj, ".claude", "skills", "public", "SKILL.md"), "public", 0o644)
		mustWriteFileMode(t, filepath.Join(proj, ".claude", "skills", "blocked-cand", "SKILL.md"), "blocked", 0o644)

		rootGuard := paths.NewGuard(
			filepath.Join(proj, ".agents") +
				string(os.PathListSeparator) +
				filepath.Join(proj, ".claude", "skills", "blocked-cand"),
		)
		got, err := fsadapter.NewProjectSkillScanner(rootGuard).Scan(proj)
		mustNoErr(t, "Scan(proj)", err)

		roots := got.Roots()
		if len(roots) != 2 {
			t.Fatalf("len(Roots()) = %d, want 2", len(roots))
		}
		if roots[0].Status != fsadapter.ProjectRootRejected ||
			roots[0].Reason != fsadapter.ProjectRejectUnprovenScope ||
			!errors.Is(roots[0].Err, paths.ErrForbidden) {
			t.Errorf("agents root = %+v, want rejected/unproven_scope wrapping ErrForbidden", roots[0])
		}
		if roots[1].Status != fsadapter.ProjectRootReady {
			t.Errorf("claude root = %+v, want ready", roots[1])
		}
		skills := got.Skills()
		if len(skills) != 1 || skills[0].Name != "public" {
			t.Errorf("Skills() = %+v, want only [public]", skills)
		}
		if len(roots[1].Rejected) != 1 ||
			roots[1].Rejected[0].Name != "blocked-cand" ||
			roots[1].Rejected[0].Reason != fsadapter.ProjectRejectUnprovenScope ||
			!errors.Is(roots[1].Rejected[0].Err, paths.ErrForbidden) {
			t.Errorf("claude rejected = %+v, want blocked-cand rejected as unproven_scope wrapping ErrForbidden", roots[1].Rejected)
		}
	})

	t.Run("forbidden .agents/skills leaf rejected before inspection", func(t *testing.T) {
		proj := filepath.Join(base, "guarded-leaf-proj")
		mustMkdirAll(t, filepath.Join(proj, ".agents"))
		mustSymlink(t, filepath.Join(forbidden, "proj"), filepath.Join(proj, ".agents", "skills"))
		mustWriteFileMode(t, filepath.Join(proj, ".claude", "skills", "ok", "SKILL.md"), "ok", 0o644)

		leafGuard := paths.NewGuard(filepath.Join(proj, ".agents", "skills"))
		got, err := fsadapter.NewProjectSkillScanner(leafGuard).Scan(proj)
		mustNoErr(t, "Scan(proj)", err)

		roots := got.Roots()
		if roots[0].Status != fsadapter.ProjectRootRejected ||
			roots[0].Reason != fsadapter.ProjectRejectUnprovenScope ||
			!errors.Is(roots[0].Err, paths.ErrForbidden) {
			t.Errorf("agents root = %+v, want rejected/unproven_scope wrapping ErrForbidden", roots[0])
		}
		if len(got.Skills()) != 1 || got.Skills()[0].Name != "ok" {
			t.Errorf("Skills() = %+v, want only [ok]", got.Skills())
		}
	})

	after, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden after", err)
	if before.SHA != after.SHA {
		t.Errorf("forbidden tree changed: %s -> %s", before.SHA, after.SHA)
	}
}

func TestProjectSkillsExactLookupInMemoryOnly(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "same", "SKILL.md"), "agents", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".claude", "skills", "same", "SKILL.md"), "claude", 0o644)

	res, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(projectDir)
	mustNoErr(t, "Scan", err)

	agentsID := fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "same")
	claudeID := fsadapter.ProjectSkillID(fsadapter.ProjectRootClaude, "same")

	gotAgents, ok := res.Lookup(agentsID)
	if !ok || gotAgents.Root != fsadapter.ProjectRootAgents || gotAgents.Path != filepath.Join(projectDir, ".agents", "skills", "same") {
		t.Fatalf("Lookup(%q) = (%+v, %v), want agents copy", agentsID, gotAgents, ok)
	}
	gotClaude, ok := res.Lookup(claudeID)
	if !ok || gotClaude.Root != fsadapter.ProjectRootClaude || gotClaude.Path != filepath.Join(projectDir, ".claude", "skills", "same") {
		t.Fatalf("Lookup(%q) = (%+v, %v), want claude copy", claudeID, gotClaude, ok)
	}

	// Create a new skill on disk after Scan; Lookup must only match in-memory enumerated identities.
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "created-later", "SKILL.md"), "later", 0o644)

	for _, badID := range []string{
		"",
		"dir:.agents/skills/same",
		"project:.agents/skills/created-later",
		"project:.agents/skills/missing",
		"project:.agents/skills/../same",
		"project:.codex/skills/same",
	} {
		if got, ok := res.Lookup(badID); ok {
			t.Errorf("Lookup(%q) = (%+v, true), want (_, false)", badID, got)
		}
	}

	// Mutating returned slices must not alter internal state.
	skillsCopy := res.Skills()
	skillsCopy[0].Name = "mutated"
	if gotAgain, ok := res.Lookup(agentsID); !ok || gotAgain.Name != "same" {
		t.Errorf("mutating Skills() affected Lookup: %+v", gotAgain)
	}
}
