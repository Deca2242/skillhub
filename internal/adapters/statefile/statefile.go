// Package statefile implements ports.StateStore on top of state.json,
// journal.jsonl and an advisory file lock under paths.Paths.
//
// state.json is version 2 (docs/design-v2.md section 14). A version 1 file
// is still read, and the first write that converts it first leaves a copy
// of it, byte for byte, in state.v1.json next to it; that copy is never
// overwritten. While a version 1 file records an operation in progress, the
// conversion waits: writes stay version 1, so a version 1 binary can still
// recover it, until the operation is cleared. A version 1 binary rejects a
// version 2 file before writing anything.
package statefile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

const (
	// stateVersion is the version every write produces once migrated.
	stateVersion = 2
	// legacyVersion is the version 1 format, still read, and still written
	// while it records an operation in progress.
	legacyVersion = 1
	// legacyCopyName is the copy of the version 1 file the migration leaves.
	legacyCopyName = "state.v1.json"
	// legacyTempPattern names the temporary file the copy is written to
	// before it is linked as legacyCopyName.
	legacyTempPattern = ".tmp-v1-*"

	defaultLockWait = 200 * time.Millisecond
	lockPollEvery   = 10 * time.Millisecond
	maxJournalLine  = 4 * 1024 * 1024
)

var (
	// ErrUnsupportedStateVersion reports a state.json whose version is
	// neither 1 nor 2.
	ErrUnsupportedStateVersion = errors.New("unsupported state version")
	// ErrMigrationBlocked reports a write of version 2 fields while the
	// version 1 state.json records an operation in progress: the migration
	// waits until recover clears it.
	ErrMigrationBlocked = errors.New("state migration waits for the interrupted version 1 operation to be recovered")
)

var _ ports.StateStore = (*Store)(nil)

type stateFile struct {
	Version    *int                           `json:"version,omitempty"`
	Candidates []domain.UpdateCandidate       `json:"candidates"`
	Skips      []domain.Skip                  `json:"skips"`
	Checks     domain.CheckTimes              `json:"checks"`
	Notified   domain.NotifiedMarks           `json:"notified"`
	Results    map[string]domain.TargetResult `json:"results,omitempty"`
	// Managed, Parked and Preferences exist only in version 2.
	Managed     map[string]domain.ManagedSkill `json:"managed,omitempty"`
	Parked      map[string]domain.ParkedSkill  `json:"parked,omitempty"`
	Preferences *domain.Preferences            `json:"preferences,omitempty"`
	Operation   *domain.Operation              `json:"operation,omitempty"`
}

// current is state.json as it is on disk: decoded, its exact bytes and
// whether it exists.
type current struct {
	sf     stateFile
	raw    []byte
	exists bool
}

// legacy reports a version 1 file; a file without a version is version 1.
func (c current) legacy() bool {
	return c.exists && (c.sf.Version == nil || *c.sf.Version == legacyVersion)
}

// hasVersion2Fields reports whether sf holds anything version 1 cannot.
func (sf stateFile) hasVersion2Fields() bool {
	return len(sf.Managed) > 0 || len(sf.Parked) > 0 || (sf.Preferences != nil && len(sf.Preferences.Targets) > 0)
}

// Store persists skillhub's state, operation log and journal.
type Store struct {
	paths paths.Paths
	mu    sync.Mutex
	// link publishes the state.v1.json copy; tests replace it to inject a
	// failure.
	link func(oldname, newname string) error
}

// New builds a Store using locations from p.
func New(p paths.Paths) *Store {
	return &Store{paths: p, link: os.Link}
}

// Lock takes the advisory state lock, waiting briefly for another holder.
func (s *Store) Lock(ctx context.Context) (func() error, error) {
	lockPath := s.paths.StateLockFile()
	if err := s.paths.Check(lockPath); err != nil {
		return nil, fmt.Errorf("state lock: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create state lock dir: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}

	deadline := time.Now().Add(defaultLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() error {
				var releaseErr error
				once.Do(func() {
					unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					closeErr := f.Close()
					releaseErr = errors.Join(unlockErr, closeErr)
				})
				return releaseErr
			}, nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("flock state lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ports.ErrStateLocked
		}

		timer := time.NewTimer(lockPollEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, fmt.Errorf("%w: %w", ports.ErrStateLocked, ctx.Err())
		case <-timer.C:
		}
	}
}

// Load reads the persisted candidates, skips, check moments and notified marks.
func (s *Store) Load() (domain.PersistedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.readStateFile()
	if err != nil {
		return domain.PersistedState{}, err
	}
	if !cur.exists {
		return domain.PersistedState{}, nil
	}
	sf := cur.sf
	st := domain.PersistedState{
		Candidates: sf.Candidates,
		Skips:      sf.Skips,
		Checks:     sf.Checks,
		Notified:   sf.Notified,
		Results:    sf.Results,
		Managed:    sf.Managed,
		Parked:     sf.Parked,
	}
	if sf.Preferences != nil {
		st.Preferences = *sf.Preferences
	}
	return st, nil
}

// Save writes state to state.json while keeping any operation in progress intact.
func (s *Store) Save(state domain.PersistedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.readStateFile()
	if err != nil {
		return err
	}
	sf := cur.sf
	sf.Candidates = state.Candidates
	sf.Skips = state.Skips
	sf.Checks = state.Checks
	sf.Notified = state.Notified
	sf.Results = state.Results
	sf.Managed = state.Managed
	sf.Parked = state.Parked
	sf.Preferences = nil
	if prefs := state.Preferences; len(prefs.Targets) > 0 {
		sf.Preferences = &prefs
	}
	return s.writeStateFile(cur, sf)
}

// Operation returns the operation in progress and whether one is recorded.
func (s *Store) Operation() (domain.Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.readStateFile()
	if err != nil {
		return domain.Operation{}, false, err
	}
	if !cur.exists || cur.sf.Operation == nil {
		return domain.Operation{}, false, nil
	}
	return *cur.sf.Operation, true, nil
}

// RecordOperation persists op as the operation in progress.
func (s *Store) RecordOperation(op domain.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.readStateFile()
	if err != nil {
		return err
	}
	sf := cur.sf
	sf.Operation = &op
	return s.writeStateFile(cur, sf)
}

// ClearOperation removes the operation in progress from state.json.
func (s *Store) ClearOperation() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.readStateFile()
	if err != nil {
		return err
	}
	if !cur.exists || cur.sf.Operation == nil {
		return nil
	}
	sf := cur.sf
	sf.Operation = nil
	return s.writeStateFile(cur, sf)
}

// AppendJournal appends one JSON line to journal.jsonl without rewriting prior lines.
// If the existing file does not end with a newline, a newline is written before the entry.
func (s *Store) AppendJournal(entry domain.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.paths.JournalFile()
	if err := s.paths.Check(path); err != nil {
		return fmt.Errorf("append journal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create journal dir: %w", err)
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal journal entry: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}

	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		var last [1]byte
		if _, readErr := f.ReadAt(last[:], info.Size()-1); readErr == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}

	_, writeErr := f.Write(line)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	return nil
}

// Journal reads all valid entries from journal.jsonl, oldest first, skipping
// any corrupt or truncated lines. Call JournalWithSkipped to inspect the count
// of skipped lines.
func (s *Store) Journal() ([]domain.HistoryEntry, error) {
	entries, _, err := s.JournalWithSkipped()
	return entries, err
}

// JournalWithSkipped reads all valid entries from journal.jsonl, oldest first,
// skipping any corrupt or truncated lines and returning how many were skipped.
func (s *Store) JournalWithSkipped() ([]domain.HistoryEntry, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.paths.JournalFile()
	if err := s.paths.Check(path); err != nil {
		return nil, 0, fmt.Errorf("read journal: %w", err)
	}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.HistoryEntry{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open journal: %w", err)
	}
	defer f.Close()

	entries := []domain.HistoryEntry{}
	skipped := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxJournalLine)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var entry domain.HistoryEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			skipped++
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan journal: %w", err)
	}
	return entries, skipped, nil
}

func (s *Store) readStateFile() (current, error) {
	path := s.paths.StateFile()
	if err := s.paths.Check(path); err != nil {
		return current{}, fmt.Errorf("read state: %w", err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return current{}, nil
	}
	if err != nil {
		return current{}, fmt.Errorf("read state file: %w", err)
	}
	sf, err := decodeStateFile(data)
	if err != nil {
		return current{}, fmt.Errorf("decode state file: %w", err)
	}
	return current{sf: sf, raw: data, exists: true}, nil
}

func decodeStateFile(data []byte) (stateFile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var sf stateFile
	if err := dec.Decode(&sf); err != nil {
		return stateFile{}, err
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return stateFile{}, errors.New("unexpected trailing data")
	}
	if sf.Version == nil || *sf.Version == legacyVersion {
		if sf.hasVersion2Fields() || sf.Preferences != nil {
			return stateFile{}, errors.New("version 2 fields in a version 1 state")
		}
		return sf, nil
	}
	if *sf.Version != stateVersion {
		return stateFile{}, fmt.Errorf("%w: %d", ErrUnsupportedStateVersion, *sf.Version)
	}
	return sf, nil
}

// writeStateFile writes sf over cur. A version 1 file that records an
// operation stays version 1; any other version 1 file is migrated, after
// its copy in state.v1.json.
func (s *Store) writeStateFile(cur current, sf stateFile) error {
	path := s.paths.StateFile()
	if err := s.paths.Check(path); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	removeLegacyTemps(filepath.Dir(path))
	v := stateVersion
	switch {
	case cur.legacy() && cur.sf.Operation != nil:
		if sf.hasVersion2Fields() {
			return ErrMigrationBlocked
		}
		v = legacyVersion
	case cur.legacy():
		if err := s.keepLegacyCopy(cur.raw); err != nil {
			return err
		}
	}
	sf.Version = &v
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state file: %w", err)
	}
	data = append(data, '\n')
	if _, err := decodeStateFile(data); err != nil {
		return fmt.Errorf("verify encoded state: %w", err)
	}
	return writeFileAtomic(path, data, 0o644)
}

// keepLegacyCopy writes raw, the version 1 state.json, to state.v1.json
// unless that file already exists: the copy is never overwritten. The copy
// is published with a hard link, which fails when the name exists, so a
// copy that appears meanwhile is kept too.
func (s *Store) keepLegacyCopy(raw []byte) error {
	path := filepath.Join(filepath.Dir(s.paths.StateFile()), legacyCopyName)
	if err := s.paths.Check(path); err != nil {
		return fmt.Errorf("copy version 1 state: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat version 1 state copy: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, legacyTempPattern)
	if err != nil {
		return fmt.Errorf("create version 1 state copy: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // the temporary name goes either way; the copy is the link.

	_, writeErr := tmp.Write(raw)
	chmodErr := tmp.Chmod(0o644)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write version 1 state copy: %w", err)
	}
	if err := s.link(tmpName, path); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("publish version 1 state copy: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync version 1 state copy dir: %w", err)
	}
	return nil
}

// removeLegacyTemps removes the temporary names keepLegacyCopy leaves when
// a crash interrupts it. Writes run under the state lock, so no other
// keepLegacyCopy owns them. Only non-directory entries matching
// legacyTempPattern go: state.v1.json never matches, and a temporary name
// that is a second link to it removes the name, not the copy. Cleanup is
// best effort: a name that cannot go now is retried by the next write and
// never blocks this one.
func removeLegacyTemps(dir string) {
	names, err := filepath.Glob(filepath.Join(dir, legacyTempPattern))
	if err != nil {
		return
	}
	for _, name := range names {
		info, err := os.Lstat(name)
		if err != nil || info.IsDir() {
			continue
		}
		_ = os.Remove(name)
	}
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	cleanup = false
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync parent dir: %w", err)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	return errors.Join(syncErr, closeErr)
}
