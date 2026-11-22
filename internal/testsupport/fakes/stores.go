package fakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/ports"
)

// ---------------------------------------------------------------------------
// SkillDirScanner

// SkillDirScanner is an in-memory ports.SkillDirScanner: each directory
// lists the entries a test set for it.
type SkillDirScanner struct {
	Faults

	mu   sync.Mutex
	dirs map[string][]ports.DirEntry
}

// NewSkillDirScanner builds a scanner with no directories.
func NewSkillDirScanner() *SkillDirScanner {
	return &SkillDirScanner{dirs: map[string][]ports.DirEntry{}}
}

// Set makes dir exist with entries. An entry without Path gets dir/Name.
func (s *SkillDirScanner) Set(dir string, entries ...ports.DirEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Path == "" {
			e.Path = filepath.Join(dir, e.Name)
		}
		out = append(out, e)
	}
	s.dirs[filepath.Clean(dir)] = out
}

// Scan implements ports.SkillDirScanner.
func (s *SkillDirScanner) Scan(dir string) ([]ports.DirEntry, error) {
	if err := s.enter("Scan", dir); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, ok := s.dirs[filepath.Clean(dir)]
	if !ok {
		return nil, &fs.PathError{Op: "scan", Path: dir, Err: fs.ErrNotExist}
	}
	out := slices.Clone(entries)
	slices.SortFunc(out, func(a, b ports.DirEntry) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// SkillsLockStore

// lockTimeLayout is how npx skills writes times: JavaScript's toISOString.
const lockTimeLayout = "2006-01-02T15:04:05.000Z"

// SkillsLockStore is an in-memory ports.SkillsLockStore. It keeps the
// compatibility rules the real store keeps, for the fields it models: only
// version 3 is written, ref is never touched, commit is updated only when
// the entry has one, installedAt is kept. Raw is rebuilt from the fields in
// the lock's key order.
type SkillsLockStore struct {
	Faults

	mu       sync.Mutex
	version  int
	entries  []domain.LockEntry
	revision int
}

// NewSkillsLockStore builds a version 3 lock with entries, in order.
func NewSkillsLockStore(entries ...domain.LockEntry) *SkillsLockStore {
	s := &SkillsLockStore{version: 3}
	for _, e := range entries {
		e.Raw = entryJSON(e)
		s.entries = append(s.entries, e)
	}
	return s
}

// SetVersion changes the lock's version, to test the refusal to write
// anything but version 3.
func (s *SkillsLockStore) SetVersion(v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = v
}

// Read implements ports.SkillsLockStore.
func (s *SkillsLockStore) Read() (domain.SkillsLock, error) {
	if err := s.enter("Read"); err != nil {
		return domain.SkillsLock{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := make([]domain.LockEntry, 0, len(s.entries))
	for _, e := range s.entries {
		e.Raw = slices.Clone(e.Raw)
		entries = append(entries, e)
	}
	return domain.SkillsLock{Version: s.version, Entries: entries, Revision: strconv.Itoa(s.revision)}, nil
}

func (s *SkillsLockStore) writableLocked(name string) (int, error) {
	if s.version != 3 {
		return 0, fmt.Errorf("version %d: %w", s.version, ports.ErrUnsupportedLockVersion)
	}
	for i, e := range s.entries {
		if e.Name == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s: %w", name, ports.ErrLockEntryNotFound)
}

// UpdateEntry implements ports.SkillsLockStore.
func (s *SkillsLockStore) UpdateEntry(name string, update ports.LockEntryUpdate) error {
	if err := s.enter("UpdateEntry", name, update.FolderHash, update.Commit); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.writableLocked(name)
	if err != nil {
		return err
	}
	e := s.entries[i]
	e.SkillFolderHash = update.FolderHash
	e.UpdatedAt = update.UpdatedAt.UTC().Format(lockTimeLayout)
	if e.Commit != "" {
		e.Commit = update.Commit
	}
	e.Raw = entryJSON(e)
	s.entries[i] = e
	s.revision++
	return nil
}

// RestoreEntry implements ports.SkillsLockStore.
func (s *SkillsLockStore) RestoreEntry(name string, raw []byte) error {
	if err := s.enter("RestoreEntry", name); err != nil {
		return err
	}
	var fields struct {
		Source          string `json:"source"`
		SourceType      string `json:"sourceType"`
		SourceURL       string `json:"sourceUrl"`
		SkillPath       string `json:"skillPath"`
		SkillFolderHash string `json:"skillFolderHash"`
		Ref             string `json:"ref"`
		Commit          string `json:"commit"`
		InstalledAt     string `json:"installedAt"`
		UpdatedAt       string `json:"updatedAt"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("restore %s: %w", name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.writableLocked(name)
	if err != nil {
		return err
	}
	s.entries[i] = domain.LockEntry{
		Name: name, Source: fields.Source, SourceType: fields.SourceType, SourceURL: fields.SourceURL,
		SkillPath: fields.SkillPath, SkillFolderHash: fields.SkillFolderHash, Ref: fields.Ref,
		Commit: fields.Commit, InstalledAt: fields.InstalledAt, UpdatedAt: fields.UpdatedAt,
		Raw: slices.Clone(raw),
	}
	s.revision++
	return nil
}

// entryJSON writes an entry with the keys npx skills uses, in its order.
func entryJSON(e domain.LockEntry) []byte {
	obj := orderedjson.NewObject()
	set := func(key, value string, always bool) {
		if always || value != "" {
			_ = obj.Set(key, orderedjson.String(value))
		}
	}
	set("source", e.Source, true)
	set("sourceType", e.SourceType, true)
	set("sourceUrl", e.SourceURL, true)
	set("skillPath", e.SkillPath, true)
	set("skillFolderHash", e.SkillFolderHash, true)
	set("ref", e.Ref, false)
	set("commit", e.Commit, false)
	set("installedAt", e.InstalledAt, true)
	set("updatedAt", e.UpdatedAt, true)
	return obj.Bytes()
}

// ---------------------------------------------------------------------------
// StateStore

// StateStore is an in-memory ports.StateStore. Load and Save copy deeply, so
// a caller never shares memory with what is stored. Lock fails at once with
// ErrStateLocked while another caller holds it.
type StateStore struct {
	Faults

	mu        sync.Mutex
	locked    bool
	state     domain.PersistedState
	operation *domain.Operation
	journal   []domain.HistoryEntry
	// version1 marks a state seeded as a version 1 file; the first write
	// that the real store would migrate clears it.
	version1 bool
}

// NewStateStore builds an empty state.
func NewStateStore() *StateStore { return &StateStore{} }

// ErrMigrationBlocked mirrors statefile.ErrMigrationBlocked, which a double
// cannot import: a write of version 2 fields while the version 1 state
// records an operation in progress.
var ErrMigrationBlocked = errors.New("state migration waits for the interrupted version 1 operation to be recovered")

// SeedVersion1 replaces the stored state with a version 1 state.json holding
// state and, when op is not nil, an operation in progress. As in the real
// store, a version 1 state cannot hold version 2 fields.
func (s *StateStore) SeedVersion1(state domain.PersistedState, op *domain.Operation) error {
	if hasVersion2Fields(state) {
		return errors.New("fake state: version 2 fields in a version 1 state")
	}
	c, err := deepCopy(state)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = c
	s.operation = nil
	if op != nil {
		o := *op
		s.operation = &o
	}
	s.version1 = true
	return nil
}

// Version1 reports whether the stored state is still version 1.
func (s *StateStore) Version1() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version1
}

// hasVersion2Fields reports whether state holds anything version 1 cannot.
func hasVersion2Fields(state domain.PersistedState) bool {
	return len(state.Managed) > 0 || len(state.Parked) > 0 || len(state.Preferences.Targets) > 0
}

// Locked reports whether the state lock is held.
func (s *StateStore) Locked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked
}

// Lock implements ports.StateStore.
func (s *StateStore) Lock(_ context.Context) (func() error, error) {
	if err := s.enter("Lock"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked {
		return nil, ports.ErrStateLocked
	}
	s.locked = true
	var once sync.Once
	return func() error {
		once.Do(func() {
			s.mu.Lock()
			s.locked = false
			s.mu.Unlock()
		})
		return nil
	}, nil
}

// Load implements ports.StateStore.
func (s *StateStore) Load() (domain.PersistedState, error) {
	if err := s.enter("Load"); err != nil {
		return domain.PersistedState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return deepCopy(s.state)
}

// Save implements ports.StateStore.
func (s *StateStore) Save(state domain.PersistedState) error {
	if err := s.enter("Save"); err != nil {
		return err
	}
	c, err := deepCopy(state)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.migrationWaits() && hasVersion2Fields(c) {
		return ErrMigrationBlocked
	}
	s.migrate()
	s.state = c
	return nil
}

// migrationWaits reports a version 1 state recording an operation: writes
// stay version 1 until it is cleared. The caller holds mu.
func (s *StateStore) migrationWaits() bool {
	return s.version1 && s.operation != nil
}

// migrate marks the state version 2 after a write the real store would
// migrate: any write to a version 1 state without an operation. The caller
// holds mu and calls it before the write, while the operation it replaces
// is still in place.
func (s *StateStore) migrate() {
	if s.version1 && !s.migrationWaits() {
		s.version1 = false
	}
}

// Operation implements ports.StateStore.
func (s *StateStore) Operation() (domain.Operation, bool, error) {
	if err := s.enter("Operation"); err != nil {
		return domain.Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operation == nil {
		return domain.Operation{}, false, nil
	}
	return *s.operation, true, nil
}

// RecordOperation implements ports.StateStore.
func (s *StateStore) RecordOperation(op domain.Operation) error {
	if err := s.enter("RecordOperation", op.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.migrate()
	s.operation = &op
	return nil
}

// ClearOperation implements ports.StateStore.
func (s *StateStore) ClearOperation() error {
	if err := s.enter("ClearOperation"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operation = nil
	return nil
}

// AppendJournal implements ports.StateStore.
func (s *StateStore) AppendJournal(entry domain.HistoryEntry) error {
	if err := s.enter("AppendJournal", string(entry.Action), entry.Target); err != nil {
		return err
	}
	c, err := deepCopy(entry)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journal = append(s.journal, c)
	return nil
}

// Journal implements ports.StateStore.
func (s *StateStore) Journal() ([]domain.HistoryEntry, error) {
	if err := s.enter("Journal"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return deepCopy(s.journal)
}

// deepCopy copies a value through JSON, which is how the real store keeps
// it, so a fake never accepts what the real file could not hold.
func deepCopy[T any](v T) (T, error) {
	var out T
	data, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("fake state: %w", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("fake state: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// SettingsStore

// SettingsStore is an in-memory ports.SettingsStore.
type SettingsStore struct {
	Faults

	mu       sync.Mutex
	settings *domain.Settings
}

// NewSettingsStore builds a store with no saved settings.
func NewSettingsStore() *SettingsStore { return &SettingsStore{} }

// Load implements ports.SettingsStore.
func (s *SettingsStore) Load() (domain.Settings, error) {
	if err := s.enter("Load"); err != nil {
		return domain.Settings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return domain.DefaultSettings(), nil
	}
	c := *s.settings
	c.Ignore = slices.Clone(c.Ignore)
	return c, nil
}

// Save implements ports.SettingsStore.
func (s *SettingsStore) Save(settings domain.Settings) error {
	if err := s.enter("Save"); err != nil {
		return err
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	if settings.Ignore == nil {
		settings.Ignore = []string{}
	} else {
		settings.Ignore = slices.Clone(settings.Ignore)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = &settings
	return nil
}

// ---------------------------------------------------------------------------
// Clock

// Clock is a ports.Clock that moves only when a test moves it.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock builds a clock stopped at now.
func NewClock(now time.Time) *Clock { return &Clock{now: now} }

// Now implements ports.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
