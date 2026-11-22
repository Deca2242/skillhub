// Package git implements ports.GitPort by running git through a
// ports.ProcessRunner, inside the clone.
//
// Describe, IsAncestor and TreeSHA only read. Fetch and FastForward are the
// only operations that change a clone, and only by adding objects and moving
// the checked-out branch forward: git fetch origin <commit>, then git merge
// --ff-only --no-overwrite-ignore --no-autostash <commit>. Nothing here
// resets, checks out files, cleans, stashes or rebases (docs/design.md
// section 7, "Clones de git").
//
// Every command runs with the clone as its working directory and starts with
// the same options:
//
//   - -c core.hooksPath=/dev/null, so no hook of the clone runs;
//   - -c core.fsmonitor=false, because a clone's config can name a file
//     system monitor command that git status would start;
//   - -c gc.auto=0 and -c maintenance.auto=false, so no automatic
//     maintenance runs, in the background or not, after a fetch or a merge;
//   - -c submodule.recurse=false, so no submodule is fetched or checked out;
//   - -c core.attributesFile=/dev/null and GIT_ATTR_NOSYSTEM=1 in the
//     environment, so neither the user's global attributes nor the system's
//     can name a filter driver or an end-of-line conversion, and -c
//     core.autocrlf=false, so no end-of-line conversion applies either: a
//     checkout writes the blobs as they are. The attributes a clone carries
//     itself, its .gitattributes files and .git/info/attributes, are checked
//     by the update flow, which refuses a clone that sets any of them;
//   - --no-optional-locks, so that git status never refreshes and rewrites
//     the index: describing a clone changes nothing in it;
//   - --git-dir=<root>/.git and --work-tree=<root>, which take precedence
//     over a GIT_DIR or GIT_WORK_TREE inherited from the environment, so git
//     always works on this clone and no other repository.
//
// GIT_CEILING_DIRECTORIES is set to the clone's parent as well, so that a
// folder that is not itself a repository fails instead of describing
// whatever repository encloses it.
//
// What this guarantees is that nothing upstream brings runs: no hook, no
// file system monitor, no filter. The user's own git configuration still
// applies, as it does to git pull: a fetch runs remote.origin.uploadpack,
// core.sshCommand, the credential helpers and whatever include.path pulls
// in.
package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"skillhub/internal/shared/ports"
)

var _ ports.GitPort = (*Client)(nil)

var (
	// ErrUnknownRevision reports a commit the clone does not have.
	ErrUnknownRevision = errors.New("git: unknown revision")
)

// exitNoSuchRemote is the exit code of git remote get-url for a remote that
// does not exist.
const exitNoSuchRemote = 2

// baseArgs start every git command, before the clone's own options; see the
// package documentation.
var baseArgs = []string{
	"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
	"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "submodule.recurse=false",
	"-c", "core.attributesFile=/dev/null", "-c", "core.autocrlf=false",
	"--no-optional-locks",
}

// Client is a ports.GitPort over the git binary.
type Client struct {
	runner ports.ProcessRunner
}

// New builds a Client that runs git through r.
func New(r ports.ProcessRunner) *Client { return &Client{runner: r} }

// Describe implements ports.GitPort. It runs git status --porcelain=v2
// --branch, which reports the head, the branch and its upstream, and is
// clean when it prints no change line; and git remote get-url origin. A
// clone with no commit yet has an empty Head.
func (c *Client) Describe(ctx context.Context, root string) (ports.CloneStatus, error) {
	if err := checkRoot(root); err != nil {
		return ports.CloneStatus{}, err
	}
	res, err := c.git(ctx, root, "status", "--porcelain=v2", "--branch", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return ports.CloneStatus{}, err
	}
	if res.ExitCode != 0 {
		return ports.CloneStatus{}, exitError(root, "status", res)
	}
	status, err := parseStatus(string(res.Stdout))
	if err != nil {
		return ports.CloneStatus{}, fmt.Errorf("git status in %s: %w", root, err)
	}

	res, err = c.git(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return ports.CloneStatus{}, err
	}
	switch res.ExitCode {
	case 0:
		status.Remote = strings.TrimSpace(string(res.Stdout))
	case exitNoSuchRemote:
		// No origin: the remote stays empty.
	default:
		return ports.CloneStatus{}, exitError(root, "remote get-url origin", res)
	}
	return status, nil
}

// parseStatus reads the output of git status --porcelain=v2 --branch.
func parseStatus(out string) (ports.CloneStatus, error) {
	status := ports.CloneStatus{Clean: true}
	sawOID := false
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		header, ok := strings.CutPrefix(line, "# ")
		if !ok {
			// Any other line is a changed, unmerged, untracked or ignored
			// entry.
			status.Clean = false
			continue
		}
		key, value, _ := strings.Cut(header, " ")
		switch key {
		case "branch.oid":
			sawOID = true
			if value != "(initial)" {
				status.Head = value
			}
		case "branch.head":
			if value != "(detached)" {
				status.Branch = value
			}
		case "branch.upstream":
			status.Upstream = value
		}
	}
	if !sawOID {
		return ports.CloneStatus{}, errors.New("output has no branch.oid header")
	}
	return status, nil
}

// IsAncestor implements ports.GitPort with git merge-base --is-ancestor.
func (c *Client) IsAncestor(ctx context.Context, root, ancestor, descendant string) (bool, error) {
	if err := checkRoot(root); err != nil {
		return false, err
	}
	if err := checkRevision(ancestor); err != nil {
		return false, err
	}
	if err := checkRevision(descendant); err != nil {
		return false, err
	}
	res, err := c.git(ctx, root, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, exitError(root, "merge-base --is-ancestor", res)
	}
}

// TreeSHA implements ports.GitPort. commit must name a commit; its root
// tree SHA is returned.
func (c *Client) TreeSHA(ctx context.Context, root, commit string) (string, error) {
	if err := checkRoot(root); err != nil {
		return "", err
	}
	if err := checkRevision(commit); err != nil {
		return "", err
	}
	res, err := c.git(ctx, root, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{commit}^{tree}")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%w: %s in %s: %w", ErrUnknownRevision, commit, root, exitError(root, "rev-parse", res))
	}
	sha := strings.TrimSpace(string(res.Stdout))
	if !isObjectID(sha) {
		return "", fmt.Errorf("git rev-parse in %s: unexpected output %q", root, sha)
	}
	return sha, nil
}

// Fetch implements ports.GitPort: git fetch origin <commit>, without tags,
// submodules or FETCH_HEAD. It only adds objects to the clone. commit must
// be a full commit SHA, the exact one the user reviewed.
func (c *Client) Fetch(ctx context.Context, root, commit string) error {
	return c.write(ctx, root, commit, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "origin", commit)
}

// FastForward implements ports.GitPort: git merge --ff-only
// --no-overwrite-ignore --no-autostash <commit>. git refuses rather than
// create a merge, overwrite an untracked or ignored file, or stash local
// changes away. commit must be a full commit SHA.
func (c *Client) FastForward(ctx context.Context, root, commit string) error {
	return c.write(ctx, root, commit, "merge", "--ff-only", "--no-overwrite-ignore", "--no-autostash", commit)
}

// write runs one of the two commands that change a clone.
func (c *Client) write(ctx context.Context, root, commit string, args ...string) error {
	if err := checkRoot(root); err != nil {
		return err
	}
	if !isObjectID(commit) {
		return fmt.Errorf("git %s: %q is not a full commit SHA", args[0], commit)
	}
	res, err := c.git(ctx, root, args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return exitError(root, args[0], res)
	}
	return nil
}

// git runs one git command inside root. A process that ran is returned with
// its exit code; only a refused or unstartable command is an error.
func (c *Client) git(ctx context.Context, root string, args ...string) (ports.Result, error) {
	full := make([]string, 0, len(baseArgs)+2+len(args))
	full = append(full, baseArgs...)
	full = append(full, "--git-dir="+filepath.Join(root, ".git"), "--work-tree="+root)
	full = append(full, args...)
	res, err := c.runner.Run(ctx, ports.Command{
		Name: "git",
		Args: full,
		Dir:  root,
		Env:  []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(root), "GIT_ATTR_NOSYSTEM=1"},
	})
	if err != nil {
		return ports.Result{}, fmt.Errorf("git %s in %s: %w", args[0], root, err)
	}
	return res, nil
}

func exitError(root, what string, res ports.Result) error {
	return fmt.Errorf("git %s in %s exited %d: %s", what, root, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
}

func checkRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("git: clone root %q is not an absolute path", root)
	}
	return nil
}

// checkRevision refuses a revision git could read as an option or that
// carries whitespace or control characters.
func checkRevision(rev string) error {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("git: invalid revision %q", rev)
	}
	for _, r := range rev {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("git: invalid revision %q", rev)
		}
	}
	return nil
}

// isObjectID reports whether s is a full SHA-1 or SHA-256 object name.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
