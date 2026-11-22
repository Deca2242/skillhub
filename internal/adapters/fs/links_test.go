package fs_test

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
)

// realWorld builds the contract layout for the real adapter with hooks.
func realWorld(t *testing.T, hooks fsadapter.LinkHooks) linkWorld {
	t.Helper()
	return newLinkWorld(t, func(_, forbidden string) ports.LinkOps {
		return fsadapter.NewLinkOpsWithHooks(paths.NewGuard(forbidden), hooks)
	})
}

func TestRealRenameNoReplaceRefusesAnotherDevice(t *testing.T) {
	const shm = "/dev/shm"
	if info, err := os.Stat(shm); err != nil || !info.IsDir() {
		t.Skip("no /dev/shm; TestRealRenameNoReplaceMapsInjectedEXDEV covers the mapping")
	}
	other, err := os.MkdirTemp(shm, "skillhub-test-")
	if err != nil {
		t.Skipf("cannot write to %s: %v", shm, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(other) })

	w := realWorld(t, fsadapter.LinkHooks{})
	var stA, stB syscall.Stat_t
	mustNoErr(t, "stat", syscall.Lstat(w.at("store/alpha"), &stA))
	mustNoErr(t, "stat", syscall.Lstat(other, &stB))
	if stA.Dev == stB.Dev {
		t.Skipf("%s is on the same file system as the temp dir", shm)
	}
	same, err := w.ops.SameDevice(w.at("store/alpha"), other)
	if err != nil || same {
		t.Errorf("SameDevice across devices = %v, %v; want false", same, err)
	}
	before := diskTree(t, w.base)
	err = w.ops.RenameNoReplace(w.at("store/alpha"), filepath.Join(other, "alpha"))
	if !errors.Is(err, ports.ErrCrossDevice) {
		t.Fatalf("RenameNoReplace across devices = %v, want ErrCrossDevice", err)
	}
	assertUnchanged(t, w.base, before)
	if names := dirNames(t, other); len(names) != 0 {
		t.Errorf("the other device holds %v, want nothing", names)
	}
}

func TestRealRenameNoReplaceMapsInjectedEXDEV(t *testing.T) {
	w := realWorld(t, fsadapter.LinkHooks{
		Renameat2: func(int, string, int, string, uint) error { return syscall.EXDEV },
	})
	before := diskTree(t, w.base)
	err := w.ops.RenameNoReplace(w.at("store/alpha"), w.at("parked/alpha"))
	if !errors.Is(err, ports.ErrCrossDevice) {
		t.Fatalf("RenameNoReplace = %v, want ErrCrossDevice", err)
	}
	assertUnchanged(t, w.base, before)
}

// noRenameat2 simulates a kernel or file system without RENAME_NOREPLACE and
// counts the link(2) fallbacks.
func noRenameat2(errno syscall.Errno, links *int) fsadapter.LinkHooks {
	return fsadapter.LinkHooks{
		Renameat2: func(int, string, int, string, uint) error { return errno },
		Linkat: func(oldFD int, oldName string, newFD int, newName string, flags int) error {
			*links++
			return unix.Linkat(oldFD, oldName, newFD, newName, flags)
		},
	}
}

func TestRealSymlinkFallsBackToLink(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.ENOSYS} {
		t.Run(errno.Error(), func(t *testing.T) {
			var links int
			w := realWorld(t, noRenameat2(errno, &links))
			mustNoErr(t, "Symlink", w.ops.Symlink("../store/alpha", w.at("harness/alpha")))
			got, err := os.Readlink(w.at("harness/alpha"))
			if err != nil || got != "../store/alpha" {
				t.Errorf("link content = %q, %v; want ../store/alpha", got, err)
			}
			if names := dirNames(t, w.at("harness")); !slices.Equal(names, []string{"alpha"}) {
				t.Errorf("harness holds %v, want only alpha", names)
			}

			plantLink(t, "../store/beta", w.at("harness/beta"))
			before := diskTree(t, w.base)
			if err := w.ops.Symlink("../store/alpha", w.at("harness/beta")); !errors.Is(err, iofs.ErrExist) {
				t.Fatalf("fallback Symlink over a link = %v, want fs.ErrExist", err)
			}
			assertUnchanged(t, w.base, before)
			if links != 2 {
				t.Errorf("link(2) ran %d times, want 2", links)
			}
		})
	}
}

func TestRealRenameNoReplaceFallback(t *testing.T) {
	var links int
	w := realWorld(t, noRenameat2(syscall.ENOSYS, &links))

	mustNoErr(t, "move a file", w.ops.RenameNoReplace(w.at("store/beta/SKILL.md"), w.at("parked/SKILL.md")))
	if _, err := os.Lstat(w.at("store/beta/SKILL.md")); !errors.Is(err, iofs.ErrNotExist) {
		t.Errorf("source file still there: %v", err)
	}
	if data, err := os.ReadFile(w.at("parked/SKILL.md")); err != nil || string(data) != "beta" {
		t.Errorf("moved file = %q, %v", data, err)
	}

	// A folder cannot be published with link(2); a plain rename could replace
	// an empty folder, so the move is refused and nothing changes.
	before := diskTree(t, w.base)
	if err := w.ops.RenameNoReplace(w.at("store/alpha"), w.at("parked/alpha")); err == nil {
		t.Fatal("folder move without RENAME_NOREPLACE succeeded")
	}
	assertUnchanged(t, w.base, before)
	if links != 1 {
		t.Errorf("link(2) ran %d times, want 1", links)
	}
}

// syncRecorder records the real folder behind every fsync'd descriptor.
type syncRecorder struct {
	mu   sync.Mutex
	dirs []string
}

func (r *syncRecorder) fsync(fd int) error {
	dir, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.dirs = append(r.dirs, dir)
	r.mu.Unlock()
	return syscall.Fsync(fd)
}

func (r *syncRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.dirs
	r.dirs = nil
	slices.Sort(out)
	return out
}

func TestRealLinkOpsSyncTheParentFolders(t *testing.T) {
	rec := &syncRecorder{}
	w := realWorld(t, fsadapter.LinkHooks{Fsync: rec.fsync})
	real := func(rel string) string {
		p, err := filepath.EvalSymlinks(w.at(rel))
		mustNoErr(t, "EvalSymlinks", err)
		return p
	}

	mustNoErr(t, "Symlink", w.ops.Symlink("../store/alpha", w.at("harness/alpha")))
	if got, want := rec.take(), []string{real("harness")}; !slices.Equal(got, want) {
		t.Errorf("Symlink synced %v, want %v", got, want)
	}
	mustNoErr(t, "RemoveLink", w.ops.RemoveLink(w.at("harness/alpha"), "../store/alpha"))
	if got, want := rec.take(), []string{real("harness")}; !slices.Equal(got, want) {
		t.Errorf("RemoveLink synced %v, want %v", got, want)
	}
	mustNoErr(t, "RenameNoReplace", w.ops.RenameNoReplace(w.at("store/alpha"), w.at("parked/alpha")))
	want := []string{real("parked"), real("store")}
	slices.Sort(want)
	if got := rec.take(); !slices.Equal(got, want) {
		t.Errorf("RenameNoReplace synced %v, want %v", got, want)
	}
}

// TestRealLinkOpsPinTheCheckedParent swaps a parent for a link into the
// forbidden prefix after the guard accepted the path: the adapter notices
// where the opened folder really is and changes nothing.
func TestRealLinkOpsPinTheCheckedParent(t *testing.T) {
	ops := map[string]func(w linkWorld) error{
		"Symlink": func(w linkWorld) error { return w.ops.Symlink("keep.txt", w.at("harness/new")) },
		"RemoveLink": func(w linkWorld) error {
			return w.ops.RemoveLink(w.at("harness/flink"), "keep.txt")
		},
		"RenameNoReplace": func(w linkWorld) error {
			return w.ops.RenameNoReplace(w.at("store/alpha"), w.at("harness/alpha"))
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			var w linkWorld
			swapped := false
			w = realWorld(t, fsadapter.LinkHooks{Opened: func(dir string) {
				if swapped || dir != w.at("harness") {
					return
				}
				swapped = true
				mustNoErr(t, "move harness away", os.Rename(dir, w.at("harness.old")))
				mustNoErr(t, "plant the swap", os.Symlink(w.forbidden, dir))
			}})
			before := diskTree(t, w.forbidden)
			err := op(w)
			if !swapped {
				t.Fatal("the hook never ran")
			}
			if !errors.Is(err, paths.ErrForbidden) {
				t.Fatalf("%s through a swapped parent = %v, want ErrForbidden", name, err)
			}
			assertUnchanged(t, w.forbidden, before)
		})
	}
}

func TestRealLinkOpsRefusesTheRealHome(t *testing.T) {
	prefixes := filepath.SplitList(testsupport.NewHome(t).Env().ForbidPrefix)
	if len(prefixes) == 0 {
		t.Fatal("testsupport.Main set no forbidden prefix")
	}
	ops := fsadapter.NewLinkOps(paths.NewGuard(strings.Join(prefixes, string(os.PathListSeparator))))
	realHome := prefixes[0]
	if err := ops.Symlink("x", filepath.Join(realHome, ".skillhub-test-link")); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("Symlink under the real home = %v, want ErrForbidden", err)
	}
	if err := ops.RemoveLink(filepath.Join(realHome, ".skillhub-test-link"), "x"); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("RemoveLink under the real home = %v, want ErrForbidden", err)
	}
	tmp := t.TempDir()
	if err := ops.RenameNoReplace(tmp, filepath.Join(realHome, ".skillhub-test-dir")); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("RenameNoReplace into the real home = %v, want ErrForbidden", err)
	}
}
