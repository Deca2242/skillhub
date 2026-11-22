//go:build realgit

package treehash_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// TestDirMatchesRealGit hashes every fixture with real git and with Dir.
// The hermetic tests compare against the values this test prints.
func TestDirMatchesRealGit(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			root := f.build(t)
			gitDir := filepath.Join(t.TempDir(), "git")
			git := func(args ...string) string {
				t.Helper()
				full := append([]string{"--git-dir=" + gitDir, "--work-tree=" + root}, args...)
				res, err := proc.New().Run(context.Background(), ports.Command{Name: "git", Args: full, Dir: root})
				if err != nil || res.ExitCode != 0 {
					t.Fatalf("git %s: %v, exit %d, stderr %s", strings.Join(args, " "), err, res.ExitCode, res.Stderr)
				}
				return string(res.Stdout)
			}
			git("init", "-q")
			git("add", "-A", "-f")
			want := strings.TrimSpace(git("write-tree"))
			listing := git("-c", "core.quotePath=false", "ls-tree", "-r", "-t", "-l", want)
			t.Logf("git tree SHA of %s: %s\n%s", f.name, want, listing)

			got, err := treehash.Dir(root)
			if err != nil {
				t.Fatalf("Dir: %v", err)
			}
			if got.SHA != want {
				t.Errorf("Dir(%s).SHA = %s, git write-tree = %s", f.name, got.SHA, want)
			}
			if mine := lsTree(got); mine != listing {
				t.Errorf("Dir(%s).Nodes:\n%s\ngit ls-tree -r -t -l:\n%s", f.name, mine, listing)
			}
		})
	}
}

// lsTree renders nodes like git ls-tree -r -t -l.
func lsTree(tree treehash.Tree) string {
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
