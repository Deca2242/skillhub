package testsupport

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// childEcho is how TestRunTestChildBody learns what its parent asked: a flag
// of the test binary, the way packages that may not read the environment
// pass arguments to their children.
var childEcho = flag.String("testsupport-child-echo", "", "what TestRunTestChildBody prints, or kill")

// TestRunTestChildBody is the body of the children RunTestChild starts here.
func TestRunTestChildBody(t *testing.T) {
	switch *childEcho {
	case "":
		t.Skip("helper for TestRunTestChild*")
	case "kill":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	default:
		fmt.Printf("CHILD-ECHO %s HOME=%s\n", *childEcho, os.Getenv("HOME"))
	}
}

func TestRunTestChildRunsOneTestOfThisBinaryInItsOwnSandbox(t *testing.T) {
	res, err := RunTestChild(t, "TestRunTestChildBody", "-testsupport-child-echo=hello")
	if err != nil {
		t.Fatalf("RunTestChild: %v\nstdout: %s\nstderr: %s", err, res.Stdout, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout: %s", res.ExitCode, res.Stdout)
	}
	line := ""
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if strings.HasPrefix(l, "CHILD-ECHO ") {
			line = l
		}
	}
	if !strings.HasPrefix(line, "CHILD-ECHO hello HOME=") {
		t.Fatalf("the child did not run the selected test with its flag: %s", res.Stdout)
	}
	childHome := strings.TrimPrefix(line, "CHILD-ECHO hello HOME=")
	if childHome == os.Getenv("HOME") || childHome == passwdHome(t) {
		t.Errorf("the child shares a home with its parent or the real one: %s", childHome)
	}
	// Only the selected test ran: the parent's other tests did not.
	if strings.Contains(string(res.Stdout), "TestMainForbidsTheRealHome") {
		t.Errorf("the child ran other tests: %s", res.Stdout)
	}
}

func TestRunTestChildReportsAKilledChildAndLeavesNoLink(t *testing.T) {
	shims := filepath.Join(active.root, "bin")
	before, err := os.ReadDir(shims)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("kill", func(t *testing.T) {
		_, err := RunTestChild(t, "TestRunTestChildBody", "-testsupport-child-echo=kill")
		if err == nil || !strings.Contains(err.Error(), "killed") {
			t.Fatalf("err = %v, want the error of a killed child", err)
		}
	})
	after, err := os.ReadDir(shims)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the shim directory holds %d entries after the child, %d before", len(after), len(before))
	}
}
