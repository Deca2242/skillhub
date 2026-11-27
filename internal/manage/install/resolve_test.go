package install_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/manage/install"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/staging"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func skillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
}

// world is a fake home and a fake GitHub whose repositories are built from
// folders of the home.
type world struct {
	t    *testing.T
	home *testsupport.Home
	gh   *fakes.GitHub
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return &world{t: t, home: testsupport.NewHome(t), gh: fakes.NewGitHub()}
}

// publish commits files, by slash path, as the head of branch in repo, and
// returns the commit and the hash of the committed folder.
func (w *world) publish(repo, branch string, files map[string]string) (string, treehash.Tree) {
	w.t.Helper()
	dir := "src/" + strings.ReplaceAll(repo, "/", "_") + "_" + strings.ReplaceAll(branch, "/", "_")
	w.home.Mkdir(dir)
	for rel, content := range files {
		w.home.WriteFile(dir+"/"+rel, content)
	}
	if _, err := w.gh.DefaultBranch(context.Background(), repo); errors.Is(err, ports.ErrRepoNotFound) {
		w.gh.AddRepo(repo, branch)
	}
	tree, err := treehash.Dir(w.home.Path(dir))
	if err != nil {
		w.t.Fatal(err)
	}
	return w.gh.Commit(w.t, repo, branch, w.home.Path(dir)), tree
}

func (w *world) resolver() install.Resolver { return install.Resolver{GitHub: w.gh} }

func (w *world) resolve(input string) (install.Resolved, error) {
	w.t.Helper()
	src, err := install.Parse(input)
	if err != nil {
		w.t.Fatalf("Parse(%q): %v", input, err)
	}
	return w.resolver().Resolve(context.Background(), src)
}

func subtreeSHA(t *testing.T, tree treehash.Tree, dir string) string {
	t.Helper()
	if dir == "" {
		return tree.SHA
	}
	for _, n := range tree.Nodes {
		if n.Path == dir && n.IsTree() {
			return n.SHA
		}
	}
	t.Fatalf("no tree %s", dir)
	return ""
}

var catalog = map[string]string{
	"README.md":                     "# Catalog\n",
	"skills/alpha/SKILL.md":         skillMD("alpha", "Alpha."),
	"skills/alpha/notes.md":         "notes\n",
	"skills/beta/SKILL.md":          skillMD("Beta Skill", "Beta."),
	"skills/beta/scripts/run.sh":    "#!/bin/sh\n",
	"skills/beta/examples/SKILL.md": skillMD("beta-example", "Nested in beta, not a skill of its own."),
}

func TestResolveFindsTheOnlySkillAtTheDefaultBranchHead(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/one", "main", map[string]string{"README.md": "x\n", "skills/only/SKILL.md": skillMD("only", "Only.")})
	head, tree := w.publish("acme/one", "main", map[string]string{
		"README.md": "x\n", "skills/only/SKILL.md": skillMD("only", "Only, second."), "skills/only/a.md": "a\n",
	})

	got, err := w.resolve("acme/one")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := install.Resolved{
		Repo: "acme/one", Commit: head, Folder: "skills/only", SkillPath: "skills/only/SKILL.md",
		FolderSHA: subtreeSHA(t, tree, "skills/only"), Name: "only", Key: "only",
		SourceURL: "https://github.com/acme/one.git",
	}
	if got.Repo != want.Repo || got.Ref != "" || got.Commit != want.Commit || got.Folder != want.Folder ||
		got.SkillPath != want.SkillPath || got.FolderSHA != want.FolderSHA || got.Name != want.Name ||
		got.Key != want.Key || got.SourceURL != want.SourceURL {
		t.Errorf("Resolve = %+v\nwant      %+v", got, want)
	}
	var paths []string
	for _, n := range got.Nodes {
		paths = append(paths, n.Path)
	}
	if !slices.Equal(paths, []string{"SKILL.md", "a.md"}) {
		t.Errorf("nodes = %v, want the folder's files relative to it", paths)
	}
}

func TestResolveRefusesSeveralSkillsWithoutASelection(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/catalog", "main", catalog)

	_, err := w.resolve("acme/catalog")

	var ae *install.AmbiguousSkillError
	if !errors.Is(err, install.ErrAmbiguousSkill) || !errors.As(err, &ae) {
		t.Fatalf("err = %v, want an AmbiguousSkillError", err)
	}
	if !slices.Equal(ae.Folders, []string{"skills/alpha", "skills/beta"}) {
		t.Errorf("folders = %v, want skills/alpha and skills/beta", ae.Folders)
	}
	wantChoices := []install.SkillChoice{
		{Name: "alpha", Folder: "skills/alpha", Source: "acme/catalog/skills/alpha"},
		{Name: "Beta Skill", Folder: "skills/beta", Source: "acme/catalog/skills/beta"},
	}
	if !slices.Equal(ae.Choices, wantChoices) {
		t.Errorf("choices = %+v, want %+v", ae.Choices, wantChoices)
	}
}

func TestResolveAmbiguousChoicesWithRefAndRoot(t *testing.T) {
	t.Run("pinned ref with subpath", func(t *testing.T) {
		w := newWorld(t)
		w.publish("acme/catalog", "main", catalog)

		_, err := w.resolve("https://github.com/acme/catalog/tree/main")
		var ae *install.AmbiguousSkillError
		if !errors.As(err, &ae) {
			t.Fatalf("err = %v, want *AmbiguousSkillError", err)
		}
		want := []install.SkillChoice{
			{Name: "alpha", Folder: "skills/alpha", Source: "https://github.com/acme/catalog/tree/main/skills/alpha"},
			{Name: "Beta Skill", Folder: "skills/beta", Source: "https://github.com/acme/catalog/tree/main/skills/beta"},
		}
		if !slices.Equal(ae.Choices, want) {
			t.Errorf("choices = %+v, want %+v", ae.Choices, want)
		}
	})

	t.Run("subpath without ref", func(t *testing.T) {
		w := newWorld(t)
		w.publish("acme/mono", "main", map[string]string{
			"packages/tools/skills/a/SKILL.md": skillMD("alpha", "Alpha."),
			"packages/tools/skills/b/SKILL.md": skillMD("beta", "Beta."),
		})

		_, err := w.resolve("acme/mono/packages/tools")
		var ae *install.AmbiguousSkillError
		if !errors.As(err, &ae) {
			t.Fatalf("err = %v, want *AmbiguousSkillError", err)
		}
		want := []install.SkillChoice{
			{Name: "alpha", Folder: "packages/tools/skills/a", Source: "acme/mono/packages/tools/skills/a"},
			{Name: "beta", Folder: "packages/tools/skills/b", Source: "acme/mono/packages/tools/skills/b"},
		}
		if !slices.Equal(ae.Choices, want) {
			t.Errorf("choices = %+v, want %+v", ae.Choices, want)
		}
	})
}

func TestResolveSelectsASkillByItsFrontmatterName(t *testing.T) {
	tests := []struct{ in, folder, name, key string }{
		{"acme/catalog@alpha", "skills/alpha", "alpha", "alpha"},
		{"acme/catalog@Beta Skill", "skills/beta", "Beta Skill", "beta-skill"},
		{"acme/catalog@BETA SKILL", "skills/beta", "Beta Skill", "beta-skill"},
		{"acme/catalog@beta_skill", "skills/beta", "Beta Skill", "beta-skill"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			w := newWorld(t)
			w.publish("acme/catalog", "main", catalog)

			got, err := w.resolve(tt.in)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Folder != tt.folder || got.Name != tt.name || got.Key != tt.key {
				t.Errorf("Resolve = folder %q name %q key %q, want %q %q %q", got.Folder, got.Name, got.Key, tt.folder, tt.name, tt.key)
			}
		})
	}
}

func TestResolveRefusesANameNoSkillHas(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/catalog", "main", catalog)

	// The folder name is not the skill's name: npx skills matches the
	// frontmatter (harnesses-v2.md section 4).
	for _, in := range []string{"acme/catalog@gamma", "acme/catalog@beta"} {
		if _, err := w.resolve(in); !errors.Is(err, install.ErrNoSkill) {
			t.Errorf("%s: err = %v, want ErrNoSkill", in, err)
		}
	}
}

func TestResolveRefusesTwoSkillsWithTheSelectedName(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/twins", "main", map[string]string{
		"skills/a/SKILL.md": skillMD("twin", "A."),
		"skills/b/SKILL.md": skillMD("twin", "B."),
	})

	_, err := w.resolve("acme/twins@twin")

	var ae *install.AmbiguousSkillError
	if !errors.As(err, &ae) || !slices.Equal(ae.Folders, []string{"skills/a", "skills/b"}) {
		t.Fatalf("err = %v, want an AmbiguousSkillError for skills/a and skills/b", err)
	}
}

func TestResolveRefusesARepositoryWithoutSkills(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/empty", "main", map[string]string{"README.md": "x\n", "docs/skill.txt": "no\n"})

	if _, err := w.resolve("acme/empty"); !errors.Is(err, install.ErrNoSkill) {
		t.Fatalf("err = %v, want ErrNoSkill", err)
	}
}

func TestResolveSkipsSkillsNpxSkips(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/odd", "main", map[string]string{
		"skills/nodesc/SKILL.md":   "---\nname: nodesc\n---\n",
		"skills/badyaml/SKILL.md":  "---\nname: [unclosed\ndescription: x\n---\n",
		"skills/noheader/SKILL.md": "# just text\n",
		"skills/internal/SKILL.md": "---\nname: internal\ndescription: x\nmetadata:\n  internal: true\n---\n",
		"skills/numname/SKILL.md":  "---\nname: 12\ndescription: x\n---\n",
		"skills/good/SKILL.md":     skillMD("good", "Good."),
	})

	got, err := w.resolve("acme/odd")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Folder != "skills/good" {
		t.Errorf("folder = %q, want skills/good: every other candidate is invalid or internal", got.Folder)
	}
}

func TestResolveTakesTheSubpathSkillAlone(t *testing.T) {
	w := newWorld(t)
	_, tree := w.publish("acme/catalog", "main", catalog)

	got, err := w.resolve("acme/catalog/skills/beta")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Folder != "skills/beta" || got.SkillPath != "skills/beta/SKILL.md" || got.Name != "Beta Skill" ||
		got.FolderSHA != subtreeSHA(t, tree, "skills/beta") {
		t.Errorf("Resolve = %+v, want skills/beta", got)
	}
}

func TestResolveSearchesBelowTheSubpath(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/catalog", "main", catalog)

	_, err := w.resolve("acme/catalog/skills")
	var ae *install.AmbiguousSkillError
	if !errors.As(err, &ae) || !slices.Equal(ae.Folders, []string{"skills/alpha", "skills/beta"}) {
		t.Fatalf("err = %v, want both skills, relative to the repository root", err)
	}

	got, err := w.resolve("https://github.com/acme/catalog/tree/main/skills")
	if !errors.As(err, &ae) {
		t.Fatalf("tree URL: Resolve = %+v, %v; want the same ambiguity", got, err)
	}
}

func TestResolveTakesARootSkillAlone(t *testing.T) {
	w := newWorld(t)
	_, tree := w.publish("acme/single", "main", map[string]string{
		"SKILL.md":                 skillMD("single", "Root."),
		"nested/other/SKILL.md":    skillMD("other", "Ignored: the root is a skill."),
		"skills/another/SKILL.md":  skillMD("another", "Ignored too."),
		"references/background.md": "x\n",
	})

	got, err := w.resolve("acme/single")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Folder != "" || got.SkillPath != "SKILL.md" || got.Name != "single" || got.FolderSHA != tree.SHA {
		t.Errorf("Resolve = %+v, want the root skill", got)
	}
}

func TestResolveFollowsNpxDiscoveryContainers(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"curated", map[string]string{
			"skills/.curated/x/SKILL.md": skillMD("x", "x"), "skills/.experimental/y/SKILL.md": skillMD("y", "y"),
		}, []string{"skills/.curated/x", "skills/.experimental/y"}},
		{"grouped", map[string]string{
			"skills/group/x/SKILL.md": skillMD("x", "x"), "skills/group/sub/y/SKILL.md": skillMD("y", "y"),
		}, []string{"skills/group/sub/y", "skills/group/x"}},
		{"agent dir", map[string]string{
			".claude/skills/x/SKILL.md": skillMD("x", "x"), "top/SKILL.md": skillMD("top", "top"),
		}, []string{".claude/skills/x", "top"}},
		{"nested in a skill", map[string]string{
			"skills/x/SKILL.md": skillMD("x", "x"), "skills/x/inner/deep/SKILL.md": skillMD("deep", "deep"),
			"skills/y/SKILL.md": skillMD("y", "y"),
		}, []string{"skills/x", "skills/y"}},
		{"skills folder inside a skill", map[string]string{
			"skills/x/SKILL.md": skillMD("x", "x"), "skills/x/skills/inner/SKILL.md": skillMD("inner", "inner"),
			"skills/y/SKILL.md": skillMD("y", "y"),
		}, []string{"skills/x", "skills/y"}},
		{"skipped dirs", map[string]string{
			"skills/node_modules/pkg/x/SKILL.md": skillMD("x", "x"), "skills/y/SKILL.md": skillMD("y", "y"),
			"skills/z/SKILL.md": skillMD("z", "z"),
		}, []string{"skills/y", "skills/z"}},
		{"fallback by depth", map[string]string{
			"a/b/c/d/e/SKILL.md": skillMD("five", "five"), "a/b/c/d/e/f/SKILL.md": skillMD("six", "six"),
			"q/r/s/t/u/SKILL.md": skillMD("other", "other"),
		}, []string{"a/b/c/d/e", "q/r/s/t/u"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.publish("acme/layout", "main", tt.files)

			_, err := w.resolve("acme/layout")

			var ae *install.AmbiguousSkillError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want an AmbiguousSkillError", err)
			}
			if !slices.Equal(ae.Folders, tt.want) {
				t.Errorf("folders = %v, want %v", ae.Folders, tt.want)
			}
		})
	}
}

func TestResolvePinsTheRefTheUserGave(t *testing.T) {
	w := newWorld(t)
	old, oldTree := w.publish("acme/one", "main", map[string]string{"skills/only/SKILL.md": skillMD("only", "Old.")})
	w.gh.Tag("acme/one", "v1.0.0", old)
	w.publish("acme/one", "main", map[string]string{"skills/only/SKILL.md": skillMD("only", "New.")})

	got, err := w.resolve("acme/one@v1.0.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Ref != "v1.0.0" || got.Commit != old || got.FolderSHA != subtreeSHA(t, oldTree, "skills/only") {
		t.Errorf("Resolve = ref %q commit %s, want v1.0.0 at %s", got.Ref, got.Commit, old)
	}
}

func TestResolveSplitsATreeURLWhoseRefHoldsSlashes(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/catalog", "main", catalog)
	feature, tree := w.publish("acme/catalog", "feature/x", catalog)

	got, err := w.resolve("https://github.com/acme/catalog/tree/feature/x/skills/alpha")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Ref != "feature/x" || got.Commit != feature || got.Folder != "skills/alpha" ||
		got.FolderSHA != subtreeSHA(t, tree, "skills/alpha") {
		t.Errorf("Resolve = %+v, want feature/x at skills/alpha", got)
	}
}

func TestResolveATreeURLWithoutAPath(t *testing.T) {
	w := newWorld(t)
	main, _ := w.publish("acme/one", "main", map[string]string{"skills/only/SKILL.md": skillMD("only", "Only.")})

	got, err := w.resolve("https://github.com/acme/one/tree/main")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Ref != "main" || got.Commit != main || got.Folder != "skills/only" {
		t.Errorf("Resolve = %+v, want main at skills/only", got)
	}
}

func TestResolvePropagatesGitHubFailures(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/one", "main", map[string]string{"skills/only/SKILL.md": skillMD("only", "Only.")})
	tests := []struct {
		in   string
		want error
	}{
		{"acme/missing", ports.ErrRepoNotFound},
		{"acme/one/skills/gone", ports.ErrRefNotFound},
		{"acme/one@v9.9.9", ports.ErrRefNotFound},
		{"https://github.com/acme/one/tree/nope/skills/only", ports.ErrRefNotFound},
	}
	for _, tt := range tests {
		if _, err := w.resolve(tt.in); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.in, err, tt.want)
		}
	}
}

// lyingBlobs serves content that does not hash to the requested SHA.
type lyingBlobs struct{ *fakes.GitHub }

func (l lyingBlobs) Blob(ctx context.Context, r, sha string) ([]byte, error) {
	content, err := l.GitHub.Blob(ctx, r, sha)
	return append(content, "\ninjected: true\n"...), err
}

func TestResolveNeverTrustsASkillMDThatDoesNotMatchItsSHA(t *testing.T) {
	w := newWorld(t)
	w.publish("acme/one", "main", map[string]string{"skills/only/SKILL.md": skillMD("only", "Only.")})
	src, _ := install.Parse("acme/one")

	_, err := install.Resolver{GitHub: lyingBlobs{w.gh}}.Resolve(context.Background(), src)

	if !errors.Is(err, staging.ErrBlobMismatch) {
		t.Fatalf("err = %v, want ErrBlobMismatch", err)
	}
}

func TestStoreKeyIsSanitizeNameOfNpxSkills(t *testing.T) {
	tests := []struct{ name, want string }{
		{"alpha", "alpha"},
		{"Beta Skill", "beta-skill"},
		{"a__b.c", "a__b.c"},
		{"..x..", "x"},
		{"-x-", "x"},
		{"Ñandú", "and"},
		{"a / b", "a-b"},
		{"", "unnamed-skill"},
		{"***", "unnamed-skill"},
		{strings.Repeat("a", 300), strings.Repeat("a", 255)},
	}
	for _, tt := range tests {
		if got := install.StoreKey(tt.name); got != tt.want {
			t.Errorf("StoreKey(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestResolveFollowsTheRepositorysDefaultBranch(t *testing.T) {
	w := newWorld(t)
	trunk, _ := w.publish("acme/branches", "trunk", map[string]string{"skills/only/SKILL.md": skillMD("only", "Trunk.")})
	w.publish("acme/branches", "main", map[string]string{"skills/only/SKILL.md": skillMD("only", "Main.")})

	got, err := w.resolve("acme/branches")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Commit != trunk || got.Ref != "" {
		t.Errorf("Resolve = commit %s ref %q, want the trunk head %s and no ref", got.Commit, got.Ref, trunk)
	}
}
