package skillslock_test

import (
	"bytes"
	"errors"
	"testing"

	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// dropper is what the engine's LockDropper asks for; the store and the double
// both have it, outside ports.LockEntries.
type dropper interface {
	DropEmpty() error
}

var (
	_ dropper = (*skillslock.Store)(nil)
	_ dropper = (*fakes.LockEntries)(nil)
)

func dropOf(t *testing.T, h entriesHarness) dropper {
	t.Helper()
	d, ok := h.entries.(dropper)
	if !ok {
		t.Fatalf("%T has no DropEmpty", h.entries)
	}
	return d
}

// emptyAfterAddRemove is the lock Add creates from nothing, after Remove takes
// the entry back out: npx's empty lock, no final newline.
const emptyAfterAddRemove = "{\n  \"version\": 3,\n  \"skills\": {},\n  \"dismissed\": {}\n}"

func TestDropEmptyDeletesTheLockAddThenRemoveLeaves(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		h := open(t, nil)
		if err := h.entries.Add("new-skill", []byte(newEntry)); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if _, err := h.entries.Remove("new-skill", entryRaw(t, h.content(t), "new-skill")); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if got := string(h.content(t)); got != emptyAfterAddRemove {
			t.Fatalf("lock after Add and Remove:\n%q\nwant\n%q", got, emptyAfterAddRemove)
		}

		if err := dropOf(t, h).DropEmpty(); err != nil {
			t.Fatalf("DropEmpty: %v", err)
		}
		if got := h.content(t); got != nil {
			t.Errorf("the lock is still there:\n%s", got)
		}
	})
}

func TestDropEmptyWithoutALockDoesNothing(t *testing.T) {
	forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
		h := open(t, nil)
		if err := dropOf(t, h).DropEmpty(); err != nil {
			t.Fatalf("DropEmpty: %v", err)
		}
		if got := h.content(t); got != nil {
			t.Errorf("DropEmpty created a lock:\n%s", got)
		}
	})
}

func TestDropEmptyRefusesAnyOtherLockWithoutWriting(t *testing.T) {
	cases := map[string]string{
		"a lock with an entry":                 string(fixtureBytes(t, "lock-v3.json")),
		"an empty lock with a final newline":   emptyAfterAddRemove + "\n",
		"an empty lock with a dismissed skill": "{\n  \"version\": 3,\n  \"skills\": {},\n  \"dismissed\": {\n    \"x\": true\n  }\n}",
		"an empty lock with another key":       "{\n  \"version\": 3,\n  \"skills\": {},\n  \"dismissed\": {},\n  \"extra\": 1\n}",
		"an empty lock of version 2":           "{\n  \"version\": 2,\n  \"skills\": {},\n  \"dismissed\": {}\n}",
		"the same content in another layout":   `{"version":3,"skills":{},"dismissed":{}}`,
	}
	for name, lock := range cases {
		t.Run(name, func(t *testing.T) {
			forEachImpl(t, func(t *testing.T, open func(*testing.T, []byte) entriesHarness) {
				h := open(t, []byte(lock))
				if err := dropOf(t, h).DropEmpty(); !errors.Is(err, ports.ErrLockChanged) {
					t.Errorf("DropEmpty error = %v, want ErrLockChanged", err)
				}
				if got := h.content(t); !bytes.Equal(got, []byte(lock)) {
					t.Errorf("DropEmpty changed the lock:\n%s", got)
				}
			})
		})
	}
}
