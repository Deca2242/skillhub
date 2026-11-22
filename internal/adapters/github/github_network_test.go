//go:build network

package github_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/adapters/github"
	"skillhub/internal/adapters/proc"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

type counting304Runner struct {
	inner    ports.ProcessRunner
	count304 int
}

func (r *counting304Runner) Run(ctx context.Context, cmd ports.Command) (ports.Result, error) {
	res, err := r.inner.Run(ctx, cmd)
	if err == nil && bytes.HasPrefix(res.Stdout, []byte("HTTP/")) {
		if line, _, ok := bytes.Cut(res.Stdout, []byte("\n")); ok && bytes.Contains(line, []byte(" 304 ")) {
			r.count304++
		}
	}
	return res, err
}

// TestClient_Network runs the shared contract suite against the real gh CLI
// when -tags network is passed (only with explicit user approval,
// docs/design.md section 11).
//
// Assumed fixture data for octocat/Hello-World at commit
// 7fd1a60b01f91b314f59955a4e4d4e80d8edf11d:
//   - default branch: master
//   - README content: "Hello World!\n" (blob SHA 980a0d5f19a64b4b30a87d4206aade58726b60e3)
//
// These values are computed locally and confirmed against the live GitHub API
// when the network test is executed.
func TestClient_Network(t *testing.T) {
	p, err := paths.FromOS()
	if err != nil {
		t.Fatalf("paths.FromOS: %v", err)
	}
	runner := &counting304Runner{inner: proc.New()}
	client, err := github.New(runner, p)
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}

	const (
		repo          = "octocat/Hello-World"
		defaultBranch = "master"
		fixedCommit   = "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"
	)
	readmeContent := []byte("Hello World!\n")
	wantBlobSHA := treehash.BlobSHA(readmeContent)

	tmpDir := t.TempDir()
	readmePath := filepath.Join(tmpDir, "README")
	if err := os.WriteFile(readmePath, readmeContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readmePath, 0o644); err != nil {
		t.Fatal(err)
	}
	wantRootTree, err := treehash.Dir(tmpDir)
	if err != nil {
		t.Fatalf("treehash.Dir: %v", err)
	}

	exp := contractExpectation{
		Repo:          repo,
		DefaultBranch: defaultBranch,
		C1:            fixedCommit,
		C2:            fixedCommit,
		RootTree:      wantRootTree,
		Blobs: map[string][]byte{
			wantBlobSHA: readmeContent,
		},
		MissingPath: "nonexistent-dir-404",
		FilePath:    "README",
	}
	runGitHubContractSuite(t, client, exp)

	// Explicit blob check for the fixed README against locally computed SHA.
	ctx := context.Background()
	gotBlob, err := client.Blob(ctx, repo, wantBlobSHA)
	if err != nil {
		t.Fatalf("Blob(%s): %v", wantBlobSHA, err)
	}
	if !bytes.Equal(gotBlob, readmeContent) {
		t.Fatalf("Blob(%s) = %q, want %q", wantBlobSHA, gotBlob, readmeContent)
	}

	// The root tree read by its own SHA, as a review reads a registered
	// version (the shared suite also covers unknown and invalid SHAs).
	gotTree, err := client.TreeBySHA(ctx, repo, wantRootTree.SHA)
	if err != nil || gotTree.SHA != wantRootTree.SHA || len(gotTree.Nodes) != len(wantRootTree.Nodes) {
		t.Fatalf("TreeBySHA(%s) = %+v, %v; want %+v", wantRootTree.SHA, gotTree, err, wantRootTree)
	}

	// Second check with a fresh Client must hit 304 for both repos/ and commits/.
	runner.count304 = 0
	client2, err := github.New(runner, p)
	if err != nil {
		t.Fatalf("github.New (2nd): %v", err)
	}
	if b, err := client2.DefaultBranch(ctx, repo); err != nil || b != defaultBranch {
		t.Fatalf("2nd DefaultBranch = %q, %v; want %q", b, err, defaultBranch)
	}
	if c, err := client2.ResolveCommit(ctx, repo, defaultBranch); err != nil || c != fixedCommit {
		t.Fatalf("2nd ResolveCommit = %q, %v; want %q", c, err, fixedCommit)
	}
	if runner.count304 != 2 {
		t.Fatalf("2nd check got %d 304 responses, want 2 (repos and commits)", runner.count304)
	}
}
