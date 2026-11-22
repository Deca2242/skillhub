package fakes_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// The LockEntries contract itself runs against this double and the real
// store in internal/adapters/skillslock (lockentries_test.go). These tests
// cover what only the double has: injected failures, recorded calls and its
// helpers.

var _ ports.LockEntries = (*fakes.LockEntries)(nil)

const fakeLock = `{
  "version": 3,
  "skills": {
    "a": {
      "source": "o/a",
      "ref": "v1"
    },
    "b": {
      "source": "o/b"
    }
  }
}
`

func TestLockEntriesAnInjectedFailureChangesNothing(t *testing.T) {
	errBoom := errors.New("boom")
	parked := domain.ParkedEntry{Name: "c", Raw: []byte(`{"source": "o/c"}`), Before: "b"}
	calls := []struct {
		op   string
		call func(l *fakes.LockEntries) error
	}{
		{"Park", func(l *fakes.LockEntries) error { _, err := l.Park("a"); return err }},
		{"Remove", func(l *fakes.LockEntries) error {
			_, err := l.Remove("b", []byte("{\n  \"source\": \"o/b\"\n}"))
			return err
		}},
		{"Unpark", func(l *fakes.LockEntries) error { return l.Unpark(parked) }},
		{"Add", func(l *fakes.LockEntries) error { return l.Add("c", []byte(`{"source": "o/c"}`)) }},
		{"Hash", func(l *fakes.LockEntries) error { _, err := l.Hash(); return err }},
	}
	for _, c := range calls {
		t.Run(c.op, func(t *testing.T) {
			l := fakes.NewLockEntries([]byte(fakeLock))
			l.Fail = func(call fakes.Call) error {
				if call.Op == c.op {
					return errBoom
				}
				return nil
			}

			if err := c.call(l); !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want the injected one", err)
			}
			if got := l.Bytes(); string(got) != fakeLock {
				t.Errorf("a failed %s changed the lock:\n%s", c.op, got)
			}
			if got := l.Calls(); len(got) != 1 || got[0].Op != c.op {
				t.Errorf("calls = %v", got)
			}
		})
	}
}

func TestLockEntriesRecordsItsCallsWithTheEntryName(t *testing.T) {
	l := fakes.NewLockEntries([]byte(fakeLock))
	parked, err := l.Park("a")
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if err := l.Unpark(parked); err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	want := []fakes.Call{{Op: "Park", Args: []string{"a"}}, {Op: "Unpark", Args: []string{"a"}}}
	got := l.Calls()
	if !slices.EqualFunc(got, want, func(a, b fakes.Call) bool { return a.Op == b.Op && slices.Equal(a.Args, b.Args) }) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if string(l.Bytes()) != fakeLock {
		t.Errorf("Park and Unpark changed the lock:\n%s", l.Bytes())
	}
}

func TestLockEntriesKeepsItsOwnCopyOfTheLock(t *testing.T) {
	data := []byte(fakeLock)
	l := fakes.NewLockEntries(data)
	data[0] = 'X'
	got := l.Bytes()
	got[1] = 'Y'
	if !bytes.Equal(l.Bytes(), []byte(fakeLock)) {
		t.Errorf("the lock shares memory with its callers:\n%s", l.Bytes())
	}

	l.SetBytes(nil)
	if l.Bytes() != nil {
		t.Errorf("SetBytes(nil) left %q, want no file", l.Bytes())
	}
	if h, err := l.Hash(); err != nil || h != "" {
		t.Errorf("Hash of no lock = %q, %v", h, err)
	}
}
