package skillslock_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// The LockEntries contract (version 2, L32) runs against the real store and
// against the in-memory double, so both keep the same rules.

var (
	_ ports.LockEntries = (*fakes.LockEntries)(nil)
)

// entriesHarness is one implementation of ports.LockEntries over one lock
// file, with a way to read the file and to write it as npx skills would.
type entriesHarness struct {
	entries ports.LockEntries
	// content returns the file's bytes now, nil when it does not exist.
	content func(t *testing.T) []byte
	// set writes the file as another process would; nil removes it.
	set func(t *testing.T, data []byte)
}

type entriesImpl struct {
	name string
	// open holds lock as the file's content; nil leaves no file.
	open func(t *testing.T, lock []byte) entriesHarness
}

var entriesImpls = []entriesImpl{
	{name: "store", open: func(t *testing.T, lock []byte) entriesHarness {
		f := newFixture(t, lock)
		return entriesHarness{
			entries: f.store,
			content: func(t *testing.T) []byte {
				t.Helper()
				f.files.Fail = nil
				data, err := f.files.ReadFile(f.path)
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				if err != nil {
					t.Fatalf("read lock: %v", err)
				}
				return data
			},
			set: func(t *testing.T, data []byte) {
				t.Helper()
				if data == nil {
					if err := f.files.RemoveTree(f.path); err != nil {
						t.Fatalf("remove lock: %v", err)
					}
					return
				}
				f.files.AddFile(f.path, string(data), 0o644)
			},
		}
	}},
	{name: "fake", open: func(t *testing.T, lock []byte) entriesHarness {
		l := fakes.NewLockEntries(lock)
		return entriesHarness{
			entries: l,
			content: func(*testing.T) []byte { return l.Bytes() },
			set:     func(_ *testing.T, data []byte) { l.SetBytes(data) },
		}
	}},
}

// forEachImpl runs a contract test against every implementation.
func forEachImpl(t *testing.T, test func(t *testing.T, open func(t *testing.T, lock []byte) entriesHarness)) {
	t.Helper()
	for _, impl := range entriesImpls {
		t.Run(impl.name, func(t *testing.T) { test(t, impl.open) })
	}
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// entryRaw is entry name of lock as Value.Bytes writes it: the bytes Read
// reports as LockEntry.Raw.
func entryRaw(t *testing.T, lock []byte, name string) []byte {
	t.Helper()
	doc, err := orderedjson.Parse(lock)
	if err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	skills, _ := doc.Root().Get("skills")
	v, ok := skills.Get(name)
	if !ok {
		t.Fatalf("entry %s not in lock", name)
	}
	return v.Bytes()
}

// skillKeys lists the entry names of lock in order.
func skillKeys(t *testing.T, lock []byte) []string {
	t.Helper()
	doc, err := orderedjson.Parse(lock)
	if err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	skills, _ := doc.Root().Get("skills")
	return skills.Keys()
}

// lockFixtures are locks in the layout npx skills writes: without and with a
// final newline, with ref, commit, skillName, pluginName and unknown keys,
// and a copy of entries of this machine's lock.
var lockFixtures = []string{"lock-v3.json", "lock-variants.json", "npx-1.9.0/machine-lock.json"}

// ---------------------------------------------------------------------------
// Park and Unpark

func TestParkThenUnparkEveryEntryIsByteIdentical(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		for _, fixture := range lockFixtures {
			orig := fixtureBytes(t, fixture)
			keys := skillKeys(t, orig)
			for i, name := range keys {
				t.Run(fixture+"/"+name, func(t *testing.T) {
					h := open(t, orig)

					parked, err := h.entries.Park(name)
					if err != nil {
						t.Fatalf("Park: %v", err)
					}
					want := domain.ParkedEntry{Name: name, Raw: entryRaw(t, orig, name), LockSHA: sha(orig)}
					if i > 0 {
						want.Before = keys[i-1]
					}
					if i < len(keys)-1 {
						want.After = keys[i+1]
					}
					if parked.Name != want.Name || !bytes.Equal(parked.Raw, want.Raw) ||
						parked.Before != want.Before || parked.After != want.After || parked.LockSHA != want.LockSHA {
						t.Errorf("parked = %+v\nwant %+v", parked, want)
					}
					if got := skillKeys(t, h.content(t)); len(got) != len(keys)-1 || slices.Contains(got, name) {
						t.Errorf("entry still in the lock: %v", got)
					}

					if err := h.entries.Unpark(parked); err != nil {
						t.Fatalf("Unpark: %v", err)
					}
					if got := h.content(t); !bytes.Equal(got, orig) {
						t.Errorf("lock is not byte identical:\n%s\nwant:\n%s", got, orig)
					}
				})
			}
		}
	})
}

func TestParkRemovesOnlyTheEntry(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		h := open(t, orig)

		if _, err := h.entries.Park("frontend-design"); err != nil {
			t.Fatalf("Park: %v", err)
		}
		block := `
    "frontend-design": {
      "source": "anthropics/skills",
      "sourceType": "github",
      "sourceUrl": "https://github.com/anthropics/skills.git",
      "skillPath": "skills/frontend-design/SKILL.md",
      "skillFolderHash": "2222222222222222222222222222222222222222",
      "installedAt": "2026-10-03T09:15:30.123Z",
      "updatedAt": "2026-10-04T09:15:30.123Z"
    },`
		want := replaceOnce(t, string(orig), block, "")
		if got := string(h.content(t)); got != want {
			t.Errorf("lock after Park:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestNeighboursParkedTogetherComeBackInReverseOrder(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		h := open(t, orig)

		first, err := h.entries.Park("frontend-design")
		if err != nil {
			t.Fatalf("Park frontend-design: %v", err)
		}
		second, err := h.entries.Park("orca-cli")
		if err != nil {
			t.Fatalf("Park orca-cli: %v", err)
		}
		if err := h.entries.Unpark(second); err != nil {
			t.Fatalf("Unpark orca-cli: %v", err)
		}
		if err := h.entries.Unpark(first); err != nil {
			t.Fatalf("Unpark frontend-design: %v", err)
		}
		if got := h.content(t); !bytes.Equal(got, orig) {
			t.Errorf("lock is not byte identical:\n%s", got)
		}
	})
}

func TestUnparkGoesBackAfterItsPreviousNeighbour(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		added := []byte(`{
  "source": "owner/new",
  "sourceType": "github"
}`)

		t.Run("the last entry, with one added after it", func(t *testing.T) {
			h := open(t, orig)
			parked, err := h.entries.Park("agent-browser")
			if err != nil {
				t.Fatalf("Park: %v", err)
			}
			if err := h.entries.Add("new-skill", added); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if err := h.entries.Unpark(parked); err != nil {
				t.Fatalf("Unpark: %v", err)
			}
			want := []string{"find-skills", "frontend-design", "orca-cli", "agent-browser", "new-skill"}
			if got := skillKeys(t, h.content(t)); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("keys = %v, want %v", got, want)
			}
		})

		t.Run("the first entry", func(t *testing.T) {
			h := open(t, orig)
			parked, err := h.entries.Park("find-skills")
			if err != nil {
				t.Fatalf("Park: %v", err)
			}
			if parked.Before != "" || parked.After != "frontend-design" {
				t.Fatalf("neighbours = %q, %q", parked.Before, parked.After)
			}
			if err := h.entries.Unpark(parked); err != nil {
				t.Fatalf("Unpark: %v", err)
			}
			if got := h.content(t); !bytes.Equal(got, orig) {
				t.Errorf("lock is not byte identical:\n%s", got)
			}
		})
	})
}

func TestUnparkRefusesWithoutWriting(t *testing.T) {
	orig := fixtureBytes(t, "lock-v3.json")
	tests := []struct {
		name string
		// after changes the lock after frontend-design was parked from it.
		after func(t *testing.T, parked []byte) []byte
		want  error
	}{
		{
			name:  "the key is back",
			after: func(*testing.T, []byte) []byte { return orig },
			want:  ports.ErrLockEntryExists,
		},
		{
			name: "the previous neighbour is gone",
			after: func(t *testing.T, parked []byte) []byte {
				return removeEntry(t, parked, "find-skills")
			},
			want: ports.ErrLockMoved,
		},
		{
			name: "the next neighbour is gone",
			after: func(t *testing.T, parked []byte) []byte {
				return removeEntry(t, parked, "orca-cli")
			},
			want: ports.ErrLockMoved,
		},
		{
			name:  "the lock is gone",
			after: func(*testing.T, []byte) []byte { return nil },
			want:  ports.ErrLockMoved,
		},
		{
			name: "the version is 4 now",
			after: func(t *testing.T, parked []byte) []byte {
				return []byte(replaceOnce(t, string(parked), `"version": 3`, `"version": 4`))
			},
			want: ports.ErrUnsupportedLockVersion,
		},
		{
			name: "the version is 2 now",
			after: func(t *testing.T, parked []byte) []byte {
				return []byte(replaceOnce(t, string(parked), `"version": 3`, `"version": 2`))
			},
			want: ports.ErrUnsupportedLockVersion,
		},
	}
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h := open(t, orig)
				parked, err := h.entries.Park("frontend-design")
				if err != nil {
					t.Fatalf("Park: %v", err)
				}
				changed := tt.after(t, h.content(t))
				h.set(t, changed)

				if err := h.entries.Unpark(parked); !errors.Is(err, tt.want) {
					t.Errorf("error = %v, want %v", err, tt.want)
				}
				if got := h.content(t); !bytes.Equal(got, changed) {
					t.Errorf("a refused Unpark wrote:\n%s", got)
				}
			})
		}
	})
}

func TestUnparkTheOnlyEntryRefusesALockThatIsGone(t *testing.T) {
	only := []byte(`{
  "version": 3,
  "skills": {
    "solo": {
      "source": "o/solo"
    }
  }
}`)
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		h := open(t, only)
		parked, err := h.entries.Park("solo")
		if err != nil {
			t.Fatalf("Park: %v", err)
		}
		if parked.Before != "" || parked.After != "" {
			t.Fatalf("neighbours = %q, %q", parked.Before, parked.After)
		}
		h.set(t, nil)

		if err := h.entries.Unpark(parked); !errors.Is(err, ports.ErrLockMoved) {
			t.Errorf("error = %v, want ErrLockMoved", err)
		}
		if got := h.content(t); got != nil {
			t.Errorf("a refused Unpark created the lock:\n%s", got)
		}
	})
}

// removeEntry returns lock without entry name, as npx skills remove writes it.
func removeEntry(t *testing.T, lock []byte, name string) []byte {
	t.Helper()
	doc, err := orderedjson.Parse(lock)
	if err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	skills, _ := doc.Root().Get("skills")
	if !skills.Delete(name) {
		t.Fatalf("entry %s not in lock", name)
	}
	return doc.Bytes()
}

func TestParkRefusesWithoutWriting(t *testing.T) {
	v3 := fixtureBytes(t, "lock-v3.json")
	tests := []struct {
		name string
		lock []byte
		key  string
		want error
	}{
		{"an entry that is not there", v3, "nope", ports.ErrLockEntryNotFound},
		{"no lock", nil, "orca-cli", ports.ErrLockEntryNotFound},
		{"version 2", fixtureBytes(t, "lock-v2.json"), "old-skill", ports.ErrUnsupportedLockVersion},
		{"version 4", []byte(replaceOnce(t, string(v3), `"version": 3`, `"version": 4`)), "orca-cli", ports.ErrUnsupportedLockVersion},
		{"version \"3\"", []byte(replaceOnce(t, string(v3), `"version": 3`, `"version": "3"`)), "orca-cli", ports.ErrUnsupportedLockVersion},
	}
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				for _, op := range []string{"Park", "Remove"} {
					h := open(t, tt.lock)
					var err error
					if op == "Park" {
						_, err = h.entries.Park(tt.key)
					} else {
						_, err = h.entries.Remove(tt.key, []byte("{}"))
					}
					if !errors.Is(err, tt.want) {
						t.Errorf("%s error = %v, want %v", op, err, tt.want)
					}
					if got := h.content(t); !bytes.Equal(got, tt.lock) {
						t.Errorf("a refused %s wrote:\n%s", op, got)
					}
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// Add

// newEntry is an entry as skillhub adds it: no ref.
const newEntry = `{
  "source": "owner/repo",
  "sourceType": "github",
  "sourceUrl": "https://github.com/owner/repo.git",
  "skillPath": "skills/new-skill/SKILL.md",
  "skillFolderHash": "9999999999999999999999999999999999999999",
  "installedAt": "2026-11-22T10:00:00.000Z",
  "updatedAt": "2026-11-22T10:00:00.000Z"
}`

// indented indents every line but the first of an entry by four spaces, as
// it sits inside the skills object.
func indented(entry string) string {
	return strings.ReplaceAll(entry, "\n", "\n    ")
}

func TestAddAppendsTheEntryLast(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		h := open(t, orig)

		if err := h.entries.Add("new-skill", []byte(newEntry)); err != nil {
			t.Fatalf("Add: %v", err)
		}
		last := `      "updatedAt": "2026-10-08T11:45:00.000Z"
    }
  },`
		want := replaceOnce(t, string(orig), last, `      "updatedAt": "2026-10-08T11:45:00.000Z"
    },
    "new-skill": `+indented(newEntry)+`
  },`)
		if got := string(h.content(t)); got != want {
			t.Errorf("lock after Add:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestAddWithoutALockCreatesTheLockNpxSkillsCreates(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		h := open(t, nil)

		if err := h.entries.Add("new-skill", []byte(newEntry)); err != nil {
			t.Fatalf("Add: %v", err)
		}
		// createEmptyLockFile, then addSkillToLock, written by
		// JSON.stringify(lock, null, 2) with no final newline.
		want := "{\n  \"version\": 3,\n  \"skills\": {\n    \"new-skill\": " + indented(newEntry) +
			"\n  },\n  \"dismissed\": {}\n}"
		if got := string(h.content(t)); got != want {
			t.Errorf("lock created:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestAddRefusesWithoutWriting(t *testing.T) {
	v3 := fixtureBytes(t, "lock-v3.json")
	withRef := strings.Replace(newEntry, `"sourceUrl"`, `"ref": "v1.0.0",
  "sourceUrl"`, 1)
	tests := []struct {
		name  string
		lock  []byte
		key   string
		entry string
		want  error
	}{
		{"an entry with a ref", v3, "new-skill", withRef, ports.ErrRefNotAllowed},
		{"an entry with a ref, and no lock", nil, "new-skill", withRef, ports.ErrRefNotAllowed},
		{"a key already there", v3, "orca-cli", newEntry, ports.ErrLockEntryExists},
		{"version 2", fixtureBytes(t, "lock-v2.json"), "new-skill", newEntry, ports.ErrUnsupportedLockVersion},
		{"version 4", []byte(replaceOnce(t, string(v3), `"version": 3`, `"version": 4`)), "new-skill", newEntry, ports.ErrUnsupportedLockVersion},
	}
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h := open(t, tt.lock)

				if err := h.entries.Add(tt.key, []byte(tt.entry)); !errors.Is(err, tt.want) {
					t.Errorf("error = %v, want %v", err, tt.want)
				}
				if got := h.content(t); !bytes.Equal(got, tt.lock) {
					t.Errorf("a refused Add wrote:\n%s", got)
				}
			})
		}
	})
}

func TestAddRefusesAnEntryThatIsNotAnObject(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		for _, raw := range []string{`"text"`, `[1]`, `{`, ``} {
			h := open(t, orig)
			if err := h.entries.Add("new-skill", []byte(raw)); err == nil {
				t.Errorf("Add(%q) succeeded", raw)
			}
			if err := h.entries.Add("", []byte(newEntry)); err == nil {
				t.Error("Add with no name succeeded")
			}
			if got := h.content(t); !bytes.Equal(got, orig) {
				t.Errorf("a refused Add wrote:\n%s", got)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Remove

func TestRemoveTakesOutTheEntryItWasShown(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-variants.json")
		raw := entryRaw(t, orig, "pinned")
		parkedLock := open(t, orig)
		if _, err := parkedLock.entries.Park("pinned"); err != nil {
			t.Fatalf("Park: %v", err)
		}
		h := open(t, orig)

		removed, err := h.entries.Remove("pinned", raw)
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if !bytes.Equal(removed.Raw, raw) || removed.Before != "plain" || removed.After != "committed" || removed.LockSHA != sha(orig) {
			t.Errorf("removed = %+v", removed)
		}
		if got := h.content(t); !bytes.Equal(got, parkedLock.content(t)) {
			t.Errorf("lock after Remove is not the lock after Park:\n%s", got)
		}
		// Undo puts it back with its ref, byte for byte.
		if err := h.entries.Unpark(removed); err != nil {
			t.Fatalf("Unpark: %v", err)
		}
		if got := h.content(t); !bytes.Equal(got, orig) {
			t.Errorf("lock after undo is not byte identical:\n%s", got)
		}
	})
}

func TestRemoveRefusesAnEntryThatChanged(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		raw := entryRaw(t, orig, "orca-cli")
		for _, expected := range [][]byte{
			bytes.Replace(raw, []byte("3333"), []byte("5555"), 1),
			append(bytes.Clone(raw), '\n'),
			entryRaw(t, orig, "agent-browser"),
			nil,
		} {
			h := open(t, orig)
			if _, err := h.entries.Remove("orca-cli", expected); !errors.Is(err, ports.ErrLockMoved) {
				t.Errorf("error = %v, want ErrLockMoved", err)
			}
			if got := h.content(t); !bytes.Equal(got, orig) {
				t.Errorf("a refused Remove wrote:\n%s", got)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Hash

func TestHashIsTheSHA256OfTheFile(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		h := open(t, orig)
		if got, err := h.entries.Hash(); err != nil || got != sha(orig) {
			t.Errorf("Hash = %q, %v; want %s", got, err, sha(orig))
		}
		if _, err := h.entries.Park("orca-cli"); err != nil {
			t.Fatalf("Park: %v", err)
		}
		if got, err := h.entries.Hash(); err != nil || got != sha(h.content(t)) || got == sha(orig) {
			t.Errorf("Hash after Park = %q, %v", got, err)
		}

		missing := open(t, nil)
		if got, err := missing.entries.Hash(); err != nil || got != "" {
			t.Errorf("Hash of no lock = %q, %v; want empty", got, err)
		}
	})
}

// ---------------------------------------------------------------------------
// Malformed locks and entries: the double refuses them with the store's
// errors (L35, pending for W2a).

// lockOp runs one LockEntries method on h and returns its error.
type lockOp struct {
	name string
	run  func(h entriesHarness) error
}

// writeOps are the four writes, on an entry that is not in the lock when the
// write adds it and is in a well-formed lock when the write takes it out.
var writeOps = []lockOp{
	{"Park", func(h entriesHarness) error { _, err := h.entries.Park("orca-cli"); return err }},
	{"Remove", func(h entriesHarness) error { _, err := h.entries.Remove("orca-cli", []byte("{}")); return err }},
	{"Unpark", func(h entriesHarness) error {
		return h.entries.Unpark(domain.ParkedEntry{Name: "new-skill", Raw: []byte(newEntry)})
	}},
	{"Add", func(h entriesHarness) error { return h.entries.Add("new-skill", []byte(newEntry)) }},
}

func TestEveryWriteRefusesAMalformedLockWithoutWriting(t *testing.T) {
	v3 := fixtureBytes(t, "lock-v3.json")
	tests := []struct {
		name string
		lock []byte
	}{
		{"not JSON", []byte(`{`)},
		{"an array", []byte(`[]`)},
		{"skills is an array", []byte("{\n  \"version\": 3,\n  \"skills\": []\n}")},
		{"another entry has a source that is a number", []byte(replaceOnce(t, string(v3),
			`"source": "vercel-labs/agent-browser"`, `"source": 5`))},
		{"another entry has a ref that is a number", []byte(replaceOnce(t, string(v3),
			`"source": "vercel-labs/agent-browser"`, `"source": "vercel-labs/agent-browser", "ref": 5`))},
	}
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		for _, tt := range tests {
			for _, op := range writeOps {
				t.Run(tt.name+"/"+op.name, func(t *testing.T) {
					h := open(t, tt.lock)
					if err := op.run(h); !errors.Is(err, skillslock.ErrMalformedLock) {
						t.Errorf("error = %v, want ErrMalformedLock", err)
					}
					if got := h.content(t); !bytes.Equal(got, tt.lock) {
						t.Errorf("a refused %s wrote:\n%s", op.name, got)
					}
				})
			}
		}
	})
}

func TestWritesOnALockWithoutSkills(t *testing.T) {
	lock := []byte("{\n  \"version\": 3,\n  \"dismissed\": {}\n}")
	want := map[string]error{
		"Park":   ports.ErrLockEntryNotFound,
		"Remove": ports.ErrLockEntryNotFound,
		"Unpark": ports.ErrLockMoved,
		"Add":    skillslock.ErrMalformedLock,
	}
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		for _, op := range writeOps {
			t.Run(op.name, func(t *testing.T) {
				h := open(t, lock)
				if err := op.run(h); !errors.Is(err, want[op.name]) {
					t.Errorf("error = %v, want %v", err, want[op.name])
				}
				if got := h.content(t); !bytes.Equal(got, lock) {
					t.Errorf("a refused %s wrote:\n%s", op.name, got)
				}
			})
		}
	})
}

func TestAddAndUnparkRefuseAnEntryOfTheWrongShape(t *testing.T) {
	raws := []struct {
		name string
		raw  string
	}{
		{"a source that is a number", `{"source":5}`},
		{"a ref that is a number", `{"source":"o/r","ref":5}`},
		{"an updatedAt that is an object", `{"updatedAt":{}}`},
		{"text", `"text"`},
		{"an array", `[1]`},
		{"not JSON", `{`},
		{"nothing", ``},
	}
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		orig := fixtureBytes(t, "lock-v3.json")
		for _, tt := range raws {
			t.Run(tt.name, func(t *testing.T) {
				h := open(t, orig)
				if err := h.entries.Add("new-skill", []byte(tt.raw)); !errors.Is(err, skillslock.ErrMalformedLock) {
					t.Errorf("Add error = %v, want ErrMalformedLock", err)
				}
				entry := domain.ParkedEntry{Name: "new-skill", Raw: []byte(tt.raw), Before: "orca-cli"}
				if err := h.entries.Unpark(entry); !errors.Is(err, skillslock.ErrMalformedLock) {
					t.Errorf("Unpark error = %v, want ErrMalformedLock", err)
				}
				if got := h.content(t); !bytes.Equal(got, orig) {
					t.Errorf("a refused write changed the lock:\n%s", got)
				}
			})
		}
		h := open(t, orig)
		if err := h.entries.Add("", []byte(newEntry)); !errors.Is(err, skillslock.ErrMalformedLock) {
			t.Errorf("Add with no name: error = %v, want ErrMalformedLock", err)
		}
		if err := h.entries.Unpark(domain.ParkedEntry{Raw: []byte(newEntry)}); !errors.Is(err, skillslock.ErrMalformedLock) {
			t.Errorf("Unpark with no name: error = %v, want ErrMalformedLock", err)
		}
	})
}
