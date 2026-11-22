package fakes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/ports"
)

// ---------------------------------------------------------------------------
// LockEntries

// emptyNpxLock is the lock npx skills 1.9.0 creates when it has none
// (createEmptyLockFile, written by JSON.stringify(lock, null, 2)).
const emptyNpxLock = "{\n  \"version\": 3,\n  \"skills\": {},\n  \"dismissed\": {}\n}"

// LockEntries is an in-memory ports.LockEntries over the bytes of one lock
// file. It keeps the rules of the real store: version 3 only, whole entries
// moved byte for byte between their neighbours, no ref on an added entry,
// every other key and the layout kept. It refuses the locks and entries the
// store refuses, with the same errors (ports.ErrMalformedLock for a
// wrong shape); the contract suite in internal/adapters/skillslock pins it.
// A nil lock is a missing file.
type LockEntries struct {
	Faults

	mu   sync.Mutex
	data []byte
}

// NewLockEntries holds lock as the file's content; nil means no file.
func NewLockEntries(lock []byte) *LockEntries {
	return &LockEntries{data: slices.Clone(lock)}
}

// Bytes returns the lock's content now, nil when there is no file.
func (l *LockEntries) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.data)
}

// SetBytes replaces the lock's content, as another writer such as npx skills
// would; nil removes the file.
func (l *LockEntries) SetBytes(lock []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data = slices.Clone(lock)
}

// Hash implements ports.LockEntries.
func (l *LockEntries) Hash() (string, error) {
	if err := l.enter("Hash"); err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.data == nil {
		return "", nil
	}
	sum := sha256.Sum256(l.data)
	return hex.EncodeToString(sum[:]), nil
}

// Park implements ports.LockEntries.
func (l *LockEntries) Park(name string) (domain.ParkedEntry, error) {
	if err := l.enter("Park", name); err != nil {
		return domain.ParkedEntry{}, err
	}
	return l.take(name, nil)
}

// Remove implements ports.LockEntries.
func (l *LockEntries) Remove(name string, expectedRaw []byte) (domain.ParkedEntry, error) {
	if err := l.enter("Remove", name); err != nil {
		return domain.ParkedEntry{}, err
	}
	if expectedRaw == nil {
		expectedRaw = []byte{} // nil never means "any bytes"
	}
	return l.take(name, expectedRaw)
}

// take removes entry name; a non-nil expected must equal its bytes.
func (l *LockEntries) take(name string, expected []byte) (domain.ParkedEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.data == nil {
		return domain.ParkedEntry{}, fmt.Errorf("%s: no lock: %w", name, ports.ErrLockEntryNotFound)
	}
	doc, skills, err := l.writableLocked()
	if err != nil {
		return domain.ParkedEntry{}, err
	}
	var entry *orderedjson.Value
	ok := false
	if skills != nil {
		entry, ok = skills.Get(name)
	}
	if !ok {
		return domain.ParkedEntry{}, fmt.Errorf("%s: %w", name, ports.ErrLockEntryNotFound)
	}
	raw := entry.Bytes()
	if expected != nil && !bytes.Equal(raw, expected) {
		return domain.ParkedEntry{}, fmt.Errorf("%s: %w", name, ports.ErrLockMoved)
	}
	keys := skills.Keys()
	i := slices.Index(keys, name)
	parked := domain.ParkedEntry{Name: name, Raw: raw}
	if i > 0 {
		parked.Before = keys[i-1]
	}
	if i < len(keys)-1 {
		parked.After = keys[i+1]
	}
	sum := sha256.Sum256(l.data)
	parked.LockSHA = hex.EncodeToString(sum[:])
	skills.Delete(name)
	l.data = doc.Bytes()
	return parked, nil
}

// Unpark implements ports.LockEntries.
func (l *LockEntries) Unpark(entry domain.ParkedEntry) error {
	if err := l.enter("Unpark", entry.Name); err != nil {
		return err
	}
	val, err := objectValue(entry.Name, entry.Raw)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.data == nil {
		return fmt.Errorf("%s: the lock is gone: %w", entry.Name, ports.ErrLockMoved)
	}
	doc, skills, err := l.writableLocked()
	if err != nil {
		return err
	}
	if skills == nil {
		return fmt.Errorf("%s: the lock has no skills: %w", entry.Name, ports.ErrLockMoved)
	}
	if _, ok := skills.Get(entry.Name); ok {
		return fmt.Errorf("%s: %w", entry.Name, ports.ErrLockEntryExists)
	}
	keys := skills.Keys()
	for _, n := range []string{entry.Before, entry.After} {
		if n != "" && !slices.Contains(keys, n) {
			return fmt.Errorf("%s: neighbour %s is gone: %w", entry.Name, n, ports.ErrLockMoved)
		}
	}
	prev := entry.Before
	if prev == "" && entry.After != "" {
		if i := slices.Index(keys, entry.After); i > 0 {
			prev = keys[i-1]
		}
	}
	if err := skills.InsertAfter(prev, entry.Name, val); err != nil {
		return fmt.Errorf("unpark %s: %w", entry.Name, err)
	}
	l.data = doc.Bytes()
	return nil
}

// Add implements ports.LockEntries.
func (l *LockEntries) Add(name string, raw []byte) error {
	if err := l.enter("Add", name); err != nil {
		return err
	}
	val, err := objectValue(name, raw)
	if err != nil {
		return err
	}
	if _, ok := val.Get("ref"); ok {
		return fmt.Errorf("%s: %w", name, ports.ErrRefNotAllowed)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.data == nil {
		l.data = []byte(emptyNpxLock)
	}
	doc, skills, err := l.writableLocked()
	if err != nil {
		return err
	}
	if skills == nil {
		return fmt.Errorf("add %s: %w: the lock has no skills object", name, ports.ErrMalformedLock)
	}
	if _, ok := skills.Get(name); ok {
		return fmt.Errorf("%s: %w", name, ports.ErrLockEntryExists)
	}
	if err := skills.Set(name, val); err != nil {
		return fmt.Errorf("add %s: %w", name, err)
	}
	l.data = doc.Bytes()
	return nil
}

// writableLocked parses the lock the way the store does: well-formed JSON
// whose skills, when present, is an object of entries that all decode, then
// version 3. skills is nil when the lock has none. The caller handles a
// missing file first.
func (l *LockEntries) writableLocked() (*orderedjson.Document, *orderedjson.Value, error) {
	doc, err := orderedjson.Parse(l.data)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ports.ErrMalformedLock, err)
	}
	root := doc.Root()
	if root.Kind() != orderedjson.KindObject {
		return nil, nil, fmt.Errorf("%w: the lock is a %s", ports.ErrMalformedLock, root.Kind())
	}
	var skills *orderedjson.Value
	if found, ok := root.Get("skills"); ok {
		if found.Kind() != orderedjson.KindObject {
			return nil, nil, fmt.Errorf("%w: skills is a %s", ports.ErrMalformedLock, found.Kind())
		}
		for _, name := range found.Keys() {
			v, _ := found.Get(name)
			if err := checkEntry(name, v); err != nil {
				return nil, nil, err
			}
		}
		skills = found
	}
	version, ok := root.Get("version")
	if !ok || version.Kind() != orderedjson.KindNumber {
		return nil, nil, fmt.Errorf("lock: %w", ports.ErrUnsupportedLockVersion)
	}
	if literal, _ := version.Literal(); literal != "3" {
		return nil, nil, fmt.Errorf("lock version %s: %w", literal, ports.ErrUnsupportedLockVersion)
	}
	return doc, skills, nil
}

// entryTextKeys are the entry fields the store decodes as strings; any other
// kind there makes the entry malformed.
var entryTextKeys = []string{
	"source", "sourceType", "sourceUrl", "skillPath", "skillFolderHash",
	"ref", "commit", "installedAt", "updatedAt",
}

// checkEntry refuses an entry the store would not decode.
func checkEntry(name string, v *orderedjson.Value) error {
	if v.Kind() != orderedjson.KindObject {
		return fmt.Errorf("entry %s: %w: not an object", name, ports.ErrMalformedLock)
	}
	for _, key := range entryTextKeys {
		fv, ok := v.Get(key)
		if !ok {
			continue
		}
		if _, err := fv.Text(); err != nil {
			return fmt.Errorf("entry %s, %s: %w: %w", name, key, ports.ErrMalformedLock, err)
		}
	}
	return nil
}

// objectValue parses a whole entry the way the store does: a named JSON
// object whose known fields decode.
func objectValue(name string, raw []byte) (*orderedjson.Value, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: an entry needs a name", ports.ErrMalformedLock)
	}
	val, err := orderedjson.ParseValue(raw)
	if err != nil {
		return nil, fmt.Errorf("entry %s: %w: %w", name, ports.ErrMalformedLock, err)
	}
	if err := checkEntry(name, val); err != nil {
		return nil, err
	}
	return val, nil
}

// DropEmpty deletes the lock when its bytes are exactly the empty lock Add
// creates from nothing, as skillslock.Store.DropEmpty does; any other content
// is ports.ErrLockChanged and stays. A missing lock is left missing.
func (l *LockEntries) DropEmpty() error {
	if err := l.enter("DropEmpty"); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.data == nil {
		return nil
	}
	if string(l.data) != emptyNpxLock {
		return fmt.Errorf("drop the skills lock: it is not the empty lock: %w", ports.ErrLockChanged)
	}
	l.data = nil
	return nil
}
