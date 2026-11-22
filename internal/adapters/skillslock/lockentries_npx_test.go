package skillslock_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Interoperability with npx skills 1.9.0 (docs/design-v2.md section 11,
// point 2), checked against its code, not by running it: the tests may not
// reach the network nor the real home. Every citation is relative to
// ~/.npm/_npx/ac0ed6aa23b37c1e/node_modules/skills (package.json:3, version
// 1.9.0), read on 2026-11-22:
//
//   - dist/_chunks/remove.mjs:4463-4480: readSkillLock accepts version 3 with
//     a skills object; anything else reads as an empty lock.
//   - remove.mjs:4481-4486: writeSkillLock writes JSON.stringify(lock, null,
//     2) with no final newline.
//   - remove.mjs:4492-4501: addSkillToLock writes the entry's own keys, then
//     installedAt (kept) and updatedAt.
//   - remove.mjs:4516-4522: createEmptyLockFile is {version: 3, skills: {},
//     dismissed: {}}.
//   - remove.mjs:6635-6698: recordGlobalLock, for a GitHub source, keys the
//     entry by skill.name and writes source, sourceType, sourceUrl, ref (left
//     out when undefined), commit (when resolved), skillPath, skillName (only
//     on a rename, :6366-6368), skillFolderHash, pluginName and installMode
//     (when set).
//   - dist/cli.mjs:2031 and :2051: update reinstalls a global entry when
//     !onDisk.has(skillName), skillName being the lock key; cli.mjs:1561-1563
//     builds onDisk from listInstalledSkills names, which are the frontmatter
//     name of each folder's SKILL.md (remove.mjs:3364-3376, :2652-2664), not
//     the folder name.
//
// testdata/npx-1.9.0/machine-lock.json holds three entries copied from this
// machine's lock (read only, 2026-11-22), with its dismissed and
// lastSelectedAgents, in the layout npx skills wrote them.

// npxEntry is an entry as recordGlobalLock writes it for a GitHub install
// with no ref: the keys in its order, then installedAt and updatedAt from
// addSkillToLock.
const npxEntry = `{
  "source": "stablyai/orca",
  "sourceType": "github",
  "sourceUrl": "https://github.com/stablyai/orca.git",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "skillPath": "skills/computer-use/SKILL.md",
  "skillFolderHash": "fab1436f0d73889492279544eadebc9bcc2694b6",
  "installedAt": "2026-11-22T10:00:00.000Z",
  "updatedAt": "2026-11-22T10:00:00.000Z"
}`

// npxReadEntry is what check and update read from a global entry
// (cli.mjs:2025-2051): a missing key decodes as nil.
type npxReadEntry struct {
	Source          *string `json:"source"`
	SourceType      *string `json:"sourceType"`
	SourceURL       *string `json:"sourceUrl"`
	Ref             *string `json:"ref"`
	SkillPath       *string `json:"skillPath"`
	SkillFolderHash *string `json:"skillFolderHash"`
	InstalledAt     *string `json:"installedAt"`
	UpdatedAt       *string `json:"updatedAt"`
}

// npxLock is the lock as readSkillLock accepts it.
type npxLock struct {
	Version int                     `json:"version"`
	Skills  map[string]npxReadEntry `json:"skills"`
}

func TestAnAddedEntryIsWhatNpxSkillsWritesAndReads(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "npx-1.9.0/machine-lock.json")
		h := open(t, orig)

		if err := h.entries.Add("orca-computer-use", []byte(npxEntry)); err != nil {
			t.Fatalf("Add: %v", err)
		}
		got := h.content(t)

		// The bytes JSON.stringify(lock, null, 2) gives for the same lock
		// with the entry added last.
		closing := "\n    }\n  },\n  \"dismissed\""
		want := replaceOnce(t, string(orig), closing,
			"\n    },\n    \"orca-computer-use\": "+indented(npxEntry)+"\n  },\n  \"dismissed\"")
		if string(got) != want {
			t.Fatalf("lock after Add:\n%s\nwant:\n%s", got, want)
		}

		var lock npxLock
		if err := json.Unmarshal(got, &lock); err != nil {
			t.Fatalf("npx skills could not parse the lock: %v", err)
		}
		if lock.Version != 3 {
			t.Fatalf("version = %d: readSkillLock would read an empty lock", lock.Version)
		}
		e, ok := lock.Skills["orca-computer-use"]
		if !ok {
			t.Fatal("the entry is not under its key")
		}
		for key, v := range map[string]*string{
			"source": e.Source, "sourceType": e.SourceType, "sourceUrl": e.SourceURL, "skillPath": e.SkillPath,
			"skillFolderHash": e.SkillFolderHash, "installedAt": e.InstalledAt, "updatedAt": e.UpdatedAt,
		} {
			if v == nil || *v == "" {
				t.Errorf("%s is missing", key)
			}
		}
		if *e.SourceType != "github" || e.Ref != nil {
			t.Errorf("sourceType = %q, ref = %v; want github and no ref", *e.SourceType, e.Ref)
		}
	})
}

// npxOnDisk mirrors installedSkillNames(true) of npx skills 1.9.0 over the
// store: the frontmatter name of every folder holding a SKILL.md with a name
// and a description.
func npxOnDisk(t *testing.T, store map[string]string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, skillMD := range store {
		parts := strings.SplitN(skillMD, "---", 3)
		if len(parts) != 3 {
			continue
		}
		var fm struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
			t.Fatalf("frontmatter: %v", err)
		}
		if fm.Name != "" && fm.Description != "" {
			names[fm.Name] = true
		}
	}
	return names
}

func TestOnDiskPairsAnAddedEntryByItsFrontmatterName(t *testing.T) {
	// The store as on this machine: computer-use is a link to
	// orca-computer-use, whose SKILL.md is named orca-computer-use.
	computerUse := "---\nname: orca-computer-use\ndescription: Drives the GUI of a local app window.\n---\n# body\n"
	store := map[string]string{
		"orca-cli":          "---\nname: orca-cli\ndescription: Operate Orca.\n---\n",
		"orca-computer-use": computerUse,
		"computer-use":      computerUse,
	}
	onDisk := npxOnDisk(t, store)

	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		h := open(t, fixtureBytes(t, "npx-1.9.0/machine-lock.json"))
		if err := h.entries.Add("orca-computer-use", []byte(npxEntry)); err != nil {
			t.Fatalf("Add: %v", err)
		}
		keys := skillKeys(t, h.content(t))
		if !slices.Contains(keys, "orca-computer-use") {
			t.Fatalf("keys = %v", keys)
		}

		// An entry keyed by the frontmatter name is on disk, so update
		// leaves it alone; one keyed by the folder name is not, so update
		// would reinstall it (the false absence of H§4).
		for key, want := range map[string]bool{"orca-cli": true, "orca-computer-use": true, "computer-use": false, "grill-me": false} {
			if !slices.Contains(keys, key) {
				t.Fatalf("%s is not in the lock", key)
			}
			if onDisk[key] != want {
				t.Errorf("onDisk.has(%q) = %v, want %v", key, onDisk[key], want)
			}
		}
	})
}

func TestTheMachineLockRoundTripsThroughParkAndUnpark(t *testing.T) {
	orig := fixtureBytes(t, "npx-1.9.0/machine-lock.json")
	if bytes.HasSuffix(orig, []byte("\n")) {
		t.Fatal("npx skills writes no final newline; the fixture has one")
	}
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		h := open(t, orig)
		parked, err := h.entries.Park("computer-use")
		if err != nil {
			t.Fatalf("Park: %v", err)
		}
		if parked.Before != "orca-cli" || parked.After != "grill-me" {
			t.Errorf("neighbours = %q, %q", parked.Before, parked.After)
		}
		if err := h.entries.Unpark(parked); err != nil {
			t.Fatalf("Unpark: %v", err)
		}
		if got := h.content(t); !bytes.Equal(got, orig) {
			t.Errorf("lock is not byte identical:\n%s", got)
		}
	})
}
