package testsupport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"skillhub/internal/shared/paths"
)

func TestNewHomeBuildsAnIsolatedHome(t *testing.T) {
	realHome := passwdHome(t)
	h := NewHome(t)
	root := h.Root()
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("Root() = %q is not a directory: %v", root, err)
	}
	if isWithin(root, realHome) {
		t.Fatalf("Root() = %q is inside the real home", root)
	}
	env := h.Env()
	if env.Home != root || env.ForbidPrefix != os.Getenv(paths.ForbidPrefixEnv) {
		t.Fatalf("Env() = %+v, want Home %q and the process forbidden prefixes", env, root)
	}
	p := h.Paths()
	if p.Home() != root || p.StoreDir() != filepath.Join(root, ".agents", "skills") {
		t.Fatalf("Paths() home = %q, store = %q", p.Home(), p.StoreDir())
	}
	if err := p.Check(filepath.Join(realHome, ".agents")); !errors.Is(err, paths.ErrForbidden) {
		t.Fatalf("Paths().Check(real home path) = %v, want ErrForbidden", err)
	}
	if other := NewHome(t).Root(); other == root {
		t.Fatalf("two fake homes share the root %q", root)
	}
}

func TestHomeWritesAFixtureTree(t *testing.T) {
	h := NewHome(t)
	root := h.Root()

	skill := h.WriteFile(".agents/skills/a/SKILL.md", "---\nname: a\n---\n")
	if skill != filepath.Join(root, ".agents", "skills", "a", "SKILL.md") {
		t.Fatalf("WriteFile returned %q", skill)
	}
	assertFile(t, skill, "---\nname: a\n---\n", 0o644)

	script := h.WriteFileMode(".agents/skills/a/run.sh", "#!/bin/sh\n", 0o755)
	assertFile(t, script, "#!/bin/sh\n", 0o755)

	dir := h.Mkdir(".claude/skills")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("Mkdir: %q is not a directory: %v", dir, err)
	}

	link := h.Symlink(".claude/skills/a", "../../.agents/skills/a")
	if target, err := os.Readlink(link); err != nil || target != "../../.agents/skills/a" {
		t.Fatalf("Readlink(%q) = %q, %v", link, target, err)
	}
	if info, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil || info.IsDir() {
		t.Fatalf("the link does not resolve to the skill: %v", err)
	}

	if got := h.Path("x/y"); got != filepath.Join(root, "x", "y") {
		t.Fatalf("Path(x/y) = %q", got)
	}
}

func TestHomeRejectsPathsOutsideIt(t *testing.T) {
	tests := []struct {
		name string
		call func(h *Home)
	}{
		{"WriteFile climbing out", func(h *Home) { h.WriteFile("../escape", "x") }},
		{"WriteFileMode with an absolute path", func(h *Home) { h.WriteFileMode(filepath.Join(os.TempDir(), "escape"), "x", 0o644) }},
		{"Mkdir climbing out", func(h *Home) { h.Mkdir("../escape") }},
		{"Symlink climbing out", func(h *Home) { h.Symlink("../escape", "target") }},
		{"Path climbing out", func(h *Home) { h.Path("../escape") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &fatalRecorder{TB: t}
			done := make(chan *Home)
			go func() {
				var h *Home
				defer func() { done <- h }()
				h = NewHome(rec)
				tt.call(h)
			}()
			h := <-done
			if rec.msg == "" {
				t.Fatal("the helper accepted a path outside the fake home")
			}
			for _, escaped := range []string{filepath.Join(filepath.Dir(h.Root()), "escape"), filepath.Join(os.TempDir(), "escape")} {
				if _, err := os.Lstat(escaped); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the helper wrote %q: %v", escaped, err)
				}
			}
		})
	}
}

func assertFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("ReadFile(%q) = %q, %v; want %q", path, data, err, content)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("mode of %q = %v, %v; want %v", path, info.Mode().Perm(), err, mode)
	}
}

// fatalRecorder records a Fatal call and ends the calling goroutine, so a
// test can check that a helper fails its test.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatal(args ...any) {
	r.msg = fmt.Sprint(args...)
	runtime.Goexit()
}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}
