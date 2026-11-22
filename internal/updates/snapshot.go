package updates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// keptSnapshots is how many snapshots per target survive pruning; one with
// keep set is never pruned (docs/design.md section 7).
const keptSnapshots = 5

// Inside a snapshot directory: the folder copy, the lock entry's exact bytes
// and the metadata. The entry has its own file because encoding/json would
// rewrite it (it escapes <, > and &), and restoring it must be byte
// identical. The metadata is written last, so a snapshot without it is
// incomplete: it was interrupted before the store changed.
const (
	snapshotFolder    = "folder"
	snapshotLockEntry = "lock-entry.json"
	snapshotMeta      = "snapshot.json"
)

// snapshotRecord is a snapshot with what restoring it needs.
type snapshotRecord struct {
	domain.Snapshot
	// LockName is the lock entry the snapshot restores; empty for a clone.
	LockName string `json:"lock_name"`
	// FolderPath is the installed folder, or the clone root, the copy goes
	// back to.
	FolderPath string `json:"folder_path"`
	// FolderSHA identifies the copy's content: its tree SHA, or for a clone
	// the work tree and .git together (cloneSHA).
	FolderSHA string `json:"folder_sha"`
	// TreeSHA is the tree of the clone's HEAD in the copy; empty for a lock
	// skill, whose FolderSHA is its tree.
	TreeSHA string `json:"tree_sha,omitempty"`
}

// contentSHA is the tree SHA the snapshot puts back, as the journal shows it.
func (r snapshotRecord) contentSHA() string {
	if r.TreeSHA != "" {
		return r.TreeSHA
	}
	return r.FolderSHA
}

// folderSHA identifies the content of dir as a snapshot of target records
// it: a tree SHA, or cloneSHA for a clone.
func (s *Service) folderSHA(target, dir string) (string, error) {
	if isCloneTarget(target) {
		return s.cloneSHA(dir)
	}
	tree, err := s.d.HashDir(dir)
	if err != nil {
		return "", err
	}
	return tree.SHA, nil
}

func (s *Service) snapshotDir(id string) string { return filepath.Join(s.d.Paths.SnapshotsDir(), id) }

// tmpRoot holds the folders an apply, an undo or a recover swaps in and out
// of the store: next to it, on the same file system, and outside skills/ so
// no harness discovers them (docs/design.md section 7, step 8).
func (s *Service) tmpRoot() string {
	return filepath.Join(filepath.Dir(s.d.Paths.StoreDir()), ".skillhub-tmp")
}

// tmpRootFor is tmpRoot for any folder of a skills directory: next to that
// directory, so a clone in ~/.claude/skills swaps through ~/.claude.
func tmpRootFor(folder string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(folder)), ".skillhub-tmp")
}

// tmpRootOf is the swap folder of a target; "" when the target names no
// folder skillhub could have swapped.
func (s *Service) tmpRootOf(target string) string {
	if !isCloneTarget(target) {
		return s.tmpRoot()
	}
	root, err := s.cloneRoot(target)
	if err != nil {
		return ""
	}
	return tmpRootFor(root)
}

// newSnapshotID returns a fresh id: the time, then the target.
func (s *Service) newSnapshotID(now time.Time, target string) (string, error) {
	base := now.UTC().Format("20060102T150405.000000000Z") + "-" + sanitize(target)
	for n := 1; ; n++ {
		id := base
		if n > 1 {
			id += "-" + strconv.Itoa(n)
		}
		exists, err := s.d.Files.Exists(s.snapshotDir(id))
		if err != nil {
			return "", fmt.Errorf("look for snapshot %s: %w", id, err)
		}
		if !exists {
			return id, nil
		}
	}
}

// sanitize keeps a target id usable as one path element.
func sanitize(target string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, target)
}

// takeSnapshot copies folder and records entry, the lock entry's exact JSON.
// The copy must hash to sha, the folder's SHA read before, or the folder
// changed while it was copied. On failure, the partial snapshot is removed.
func (s *Service) takeSnapshot(rec snapshotRecord) (snapshotRecord, error) {
	dir := s.snapshotDir(rec.ID)
	rec.FolderCopyPath = filepath.Join(dir, snapshotFolder)
	err := s.writeSnapshot(rec)
	if err != nil {
		return snapshotRecord{}, errors.Join(fmt.Errorf("snapshot %s: %w", rec.ID, err), s.d.Files.RemoveTree(dir))
	}
	return rec, nil
}

func (s *Service) writeSnapshot(rec snapshotRecord) error {
	if err := s.d.Files.CopyTree(rec.FolderPath, rec.FolderCopyPath); err != nil {
		return fmt.Errorf("copy %s: %w", rec.FolderPath, err)
	}
	copied, err := s.folderSHA(rec.Target, rec.FolderCopyPath)
	if err != nil {
		return fmt.Errorf("hash the copy: %w", err)
	}
	if copied != rec.FolderSHA {
		return fmt.Errorf("%w: the folder changed while it was copied", ErrInstalledChanged)
	}
	if len(rec.LockEntryBefore) > 0 {
		entry := filepath.Join(s.snapshotDir(rec.ID), snapshotLockEntry)
		if err := s.d.Files.WriteFileAtomic(entry, rec.LockEntryBefore, 0o644); err != nil {
			return fmt.Errorf("save the lock entry: %w", err)
		}
	}
	meta := rec
	meta.LockEntryBefore = nil
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return s.d.Files.WriteFileAtomic(filepath.Join(s.snapshotDir(rec.ID), snapshotMeta), append(data, '\n'), 0o644)
}

// readSnapshot reads a complete snapshot. A snapshot without metadata, or
// one that does not exist, is ErrSnapshotNotFound. Paths are rebuilt from
// the id and checked rather than trusted from the file.
func (s *Service) readSnapshot(id string) (snapshotRecord, error) {
	if id == "" || sanitize(id) != id {
		return snapshotRecord{}, fmt.Errorf("%q: %w", id, ErrSnapshotNotFound)
	}
	data, err := s.d.Files.ReadFile(filepath.Join(s.snapshotDir(id), snapshotMeta))
	if errors.Is(err, fs.ErrNotExist) {
		return snapshotRecord{}, fmt.Errorf("%s: %w", id, ErrSnapshotNotFound)
	}
	if err != nil {
		return snapshotRecord{}, fmt.Errorf("read snapshot %s: %w", id, err)
	}
	var rec snapshotRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return snapshotRecord{}, fmt.Errorf("snapshot %s: %w", id, err)
	}
	if rec.ID != id || !s.validRecord(rec) {
		return snapshotRecord{}, fmt.Errorf("snapshot %s: %w: its record is not valid", id, ErrSnapshotNotFound)
	}
	rec.FolderCopyPath = filepath.Join(s.snapshotDir(id), snapshotFolder)
	entry, err := s.d.Files.ReadFile(filepath.Join(s.snapshotDir(id), snapshotLockEntry))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		rec.LockEntryBefore = nil
	case err != nil:
		return snapshotRecord{}, fmt.Errorf("read the lock entry of snapshot %s: %w", id, err)
	default:
		rec.LockEntryBefore = entry
	}
	return rec, nil
}

// validRecord accepts a snapshot whose paths are those its target names: a
// store folder for a lock entry, the clone root for a clone.
func (s *Service) validRecord(rec snapshotRecord) bool {
	if isCloneTarget(rec.Target) {
		root, err := s.cloneRoot(rec.Target)
		return err == nil && rec.FolderPath == root && rec.LockName == "" && rec.TreeSHA != ""
	}
	return validName(rec.LockName) && rec.Target == domain.LockID(rec.LockName) &&
		filepath.Dir(rec.FolderPath) == s.d.Paths.StoreDir() && validName(filepath.Base(rec.FolderPath)) &&
		filepath.Base(rec.FolderPath) != offDirName
}

// snapshots returns the complete snapshots of target, newest first.
func (s *Service) snapshots(target string) ([]snapshotRecord, error) {
	entries, err := s.d.Dirs.Scan(s.d.Paths.SnapshotsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []snapshotRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	out := []snapshotRecord{}
	for _, e := range entries {
		if e.Kind != ports.EntryDir {
			continue
		}
		rec, err := s.readSnapshot(e.Name)
		if errors.Is(err, ErrSnapshotNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if rec.Target == target {
			out = append(out, rec)
		}
	}
	slices.SortFunc(out, func(a, b snapshotRecord) int {
		if c := b.TakenAt.Compare(a.TakenAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

// Snapshots returns the snapshots of target, newest first.
func (s *Service) Snapshots(target string) ([]domain.Snapshot, error) {
	recs, err := s.snapshots(target)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Snapshot, len(recs))
	for i, r := range recs {
		out[i] = r.Snapshot
	}
	return out, nil
}

// prune removes the snapshots of target beyond the newest keptSnapshots,
// except those with keep set. Pruning frees disk space only, so a failure is
// left for the next prune.
func (s *Service) prune(target string) {
	recs, err := s.snapshots(target)
	if err != nil {
		return
	}
	for i, r := range recs {
		if i >= keptSnapshots && !r.Keep {
			_ = s.d.Files.RemoveTree(s.snapshotDir(r.ID)) //nolint:errcheck // see the doc comment.
		}
	}
}

// restore puts the snapshot's folder and lock entry back. The folder is
// swapped only when it differs. The lock entry is rewritten only when it
// differs and ours accepts it as skillhub's own write (nil accepts any);
// it is never recreated when another tool removed it, and never written when
// its ref changed, since skillhub never changes a ref. Those cases are not
// failures: the folder is back, and note says what the lock kept.
func (s *Service) restore(rec snapshotRecord, ours func(domain.LockEntry) bool) (note string, err error) {
	files := s.d.Files
	tmp := tmpRootFor(rec.FolderPath)
	name := filepath.Base(rec.FolderPath)
	if rec.LockName != "" {
		name = rec.LockName
	}
	if sha, err := s.folderSHA(rec.Target, rec.FolderPath); err != nil || sha != rec.FolderSHA {
		back := filepath.Join(tmp, name+".restore")
		gone := filepath.Join(tmp, name+".discard")
		if err := errors.Join(files.RemoveTree(back), files.RemoveTree(gone)); err != nil {
			return "", fmt.Errorf("clear %s: %w", tmp, err)
		}
		if err := files.CopyTree(rec.FolderCopyPath, back); err != nil {
			return "", fmt.Errorf("copy the snapshot: %w", err)
		}
		exists, err := files.Exists(rec.FolderPath)
		if err != nil {
			return "", fmt.Errorf("look for %s: %w", rec.FolderPath, err)
		}
		if exists {
			if err := files.Rename(rec.FolderPath, gone); err != nil {
				return "", fmt.Errorf("move the current folder aside: %w", err)
			}
		}
		if err := files.Rename(back, rec.FolderPath); err != nil {
			return "", fmt.Errorf("put the snapshot in place: %w", err)
		}
	}
	if len(rec.LockEntryBefore) > 0 {
		if note, err = s.restoreEntry(rec, ours); err != nil {
			return "", err
		}
	}
	if err := files.RemoveTree(tmp); err != nil {
		return "", fmt.Errorf("clear %s: %w", tmp, err)
	}
	sha, err := s.folderSHA(rec.Target, rec.FolderPath)
	if err != nil {
		return "", fmt.Errorf("%w: hash the restored folder: %w", ErrVerifyFailed, err)
	}
	if sha != rec.FolderSHA {
		return "", fmt.Errorf("%w: restored folder is %s, want %s", ErrVerifyFailed, sha, rec.FolderSHA)
	}
	return note, nil
}

// Notes of a restore whose lock entry was left as another tool made it.
const (
	noteEntryRemoved = "lock entry removed by another tool; not recreated"
	noteRefChanged   = "lock entry ref changed by another tool; left as is"
	noteEntryChanged = "lock entry rewritten by another tool; left as is"
)

func (s *Service) restoreEntry(rec snapshotRecord, ours func(domain.LockEntry) bool) (string, error) {
	lock, err := s.d.Lock.Read()
	if err != nil {
		return "", fmt.Errorf("read the skills lock: %w", err)
	}
	entry, ok := findEntry(lock, rec.LockName)
	if !ok {
		return noteEntryRemoved, nil
	}
	if bytes.Equal(entry.Raw, rec.LockEntryBefore) {
		return "", nil
	}
	if entry.Ref != rawRef(rec.LockEntryBefore) {
		return noteRefChanged, nil
	}
	if ours != nil && !ours(entry) {
		return noteEntryChanged, nil
	}
	if err := s.d.Lock.RestoreEntry(rec.LockName, rec.LockEntryBefore); err != nil {
		return "", fmt.Errorf("restore the lock entry: %w", err)
	}
	return "", nil
}

// rawRef is the ref of an entry's exact JSON; "" when it has none.
func rawRef(raw []byte) string {
	var e struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	return e.Ref
}

func findEntry(lock domain.SkillsLock, name string) (domain.LockEntry, bool) {
	i := slices.IndexFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == name })
	if i < 0 {
		return domain.LockEntry{}, false
	}
	return lock.Entries[i], true
}
