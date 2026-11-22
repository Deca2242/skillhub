package testsupport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

func TestMain(m *testing.M) { Main(m) }

// childModeEnv makes TestChildProcess act as the body of a child test run,
// so tests can observe what Main does at process boundaries.
const childModeEnv = "SKILLHUB_TESTSUPPORT_CHILD"

const unset = "<unset>"

func passwdHome(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Fatalf("cannot read the real home from the password database: %v", err)
	}
	return u.HomeDir
}

func isWithin(path, prefix string) bool {
	rel, err := filepath.Rel(prefix, path)
	return err == nil && filepath.IsLocal(rel)
}

func TestMainPointsHomeAndXDGIntoATemporaryRoot(t *testing.T) {
	realHome := passwdHome(t)
	home := os.Getenv("HOME")
	if home == "" || home == realHome {
		t.Fatalf("HOME = %q, want a fake home other than the real one %q", home, realHome)
	}
	root := filepath.Dir(home)
	if isWithin(root, realHome) {
		t.Fatalf("the test root %q is inside the real home %q", root, realHome)
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "TMPDIR"} {
		value := os.Getenv(key)
		if value == root || !isWithin(value, root) {
			t.Errorf("%s = %q, want a directory inside the test root %q", key, value, root)
			continue
		}
		if info, err := os.Stat(value); err != nil || !info.IsDir() {
			t.Errorf("%s = %q is not an existing directory: %v", key, value, err)
		}
	}
	for _, key := range []string{"XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		for _, entry := range filepath.SplitList(os.Getenv(key)) {
			if !isWithin(entry, root) {
				t.Errorf("%s entry %q is outside the test root %q", key, entry, root)
			}
		}
	}
	if dir := t.TempDir(); !isWithin(dir, root) {
		t.Errorf("t.TempDir() = %q, want it inside the test root %q", dir, root)
	}
}

func TestMainForbidsTheRealHome(t *testing.T) {
	realHome := passwdHome(t)
	prefixes := filepath.SplitList(os.Getenv(paths.ForbidPrefixEnv))
	found := false
	for _, prefix := range prefixes {
		found = found || prefix == realHome
	}
	if !found {
		t.Fatalf("%s = %q does not list the real home %q", paths.ForbidPrefixEnv, os.Getenv(paths.ForbidPrefixEnv), realHome)
	}

	planted := filepath.Join(realHome, ".agents", "skills", "planted")
	p, err := paths.FromOS()
	if err != nil {
		t.Fatalf("paths.FromOS under testsupport.Main: %v", err)
	}
	if p.Home() != os.Getenv("HOME") {
		t.Fatalf("paths.FromOS().Home() = %q, want the fake HOME %q", p.Home(), os.Getenv("HOME"))
	}
	if err := p.Check(planted); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("Paths.Check(real home path) = %v, want ErrForbidden", err)
	}
	if err := NewHome(t).Paths().Check(planted); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("fake Home Paths.Check(real home path) = %v, want ErrForbidden", err)
	}
	if _, err := paths.New(paths.Env{Home: realHome, ForbidPrefix: os.Getenv(paths.ForbidPrefixEnv)}); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("paths.New with the real home = %v, want ErrForbidden", err)
	}
	if _, err := proc.New().Run(context.Background(), ports.Command{Name: "true", Dir: realHome}); !errors.Is(err, paths.ErrForbidden) {
		t.Errorf("proc Run in the real home = %v, want ErrForbidden", err)
	}
}

func TestShimsBlockRealTools(t *testing.T) {
	shimDir := filepath.SplitList(os.Getenv("PATH"))[0]
	if !isWithin(shimDir, filepath.Dir(os.Getenv("HOME"))) {
		t.Fatalf("the first PATH entry %q is not the shim directory of the test root", shimDir)
	}
	// The tools named in docs/design.md section 11, and curl, which the
	// skills.sh adapter runs (S14).
	for _, tool := range []string{"gh", "git", "npx", "claude", "gentle-ai", "notify-send", "systemctl", "orca", "curl"} {
		t.Run(tool, func(t *testing.T) {
			if realTools[tool] {
				t.Skipf("the real %s is allowed by a build tag", tool)
			}
			requireShim(t, tool)
			res, err := proc.New().Run(context.Background(), ports.Command{Name: tool, Args: []string{"api", "user"}, Dir: t.TempDir()})
			calls := drainBlockedCalls()
			if err != nil {
				t.Fatalf("Run(%s): %v", tool, err)
			}
			if res.ExitCode != shimExitCode {
				t.Errorf("exit code = %d, want %d", res.ExitCode, shimExitCode)
			}
			if stderr := string(res.Stderr); !strings.Contains(stderr, "blocked") || !strings.Contains(stderr, tool) {
				t.Errorf("stderr = %q, want a loud message naming %s", stderr, tool)
			}
			if want := []string{tool + " api user"}; fmt.Sprint(calls) != fmt.Sprint(want) {
				t.Errorf("recorded calls = %q, want %q", calls, want)
			}
		})
	}
}

func TestAnUnhandledBlockedCallFailsTheRun(t *testing.T) {
	if realTools["gh"] {
		t.Skip("the real gh is allowed by a build tag")
	}
	res := runChild(t, "call-gh", nil)
	if res.ExitCode == 0 {
		t.Fatalf("a child run whose test called the real gh exited 0\nstdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	}
	if stderr := string(res.Stderr); !strings.Contains(stderr, "gh api user") || !strings.Contains(stderr, "blocked") {
		t.Fatalf("stderr = %q, want it to report the blocked call", stderr)
	}
}

func TestMainCleansTheEnvironmentAndRemovesItsRoot(t *testing.T) {
	res := runChild(t, "report", []string{
		"XDG_SESSION_TYPE=wayland",
		"XDG_PLANTED=/somewhere",
		"GH_TOKEN=planted",
		"GITHUB_TOKEN=planted",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/planted",
		"SSH_AUTH_SOCK=/planted/agent.sock",
		"GIT_ASKPASS=/planted/askpass",
		"SSH_ASKPASS=/planted/ssh-askpass",
		"GIT_SSH_COMMAND=ssh -i /planted/key",
		"GIT_SSH=/planted/ssh",
	})
	if res.ExitCode != 0 {
		t.Fatalf("child run exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	report := parseReport(t, res.Stdout)
	want := map[string]string{
		"XDG_SESSION_TYPE":         unset,
		"XDG_PLANTED":              unset,
		"DBUS_SESSION_BUS_ADDRESS": unset,
	}
	// The git and ssh credentials go whatever the build tags, realgit
	// included (docs/design.md section 11).
	for _, key := range gitCredentialVars {
		want[key] = unset
	}
	if !realTools["gh"] {
		want["GH_TOKEN"] = unset
		want["GITHUB_TOKEN"] = unset
	}
	for key, value := range want {
		if report[key] != value {
			t.Errorf("child %s = %q, want %q", key, report[key], value)
		}
	}
	childRoot := filepath.Dir(report["HOME"])
	if childRoot == filepath.Dir(os.Getenv("HOME")) {
		t.Fatalf("the child reused this run's root %q", childRoot)
	}
	if _, err := os.Stat(childRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the child root %q still exists after the run: %v", childRoot, err)
	}
}

func TestTheGitAndSSHCredentialsAreGoneFromThisRun(t *testing.T) {
	want := []string{"SSH_AUTH_SOCK", "GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH_COMMAND", "GIT_SSH"}
	if fmt.Sprint(gitCredentialVars) != fmt.Sprint(want) {
		t.Fatalf("gitCredentialVars = %q, want the variables of docs/design.md section 11 %q", gitCredentialVars, want)
	}
	for _, key := range want {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q is still set under Main", key, value)
		}
	}
}

// A setup that fails after creating its root removes the root before
// reporting the failure (docs/design.md section 11).
func TestASetupThatFailsRemovesItsRoot(t *testing.T) {
	boom := errors.New("planted failure")
	var root string
	saved := populate
	t.Cleanup(func() { populate = saved })
	populate = func(box *sandbox, _ string) error {
		root = box.root
		// Leave something behind, as a half-done setup would.
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
			return err
		}
		return boom
	}
	box, err := setup()
	if !errors.Is(err, boom) || box != nil {
		t.Fatalf("setup() = %v, %v; want nil and the planted failure", box, err)
	}
	if root == "" {
		t.Fatal("the planted step never ran; setup failed before creating its root")
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the root %q of the failed setup still exists: %v", root, statErr)
	}
}

func TestMainRefusesATempDirUnderAForbiddenPrefix(t *testing.T) {
	forbidden := t.TempDir()
	tmp := filepath.Join(forbidden, "tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	res := runChild(t, "must-not-run", []string{paths.ForbidPrefixEnv + "=" + forbidden, "TMPDIR=" + tmp})
	if res.ExitCode != setupFailedCode {
		t.Fatalf("child exited %d, want %d\nstdout: %s\nstderr: %s", res.ExitCode, setupFailedCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(string(res.Stderr), "TMPDIR") {
		t.Errorf("stderr = %q, want it to name TMPDIR", res.Stderr)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("Main wrote under the forbidden prefix: %v entries, err %v", len(entries), err)
	}
}

// TestChildProcess is the body of the child runs started by runChild.
func TestChildProcess(t *testing.T) {
	switch mode := os.Getenv(childModeEnv); mode {
	case "":
		t.Skip("helper for tests that run Main in a child process")
	case "report":
		report := map[string]string{}
		keys := []string{"HOME", "XDG_SESSION_TYPE", "XDG_PLANTED", "GH_TOKEN", "GITHUB_TOKEN", "DBUS_SESSION_BUS_ADDRESS"}
		for _, key := range append(keys, gitCredentialVars...) {
			value, ok := os.LookupEnv(key)
			if !ok {
				value = unset
			}
			report[key] = value
		}
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("CHILD-REPORT %s\n", data)
	case "call-gh":
		// A planted real call that nothing inspects: Main must still fail the run.
		requireShim(t, "gh")
		_, _ = proc.New().Run(context.Background(), ports.Command{Name: "gh", Args: []string{"api", "user"}, Dir: t.TempDir()})
	default:
		t.Fatalf("child mode %q ran its tests, but Main should have refused to", mode)
	}
}

// requireShim stops the test unless the first PATH entry is a shim directory
// inside the fake root that holds tool, so a broken Main can never let a test
// reach the real tool.
func requireShim(t *testing.T, tool string) {
	t.Helper()
	home := os.Getenv("HOME")
	shimDir := filepath.SplitList(os.Getenv("PATH"))[0]
	if home == passwdHome(t) || !isWithin(shimDir, filepath.Dir(home)) {
		t.Fatalf("refusing to run %s: the first PATH entry %q is not a shim directory of a fake root", tool, shimDir)
	}
	if info, err := os.Stat(filepath.Join(shimDir, tool)); err != nil || info.IsDir() {
		t.Fatalf("refusing to run %s: no shim for it in %q: %v", tool, shimDir, err)
	}
}

// runChild runs this test binary again with TestChildProcess selected. The
// binary is reached through PATH because proc only runs bare command names.
func runChild(t *testing.T, mode string, env []string) ports.Result {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	const name = "skillhub-testsupport-child"
	if err := os.Symlink(exe, filepath.Join(bin, name)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	res, err := proc.New().Run(context.Background(), ports.Command{
		Name: name,
		Dir:  t.TempDir(),
		Args: []string{"-test.run=^TestChildProcess$", "-test.count=1"},
		Env:  append([]string{childModeEnv + "=" + mode}, env...),
	})
	if err != nil {
		t.Fatalf("running the child: %v", err)
	}
	return res
}

func parseReport(t *testing.T, stdout []byte) map[string]string {
	t.Helper()
	for _, line := range strings.Split(string(stdout), "\n") {
		if data, ok := strings.CutPrefix(line, "CHILD-REPORT "); ok {
			report := map[string]string{}
			if err := json.Unmarshal([]byte(data), &report); err != nil {
				t.Fatal(err)
			}
			return report
		}
	}
	t.Fatalf("no report in the child output: %s", stdout)
	return nil
}
