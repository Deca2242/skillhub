package treehash_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// recorded holds the tree SHAs real git 2.53 gave for each fixture
// (git add -A -f, then git write-tree), recorded by TestDirMatchesRealGit
// under -tags realgit.
var recorded = map[string]string{
	"empty":                  "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
	"only-empty-dirs":        "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
	"single-file":            "5f29ca8f79c6b4ee7afc8aa1d5410870a0f4d3f0",
	"single-file-executable": "4982d3577a61c9990440d24b793f2d6ee2d31006",
	"full":                   "6037c7fd878258c3e60cb3af6bcef6d8439496f9",
}

// recordedFullListing is git ls-tree -r -t -l of the "full" fixture, with
// core.quotePath=false.
const recordedFullListing = `100644 blob b54d3728ae3e4b99021c74e88ffd6fcc3468a33e      47	SKILL.md
040000 tree 9a10e812b6727caf821436d36b4f4c01ce5d7c46       -	docs
100644 blob 603cc9ac3a6f3932449eb9e836d7fa36a3d311c3       9	docs/a b.md
100644 blob a5244212fb934bb57faf3d5e3e5237a8d7ebaa94       6	docs/ñandú.md
100644 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391       0	empty-file
100644 blob a2544f7ec3007899167de1fef481a5a0fd63fa41       5	foo-bar
100644 blob a2373c722dedbf05f6669eba1ea044484213d03d       4	foo.txt
040000 tree e24649deb540dcf7f7e96d060623e3b75bfe587f       -	foo
100644 blob f05648e753bc95da97c2b753903c1111061d67af       6	foo/inner.md
120000 blob d69512cfc83b99237b23cf465aecb450d8df1a73       8	link
040000 tree cbd053a1865c0e27829676d4e6f7ebcaf606d778       -	scripts
120000 blob f3ade9ba11f7033256308a651b8795066b26c166      10	scripts/dangling
100644 blob f03a1482dc7e4ae369f5f0a06d8c8a5a1bb7d269       7	scripts/group-exec.sh
040000 tree 85fc703c91585c0f468a55ea33e2cea69f818a44       -	scripts/lib
100644 blob 3759e933a83a2d21b350e7aed1948afa2898e588       5	scripts/lib/util.txt
100755 blob 4163036efa65bd4a469e752267498f01ea36a55c      18	scripts/run.sh
`

func TestDirEqualsTheSHAsRecordedFromGit(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			got, err := treehash.Dir(f.build(t))
			if err != nil {
				t.Fatalf("Dir: %v", err)
			}
			if want := recorded[f.name]; got.SHA != want {
				t.Fatalf("Dir(%s).SHA = %s, want %s from git", f.name, got.SHA, want)
			}
		})
	}
}

// The listing covers executable bits, nesting, git's ordering, symlinks,
// and the empty directories and .git entries git leaves out.
func TestDirListsEveryNodeLikeGitLsTree(t *testing.T) {
	full := fixtures()[len(fixtures())-1]
	got, err := treehash.Dir(full.build(t))
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if listing := render(got); listing != recordedFullListing {
		t.Fatalf("Dir nodes:\n%s\nwant (git ls-tree -r -t -l):\n%s", listing, recordedFullListing)
	}
}

func render(tree treehash.Tree) string {
	var b strings.Builder
	for _, n := range tree.Nodes {
		kind, size := "blob", fmt.Sprintf("%7d", n.Size)
		if n.IsTree() {
			kind, size = "tree", "      -"
		}
		fmt.Fprintf(&b, "%s %s %s %s\t%s\n", n.Mode, kind, n.SHA, size, n.Path)
	}
	return b.String()
}

func TestOnlyTheOwnerExecuteBitChangesTheSHA(t *testing.T) {
	single := fixtures()[2]
	plain, err := treehash.Dir(single.build(t))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Nodes[0].Mode != treehash.ModeFile {
		t.Fatalf("mode of a 0644 file = %s, want %s", plain.Nodes[0].Mode, treehash.ModeFile)
	}
	exec := fixtures()[3]
	withExec, err := treehash.Dir(exec.build(t))
	if err != nil {
		t.Fatal(err)
	}
	if withExec.SHA == plain.SHA || withExec.Nodes[0].Mode != treehash.ModeExecutable {
		t.Fatalf("an executable SKILL.md hashed as %s (%s), the plain one as %s", withExec.SHA, withExec.Nodes[0].Mode, plain.SHA)
	}
}

func TestBlobSHA(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty content", "", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{"hello with a newline", "hello\n", "ce013625030ba8dba906f756967f9e9ca394464a"},
		{"the SKILL.md of the single-file fixture", "---\nname: demo\n---\nhola\n", "db3032ae74d7c872d5e0eeb1ada1d9cbc58ee279"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := treehash.BlobSHA([]byte(tt.content)); got != tt.want {
				t.Fatalf("BlobSHA = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDirFollowsASymlinkedRoot(t *testing.T) {
	single := fixtures()[2]
	real := single.build(t)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := treehash.Dir(link)
	if err != nil {
		t.Fatalf("Dir(symlink to a folder): %v", err)
	}
	if got.SHA != recorded["single-file"] {
		t.Fatalf("Dir(symlink).SHA = %s, want the folder's %s", got.SHA, recorded["single-file"])
	}
}

func TestDirRejectsWhatGitCannotHash(t *testing.T) {
	t.Run("a missing folder", func(t *testing.T) {
		_, err := treehash.Dir(filepath.Join(t.TempDir(), "missing"))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Dir(missing) = %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("a file instead of a folder", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := treehash.Dir(file); !errors.Is(err, treehash.ErrUnsupportedEntry) {
			t.Fatalf("Dir(file) = %v, want ErrUnsupportedEntry", err)
		}
	})
	t.Run("a named pipe inside the folder", func(t *testing.T) {
		root := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
			t.Skipf("cannot create a named pipe here: %v", err)
		}
		if _, err := treehash.Dir(root); !errors.Is(err, treehash.ErrUnsupportedEntry) {
			t.Fatalf("Dir(folder with a pipe) = %v, want ErrUnsupportedEntry", err)
		}
	})
}
