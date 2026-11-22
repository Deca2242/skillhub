package proc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// The adapter implements the shared port.
var _ ports.ProcessRunner = proc.Runner{}

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		cmd  ports.Command
		want ports.Result
	}{
		{
			name: "stdout, stderr and a non-zero exit code",
			cmd:  ports.Command{Name: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}, Dir: dir},
			want: ports.Result{Stdout: []byte("out\n"), Stderr: []byte("err\n"), ExitCode: 3},
		},
		{
			name: "stdin is fed to the process",
			cmd:  ports.Command{Name: "sh", Args: []string{"-c", "cat"}, Stdin: []byte("hola"), Dir: dir},
			want: ports.Result{Stdout: []byte("hola")},
		},
		{
			name: "extra environment entries are added",
			cmd: ports.Command{
				Name: "sh",
				Args: []string{"-c", `printf %s "$SKILLHUB_PROC_TEST"`},
				Env:  []string{"SKILLHUB_PROC_TEST=valor"},
				Dir:  dir,
			},
			want: ports.Result{Stdout: []byte("valor")},
		},
		{
			name: "a key that only starts with PATH is still allowed",
			cmd:  ports.Command{Name: "sh", Args: []string{"-c", `printf %s "$PATHLIKE"`}, Env: []string{"PATHLIKE=valor"}, Dir: dir},
			want: ports.Result{Stdout: []byte("valor")},
		},
		{
			name: "the working directory is honored",
			cmd:  ports.Command{Name: "sh", Args: []string{"-c", "pwd -P"}, Dir: dir},
			want: ports.Result{Stdout: []byte(mustEval(t, dir) + "\n")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := proc.New().Run(context.Background(), tt.cmd)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			assertResult(t, got, tt.want)
		})
	}
}

func TestRunRefusesBeforeStartingAnything(t *testing.T) {
	// testsupport.Main already lists the real home as a forbidden prefix. The
	// directory below is refused lexically, before anything touches it.
	prefixes := filepath.SplitList(os.Getenv(paths.ForbidPrefixEnv))
	if len(prefixes) == 0 {
		t.Fatalf("%s is empty; testsupport.Main did not run", paths.ForbidPrefixEnv)
	}
	forbidden := filepath.Join(prefixes[0], "skillhub-proc-test-not-created")
	allowed := t.TempDir()
	marker := filepath.Join(allowed, "marker")

	tests := []struct {
		name    string
		cmd     ports.Command
		wantErr error
	}{
		{"an absolute command path bypasses the PATH shims", ports.Command{Name: "/usr/bin/touch", Args: []string{marker}, Dir: allowed}, ports.ErrInvalidCommand},
		{"a relative command path", ports.Command{Name: "./touch", Args: []string{marker}, Dir: allowed}, ports.ErrInvalidCommand},
		{"an empty command name", ports.Command{Name: "", Dir: allowed}, ports.ErrInvalidCommand},
		{"a working directory under the forbidden prefix", ports.Command{Name: "touch", Args: []string{marker}, Dir: forbidden}, paths.ErrForbidden},
		{"a relative working directory", ports.Command{Name: "touch", Args: []string{marker}, Dir: "rel"}, paths.ErrInvalidPath},
		{
			"a PATH entry would hide the shims from grandchildren",
			ports.Command{Name: "touch", Args: []string{marker}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: allowed},
			ports.ErrInvalidCommand,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := proc.New().Run(context.Background(), tt.cmd)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, tt.wantErr)
			}
			assertNotCreated(t, marker)
		})
	}
}

// A command without Dir inherits the process's current directory, so that
// directory is checked like an explicit one (docs/design.md section 5).
func TestRunChecksTheCurrentDirectoryWhenNoDirIsGiven(t *testing.T) {
	forbidden := t.TempDir()
	allowed := t.TempDir()
	marker := filepath.Join(allowed, "marker")
	runner := proc.NewWithGuard(paths.NewGuard(forbidden))

	t.Chdir(forbidden)
	_, err := runner.Run(context.Background(), ports.Command{Name: "touch", Args: []string{marker}})
	if !errors.Is(err, paths.ErrForbidden) {
		t.Fatalf("Run with the current directory under a forbidden prefix = %v, want ErrForbidden", err)
	}
	assertNotCreated(t, marker)

	t.Chdir(allowed)
	res, err := runner.Run(context.Background(), ports.Command{Name: "sh", Args: []string{"-c", "pwd -P"}})
	if err != nil {
		t.Fatalf("Run with an allowed current directory: %v", err)
	}
	assertResult(t, res, ports.Result{Stdout: []byte(mustEval(t, allowed) + "\n")})
}

func TestRunReportsCommandsThatCannotRun(t *testing.T) {
	dir := t.TempDir()
	t.Run("a command missing from PATH", func(t *testing.T) {
		_, err := proc.New().Run(context.Background(), ports.Command{Name: "skillhub-no-such-command", Dir: dir})
		if err == nil {
			t.Fatal("Run of a missing command returned no error")
		}
	})
	t.Run("a context that expires kills the process", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		res, err := proc.New().Run(ctx, ports.Command{Name: "sh", Args: []string{"-c", "sleep 5"}, Dir: dir})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run error = %v, want context.DeadlineExceeded", err)
		}
		if res.ExitCode != -1 {
			t.Errorf("ExitCode = %d, want -1 for a killed process", res.ExitCode)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("Run took %v; the process was not killed", elapsed)
		}
	})
}

// A process that exits on its own reports its result even when the context
// expires before Run returns. Here a background grandchild keeps stdout open
// past the deadline, so the context ends while Run still waits for output.
func TestRunReportsAProcessThatFinishedBeforeTheContextExpired(t *testing.T) {
	dir := t.TempDir()
	for _, code := range []int{0, 3} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		script := "echo done; (sleep 0.4) & exit " + string(rune('0'+code))
		res, err := proc.New().Run(ctx, ports.Command{Name: "sh", Args: []string{"-c", script}, Dir: dir})
		cancel()
		if err != nil {
			t.Fatalf("exit %d: Run error = %v, want the finished process's result", code, err)
		}
		if ctx.Err() == nil {
			t.Fatalf("exit %d: the context did not expire during Run; the test proves nothing", code)
		}
		assertResult(t, res, ports.Result{Stdout: []byte("done\n"), ExitCode: code})
	}
}

func assertResult(t *testing.T, got, want ports.Result) {
	t.Helper()
	if string(got.Stdout) != string(want.Stdout) || string(got.Stderr) != string(want.Stderr) || got.ExitCode != want.ExitCode {
		t.Fatalf("Run = {%q %q %d}, want {%q %q %d}", got.Stdout, got.Stderr, got.ExitCode, want.Stdout, want.Stderr, want.ExitCode)
	}
}

func assertNotCreated(t *testing.T, marker string) {
	t.Helper()
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused command ran: marker stat = %v", statErr)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
