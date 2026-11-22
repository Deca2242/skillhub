package toggle_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
)

// removedPaths lists the paths of a change's removed links, sorted.
func removedPaths(c toggle.SkillChange) []string {
	var out []string
	for _, l := range c.RemovedLinks {
		out = append(out, l.Path)
	}
	slices.Sort(out)
	return out
}

func (m *machine) paths(rels ...string) []string {
	var out []string
	for _, r := range rels {
		out = append(out, m.h.Path(r))
	}
	slices.Sort(out)
	return out
}

func (m *machine) saveState(st domain.PersistedState) {
	m.t.Helper()
	must(m.t, statefile.New(m.p).Save(st))
}

// TestDisableThenEnableRoundTripsByteForByte: disabling unlinks exactly the
// links that resolve to the folder, parks the folder and the lock entry and
// records them; enabling puts back the folder, the same links and the entry
// in its position, so the home and the lock are identical byte for byte.
func TestDisableThenEnableRoundTripsByteForByte(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		links     []string
		lockEntry bool
		managed   bool
	}{
		{name: "with a lock entry, managed", key: "find-skills", lockEntry: true, managed: true,
			links: []string{".claude/skills/find-skills", ".pi/agent/skills/find-skills"}},
		{name: "without a lock entry", key: "i-have-adhd",
			links: []string{".claude/skills/i-have-adhd", ".codex/skills/i-have-adhd", ".gemini/skills/i-have-adhd",
				".cursor/skills/i-have-adhd", ".copilot/skills/i-have-adhd"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t)
			if tc.managed {
				m.saveState(domain.PersistedState{Managed: map[string]domain.ManagedSkill{
					tc.key: {Key: tc.key, Targets: []string{"claude-code", "codex"}}}})
			}
			world0, lock0, state0 := m.world(), readFile(t, m.p.SkillsLockFile()), m.state()
			store := filepath.Join(m.p.StoreDir(), tc.key)
			parked := filepath.Join(m.p.ParkedDir(), tc.key)
			wantLinks := map[string]string{}
			for _, l := range m.paths(tc.links...) {
				wantLinks[l] = readlink(t, l)
			}

			pr, err := m.planner().Disable(m.graph(), tc.key)
			if err != nil {
				t.Fatalf("Disable() error = %v", err)
			}
			pv := pr.Preview
			if pv.Action != toggle.ActionDisable || pv.PlanID != pr.Plan.ID || pr.Plan.ID == "" || len(pv.Skills) != 1 {
				t.Fatalf("preview = %+v", pv)
			}
			c := pv.Skills[0]
			if c.Key != tc.key || c.From != store || c.To != parked || c.LockEntry != tc.lockEntry {
				t.Errorf("change = %+v", c)
			}
			if got := removedPaths(c); !reflect.DeepEqual(got, m.paths(tc.links...)) {
				t.Errorf("removed links = %v, want %v", got, m.paths(tc.links...))
			}
			if !reflect.DeepEqual(world0, m.world()) {
				t.Fatal("planning changed the home")
			}

			out := m.apply(pr)
			if exists(t, store) || !exists(t, filepath.Join(parked, "SKILL.md")) {
				t.Error("the folder was not parked")
			}
			for l := range wantLinks {
				if exists(t, l) {
					t.Errorf("link %s is still there", l)
				}
			}
			if keys := m.lockKeys(); slices.Contains(keys, tc.key) {
				t.Errorf("the lock still holds %s", tc.key)
			}
			st := m.state()
			if _, ok := st.Managed[tc.key]; ok {
				t.Error("the skill is still managed")
			}
			ps, ok := st.Parked[tc.key]
			if !ok {
				t.Fatal("no parked record")
			}
			gotLinks := map[string]string{}
			for _, l := range ps.Links {
				gotLinks[l.Path] = l.Target
			}
			if ps.Key != tc.key || ps.Folder != parked || !reflect.DeepEqual(gotLinks, wantLinks) || ps.SnapshotID != out.SnapshotID {
				t.Errorf("parked = %+v, want links %v", ps, wantLinks)
			}
			if (ps.Entry != nil) != tc.lockEntry {
				t.Errorf("parked entry = %+v, want one: %t", ps.Entry, tc.lockEntry)
			}
			if ps.Entry != nil && (ps.Entry.Name != tc.key || !bytes.Contains(ps.Entry.Raw, []byte(`"owner/`+tc.key+`"`))) {
				t.Errorf("parked entry = %+v", ps.Entry)
			}

			en, err := m.planner().Enable(tc.key)
			if err != nil {
				t.Fatalf("Enable() error = %v", err)
			}
			if en.Preview.Action != toggle.ActionEnable || len(en.Preview.Skills) != 1 ||
				en.Preview.Skills[0].From != parked || en.Preview.Skills[0].To != store ||
				len(en.Preview.Skills[0].CreatedLinks) != len(tc.links) || en.Preview.Skills[0].LockEntry != tc.lockEntry {
				t.Errorf("enable preview = %+v", en.Preview)
			}
			m.apply(en)

			sameWorld(t, "disable then enable", world0, m.world())
			if got := readFile(t, m.p.SkillsLockFile()); !bytes.Equal(got, lock0) {
				t.Errorf("the lock is not identical:\n%s\nwant\n%s", got, lock0)
			}
			if got := m.state(); !reflect.DeepEqual(got.Managed, state0.Managed) || len(got.Parked) != 0 {
				t.Errorf("state after enable: managed %v parked %v, want managed %v", got.Managed, got.Parked, state0.Managed)
			}
			if exists(t, parked) {
				t.Error("the parking area still holds the folder")
			}
		})
	}
}

// TestNpxUpdateAfterDisableDoesNotSeeTheEntry: npx skills update reinstalls
// a lock entry whose folder is missing (fact 4), so a disabled skill's entry
// must be out of the lock, and only that one, in the same order.
func TestNpxUpdateAfterDisableDoesNotSeeTheEntry(t *testing.T) {
	m := newMachine(t)
	before := m.lockKeys()
	pr, err := m.planner().Disable(m.graph(), "grill-me")
	must(t, err)
	m.apply(pr)
	want := slices.DeleteFunc(slices.Clone(before), func(k string) bool { return k == "grill-me" })
	if got := m.lockKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("lock entries = %v, want %v", got, want)
	}
	if bytes.Contains(readFile(t, m.p.SkillsLockFile()), []byte(`"grill-me"`)) {
		t.Error("the lock file still names grill-me")
	}
}

// TestEnableRefusesAReinstalledKey: when npx skills installed a skill with
// the same key while it was disabled, Enable refuses with a typed error that
// says what took the key, and writes nothing.
func TestEnableRefusesAReinstalledKey(t *testing.T) {
	cases := []struct {
		name          string
		folder, entry bool
	}{
		{"folder and lock entry", true, true},
		{"only the lock entry", false, true},
		{"only the folder", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t)
			pr, err := m.planner().Disable(m.graph(), "find-skills")
			must(t, err)
			m.apply(pr)
			store := filepath.Join(m.p.StoreDir(), "find-skills")
			if tc.folder {
				m.h.WriteFile(".agents/skills/find-skills/SKILL.md", graphtest.SkillMD("find-skills", "reinstalled"))
			}
			if tc.entry {
				writeLock(t, m.p, m.entries)
			}
			world, st := m.world(), m.state()

			_, err = m.planner().Enable("find-skills")
			if !errors.Is(err, toggle.ErrKeyReinstalled) {
				t.Fatalf("Enable() error = %v, want ErrKeyReinstalled", err)
			}
			var re *toggle.KeyReinstalledError
			if !errors.As(err, &re) {
				t.Fatalf("error %v is not a KeyReinstalledError", err)
			}
			wantFolder := ""
			if tc.folder {
				wantFolder = store
			}
			if re.Key != "find-skills" || re.Folder != wantFolder || re.LockEntry != tc.entry {
				t.Errorf("error = %+v", re)
			}
			noWrite(t, m, world, st)
		})
	}
}

// TestDisableTouchesOnlyTheSkillOfTheKey: a homonym elsewhere (a real folder
// with the same name) and a link that resolves to it are never touched.
func TestDisableTouchesOnlyTheSkillOfTheKey(t *testing.T) {
	m := newMachine(t)
	m.h.WriteFile(".codex/skills/grill-me/SKILL.md", graphtest.SkillMD("grill-me", "a homonym of codex"))
	m.h.Symlink(".gemini/config/skills/grill-me", "../../../.codex/skills/grill-me")
	homonym := m.h.Path(".codex/skills/grill-me/SKILL.md")
	homonymData := readFile(t, homonym)

	pr, err := m.planner().Disable(m.graph(), "grill-me")
	must(t, err)
	if got := removedPaths(pr.Preview.Skills[0]); !reflect.DeepEqual(got, m.paths(".claude/skills/grill-me")) {
		t.Errorf("removed links = %v, want only the one to the store folder", got)
	}
	m.apply(pr)
	if !bytes.Equal(readFile(t, homonym), homonymData) {
		t.Error("the homonym changed")
	}
	if got := readlink(t, m.h.Path(".gemini/config/skills/grill-me")); got != "../../../.codex/skills/grill-me" {
		t.Errorf("the link to the homonym is now %q", got)
	}
}

// TestDisableWarnsWhenAnOwnCopyKeepsTheSkillVisible: disabling acts on the
// managed targets only (design-v2 section 11, item 4); a harness's own copy
// stays, untouched, and the preview says which harness still sees the skill,
// where and how.
func TestDisableWarnsWhenAnOwnCopyKeepsTheSkillVisible(t *testing.T) {
	m := newMachine(t)
	skillMD := readFile(t, m.h.Path(".agents/skills/grill-me/SKILL.md"))
	m.h.WriteFile(".pi/agent/skills/grill-me/SKILL.md", string(skillMD))
	copyMD := m.h.Path(".pi/agent/skills/grill-me/SKILL.md")
	want := []graph.Sighting{{Harness: "pi", Kind: graph.OwnCopy, Entry: m.h.Path(".pi/agent/skills/grill-me")}}

	pr, err := m.planner().Disable(m.graph(), "grill-me")
	must(t, err)
	if !reflect.DeepEqual(pr.Preview.StillVisible, want) {
		t.Errorf("preview still visible = %+v, want %+v", pr.Preview.StillVisible, want)
	}
	if !reflect.DeepEqual(pr.Preview.Skills[0].StillVisible, want) {
		t.Errorf("change still visible = %+v, want %+v", pr.Preview.Skills[0].StillVisible, want)
	}
	for _, s := range pr.Plan.Steps {
		if s.Path == filepath.Dir(copyMD) || s.Src == filepath.Dir(copyMD) {
			t.Errorf("a %s step touches the own copy", s.Kind)
		}
	}
	m.apply(pr)
	if !bytes.Equal(readFile(t, copyMD), skillMD) {
		t.Error("the own copy changed")
	}
}

// TestDisableAndEnableRefusals checks every refusal: it is typed and writes
// nothing.
func TestDisableAndEnableRefusals(t *testing.T) {
	cases := []struct {
		name string
		run  func(m *machine) error
		want error
	}{
		{"a key that is a path", func(m *machine) error {
			_, err := m.planner().Disable(m.graph(), "../find-skills")
			return err
		}, toggle.ErrInvalidKey},
		{"a key with no folder", func(m *machine) error {
			_, err := m.planner().Disable(m.graph(), "nope")
			return err
		}, toggle.ErrNotFound},
		{"a skill parked in OFF/", func(m *machine) error {
			_, err := m.planner().Disable(m.graph(), "accessibility")
			return err
		}, toggle.ErrNotFound},
		{"an alias in the store", func(m *machine) error {
			_, err := m.planner().Disable(m.graph(), "computer-use")
			return err
		}, toggle.ErrNotFound},
		{"a folder an alias reaches", func(m *machine) error {
			_, err := m.planner().Disable(m.graph(), "orca-computer-use")
			return err
		}, toggle.ErrAmbiguous},
		{"a folder a plain alias in the store reaches", func(m *machine) error {
			m.h.Symlink(".agents/skills/fs-alias", "find-skills")
			_, err := m.planner().Disable(m.graph(), "find-skills")
			return err
		}, toggle.ErrAmbiguous},
		{"a key already parked", func(m *machine) error {
			m.saveState(domain.PersistedState{Parked: map[string]domain.ParkedSkill{
				"archify": {Key: "archify", Folder: filepath.Join(m.p.ParkedDir(), "archify")}}})
			_, err := m.planner().Disable(m.graph(), "archify")
			return err
		}, toggle.ErrAlreadyParked},
		{"enable a key that is not parked", func(m *machine) error {
			_, err := m.planner().Enable("archify")
			return err
		}, toggle.ErrNotParked},
		{"enable a record outside the parking area", func(m *machine) error {
			m.saveState(domain.PersistedState{Parked: map[string]domain.ParkedSkill{
				"zz": {Key: "zz", Folder: m.p.StoreDir() + "/archify"}}})
			_, err := m.planner().Enable("zz")
			return err
		}, toggle.ErrBadParkedRecord},
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
		for _, key := range []string{"nope", "accessibility", "orca-computer-use"} {
			if _, err := m.planner().Disable(m.graph(), key); err == nil {
				t.Fatalf("Disable(%s) succeeded", key)
			}
		}
		noWrite(t, m, world, st)
	})
}

// TestTheParkingAreaMustBeInvisible: a parking area inside a directory some
// harness reads would leave the skill visible, so Disable refuses.
func TestTheParkingAreaMustBeInvisible(t *testing.T) {
	m := newMachine(t)
	m.harness = append(m.harness, data.Harness{ID: "reads-agents", ReadsStore: data.Yes,
		ReadRoots: []data.ReadRoot{{Dir: ".agents", Depth: data.DepthUnlimited}}})
	_, err := m.planner().Disable(m.graph(), "find-skills")
	if !errors.Is(err, toggle.ErrParkingVisible) {
		t.Fatalf("Disable() error = %v, want ErrParkingVisible", err)
	}
}
