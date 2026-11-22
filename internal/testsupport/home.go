package testsupport

import (
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/shared/paths"
)

// Home is a fake home directory for one test, inside t.TempDir(). Every
// helper takes a slash-separated path relative to the home and fails the test
// when that path is empty, absolute, climbs out of the home or resolves under
// a forbidden prefix.
type Home struct {
	tb    testing.TB
	root  string
	paths paths.Paths
}

// NewHome creates an empty fake home. Its Paths carry the forbidden prefixes
// that Main set, so they refuse the real home like production Paths would.
func NewHome(tb testing.TB) *Home {
	tb.Helper()
	root := filepath.Join(tb.TempDir(), "home")
	if err := os.Mkdir(root, 0o755); err != nil {
		tb.Fatalf("create fake home: %v", err)
	}
	h := &Home{tb: tb, root: root}
	p, err := paths.New(h.Env())
	if err != nil {
		tb.Fatalf("build Paths for the fake home: %v", err)
	}
	h.paths = p
	return h
}

// Root is the absolute path of the fake home.
func (h *Home) Root() string { return h.root }

// Env is the environment the fake home's Paths are built from: no XDG
// variable, and the process forbidden prefixes. Tests that need XDG roots
// change the returned value and call paths.New themselves.
func (h *Home) Env() paths.Env {
	return paths.Env{Home: h.root, ForbidPrefix: os.Getenv(paths.ForbidPrefixEnv)}
}

// Paths resolves skillhub locations inside the fake home.
func (h *Home) Paths() paths.Paths { return h.paths }

// Path returns the absolute path of rel inside the fake home.
func (h *Home) Path(rel string) string {
	h.tb.Helper()
	abs, err := h.paths.Join(filepath.FromSlash(rel))
	if err != nil {
		h.tb.Fatalf("fake home path %q: %v", rel, err)
	}
	return abs
}

// Mkdir creates the directory rel and its parents, and returns its path.
func (h *Home) Mkdir(rel string) string {
	h.tb.Helper()
	abs := h.Path(rel)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		h.tb.Fatalf("mkdir %q: %v", rel, err)
	}
	return abs
}

// WriteFile writes content to rel with mode 0644, creating parents, and
// returns its path.
func (h *Home) WriteFile(rel, content string) string {
	h.tb.Helper()
	return h.WriteFileMode(rel, content, 0o644)
}

// WriteFileMode writes content to rel with mode, creating parents, and
// returns its path. The mode is applied exactly, regardless of the umask.
func (h *Home) WriteFileMode(rel, content string, mode os.FileMode) string {
	h.tb.Helper()
	abs := h.Path(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		h.tb.Fatalf("mkdir for %q: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		h.tb.Fatalf("write %q: %v", rel, err)
	}
	if err := os.Chmod(abs, mode); err != nil {
		h.tb.Fatalf("chmod %q: %v", rel, err)
	}
	return abs
}

// Symlink creates a symlink at rel pointing at target, creating parents, and
// returns the link's path. target is stored as given: it may be relative,
// dangling, or point outside the home to build broken-link fixtures.
func (h *Home) Symlink(rel, target string) string {
	h.tb.Helper()
	abs := h.Path(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		h.tb.Fatalf("mkdir for %q: %v", rel, err)
	}
	if err := os.Symlink(filepath.FromSlash(target), abs); err != nil {
		h.tb.Fatalf("symlink %q -> %q: %v", rel, target, err)
	}
	return abs
}
