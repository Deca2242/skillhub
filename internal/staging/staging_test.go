package staging_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/staging"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

const repo = "acme/skills"

// fixture is a fake home, a fake GitHub holding one commit of upstream, and
// a stager writing through the real file adapter.
type fixture struct {
	t     *testing.T
	home  *testsupport.Home
	gh    *fakes.GitHub
	tree  treehash.Tree
	st    staging.Stager
	dir   string
	files map[string]string
}

var upstream = map[string]string{
	"SKILL.md":           "---\nname: alpha\ndescription: Alpha.\n---\nBody\n",
	"scripts/run.sh":     "#!/bin/sh\necho hi\n",
	"references/deep.md": "Deep\n",
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, upstream)
}

// newFixtureWith is newFixture with upstream holding files.
func newFixtureWith(t *testing.T, files map[string]string) *fixture {
	t.Helper()
	home := testsupport.NewHome(t)
	src := home.Mkdir("src/alpha")
	for rel, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		home.WriteFileMode("src/alpha/"+rel, content, mode)
	}
	gh := fakes.NewGitHub()
	gh.AddRepo(repo, "main")
	commit := gh.Commit(t, repo, "main", src)
	tree, err := gh.Tree(context.Background(), repo, commit, "")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		t:     t,
		home:  home,
		gh:    gh,
		tree:  tree,
		st:    staging.Stager{Files: fsadapter.NewFileOps(home.Paths().Guard()), Blobs: gh},
		dir:   home.Path(".agents/.skillhub/staging/abc"),
		files: files,
	}
}

func (f *fixture) request() staging.Request {
	return staging.Request{Repo: repo, Dir: f.dir, Nodes: f.tree.Nodes, SHA: f.tree.SHA}
}

func (f *fixture) blobCalls() int {
	n := 0
	for _, c := range f.gh.Calls() {
		if c.Op == "Blob" {
			n++
		}
	}
	return n
}

func (f *fixture) assertNoDir() {
	f.t.Helper()
	if _, err := os.Lstat(f.dir); !errors.Is(err, os.ErrNotExist) {
		f.t.Errorf("staging location %s left behind (err %v)", f.dir, err)
	}
}

func TestStageWritesTheVerifiedTreeAtTheCallersLocation(t *testing.T) {
	f := newFixture(t)

	got, err := f.st.Stage(context.Background(), f.request())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if got.SHA != f.tree.SHA {
		t.Errorf("staged SHA = %s, want %s", got.SHA, f.tree.SHA)
	}
	onDisk, err := treehash.Dir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.SHA != f.tree.SHA {
		t.Errorf("tree on disk = %s, want %s", onDisk.SHA, f.tree.SHA)
	}
	for rel, want := range f.files {
		data, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(rel)))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", rel, data, err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(f.dir, "scripts", "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh mode = %v, %v; want executable", info.Mode(), err)
	}
}

func TestStageReplacesWhatWasAtTheLocation(t *testing.T) {
	f := newFixture(t)
	f.home.WriteFile(".agents/.skillhub/staging/abc/stale.md", "stale\n")

	if _, err := f.st.Stage(context.Background(), f.request()); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(f.dir, "stale.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale file kept: %v", err)
	}
}

func file(p string, size int64) treehash.Node {
	return treehash.Node{Path: p, Mode: treehash.ModeFile, SHA: strings.Repeat("a", 40), Size: size}
}

func TestStageRefusesTreesItMustNotWrite(t *testing.T) {
	many := []treehash.Node{}
	for i := range 2001 {
		many = append(many, file(fmt.Sprintf("f%d.md", i), 1))
	}
	tests := []struct {
		name  string
		nodes []treehash.Node
		want  error
	}{
		{"parent element", []treehash.Node{file("../escape.md", 1)}, staging.ErrUnsupportedEntry},
		{"empty element", []treehash.Node{file("a//b.md", 1)}, staging.ErrUnsupportedEntry},
		{"dot element", []treehash.Node{file("./b.md", 1)}, staging.ErrUnsupportedEntry},
		{"absolute", []treehash.Node{file("/etc/passwd", 1)}, staging.ErrUnsupportedEntry},
		{".git", []treehash.Node{file(".git/config", 1)}, staging.ErrUnsupportedEntry},
		{".GIT", []treehash.Node{file(".GIT/hooks/post-merge", 1)}, staging.ErrUnsupportedEntry},
		{"backslash", []treehash.Node{file(`a\b.md`, 1)}, staging.ErrUnsupportedEntry},
		{"NUL", []treehash.Node{file("a\x00b.md", 1)}, staging.ErrUnsupportedEntry},
		{"carriage return", []treehash.Node{file("a\rb.md", 1)}, staging.ErrUnsupportedEntry},
		{"escape sequence", []treehash.Node{file("a\x1b[2Kb.md", 1)}, staging.ErrUnsupportedEntry},
		{"newline", []treehash.Node{file("a\nb.md", 1)}, staging.ErrUnsupportedEntry},
		{"tab", []treehash.Node{file("a\tb.md", 1)}, staging.ErrUnsupportedEntry},
		{"DEL", []treehash.Node{file("a\x7fb.md", 1)}, staging.ErrUnsupportedEntry},
		{"C1 control", []treehash.Node{file("a"+string(rune(0x9b))+"2Kb.md", 1)}, staging.ErrUnsupportedEntry},
		{"not UTF-8", []treehash.Node{file("a\xffb.md", 1)}, staging.ErrUnsupportedEntry},
		{"bidi mark", []treehash.Node{file("factura"+string(rune(0x202e))+"dm.exe", 1)}, staging.ErrUnsupportedEntry},
		{"bidi mark in a directory", []treehash.Node{file("ref"+string(rune(0x2066))+"/a.md", 1)}, staging.ErrUnsupportedEntry},
		{"symlink", []treehash.Node{{Path: "link", Mode: treehash.ModeSymlink, SHA: strings.Repeat("b", 40), Size: 4}}, staging.ErrUnsupportedEntry},
		{"submodule", []treehash.Node{{Path: "sub", Mode: treehash.ModeSubmodule, SHA: strings.Repeat("c", 40)}}, staging.ErrUnsupportedEntry},
		{"too many files", many, staging.ErrLimitsExceeded},
		{"too large", []treehash.Node{file("big.md", 65<<20)}, staging.ErrLimitsExceeded},
		{"too deep", []treehash.Node{file(strings.Repeat("d/", 24)+"f.md", 1)}, staging.ErrLimitsExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			req := f.request()
			req.Nodes = tt.nodes

			_, err := f.st.Stage(context.Background(), req)

			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if n := f.blobCalls(); n != 0 {
				t.Errorf("%d blobs fetched for a refused tree", n)
			}
			f.assertNoDir()
		})
	}
}

// A file name with a bidi control would print reordered: each of the
// Unicode bidi controls is refused (A28), in a file or a directory name.
func TestCheckNodesRefusesEveryBidiControl(t *testing.T) {
	bidi := []rune{0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069}
	for _, r := range bidi {
		for _, p := range []string{"dir/a" + string(r) + "b.md", "d" + string(r) + "/a.md"} {
			err := staging.CheckNodes([]treehash.Node{file(p, 1)})
			if !errors.Is(err, staging.ErrUnsupportedEntry) {
				t.Errorf("U+%04X in %q: err = %v, want %v", r, p, err, staging.ErrUnsupportedEntry)
			}
			if err != nil && strings.ContainsRune(err.Error(), r) {
				t.Errorf("U+%04X: the error prints the name as it is: %q", r, err.Error())
			}
		}
	}
	// A symlink is refused for its mode too, but its name is checked first:
	// the error quotes it instead of printing it as it is.
	link := treehash.Node{Path: "enlace\x1b[2K" + string(rune(0x202e)), Mode: treehash.ModeSymlink, SHA: strings.Repeat("b", 40)}
	err := staging.CheckNodes([]treehash.Node{link})
	if !errors.Is(err, staging.ErrUnsupportedEntry) || strings.ContainsAny(err.Error(), "\x1b"+string(rune(0x202e))) {
		t.Errorf("a symlink with a hostile name: err = %q, want %v with the name quoted", err, staging.ErrUnsupportedEntry)
	}
}

// Names in any script stage as they are: accents, eñes, emoji, other
// alphabets and spaces are not control characters.
func TestStageWritesFileNamesWithAccents(t *testing.T) {
	files := map[string]string{
		"SKILL.md":                  "---\nname: alpha\ndescription: Alpha.\n---\nBody\n",
		"referencias/canción.md":    "Canción\n",
		"guía del ñandú 🎉.md":       "Ñandú\n",
		"עברית/árabe-مرحبا.txt":     "Hola\n",
		"Ångström/pingüino café.md": "Café\n",
	}
	f := newFixtureWith(t, files)

	got, err := f.st.Stage(context.Background(), f.request())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if got.SHA != f.tree.SHA {
		t.Errorf("staged SHA = %s, want %s", got.SHA, f.tree.SHA)
	}
	for rel, want := range files {
		data, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(rel)))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", rel, data, err, want)
		}
	}
}

func TestStageAcceptsATreeAtTheLimits(t *testing.T) {
	f := newFixture(t)
	nodes := []treehash.Node{}
	for i := range 2000 {
		nodes = append(nodes, file(fmt.Sprintf("f%d.md", i), 1))
	}
	nodes = append(nodes, file(strings.Repeat("d/", 23)+"f.md", 1))
	req := f.request()
	req.Nodes = nodes[1:] // 1999 files plus the deep one: at both limits

	_, err := f.st.Stage(context.Background(), req)

	// The blobs do not exist upstream: the tree passed the limits and
	// failed at the download, which proves the limits are inclusive.
	if errors.Is(err, staging.ErrLimitsExceeded) || errors.Is(err, staging.ErrUnsupportedEntry) || err == nil {
		t.Fatalf("err = %v, want a download failure", err)
	}
	f.assertNoDir()
}

// lyingBlobs serves content that does not hash to the requested SHA.
type lyingBlobs struct{ *fakes.GitHub }

func (l lyingBlobs) Blob(ctx context.Context, r, sha string) ([]byte, error) {
	content, err := l.GitHub.Blob(ctx, r, sha)
	return append(content, "tampered"...), err
}

func TestStageRefusesABlobThatDoesNotMatchItsSHA(t *testing.T) {
	f := newFixture(t)
	f.st.Blobs = lyingBlobs{f.gh}

	_, err := f.st.Stage(context.Background(), f.request())

	if !errors.Is(err, staging.ErrBlobMismatch) {
		t.Fatalf("err = %v, want ErrBlobMismatch", err)
	}
	f.assertNoDir()
}

func TestStageRefusesATreeWhoseSHAIsNotTheExpectedOne(t *testing.T) {
	f := newFixture(t)
	req := f.request()
	req.SHA = strings.Repeat("f", 40)

	_, err := f.st.Stage(context.Background(), req)

	if !errors.Is(err, staging.ErrTreeMismatch) {
		t.Fatalf("err = %v, want ErrTreeMismatch", err)
	}
	f.assertNoDir()
}

func TestStagePropagatesADownloadFailureAndCleansUp(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("network down")
	f.gh.Fail = func(c fakes.Call) error {
		if c.Op == "Blob" {
			return boom
		}
		return nil
	}

	_, err := f.st.Stage(context.Background(), f.request())

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the download failure", err)
	}
	if errors.Is(err, staging.ErrBlobMismatch) || errors.Is(err, staging.ErrTreeMismatch) {
		t.Errorf("a download failure reported as a mismatch: %v", err)
	}
	f.assertNoDir()
}

func TestStageReusesLocalFilesWhoseContentMatches(t *testing.T) {
	f := newFixture(t)
	local := f.home.Path("src/alpha")
	localTree, err := treehash.Dir(local)
	if err != nil {
		t.Fatal(err)
	}
	req := f.request()
	req.Reuse, req.ReuseNodes = local, localTree.Nodes

	if _, err := f.st.Stage(context.Background(), req); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if n := f.blobCalls(); n != 0 {
		t.Errorf("%d blobs downloaded although every file was local", n)
	}
}

func TestStageDownloadsALocalFileThatChangedSinceItWasHashed(t *testing.T) {
	f := newFixture(t)
	local := f.home.Path("src/alpha")
	localTree, err := treehash.Dir(local)
	if err != nil {
		t.Fatal(err)
	}
	f.home.WriteFile("src/alpha/references/deep.md", "Changed after hashing\n")
	req := f.request()
	req.Reuse, req.ReuseNodes = local, localTree.Nodes

	got, err := f.st.Stage(context.Background(), req)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if n := f.blobCalls(); n != 1 {
		t.Errorf("%d blobs downloaded, want 1 (the changed file)", n)
	}
	if got.SHA != f.tree.SHA {
		t.Errorf("staged SHA = %s, want %s", got.SHA, f.tree.SHA)
	}
}
