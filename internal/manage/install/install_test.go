package install_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
)

// tool is a skill whose frontmatter name is its store key.
var tool = map[string]string{
	"README.md":                  "# Tools\n",
	"skills/tool/SKILL.md":       skillMD("tool", "A tool."),
	"skills/tool/notes.md":       "Notes\n",
	"skills/tool/scripts/run.sh": "#!/bin/sh\necho run\n",
}

// targets reaches three harnesses through two link directories:
// Antigravity and Antigravity CLI share theirs.
var targets = []string{"claude-code", "antigravity-cli", "antigravity"}

func TestInstallPublishesTheReviewedSkillAndUndoRestoresTheHome(t *testing.T) {
	for _, machine := range []bool{true, false} {
		name := map[bool]string{true: "with the lock of this machine", false: "with no lock"}[machine]
		t.Run(name, func(t *testing.T) {
			s := newSite(t, machine)
			commit, tree := s.publish("acme/tool", "main", tool)
			folderSHA := subtreeSHA(t, tree, "skills/tool")
			rev := s.review("acme/tool")
			home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

			pr, err := s.planner().Install(rev, targets)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}

			// Planning writes nothing.
			sameHome(t, "planning", home0, s.everything())
			if !bytes.Equal(s.lockBytes(), lock0) || exists(t, engine.RecordFile(s.p)) {
				t.Fatal("planning changed the lock or left a record")
			}

			store := filepath.Join(s.p.StoreDir(), "tool")
			claude := s.home.Path(".claude/skills/tool")
			gemini := s.home.Path(".gemini/config/skills/tool")
			entry := npxEntry("acme/tool", commit, "skills/tool/SKILL.md", folderSHA)
			wantPreview := install.Preview{
				PlanID: pr.Plan.ID, Key: "tool", Name: "tool",
				Source: "acme/tool", SourceURL: "https://github.com/acme/tool.git", Commit: commit,
				SkillPath: "skills/tool/SKILL.md", TreeSHA: folderSHA,
				StagedPath: rev.StagedPath, StorePath: store,
				Targets: []string{"antigravity", "antigravity-cli", "claude-code"},
				Links: []install.LinkChange{
					{Path: claude, Target: "../../.agents/skills/tool", Harnesses: []string{"claude-code"}},
					{Path: gemini, Target: "../../../.agents/skills/tool", Harnesses: []string{"antigravity", "antigravity-cli"}},
				},
				LockEntry: []byte(entry), LockCreated: !machine, FollowsDefaultBranch: true,
			}
			slices.SortFunc(wantPreview.Links, func(a, b install.LinkChange) int { return strings.Compare(a.Path, b.Path) })
			if !reflect.DeepEqual(pr.Preview, wantPreview) {
				t.Errorf("Preview =\n%+v\nwant\n%+v", pr.Preview, wantPreview)
			}
			if pr.Plan.Action != install.ActionInstall || pr.Plan.ReviewedTreeSHA != folderSHA ||
				!reflect.DeepEqual(pr.Plan.Targets, wantPreview.Targets) || pr.Plan.Canonical != store {
				t.Errorf("plan meta = %+v", pr.Plan.Meta)
			}

			out, err := s.engine().Apply(t.Context(), pr.Plan)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			// The reviewed tree is in the store, a real folder; the staging
			// is gone.
			if info, err := os.Lstat(store); err != nil || !info.IsDir() {
				t.Fatalf("store folder: %v, %v", info, err)
			}
			if got, err := treehash.Dir(store); err != nil || got.SHA != folderSHA {
				t.Errorf("store tree = %s, %v; want the reviewed %s", got.SHA, err, folderSHA)
			}
			if exists(t, rev.StagedPath) {
				t.Error("the staged copy is still there")
			}
			// A relative link in each link directory, as npx skills writes it.
			for _, l := range wantPreview.Links {
				if got, err := os.Readlink(l.Path); err != nil || got != l.Target {
					t.Errorf("link %s = %q, %v; want %q", l.Path, got, err, l.Target)
				}
				if resolved, err := filepath.EvalSymlinks(l.Path); err != nil || resolved != mustEval(t, store) {
					t.Errorf("link %s resolves to %s, %v", l.Path, resolved, err)
				}
			}

			// The lock: the entry npx skills writes, last; every byte a fixed
			// point of JSON.stringify(..., null, 2).
			lock := s.lockBytes()
			if got := jsStringify(t, lock); got != string(lock) {
				t.Errorf("the lock is not what JSON.stringify writes:\n%s\nwant\n%s", lock, got)
			}
			var want string
			if machine {
				closing := "\n    }\n  },\n  \"dismissed\""
				if !strings.Contains(string(lock0), closing) {
					t.Fatalf("fixture lock without %q", closing)
				}
				want = strings.Replace(string(lock0), closing,
					"\n    },\n    \"tool\": "+indented(entry)+"\n  },\n  \"dismissed\"", 1)
			} else {
				want = "{\n  \"version\": 3,\n  \"skills\": {\n    \"tool\": " + indented(entry) +
					"\n  },\n  \"dismissed\": {}\n}"
			}
			if string(lock) != want {
				t.Errorf("lock after install:\n%s\nwant:\n%s", lock, want)
			}
			read, err := skillslock.New(s.p, s.files()).Read()
			must(t, err)
			i := slices.IndexFunc(read.Entries, func(e domain.LockEntry) bool { return e.Name == "tool" })
			if i != len(read.Entries)-1 {
				t.Fatalf("skillslock reads the entry at %d of %d", i, len(read.Entries))
			}
			if e := read.Entries[i]; e.Source != "acme/tool" || e.SourceType != "github" ||
				e.SkillPath != "skills/tool/SKILL.md" || e.SkillFolderHash != folderSHA || e.Ref != "" {
				t.Errorf("skillslock reads %+v", e)
			}
			if strings.Contains(string(lock), `"ref"`) {
				t.Error("the install wrote a ref")
			}

			// The effect: the skill is managed, with its targets.
			if got := s.state().Managed["tool"]; !reflect.DeepEqual(got, domain.ManagedSkill{Key: "tool", Targets: wantPreview.Targets}) {
				t.Errorf("Managed[tool] = %+v", got)
			}

			// The graph rebuilt from the disk sees the skill in each target.
			g := s.graph()
			sk := findSkill(t, g, store)
			if sk.LockKey != "tool" {
				t.Errorf("graph lock key = %q", sk.LockKey)
			}
			for _, h := range targets {
				l := claude
				if h != "claude-code" {
					l = gemini
				}
				if !slices.ContainsFunc(sk.Visibility, func(v graph.Sighting) bool { return v.Harness == h && v.Entry == l }) {
					t.Errorf("the graph does not see the skill in %s through %s: %+v", h, l, sk.Visibility)
				}
			}

			// Undo leaves the home, the lock and the state as they were.
			if _, err := s.engine().Undo(t.Context(), out.SnapshotID); err != nil {
				t.Fatalf("Undo: %v", err)
			}
			sameHome(t, "install then undo", home0, s.everything())
			if got := s.lockBytes(); !bytes.Equal(got, lock0) {
				t.Errorf("lock after undo:\n%s\nwant:\n%s", got, lock0)
			}
			if got := s.state(); !reflect.DeepEqual(got.Managed, state0.Managed) || !reflect.DeepEqual(got.Parked, state0.Parked) {
				t.Errorf("state after undo: managed %v parked %v", got.Managed, got.Parked)
			}
		})
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(p)
	must(t, err)
	return out
}

func findSkill(t *testing.T, g graph.Graph, canonical string) graph.Skill {
	t.Helper()
	for _, s := range g.Skills {
		if s.Canonical == canonical {
			return s
		}
	}
	t.Fatalf("the graph has no skill at %s", canonical)
	return graph.Skill{}
}

// TestTheLinkIsTheOneLinkCreates: install and toggle's Link write the same
// link for the same key and harness.
func TestTheLinkIsTheOneLinkCreates(t *testing.T) {
	s := newSite(t, false)
	s.publish("acme/tool", "main", tool)
	pr, err := s.planner().Install(s.review("acme/tool"), []string{"claude-code"})
	must(t, err)
	h := s.harness[slices.IndexFunc(s.harness, func(h data.Harness) bool { return h.ID == "claude-code" })]
	dir, err := toggle.LinkDir(s.p, h)
	must(t, err)
	path, target, err := toggle.StoreLink(toggle.Resolver{Paths: s.p, Dirs: fsadapter.NewScanner(s.p.Guard())}, dir, "tool")
	must(t, err)
	if want := []install.LinkChange{{Path: path, Target: target, Harnesses: []string{"claude-code"}}}; !reflect.DeepEqual(pr.Preview.Links, want) {
		t.Errorf("links = %+v, want %+v", pr.Preview.Links, want)
	}
}

// TestSkillFolderHashIsTheGitTreeOfTheFolder: npx skills records
// getSkillFolderHashFromTree (remove.mjs:4668-4675, called at :6671): the
// SHA of the folder's tree entry in the GitHub tree, or the root tree for a
// skill at the root. That is the git tree hash of the folder on disk.
func TestSkillFolderHashIsTheGitTreeOfTheFolder(t *testing.T) {
	cases := []struct {
		name, input, folder, skillPath string
		files                          map[string]string
	}{
		{"a folder below skills/", "acme/tool", "skills/tool", "skills/tool/SKILL.md", tool},
		{"the repository root", "acme/root", "", "SKILL.md", map[string]string{
			"SKILL.md": skillMD("root", "At the root."), "lib/x.md": "x\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, false)
			_, tree := s.publish(tc.input, "main", tc.files)
			pr, err := s.planner().Install(s.review(tc.input), nil)
			must(t, err)
			want := subtreeSHA(t, tree, tc.folder)
			if !strings.Contains(string(pr.Preview.LockEntry), `"skillFolderHash": "`+want+`"`) ||
				!strings.Contains(string(pr.Preview.LockEntry), `"skillPath": "`+tc.skillPath+`"`) {
				t.Errorf("entry = %s; want skillFolderHash %s and skillPath %s", pr.Preview.LockEntry, want, tc.skillPath)
			}
			if _, err := s.engine().Apply(t.Context(), pr.Plan); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, err := treehash.Dir(pr.Preview.StorePath); err != nil || got.SHA != want {
				t.Errorf("installed tree = %s, %v; want %s", got.SHA, err, want)
			}
		})
	}
}

// TestApplyRefusesAStagedCopyThatChangedAfterTheReview: the approval binds
// the exact reviewed tree.
func TestApplyRefusesAStagedCopyThatChangedAfterTheReview(t *testing.T) {
	s := newSite(t, true)
	s.publish("acme/tool", "main", tool)
	rev := s.review("acme/tool")
	pr, err := s.planner().Install(rev, targets)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(rev.StagedPath, "notes.md"), []byte("Changed\n"), 0o644))
	home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

	_, err = s.engine().Apply(t.Context(), pr.Plan)
	if !errors.Is(err, engine.ErrPrecondition) {
		t.Fatalf("Apply error = %v, want ErrPrecondition", err)
	}
	sameHome(t, "refused apply", home0, s.everything())
	if !bytes.Equal(s.lockBytes(), lock0) || !reflect.DeepEqual(s.state(), state0) {
		t.Error("a refused apply changed the lock or the state")
	}
	if exists(t, engine.RecordFile(s.p)) {
		t.Error("a refused apply left a record")
	}
}

// TestACrashDuringInstallIsRecovered: the process dies after each of the
// plan's writes; a later recover leaves the home, the lock and the state as
// they were before.
func TestACrashDuringInstallIsRecovered(t *testing.T) {
	// The move, then the two links; the lock write and the close are crashed
	// through the dead flag the last link sets.
	for at := 1; at <= 3; at++ {
		for _, machine := range []bool{true, false} {
			t.Run(map[bool]string{true: "lock", false: "no lock"}[machine]+"/"+string(rune('0'+at)), func(t *testing.T) {
				s := newSite(t, machine)
				s.publish("acme/tool", "main", tool)
				pr, err := s.planner().Install(s.review("acme/tool"), targets)
				must(t, err)
				home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

				c := &crash{at: at}
				s.wrap = c.wrap
				if _, err := s.engine().Apply(t.Context(), pr.Plan); !errors.Is(err, errCrash) {
					t.Fatalf("Apply() error = %v, want the crash", err)
				}
				s.wrap = nil
				if !c.dead {
					t.Fatal("the crash point was never reached")
				}

				res, err := s.clean().Recover(t.Context())
				if err != nil || !res.Found || !res.RolledBack {
					t.Fatalf("Recover() = %+v, %v; want a rollback", res, err)
				}
				sameHome(t, "crash then recover", home0, s.everything())
				if !bytes.Equal(s.lockBytes(), lock0) {
					t.Errorf("lock after recover:\n%s", s.lockBytes())
				}
				if got := s.state(); !reflect.DeepEqual(got.Managed, state0.Managed) {
					t.Errorf("managed after recover: %v", got.Managed)
				}
				if exists(t, engine.RecordFile(s.p)) {
					t.Error("the record is still there")
				}
			})
		}
	}
}
