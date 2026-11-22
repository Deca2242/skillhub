package toggle_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// TestUnlinkThenLinkIsIdentical: Unlink removes the harness's link to the
// skill and Link creates the same link again, with the target npx skills
// writes (relative to the store), so the home is identical; both keep
// Managed[key].Targets up to date.
func TestUnlinkThenLinkIsIdentical(t *testing.T) {
	cases := []struct {
		key, harness, link, target string
		shares                     []string
	}{
		{"find-skills", "claude-code", ".claude/skills/find-skills", "../../.agents/skills/find-skills", []string{"opencode"}},
		{"orchestration", "antigravity", ".gemini/config/skills/orchestration", "../../../.agents/skills/orchestration", []string{"antigravity-cli"}},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			m := newMachine(t)
			m.saveState(domain.PersistedState{Managed: map[string]domain.ManagedSkill{
				tc.key: {Key: tc.key, Targets: []string{tc.harness, "codex"}}}})
			world0, lock0 := m.world(), readFile(t, m.p.SkillsLockFile())
			link := m.h.Path(tc.link)

			un, err := m.planner().Unlink(m.graph(), tc.key, tc.harness)
			if err != nil {
				t.Fatalf("Unlink() error = %v", err)
			}
			pv := un.Preview
			if pv.Action != toggle.ActionUnlink || pv.Harness != tc.harness || pv.PlanID != un.Plan.ID || len(pv.Skills) != 1 {
				t.Fatalf("preview = %+v", pv)
			}
			if got := pv.Skills[0].RemovedLinks; len(got) != 1 || got[0].Path != link || got[0].Target != tc.target {
				t.Errorf("removed = %+v", got)
			}
			if !reflect.DeepEqual(pv.SharesDirWith, tc.shares) {
				t.Errorf("shares = %v, want %v", pv.SharesDirWith, tc.shares)
			}
			if len(pv.StillVisible) != 0 {
				t.Errorf("still visible = %v, want none", pv.StillVisible)
			}
			m.apply(un)
			if exists(t, link) {
				t.Fatal("the link is still there")
			}
			if got := m.state().Managed[tc.key].Targets; !reflect.DeepEqual(got, []string{"codex"}) {
				t.Errorf("targets after unlink = %v", got)
			}

			ln, err := m.planner().Link(m.graph(), tc.key, tc.harness)
			if err != nil {
				t.Fatalf("Link() error = %v", err)
			}
			if got := ln.Preview.Skills[0].CreatedLinks; len(got) != 1 || got[0].Path != link || got[0].Target != tc.target {
				t.Errorf("created = %+v", got)
			}
			m.apply(ln)
			sameWorld(t, "unlink then link", world0, m.world())
			if !bytes.Equal(readFile(t, m.p.SkillsLockFile()), lock0) {
				t.Error("the lock changed")
			}
			// The seeded targets are sorted, so Link gives them back as they were.
			if got := m.state().Managed[tc.key].Targets; !reflect.DeepEqual(got, []string{tc.harness, "codex"}) {
				t.Errorf("targets after link = %v, want the seeded ones", got)
			}
		})
	}
}

// TestUnlinkWarnsWhenTheHarnessStillSeesTheSkill: a copy of the skill in the
// harness keeps it visible after the link goes; the preview says how, and
// Link's preview says how the harness already sees it.
func TestUnlinkWarnsWhenTheHarnessStillSeesTheSkill(t *testing.T) {
	m := newMachine(t)
	copyPath := ".claude/skills/find-skills-copy"
	m.h.WriteFile(copyPath+"/SKILL.md", graphtest.SkillMD("find-skills", "find-skills"))
	want := []graph.Sighting{{Harness: "claude-code", Kind: graph.OwnCopy, Entry: m.h.Path(copyPath)}}

	un, err := m.planner().Unlink(m.graph(), "find-skills", "claude-code")
	must(t, err)
	if !reflect.DeepEqual(un.Preview.StillVisible, want) {
		t.Errorf("still visible = %+v, want %+v", un.Preview.StillVisible, want)
	}
	m.apply(un)
	ln, err := m.planner().Link(m.graph(), "find-skills", "claude-code")
	must(t, err)
	if !reflect.DeepEqual(ln.Preview.Visible, want) {
		t.Errorf("visible before link = %+v, want %+v", ln.Preview.Visible, want)
	}
}

// TestUnlinkWithoutManagedRecordLeavesTheStateAlone: a skill skillhub does
// not manage gets no Managed record from Unlink or Link.
func TestUnlinkWithoutManagedRecordLeavesTheStateAlone(t *testing.T) {
	m := newMachine(t)
	un, err := m.planner().Unlink(m.graph(), "archify", "claude-code")
	must(t, err)
	m.apply(un)
	ln, err := m.planner().Link(m.graph(), "archify", "claude-code")
	must(t, err)
	m.apply(ln)
	if st := m.state(); len(st.Managed) != 0 {
		t.Errorf("managed = %v, want none", st.Managed)
	}
}

// TestLinkAndUnlinkRefusals: only a harness with a link directory is
// administrable; every refusal is typed and writes nothing.
func TestLinkAndUnlinkRefusals(t *testing.T) {
	cases := []struct {
		name string
		run  func(m *machine) error
		want error
	}{
		{"unlink in a universal harness", func(m *machine) error {
			_, err := m.planner().Unlink(m.graph(), "i-have-adhd", "codex")
			return err
		}, toggle.ErrNotAdministrable},
		{"link in a universal harness", func(m *machine) error {
			_, err := m.planner().Link(m.graph(), "find-skills", "pi")
			return err
		}, toggle.ErrNotAdministrable},
		{"an unknown harness", func(m *machine) error {
			_, err := m.planner().Link(m.graph(), "find-skills", "nope")
			return err
		}, toggle.ErrUnknownHarness},
		{"unlink where there is no link", func(m *machine) error {
			_, err := m.planner().Unlink(m.graph(), "find-skills", "junie")
			return err
		}, toggle.ErrNotLinked},
		{"link where it is linked", func(m *machine) error {
			_, err := m.planner().Link(m.graph(), "find-skills", "claude-code")
			return err
		}, toggle.ErrAlreadyLinked},
		{"link over something that exists", func(m *machine) error {
			m.h.WriteFile(".junie/skills/find-skills/notes.txt", "mine")
			_, err := m.planner().Link(m.graph(), "find-skills", "junie")
			return err
		}, toggle.ErrPathTaken},
		{"link in a harness not on the machine", func(m *machine) error {
			_, err := m.planner().Link(m.graph(), "find-skills", "windsurf")
			return err
		}, toggle.ErrHarnessNotDetected},
		{"link a skill that is not active", func(m *machine) error {
			_, err := m.planner().Link(m.graph(), "accessibility", "junie")
			return err
		}, toggle.ErrNotFound},
		{"unlink a key that is a path", func(m *machine) error {
			_, err := m.planner().Unlink(m.graph(), "a/b", "claude-code")
			return err
		}, toggle.ErrInvalidKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t)
			err := tc.run(m)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("nothing is written", func(t *testing.T) {
		m := newMachine(t)
		world, st := m.world(), m.state()
		if _, err := m.planner().Unlink(m.graph(), "i-have-adhd", "codex"); err == nil {
			t.Fatal("Unlink in codex succeeded")
		}
		if _, err := m.planner().Link(m.graph(), "find-skills", "claude-code"); err == nil {
			t.Fatal("Link over an existing link succeeded")
		}
		noWrite(t, m, world, st)
	})
}

// stowClaude moves ~/.claude to ~/dotfiles/a/claude and leaves ~/.claude as a
// relative symlink to it, the way dotfiles managed with stow look, and
// registers the clone inside it again under its new real path.
func stowClaude(t *testing.T, m *machine) {
	t.Helper()
	must(t, os.MkdirAll(m.h.Path("dotfiles/a"), 0o755))
	must(t, os.Rename(m.h.Path(".claude"), m.h.Path("dotfiles/a/claude")))
	must(t, os.Symlink("dotfiles/a/claude", m.h.Path(".claude")))
	clone, err := filepath.EvalSymlinks(m.h.Path(graphtest.KarpathyClone))
	must(t, err)
	m.git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
}

// TestLinkUnderALinkedAncestorResolves: with ~/.claude a symlink to
// ~/dotfiles/a/claude, the link Link creates in ~/.claude/skills is relative
// to the real link directory, as npx skills writes it
// (relative(realpath(linkDir), target), remove.mjs:2882-2883), so it reaches
// the store folder; relative to the unresolved path it would be broken.
func TestLinkUnderALinkedAncestorResolves(t *testing.T) {
	m := newMachine(t)
	un, err := m.planner().Unlink(m.graph(), "find-skills", "claude-code")
	must(t, err)
	m.apply(un)
	stowClaude(t, m)

	ln, err := m.planner().Link(m.graph(), "find-skills", "claude-code")
	must(t, err)
	const want = "../../../../.agents/skills/find-skills"
	if got := ln.Preview.Skills[0].CreatedLinks; len(got) != 1 || got[0].Target != want {
		t.Errorf("created = %+v, want the target %s", got, want)
	}
	m.apply(ln)
	link := m.h.Path(".claude/skills/find-skills")
	if got := readlink(t, link); got != want {
		t.Errorf("link text = %q, want %q", got, want)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("the link does not resolve: %v", err)
	}
	if store := filepath.Join(m.p.StoreDir(), "find-skills"); resolved != store {
		t.Errorf("the link resolves to %s, want %s", resolved, store)
	}
}

// TestLinkWithoutADirListerRefusesALinkedAncestor: a planner built without
// Dirs cannot read the real path of a link directory, so under a symlinked
// ancestor it refuses with ErrLinkedLinkDir instead of planning a broken
// link; in a home without one, the link is the one npx skills writes.
func TestLinkWithoutADirListerRefusesALinkedAncestor(t *testing.T) {
	m := newMachine(t)
	un, err := m.planner().Unlink(m.graph(), "find-skills", "claude-code")
	must(t, err)
	m.apply(un)
	files := m.files()
	noDirs := toggle.New(toggle.Deps{
		Paths: m.p, Harnesses: m.harness, Plans: m.clean(),
		State: statefile.New(m.p), Lock: skillslock.New(m.p, files), Stat: files,
	})

	ln, err := noDirs.Link(m.graph(), "find-skills", "claude-code")
	must(t, err)
	if got := ln.Preview.Skills[0].CreatedLinks; len(got) != 1 || got[0].Target != "../../.agents/skills/find-skills" {
		t.Errorf("created = %+v", got)
	}

	stowClaude(t, m)
	before, st := m.world(), m.state()
	if _, err := noDirs.Link(m.graph(), "find-skills", "claude-code"); !errors.Is(err, toggle.ErrLinkedLinkDir) {
		t.Fatalf("Link() error = %v, want ErrLinkedLinkDir", err)
	}
	noWrite(t, m, before, st)
}
