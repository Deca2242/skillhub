package github_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/adapters/github"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

var _ ports.GitHubPort = (*github.Client)(nil)

func newTestPaths(t *testing.T) paths.Paths {
	t.Helper()
	home := t.TempDir()
	p, err := paths.New(paths.Env{
		Home:          home,
		XDGConfigHome: filepath.Join(home, ".config"),
		XDGCacheHome:  filepath.Join(home, ".cache"),
		XDGStateHome:  filepath.Join(home, ".local", "state"),
	})
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	return p
}

// writeFixtureRepo creates a fixture repository on disk and returns its root.
func writeFixtureRepo(t *testing.T, files map[string]string, exec ...string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if slices.Contains(exec, rel) {
			mode = 0o755
		}
		if err := os.WriteFile(abs, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(abs, mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// ghAPIResp builds a realistic gh api -i stdout payload where the status line
// ends with \n and headers plus the trailing blank line end with \r\n.
func ghAPIResp(status string, headers map[string]string, body []byte) []byte {
	var b strings.Builder
	b.WriteString("HTTP/2.0 ")
	b.WriteString(status)
	b.WriteByte('\n')
	for k, v := range headers {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	out := []byte(b.String())
	return append(out, body...)
}

type treeEntryJSON struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size,omitempty"`
	URL  string `json:"url"`
}

func treeType(m treehash.Mode) string {
	switch m {
	case treehash.ModeTree:
		return "tree"
	case treehash.ModeSubmodule:
		return "commit"
	default:
		return "blob"
	}
}

func buildTreeJSON(t *testing.T, repo string, tree treehash.Tree, truncated bool) []byte {
	t.Helper()
	entries := make([]treeEntryJSON, 0, len(tree.Nodes))
	for _, n := range tree.Nodes {
		kind := "blobs"
		if n.IsTree() {
			kind = "trees"
		}
		entries = append(entries, treeEntryJSON{
			Path: n.Path,
			Mode: string(n.Mode),
			Type: treeType(n.Mode),
			SHA:  n.SHA,
			Size: n.Size,
			URL:  fmt.Sprintf("https://api.github.com/repos/%s/git/%s/%s", repo, kind, n.SHA),
		})
	}
	payload := struct {
		SHA       string          `json:"sha"`
		URL       string          `json:"url"`
		Tree      []treeEntryJSON `json:"tree"`
		Truncated bool            `json:"truncated"`
	}{
		SHA:       tree.SHA,
		URL:       fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s", repo, tree.SHA),
		Tree:      entries,
		Truncated: truncated,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func buildBlobJSON(t *testing.T, repo, sha string, content []byte) []byte {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(content)
	payload := struct {
		SHA      string `json:"sha"`
		NodeID   string `json:"node_id"`
		Size     int    `json:"size"`
		URL      string `json:"url"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{
		SHA:      sha,
		NodeID:   "B_test",
		Size:     len(content),
		URL:      fmt.Sprintf("https://api.github.com/repos/%s/git/blobs/%s", repo, sha),
		Content:  encoded,
		Encoding: "base64",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type contractExpectation struct {
	Repo          string
	DefaultBranch string
	Tag           string
	AnnotatedTag  string
	C1            string
	C2            string
	SubPath       string
	SubTree       treehash.Tree
	RootTree      treehash.Tree
	Blobs         map[string][]byte
	MissingPath   string
	FilePath      string
}

// runGitHubContractSuite verifies any ports.GitHubPort implementation against
// the full port contract.
func runGitHubContractSuite(t *testing.T, gh ports.GitHubPort, exp contractExpectation) {
	t.Helper()
	ctx := context.Background()

	branch, err := gh.DefaultBranch(ctx, exp.Repo)
	if err != nil || branch != exp.DefaultBranch {
		t.Fatalf("DefaultBranch = %q, %v; want %q", branch, err, exp.DefaultBranch)
	}

	for ref, want := range map[string]string{
		exp.DefaultBranch: exp.C2,
		exp.C1:            exp.C1,
		exp.C2:            exp.C2,
	} {
		if ref == "" {
			continue
		}
		got, err := gh.ResolveCommit(ctx, exp.Repo, ref)
		if err != nil || got != want {
			t.Errorf("ResolveCommit(%s) = %q, %v; want %s", ref, got, err, want)
		}
	}

	t.Run("tags", func(t *testing.T) {
		if exp.Tag == "" && exp.AnnotatedTag == "" {
			t.Skip("no stable tags configured for this repository")
		}
		for ref, want := range map[string]string{
			exp.Tag:          exp.C1,
			exp.AnnotatedTag: exp.C1,
		} {
			if ref == "" {
				continue
			}
			got, err := gh.ResolveCommit(ctx, exp.Repo, ref)
			if err != nil || got != want {
				t.Errorf("ResolveCommit(%s) = %q, %v; want %s", ref, got, err, want)
			}
		}
	})

	t.Run("subtree", func(t *testing.T) {
		if exp.SubPath == "" {
			t.Skip("no subdirectory configured for this repository")
		}
		tree, err := gh.Tree(ctx, exp.Repo, exp.C1, exp.SubPath)
		if err != nil {
			t.Fatalf("Tree(%s): %v", exp.SubPath, err)
		}
		if tree.SHA != exp.SubTree.SHA || len(tree.Nodes) != len(exp.SubTree.Nodes) {
			t.Fatalf("Tree(%s) = %s (%d nodes), want %s (%d nodes)", exp.SubPath, tree.SHA, len(tree.Nodes), exp.SubTree.SHA, len(exp.SubTree.Nodes))
		}
		for i, node := range tree.Nodes {
			if node != exp.SubTree.Nodes[i] {
				t.Errorf("node %d = %+v, want %+v", i, node, exp.SubTree.Nodes[i])
			}
			if node.IsTree() {
				continue
			}
			content, err := gh.Blob(ctx, exp.Repo, node.SHA)
			if err != nil {
				t.Fatalf("Blob(%s): %v", node.Path, err)
			}
			if want, ok := exp.Blobs[node.SHA]; ok && string(content) != string(want) {
				t.Errorf("Blob(%s) = %q, want %q", node.Path, content, want)
			}
			if treehash.BlobSHA(content) != node.SHA {
				t.Errorf("Blob(%s) SHA = %s, want %s", node.Path, treehash.BlobSHA(content), node.SHA)
			}
		}
	})

	root, err := gh.Tree(ctx, exp.Repo, exp.C1, "")
	if err != nil || root.SHA != exp.RootTree.SHA || len(root.Nodes) != len(exp.RootTree.Nodes) {
		t.Fatalf("Tree(root) = %s (%d nodes), %v; want %s (%d nodes)", root.SHA, len(root.Nodes), err, exp.RootTree.SHA, len(exp.RootTree.Nodes))
	}
	if root.SHA == exp.C1 {
		t.Fatalf("Tree(root).SHA = %s equals commit SHA; want distinct tree SHA %s", root.SHA, exp.RootTree.SHA)
	}

	t.Run("tree by sha", func(t *testing.T) {
		want := []treehash.Tree{exp.RootTree}
		if exp.SubPath != "" {
			want = append(want, exp.SubTree)
		}
		for _, w := range want {
			got, err := gh.TreeBySHA(ctx, exp.Repo, w.SHA)
			if err != nil {
				t.Fatalf("TreeBySHA(%s): %v", w.SHA, err)
			}
			if got.SHA != w.SHA || !slices.Equal(got.Nodes, w.Nodes) {
				t.Errorf("TreeBySHA(%s) = %s %+v, want %s %+v", w.SHA, got.SHA, got.Nodes, w.SHA, w.Nodes)
			}
		}
	})

	errCases := []struct {
		name string
		fn   func() error
		want error
	}{
		{"DefaultBranch unknown repo", func() error { _, err := gh.DefaultBranch(ctx, "owner/nonexistent-repo-404"); return err }, ports.ErrRepoNotFound},
		{"ResolveCommit unknown repo", func() error { _, err := gh.ResolveCommit(ctx, "owner/nonexistent-repo-404", "main"); return err }, ports.ErrRepoNotFound},
		{"Tree unknown repo", func() error { _, err := gh.Tree(ctx, "owner/nonexistent-repo-404", exp.C1, ""); return err }, ports.ErrRepoNotFound},
		{"unknown ref", func() error { _, err := gh.ResolveCommit(ctx, exp.Repo, "nope-ref-404"); return err }, ports.ErrRefNotFound},
		{"missing tree path", func() error { _, err := gh.Tree(ctx, exp.Repo, exp.C1, exp.MissingPath); return err }, ports.ErrRefNotFound},
		{"file as tree path", func() error { _, err := gh.Tree(ctx, exp.Repo, exp.C1, exp.FilePath); return err }, ports.ErrRefNotFound},
		{"unknown blob", func() error { _, err := gh.Blob(ctx, exp.Repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); return err }, ports.ErrRefNotFound},
		{"TreeBySHA unknown tree", func() error {
			_, err := gh.TreeBySHA(ctx, exp.Repo, "feedfacefeedfacefeedfacefeedfacefeedface")
			return err
		}, ports.ErrRefNotFound},
		{"TreeBySHA not a sha", func() error { _, err := gh.TreeBySHA(ctx, exp.Repo, "main"); return err }, ports.ErrRefNotFound},
		{"TreeBySHA short sha", func() error { _, err := gh.TreeBySHA(ctx, exp.Repo, exp.RootTree.SHA[:12]); return err }, ports.ErrRefNotFound},
		{"TreeBySHA unknown repo", func() error {
			_, err := gh.TreeBySHA(ctx, "owner/nonexistent-repo-404", exp.RootTree.SHA)
			return err
		}, ports.ErrRepoNotFound},
	}
	for _, tc := range errCases {
		if err := tc.fn(); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func setupContractFixture(t *testing.T) (contractExpectation, string) {
	t.Helper()
	v1Files := map[string]string{
		"README.md":                   "repo\n",
		"skills/a/SKILL.md":           "---\nname: a\n---\n",
		"skills/a/scripts/run.sh":     "#!/bin/sh\n",
		"skills/a/references/deep.md": "deep\n",
		"skills/b/SKILL.md":           "---\nname: b\n---\n",
	}
	v1Dir := writeFixtureRepo(t, v1Files, "skills/a/scripts/run.sh")
	v1RootTree, err := treehash.Dir(v1Dir)
	if err != nil {
		t.Fatal(err)
	}
	v1SkillATree, err := treehash.Dir(filepath.Join(v1Dir, "skills", "a"))
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, n := range v1RootTree.Nodes {
		if n.IsTree() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(v1Dir, filepath.FromSlash(n.Path)))
		if err != nil {
			t.Fatal(err)
		}
		blobs[n.SHA] = data
	}
	return contractExpectation{
		Repo:          "owner/repo",
		DefaultBranch: "main",
		Tag:           "v1",
		AnnotatedTag:  "v1-annotated",
		C1:            "1111111111111111111111111111111111111111",
		C2:            "2222222222222222222222222222222222222222",
		SubPath:       "skills/a",
		SubTree:       v1SkillATree,
		RootTree:      v1RootTree,
		Blobs:         blobs,
		MissingPath:   "skills/zzz",
		FilePath:      "README.md",
	}, v1Dir
}

func expectTreeLevels(t *testing.T, runner *fakes.ProcessRunner, repo string, rootTree treehash.Tree) {
	t.Helper()
	type level struct {
		sha     string
		entries []treeEntryJSON
	}
	levels := map[string]*level{
		"": {sha: rootTree.SHA},
	}
	for _, n := range rootTree.Nodes {
		if n.IsTree() {
			levels[n.Path] = &level{sha: n.SHA}
		}
	}
	for _, n := range rootTree.Nodes {
		parent := ""
		base := n.Path
		if idx := strings.LastIndex(n.Path, "/"); idx >= 0 {
			parent = n.Path[:idx]
			base = n.Path[idx+1:]
		}
		kind := "blobs"
		if n.IsTree() {
			kind = "trees"
		}
		levels[parent].entries = append(levels[parent].entries, treeEntryJSON{
			Path: base,
			Mode: string(n.Mode),
			Type: treeType(n.Mode),
			SHA:  n.SHA,
			Size: n.Size,
			URL:  fmt.Sprintf("https://api.github.com/repos/%s/git/%s/%s", repo, kind, n.SHA),
		})
	}
	for _, lv := range levels {
		payload, err := json.Marshal(struct {
			SHA       string          `json:"sha"`
			URL       string          `json:"url"`
			Tree      []treeEntryJSON `json:"tree"`
			Truncated bool            `json:"truncated"`
		}{
			SHA:  lv.sha,
			URL:  fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s", repo, lv.sha),
			Tree: lv.entries,
		})
		if err != nil {
			t.Fatal(err)
		}
		runner.ExpectAlways(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/%s/git/trees/%s", repo, lv.sha)}},
			ports.Result{Stdout: ghAPIResp("200 OK", nil, payload)},
			nil,
		)
	}
}

func TestGitHubContract_Shared(t *testing.T) {
	exp, v1Dir := setupContractFixture(t)

	t.Run("fakes.GitHub", func(t *testing.T) {
		gh := fakes.NewGitHub()
		gh.AddRepo(exp.Repo, exp.DefaultBranch)
		c1 := gh.Commit(t, exp.Repo, exp.DefaultBranch, v1Dir)
		gh.Tag(exp.Repo, exp.Tag, c1)
		gh.Tag(exp.Repo, exp.AnnotatedTag, c1)
		v2Dir := writeFixtureRepo(t, map[string]string{"skills/a/SKILL.md": "---\nname: a\n---\nv2\n"})
		c2 := gh.Commit(t, exp.Repo, exp.DefaultBranch, v2Dir)

		fakeExp := exp
		fakeExp.C1 = c1
		fakeExp.C2 = c2
		runGitHubContractSuite(t, gh, fakeExp)
	})

	t.Run("github.Client", func(t *testing.T) {
		runner := fakes.NewProcessRunner()
		p := newTestPaths(t)
		client, err := github.New(runner, p)
		if err != nil {
			t.Fatalf("github.New: %v", err)
		}

		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
			ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `W/"repo-1"`}, []byte(`{"id":1,"full_name":"owner/repo","default_branch":"main"}`))},
			nil,
		)
		for ref, sha := range map[string]string{
			"main":         exp.C2,
			"v1":           exp.C1,
			"v1-annotated": exp.C1,
			exp.C1:         exp.C1,
			exp.C2:         exp.C2,
		} {
			runner.Expect(
				ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", fmt.Sprintf("repos/owner/repo/commits/%s", ref)}},
				ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": fmt.Sprintf(`W/"commit-%s"`, ref)}, []byte(sha+"\n"))},
				nil,
			)
		}
		// git/commits/{c1} resolves the real root tree SHA (distinct from commit SHA c1).
		commitObjJSON := []byte(fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, exp.C1, exp.RootTree.SHA))
		runner.ExpectAlways(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/commits/%s", exp.C1)}},
			ports.Result{Stdout: ghAPIResp("200 OK", nil, commitObjJSON)},
			nil,
		)
		// Simulate truncated recursive tree in the shared contract suite too.
		partial := treehash.Tree{SHA: exp.RootTree.SHA, Nodes: exp.RootTree.Nodes[:1]}
		runner.ExpectAlways(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/trees/%s?recursive=1", exp.RootTree.SHA)}},
			ports.Result{Stdout: ghAPIResp("200 OK", nil, buildTreeJSON(t, exp.Repo, partial, true))},
			nil,
		)
		expectTreeLevels(t, runner, exp.Repo, exp.RootTree)

		for _, n := range exp.SubTree.Nodes {
			if n.IsTree() {
				continue
			}
			runner.Expect(
				ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/blobs/%s", n.SHA)}},
				ports.Result{Stdout: ghAPIResp("200 OK", nil, buildBlobJSON(t, exp.Repo, n.SHA, exp.Blobs[n.SHA]))},
				nil,
			)
		}
		// Error cases in contract suite:
		// 1. DefaultBranch unknown repo
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/nonexistent-repo-404"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		// 2. ResolveCommit unknown repo (404 on commit, then 404 on repo)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", "repos/owner/nonexistent-repo-404/commits/main"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/nonexistent-repo-404"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		// 3. Tree unknown repo (404 on git/commits, then 404 on repo)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/nonexistent-repo-404/git/commits/%s", exp.C1)}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/nonexistent-repo-404"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		// 4. unknown ref (422 No commit found)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", "repos/owner/repo/commits/nope-ref-404"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("422 Unprocessable Entity", nil, []byte(`{"message":"No commit found for SHA: nope-ref-404"}`)), Stderr: []byte("gh: No commit found for SHA: nope-ref-404 (HTTP 422)")},
			nil,
		)
		// 5. unknown blob (404 on blob, then repo check hits ETag -> 304)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo/git/blobs/deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", `If-None-Match: W/"repo-1"`, "repos/owner/repo"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("304 Not Modified", map[string]string{"Etag": `W/"repo-1"`}, nil), Stderr: []byte("gh: HTTP 304")},
			nil,
		)
		// TreeBySHA: the subtree is served whole by its recursive listing; the
		// root tree reuses the truncated listing above, walked level by level.
		runner.ExpectAlways(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/trees/%s?recursive=1", exp.SubTree.SHA)}},
			ports.Result{Stdout: ghAPIResp("200 OK", nil, buildTreeJSON(t, exp.Repo, exp.SubTree, false))},
			nil,
		)
		// 6. TreeBySHA unknown tree (404 on the tree, then the repo answers 304)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo/git/trees/feedfacefeedfacefeedfacefeedfacefeedface?recursive=1"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", `If-None-Match: W/"repo-1"`, "repos/owner/repo"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("304 Not Modified", map[string]string{"Etag": `W/"repo-1"`}, nil), Stderr: []byte("gh: HTTP 304")},
			nil,
		)
		// 7. TreeBySHA unknown repo (404 on the tree, then 404 on the repo)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/nonexistent-repo-404/git/trees/%s?recursive=1", exp.RootTree.SHA)}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/nonexistent-repo-404"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
			nil,
		)

		runGitHubContractSuite(t, client, exp)
		if unused := runner.Unused(); len(unused) > 0 {
			t.Errorf("expected gh calls never made: %v", unused)
		}
	})
}

func TestClient_RootTreeSHADistinctFromCommitSHA(t *testing.T) {
	ctx := context.Background()
	exp, _ := setupContractFixture(t)
	runner := fakes.NewProcessRunner()
	p := newTestPaths(t)
	client, err := github.New(runner, p)
	if err != nil {
		t.Fatal(err)
	}

	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/commits/%s", exp.C1)}},
		ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `"gc-1"`}, []byte(fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, exp.C1, exp.RootTree.SHA)))},
		nil,
	)
	// Deliberately put exp.C1 in the tree response's "sha" field to prove
	// buildTreeFromRecursive uses tree.sha from git/commits, not resp.SHA.
	mismatchedResp := exp.RootTree
	mismatchedResp.SHA = exp.C1
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/trees/%s?recursive=1", exp.RootTree.SHA)}},
		ports.Result{Stdout: ghAPIResp("200 OK", nil, buildTreeJSON(t, exp.Repo, mismatchedResp, false))},
		nil,
	)

	got, err := client.Tree(ctx, exp.Repo, exp.C1, "")
	if err != nil {
		t.Fatalf("Tree(root): %v", err)
	}
	if got.SHA != exp.RootTree.SHA {
		t.Fatalf("Tree(root).SHA = %s, want %s (must not use resp.SHA %s)", got.SHA, exp.RootTree.SHA, exp.C1)
	}
	if _, err := os.Stat(p.ETagsFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("etags.json should not be written for git/commits or git/trees; stat err = %v", err)
	}
}

func TestClient_ExitCodeAndBlobHandling(t *testing.T) {
	ctx := context.Background()
	exp, _ := setupContractFixture(t)
	blobSHA := exp.SubTree.Nodes[0].SHA

	t.Run("200 with exit code 1 and empty body fails and is not cached", func(t *testing.T) {
		runner := fakes.NewProcessRunner()
		p := newTestPaths(t)
		client, err := github.New(runner, p)
		if err != nil {
			t.Fatal(err)
		}
		// gh rejects a raw/unsafe body by exiting 1 after printing headers, with empty body.
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/blobs/%s", blobSHA)}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `"b-1"`}, nil), Stderr: []byte("gh: response body contains terminal escape sequences")},
			nil,
		)
		if _, err := client.Blob(ctx, exp.Repo, blobSHA); err == nil {
			t.Fatal("Blob succeeded on exit code 1 with empty body, want error")
		}
		if _, err := os.Stat(p.ETagsFile()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("etags.json should not be written on failed blob; stat err = %v", err)
		}
	})

	t.Run("200 with exit code 1 and valid JSON fails and is not cached", func(t *testing.T) {
		runner := fakes.NewProcessRunner()
		p := newTestPaths(t)
		client, err := github.New(runner, p)
		if err != nil {
			t.Fatal(err)
		}
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `W/"r-bad"`}, []byte(`{"default_branch":"main"}`)), Stderr: []byte("gh: simulated failure")},
			nil,
		)
		if _, err := client.DefaultBranch(ctx, exp.Repo); err == nil {
			t.Fatal("DefaultBranch succeeded on exit code 1 with 200 JSON, want error")
		}
		if _, err := os.Stat(p.ETagsFile()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("etags.json should not be written on exit code 1; stat err = %v", err)
		}
	})

	t.Run("304 with exit code 1 succeeds only when cached", func(t *testing.T) {
		runner := fakes.NewProcessRunner()
		p := newTestPaths(t)
		client, err := github.New(runner, p)
		if err != nil {
			t.Fatal(err)
		}
		// Seed cache with 200.
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
			ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `W/"r-1"`}, []byte(`{"default_branch":"main"}`))},
			nil,
		)
		if _, err := client.DefaultBranch(ctx, exp.Repo); err != nil {
			t.Fatal(err)
		}
		// Next call gets real gh 304: ExitCode 1 and Stderr "gh: HTTP 304".
		// If get checked ExitCode before HTTP status, this would fail!
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", `If-None-Match: W/"r-1"`, "repos/owner/repo"}},
			ports.Result{ExitCode: 1, Stdout: ghAPIResp("304 Not Modified", map[string]string{"Etag": `W/"r-1"`}, nil), Stderr: []byte("gh: HTTP 304")},
			nil,
		)
		branch, err := client.DefaultBranch(ctx, exp.Repo)
		if err != nil || branch != "main" {
			t.Fatalf("DefaultBranch on 304 with exit code 1 = %q, %v; want main, nil", branch, err)
		}
	})
}

func TestClient_SecondCheckAll304AndBoundedETagCache(t *testing.T) {
	ctx := context.Background()
	exp, _ := setupContractFixture(t)
	runner := fakes.NewProcessRunner()
	p := newTestPaths(t)

	client1, err := github.New(runner, p)
	if err != nil {
		t.Fatal(err)
	}
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
		ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `W/"e-repo"`}, []byte(`{"default_branch":"main"}`))},
		nil,
	)
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", "repos/owner/repo/commits/main"}},
		ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `W/"e-commit"`}, []byte(exp.C1+"\n"))},
		nil,
	)
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/commits/%s", exp.C1)}},
		ports.Result{Stdout: ghAPIResp("200 OK", nil, []byte(fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, exp.C1, exp.RootTree.SHA)))},
		nil,
	)
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/trees/%s?recursive=1", exp.RootTree.SHA)}},
		ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": `"e-tree"`}, buildTreeJSON(t, exp.Repo, exp.RootTree, false))},
		nil,
	)
	blobSHA := exp.SubTree.Nodes[0].SHA
	blobSHA2 := exp.RootTree.Nodes[0].SHA
	for _, sha := range []string{blobSHA, blobSHA2} {
		runner.Expect(
			ports.Command{Name: "gh", Args: []string{"api", "-i", fmt.Sprintf("repos/owner/repo/git/blobs/%s", sha)}},
			ports.Result{Stdout: ghAPIResp("200 OK", map[string]string{"Etag": fmt.Sprintf(`"e-blob-%s"`, sha)}, buildBlobJSON(t, exp.Repo, sha, exp.Blobs[sha]))},
			nil,
		)
	}

	if _, err := client1.DefaultBranch(ctx, exp.Repo); err != nil {
		t.Fatal(err)
	}
	if _, err := client1.ResolveCommit(ctx, exp.Repo, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := client1.Tree(ctx, exp.Repo, exp.C1, "skills/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := client1.Blob(ctx, exp.Repo, blobSHA); err != nil {
		t.Fatal(err)
	}
	sizeBefore, err := os.ReadFile(p.ETagsFile())
	if err != nil {
		t.Fatalf("reading etags.json: %v", err)
	}
	if _, err := client1.Blob(ctx, exp.Repo, blobSHA2); err != nil {
		t.Fatal(err)
	}
	sizeAfter, err := os.ReadFile(p.ETagsFile())
	if err != nil {
		t.Fatalf("reading etags.json after 2nd blob: %v", err)
	}
	if string(sizeBefore) != string(sizeAfter) {
		t.Fatalf("etags.json grew after fetching blobs: before=%d bytes, after=%d bytes", len(sizeBefore), len(sizeAfter))
	}
	var cached map[string]any
	if err := json.Unmarshal(sizeAfter, &cached); err != nil {
		t.Fatal(err)
	}
	if len(cached) != 2 {
		t.Fatalf("etags.json has %d entries %v, want only repos/ and commits/ (2 entries)", len(cached), cached)
	}

	// Second check with fresh Client: repos/ and commits/ get 304 (ExitCode 1).
	client2, err := github.New(runner, p)
	if err != nil {
		t.Fatal(err)
	}
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", `If-None-Match: W/"e-repo"`, "repos/owner/repo"}},
		ports.Result{ExitCode: 1, Stdout: ghAPIResp("304 Not Modified", map[string]string{"Etag": `W/"e-repo"`}, nil), Stderr: []byte("gh: HTTP 304")},
		nil,
	)
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", "-H", `If-None-Match: W/"e-commit"`, "repos/owner/repo/commits/main"}},
		ports.Result{ExitCode: 1, Stdout: ghAPIResp("304 Not Modified", map[string]string{"Etag": `W/"e-commit"`}, nil), Stderr: []byte("gh: HTTP 304")},
		nil,
	)
	if b, err := client2.DefaultBranch(ctx, exp.Repo); err != nil || b != "main" {
		t.Fatalf("2nd DefaultBranch = %q, %v", b, err)
	}
	if c, err := client2.ResolveCommit(ctx, exp.Repo, "main"); err != nil || c != exp.C1 {
		t.Fatalf("2nd ResolveCommit = %q, %v", c, err)
	}
	if unused := runner.Unused(); len(unused) != 0 {
		t.Fatalf("unused expectations: %v", unused)
	}
}

func TestClient_InputValidationAndEscaping(t *testing.T) {
	ctx := context.Background()
	runner := fakes.NewProcessRunner()
	p := newTestPaths(t)
	client, err := github.New(runner, p)
	if err != nil {
		t.Fatal(err)
	}

	// Ref with special characters is escaped segment-by-segment.
	sha := "1111111111111111111111111111111111111111"
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", "repos/owner/repo/commits/feat/v1%231%20a%3Fb"}},
		ports.Result{Stdout: ghAPIResp("200 OK", nil, []byte(sha+"\n"))},
		nil,
	)
	got, err := client.ResolveCommit(ctx, "owner/repo", "feat/v1#1 a?b")
	if err != nil || got != sha {
		t.Fatalf("ResolveCommit(escaped) = %q, %v; want %s", got, err, sha)
	}

	// Invalid SHAs rejected without calling gh.
	for _, badSHA := range []string{"", "abc", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "111111111111111111111111111111111111111"} {
		if _, err := client.Tree(ctx, "owner/repo", badSHA, ""); !errors.Is(err, ports.ErrRefNotFound) {
			t.Errorf("Tree(badSHA=%q) = %v, want ErrRefNotFound", badSHA, err)
		}
		if _, err := client.Blob(ctx, "owner/repo", badSHA); !errors.Is(err, ports.ErrRefNotFound) {
			t.Errorf("Blob(badSHA=%q) = %v, want ErrRefNotFound", badSHA, err)
		}
	}

	// Refs with . or .. segments are rejected without calling gh.
	for _, badRef := range []string{".", "..", "../../x", "a/../b", "./a", "a/."} {
		if _, err := client.ResolveCommit(ctx, "owner/repo", badRef); !errors.Is(err, ports.ErrRefNotFound) {
			t.Errorf("ResolveCommit(badRef=%q) = %v, want ErrRefNotFound", badRef, err)
		}
	}

	// Valid repo starting with dot (e.g. acme/.github) is accepted.
	runner.Expect(
		ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/acme/.github"}},
		ports.Result{Stdout: ghAPIResp("200 OK", nil, []byte(`{"default_branch":"main"}`))},
		nil,
	)
	if branch, err := client.DefaultBranch(ctx, "acme/.github"); err != nil || branch != "main" {
		t.Errorf("DefaultBranch(acme/.github) = %q, %v; want main, nil", branch, err)
	}

	// Invalid repos rejected without calling gh.
	for _, badRepo := range []string{"", "owner", "a/b/c", "./repo", "../repo", "owner/.", "owner/..", "owner/bad repo", "owner/repo?x=1"} {
		if _, err := client.DefaultBranch(ctx, badRepo); !errors.Is(err, ports.ErrRepoNotFound) {
			t.Errorf("DefaultBranch(badRepo=%q) = %v, want ErrRepoNotFound", badRepo, err)
		}
	}
}

func TestClient_ErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		call    func(c *github.Client, r *fakes.ProcessRunner) error
		wantErr error
		notWant error
	}{
		{
			name: "repo not found 404",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
					ports.Result{ExitCode: 1, Stdout: ghAPIResp("404 Not Found", nil, []byte(`{"message":"Not Found"}`)), Stderr: []byte("gh: Not Found (HTTP 404)")},
					nil,
				)
				_, err := c.DefaultBranch(context.Background(), "owner/repo")
				return err
			},
			wantErr: ports.ErrRepoNotFound,
		},
		{
			name: "422 No commit found maps to ErrRefNotFound",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", "repos/owner/repo/commits/missing"}},
					ports.Result{ExitCode: 1, Stdout: ghAPIResp("422 Unprocessable Entity", nil, []byte(`{"message":"No commit found for SHA: missing"}`)), Stderr: []byte("gh: No commit found for SHA: missing (HTTP 422)")},
					nil,
				)
				_, err := c.ResolveCommit(context.Background(), "owner/repo", "missing")
				return err
			},
			wantErr: ports.ErrRefNotFound,
		},
		{
			name: "422 other message is generic error not ErrRefNotFound",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "-H", "Accept: application/vnd.github.sha", "repos/owner/repo/commits/bad"}},
					ports.Result{ExitCode: 1, Stdout: ghAPIResp("422 Unprocessable Entity", nil, []byte(`{"message":"Validation Failed"}`)), Stderr: []byte("gh: Validation Failed (HTTP 422)")},
					nil,
				)
				_, err := c.ResolveCommit(context.Background(), "owner/repo", "bad")
				return err
			},
			notWant: ports.ErrRefNotFound,
		},
		{
			name: "422 on a tree by SHA maps to ErrRefNotFound",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				sha := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo/git/trees/" + sha + "?recursive=1"}},
					ports.Result{ExitCode: 1, Stdout: ghAPIResp("422 Unprocessable Entity", nil, []byte(`{"message":"Invalid tree info"}`)), Stderr: []byte("gh: Invalid tree info (HTTP 422)")},
					nil,
				)
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
					ports.Result{Stdout: ghAPIResp("200 OK", nil, []byte(`{"default_branch":"main"}`))},
					nil,
				)
				_, err := c.TreeBySHA(context.Background(), "owner/repo", sha)
				return err
			},
			wantErr: ports.ErrRefNotFound,
		},
		{
			name: "rate limit 403 remaining 0",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
					ports.Result{ExitCode: 1, Stdout: ghAPIResp("403 Forbidden", map[string]string{"X-RateLimit-Remaining": "0"}, []byte(`{"message":"API rate limit exceeded"}`)), Stderr: []byte("gh: API rate limit exceeded (HTTP 403)")},
					nil,
				)
				_, err := c.DefaultBranch(context.Background(), "owner/repo")
				return err
			},
			wantErr: ports.ErrRateLimited,
		},
		{
			name: "rate limit 429",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
					ports.Result{ExitCode: 1, Stdout: ghAPIResp("429 Too Many Requests", nil, []byte(`{"message":"You have exceeded a secondary rate limit"}`)), Stderr: []byte("gh: HTTP 429")},
					nil,
				)
				_, err := c.DefaultBranch(context.Background(), "owner/repo")
				return err
			},
			wantErr: ports.ErrRateLimited,
		},
		{
			name: "not authenticated 401",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
					ports.Result{ExitCode: 1, Stdout: ghAPIResp("401 Unauthorized", nil, []byte(`{"message":"Bad credentials"}`)), Stderr: []byte("gh: Bad credentials (HTTP 401)")},
					nil,
				)
				_, err := c.DefaultBranch(context.Background(), "owner/repo")
				return err
			},
			wantErr: ports.ErrNotAuthenticated,
		},
		{
			name: "not authenticated gh cli stderr",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
					ports.Result{ExitCode: 4, Stderr: []byte("To get started with GitHub CLI, please run:  gh auth login")},
					nil,
				)
				_, err := c.DefaultBranch(context.Background(), "owner/repo")
				return err
			},
			wantErr: ports.ErrNotAuthenticated,
		},
		{
			name: "generic stderr mentioning authentication word is not ErrNotAuthenticated",
			call: func(c *github.Client, r *fakes.ProcessRunner) error {
				r.Expect(
					ports.Command{Name: "gh", Args: []string{"api", "-i", "repos/owner/repo"}},
					ports.Result{ExitCode: 1, Stderr: []byte("gh: authentication plugin returned unexpected TLS error")},
					nil,
				)
				_, err := c.DefaultBranch(context.Background(), "owner/repo")
				return err
			},
			notWant: ports.ErrNotAuthenticated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			p := newTestPaths(t)
			client, err := github.New(runner, p)
			if err != nil {
				t.Fatal(err)
			}
			err = tc.call(client, runner)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if tc.notWant != nil && errors.Is(err, tc.notWant) {
				t.Fatalf("got %v, should NOT match %v", err, tc.notWant)
			}
		})
	}
}
