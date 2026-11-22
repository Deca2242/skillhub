package testsupport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/ports"
)

// childSeq keeps the shim names of concurrent children apart.
var childSeq atomic.Int64

// RunTestChild runs this test binary again, only for the test named test,
// with extra arguments for it (usually its own flags), and returns what the
// child printed and how it ended. A child killed by a signal comes back as an
// error that says "killed".
//
// The process adapter runs only bare names looked up on PATH and refuses to
// set PATH, so the binary is linked into the shim directory Main put first on
// PATH, and the link is removed when t ends. The child calls Main itself and
// so gets a sandbox of its own; its TMPDIR, where that sandbox lives, is a
// temporary directory of t, because a killed child cannot remove it. Nothing
// reaches the child through the environment except TMPDIR: arguments go as
// flags, which packages that may not read the environment can parse.
func RunTestChild(t testing.TB, test string, args ...string) (ports.Result, error) {
	t.Helper()
	if active == nil {
		t.Fatal("testsupport.RunTestChild needs testsupport.Main to have built the sandbox")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("testsupport.RunTestChild: %v", err)
	}
	name := fmt.Sprintf("skillhub-test-child-%d-%d", os.Getpid(), childSeq.Add(1))
	link := filepath.Join(active.root, "bin", name)
	if err := os.Symlink(exe, link); err != nil {
		t.Fatalf("testsupport.RunTestChild: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	return proc.New().Run(context.Background(), ports.Command{
		Name: name,
		Dir:  t.TempDir(),
		Args: append([]string{"-test.run=^" + test + "$", "-test.count=1"}, args...),
		Env:  []string{"TMPDIR=" + t.TempDir()},
	})
}
