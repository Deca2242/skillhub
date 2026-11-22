package treehash_test

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureFile is one entry of a fixture folder. A zero Mode means a
// directory; Link makes a symlink.
type fixtureFile struct {
	path    string
	content string
	mode    os.FileMode
	link    string
	dir     bool
}

// fixture is a folder to hash, built fresh in a temporary directory.
type fixture struct {
	name  string
	files []fixtureFile
}

// fixtures are the folders whose SHAs were recorded from real git
// (git add -A -f, then git write-tree, see treehash_realgit_test.go).
func fixtures() []fixture {
	return []fixture{
		{name: "empty", files: nil},
		{name: "only-empty-dirs", files: []fixtureFile{
			{path: "a", dir: true},
			{path: "b/c/d", dir: true},
		}},
		{name: "single-file", files: []fixtureFile{
			{path: "SKILL.md", content: "---\nname: demo\n---\nhola\n", mode: 0o644},
		}},
		{name: "single-file-executable", files: []fixtureFile{
			{path: "SKILL.md", content: "---\nname: demo\n---\nhola\n", mode: 0o755},
		}},
		{name: "full", files: []fixtureFile{
			{path: "SKILL.md", content: "---\nname: demo\ndescription: una skill\n---\nhola\n", mode: 0o644},
			{path: "scripts/run.sh", content: "#!/bin/sh\necho hi\n", mode: 0o755},
			{path: "scripts/lib/util.txt", content: "util\n", mode: 0o644},
			// Only the owner's execute bit counts, as in git.
			{path: "scripts/group-exec.sh", content: "echo g\n", mode: 0o654},
			{path: "docs/a b.md", content: "espacios\n", mode: 0o644},
			{path: "docs/ñandú.md", content: "utf-8\n", mode: 0o600},
			// git orders a tree as if its name ended in "/": foo-bar, foo.txt, foo.
			{path: "foo-bar", content: "dash\n", mode: 0o644},
			{path: "foo.txt", content: "dot\n", mode: 0o644},
			{path: "foo/inner.md", content: "inner\n", mode: 0o644},
			{path: "empty-file", content: "", mode: 0o644},
			{path: "link", link: "SKILL.md"},
			{path: "scripts/dangling", link: "../missing"},
			// Empty directories are not in a git tree.
			{path: "empty", dir: true},
			{path: "nested-empty/inner", dir: true},
			// Nor is anything named .git, at any depth.
			{path: ".git/HEAD", content: "ref: refs/heads/main\n", mode: 0o644},
			{path: "docs/.git/config", content: "x\n", mode: 0o644},
		}},
	}
}

// build writes the fixture under a new temporary directory and returns its
// root.
func (f fixture) build(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), f.name)
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range f.files {
		abs := filepath.Join(root, filepath.FromSlash(file.path))
		switch {
		case file.dir:
			if err := os.MkdirAll(abs, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		default:
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if file.link != "" {
			if err := os.Symlink(file.link, abs); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(abs, []byte(file.content), file.mode); err != nil {
			t.Fatal(err)
		}
		// Exact mode regardless of the umask.
		if err := os.Chmod(abs, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
