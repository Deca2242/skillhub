// Package testsupport is the hermetic test harness of skillhub: the TestMain
// body every test package calls and the fake home builder. The in-memory
// doubles of every port live in the subpackage fakes, so that packages under
// internal/shared can import testsupport from their tests without a cycle.
package testsupport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/shared/paths"
)

const (
	// shimExitCode is the exit code of every blocked tool.
	shimExitCode = 97
	// setupFailedCode is the exit code of a test run Main refused to start.
	setupFailedCode = 3
	// blockedLogName is the file, in the sandbox root, where shims record calls.
	blockedLogName = "blocked-calls.log"
)

// blockedTools are the tools whose real binaries a test must never reach.
// The first eight are the ones docs/design.md section 11 names; the rest
// could reach the network or the desktop the same way.
var blockedTools = []string{
	"gh", "git", "npx", "claude", "gentle-ai", "notify-send", "systemctl", "orca",
	"npm", "node", "curl", "wget", "systemd-run", "xdg-open",
}

// realTools lists blocked tools a build tag lets through: realgit for git,
// network for gh. It is filled by init functions in tagged files.
var realTools = map[string]bool{}

// tokenVars are credentials removed from the test environment unless the
// network build tag lets the real gh through.
var tokenVars = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}

// gitCredentialVars are the variables through which git or ssh would
// authenticate as the user. They are removed whatever the build tags,
// realgit included.
var gitCredentialVars = []string{"SSH_AUTH_SOCK", "GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH_COMMAND", "GIT_SSH"}

// sandbox is the temporary root of the current test binary.
type sandbox struct {
	root       string
	blockedLog string
}

var active *sandbox

// Main is the TestMain body every test package in skillhub must call
// (enforced by internal/arch):
//
//	func TestMain(m *testing.M) { testsupport.Main(m) }
//
// Before any test runs it:
//   - points HOME, TMPDIR and every XDG base directory variable at a fresh
//     temporary root, and unsets every other XDG_* variable;
//   - sets paths.ForbidPrefixEnv to the real home, read from the password
//     database rather than from HOME, plus the inherited HOME and any
//     prefixes already listed, so Paths, FileOps and proc refuse them;
//   - puts first on PATH a directory of shims that make gh, git, npx and the
//     other blockedTools fail loudly and record the call;
//   - removes from the environment the GitHub tokens (unless the network
//     tag lets the real gh through), the variables through which git or ssh
//     would authenticate as the user (always, realgit included) and the
//     desktop bus address.
//
// After the tests it fails the run if any blocked tool was called, even when
// the code under test swallowed the failure, and removes the root.
// If the sandbox cannot be built safely, no test runs, and a root already
// created is removed first.
func Main(m *testing.M) {
	os.Exit(run(m.Run))
}

func run(tests func() int) int {
	box, err := setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testsupport: refusing to run tests: %v\n", err)
		return setupFailedCode
	}
	active = box
	code := tests()
	if calls := drainBlockedCalls(); len(calls) > 0 {
		fmt.Fprintf(os.Stderr, "testsupport: tests called real tools that are blocked in tests; use the fake ProcessRunner:\n  %s\n",
			strings.Join(calls, "\n  "))
		if code == 0 {
			code = 1
		}
	}
	if err := removeAll(box.root); err != nil {
		fmt.Fprintf(os.Stderr, "testsupport: cannot remove the test root %s: %v\n", box.root, err)
	}
	return code
}

func setup() (*sandbox, error) {
	prefixes, err := forbiddenPrefixes()
	if err != nil {
		return nil, err
	}
	guard := paths.NewGuard(prefixes)
	// Checked before anything is created, so nothing is ever written there.
	if err := guard.Check(os.TempDir()); err != nil {
		return nil, fmt.Errorf("the temporary directory is under a forbidden prefix; set TMPDIR outside the real home: %w", err)
	}
	root, err := os.MkdirTemp("", "skillhub-test-")
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := guard.Check(root); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("the test root resolves under a forbidden prefix; set TMPDIR outside the real home: %w", err)
	}
	box := &sandbox{root: root, blockedLog: filepath.Join(root, blockedLogName)}
	if err := populate(box, prefixes); err != nil {
		// Leave nothing behind: the root is removed before the failure is
		// reported.
		if rmErr := removeAll(root); rmErr != nil {
			return nil, errors.Join(err, fmt.Errorf("cannot remove the test root %s: %w", root, rmErr))
		}
		return nil, err
	}
	return box, nil
}

// populate fills the sandbox root and points the environment at it. It is a
// variable so a test can make it fail after the root exists.
var populate = populateSandbox

func populateSandbox(box *sandbox, prefixes string) error {
	root := box.root
	dirs := map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "xdg/config",
		"XDG_CACHE_HOME":  "xdg/cache",
		"XDG_STATE_HOME":  "xdg/state",
		"XDG_DATA_HOME":   "xdg/data",
		"XDG_RUNTIME_DIR": "xdg/runtime",
		"XDG_CONFIG_DIRS": "xdg/config-dirs",
		"XDG_DATA_DIRS":   "xdg/data-dirs",
		"TMPDIR":          "tmp",
	}
	set := map[string]string{
		paths.ForbidPrefixEnv: prefixes,
		"GIT_CONFIG_NOSYSTEM": "1",
	}
	for key, rel := range dirs {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		set[key] = dir
	}
	shimDir := filepath.Join(root, "bin")
	if err := writeShims(shimDir, box.blockedLog); err != nil {
		return err
	}
	set["PATH"] = shimDir + string(os.PathListSeparator) + os.Getenv("PATH")

	unset := append([]string{"DBUS_SESSION_BUS_ADDRESS"}, gitCredentialVars...)
	if !realTools["gh"] {
		unset = append(unset, tokenVars...)
		unset = append(unset, "GH_CONFIG_DIR", "GH_HOST")
	}
	for _, entry := range os.Environ() {
		if key, _, _ := strings.Cut(entry, "="); strings.HasPrefix(key, "XDG_") {
			unset = append(unset, key)
		}
	}
	for _, key := range unset {
		if err := os.Unsetenv(key); err != nil {
			return err
		}
	}
	for key, value := range set {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

// forbiddenPrefixes lists the real home from the password database, the
// inherited HOME and any prefix already listed in paths.ForbidPrefixEnv.
func forbiddenPrefixes() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cannot read the real home from the password database: %w", err)
	}
	realHome := filepath.Clean(u.HomeDir)
	if !filepath.IsAbs(realHome) || realHome == string(filepath.Separator) {
		return "", fmt.Errorf("the real home %q from the password database is not usable as a forbidden prefix", u.HomeDir)
	}
	list := []string{realHome}
	candidates := append([]string{os.Getenv("HOME")}, filepath.SplitList(os.Getenv(paths.ForbidPrefixEnv))...)
	for _, candidate := range candidates {
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		if clean := filepath.Clean(candidate); !slices.Contains(list, clean) {
			list = append(list, clean)
		}
	}
	return strings.Join(list, string(os.PathListSeparator)), nil
}

// drainBlockedCalls returns the calls recorded by the shims and clears them.
// It is unexported on purpose: only this package's own tests may consume a
// planted call; for every other package a blocked call fails the run.
func drainBlockedCalls() []string {
	if active == nil {
		return nil
	}
	data, err := os.ReadFile(active.blockedLog)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []string{fmt.Sprintf("(cannot read %s: %v)", active.blockedLog, err)}
	}
	_ = os.Remove(active.blockedLog)
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// removeAll removes root even when tests left read-only directories in it.
func removeAll(root string) error {
	if err := os.RemoveAll(root); err == nil {
		return nil
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(root)
}
