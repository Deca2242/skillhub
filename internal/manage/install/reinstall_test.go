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

	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/shared/domain"
)

// ghostLock is a lock whose entry "tool" has no folder anywhere, between two
// other entries; extra is spliced into tool's entry.
func ghostLock(source, extra string) string {
	return `{
  "version": 3,
  "skills": {
    "before": {
      "source": "owner/before",
      "sourceType": "github",
      "skillPath": "skills/before/SKILL.md",
      "skillFolderHash": "1111111111111111111111111111111111111111"
    },
    "tool": {
      "source": "` + source + `",
      "sourceType": "github",` + extra + `
      "skillPath": "skills/tool/SKILL.md",
      "skillFolderHash": "2222222222222222222222222222222222222222"
    },
    "after": {
      "source": "owner/after",
      "sourceType": "github",
      "skillPath": "skills/after/SKILL.md",
      "skillFolderHash": "3333333333333333333333333333333333333333"
    }
  },
  "dismissed": {}
}`
}

// probeFields are the fields of the verifier's probe (B1): an install date
// to keep and a key skillhub never writes.
const probeFields = `
      "installedAt": "2025-01-01T00:00:00.000Z",
      "updatedAt": "2025-01-01T00:00:00.000Z",
      "x-custom": 1,`

// TestReinstallReplacesTheEntryInItsPlace: for a lock entry whose folder is
// gone, Reinstall plans the whole install and replaces the entry in its
// position, as npx skills' addSkillToLock does: it keeps installedAt and
// every key skillhub does not write, updates source, sourceType, sourceUrl,
// commit, skillPath, skillFolderHash and updatedAt, and never adds a ref.
// Undo gives back the home, the lock byte for byte and the state.
func TestReinstallReplacesTheEntryInItsPlace(t *testing.T) {
	cases := []struct {
		name, extra string
		// entry is the new entry as the lock holds it, given the commit, the
		// folder hash and the time now.
		entry func(commit, folderSHA string) string
	}{
		{name: "an entry with installedAt and an unknown key", extra: probeFields,
			entry: func(commit, folderSHA string) string {
				return `{
      "source": "acme/tool",
      "sourceType": "github",
      "installedAt": "2025-01-01T00:00:00.000Z",
      "updatedAt": "` + nowISO + `",
      "x-custom": 1,
      "skillPath": "skills/tool/SKILL.md",
      "skillFolderHash": "` + folderSHA + `",
      "sourceUrl": "https://github.com/acme/tool.git",
      "commit": "` + commit + `"
    }`
			}},
		{name: "an entry without dates", extra: "",
			entry: func(commit, folderSHA string) string {
				return `{
      "source": "acme/tool",
      "sourceType": "github",
      "skillPath": "skills/tool/SKILL.md",
      "skillFolderHash": "` + folderSHA + `",
      "sourceUrl": "https://github.com/acme/tool.git",
      "commit": "` + commit + `",
      "installedAt": "` + nowISO + `",
      "updatedAt": "` + nowISO + `"
    }`
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, false)
			old := ghostLock("acme/tool", tc.extra)
			must(t, os.WriteFile(s.p.SkillsLockFile(), []byte(old), 0o644))
			commit, tree := s.publish("acme/tool", "main", tool)
			folderSHA := subtreeSHA(t, tree, "skills/tool")
			rev := s.review("acme/tool")
			home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

			if _, err := s.planner().Install(rev, targets); !errors.Is(err, install.ErrKeyExists) {
				t.Fatalf("Install() error = %v, want ErrKeyExists: an install never replaces an entry", err)
			}
			pr, err := s.planner().Reinstall(rev, targets)
			if err != nil {
				t.Fatalf("Reinstall: %v", err)
			}
			sameHome(t, "planning", home0, s.everything())

			var kinds []engine.StepKind
			for _, st := range pr.Plan.Steps {
				kinds = append(kinds, st.Kind)
			}
			want := []engine.StepKind{engine.StepMove, engine.StepLink, engine.StepLink, engine.StepLockReplace}
			if !slices.Equal(kinds, want) {
				t.Errorf("steps = %v, want %v", kinds, want)
			}
			oldEntry := old[strings.Index(old, `"tool": `)+len(`"tool": `) : strings.Index(old, `,
    "after"`)]
			if string(pr.Preview.ReplacedEntry) != strings.ReplaceAll(oldEntry, "\n    ", "\n") {
				t.Errorf("ReplacedEntry = %s, want the old entry", pr.Preview.ReplacedEntry)
			}
			entry := tc.entry(commit, folderSHA)
			if string(pr.Preview.LockEntry) != strings.ReplaceAll(entry, "\n    ", "\n") ||
				pr.Preview.LockCreated || pr.Plan.Action != install.ActionReinstall {
				t.Errorf("preview entry = %s, created %t, action %s", pr.Preview.LockEntry, pr.Preview.LockCreated, pr.Plan.Action)
			}

			out, err := s.engine().Apply(t.Context(), pr.Plan)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			store := filepath.Join(s.p.StoreDir(), "tool")
			for _, l := range pr.Preview.Links {
				if resolved, err := filepath.EvalSymlinks(l.Path); err != nil || resolved != store {
					t.Errorf("link %s resolves to %q, %v", l.Path, resolved, err)
				}
			}
			lock := s.lockBytes()
			if wantLock := strings.Replace(old, oldEntry, entry, 1); string(lock) != wantLock {
				t.Errorf("lock after reinstall:\n%s\nwant:\n%s", lock, wantLock)
			}
			if got := jsStringify(t, lock); got != string(lock) {
				t.Errorf("the lock is not what JSON.stringify writes:\n%s", lock)
			}
			if got := s.state().Managed["tool"]; !reflect.DeepEqual(got, domain.ManagedSkill{Key: "tool", Targets: pr.Preview.Targets}) {
				t.Errorf("Managed[tool] = %+v", got)
			}

			if _, err := s.engine().Undo(t.Context(), out.SnapshotID); err != nil {
				t.Fatalf("Undo: %v", err)
			}
			sameHome(t, "reinstall then undo", home0, s.everything())
			if !bytes.Equal(s.lockBytes(), lock0) {
				t.Errorf("lock after undo:\n%s\nwant:\n%s", s.lockBytes(), lock0)
			}
			if got := s.state(); !reflect.DeepEqual(got.Managed, state0.Managed) {
				t.Errorf("managed after undo: %v", got.Managed)
			}
		})
	}
}

// TestReinstallRefusesWithoutWriting: an entry with a ref, whose ref is never
// removed, an entry from another source, a key with no entry, and a key that
// has a folder or a parked record are refused with typed errors.
func TestReinstallRefusesWithoutWriting(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *site)
		want  error
	}{
		{name: "an entry with a ref", setup: func(s *site) {
			must(t, os.WriteFile(s.p.SkillsLockFile(), []byte(ghostLock("acme/tool", "\n      \"ref\": \"v1\",")), 0o644))
		}, want: install.ErrEntryPinsRef},
		{name: "an entry from another source", setup: func(s *site) {
			must(t, os.WriteFile(s.p.SkillsLockFile(), []byte(ghostLock("other/tool", "")), 0o644))
		}, want: install.ErrSourceMismatch},
		{name: "no entry with the key", setup: func(s *site) {
			writeLock(t, s.p, nil)
		}, want: install.ErrNoEntryToReinstall},
		{name: "no lock", setup: func(*site) {}, want: install.ErrNoEntryToReinstall},
		{name: "a folder in the store", setup: func(s *site) {
			must(t, os.WriteFile(s.p.SkillsLockFile(), []byte(ghostLock("acme/tool", "")), 0o644))
			s.home.WriteFile(".agents/skills/tool/SKILL.md", skillMD("tool", "Mine."))
		}, want: install.ErrKeyExists},
		{name: "a parked record", setup: func(s *site) {
			must(t, os.WriteFile(s.p.SkillsLockFile(), []byte(ghostLock("acme/tool", "")), 0o644))
			s.saveState(domain.PersistedState{Parked: map[string]domain.ParkedSkill{
				"tool": {Key: "tool", Links: []domain.LinkRecord{}}}})
		}, want: install.ErrKeyExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, false)
			tc.setup(s)
			s.publish("acme/tool", "main", tool)
			rev := s.review("acme/tool")
			home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

			if pr, err := s.planner().Reinstall(rev, targets); !errors.Is(err, tc.want) {
				t.Fatalf("Reinstall() = %+v, %v; want %v", pr.Preview, err, tc.want)
			}
			sameHome(t, "refused reinstall", home0, s.everything())
			if !bytes.Equal(s.lockBytes(), lock0) || !reflect.DeepEqual(s.state(), state0) {
				t.Error("a refused reinstall changed the lock or the state")
			}
		})
	}
}

// TestACrashDuringAReinstallIsRecovered: a crash at every rename and symlink
// of a reinstall, then Recover: before the commit the old entry is back in
// its position and nothing is installed; after it, the reinstall is whole.
func TestACrashDuringAReinstallIsRecovered(t *testing.T) {
	for at := 1; ; at++ {
		s := newSite(t, false)
		must(t, os.WriteFile(s.p.SkillsLockFile(), []byte(ghostLock("acme/tool", "")), 0o644))
		s.publish("acme/tool", "main", tool)
		pr, err := s.planner().Reinstall(s.review("acme/tool"), targets)
		must(t, err)
		home0, lock0 := s.everything(), s.lockBytes()

		c := &crash{at: at}
		s.wrap = c.wrap
		_, err = s.engine().Apply(t.Context(), pr.Plan)
		s.wrap = nil
		if !c.dead {
			if err != nil || at < 4 {
				t.Fatalf("crash point %d not reached: %v", at, err)
			}
			return
		}
		res, err := s.clean().Recover(t.Context())
		if err != nil || !res.Found {
			t.Fatalf("crash %d: Recover() = %+v, %v", at, res, err)
		}
		if res.RolledBack {
			sameHome(t, "crash then recover", home0, s.everything())
			if !bytes.Equal(s.lockBytes(), lock0) {
				t.Errorf("crash %d: lock after recover:\n%s", at, s.lockBytes())
			}
		} else if lock := s.lockBytes(); !bytes.Contains(lock, []byte(pr.Preview.TreeSHA)) ||
			bytes.Contains(lock, []byte("2222222222222222222222222222222222222222")) || !exists(t, pr.Preview.StorePath) {
			t.Errorf("crash %d: a completed reinstall is not whole", at)
		}
		if exists(t, engine.RecordFile(s.p)) {
			t.Errorf("crash %d: the record is still there", at)
		}
	}
}
