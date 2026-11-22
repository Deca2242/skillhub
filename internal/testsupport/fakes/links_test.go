package fakes_test

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

var _ ports.LinkOps = (*fakes.LinkOps)(nil)

// The shared contract of LinkOps, run on this double and on the real adapter,
// lives in internal/adapters/fs/links_contract_test.go. These tests cover what
// only the double offers.

func newFakeLinks(t *testing.T) (*fakes.LinkOps, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "store", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "parked"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fakes.NewLinkOps(root, paths.Guard{}), root
}

func TestFakeLinkOpsRecordsCalls(t *testing.T) {
	l, root := newFakeLinks(t)
	link := filepath.Join(root, "alpha")
	if err := l.Symlink("store/alpha", link); err != nil {
		t.Fatal(err)
	}
	if err := l.RemoveLink(link, "store/alpha"); err != nil {
		t.Fatal(err)
	}
	want := []fakes.Call{
		{Op: "Symlink", Args: []string{"store/alpha", link}},
		{Op: "RemoveLink", Args: []string{link, "store/alpha"}},
	}
	got := l.Calls()
	if !slices.EqualFunc(got, want, func(a, b fakes.Call) bool { return a.Op == b.Op && slices.Equal(a.Args, b.Args) }) {
		t.Errorf("Calls = %v, want %v", got, want)
	}
}

func TestFakeLinkOpsInjectedFailureChangesNothing(t *testing.T) {
	l, root := newFakeLinks(t)
	l.Fail = failOn("RenameNoReplace")
	src, dst := filepath.Join(root, "store", "alpha"), filepath.Join(root, "parked", "alpha")
	if err := l.RenameNoReplace(src, dst); !errors.Is(err, errPlanted) {
		t.Fatalf("RenameNoReplace = %v, want the planted failure", err)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Errorf("source moved: %v", err)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, iofs.ErrNotExist) {
		t.Errorf("destination created: %v", err)
	}
	if err := l.Symlink("store/alpha", filepath.Join(root, "alpha")); err != nil {
		t.Errorf("another call failed: %v", err)
	}
}

func TestFakeLinkOpsSimulatesAnotherDevice(t *testing.T) {
	l, root := newFakeLinks(t)
	src, dst := filepath.Join(root, "store", "alpha"), filepath.Join(root, "parked", "alpha")
	l.CrossDevice = func(a, b string) bool { return a == src || b == src }

	if err := l.RenameNoReplace(src, dst); !errors.Is(err, ports.ErrCrossDevice) {
		t.Fatalf("RenameNoReplace = %v, want ErrCrossDevice", err)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Errorf("source moved: %v", err)
	}
	if same, err := l.SameDevice(src, filepath.Join(root, "parked")); err != nil || same {
		t.Errorf("SameDevice = %v, %v; want false", same, err)
	}
	if same, err := l.SameDevice(filepath.Join(root, "parked"), root); err != nil || !same {
		t.Errorf("SameDevice of unmarked paths = %v, %v; want true", same, err)
	}
}

func TestFakeLinkOpsStaysInsideItsRoot(t *testing.T) {
	l, root := newFakeLinks(t)
	outside := t.TempDir()
	if err := l.Symlink("x", filepath.Join(outside, "link")); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("Symlink outside the root = %v, want ErrForbidden", err)
	}
	if err := l.RenameNoReplace(filepath.Join(root, "store", "alpha"), filepath.Join(outside, "alpha")); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("RenameNoReplace outside the root = %v, want ErrForbidden", err)
	}
	if names, err := os.ReadDir(outside); err != nil || len(names) != 0 {
		t.Errorf("outside holds %v, %v; want nothing", names, err)
	}
}
