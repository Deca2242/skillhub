package fs_test

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// fileOpsImpl is one implementation under the FileOps contract.
type fileOpsImpl struct {
	name string
	// setup returns the FileOps, a writable root and a forbidden directory
	// under which every call must fail, plus a way to plant a symlink.
	setup func(t *testing.T) (ops ports.FileOps, root, forbidden string, symlink func(path, target string))
}

var fileOpsImpls = []fileOpsImpl{
	{
		name: "fake",
		setup: func(t *testing.T) (ports.FileOps, string, string, func(string, string)) {
			base := t.TempDir()
			forbidden := filepath.Join(base, "forbidden")
			f := fakes.NewFileOps(paths.NewGuard(forbidden))
			return f, filepath.Join(base, "root"), forbidden, f.AddSymlink
		},
	},
	{
		name: "real",
		setup: func(t *testing.T) (ports.FileOps, string, string, func(string, string)) {
			base := t.TempDir()
			forbidden := filepath.Join(base, "forbidden")
			if err := os.MkdirAll(forbidden, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(forbidden, "keep.txt"), []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			}
			link := func(path, target string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			return fsadapter.NewFileOps(paths.NewGuard(forbidden)), filepath.Join(base, "root"), forbidden, link
		},
	},
}

func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func readString(t *testing.T, ops ports.FileOps, path string) string {
	t.Helper()
	b, err := ops.ReadFile(path)
	mustNoErr(t, "ReadFile "+path, err)
	return string(b)
}

func TestFileOpsContract(t *testing.T) {
	for _, impl := range fileOpsImpls {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("write atomically creates parents and replaces content", func(t *testing.T) {
				ops, root, _, _ := impl.setup(t)
				p := filepath.Join(root, "a", "b", "file.txt")
				mustNoErr(t, "first write", ops.WriteFileAtomic(p, []byte("one"), 0o600))
				mustNoErr(t, "second write", ops.WriteFileAtomic(p, []byte("two"), 0o755))
				if got := readString(t, ops, p); got != "two" {
					t.Errorf("content = %q, want %q", got, "two")
				}
				mode, err := ops.Mode(p)
				mustNoErr(t, "Mode", err)
				if mode.Perm() != 0o755 || !mode.IsRegular() {
					t.Errorf("mode = %v, want regular 0755", mode)
				}
			})
			t.Run("rename moves a whole subtree", func(t *testing.T) {
				ops, root, _, _ := impl.setup(t)
				old := filepath.Join(root, "old")
				mustNoErr(t, "write", ops.WriteFileAtomic(filepath.Join(old, "sub", "f"), []byte("x"), 0o644))
				mustNoErr(t, "Rename", ops.Rename(old, filepath.Join(root, "new")))
				if got := readString(t, ops, filepath.Join(root, "new", "sub", "f")); got != "x" {
					t.Errorf("moved content = %q", got)
				}
				if ok, err := ops.Exists(old); err != nil || ok {
					t.Errorf("Exists(old) = %v, %v; want false", ok, err)
				}
			})
			t.Run("copy tree keeps content, modes and links", func(t *testing.T) {
				ops, root, _, symlink := impl.setup(t)
				src := filepath.Join(root, "src")
				mustNoErr(t, "write", ops.WriteFileAtomic(filepath.Join(src, "SKILL.md"), []byte("skill"), 0o644))
				mustNoErr(t, "write", ops.WriteFileAtomic(filepath.Join(src, "scripts", "run.sh"), []byte("#!/bin/sh"), 0o755))
				symlink(filepath.Join(src, "link"), "SKILL.md")
				dst := filepath.Join(root, "copies", "dst")
				mustNoErr(t, "CopyTree", ops.CopyTree(src, dst))
				if got := readString(t, ops, filepath.Join(dst, "scripts", "run.sh")); got != "#!/bin/sh" {
					t.Errorf("copied script = %q", got)
				}
				mode, err := ops.Mode(filepath.Join(dst, "scripts", "run.sh"))
				mustNoErr(t, "Mode", err)
				if mode.Perm() != 0o755 {
					t.Errorf("copied script mode = %v, want 0755", mode.Perm())
				}
				lmode, err := ops.Mode(filepath.Join(dst, "link"))
				mustNoErr(t, "Mode link", err)
				if lmode&iofs.ModeSymlink == 0 {
					t.Errorf("copied link mode = %v, want a symlink", lmode)
				}
				if got := readString(t, ops, filepath.Join(src, "SKILL.md")); got != "skill" {
					t.Errorf("source changed: %q", got)
				}
				if err := ops.CopyTree(src, dst); !errors.Is(err, iofs.ErrExist) {
					t.Errorf("CopyTree onto an existing dst = %v, want ErrExist", err)
				}
				if err := ops.CopyTree(filepath.Join(root, "missing"), filepath.Join(root, "x")); !errors.Is(err, iofs.ErrNotExist) {
					t.Errorf("CopyTree of a missing src = %v, want ErrNotExist", err)
				}
			})
			t.Run("remove tree removes everything and tolerates a missing path", func(t *testing.T) {
				ops, root, _, _ := impl.setup(t)
				dir := filepath.Join(root, "gone")
				mustNoErr(t, "write", ops.WriteFileAtomic(filepath.Join(dir, "a", "b"), []byte("x"), 0o644))
				mustNoErr(t, "RemoveTree", ops.RemoveTree(dir))
				if ok, err := ops.Exists(dir); err != nil || ok {
					t.Errorf("Exists after RemoveTree = %v, %v", ok, err)
				}
				mustNoErr(t, "RemoveTree missing", ops.RemoveTree(dir))
			})
			t.Run("mode, chmod and exists do not follow a final link", func(t *testing.T) {
				ops, root, _, symlink := impl.setup(t)
				mustNoErr(t, "MkdirAll", ops.MkdirAll(filepath.Join(root, "d"), 0o755))
				f := filepath.Join(root, "d", "f")
				mustNoErr(t, "write", ops.WriteFileAtomic(f, []byte("x"), 0o644))
				mustNoErr(t, "Chmod", ops.Chmod(f, 0o700))
				mode, err := ops.Mode(f)
				mustNoErr(t, "Mode", err)
				if mode.Perm() != 0o700 {
					t.Errorf("mode after Chmod = %v", mode.Perm())
				}
				broken := filepath.Join(root, "d", "broken")
				symlink(broken, "nowhere")
				if ok, err := ops.Exists(broken); err != nil || !ok {
					t.Errorf("Exists(broken link) = %v, %v; want true", ok, err)
				}
				if ok, err := ops.Exists(filepath.Join(root, "d", "none")); err != nil || ok {
					t.Errorf("Exists(missing) = %v, %v; want false", ok, err)
				}
			})
			t.Run("every operation refuses a forbidden path", func(t *testing.T) {
				ops, root, forbidden, _ := impl.setup(t)
				inside := filepath.Join(forbidden, "keep.txt")
				outside := filepath.Join(root, "ok")
				mustNoErr(t, "write outside", ops.WriteFileAtomic(filepath.Join(outside, "f"), []byte("x"), 0o644))
				calls := map[string]func() error{
					"ReadFile":         func() error { _, err := ops.ReadFile(inside); return err },
					"WriteFileAtomic":  func() error { return ops.WriteFileAtomic(inside, []byte("x"), 0o644) },
					"Rename from":      func() error { return ops.Rename(inside, filepath.Join(root, "stolen")) },
					"Rename to":        func() error { return ops.Rename(outside, filepath.Join(forbidden, "in")) },
					"CopyTree from":    func() error { return ops.CopyTree(forbidden, filepath.Join(root, "c")) },
					"CopyTree to":      func() error { return ops.CopyTree(outside, filepath.Join(forbidden, "c")) },
					"RemoveTree":       func() error { return ops.RemoveTree(forbidden) },
					"MkdirAll":         func() error { return ops.MkdirAll(filepath.Join(forbidden, "new"), 0o755) },
					"Mode":             func() error { _, err := ops.Mode(inside); return err },
					"Chmod":            func() error { return ops.Chmod(inside, 0o600) },
					"Exists":           func() error { _, err := ops.Exists(inside); return err },
					"relative path":    func() error { _, err := ops.ReadFile("relative/file"); return err },
					"forbidden itself": func() error { _, err := ops.Exists(forbidden); return err },
				}
				for name, call := range calls {
					err := call()
					want := paths.ErrForbidden
					if name == "relative path" {
						want = paths.ErrInvalidPath
					}
					if !errors.Is(err, want) {
						t.Errorf("%s: err = %v, want %v", name, err, want)
					}
				}
				if got := readString(t, ops, filepath.Join(outside, "f")); got != "x" {
					t.Errorf("outside file changed: %q", got)
				}
			})
		})
	}
}

// TestRealFileOpsLeavesForbiddenDiskUntouched checks on disk what the
// contract checks through the port: a refused call changed nothing.
func TestRealFileOpsLeavesForbiddenDiskUntouched(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	if err := os.MkdirAll(forbidden, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(forbidden, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A link from an allowed place into the forbidden one is refused too.
	link := filepath.Join(base, "allowed", "link")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(forbidden, link); err != nil {
		t.Fatal(err)
	}
	before, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash before", err)

	ops := fsadapter.NewFileOps(paths.NewGuard(forbidden))
	if err := ops.WriteFileAtomic(filepath.Join(link, "keep.txt"), []byte("overwritten"), 0o644); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("write through a link into the forbidden dir = %v, want ErrForbidden", err)
	}
	if err := ops.RemoveTree(link + "/"); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("RemoveTree through a link = %v, want ErrForbidden", err)
	}
	if err := ops.RemoveTree(forbidden); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("RemoveTree = %v, want ErrForbidden", err)
	}
	after, err := treehash.Dir(forbidden)
	mustNoErr(t, "hash after", err)
	if before.SHA != after.SHA {
		t.Errorf("forbidden dir changed: %s -> %s", before.SHA, after.SHA)
	}
}

// TestRealFileOpsRefusesTheRealHome uses the prefix testsupport.Main set to
// the real home: the adapter refuses it without touching it.
func TestRealFileOpsRefusesTheRealHome(t *testing.T) {
	prefixes := filepath.SplitList(testsupport.NewHome(t).Env().ForbidPrefix)
	if len(prefixes) == 0 {
		t.Fatal("testsupport.Main set no forbidden prefix")
	}
	ops := fsadapter.NewFileOps(paths.NewGuard(strings.Join(prefixes, string(os.PathListSeparator))))
	realHome := prefixes[0]
	if _, err := ops.ReadFile(filepath.Join(realHome, ".agents", ".skill-lock.json")); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("ReadFile under the real home = %v, want ErrForbidden", err)
	}
	if err := ops.WriteFileAtomic(filepath.Join(realHome, ".skillhub-test"), nil, 0o644); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("WriteFileAtomic under the real home = %v, want ErrForbidden", err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	mustNoErr(t, "ReadDir", err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRealWriteFileAtomicLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	ops := fsadapter.NewFileOps(paths.Guard{})
	target := filepath.Join(dir, "file.json")
	mustNoErr(t, "write", ops.WriteFileAtomic(target, []byte("{}"), 0o644))
	if got := dirNames(t, dir); !slices.Equal(got, []string{"file.json"}) {
		t.Errorf("dir after a write = %v, want only file.json", got)
	}

	// Writing over a directory fails, keeps the directory and leaves no
	// temporary file behind.
	sub := filepath.Join(dir, "sub")
	mustNoErr(t, "mkdir", os.Mkdir(sub, 0o755))
	mustNoErr(t, "seed", os.WriteFile(filepath.Join(sub, "inner"), []byte("inner"), 0o644))
	if err := ops.WriteFileAtomic(sub, []byte("x"), 0o644); err == nil {
		t.Error("WriteFileAtomic over a non-empty directory succeeded")
	}
	if got := dirNames(t, dir); !slices.Equal(got, []string{"file.json", "sub"}) {
		t.Errorf("dir after a failed write = %v, want file.json and sub only", got)
	}
	if b, err := os.ReadFile(filepath.Join(sub, "inner")); err != nil || string(b) != "inner" {
		t.Errorf("directory content after a failed write = %q, %v", b, err)
	}
}

func TestRealCopyTreeFailureLeavesNoDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustNoErr(t, "mkdir", os.Mkdir(src, 0o755))
	mustNoErr(t, "seed", os.WriteFile(filepath.Join(src, "a.md"), []byte("a"), 0o644))
	mustNoErr(t, "mkfifo", syscall.Mkfifo(filepath.Join(src, "pipe"), 0o644))

	ops := fsadapter.NewFileOps(paths.Guard{})
	if err := ops.CopyTree(src, filepath.Join(dir, "dst")); err == nil {
		t.Fatal("CopyTree of a folder holding a named pipe succeeded")
	}
	if got := dirNames(t, dir); !slices.Equal(got, []string{"src"}) {
		t.Errorf("dir after a failed copy = %v, want only src", got)
	}
}

// TestFailingFileOpsOverTheRealAdapter injects a failure in front of the real
// adapter: the failing call never reaches the disk, the others do.
func TestFailingFileOpsOverTheRealAdapter(t *testing.T) {
	dir := t.TempDir()
	ops := &fakes.FailingFileOps{Inner: fsadapter.NewFileOps(paths.Guard{})}
	injected := errors.New("injected")
	ops.Fail = func(c fakes.Call) error {
		if c.Op == "WriteFileAtomic" && strings.HasSuffix(c.Args[0], "fail.txt") {
			return injected
		}
		return nil
	}
	if err := ops.WriteFileAtomic(filepath.Join(dir, "fail.txt"), []byte("x"), 0o644); !errors.Is(err, injected) {
		t.Errorf("failing write = %v, want the injected error", err)
	}
	mustNoErr(t, "passing write", ops.WriteFileAtomic(filepath.Join(dir, "ok.txt"), []byte("ok"), 0o644))
	if got := dirNames(t, dir); !slices.Equal(got, []string{"ok.txt"}) {
		t.Errorf("dir = %v, want only ok.txt", got)
	}
	if calls := ops.Calls(); len(calls) != 2 || calls[0].Op != "WriteFileAtomic" {
		t.Errorf("recorded calls = %+v", calls)
	}
}
