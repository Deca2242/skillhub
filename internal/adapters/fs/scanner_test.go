package fs_test

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
)

func TestScannerListsEveryEntryKind(t *testing.T) {
	h := testsupport.NewHome(t)
	h.WriteFile(".agents/skills/plain/SKILL.md", "---\nname: plain\n---\n")
	h.Mkdir(".agents/skills/no-skill-md")
	h.WriteFile(".agents/skills/cloned/SKILL.md", "x")
	h.Mkdir(".agents/skills/cloned/.git")
	h.WriteFile(".agents/skills/gitfile/SKILL.md", "x")
	h.WriteFile(".agents/skills/gitfile/.git", "gitdir: elsewhere")
	h.Symlink(".agents/skills/alias", "plain")
	h.Symlink(".agents/skills/chain", "alias")
	h.Symlink(".agents/skills/broken", "OFF/gone")
	h.WriteFile(".agents/skills/README.md", "readme")
	dir := h.Path(".agents/skills")
	before, err := treehash.Dir(dir)
	mustNoErr(t, "hash before", err)

	got, err := fsadapter.NewScanner(h.Paths().Guard()).Scan(dir)
	mustNoErr(t, "Scan", err)

	realDir, err := filepath.EvalSymlinks(dir)
	mustNoErr(t, "EvalSymlinks", err)
	plain := filepath.Join(realDir, "plain")
	want := []ports.DirEntry{
		{Name: "README.md", Path: filepath.Join(dir, "README.md"), Kind: ports.EntryFile, ResolvedPath: filepath.Join(realDir, "README.md")},
		{Name: "alias", Path: filepath.Join(dir, "alias"), Kind: ports.EntrySymlink, ResolvedPath: plain, HasSkillMD: true},
		{Name: "broken", Path: filepath.Join(dir, "broken"), Kind: ports.EntryBrokenLink},
		{Name: "chain", Path: filepath.Join(dir, "chain"), Kind: ports.EntrySymlink, ResolvedPath: plain, HasSkillMD: true},
		{Name: "cloned", Path: filepath.Join(dir, "cloned"), Kind: ports.EntryDir, ResolvedPath: filepath.Join(realDir, "cloned"), HasSkillMD: true, HasGit: true},
		{Name: "gitfile", Path: filepath.Join(dir, "gitfile"), Kind: ports.EntryDir, ResolvedPath: filepath.Join(realDir, "gitfile"), HasSkillMD: true},
		{Name: "no-skill-md", Path: filepath.Join(dir, "no-skill-md"), Kind: ports.EntryDir, ResolvedPath: filepath.Join(realDir, "no-skill-md")},
		{Name: "plain", Path: filepath.Join(dir, "plain"), Kind: ports.EntryDir, ResolvedPath: plain, HasSkillMD: true},
	}
	if len(got) != len(want) {
		t.Fatalf("Scan returned %d entries, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}

	after, err := treehash.Dir(dir)
	mustNoErr(t, "hash after", err)
	if before.SHA != after.SHA {
		t.Error("Scan changed the skills directory")
	}
}

func TestScannerMissingDirectory(t *testing.T) {
	h := testsupport.NewHome(t)
	_, err := fsadapter.NewScanner(h.Paths().Guard()).Scan(h.Path(".kilo/skills"))
	if !errors.Is(err, iofs.ErrNotExist) {
		t.Errorf("Scan of a missing dir = %v, want ErrNotExist", err)
	}
}

func TestScannerRefusesForbiddenPaths(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	mustNoErr(t, "mkdir", os.MkdirAll(filepath.Join(forbidden, "skill"), 0o755))
	mustNoErr(t, "write", os.WriteFile(filepath.Join(forbidden, "skill", "SKILL.md"), []byte("x"), 0o644))
	allowed := filepath.Join(base, "skills")
	mustNoErr(t, "mkdir", os.MkdirAll(allowed, 0o755))
	mustNoErr(t, "link", os.Symlink(filepath.Join(forbidden, "skill"), filepath.Join(allowed, "leak")))

	scanner := fsadapter.NewScanner(paths.NewGuard(forbidden))
	if _, err := scanner.Scan(forbidden); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("Scan of a forbidden dir = %v, want ErrForbidden", err)
	}
	if _, err := scanner.Scan(allowed); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("Scan of a dir linking into a forbidden one = %v, want ErrForbidden", err)
	}
}
