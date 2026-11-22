package remove_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/data"
	"skillhub/internal/manage/remove"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
)

// npxMachineLock is the lock npx skills 1.9.0 wrote on this machine, kept as
// a fixture by the lock store.
const npxMachineLock = "../../adapters/skillslock/testdata/npx-1.9.0/machine-lock.json"

// onlyEntry is data with every lock entry but name taken out, in the layout
// it had.
func onlyEntry(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	doc, err := orderedjson.Parse(data)
	must(t, err)
	skills, ok := doc.Root().Get("skills")
	if !ok {
		t.Fatal("the lock has no skills")
	}
	for _, k := range skills.Keys() {
		if k != name {
			skills.Delete(k)
		}
	}
	if skills.Len() != 1 {
		t.Fatalf("the lock does not hold %s", name)
	}
	return doc.Bytes()
}

// withoutEntry is data with entry name taken out: what npx skills remove
// leaves.
func withoutEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	doc, err := orderedjson.Parse(data)
	must(t, err)
	skills, _ := doc.Root().Get("skills")
	skills.Delete(name)
	return string(doc.Bytes())
}

// The last entry of a lock that holds anything besides what an add on a
// missing lock writes (npx's lastSelectedAgents, no dismissed, an unknown
// key) leaves the lock with "skills": {} and every other key intact, as npx
// skills remove does: deleting it would refuse at apply on every retry.
func TestTheLastEntryOfALockWithMoreKeepsTheLock(t *testing.T) {
	machineLock, err := os.ReadFile(filepath.FromSlash(npxMachineLock))
	must(t, err)
	entry := `{
      "source": "owner/grill-me",
      "sourceType": "github",
      "skillFolderHash": "SHA"
    }`
	cases := []struct {
		name string
		lock func(sha string) []byte
	}{
		{"npx 1.9.0 with lastSelectedAgents", func(string) []byte { return onlyEntry(t, machineLock, "grill-me") }},
		{"without dismissed", func(sha string) []byte {
			return []byte("{\n  \"version\": 3,\n  \"skills\": {\n    \"grill-me\": " + replaceSHA(entry, sha) + "\n  }\n}")
		}},
		{"unknown top-level key", func(sha string) []byte {
			return []byte("{\n  \"version\": 3,\n  \"skills\": {\n    \"grill-me\": " + replaceSHA(entry, sha) + "\n  },\n  \"dismissed\": {},\n  \"future\": true\n}")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			p := e.h.Paths()
			e.skill("grill-me")
			lock := tc.lock(e.treeSHA(".agents/skills/grill-me"))
			e.h.WriteFile(".agents/.skill-lock.json", string(lock))
			before := e.everything()

			plan := e.plan("grill-me")

			if pv := plan.Preview; pv.LockEntry == nil || pv.LockEntry.Name != "grill-me" || pv.LockEntry.DeletesLock {
				t.Errorf("LockEntry = %+v, want grill-me with the lock kept", pv.LockEntry)
			}

			out := e.apply(plan)

			if got, want := fileOrAbsent(t, p.SkillsLockFile()), withoutEntry(t, lock, "grill-me"); got != want {
				t.Errorf("lock after =\n%s\nwant\n%s", got, want)
			}

			e.undo(out)
			e.sameAs(before, "remove and undo")
		})
	}
}

func replaceSHA(entry, sha string) string {
	out := []byte{}
	for i := 0; i < len(entry); i++ {
		if i+3 <= len(entry) && entry[i:i+3] == "SHA" {
			out = append(out, sha...)
			i += 2
			continue
		}
		out = append(out, entry[i])
	}
	return string(out)
}

// staleLock reads the lock as it was before somebody rewrote an entry.
type staleLock struct {
	remove.LockReader
	name string
}

func (s staleLock) Read() (domain.SkillsLock, error) {
	lock, err := s.LockReader.Read()
	for i := range lock.Entries {
		if lock.Entries[i].Name == s.name {
			lock.Entries[i].Raw = []byte(`{"source":"owner/older"}`)
		}
	}
	return lock, err
}

// When the entry read from the lock store is not the one in the lock's
// bytes, the lock changed between the two reads: the removal is refused
// rather than deciding on one version and running on another.
func TestALockThatChangesBetweenTheReadsIsRefused(t *testing.T) {
	e := newEnv(t)
	e.skill("solo")
	e.addEntry("solo", "owner/solo", "skills/solo/SKILL.md", e.treeSHA(".agents/skills/solo"))
	profiles, err := data.DiagnosticProfiles()
	must(t, err)
	r := remove.New(remove.Deps{
		Paths: e.h.Paths(), Planner: e.eng, Lock: staleLock{LockReader: e.lock, name: "solo"}, State: e.state,
		Observe: e.observe, Profiles: profiles, Files: e.files,
		Dirs: fsadapter.NewScanner(e.h.Paths().Guard()), Links: osLinks{},
	})
	before := e.everything()

	_, err = r.Remove("solo")

	if !errors.Is(err, remove.ErrChanged) {
		t.Fatalf("Remove() error = %v, want ErrChanged", err)
	}
	e.sameAs(before, "a refused removal")
}
