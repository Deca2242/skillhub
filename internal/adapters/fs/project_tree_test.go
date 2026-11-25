package fs_test

import (
	"errors"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
)

func observedBinding(projectDir string, root fsadapter.ProjectRootKind, name string) fsadapter.ProjectSkill {
	skillPath := filepath.Join(projectDir, filepath.FromSlash(string(root)), name)
	return fsadapter.ProjectSkill{
		ID:          fsadapter.ProjectSkillID(root, name),
		ProjectDir:  projectDir,
		Root:        root,
		Name:        name,
		Path:        skillPath,
		SkillMDPath: filepath.Join(skillPath, "SKILL.md"),
	}
}

func walkProjectTree(
	tree fsadapter.ProjectTree,
	rel string,
	fn func(rel string, d iofs.DirEntry, walkErr error) error,
) error {
	info, err := tree.Stat(rel)
	if err != nil {
		return fn(rel, nil, err)
	}
	entry := iofs.FileInfoToDirEntry(info)
	if err := fn(rel, entry, nil); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := tree.ReadDir(rel)
	if err != nil {
		return fn(rel, entry, err)
	}
	for _, child := range entries {
		childRel := child.Name()
		if rel != "." {
			childRel = path.Join(rel, child.Name())
		}
		if err := walkProjectTree(tree, childRel, fn); err != nil {
			return err
		}
	}
	return nil
}

func copyFromProjectTree(t *testing.T, tree fsadapter.ProjectTree, dstDir string) {
	t.Helper()
	err := walkProjectTree(tree, ".", func(rel string, d iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := dstDir
		if rel != "." {
			target = filepath.Join(dstDir, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := tree.ReadFile(rel)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	mustNoErr(t, "walkProjectTree copy", err)

	// Apply directory permissions bottom-up so read-only directories can still
	// receive their children first.
	var dirs []struct {
		rel  string
		perm iofs.FileMode
	}
	err = walkProjectTree(tree, ".", func(rel string, d iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		dirs = append(dirs, struct {
			rel  string
			perm iofs.FileMode
		}{rel: rel, perm: info.Mode().Perm()})
		return nil
	})
	mustNoErr(t, "walkProjectTree dir perms", err)
	for i := len(dirs) - 1; i >= 0; i-- {
		target := dstDir
		if dirs[i].rel != "." {
			target = filepath.Join(dstDir, filepath.FromSlash(dirs[i].rel))
		}
		mustNoErr(t, "chmod "+target, os.Chmod(target, dirs[i].perm))
	}
	t.Cleanup(func() {
		for i := range dirs {
			target := dstDir
			if dirs[i].rel != "." {
				target = filepath.Join(dstDir, filepath.FromSlash(dirs[i].rel))
			}
			_ = os.Chmod(target, 0o755)
		}
	})
}

func openFDs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	mustNoErr(t, "readdir /proc/self/fd", err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func TestProjectTreeAdapterDirectCopyAndHashParity(t *testing.T) {
	tests := []struct {
		name  string
		root  fsadapter.ProjectRootKind
		skill string
	}{
		{
			name:  "agents skill scanned binding",
			root:  fsadapter.ProjectRootAgents,
			skill: "shared",
		},
		{
			name:  "claude skill scanned binding",
			root:  fsadapter.ProjectRootClaude,
			skill: "shared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			projectDir := filepath.Join(base, "project")
			agentsSkill := filepath.Join(projectDir, ".agents", "skills", "shared")
			claudeSkill := filepath.Join(projectDir, ".claude", "skills", "shared")

			// Seed distinct same-name skills in both roots with nested files,
			// executable script, read-only file, read-only dir, and empty dir.
			mustWriteFileMode(t, filepath.Join(agentsSkill, "SKILL.md"), "# Agents Shared\n", 0o644)
			mustWriteFileMode(t, filepath.Join(agentsSkill, "scripts", "run.sh"), "#!/bin/sh\necho agents\n", 0o755)
			mustWriteFileMode(t, filepath.Join(agentsSkill, "assets", "ro.txt"), "readonly\n", 0o444)
			mustMkdirAll(t, filepath.Join(agentsSkill, "empty-dir"))
			mustNoErr(t, "chmod assets", os.Chmod(filepath.Join(agentsSkill, "assets"), 0o555))
			t.Cleanup(func() {
				_ = os.Chmod(filepath.Join(agentsSkill, "assets"), 0o755)
			})

			mustWriteFileMode(t, filepath.Join(claudeSkill, "SKILL.md"), "# Claude Shared\n", 0o644)
			mustWriteFileMode(t, filepath.Join(claudeSkill, "notes", "info.txt"), "claude only\n", 0o640)
			mustMkdirAll(t, filepath.Join(claudeSkill, "empty-claude"))

			scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(projectDir)
			mustNoErr(t, "Scan", err)
			binding, ok := scanned.Lookup(fsadapter.ProjectSkillID(tt.root, tt.skill))
			if !ok {
				t.Fatalf("Lookup(%s) returned false", fsadapter.ProjectSkillID(tt.root, tt.skill))
			}

			adapter := fsadapter.NewProjectTreeAdapter(paths.Guard{})
			tree, err := adapter.Open(scanned, binding)
			mustNoErr(t, "Open", err)
			defer func() { mustNoErr(t, "Close", tree.Close()) }()

			gotSkill := tree.Skill()
			wantSrc := filepath.Join(projectDir, filepath.FromSlash(string(tt.root)), tt.skill)
			if gotSkill.ID != fsadapter.ProjectSkillID(tt.root, tt.skill) ||
				gotSkill.ProjectDir != projectDir ||
				gotSkill.Root != tt.root ||
				gotSkill.Name != tt.skill ||
				gotSkill.Path != wantSrc ||
				gotSkill.SkillMDPath != filepath.Join(wantSrc, "SKILL.md") {
				t.Fatalf("Skill() = %+v, want canonical fields for %s", gotSkill, wantSrc)
			}

			// Copy using only the ProjectTree read-only one-shot interface.
			dst := filepath.Join(base, "copy-"+string(tt.root[1:]))
			copyFromProjectTree(t, tree, dst)

			wantHash, err := treehash.Dir(wantSrc)
			mustNoErr(t, "hash original", err)
			gotHash, err := treehash.Dir(dst)
			mustNoErr(t, "hash copy", err)
			if gotHash.SHA != wantHash.SHA || !slices.Equal(gotHash.Nodes, wantHash.Nodes) {
				t.Fatalf("copied treehash mismatch:\ngot:  %+v\nwant: %+v", gotHash, wantHash)
			}

			// Verify empty directories and directory permissions were also preserved.
			if tt.root == fsadapter.ProjectRootAgents {
				if info, err := os.Lstat(filepath.Join(dst, "empty-dir")); err != nil || !info.IsDir() {
					t.Fatalf("empty-dir missing in copy: info=%v err=%v", info, err)
				}
				if info, err := os.Lstat(filepath.Join(dst, "assets")); err != nil || info.Mode().Perm() != 0o555 {
					t.Fatalf("assets perm = %v err=%v, want 0555", info, err)
				}
			} else {
				if info, err := os.Lstat(filepath.Join(dst, "empty-claude")); err != nil || !info.IsDir() {
					t.Fatalf("empty-claude missing in copy: info=%v err=%v", info, err)
				}
			}

			// Verify deterministic ReadDir listing and ReadFile content.
			entries, err := tree.ReadDir(".")
			mustNoErr(t, "ReadDir(.)", err)
			if len(entries) == 0 {
				t.Fatalf("ReadDir(.) returned empty slice")
			}
			for i := 1; i < len(entries); i++ {
				if entries[i-1].Name() >= entries[i].Name() {
					t.Fatalf("ReadDir(.) not strictly sorted: %q before %q", entries[i-1].Name(), entries[i].Name())
				}
			}
			data, err := tree.ReadFile("SKILL.md")
			mustNoErr(t, "ReadFile(SKILL.md)", err)
			if len(data) == 0 {
				t.Fatalf("ReadFile(SKILL.md) returned empty content")
			}
		})
	}
}

func TestProjectTreeAdapterPreservesSpecialBitsInStat(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	skillDir := filepath.Join(projectDir, ".agents", "skills", "sticky")
	filePath := filepath.Join(skillDir, "SKILL.md")
	mustWriteFileMode(t, filePath, "sticky\n", 0o644)
	mustNoErr(t, "chmod sticky", os.Chmod(filePath, 0o644|os.ModeSticky))

	scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(projectDir)
	mustNoErr(t, "Scan", err)
	binding, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "sticky"))
	if !ok {
		t.Fatal("Lookup(sticky) = false")
	}

	adapter := fsadapter.NewProjectTreeAdapter(paths.Guard{})
	tree, err := adapter.Open(scanned, binding)
	mustNoErr(t, "Open", err)
	defer func() { _ = tree.Close() }()

	info, err := tree.Stat("SKILL.md")
	mustNoErr(t, "Stat(SKILL.md)", err)
	if info.Mode()&iofs.ModeSticky == 0 {
		t.Fatalf("Stat(SKILL.md).Mode() = %v, want ModeSticky preserved", info.Mode())
	}
}

func TestProjectTreeAdapterUnchangedNeighbors(t *testing.T) {
	base := t.TempDir()
	parentDir := filepath.Join(base, "workspace")
	projectDir := filepath.Join(parentDir, "project")
	siblingDir := filepath.Join(parentDir, "project-sibling")

	mustWriteFileMode(t, filepath.Join(parentDir, ".agents", "skills", "parent-skill", "SKILL.md"), "parent", 0o644)
	mustWriteFileMode(t, filepath.Join(siblingDir, ".agents", "skills", "sibling-skill", "SKILL.md"), "sibling", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".codex", "skills", "codex-skill", "SKILL.md"), "codex", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "neighbor", "SKILL.md"), "neighbor", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".claude", "skills", "target", "SKILL.md"), "claude-target", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "target", "SKILL.md"), "agents-target", 0o644)
	mustWriteFileMode(t, filepath.Join(projectDir, ".agents", "skills", "target", "sub", "data.txt"), "data", 0o644)

	watchDirs := []string{
		filepath.Join(parentDir, ".agents", "skills", "parent-skill"),
		filepath.Join(siblingDir, ".agents", "skills", "sibling-skill"),
		filepath.Join(projectDir, ".codex", "skills", "codex-skill"),
		filepath.Join(projectDir, ".agents", "skills", "neighbor"),
		filepath.Join(projectDir, ".claude", "skills", "target"),
		filepath.Join(projectDir, ".agents", "skills", "target"),
	}
	before := make([]treehash.Tree, len(watchDirs))
	for i, d := range watchDirs {
		h, err := treehash.Dir(d)
		mustNoErr(t, "hash before "+d, err)
		before[i] = h
	}

	scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(projectDir)
	mustNoErr(t, "Scan", err)
	binding, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "target"))
	if !ok {
		t.Fatal("Lookup(target) = false")
	}

	adapter := fsadapter.NewProjectTreeAdapter(paths.Guard{})
	tree, err := adapter.Open(scanned, binding)
	mustNoErr(t, "Open", err)

	dst := filepath.Join(base, "backup-copy")
	copyFromProjectTree(t, tree, dst)
	mustNoErr(t, "Close", tree.Close())

	for i, d := range watchDirs {
		after, err := treehash.Dir(d)
		mustNoErr(t, "hash after "+d, err)
		if after.SHA != before[i].SHA {
			t.Errorf("%s changed: %s -> %s", d, before[i].SHA, after.SHA)
		}
	}
}

func TestProjectTreeAdapterRejectsInvalidAndArbitraryBindings(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	mustWriteFileMode(t, filepath.Join(forbidden, "proj", ".agents", "skills", "secret", "SKILL.md"), "secret", 0o644)
	beforeForbidden, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden before", err)

	prefixes := filepath.SplitList(testsupport.NewHome(t).Env().ForbidPrefix)
	if len(prefixes) == 0 {
		t.Fatal("testsupport.Main set no forbidden prefix")
	}
	realHome := prefixes[0]

	proj := filepath.Join(base, "proj")
	mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "valid", "SKILL.md"), "valid", 0o644)
	mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "other-skill", "SKILL.md"), "other", 0o644)

	scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(proj)
	mustNoErr(t, "Scan proj", err)

	mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "blocked-skill", "SKILL.md"), "blocked", 0o644)
	mustWriteFileMode(t, filepath.Join(proj, ".codex", "skills", "codex", "SKILL.md"), "codex", 0o644)

	guard := paths.NewGuard(
		forbidden +
			string(os.PathListSeparator) +
			realHome +
			string(os.PathListSeparator) +
			filepath.Join(proj, ".agents", "skills", "blocked-skill"),
	)
	adapter := fsadapter.NewProjectTreeAdapter(guard)
	validObserved, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "valid"))
	if !ok {
		t.Fatal("Lookup(valid) = false")
	}

	tests := []struct {
		name    string
		skill   fsadapter.ProjectSkill
		wantErr error
	}{
		{
			name:    "empty project dir",
			skill:   observedBinding("", fsadapter.ProjectRootAgents, "valid"),
			wantErr: paths.ErrInvalidPath,
		},
		{
			name:    "relative project dir",
			skill:   observedBinding("rel/proj", fsadapter.ProjectRootAgents, "valid"),
			wantErr: paths.ErrInvalidPath,
		},
		{
			name:    "parent traversal in project dir",
			skill:   observedBinding(proj+"/../proj", fsadapter.ProjectRootAgents, "valid"),
			wantErr: paths.ErrInvalidPath,
		},
		{
			name:    "forbidden project dir",
			skill:   observedBinding(filepath.Join(forbidden, "proj"), fsadapter.ProjectRootAgents, "secret"),
			wantErr: paths.ErrForbidden,
		},
		{
			name:    "real home project dir",
			skill:   observedBinding(realHome, fsadapter.ProjectRootAgents, "valid"),
			wantErr: paths.ErrForbidden,
		},
		{
			name:    "unsupported root kind",
			skill:   observedBinding(proj, ".codex/skills", "codex"),
			wantErr: fsadapter.ErrProjectRootUnsupported,
		},
		{
			name:    "empty root kind",
			skill:   observedBinding(proj, "", "valid"),
			wantErr: fsadapter.ErrProjectRootUnsupported,
		},
		{
			name:    "empty skill name",
			skill:   observedBinding(proj, fsadapter.ProjectRootAgents, ""),
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name:    "dot skill name",
			skill:   observedBinding(proj, fsadapter.ProjectRootAgents, "."),
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name:    "dotdot skill name",
			skill:   observedBinding(proj, fsadapter.ProjectRootAgents, ".."),
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name:    "git skill name",
			skill:   observedBinding(proj, fsadapter.ProjectRootAgents, ".git"),
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name:    "nested path in skill name",
			skill:   observedBinding(proj, fsadapter.ProjectRootAgents, "valid/sub"),
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "missing all observed fields",
			skill: fsadapter.ProjectSkill{
				ProjectDir: proj,
				Root:       fsadapter.ProjectRootAgents,
				Name:       "valid",
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "missing ID",
			skill: fsadapter.ProjectSkill{
				ProjectDir:  validObserved.ProjectDir,
				Root:        validObserved.Root,
				Name:        validObserved.Name,
				Path:        validObserved.Path,
				SkillMDPath: validObserved.SkillMDPath,
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "missing Path",
			skill: fsadapter.ProjectSkill{
				ID:          validObserved.ID,
				ProjectDir:  validObserved.ProjectDir,
				Root:        validObserved.Root,
				Name:        validObserved.Name,
				SkillMDPath: validObserved.SkillMDPath,
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "missing SkillMDPath",
			skill: fsadapter.ProjectSkill{
				ID:         validObserved.ID,
				ProjectDir: validObserved.ProjectDir,
				Root:       validObserved.Root,
				Name:       validObserved.Name,
				Path:       validObserved.Path,
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "uncleaned ProjectDir",
			skill: fsadapter.ProjectSkill{
				ID:          validObserved.ID,
				ProjectDir:  proj + string(filepath.Separator),
				Root:        validObserved.Root,
				Name:        validObserved.Name,
				Path:        validObserved.Path,
				SkillMDPath: validObserved.SkillMDPath,
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "mismatched ID",
			skill: fsadapter.ProjectSkill{
				ID:          "project:.claude/skills/valid",
				ProjectDir:  proj,
				Root:        fsadapter.ProjectRootAgents,
				Name:        "valid",
				Path:        validObserved.Path,
				SkillMDPath: validObserved.SkillMDPath,
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "arbitrary Path override",
			skill: fsadapter.ProjectSkill{
				ID:          validObserved.ID,
				ProjectDir:  proj,
				Root:        fsadapter.ProjectRootAgents,
				Name:        "valid",
				Path:        filepath.Join(proj, ".agents", "skills", "other-skill"),
				SkillMDPath: validObserved.SkillMDPath,
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name: "arbitrary SkillMDPath override",
			skill: fsadapter.ProjectSkill{
				ID:          validObserved.ID,
				ProjectDir:  proj,
				Root:        fsadapter.ProjectRootAgents,
				Name:        "valid",
				Path:        validObserved.Path,
				SkillMDPath: filepath.Join(proj, ".agents", "skills", "other-skill", "SKILL.md"),
			},
			wantErr: fsadapter.ErrProjectSkillInvalid,
		},
		{
			name:    "forbidden skill prefix",
			skill:   observedBinding(proj, fsadapter.ProjectRootAgents, "blocked-skill"),
			wantErr: paths.ErrForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, err := adapter.Open(scanned, tt.skill)
			if !errors.Is(err, tt.wantErr) {
				if tree != nil {
					_ = tree.Close()
				}
				t.Fatalf("Open(%+v) error = %v, want %v", tt.skill, err, tt.wantErr)
			}
			if tree != nil {
				_ = tree.Close()
				t.Fatalf("Open(%+v) returned non-nil tree on error", tt.skill)
			}
		})
	}

	toctouTests := []struct {
		name    string
		mutate  func(t *testing.T, caseProj, skillDir string)
		wantErr error
	}{
		{
			name: "missing skill",
			mutate: func(t *testing.T, _, skillDir string) {
				mustNoErr(t, "remove skill dir", os.RemoveAll(skillDir))
			},
			wantErr: iofs.ErrNotExist,
		},
		{
			name: "missing root",
			mutate: func(t *testing.T, caseProj, _ string) {
				mustNoErr(t, "remove agents skills root", os.RemoveAll(filepath.Join(caseProj, ".agents", "skills")))
			},
			wantErr: iofs.ErrNotExist,
		},
		{
			name: "missing SKILL.md",
			mutate: func(t *testing.T, _, skillDir string) {
				mustNoErr(t, "remove SKILL.md", os.Remove(filepath.Join(skillDir, "SKILL.md")))
			},
			wantErr: iofs.ErrNotExist,
		},
		{
			name: "plain file instead of skill dir",
			mutate: func(t *testing.T, _, skillDir string) {
				mustNoErr(t, "remove skill dir", os.RemoveAll(skillDir))
				mustWriteFileMode(t, skillDir, "not a dir", 0o644)
			},
			wantErr: fsadapter.ErrProjectTreeNotDir,
		},
		{
			name: "directory SKILL.md",
			mutate: func(t *testing.T, _, skillDir string) {
				mustNoErr(t, "remove SKILL.md", os.Remove(filepath.Join(skillDir, "SKILL.md")))
				mustMkdirAll(t, filepath.Join(skillDir, "SKILL.md"))
			},
			wantErr: fsadapter.ErrProjectTreeUnsupported,
		},
		{
			name: "fifo SKILL.md",
			mutate: func(t *testing.T, _, skillDir string) {
				mustNoErr(t, "remove SKILL.md", os.Remove(filepath.Join(skillDir, "SKILL.md")))
				mustNoErr(t, "mkfifo", syscall.Mkfifo(filepath.Join(skillDir, "SKILL.md"), 0o644))
			},
			wantErr: fsadapter.ErrProjectTreeUnsupported,
		},
	}

	for _, tt := range toctouTests {
		t.Run(tt.name, func(t *testing.T) {
			caseProj := filepath.Join(base, "toctou-"+path.Base(tt.name))
			skillDir := filepath.Join(caseProj, ".agents", "skills", "target")
			mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)

			caseScanned, err := fsadapter.NewProjectSkillScanner(guard).Scan(caseProj)
			mustNoErr(t, "Scan caseProj", err)
			observed, ok := caseScanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "target"))
			if !ok {
				t.Fatal("Lookup(target) = false")
			}

			tt.mutate(t, caseProj, skillDir)

			tree, err := adapter.Open(caseScanned, observed)
			if !errors.Is(err, tt.wantErr) {
				if tree != nil {
					_ = tree.Close()
				}
				t.Fatalf("Open(%+v) error = %v, want %v", observed, err, tt.wantErr)
			}
			if tree != nil {
				_ = tree.Close()
				t.Fatalf("Open(%+v) returned non-nil tree on error", observed)
			}
		})
	}

	afterForbidden, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden after", err)
	if beforeForbidden.SHA != afterForbidden.SHA {
		t.Fatalf("forbidden tree changed: %s -> %s", beforeForbidden.SHA, afterForbidden.SHA)
	}
}

func TestProjectTreeAdapterRefusesSymlinkComponentsAndDescendants(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	mustWriteFileMode(t, filepath.Join(forbidden, "secret-dir", "SKILL.md"), "secret", 0o644)
	mustWriteFileMode(t, filepath.Join(forbidden, "secret.txt"), "secret-file", 0o644)
	beforeForbidden, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden before", err)

	guard := paths.NewGuard(forbidden)
	adapter := fsadapter.NewProjectTreeAdapter(guard)

	t.Run("rejects symlink startup dir and ancestor", func(t *testing.T) {
		realProj := filepath.Join(base, "real-proj")
		mustWriteFileMode(t, filepath.Join(realProj, "nested", ".agents", "skills", "ok", "SKILL.md"), "ok", 0o644)
		linkedProj := filepath.Join(base, "linked-proj")
		mustSymlink(t, realProj, linkedProj)

		// Unscanned selection against a symlink startup dir is rejected before disk traversal.
		for _, dir := range []string{linkedProj, filepath.Join(linkedProj, "nested")} {
			tree, err := adapter.Open(fsadapter.ProjectSkills{}, observedBinding(dir, fsadapter.ProjectRootAgents, "ok"))
			if !errors.Is(err, fsadapter.ErrProjectSkillInvalid) {
				if tree != nil {
					_ = tree.Close()
				}
				t.Fatalf("Open(empty, %q) err = %v, want ErrProjectSkillInvalid", dir, err)
			}
		}

		// A real startup dir replaced with a symlink after scanning is rejected with ErrProjectDirLinked.
		postScanProj := filepath.Join(base, "post-scan-real-proj")
		mustWriteFileMode(t, filepath.Join(postScanProj, ".agents", "skills", "ok", "SKILL.md"), "ok", 0o644)
		mustWriteFileMode(t, filepath.Join(postScanProj, "nested", ".agents", "skills", "ok", "SKILL.md"), "ok", 0o644)
		for _, dir := range []string{postScanProj, filepath.Join(postScanProj, "nested")} {
			scanned, err := fsadapter.NewProjectSkillScanner(guard).Scan(dir)
			mustNoErr(t, "Scan "+dir, err)
			observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "ok"))
			if !ok {
				t.Fatalf("Lookup(ok) = false for %s", dir)
			}

			renamed := postScanProj + "-moved"
			mustNoErr(t, "rename postScanProj", os.Rename(postScanProj, renamed))
			mustSymlink(t, renamed, postScanProj)

			tree, err := adapter.Open(scanned, observed)
			if !errors.Is(err, fsadapter.ErrProjectDirLinked) {
				if tree != nil {
					_ = tree.Close()
				}
				t.Fatalf("Open(%q) err = %v, want ErrProjectDirLinked", dir, err)
			}

			mustNoErr(t, "remove symlink postScanProj", os.Remove(postScanProj))
			mustNoErr(t, "restore postScanProj", os.Rename(renamed, postScanProj))
		}
	})

	t.Run("rejects symlink .agents, skills, skill dir, and SKILL.md", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(t *testing.T, proj string)
		}{
			{
				name: "linked .agents",
				mutate: func(t *testing.T, proj string) {
					mustNoErr(t, "remove .agents", os.RemoveAll(filepath.Join(proj, ".agents")))
					mustSymlink(t, forbidden, filepath.Join(proj, ".agents"))
				},
			},
			{
				name: "linked .agents/skills",
				mutate: func(t *testing.T, proj string) {
					mustNoErr(t, "remove .agents/skills", os.RemoveAll(filepath.Join(proj, ".agents", "skills")))
					mustSymlink(t, forbidden, filepath.Join(proj, ".agents", "skills"))
				},
			},
			{
				name: "linked skill dir",
				mutate: func(t *testing.T, proj string) {
					skillDir := filepath.Join(proj, ".agents", "skills", "target")
					mustNoErr(t, "remove skill dir", os.RemoveAll(skillDir))
					mustSymlink(t, filepath.Join(forbidden, "secret-dir"), skillDir)
				},
			},
			{
				name: "linked SKILL.md",
				mutate: func(t *testing.T, proj string) {
					skillMD := filepath.Join(proj, ".agents", "skills", "target", "SKILL.md")
					mustNoErr(t, "remove SKILL.md", os.Remove(skillMD))
					mustSymlink(t, filepath.Join(forbidden, "secret.txt"), skillMD)
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				proj := filepath.Join(base, "case-"+path.Base(tc.name))
				mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "target", "SKILL.md"), "ok", 0o644)

				scanned, err := fsadapter.NewProjectSkillScanner(guard).Scan(proj)
				mustNoErr(t, "Scan "+tc.name, err)
				observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "target"))
				if !ok {
					t.Fatal("Lookup(target) = false")
				}

				tc.mutate(t, proj)

				tree, err := adapter.Open(scanned, observed)
				if !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
					if tree != nil {
						_ = tree.Close()
					}
					t.Fatalf("Open(%s) err = %v, want ErrProjectTreeLinked", tc.name, err)
				}
			})
		}
	})

	t.Run("rejects symlink descendants during traversal and file reads", func(t *testing.T) {
		proj := filepath.Join(base, "descendant-links")
		skillDir := filepath.Join(proj, ".claude", "skills", "target")
		mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)
		mustSymlink(t, filepath.Join(forbidden, "secret.txt"), filepath.Join(skillDir, "leak.txt"))
		mustSymlink(t, filepath.Join(forbidden, "secret-dir"), filepath.Join(skillDir, "sub"))
		mustSymlink(t, filepath.Join(base, "does-not-exist"), filepath.Join(skillDir, "dangling"))

		scanned, err := fsadapter.NewProjectSkillScanner(guard).Scan(proj)
		mustNoErr(t, "Scan descendant-links", err)
		observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootClaude, "target"))
		if !ok {
			t.Fatal("Lookup(target) = false")
		}

		tree, err := adapter.Open(scanned, observed)
		mustNoErr(t, "Open", err)
		defer func() { mustNoErr(t, "Close", tree.Close()) }()

		if _, err := tree.ReadDir("."); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
			t.Fatalf("ReadDir(.) err = %v, want ErrProjectTreeLinked", err)
		}
		if err := walkProjectTree(tree, ".", func(_ string, _ iofs.DirEntry, walkErr error) error {
			return walkErr
		}); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
			t.Fatalf("walkProjectTree(.) err = %v, want ErrProjectTreeLinked", err)
		}
		for _, target := range []string{"leak.txt", "sub", "sub/SKILL.md", "dangling"} {
			if _, err := tree.Stat(target); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
				t.Errorf("Stat(%q) err = %v, want ErrProjectTreeLinked", target, err)
			}
			if _, err := tree.ReadFile(target); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
				t.Errorf("ReadFile(%q) err = %v, want ErrProjectTreeLinked", target, err)
			}
		}
	})

	afterForbidden, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden after", err)
	if beforeForbidden.SHA != afterForbidden.SHA {
		t.Fatalf("forbidden tree changed: %s -> %s", beforeForbidden.SHA, afterForbidden.SHA)
	}
}

func TestProjectTreeAdapterRefusesGitUnsupportedEntriesAndUnprovenScope(t *testing.T) {
	base := t.TempDir()

	t.Run("refuses .git directory, .git file, and nested .git", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			relGit string
			asFile bool
		}{
			{name: "root .git dir", relGit: ".git", asFile: false},
			{name: "root .git file", relGit: ".git", asFile: true},
			{name: "nested .git dir", relGit: "sub/.git", asFile: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				proj := filepath.Join(base, "git-"+path.Base(tc.name))
				skillDir := filepath.Join(proj, ".agents", "skills", "cloned")
				mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)
				if tc.asFile {
					mustWriteFileMode(t, filepath.Join(skillDir, filepath.FromSlash(tc.relGit)), "gitdir: ../elsewhere\n", 0o644)
				} else {
					mustWriteFileMode(t, filepath.Join(skillDir, filepath.FromSlash(tc.relGit), "HEAD"), "ref: refs/heads/main\n", 0o644)
				}

				scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(proj)
				mustNoErr(t, "Scan cloned", err)
				observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "cloned"))
				if !ok {
					t.Fatal("Lookup(cloned) = false")
				}

				tree, err := fsadapter.NewProjectTreeAdapter(paths.Guard{}).Open(scanned, observed)
				mustNoErr(t, "Open", err)
				defer func() { mustNoErr(t, "Close", tree.Close()) }()

				err = walkProjectTree(tree, ".", func(_ string, _ iofs.DirEntry, walkErr error) error {
					return walkErr
				})
				if !errors.Is(err, fsadapter.ErrProjectTreeGit) {
					t.Fatalf("walkProjectTree err = %v, want ErrProjectTreeGit", err)
				}
				pathsToCheck := []string{tc.relGit}
				if !tc.asFile {
					pathsToCheck = append(pathsToCheck, tc.relGit+"/HEAD")
				}
				for _, p := range pathsToCheck {
					if _, err := tree.Stat(p); !errors.Is(err, fsadapter.ErrProjectTreeGit) {
						t.Errorf("Stat(%q) err = %v, want ErrProjectTreeGit", p, err)
					}
					if _, err := tree.ReadFile(p); !errors.Is(err, fsadapter.ErrProjectTreeGit) {
						t.Errorf("ReadFile(%q) err = %v, want ErrProjectTreeGit", p, err)
					}
				}
			})
		}
	})

	if os.Geteuid() != 0 {
		t.Run("fails closed on unreadable file and directory", func(t *testing.T) {
			proj := filepath.Join(base, "perm-proj")
			skillDir := filepath.Join(proj, ".agents", "skills", "locked")
			mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)
			lockedFile := filepath.Join(skillDir, "secret.txt")
			mustWriteFileMode(t, lockedFile, "secret", 0o000)
			lockedDir := filepath.Join(skillDir, "locked-sub")
			mustWriteFileMode(t, filepath.Join(lockedDir, "inner.txt"), "inner", 0o644)
			mustNoErr(t, "chmod lockedDir", os.Chmod(lockedDir, 0o000))
			t.Cleanup(func() {
				_ = os.Chmod(lockedFile, 0o644)
				_ = os.Chmod(lockedDir, 0o755)
			})

			scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(proj)
			mustNoErr(t, "Scan locked", err)
			observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "locked"))
			if !ok {
				t.Fatal("Lookup(locked) = false")
			}

			tree, err := fsadapter.NewProjectTreeAdapter(paths.Guard{}).Open(scanned, observed)
			mustNoErr(t, "Open", err)
			defer func() { mustNoErr(t, "Close", tree.Close()) }()

			if _, err := tree.ReadFile("secret.txt"); !errors.Is(err, iofs.ErrPermission) {
				t.Errorf("ReadFile(secret.txt) err = %v, want ErrPermission", err)
			}
			if _, err := tree.ReadDir("locked-sub"); !errors.Is(err, iofs.ErrPermission) {
				t.Errorf("ReadDir(locked-sub) err = %v, want ErrPermission", err)
			}
		})
	}

	t.Run("refuses FIFO unsupported entry and invalid paths", func(t *testing.T) {
		proj := filepath.Join(base, "fifo-proj")
		skillDir := filepath.Join(proj, ".agents", "skills", "with-fifo")
		mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)
		mustMkdirAll(t, filepath.Join(skillDir, "sub"))
		mustNoErr(t, "mkfifo", syscall.Mkfifo(filepath.Join(skillDir, "sub", "pipe"), 0o644))

		scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(proj)
		mustNoErr(t, "Scan with-fifo", err)
		observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "with-fifo"))
		if !ok {
			t.Fatal("Lookup(with-fifo) = false")
		}

		tree, err := fsadapter.NewProjectTreeAdapter(paths.Guard{}).Open(scanned, observed)
		mustNoErr(t, "Open", err)
		defer func() { mustNoErr(t, "Close", tree.Close()) }()

		if _, err := tree.ReadDir("sub"); !errors.Is(err, fsadapter.ErrProjectTreeUnsupported) {
			t.Fatalf("ReadDir(sub) err = %v, want ErrProjectTreeUnsupported", err)
		}
		if _, err := tree.Stat("sub/pipe"); !errors.Is(err, fsadapter.ErrProjectTreeUnsupported) {
			t.Fatalf("Stat(sub/pipe) err = %v, want ErrProjectTreeUnsupported", err)
		}
		if _, err := tree.ReadFile("sub/pipe"); !errors.Is(err, fsadapter.ErrProjectTreeUnsupported) {
			t.Fatalf("ReadFile(sub/pipe) err = %v, want ErrProjectTreeUnsupported", err)
		}

		for _, badRel := range []string{"", "/SKILL.md", "../with-fifo/SKILL.md", "./SKILL.md", "sub/"} {
			if _, err := tree.Stat(badRel); !errors.Is(err, iofs.ErrInvalid) {
				t.Errorf("Stat(%q) err = %v, want ErrInvalid", badRel, err)
			}
			if _, err := tree.ReadDir(badRel); !errors.Is(err, iofs.ErrInvalid) {
				t.Errorf("ReadDir(%q) err = %v, want ErrInvalid", badRel, err)
			}
			if _, err := tree.ReadFile(badRel); !errors.Is(err, iofs.ErrInvalid) {
				t.Errorf("ReadFile(%q) err = %v, want ErrInvalid", badRel, err)
			}
		}

		// Reading a directory as a file and listing a regular file as a directory must fail.
		for _, dirRel := range []string{".", "sub"} {
			if _, err := tree.ReadFile(dirRel); err == nil {
				t.Errorf("ReadFile(%q) succeeded on directory, want error", dirRel)
			}
		}
		if _, err := tree.ReadDir("SKILL.md"); !errors.Is(err, fsadapter.ErrProjectTreeNotDir) {
			t.Errorf("ReadDir(SKILL.md) err = %v, want ErrProjectTreeNotDir", err)
		}
	})

	t.Run("fails closed when opened directory moves outside scope", func(t *testing.T) {
		proj := filepath.Join(base, "moved-proj")
		skillDir := filepath.Join(proj, ".agents", "skills", "movable")
		mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)

		scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(proj)
		mustNoErr(t, "Scan movable", err)
		observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "movable"))
		if !ok {
			t.Fatal("Lookup(movable) = false")
		}

		tree, err := fsadapter.NewProjectTreeAdapter(paths.Guard{}).Open(scanned, observed)
		mustNoErr(t, "Open", err)
		defer func() { _ = tree.Close() }()

		moved := filepath.Join(base, "moved-away")
		mustNoErr(t, "rename skill outside scope", os.Rename(skillDir, moved))

		if _, err := tree.ReadFile("SKILL.md"); !errors.Is(err, fsadapter.ErrProjectScopeUnproven) {
			t.Fatalf("ReadFile after move err = %v, want ErrProjectScopeUnproven", err)
		}
		if _, err := tree.ReadDir("."); !errors.Is(err, fsadapter.ErrProjectScopeUnproven) {
			t.Fatalf("ReadDir after move err = %v, want ErrProjectScopeUnproven", err)
		}
		if _, err := tree.Stat("SKILL.md"); !errors.Is(err, fsadapter.ErrProjectScopeUnproven) {
			t.Fatalf("Stat after move err = %v, want ErrProjectScopeUnproven", err)
		}
	})

	t.Run("checks forbidden lexical prefixes inside opened tree", func(t *testing.T) {
		proj := filepath.Join(base, "guarded-inside")
		skillDir := filepath.Join(proj, ".agents", "skills", "guarded")
		mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)
		mustWriteFileMode(t, filepath.Join(skillDir, "secret", "token.txt"), "top-secret", 0o644)

		guard := paths.NewGuard(filepath.Join(skillDir, "secret"))
		scanned, err := fsadapter.NewProjectSkillScanner(guard).Scan(proj)
		mustNoErr(t, "Scan guarded", err)
		observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "guarded"))
		if !ok {
			t.Fatal("Lookup(guarded) = false")
		}

		tree, err := fsadapter.NewProjectTreeAdapter(guard).Open(scanned, observed)
		mustNoErr(t, "Open", err)
		defer func() { mustNoErr(t, "Close", tree.Close()) }()

		if _, err := tree.ReadDir("."); !errors.Is(err, paths.ErrForbidden) {
			t.Fatalf("ReadDir(.) err = %v, want ErrForbidden", err)
		}
		if _, err := tree.ReadFile("secret/token.txt"); !errors.Is(err, paths.ErrForbidden) {
			t.Fatalf("ReadFile(secret/token.txt) err = %v, want ErrForbidden", err)
		}
	})
}

func TestProjectTreeAdapterResourceCleanup(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "cleanup-proj")
	skillDir := filepath.Join(proj, ".agents", "skills", "clean")
	mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "ok", 0o644)
	mustWriteFileMode(t, filepath.Join(skillDir, "sub", "a.txt"), "a", 0o644)

	badProj := filepath.Join(base, "bad-cleanup-proj")
	badSkillDir := filepath.Join(badProj, ".agents", "skills", "bad")
	mustWriteFileMode(t, filepath.Join(badSkillDir, "SKILL.md"), "ok", 0o644)
	mustSymlink(t, filepath.Join(badSkillDir, "SKILL.md"), filepath.Join(badSkillDir, "link.md"))

	scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(proj)
	mustNoErr(t, "Scan proj", err)
	cleanObserved, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "clean"))
	if !ok {
		t.Fatal("Lookup(clean) = false")
	}

	badScanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(badProj)
	mustNoErr(t, "Scan badProj", err)
	badObserved, ok := badScanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "bad"))
	if !ok {
		t.Fatal("Lookup(bad) = false")
	}

	// An unscanned on-disk skill added after Scan must fail cleanly with zero leaked FDs.
	mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "unscanned", "SKILL.md"), "unscanned", 0o644)

	before := openFDs(t)
	adapter := fsadapter.NewProjectTreeAdapter(paths.Guard{})

	// Failed Open calls must leave zero open file descriptors.
	for _, badSkill := range []fsadapter.ProjectSkill{
		observedBinding(proj, fsadapter.ProjectRootAgents, "missing"),
		observedBinding(proj, fsadapter.ProjectRootAgents, "unscanned"),
		observedBinding(proj, ".codex/skills", "clean"),
		{ProjectDir: proj, Root: fsadapter.ProjectRootAgents, Name: "clean"},
	} {
		tree, err := adapter.Open(scanned, badSkill)
		if err == nil {
			_ = tree.Close()
			t.Fatalf("expected error for %+v", badSkill)
		}
	}
	if afterFailedOpen := openFDs(t); !slices.Equal(before, afterFailedOpen) {
		t.Fatalf("FDs leaked after failed Open:\nbefore: %v\nafter:  %v", before, afterFailedOpen)
	}

	// Successful operations followed by Close must return to baseline FDs.
	tree, err := adapter.Open(scanned, cleanObserved)
	mustNoErr(t, "Open", err)
	_, err = tree.Stat(".")
	mustNoErr(t, "Stat(.)", err)
	_, err = tree.ReadDir(".")
	mustNoErr(t, "ReadDir(.)", err)
	_, err = tree.ReadFile("sub/a.txt")
	mustNoErr(t, "ReadFile(sub/a.txt)", err)
	if afterOps := openFDs(t); len(afterOps) != len(before)+1 {
		t.Fatalf("expected only rootFD open while tree is open: before=%d after=%d", len(before), len(afterOps))
	}
	mustNoErr(t, "Close tree", tree.Close())
	// Close must be idempotent.
	mustNoErr(t, "Close tree again", tree.Close())

	if afterClose := openFDs(t); !slices.Equal(before, afterClose) {
		t.Fatalf("FDs leaked after Close:\nbefore: %v\nafter:  %v", before, afterClose)
	}

	// Operations after Close must fail closed with ErrProjectTreeClosed.
	if _, err := tree.Stat("."); !errors.Is(err, fsadapter.ErrProjectTreeClosed) {
		t.Errorf("Stat after Close err = %v, want ErrProjectTreeClosed", err)
	}
	if _, err := tree.ReadDir("."); !errors.Is(err, fsadapter.ErrProjectTreeClosed) {
		t.Errorf("ReadDir after Close err = %v, want ErrProjectTreeClosed", err)
	}
	if _, err := tree.ReadFile("SKILL.md"); !errors.Is(err, fsadapter.ErrProjectTreeClosed) {
		t.Errorf("ReadFile after Close err = %v, want ErrProjectTreeClosed", err)
	}

	// Failed traversal followed by Close must leave zero open FDs.
	badTree, err := adapter.Open(badScanned, badObserved)
	mustNoErr(t, "Open badTree", err)
	if _, err := badTree.ReadDir("."); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
		_ = badTree.Close()
		t.Fatalf("badTree.ReadDir(.) err = %v, want ErrProjectTreeLinked", err)
	}
	mustNoErr(t, "Close badTree", badTree.Close())

	if afterBad := openFDs(t); !slices.Equal(before, afterBad) {
		t.Fatalf("FDs leaked after badTree Close:\nbefore: %v\nafter:  %v", before, afterBad)
	}
}

func TestProjectTreeAdapterIndependentFindings(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	skillDir := filepath.Join(proj, ".agents", "skills", "target")
	mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "skill body\n", 0o644)
	mustWriteFileMode(t, filepath.Join(skillDir, "a.txt"), "alpha\n", 0o644)
	mustSymlink(t, filepath.Join(skillDir, "a.txt"), filepath.Join(skillDir, "z-link.txt"))

	scanned, err := fsadapter.NewProjectSkillScanner(paths.Guard{}).Scan(proj)
	mustNoErr(t, "Scan", err)
	observed, ok := scanned.Lookup(fsadapter.ProjectSkillID(fsadapter.ProjectRootAgents, "target"))
	if !ok {
		t.Fatal("Lookup(target) = false, want true")
	}

	adapter := fsadapter.NewProjectTreeAdapter(paths.Guard{})

	t.Run("rejects selector missing scanner-observed identity", func(t *testing.T) {
		incomplete := []struct {
			name  string
			skill fsadapter.ProjectSkill
		}{
			{
				name: "missing ID, Path, and SkillMDPath",
				skill: fsadapter.ProjectSkill{
					ProjectDir: proj,
					Root:       fsadapter.ProjectRootAgents,
					Name:       "target",
				},
			},
			{
				name: "missing ID only",
				skill: fsadapter.ProjectSkill{
					ProjectDir:  observed.ProjectDir,
					Root:        observed.Root,
					Name:        observed.Name,
					Path:        observed.Path,
					SkillMDPath: observed.SkillMDPath,
				},
			},
			{
				name: "missing Path only",
				skill: fsadapter.ProjectSkill{
					ID:          observed.ID,
					ProjectDir:  observed.ProjectDir,
					Root:        observed.Root,
					Name:        observed.Name,
					SkillMDPath: observed.SkillMDPath,
				},
			},
			{
				name: "missing SkillMDPath only",
				skill: fsadapter.ProjectSkill{
					ID:         observed.ID,
					ProjectDir: observed.ProjectDir,
					Root:       observed.Root,
					Name:       observed.Name,
					Path:       observed.Path,
				},
			},
			{
				name: "uncleaned ProjectDir",
				skill: fsadapter.ProjectSkill{
					ID:          observed.ID,
					ProjectDir:  proj + string(filepath.Separator),
					Root:        observed.Root,
					Name:        observed.Name,
					Path:        observed.Path,
					SkillMDPath: observed.SkillMDPath,
				},
			},
		}

		for _, tc := range incomplete {
			t.Run(tc.name, func(t *testing.T) {
				tree, err := adapter.Open(scanned, tc.skill)
				if !errors.Is(err, fsadapter.ErrProjectSkillInvalid) {
					if tree != nil {
						_ = tree.Close()
					}
					t.Fatalf("Open(%s) err = %v, want ErrProjectSkillInvalid", tc.name, err)
				}
			})
		}
	})

	t.Run("one-shot interface exposes no Open or raw handles and repeats ReadDir refusal", func(t *testing.T) {
		tree, err := adapter.Open(scanned, observed)
		mustNoErr(t, "Open", err)
		defer func() { _ = tree.Close() }()

		if _, hasOpen := any(tree).(iofs.FS); hasOpen {
			t.Fatal("ProjectTree must not implement io/fs.FS or expose Open raw handles")
		}

		for attempt := 1; attempt <= 2; attempt++ {
			entries, err := tree.ReadDir(".")
			if !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
				t.Fatalf("ReadDir(.) attempt %d err = %v, want ErrProjectTreeLinked", attempt, err)
			}
			if len(entries) != 0 {
				t.Fatalf("ReadDir(.) attempt %d returned %d partial entries, want 0", attempt, len(entries))
			}
		}

		// Closing the tree must immediately block subsequent one-shot reads.
		mustNoErr(t, "Close", tree.Close())
		if _, err := tree.ReadFile("SKILL.md"); !errors.Is(err, fsadapter.ErrProjectTreeClosed) {
			t.Fatalf("ReadFile after Close err = %v, want ErrProjectTreeClosed", err)
		}
	})
}

func TestProjectTreeAdapterRejectsUnscannedSelections(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "present", "SKILL.md"), "present\n", 0o644)

	otherProj := filepath.Join(base, "other-proj")
	mustWriteFileMode(t, filepath.Join(otherProj, ".agents", "skills", "present", "SKILL.md"), "other\n", 0o644)

	scanner := fsadapter.NewProjectSkillScanner(paths.Guard{})
	scanned, err := scanner.Scan(proj)
	mustNoErr(t, "Scan proj", err)
	otherScanned, err := scanner.Scan(otherProj)
	mustNoErr(t, "Scan otherProj", err)

	// Materialize valid skill directories on disk AFTER Scan(proj) ran. None of these
	// exact copies were emitted by scanned, even though their canonical paths match.
	mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "added-after-scan", "SKILL.md"), "added\n", 0o644)
	mustWriteFileMode(t, filepath.Join(proj, ".claude", "skills", "present", "SKILL.md"), "claude-added\n", 0o644)

	presentBinding := observedBinding(proj, fsadapter.ProjectRootAgents, "present")
	addedBinding := observedBinding(proj, fsadapter.ProjectRootAgents, "added-after-scan")
	claudeAddedBinding := observedBinding(proj, fsadapter.ProjectRootClaude, "present")

	adapter := fsadapter.NewProjectTreeAdapter(paths.Guard{})
	before := openFDs(t)

	cases := []struct {
		name    string
		scanned fsadapter.ProjectSkills
		skill   fsadapter.ProjectSkill
	}{
		{
			name:    "skill added on disk after scan even when paths match",
			scanned: scanned,
			skill:   addedBinding,
		},
		{
			name:    "homonym in other root added after scan",
			scanned: scanned,
			skill:   claudeAddedBinding,
		},
		{
			name:    "zero ProjectSkills with exact on-disk canonical selector",
			scanned: fsadapter.ProjectSkills{},
			skill:   presentBinding,
		},
		{
			name:    "cross-project scan replay with matching root and skill name",
			scanned: otherScanned,
			skill:   presentBinding,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := adapter.Open(tc.scanned, tc.skill)
			if !errors.Is(err, fsadapter.ErrProjectSkillInvalid) {
				if tree != nil {
					_ = tree.Close()
				}
				t.Fatalf("Open(%s) err = %v, want ErrProjectSkillInvalid", tc.name, err)
			}
			if tree != nil {
				_ = tree.Close()
				t.Fatalf("Open(%s) returned non-nil tree on error", tc.name)
			}
		})
	}

	// Empty project scan (valid projectDir, zero emitted skills) also rejects later skills.
	emptyProj := filepath.Join(base, "empty-proj")
	mustMkdirAll(t, emptyProj)
	emptyScanned, err := scanner.Scan(emptyProj)
	mustNoErr(t, "Scan emptyProj", err)
	mustWriteFileMode(t, filepath.Join(emptyProj, ".agents", "skills", "late", "SKILL.md"), "late\n", 0o644)
	if tree, err := adapter.Open(emptyScanned, observedBinding(emptyProj, fsadapter.ProjectRootAgents, "late")); !errors.Is(err, fsadapter.ErrProjectSkillInvalid) {
		if tree != nil {
			_ = tree.Close()
		}
		t.Fatalf("Open(emptyScanned, late) err = %v, want ErrProjectSkillInvalid", err)
	}

	if after := openFDs(t); !slices.Equal(before, after) {
		t.Fatalf("FDs leaked after rejected Open calls:\nbefore: %v\nafter:  %v", before, after)
	}
}
