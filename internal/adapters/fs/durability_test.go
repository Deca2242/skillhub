package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
)

// syncLog records every fsync a FileOps makes, by file name, and fails the
// one whose name ends with failSuffix.
type syncLog struct {
	names      []string
	failSuffix string
}

var errSyncInjected = errors.New("injected fsync failure")

func (l *syncLog) sync(f *os.File) error {
	l.names = append(l.names, f.Name())
	if l.failSuffix != "" && strings.HasSuffix(f.Name(), l.failSuffix) {
		return errSyncInjected
	}
	return f.Sync()
}

// synced reports whether some fsync'd name is the copy of rel: under a
// temporary sibling while the copy fills, so only the suffix is stable.
func (l *syncLog) synced(rel string) bool {
	return slices.ContainsFunc(l.names, func(n string) bool {
		return strings.HasSuffix(n, string(filepath.Separator)+rel)
	})
}

func TestWriteFileAtomicSyncsTheFileThenItsDirectory(t *testing.T) {
	root := t.TempDir()
	log := &syncLog{}
	ops := fsadapter.NewFileOpsWithSync(paths.NewGuard(filepath.Join(root, "forbidden")), log.sync)
	target := filepath.Join(root, "state", "record.json")

	mustNoErr(t, "write", ops.WriteFileAtomic(target, []byte("{}\n"), 0o600))

	if len(log.names) != 2 {
		t.Fatalf("fsync calls = %q, want the temporary file and the directory", log.names)
	}
	if dir := filepath.Dir(log.names[0]); dir != filepath.Dir(target) || !strings.Contains(filepath.Base(log.names[0]), ".tmp-") {
		t.Errorf("first fsync = %s, want the temporary file next to %s", log.names[0], target)
	}
	if log.names[1] != filepath.Dir(target) {
		t.Errorf("second fsync = %s, want the directory %s, after the rename", log.names[1], filepath.Dir(target))
	}
}

func TestWriteFileAtomicReportsADirectorySyncFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "state", "record.json")
	log := &syncLog{failSuffix: string(filepath.Separator) + "state"}
	ops := fsadapter.NewFileOpsWithSync(paths.NewGuard(filepath.Join(root, "forbidden")), log.sync)

	err := ops.WriteFileAtomic(target, []byte("{}\n"), 0o600)
	if !errors.Is(err, errSyncInjected) {
		t.Fatalf("error = %v, want the directory fsync failure", err)
	}
}

func TestCopyTreeSyncsEveryFileEveryDirectoryAndTheParent(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	for path, content := range map[string]string{"SKILL.md": "a\n", "scripts/run.sh": "b\n", "scripts/deep/x.txt": "c\n"} {
		full := filepath.Join(src, path)
		mustNoErr(t, "mkdir", os.MkdirAll(filepath.Dir(full), 0o755))
		mustNoErr(t, "write", os.WriteFile(full, []byte(content), 0o644))
	}
	mustNoErr(t, "symlink", os.Symlink("SKILL.md", filepath.Join(src, "README.md")))
	log := &syncLog{}
	ops := fsadapter.NewFileOpsWithSync(paths.NewGuard(filepath.Join(root, "forbidden")), log.sync)
	dst := filepath.Join(root, "snapshots", "copy")

	mustNoErr(t, "copy", ops.CopyTree(src, dst))

	for _, rel := range []string{"SKILL.md", "scripts/run.sh", "scripts/deep/x.txt", "scripts", "scripts/deep"} {
		if !log.synced(filepath.FromSlash(rel)) {
			t.Errorf("%s was not synced; fsync calls %q", rel, log.names)
		}
	}
	if !slices.ContainsFunc(log.names, func(n string) bool { return strings.Contains(filepath.Base(n), ".copy-") }) {
		t.Errorf("the copy's root was not synced; fsync calls %q", log.names)
	}
	if last := log.names[len(log.names)-1]; last != filepath.Dir(dst) {
		t.Errorf("last fsync = %s, want the parent %s after the rename", last, filepath.Dir(dst))
	}
}

func TestCopyTreeFailsAndLeavesNothingWhenASyncFails(t *testing.T) {
	for _, failing := range []string{"x.txt", "deep", "snapshots"} {
		t.Run(failing, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "src")
			mustNoErr(t, "mkdir", os.MkdirAll(filepath.Join(src, "deep"), 0o755))
			mustNoErr(t, "write", os.WriteFile(filepath.Join(src, "deep", "x.txt"), []byte("c\n"), 0o644))
			log := &syncLog{failSuffix: string(filepath.Separator) + failing}
			ops := fsadapter.NewFileOpsWithSync(paths.NewGuard(filepath.Join(root, "forbidden")), log.sync)
			dst := filepath.Join(root, "snapshots", "copy")

			if err := ops.CopyTree(src, dst); !errors.Is(err, errSyncInjected) {
				t.Fatalf("error = %v, want the fsync failure", err)
			}
			if failing == "snapshots" {
				// The parent's sync comes after the rename: the copy is complete
				// but its durability is not promised, and the caller knows.
				return
			}
			if names := dirNames(t, filepath.Dir(dst)); len(names) != 0 {
				t.Errorf("a failed copy left %q", names)
			}
		})
	}
}
