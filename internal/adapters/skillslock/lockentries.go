package skillslock

import (
	"bytes"
	"fmt"
	"slices"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/ports"
)

// Version 2: Store is also the real ports.LockEntries, under the lock
// exception the user approved in L32 (docs/design-v2.md section 11, point 1).
//
//   - A whole entry leaves the lock with its ref and comes back identical,
//     between the same neighbours. On a lock written the way npx skills
//     writes it (JSON.stringify with two spaces), Park and then Unpark give
//     back the same bytes.
//   - The ref of an entry that stays in the lock is never touched; an added
//     entry never carries one.
//   - Every write refuses a version other than 3, keeps every other key and
//     the final newline, and goes through commit: re-read and compare before
//     an atomic write, one retry, then ErrLockChanged.
//   - Unpark anchors on the previous neighbour (the next one when the entry
//     was first) and refuses when a neighbour is gone, so it never guesses a
//     position in a lock somebody else rewrote.

var _ ports.LockEntries = (*Store)(nil)

// emptyLock is the lock npx skills 1.9.0 creates when it has none:
// createEmptyLockFile written by writeSkillLock with JSON.stringify(lock,
// null, 2) and no final newline (dist/_chunks/remove.mjs:4481-4486 and
// :4516-4522).
const emptyLock = "{\n  \"version\": 3,\n  \"skills\": {},\n  \"dismissed\": {}\n}"

// Park implements ports.LockEntries.
func (s *Store) Park(name string) (domain.ParkedEntry, error) {
	return s.take(name, nil)
}

// Remove implements ports.LockEntries.
func (s *Store) Remove(name string, expectedRaw []byte) (domain.ParkedEntry, error) {
	if expectedRaw == nil {
		expectedRaw = []byte{}
	}
	return s.take(name, expectedRaw)
}

// take removes entry name from the lock and returns it with its neighbours
// and the hash of the lock it left. A non-nil expected must be the entry's
// bytes, or ErrLockMoved.
func (s *Store) take(name string, expected []byte) (domain.ParkedEntry, error) {
	var parked domain.ParkedEntry
	err := s.commit(name, func(data []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, fmt.Errorf("%s: %w", name, ports.ErrLockEntryNotFound)
		}
		p, err := writable(data)
		if err != nil {
			return nil, err
		}
		var entry *orderedjson.Value
		if p.skills != nil {
			entry, _ = p.skills.Get(name)
		}
		if entry == nil {
			return nil, fmt.Errorf("%s: %w", name, ports.ErrLockEntryNotFound)
		}
		raw := entry.Bytes()
		if expected != nil && !bytes.Equal(raw, expected) {
			return nil, fmt.Errorf("%s is not the entry that was read: %w", name, ports.ErrLockMoved)
		}
		keys := p.skills.Keys()
		i := slices.Index(keys, name)
		parked = domain.ParkedEntry{Name: name, Raw: raw, LockSHA: revision(data)}
		if i > 0 {
			parked.Before = keys[i-1]
		}
		if i < len(keys)-1 {
			parked.After = keys[i+1]
		}
		p.skills.Delete(name)
		return p.doc.Bytes(), nil
	})
	if err != nil {
		return domain.ParkedEntry{}, err
	}
	return parked, nil
}

// Unpark implements ports.LockEntries.
func (s *Store) Unpark(entry domain.ParkedEntry) error {
	if _, err := entryValue(entry.Name, entry.Raw); err != nil {
		return err
	}
	return s.commit(entry.Name, func(data []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, fmt.Errorf("%s: the lock is gone: %w", entry.Name, ports.ErrLockMoved)
		}
		p, err := writable(data)
		if err != nil {
			return nil, err
		}
		if p.skills == nil {
			return nil, fmt.Errorf("%s: the lock has no skills: %w", entry.Name, ports.ErrLockMoved)
		}
		if _, ok := p.skills.Get(entry.Name); ok {
			return nil, fmt.Errorf("%s: %w", entry.Name, ports.ErrLockEntryExists)
		}
		keys := p.skills.Keys()
		for _, n := range []string{entry.Before, entry.After} {
			if n != "" && !slices.Contains(keys, n) {
				return nil, fmt.Errorf("%s: its neighbour %s is gone: %w", entry.Name, n, ports.ErrLockMoved)
			}
		}
		prev := entry.Before
		if prev == "" && entry.After != "" {
			if i := slices.Index(keys, entry.After); i > 0 {
				prev = keys[i-1]
			}
		}
		// A fresh value per attempt: a retry must not share members.
		val, err := entryValue(entry.Name, entry.Raw)
		if err != nil {
			return nil, err
		}
		if err := p.skills.InsertAfter(prev, entry.Name, val); err != nil {
			return nil, fmt.Errorf("unpark %s: %w", entry.Name, err)
		}
		return p.doc.Bytes(), nil
	})
}

// Add implements ports.LockEntries.
func (s *Store) Add(name string, raw []byte) error {
	val, err := entryValue(name, raw)
	if err != nil {
		return err
	}
	if _, ok := val.Get(keyRef); ok {
		return fmt.Errorf("add %s: %w", name, ports.ErrRefNotAllowed)
	}
	return s.commit(name, func(data []byte, exists bool) ([]byte, error) {
		if !exists {
			data = []byte(emptyLock)
		}
		p, err := writable(data)
		if err != nil {
			return nil, err
		}
		if p.skills == nil {
			return nil, fmt.Errorf("add %s: %w: the lock has no skills object", name, ErrMalformedLock)
		}
		if _, ok := p.skills.Get(name); ok {
			return nil, fmt.Errorf("%s: %w", name, ports.ErrLockEntryExists)
		}
		val, err := entryValue(name, raw)
		if err != nil {
			return nil, err
		}
		if err := p.skills.Set(name, val); err != nil {
			return nil, fmt.Errorf("add %s: %w", name, err)
		}
		return p.doc.Bytes(), nil
	})
}

// Hash implements ports.LockEntries.
func (s *Store) Hash() (string, error) {
	data, exists, err := s.readLock()
	if err != nil {
		return "", fmt.Errorf("read skills lock: %w", err)
	}
	if !exists {
		return "", nil
	}
	return revision(data), nil
}

// writable parses a lock that a write may change: well formed and version 3.
func writable(data []byte) (parsed, error) {
	p, err := parse(data)
	if err != nil {
		return parsed{}, err
	}
	if err := checkVersion(p.doc.Root()); err != nil {
		return parsed{}, err
	}
	return p, nil
}

// entryValue parses a whole entry: a named JSON object whose known fields
// decode as Read decodes them.
func entryValue(name string, raw []byte) (*orderedjson.Value, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: an entry needs a name", ErrMalformedLock)
	}
	val, err := orderedjson.ParseValue(raw)
	if err != nil {
		return nil, fmt.Errorf("entry %s: %w: %w", name, ErrMalformedLock, err)
	}
	if _, err := decodeEntry(name, val); err != nil {
		return nil, err
	}
	return val, nil
}

// DropEmpty deletes the lock file when its bytes are exactly the empty lock
// Add creates when there is none, which is what Add and then Remove of the
// same entry leave. Any other content, even an equal lock in another layout
// or with a dismissed skill, fails with ports.ErrLockChanged and is kept. A
// missing lock is left missing. The engine uses it to bring back the
// preimage "the lock did not exist" (L35); it is not part of
// ports.LockEntries.
func (s *Store) DropEmpty() error {
	data, exists, err := s.readLock()
	if err != nil {
		return fmt.Errorf("read skills lock: %w", err)
	}
	if !exists {
		return nil
	}
	if string(data) != emptyLock {
		return fmt.Errorf("drop the skills lock: it is not the empty lock: %w", ports.ErrLockChanged)
	}
	if err := s.files.RemoveTree(s.path); err != nil {
		return fmt.Errorf("drop the skills lock: %w", err)
	}
	return nil
}
