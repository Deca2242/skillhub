// Package skillslock is the real ports.SkillsLockStore: it reads and
// rewrites the global lock of npx skills under the compatibility rules of
// docs/research/skills-cli-internals.md, consequences 2.
//
//   - The lock path comes from paths.Paths, which follows XDG_STATE_HOME the
//     way npx skills does (fact 1).
//   - Only a lock whose version is exactly the number 3 is written; any other
//     version is refused with ports.ErrUnsupportedLockVersion, never
//     "upgraded" (fact 2).
//   - The file goes through an orderedjson.Document, so key order, unknown
//     keys (skillName, dismissed, lastSelectedAgents, anything newer), exact
//     scalars, two-space indentation and the final newline all survive.
//   - An update rewrites skillFolderHash, updatedAt and, only when the entry
//     already has one, commit (facts 13 and 14). It never adds, changes or
//     removes ref, and it keeps installedAt.
//   - npx skills rewrites the lock with no lock of its own (fact 3), so every
//     write re-reads the file right before writing and compares it with what
//     it read first. A change is retried once from the new content; a second
//     change fails with ports.ErrLockChanged and writes nothing.
//
// Every file access goes through ports.FileOps, whose WriteFileAtomic writes a
// temporary file in the lock's directory and renames it into place.
//
// Store is also ports.LockEntries, the version 2 whole-entry moves allowed by
// the lock exception of L32; lockentries.go states those rules.
package skillslock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

var (
	// ErrMalformedLock reports a lock that is valid JSON of the wrong shape,
	// or not JSON at all.
	ErrMalformedLock = ports.ErrMalformedLock
	// ErrRefMismatch reports a restore whose entry would add, change or
	// remove the ref the lock holds now.
	ErrRefMismatch = errors.New("restored skills lock entry would change its ref")
	// ErrInvalidUpdate reports an update missing a value it must write.
	ErrInvalidUpdate = errors.New("invalid skills lock entry update")
)

const (
	// supportedVersion is the only lock version the store writes.
	supportedVersion = 3
	// timeLayout is how npx skills writes times: JavaScript's toISOString.
	timeLayout = "2006-01-02T15:04:05.000Z"
	// missingRevision is the revision of a lock file that does not exist.
	missingRevision = "missing"
	// defaultPerm is the mode of a lock whose mode cannot be read.
	defaultPerm fs.FileMode = 0o644
)

// Lock keys.
const (
	keyVersion    = "version"
	keySkills     = "skills"
	keyRef        = "ref"
	keyCommit     = "commit"
	keyFolderHash = "skillFolderHash"
	keyUpdatedAt  = "updatedAt"
)

// Store is the global lock of npx skills.
type Store struct {
	path  string
	files ports.FileOps
}

var _ ports.SkillsLockStore = (*Store)(nil)

// New builds a store for the lock at p.SkillsLockFile(); every read and write
// goes through files.
func New(p paths.Paths, files ports.FileOps) *Store {
	return &Store{path: p.SkillsLockFile(), files: files}
}

// Read implements ports.SkillsLockStore. A missing file is an empty lock of
// version 3. Another version is reported as found, not refused: only writes
// refuse it. A version that is not an integer is reported as 0.
func (s *Store) Read() (domain.SkillsLock, error) {
	data, err := s.files.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return domain.SkillsLock{Version: supportedVersion, Entries: []domain.LockEntry{}, Revision: missingRevision}, nil
	}
	if err != nil {
		return domain.SkillsLock{}, fmt.Errorf("read skills lock: %w", err)
	}
	p, err := parse(data)
	if err != nil {
		return domain.SkillsLock{}, err
	}
	lock := domain.SkillsLock{Entries: p.entries, Revision: revision(data)}
	if v, ok := p.doc.Root().Get(keyVersion); ok {
		if n, err := v.Int(); err == nil && n >= 0 && n <= math.MaxInt32 {
			lock.Version = int(n)
		}
	}
	return lock, nil
}

// UpdateEntry implements ports.SkillsLockStore.
func (s *Store) UpdateEntry(name string, update ports.LockEntryUpdate) error {
	if update.FolderHash == "" || update.UpdatedAt.IsZero() {
		return fmt.Errorf("update %s: %w: folder hash and time are required", name, ErrInvalidUpdate)
	}
	return s.rewrite(name, func(entry *orderedjson.Value) (*orderedjson.Value, error) {
		_, hasCommit := entry.Get(keyCommit)
		if hasCommit && update.Commit == "" {
			return nil, fmt.Errorf("update %s: %w: the entry has a commit and the update has none", name, ErrInvalidUpdate)
		}
		// Set keeps each existing key in its place. ref and installedAt are
		// never touched; commit is written only when the entry has one.
		err := errors.Join(
			entry.Set(keyFolderHash, orderedjson.String(update.FolderHash)),
			entry.Set(keyUpdatedAt, orderedjson.String(update.UpdatedAt.UTC().Format(timeLayout))),
		)
		if err == nil && hasCommit {
			err = entry.Set(keyCommit, orderedjson.String(update.Commit))
		}
		if err != nil {
			return nil, fmt.Errorf("update %s: %w", name, err)
		}
		return entry, nil
	})
}

// RestoreEntry implements ports.SkillsLockStore. The entry must still be in
// the lock, and raw must carry the same ref as the entry does now, so a
// restore never adds, changes or removes a ref.
func (s *Store) RestoreEntry(name string, raw []byte) error {
	saved, err := orderedjson.ParseValue(raw)
	if err != nil {
		return fmt.Errorf("restore %s: %w: %w", name, ErrMalformedLock, err)
	}
	if saved.Kind() != orderedjson.KindObject {
		return fmt.Errorf("restore %s: %w: the entry is a %s", name, ErrMalformedLock, saved.Kind())
	}
	return s.rewrite(name, func(entry *orderedjson.Value) (*orderedjson.Value, error) {
		if !sameRef(entry, saved) {
			return nil, fmt.Errorf("restore %s: %w", name, ErrRefMismatch)
		}
		return saved, nil
	})
}

// editFunc receives the current entry and returns the entry to write in its
// place; it may modify and return the entry it received.
type editFunc func(entry *orderedjson.Value) (*orderedjson.Value, error)

// rewrite reads the lock, replaces entry name with what edit returns, and
// writes the result if the file did not change meanwhile. One change is
// retried from the new content; a second one fails with ErrLockChanged.
func (s *Store) rewrite(name string, edit editFunc) error {
	return s.commit(name, func(data []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, fmt.Errorf("%s: %w", name, ports.ErrLockEntryNotFound)
		}
		return edited(data, name, edit)
	})
}

// buildFunc returns the next content of the lock from its current content;
// exists is false, and data nil, when the file does not exist.
type buildFunc func(data []byte, exists bool) ([]byte, error)

// commit reads the lock, builds its next content and writes it if the file
// did not change meanwhile, keeping its mode. A file that changed between
// the read and the re-read before writing, appeared or disappeared included,
// is built again from the new content once; a second change fails with
// ErrLockChanged and writes nothing.
func (s *Store) commit(name string, build buildFunc) error {
	for attempt := 0; attempt < 2; attempt++ {
		read, exists, err := s.readLock()
		if err != nil {
			return fmt.Errorf("read skills lock: %w", err)
		}
		next, err := build(read, exists)
		if err != nil {
			return err
		}
		again, existsAgain, err := s.readLock()
		if err != nil {
			return fmt.Errorf("re-read skills lock: %w", err)
		}
		if existsAgain != exists || !bytes.Equal(again, read) {
			continue // npx skills wrote meanwhile: start over from its lock
		}
		perm := defaultPerm
		if mode, err := s.files.Mode(s.path); err == nil {
			perm = mode.Perm()
		}
		if err := s.files.WriteFileAtomic(s.path, next, perm); err != nil {
			return fmt.Errorf("write skills lock: %w", err)
		}
		return nil
	}
	return fmt.Errorf("%s: %w", name, ports.ErrLockChanged)
}

// readLock reads the lock file; a missing file is not an error.
func (s *Store) readLock() (data []byte, exists bool, err error) {
	data, err = s.files.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// edited returns the lock data with entry name edited, refusing a version
// other than 3 and a missing entry.
func edited(data []byte, name string, edit editFunc) ([]byte, error) {
	p, err := parse(data)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(p.doc.Root()); err != nil {
		return nil, err
	}
	var entry *orderedjson.Value
	if p.skills != nil {
		entry, _ = p.skills.Get(name)
	}
	if entry == nil {
		return nil, fmt.Errorf("%s: %w", name, ports.ErrLockEntryNotFound)
	}
	next, err := edit(entry)
	if err != nil {
		return nil, err
	}
	if err := p.skills.Set(name, next); err != nil {
		return nil, fmt.Errorf("entry %s: %w", name, err)
	}
	return p.doc.Bytes(), nil
}

// parsed is a lock checked for shape: every entry decodes.
type parsed struct {
	doc *orderedjson.Document
	// skills is the skills object, nil when the lock has none.
	skills *orderedjson.Value
	// entries are the decoded entries, in the file's order.
	entries []domain.LockEntry
}

// parse reads the lock and decodes its entries, so reading and writing
// refuse the same malformed locks.
func parse(data []byte) (parsed, error) {
	doc, err := orderedjson.Parse(data)
	if err != nil {
		return parsed{}, fmt.Errorf("%w: %w", ErrMalformedLock, err)
	}
	if doc.Root().Kind() != orderedjson.KindObject {
		return parsed{}, fmt.Errorf("%w: the lock is a %s", ErrMalformedLock, doc.Root().Kind())
	}
	p := parsed{doc: doc, entries: []domain.LockEntry{}}
	skills, ok := doc.Root().Get(keySkills)
	if !ok {
		return p, nil
	}
	if skills.Kind() != orderedjson.KindObject {
		return parsed{}, fmt.Errorf("%w: skills is a %s", ErrMalformedLock, skills.Kind())
	}
	p.skills = skills
	for _, name := range skills.Keys() {
		v, _ := skills.Get(name)
		entry, err := decodeEntry(name, v)
		if err != nil {
			return parsed{}, err
		}
		p.entries = append(p.entries, entry)
	}
	return p, nil
}

// checkVersion accepts only the number literal 3.
func checkVersion(root *orderedjson.Value) error {
	v, ok := root.Get(keyVersion)
	if !ok {
		return fmt.Errorf("no version: %w", ports.ErrUnsupportedLockVersion)
	}
	if v.Kind() != orderedjson.KindNumber {
		return fmt.Errorf("version is a %s: %w", v.Kind(), ports.ErrUnsupportedLockVersion)
	}
	literal, _ := v.Literal()
	if literal != "3" {
		return fmt.Errorf("version %s: %w", literal, ports.ErrUnsupportedLockVersion)
	}
	return nil
}

// sameRef reports whether a and b both lack ref or carry the same ref bytes.
func sameRef(a, b *orderedjson.Value) bool {
	ra, okA := a.Get(keyRef)
	rb, okB := b.Get(keyRef)
	if okA != okB {
		return false
	}
	return !okA || bytes.Equal(ra.Bytes(), rb.Bytes())
}

// decodeEntry decodes the fields skillhub uses; Raw keeps the whole entry.
func decodeEntry(name string, v *orderedjson.Value) (domain.LockEntry, error) {
	if v.Kind() != orderedjson.KindObject {
		return domain.LockEntry{}, fmt.Errorf("entry %s: %w: not an object", name, ErrMalformedLock)
	}
	e := domain.LockEntry{Name: name, Raw: v.Bytes()}
	fields := []struct {
		key string
		dst *string
	}{
		{"source", &e.Source},
		{"sourceType", &e.SourceType},
		{"sourceUrl", &e.SourceURL},
		{"skillPath", &e.SkillPath},
		{keyFolderHash, &e.SkillFolderHash},
		{keyRef, &e.Ref},
		{keyCommit, &e.Commit},
		{"installedAt", &e.InstalledAt},
		{keyUpdatedAt, &e.UpdatedAt},
	}
	for _, f := range fields {
		fv, ok := v.Get(f.key)
		if !ok {
			continue
		}
		text, err := fv.Text()
		if err != nil {
			return domain.LockEntry{}, fmt.Errorf("entry %s, %s: %w: %w", name, f.key, ErrMalformedLock, err)
		}
		*f.dst = text
	}
	return e, nil
}

// revision identifies the bytes of a lock file.
func revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
