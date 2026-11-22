package install_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/data"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/shared/domain"
)

// TestInstallRefusesWithoutWriting: every refusal is a typed error and
// leaves the home, the staging, the lock and the state as they were.
func TestInstallRefusesWithoutWriting(t *testing.T) {
	cases := []struct {
		name    string
		machine bool
		input   string
		files   map[string]string
		// setup changes the site after the review; it returns the review
		// and the targets to install with.
		setup func(s *site, rev install.Review) (install.Review, []string)
		want  error
		check func(t *testing.T, err error)
	}{
		{name: "a folder with the key in the store", setup: func(s *site, rev install.Review) (install.Review, []string) {
			s.home.WriteFile(".agents/skills/tool/SKILL.md", skillMD("tool", "Mine."))
			return rev, targets
		}, want: install.ErrKeyExists, check: func(t *testing.T, err error) {
			var ke *install.KeyExistsError
			if !errors.As(err, &ke) || ke.Key != "tool" || filepath.Base(ke.Store) != "tool" || ke.Parked != "" {
				t.Errorf("KeyExistsError = %+v", ke)
			}
		}},
		{name: "a link with the key in the store", setup: func(s *site, rev install.Review) (install.Review, []string) {
			s.home.Symlink(".agents/skills/tool", "elsewhere")
			return rev, targets
		}, want: install.ErrKeyExists},
		{name: "a parked folder with the key", setup: func(s *site, rev install.Review) (install.Review, []string) {
			s.home.WriteFile(".agents/.skillhub/disabled/tool/SKILL.md", skillMD("tool", "Parked."))
			return rev, targets
		}, want: install.ErrKeyExists, check: func(t *testing.T, err error) {
			var ke *install.KeyExistsError
			if !errors.As(err, &ke) || filepath.Base(ke.Parked) != "tool" || ke.Store != "" {
				t.Errorf("KeyExistsError = %+v", ke)
			}
		}},
		{name: "a lock entry with the key", setup: func(s *site, rev install.Review) (install.Review, []string) {
			writeLock(s.t, s.p, []domain.LockEntry{{Name: "tool", Source: "other/tool", SourceType: "github",
				SkillPath: "SKILL.md", SkillFolderHash: "abc"}})
			return rev, targets
		}, want: install.ErrKeyExists, check: func(t *testing.T, err error) {
			var ke *install.KeyExistsError
			if !errors.As(err, &ke) || !ke.LockEntry || ke.Store != "" {
				t.Errorf("KeyExistsError = %+v", ke)
			}
		}},
		{name: "a managed record with the key", setup: func(s *site, rev install.Review) (install.Review, []string) {
			s.saveState(domain.PersistedState{Managed: map[string]domain.ManagedSkill{"tool": {Key: "tool", Targets: []string{}}}})
			return rev, targets
		}, want: install.ErrKeyExists},
		{name: "a parked record with the key", setup: func(s *site, rev install.Review) (install.Review, []string) {
			s.saveState(domain.PersistedState{Parked: map[string]domain.ParkedSkill{"tool": {Key: "tool"}}})
			return rev, targets
		}, want: install.ErrKeyExists},
		{name: "a name that is not its store key", input: "acme/beta",
			files: map[string]string{"skills/beta/SKILL.md": skillMD("Beta Skill", "Beta.")},
			want:  install.ErrNameMismatch},
		{name: "a ref pinned with @", input: "acme/tool@v1.0.0", setup: nil, want: install.ErrPinnedRef},
		{name: "a ref in a tree URL", input: "https://github.com/acme/tool/tree/main/skills/tool", want: install.ErrPinnedRef},
		{name: "an unknown target", setup: func(s *site, rev install.Review) (install.Review, []string) {
			return rev, []string{"claude-code", "no-such-harness"}
		}, want: install.ErrUnknownHarness},
		{name: "a target without a link directory", setup: func(s *site, rev install.Review) (install.Review, []string) {
			i := slices.IndexFunc(s.harness, func(h data.Harness) bool { return h.LinkDir == "" })
			return rev, []string{"claude-code", s.harness[i].ID}
		}, want: install.ErrNotAdministrable},
		{name: "a target whose link directory is missing", setup: func(s *site, rev install.Review) (install.Review, []string) {
			must(s.t, os.Remove(s.home.Path(".gemini/config/skills")))
			return rev, targets
		}, want: install.ErrMissingDir},
		{name: "a store that is not a directory", setup: func(s *site, rev install.Review) (install.Review, []string) {
			must(s.t, os.Remove(s.p.StoreDir()))
			s.home.WriteFile(".agents/skills", "not a folder\n")
			return rev, targets
		}, want: install.ErrMissingDir},
		{name: "a link path that is taken", setup: func(s *site, rev install.Review) (install.Review, []string) {
			s.home.Symlink(".claude/skills/tool", "../../elsewhere")
			return rev, targets
		}, want: install.ErrPathTaken},
		{name: "a staged path outside the staging root", setup: func(s *site, rev install.Review) (install.Review, []string) {
			s.home.WriteFile("decoy/SKILL.md", skillMD("tool", "Not reviewed."))
			rev.StagedPath = s.home.Path("decoy")
			return rev, targets
		}, want: install.ErrInvalidReview},
		{name: "a tree that is not the resolved folder's", setup: func(s *site, rev install.Review) (install.Review, []string) {
			rev.Resolved.FolderSHA = "0000000000000000000000000000000000000000"
			return rev, targets
		}, want: install.ErrInvalidReview},
		{name: "a staged copy that changed since the review", setup: func(s *site, rev install.Review) (install.Review, []string) {
			must(s.t, os.WriteFile(filepath.Join(rev.StagedPath, "notes.md"), []byte("Changed\n"), 0o644))
			return rev, targets
		}, want: install.ErrStagingChanged},
		{name: "a staged copy that is gone", setup: func(s *site, rev install.Review) (install.Review, []string) {
			must(s.t, os.RemoveAll(rev.StagedPath))
			return rev, targets
		}, want: install.ErrStagingChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, tc.machine)
			files, input := tool, "acme/tool"
			if tc.files != nil {
				files = tc.files
			}
			if tc.input != "" {
				input = tc.input
			}
			repo := "acme/tool"
			if input == "acme/beta" {
				repo = "acme/beta"
			}
			commit, _ := s.publish(repo, "main", files)
			s.gh.Tag(repo, "v1.0.0", commit)
			rev := s.review(input)
			tg := targets
			if tc.setup != nil {
				rev, tg = tc.setup(s, rev)
			}
			home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

			pr, err := s.planner().Install(rev, tg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Install() = %+v, %v; want %v", pr.Preview, err, tc.want)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
			sameHome(t, "refused install", home0, s.everything())
			if !bytes.Equal(s.lockBytes(), lock0) || !reflect.DeepEqual(s.state(), state0) {
				t.Error("a refused install changed the lock or the state")
			}
			if exists(t, engine.RecordFile(s.p)) {
				t.Error("a refused install left a record")
			}
		})
	}
}
