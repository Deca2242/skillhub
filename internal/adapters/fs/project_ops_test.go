package fs_test

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
)

func TestProjectOpsObserveCopyRemoveRestoreParityAndCleanup(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	skillDir := filepath.Join(proj, ".agents", "skills", "alpha")
	mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: alpha\n---\nAlpha\n", 0o644)
	mustWriteFileMode(t, filepath.Join(skillDir, "scripts", "run.sh"), "#!/bin/sh\necho ok\n", 0o755)
	mustWriteFileMode(t, filepath.Join(skillDir, "assets", "read-only.txt"), "data\n", 0o440)
	mustMkdirAll(t, filepath.Join(skillDir, "empty-dir"))
	mustNoErr(t, "chmod assets", os.Chmod(filepath.Join(skillDir, "assets"), 0o555))
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(skillDir, "assets"), 0o755)
	})

	wantHash, err := treehash.Dir(skillDir)
	mustNoErr(t, "Dir before", err)

	ops := fsadapter.NewProjectOps(paths.Guard{})
	beforeFDs := openFDs(t)

	// SameDevice on same filesystem must return true.
	stagingRoot := filepath.Join(base, "skillhub-internal")
	mustMkdirAll(t, stagingRoot)
	sameDev, err := ops.SameDevice(proj, string(fsadapter.ProjectRootAgents), filepath.Join(stagingRoot, "future", "trash"))
	mustNoErr(t, "SameDevice", err)
	if !sameDev {
		t.Fatal("SameDevice = false inside same temp filesystem, want true")
	}

	// ObserveSkill sees exact tree hash.
	exists, foreign, gotHash, err := ops.ObserveSkill(proj, string(fsadapter.ProjectRootAgents), "alpha")
	mustNoErr(t, "ObserveSkill(alpha)", err)
	if !exists || foreign {
		t.Fatalf("ObserveSkill(alpha) exists=%v foreign=%v, want true/false", exists, foreign)
	}
	if gotHash.SHA != wantHash.SHA || !slices.Equal(gotHash.Nodes, wantHash.Nodes) {
		t.Fatalf("ObserveSkill hash mismatch: got=%+v want=%+v", gotHash, wantHash)
	}

	// Sibling root without alpha reports absent, not error.
	existsClaude, foreignClaude, _, err := ops.ObserveSkill(proj, string(fsadapter.ProjectRootClaude), "alpha")
	mustNoErr(t, "ObserveSkill(claude/alpha)", err)
	if existsClaude || foreignClaude {
		t.Fatalf("ObserveSkill(claude/alpha) exists=%v foreign=%v, want false/false", existsClaude, foreignClaude)
	}

	// CopySkill duplicates to snapshot dir preserving modes and exact treehash.
	snapCopy := filepath.Join(stagingRoot, "snapshots", "op-1", "trees", "0")
	mustMkdirAll(t, filepath.Dir(snapCopy))
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(snapCopy, "assets"), 0o755)
	})
	copiedTree, err := ops.CopySkill(proj, string(fsadapter.ProjectRootAgents), "alpha", snapCopy)
	mustNoErr(t, "CopySkill", err)
	if copiedTree.SHA != wantHash.SHA {
		t.Fatalf("CopySkill SHA = %s, want %s", copiedTree.SHA, wantHash.SHA)
	}

	// RemoveSkillToTrash moves alpha atomically to trash destination and verifies absence.
	trashDest := filepath.Join(stagingRoot, "trash", "op-1-0")
	mustMkdirAll(t, filepath.Dir(trashDest))
	mustNoErr(t, "RemoveSkillToTrash", ops.RemoveSkillToTrash(proj, string(fsadapter.ProjectRootAgents), "alpha", trashDest))

	if _, err := os.Lstat(skillDir); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("skillDir still exists after RemoveSkillToTrash: %v", err)
	}
	trashHash, err := treehash.Dir(trashDest)
	mustNoErr(t, "hash trashDest", err)
	if trashHash.SHA != wantHash.SHA {
		t.Fatalf("trashDest SHA = %s, want %s", trashHash.SHA, wantHash.SHA)
	}

	// Removing .agents/skills completely tests that RestoreSkillFromStaging recreates missing roots safely.
	mustNoErr(t, "remove .agents/skills", os.RemoveAll(filepath.Join(proj, ".agents")))

	restoreStaging := filepath.Join(stagingRoot, "restore", "op-1-0")
	mustMkdirAll(t, filepath.Dir(restoreStaging))
	if _, err := os.Lstat(snapCopy); err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, "Rename trash back to restoreStaging", os.Rename(trashDest, restoreStaging))

	mustNoErr(t, "RestoreSkillFromStaging", ops.RestoreSkillFromStaging(restoreStaging, proj, string(fsadapter.ProjectRootAgents), "alpha"))

	restoredHash, err := treehash.Dir(skillDir)
	mustNoErr(t, "hash restored skillDir", err)
	if restoredHash.SHA != wantHash.SHA || !slices.Equal(restoredHash.Nodes, wantHash.Nodes) {
		t.Fatalf("restored tree mismatch:\ngot:  %+v\nwant: %+v", restoredHash, wantHash)
	}
	if _, err := os.Lstat(filepath.Join(skillDir, "empty-dir")); err != nil {
		t.Fatalf("empty-dir missing after restore: %v", err)
	}

	if afterFDs := openFDs(t); !slices.Equal(beforeFDs, afterFDs) {
		t.Fatalf("FDs leaked:\nbefore: %v\nafter:  %v", beforeFDs, afterFDs)
	}
}

func TestProjectOpsRejectsLinksGitSpecialBitsForeignAndGuards(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	mustWriteFileMode(t, filepath.Join(forbidden, "secret", "SKILL.md"), "secret", 0o644)
	beforeForbidden, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden before", err)

	prefixes := filepath.SplitList(testsupport.NewHome(t).Env().ForbidPrefix)
	if len(prefixes) == 0 {
		t.Fatal("missing forbidden prefix")
	}
	guard := paths.NewGuard(forbidden + string(os.PathListSeparator) + prefixes[0])
	ops := fsadapter.NewProjectOps(guard)
	beforeFDs := openFDs(t)

	t.Run("ObserveSkill treats non-dir and no-SKILL.md as foreign and rejects .git, symlinks, special bits", func(t *testing.T) {
		proj := filepath.Join(base, "obs-proj")
		mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "file-occupant"), "not-a-dir", 0o644)
		mustMkdirAll(t, filepath.Join(proj, ".agents", "skills", "empty-dir"))
		mustSymlink(t, filepath.Join(forbidden, "secret"), filepath.Join(proj, ".agents", "skills", "sym-entry"))
		mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "with-git", "SKILL.md"), "git\n", 0o644)
		mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "with-git", ".git", "HEAD"), "ref: refs/heads/main\n", 0o644)
		mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "with-sticky", "SKILL.md"), "sticky\n", 0o644)
		mustNoErr(t, "chmod sticky", os.Chmod(filepath.Join(proj, ".agents", "skills", "with-sticky", "SKILL.md"), 0o644|os.ModeSticky))

		for _, name := range []string{"file-occupant", "empty-dir", "sym-entry"} {
			exists, foreign, _, err := ops.ObserveSkill(proj, string(fsadapter.ProjectRootAgents), name)
			mustNoErr(t, "ObserveSkill("+name+")", err)
			if !exists || !foreign {
				t.Errorf("ObserveSkill(%s) exists=%v foreign=%v, want true/true", name, exists, foreign)
			}
		}

		if _, _, _, err := ops.ObserveSkill(proj, string(fsadapter.ProjectRootAgents), "with-git"); !errors.Is(err, fsadapter.ErrProjectTreeGit) {
			t.Errorf("ObserveSkill(with-git) err = %v, want ErrProjectTreeGit", err)
		}
		if _, _, _, err := ops.ObserveSkill(proj, string(fsadapter.ProjectRootAgents), "with-sticky"); !errors.Is(err, fsadapter.ErrProjectTreeUnsupported) {
			t.Errorf("ObserveSkill(with-sticky) err = %v, want ErrProjectTreeUnsupported", err)
		}
	})

	t.Run("refuses symlinked project root or skill dir during remove and restore", func(t *testing.T) {
		proj := filepath.Join(base, "sym-proj")
		mustMkdirAll(t, filepath.Join(proj, ".agents"))
		mustSymlink(t, filepath.Join(forbidden, "secret"), filepath.Join(proj, ".agents", "skills"))

		if _, err := ops.SameDevice(proj, string(fsadapter.ProjectRootAgents), base); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
			t.Errorf("SameDevice(linked root) err = %v, want ErrProjectTreeLinked", err)
		}
		if err := ops.RemoveSkillToTrash(proj, string(fsadapter.ProjectRootAgents), "secret", filepath.Join(base, "trash1")); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
			t.Errorf("RemoveSkillToTrash(linked root) err = %v, want ErrProjectTreeLinked", err)
		}

		staging := filepath.Join(base, "staging-ok")
		mustWriteFileMode(t, filepath.Join(staging, "SKILL.md"), "ok\n", 0o644)
		if err := ops.RestoreSkillFromStaging(staging, proj, string(fsadapter.ProjectRootAgents), "restored"); !errors.Is(err, fsadapter.ErrProjectTreeLinked) {
			t.Errorf("RestoreSkillFromStaging(linked root) err = %v, want ErrProjectTreeLinked", err)
		}
	})

	t.Run("refuses existing destination without overwriting on CopySkill, RemoveSkillToTrash, and RestoreSkillFromStaging", func(t *testing.T) {
		proj := filepath.Join(base, "noreplace-proj")
		skillDir := filepath.Join(proj, ".agents", "skills", "beta")
		mustWriteFileMode(t, filepath.Join(skillDir, "SKILL.md"), "beta\n", 0o644)

		existing := filepath.Join(base, "already-there")
		mustWriteFileMode(t, filepath.Join(existing, "keep.txt"), "keep\n", 0o644)

		if _, err := ops.CopySkill(proj, string(fsadapter.ProjectRootAgents), "beta", existing); !errors.Is(err, iofs.ErrExist) {
			t.Errorf("CopySkill(existing) err = %v, want ErrExist", err)
		}
		if err := ops.RemoveSkillToTrash(proj, string(fsadapter.ProjectRootAgents), "beta", existing); !errors.Is(err, iofs.ErrExist) {
			t.Errorf("RemoveSkillToTrash(existing) err = %v, want ErrExist", err)
		}
		if _, err := os.Lstat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			t.Fatalf("beta damaged after failed RemoveSkillToTrash: %v", err)
		}

		staging := filepath.Join(base, "staging-beta")
		mustWriteFileMode(t, filepath.Join(staging, "SKILL.md"), "new\n", 0o644)
		if err := ops.RestoreSkillFromStaging(staging, proj, string(fsadapter.ProjectRootAgents), "beta"); !errors.Is(err, iofs.ErrExist) {
			t.Errorf("RestoreSkillFromStaging(existing) err = %v, want ErrExist", err)
		}
		if err := ops.RestoreSkillFromStaging(filepath.Join(base, "staging-with-fifo"), proj, string(fsadapter.ProjectRootAgents), "gamma"); err == nil {
			t.Error("expected missing staging error")
		}
		// Staging containing .git, symlink, or FIFO must be rejected before rename.
		fifoStaging := filepath.Join(base, "fifo-staging")
		mustWriteFileMode(t, filepath.Join(fifoStaging, "SKILL.md"), "ok\n", 0o644)
		mustNoErr(t, "mkfifo", syscall.Mkfifo(filepath.Join(fifoStaging, "pipe"), 0o644))
		if err := ops.RestoreSkillFromStaging(fifoStaging, proj, string(fsadapter.ProjectRootAgents), "fifo"); !errors.Is(err, fsadapter.ErrProjectTreeUnsupported) {
			t.Errorf("RestoreSkillFromStaging(fifo) err = %v, want ErrProjectTreeUnsupported", err)
		}
	})

	t.Run("maps EXDEV to ports.ErrCrossDevice", func(t *testing.T) {
		proj := filepath.Join(base, "exdev-proj")
		mustWriteFileMode(t, filepath.Join(proj, ".agents", "skills", "exdev", "SKILL.md"), "ok\n", 0o644)
		exdevOps := fsadapter.NewProjectOpsForTest(guard, func(_ int, _ string, _ int, _ string, _ uint) error {
			return unix.EXDEV
		})
		trash := filepath.Join(base, "trash-exdev")
		if err := exdevOps.RemoveSkillToTrash(proj, string(fsadapter.ProjectRootAgents), "exdev", trash); !errors.Is(err, ports.ErrCrossDevice) {
			t.Errorf("RemoveSkillToTrash(EXDEV) err = %v, want ErrCrossDevice", err)
		}
		staging := filepath.Join(base, "staging-exdev")
		mustWriteFileMode(t, filepath.Join(staging, "SKILL.md"), "ok\n", 0o644)
		if err := exdevOps.RestoreSkillFromStaging(staging, proj, string(fsadapter.ProjectRootAgents), "fresh"); !errors.Is(err, ports.ErrCrossDevice) {
			t.Errorf("RestoreSkillFromStaging(EXDEV) err = %v, want ErrCrossDevice", err)
		}
	})

	afterForbidden, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash forbidden after", err)
	if beforeForbidden.SHA != afterForbidden.SHA {
		t.Fatalf("forbidden changed: %s -> %s", beforeForbidden.SHA, afterForbidden.SHA)
	}
	if afterFDs := openFDs(t); !slices.Equal(beforeFDs, afterFDs) {
		t.Fatalf("FDs leaked:\nbefore: %v\nafter:  %v", beforeFDs, afterFDs)
	}
}
