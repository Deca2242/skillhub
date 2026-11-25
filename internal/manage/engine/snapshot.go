package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"skillhub/internal/shared/domain"
)

// A version 2 snapshot lives in its own directory under SnapshotsDir(): the
// copies of the folders the plan removes or moves away under trees/<n>, and
// snapshot.json, written last, with the link targets, the lock entries and
// the lock's existence. A directory without snapshot.json is incomplete. The
// lock entries are kept as []byte (base64 in JSON) so they come back byte
// for byte. Version 1 skips these directories: their metadata has no "id"
// matching the directory name.
const (
	snapshotFormat  = "skillhub.snapshot"
	snapshotVersion = 2
	snapshotMeta    = "snapshot.json"
	snapshotTrees   = "trees"
	// snapshotPrefix starts every version 2 snapshot id.
	snapshotPrefix = "op-"
)

// TreeCopy is a folder the snapshot keeps: where it was, its tree SHA (or
// complete whole-clone digest for a clone) and its copy, relative to the
// snapshot directory.
type TreeCopy struct {
	Path     string `json:"path"`
	TreeSHA  string `json:"tree_sha,omitempty"`
	CloneSHA string `json:"clone_sha,omitempty"`
	Copy     string `json:"copy"`
}

// LinkCopy is a symlink the snapshot keeps: its path and exact content.
type LinkCopy struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// EntryCopy is a lock entry as it was before the operation: its bytes
// (StateEntry), absent from the lock (StateAbsent) or with no lock at all
// (StateNoLock).
type EntryCopy struct {
	Name  string     `json:"name"`
	State ImageState `json:"state"`
	Raw   []byte     `json:"raw,omitempty"`
}

// Snapshot is everything an operation's plan touches, as it was before the
// operation.
type Snapshot struct {
	Format     string      `json:"format"`
	Version    int         `json:"version"`
	SnapshotID string      `json:"snapshot_id"`
	PlanID     string      `json:"plan_id"`
	TakenAt    time.Time   `json:"taken_at"`
	Trees      []TreeCopy  `json:"trees"`
	Links      []LinkCopy  `json:"links"`
	Entries    []EntryCopy `json:"entries"`
	// LockExists and LockSHA are the lock before the operation, when the plan
	// touches it.
	LockExists bool   `json:"lock_exists"`
	LockSHA    string `json:"lock_sha,omitempty"`
	// LockKeys are the lock's entries in order before the operation, so a
	// rollback puts a taken entry back between the same neighbours even when
	// the step's result was not recorded.
	LockKeys []string `json:"lock_keys"`
	// State is skillhub's state before the operation, for the keys the
	// plan's effect touches.
	State StateImage `json:"state"`
	// Plan is the plan the snapshot was taken for; everything above must be
	// what it says, and Undo builds its inverse from it.
	Plan Plan `json:"plan"`
	// MissingDirs are the levels a move may create (~/.agents/.skillhub, the
	// parking zone and the store) that did not exist before the operation; a
	// rollback, or the undo of the operation, removes the ones it created,
	// once they are empty again.
	MissingDirs []string `json:"missing_dirs,omitempty"`
}

// SnapshotDir is the directory of snapshot id.
func (e *Engine) SnapshotDir(id string) string {
	return filepath.Join(e.d.Paths.SnapshotsDir(), id)
}

// validSnapshotID accepts what newSnapshotID makes: the prefix, then
// letters, digits, '.', '-' and '_'.
func validSnapshotID(id string) bool {
	if !strings.HasPrefix(id, snapshotPrefix) || len(id) == len(snapshotPrefix) {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// newSnapshotID returns a fresh id: the prefix, the time and the start of
// the plan id.
func (e *Engine) newSnapshotID(planID string) (string, error) {
	short := planID
	if len(short) > 12 {
		short = short[:12]
	}
	base := snapshotPrefix + e.d.Clock.Now().UTC().Format("20060102T150405.000000000Z") + "-" + short
	for n := 1; ; n++ {
		id := base
		if n > 1 {
			id += "-" + strconv.Itoa(n)
		}
		exists, err := e.d.Files.Exists(e.SnapshotDir(id))
		if err != nil {
			return "", fmt.Errorf("look for snapshot %s: %w", id, err)
		}
		if !exists {
			return id, nil
		}
	}
}

// snapshotOf lists what the snapshot of p keeps: every object as the plan
// first sees it. A folder is copied, a link's content and a lock entry's
// bytes or absence are recorded.
func snapshotOf(p Plan) Snapshot {
	snap := Snapshot{Format: snapshotFormat, Version: snapshotVersion, PlanID: p.ID,
		Trees: []TreeCopy{}, Links: []LinkCopy{}, Entries: []EntryCopy{}, LockKeys: []string{},
		State: StateImage{Managed: map[string]domain.ManagedSkill{}, Parked: map[string]domain.ParkedSkill{}},
		Plan:  p}
	seen := map[string]bool{}
	for _, s := range p.Steps {
		if s.Kind == StepRequire {
			continue // it changes nothing, so there is nothing to keep
		}
		for _, img := range s.Pre {
			if seen[img.object()] {
				continue
			}
			seen[img.object()] = true
			switch {
			case img.Entry != "":
				snap.Entries = append(snap.Entries, EntryCopy{Name: img.Entry, State: img.State, Raw: img.Raw})
			case img.State == StateTree:
				copyPath := filepath.ToSlash(filepath.Join(snapshotTrees, strconv.Itoa(len(snap.Trees))))
				snap.Trees = append(snap.Trees, TreeCopy{Path: img.Path, TreeSHA: img.TreeSHA, Copy: copyPath})
			case img.State == StateClone:
				copyPath := filepath.ToSlash(filepath.Join(snapshotTrees, strconv.Itoa(len(snap.Trees))))
				snap.Trees = append(snap.Trees, TreeCopy{Path: img.Path, CloneSHA: img.CloneSHA, Copy: copyPath})
			case img.State == StateLink:
				snap.Links = append(snap.Links, LinkCopy{Path: img.Path, Target: img.Target})
			}
		}
	}
	if p.TouchesLock {
		snap.LockExists = p.LockSHA != ""
		snap.LockSHA = p.LockSHA
	}
	return snap
}

// takeSnapshot copies and records everything p touches, verifies every copy
// and writes snapshot.json last. On failure it removes what it wrote, so no
// partial snapshot stays behind unless the removal fails too; Orphans
// reports that one.
func (e *Engine) takeSnapshot(p Plan) (Snapshot, string, error) {
	id, err := e.newSnapshotID(p.ID)
	if err != nil {
		return Snapshot{}, "", err
	}
	snap := snapshotOf(p)
	snap.SnapshotID = id
	snap.TakenAt = e.d.Clock.Now().UTC()
	if err := e.observeBefore(&snap); err != nil {
		return Snapshot{}, "", err
	}
	sum, err := e.writeSnapshot(snap)
	if err != nil {
		return Snapshot{}, "", errors.Join(fmt.Errorf("snapshot %s: %w", id, err), e.removeOwn(e.SnapshotDir(id)))
	}
	return snap, sum, nil
}

// metaSHA is the SHA-256 of snapshot.json's bytes. The record and the
// journal keep it, so a snapshot.json edited afterwards (its lock order or
// its state image, which nothing else derives) is refused.
func metaSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeSnapshot copies the folders and writes snapshot.json last; it returns
// the hash of snapshot.json.
func (e *Engine) writeSnapshot(snap Snapshot) (string, error) {
	dir := e.SnapshotDir(snap.SnapshotID)
	for _, t := range snap.Trees {
		copyPath := filepath.Join(dir, filepath.FromSlash(t.Copy))
		if snap.Plan.HasProjectScope() {
			proj := snap.Plan.Project
			if err := e.d.Files.MkdirAll(filepath.Dir(copyPath), 0o755); err != nil {
				return "", fmt.Errorf("create copy parent %s: %w", filepath.Dir(copyPath), err)
			}
			copiedTree, err := e.d.ProjectFS.CopySkill(proj.ProjectDir, string(proj.Root), proj.SkillName, copyPath)
			if err != nil {
				return "", fmt.Errorf("copy %s: %w", t.Path, err)
			}
			if copiedTree.SHA != t.TreeSHA {
				return "", fmt.Errorf("%w: %s is %s, the plan saw %s", ErrPlanChanged, t.Path, copiedTree.SHA, t.TreeSHA)
			}
			if err := e.refuseSpecialBits(copyPath, copiedTree); err != nil {
				return "", fmt.Errorf("%w: %w", ErrSnapshotMismatch, err)
			}
			continue
		}
		mode, err := e.d.Files.Mode(t.Path)
		if err != nil {
			return "", fmt.Errorf("look at %s: %w", t.Path, err)
		}
		if !mode.IsDir() {
			return "", fmt.Errorf("%w: %s is no longer a folder", ErrSnapshotMismatch, t.Path)
		}
		if t.CloneSHA != "" {
			srcManifest, err := e.inspectClone(t.Path)
			if err != nil {
				return "", err
			}
			if srcManifest.SHA != t.CloneSHA {
				return "", fmt.Errorf("%w: %s is %s, the plan saw %s", ErrPlanChanged, t.Path, srcManifest.SHA, t.CloneSHA)
			}
		}
		if err := e.d.Files.CopyTree(t.Path, copyPath); err != nil {
			return "", fmt.Errorf("copy %s: %w", t.Path, err)
		}
		if t.CloneSHA != "" {
			if err := e.verifyCloneCopy(t.Path, copyPath, t.CloneSHA); err != nil {
				return "", err
			}
		} else if err := e.verifyCopy(t.Path, copyPath, t.TreeSHA); err != nil {
			return "", err
		}
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("serialize snapshot: %w", err)
	}
	data = append(data, '\n')
	if err := e.d.Files.WriteFileAtomic(filepath.Join(dir, snapshotMeta), data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", snapshotMeta, err)
	}
	return metaSHA(data), nil
}

// verifyCopy checks that copyPath is exactly the folder src the plan saw: the
// tree SHA the plan recorded (bytes, paths, the execute bit and link
// targets), and the same whole mode (type, permission and special bits) on
// the root and on every entry the tree lists.
func (e *Engine) verifyCopy(src, copyPath, sha string) error {
	tree, err := e.d.HashDir(copyPath)
	if err != nil {
		return fmt.Errorf("hash the copy of %s: %w", src, err)
	}
	if tree.SHA != sha {
		return fmt.Errorf("%w: the copy of %s is %s, the plan saw %s", ErrSnapshotMismatch, src, tree.SHA, sha)
	}
	for _, r := range treePaths(tree) {
		a, err := e.d.Files.Mode(filepath.Join(src, r))
		if err != nil {
			return fmt.Errorf("look at %s: %w", filepath.Join(src, r), err)
		}
		b, err := e.d.Files.Mode(filepath.Join(copyPath, r))
		if err != nil {
			return fmt.Errorf("look at the copy of %s: %w", filepath.Join(src, r), err)
		}
		if a != b {
			return fmt.Errorf("%w: %s is %v, its copy is %v", ErrSnapshotMismatch, filepath.Join(src, r), a, b)
		}
	}
	return nil
}

// ReadSnapshot reads a complete version 2 snapshot. A missing or incomplete
// one is ErrSnapshotNotFound; one of an unknown format or version, or whose
// content does not match its directory, is ErrUnsupportedRecord. Copy paths
// are checked against the layout rather than trusted.
func (e *Engine) ReadSnapshot(id string) (Snapshot, error) {
	snap, _, err := e.readSnapshot(id)
	return snap, err
}

// readSnapshot is ReadSnapshot, also returning the hash of snapshot.json.
func (e *Engine) readSnapshot(id string) (Snapshot, string, error) {
	if !validSnapshotID(id) {
		return Snapshot{}, "", fmt.Errorf("%q: %w", id, ErrSnapshotNotFound)
	}
	data, err := e.d.Files.ReadFile(filepath.Join(e.SnapshotDir(id), snapshotMeta))
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, "", fmt.Errorf("%s: %w", id, ErrSnapshotNotFound)
	}
	if err != nil {
		return Snapshot{}, "", fmt.Errorf("read snapshot %s: %w", id, err)
	}
	var snap Snapshot
	if err := decodeStrict(data, &snap); err != nil {
		return Snapshot{}, "", fmt.Errorf("snapshot %s: %w: %w", id, ErrUnsupportedRecord, err)
	}
	if snap.Format != snapshotFormat || snap.Version != snapshotVersion {
		return Snapshot{}, "", fmt.Errorf("snapshot %s: %w: format %q version %d", id, ErrUnsupportedRecord, snap.Format, snap.Version)
	}
	if snap.SnapshotID != id {
		return Snapshot{}, "", fmt.Errorf("snapshot %s: %w: it names itself %q", id, ErrUnsupportedRecord, snap.SnapshotID)
	}
	if err := snap.coherent(); err != nil {
		return Snapshot{}, "", fmt.Errorf("snapshot %s: %w: %w", id, ErrUnsupportedRecord, err)
	}
	return snap, metaSHA(data), nil
}

// observeBefore records what the plan's lock steps and effect need beyond
// the objects: the lock's order and the state the effect overwrites. It runs
// under the state lock, after Check saw the lock the plan was built on.
func (e *Engine) observeBefore(snap *Snapshot) error {
	p := snap.Plan
	for _, dir := range e.ownLevels() {
		exists, err := e.d.Files.Exists(dir)
		if errors.Is(err, syscall.ENOTDIR) {
			// A level above it is a file: it is missing, and removeIfEmpty
			// never touches what is there (A2 of P4).
			exists, err = false, nil
		}
		if err != nil {
			return fmt.Errorf("look at %s: %w", dir, err)
		}
		if !exists {
			snap.MissingDirs = append(snap.MissingDirs, dir)
		}
	}
	if p.TouchesLock {
		v := e.newView()
		if err := v.loadLock(); err != nil {
			return err
		}
		if v.lockHash != p.LockSHA {
			return fmt.Errorf("%w: the skills lock is %q, the plan saw %q", ErrPlanChanged, v.lockHash, p.LockSHA)
		}
		snap.LockKeys = append(snap.LockKeys, v.keys...)
	}
	if !p.Effect.IsZero() {
		st, err := e.d.State.Load()
		if err != nil {
			return fmt.Errorf("read the state: %w", err)
		}
		snap.State = stateImage(st, p.Effect)
	}
	return nil
}

// coherent checks that the snapshot is the one of its plan: the plan is
// valid and hashes to PlanID, every folder, link, entry and the lock's
// existence are exactly what the plan first sees, the lock order holds the
// entries that were there and not the absent ones, and the state image only
// holds keys the effect touches.
func (snap Snapshot) coherent() error {
	p := snap.Plan
	if p.ID != snap.PlanID {
		return fmt.Errorf("it was taken for plan %s, it holds plan %s", snap.PlanID, p.ID)
	}
	if err := p.verifyID(); err != nil {
		return err
	}
	if err := p.validate(); err != nil {
		return err
	}
	want := snapshotOf(p)
	if !reflect.DeepEqual(snap.Trees, want.Trees) || !reflect.DeepEqual(snap.Links, want.Links) ||
		!reflect.DeepEqual(snap.Entries, want.Entries) || snap.LockExists != want.LockExists || snap.LockSHA != want.LockSHA {
		return errors.New("its folders, links, entries or lock are not the ones its plan touches")
	}
	if !p.TouchesLock || !snap.LockExists {
		if len(snap.LockKeys) != 0 {
			return errors.New("it orders a lock it did not see")
		}
	}
	keys := map[string]bool{}
	for _, k := range snap.LockKeys {
		if !validText(k) || keys[k] {
			return fmt.Errorf("lock key %q is empty or repeated", k)
		}
		keys[k] = true
	}
	for _, en := range snap.Entries {
		if keys[en.Name] != (en.State == StateEntry) {
			return fmt.Errorf("the lock order and entry %s disagree", en.Name)
		}
	}
	for k := range snap.State.Managed {
		if !slices.Contains(p.Effect.managedKeys(), k) || snap.State.Managed[k].Key != k {
			return fmt.Errorf("it keeps managed skill %s, which the effect does not touch", k)
		}
	}
	for k := range snap.State.Parked {
		if !slices.Contains(p.Effect.parkedKeys(), k) || snap.State.Parked[k].Key != k {
			return fmt.Errorf("it keeps parked skill %s, which the effect does not touch", k)
		}
	}
	return nil
}

// treeCopy finds the copy of the folder at path.
func (snap Snapshot) treeCopy(path string) (TreeCopy, bool) {
	for _, t := range snap.Trees {
		if t.Path == path {
			return t, true
		}
	}
	return TreeCopy{}, false
}

// VerifySnapshot reads snapshot id and checks that every folder copy still
// hashes to the tree it recorded.
func (e *Engine) VerifySnapshot(id string) (Snapshot, error) {
	snap, _, err := e.verifySnapshot(id)
	return snap, err
}

// verifySnapshot is VerifySnapshot, also returning the hash of snapshot.json.
func (e *Engine) verifySnapshot(id string) (Snapshot, string, error) {
	snap, sum, err := e.readSnapshot(id)
	if err != nil {
		return Snapshot{}, "", err
	}
	for _, t := range snap.Trees {
		copyDir := filepath.Join(e.SnapshotDir(id), filepath.FromSlash(t.Copy))
		if t.CloneSHA != "" {
			if err := e.verifyCloneSnapshotCopy(t.Path, copyDir, t.CloneSHA); err != nil {
				return Snapshot{}, "", fmt.Errorf("snapshot %s: %w", id, err)
			}
			continue
		}
		tree, err := e.d.HashDir(copyDir)
		if err != nil {
			return Snapshot{}, "", fmt.Errorf("snapshot %s: hash the copy of %s: %w", id, t.Path, err)
		}
		if tree.SHA != t.TreeSHA {
			return Snapshot{}, "", fmt.Errorf("snapshot %s: %w: the copy of %s is %s, want %s",
				id, ErrSnapshotMismatch, t.Path, tree.SHA, t.TreeSHA)
		}
	}
	return snap, sum, nil
}

// decodeStrict decodes one JSON document into v, refusing unknown fields and
// trailing data, as statefile does.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing data")
	}
	return nil
}
