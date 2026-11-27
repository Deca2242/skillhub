package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// Outcome is an operation that closed: its plan and its snapshot, which the
// journal line names and Undo takes. When ClosePending is true, Committed
// distinguishes a confirmed journal commit (SnapshotID preserved) from an
// unreadable journal where commit could not be confirmed.
type Outcome struct {
	PlanID       string
	SnapshotID   string
	Committed    bool
	ClosePending bool
}

// ErrClosePending reports that the operation's commit line may already be in
// the journal (or is confirmed there), so the close could not finish and the
// record stays pending for Recover.
var ErrClosePending = errors.New("operation close pending")

// errNotCommitted marks an appendOnce failure where the journal confirmed the
// commit line is absent, so Execute may safely roll back.
var errNotCommitted = errors.New("operation not committed")

// ForeignStateError reports the step that met foreign state and what it saw.
// It matches ErrForeignState; its text is the record's diagnostic.
type ForeignStateError struct {
	Step   int
	Kind   StepKind
	Detail string
}

func (f *ForeignStateError) Error() string {
	return fmt.Sprintf("%v: step %d (%s): %s", ErrForeignState, f.Step, f.Kind, f.Detail)
}

func (f *ForeignStateError) Unwrap() error { return ErrForeignState }

// foreign builds the error for step i of plan p, from what was seen.
func foreign(p Plan, i int, seen []Image) *ForeignStateError {
	s := p.Steps[i]
	var parts []string
	for k := range seen {
		if seen[k].same(s.Pre[k]) || seen[k].same(s.Post[k]) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s is %s; the step needs %s before or leaves %s",
			strings.TrimPrefix(strings.TrimPrefix(seen[k].object(), "path:"), "entry:"),
			seen[k].describe(), s.Pre[k].describe(), s.Post[k].describe()))
	}
	return &ForeignStateError{Step: i, Kind: s.Kind, Detail: strings.Join(parts, "; ")}
}

// Apply runs an approved plan: Begin, every step, the verification and the
// close, then releases the state lock.
func (e *Engine) Apply(ctx context.Context, p Plan) (Outcome, error) {
	if err := p.verifyAndCheckProjectScope(e, "apply"); err != nil {
		return Outcome{}, err
	}
	run, err := e.Begin(ctx, p)
	if err != nil {
		return Outcome{}, err
	}
	out, err := run.Execute()
	return out, errors.Join(err, run.Release())
}

// Execute runs the steps of an operation that began, in order. Before each
// step it checks that every object is still the step's preimage, and records
// the progress after it. It then verifies every postimage and closes: the
// journal line that commits the operation, the plan's state effect and the
// removal of the record, in that order.
//
// A step that fails, a progress update that fails and a verification that
// fails roll back what ran, in reverse order, to exactly the preimages, and
// journal the operation as failed. Foreign state stops everything: nothing is
// touched or rolled back, and the record keeps a diagnostic for Recover.
func (r *Run) Execute() (Outcome, error) {
	if err := r.e.requireProjectGate(r.rec.Plan, "execute"); err != nil {
		return Outcome{}, err
	}
	e := r.e
	snap, err := e.verifiedSnapshot(r.rec)
	if err != nil {
		return Outcome{}, err
	}
	steps := r.rec.Plan.Steps
	clones := cloneObjects(r.rec.Plan)
	for i := r.rec.LastCompleted + 1; i < len(steps); i++ {
		prog, seen, err := e.observe(r.rec.Plan, steps[i], clones)
		if err != nil {
			return Outcome{}, r.rollback(snap, err)
		}
		if prog != NotRun {
			return Outcome{}, r.stop(foreign(r.rec.Plan, i, seen))
		}
		taken, err := e.do(r.rec, i)
		if err == nil && taken != nil {
			if want := neighbours(r.rec.Plan, snap, i); taken.Before != want.Before || taken.After != want.After {
				// Somebody rewrote the lock between the check and the step:
				// what the record keeps must be what the snapshot derives.
				err = fmt.Errorf("%w: %s was between %q and %q, not %q and %q",
					ports.ErrLockChanged, taken.Name, taken.Before, taken.After, want.Before, want.After)
				// The rollback puts the entry back where it really was; this
				// copy of the progress stays in memory, unrecorded.
				r.rec.Parked = cloneEntries(r.rec.Parked)
				if r.rec.Parked == nil {
					r.rec.Parked = map[int]domain.ParkedEntry{}
				}
				r.rec.Parked[i] = *taken
			}
		}
		if err != nil {
			return Outcome{}, r.rollback(snap, fmt.Errorf("step %d (%s): %w", i, steps[i].Kind, err))
		}
		if err := r.stepDone(i, taken); err != nil {
			return Outcome{}, r.rollback(snap, err)
		}
	}
	if err := e.verify(r.rec.Plan); err != nil {
		return Outcome{}, r.rollback(snap, err)
	}
	committed, err := e.closeCommitted(r.rec, snap)
	if err != nil {
		if errors.Is(err, errNotCommitted) {
			return Outcome{}, r.rollback(snap, err)
		}
		out := Outcome{PlanID: r.rec.PlanID, Committed: committed, ClosePending: true}
		if committed {
			out.SnapshotID = r.rec.SnapshotID
		}
		return out, fmt.Errorf("%w: %w", ErrClosePending, err)
	}
	return Outcome{PlanID: r.rec.PlanID, SnapshotID: r.rec.SnapshotID, Committed: true}, nil
}

// stepDone records that step i completed, with the entry it took out of the
// lock if any.
func (r *Run) stepDone(i int, taken *domain.ParkedEntry) error {
	if i != r.rec.LastCompleted+1 || i >= len(r.rec.Plan.Steps) {
		return fmt.Errorf("%w: step %d after step %d of %d", ErrProgress, i, r.rec.LastCompleted, len(r.rec.Plan.Steps))
	}
	next := r.rec
	next.LastCompleted = i
	if taken != nil {
		next.Parked = cloneEntries(r.rec.Parked)
		if next.Parked == nil {
			next.Parked = map[int]domain.ParkedEntry{}
		}
		next.Parked[i] = *taken
	}
	if err := r.e.writeRecord(next); err != nil {
		return err
	}
	r.rec = next
	return nil
}

// stop keeps the record with the diagnostic of foreign state.
func (r *Run) stop(fe *ForeignStateError) error {
	return r.e.stop(r.rec, fe)
}

func (e *Engine) stop(rec Record, fe *ForeignStateError) error {
	rec.Diagnostic = fe.Error()
	return errors.Join(fe, e.writeRecord(rec))
}

// rollback undoes what ran after cause, closes the operation as failed and
// returns cause. When the rollback cannot finish, the record stays for
// Recover, with a diagnostic when the reason is foreign state.
func (r *Run) rollback(snap Snapshot, cause error) error {
	e := r.e
	// The record on disk may be one step ahead of r.rec: a progress update
	// that failed after writing. Observing the disk settles it.
	if err := e.rollbackSteps(r.rec, snap); err != nil {
		var fe *ForeignStateError
		if errors.As(err, &fe) {
			return errors.Join(cause, e.stop(r.rec, fe))
		}
		return errors.Join(cause, fmt.Errorf("roll back: %w", err))
	}
	return errors.Join(cause, e.closeRolledBack(r.rec, snap, domain.ActionFailed))
}

// verifiedSnapshot reads the record's snapshot, checks that it is the one of
// the record's plan and that every folder copy still verifies.
//
// It also binds what the snapshot and the record keep beyond the plan: the
// snapshot must hash to the record's SnapshotSHA, and every entry the record
// says a step took must sit between the neighbours the snapshot's lock order
// gives, so an edited lock order, state image or neighbour is refused before
// anything is touched.
func (e *Engine) verifiedSnapshot(rec Record) (Snapshot, error) {
	snap, sum, err := e.verifySnapshot(rec.SnapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	if snap.PlanID != rec.PlanID {
		return Snapshot{}, fmt.Errorf("%w: snapshot %s is of plan %s, the record of %s",
			ErrUnsupportedRecord, rec.SnapshotID, snap.PlanID, rec.PlanID)
	}
	if sum != rec.SnapshotSHA {
		return Snapshot{}, fmt.Errorf("%w: snapshot %s changed since the record was written", ErrUnsupportedRecord, rec.SnapshotID)
	}
	for i, taken := range rec.Parked {
		want := neighbours(rec.Plan, snap, i)
		if taken.Before != want.Before || taken.After != want.After {
			return Snapshot{}, fmt.Errorf("%w: step %d took %s between %q and %q, the snapshot says %q and %q",
				ErrUnsupportedRecord, i, taken.Name, taken.Before, taken.After, want.Before, want.After)
		}
	}
	return snap, nil
}

// halfway is a lock step half undone or half done: the entry is gone but an
// empty lock is still there where the step's other image has no lock.
const halfway Progress = "halfway"

// observe tells where step s stands on disk, using clones so clone-root
// observation stays consistent across forward execution and rollback/recovery.
func (e *Engine) observe(p Plan, s Step, clones map[string]bool) (Progress, []Image, error) {
	v := e.newPlanView(p.Project)
	allowStep := stepAllowsClone(s)
	seen := make([]Image, len(s.Pre))
	for i := range s.Pre {
		allowClone := allowStep || clones[s.Pre[i].object()]
		img, err := v.observe(s.Pre[i], allowClone)
		if err != nil {
			return "", nil, err
		}
		seen[i] = img
	}
	return classify(s, seen), seen, nil
}

// classify compares what was seen with the step's images.
func classify(s Step, seen []Image) Progress {
	pre, post := true, true
	for i := range seen {
		pre = pre && seen[i].same(s.Pre[i])
		post = post && seen[i].same(s.Post[i])
	}
	switch {
	case pre:
		return NotRun
	case post:
		return Done
	}
	emptied := seen[0].Entry != "" && seen[0].State == StateAbsent && seen[0].emptyLock
	if emptied && ((s.Kind == StepLockAdd && s.Pre[0].State == StateNoLock) ||
		(s.Kind == StepLockRemove && s.Post[0].State == StateNoLock)) {
		return halfway
	}
	return Foreign
}

// do runs step i of rec's plan and returns the entry it took out of the
// lock, if any.
func (e *Engine) do(rec Record, i int) (*domain.ParkedEntry, error) {
	if err := e.requireProjectGate(rec.Plan, "execute step"); err != nil {
		return nil, err
	}
	s := rec.Plan.Steps[i]
	if rec.Plan.HasProjectScope() {
		proj := *rec.Plan.Project
		switch s.Kind {
		case StepUnlink:
			root, name, ok := parseProjectSlot(proj.ProjectDir, s.Path)
			if !ok {
				return nil, fmt.Errorf("%w: invalid project link path %q", ErrInvalidStep, s.Path)
			}
			return nil, e.d.ProjectFS.RemoveLink(proj.ProjectDir, string(root), name, s.Target)
		case StepLink:
			root, name, ok := parseProjectSlot(proj.ProjectDir, s.Path)
			if !ok {
				return nil, fmt.Errorf("%w: invalid project link path %q", ErrInvalidStep, s.Path)
			}
			return nil, e.d.ProjectFS.CreateLink(proj.ProjectDir, string(root), name, s.Target)
		case StepRemoveTree:
			return nil, e.removeProjectTree(proj, e.trash(rec, i))
		case StepRestoreTree:
			if !within(s.Src, e.d.Paths.SnapshotsDir()) {
				return nil, fmt.Errorf("%w: restore from %s, outside the snapshots", ErrInvalidStep, s.Src)
			}
			return nil, e.restoreProjectTree(s.Src, proj, s.Post[0].TreeSHA, e.staging(rec, i))
		default:
			return nil, fmt.Errorf("%w: project-scoped step %q", ErrInvalidStep, s.Kind)
		}
	}
	switch s.Kind {
	case StepMove:
		if err := e.ensureParent(s.Dst); err != nil {
			return nil, err
		}
		return nil, e.d.Links.RenameNoReplace(s.Src, s.Dst)
	case StepLink:
		return nil, e.d.Links.Symlink(s.Target, s.Path)
	case StepUnlink:
		return nil, e.d.Links.RemoveLink(s.Path, s.Target)
	case StepRemoveTree, StepRemoveClone:
		return nil, e.removeTree(s.Path, e.trash(rec, i))
	case StepRestoreTree:
		if !within(s.Src, e.d.Paths.SnapshotsDir()) {
			return nil, fmt.Errorf("%w: restore from %s, outside the snapshots", ErrInvalidStep, s.Src)
		}
		return nil, e.publish(s.Src, s.Path, s.Post[0].TreeSHA, e.staging(rec, i))
	case StepRestoreClone:
		if !within(s.Src, e.d.Paths.SnapshotsDir()) {
			return nil, fmt.Errorf("%w: restore from %s, outside the snapshots", ErrInvalidStep, s.Src)
		}
		return nil, e.publishClone(s.Src, s.Path, s.Post[0].CloneSHA, e.staging(rec, i))
	case StepRequire:
		return nil, nil // checked before the step, and again by verify
	case StepLockPark:
		entry, err := e.d.Lock.Park(s.Name)
		if err != nil {
			return nil, err
		}
		return &entry, nil
	case StepLockUnpark:
		return nil, e.d.Lock.Unpark(*s.Entry)
	case StepLockAdd:
		return nil, e.d.Lock.Add(s.Name, s.Raw)
	case StepLockReplace:
		return nil, e.d.Lock.RestoreEntry(s.Name, s.Raw)
	case StepLockRemove:
		entry, err := e.d.Lock.Remove(s.Name, s.Raw)
		if err != nil {
			return nil, err
		}
		if s.Post[0].State == StateNoLock {
			if err := e.dropLock(); err != nil {
				return &entry, err
			}
		}
		return &entry, nil
	}
	return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidStep, s.Kind)
}

func (e *Engine) dropLock() error {
	if e.d.DropLock == nil {
		return ErrLockDropUnsupported
	}
	return e.d.DropLock.DropEmpty()
}

// restoreRoot is where the engine prepares the folders it restores:
// ~/.agents/.skillhub/restore, next to the parking zone. It is outside every
// directory a harness reads, so a crash in the middle of a copy leaves
// nothing a harness or a scan takes for a skill, and on the same file system
// as the store, so the prepared copy is published with a rename. Nothing in
// it outlives an operation: Begin and every close, Recover's included,
// remove it whole.
func (e *Engine) restoreRoot() string {
	return filepath.Join(filepath.Dir(e.d.Paths.ParkedDir()), "restore")
}

// staging is where step i of rec prepares the folder it publishes.
func (e *Engine) staging(rec Record, i int) string {
	return filepath.Join(e.restoreRoot(), rec.SnapshotID+"-"+strconv.Itoa(i))
}

// trashRoot is where a removeTree step moves a folder before deleting it:
// ~/.agents/.skillhub/trash, outside every harness and on the store's file
// system. The folder leaves the store in one rename, so a crash in the
// middle of the deletion never leaves a half-deleted folder where the skill
// was: Recover sees the path absent, the step done, and restores the folder
// from the snapshot, never from the trash.
func (e *Engine) trashRoot() string {
	return filepath.Join(filepath.Dir(e.d.Paths.ParkedDir()), "trash")
}

// trash is where step i of rec moves the folder it removes.
func (e *Engine) trash(rec Record, i int) string {
	return filepath.Join(e.trashRoot(), rec.SnapshotID+"-"+strconv.Itoa(i))
}

// ensureProjectScratchBase ensures the parent directory above ~/.agents/.skillhub
// exists when a clean home has never used ~/.agents yet.
func (e *Engine) ensureProjectScratchBase() error {
	parent := filepath.Dir(filepath.Dir(e.d.Paths.ParkedDir()))
	if _, err := e.d.Files.Mode(parent); errors.Is(err, fs.ErrNotExist) {
		return e.d.Files.MkdirAll(parent, 0o755)
	} else if err != nil {
		return fmt.Errorf("look at %s: %w", parent, err)
	}
	return nil
}

func (e *Engine) removeProjectTree(scope ProjectScope, trash string) error {
	if err := e.ensureProjectScratchBase(); err != nil {
		return err
	}
	if err := e.ensureParent(trash); err != nil {
		return err
	}
	if err := e.removeOwn(trash); err != nil {
		return err
	}
	if err := e.d.ProjectFS.RemoveSkillToTrash(scope.ProjectDir, string(scope.Root), scope.SkillName, trash); err != nil {
		return err
	}
	return e.removeOwn(trash)
}

func (e *Engine) restoreProjectTree(copyDir string, scope ProjectScope, sha, staging string) error {
	if err := e.ensureProjectScratchBase(); err != nil {
		return err
	}
	if err := e.ensureParent(staging); err != nil {
		return err
	}
	if err := e.removeOwn(staging); err != nil {
		return err
	}
	if err := e.d.Files.CopyTree(copyDir, staging); err != nil {
		return fmt.Errorf("copy %s next to %s: %w", copyDir, staging, err)
	}
	err := e.verifyCopy(copyDir, staging, sha)
	if err == nil {
		err = e.d.ProjectFS.RestoreSkillFromStaging(staging, scope.ProjectDir, string(scope.Root), scope.SkillName)
	}
	if err != nil {
		return errors.Join(err, e.removeOwn(staging))
	}
	return nil
}

// removeTree moves the folder at path to trash with a rename that never
// replaces, then deletes it there.
func (e *Engine) removeTree(path, trash string) error {
	if err := e.ensureParent(trash); err != nil {
		return err
	}
	if err := e.removeOwn(trash); err != nil {
		return err
	}
	if err := e.d.Links.RenameNoReplace(path, trash); err != nil {
		return err
	}
	return e.removeOwn(trash)
}

// clearRestore removes the engine's scratch roots, restore/ and trash/,
// everything an interrupted copy or deletion left in them, and the
// temporary levels an interrupted ensureDir left.
func (e *Engine) clearRestore() error {
	base := filepath.Dir(e.d.Paths.ParkedDir())
	roots := []string{e.restoreRoot(), e.trashRoot(), mkdirTemp(base), mkdirTemp(e.d.Paths.StoreDir())}
	for _, level := range []string{e.d.Paths.ParkedDir(), e.restoreRoot(), e.trashRoot()} {
		roots = append(roots, mkdirTemp(level))
	}
	for _, root := range roots {
		if err := e.removeOwn(root); err != nil {
			return fmt.Errorf("clear %s: %w", root, err)
		}
	}
	return nil
}

// removeOwn deletes a folder in the engine's own roots, if it exists. A
// folder that cannot be deleted because one of its directories is not
// writable (a skill may ship one) gets every directory made writable first;
// that only ever happens to what the engine already took out of the store.
func (e *Engine) removeOwn(path string) error {
	exists, err := e.d.Files.Exists(path)
	if errors.Is(err, syscall.ENOTDIR) {
		return nil // a level above it is a file: nothing of ours is there (A2 of P4)
	}
	if err != nil || !exists {
		return err
	}
	err = e.d.Files.RemoveTree(path)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if err := e.makeWritable(path); err != nil {
		return err
	}
	return e.d.Files.RemoveTree(path)
}

// makeWritable gives dir and every real directory under it the owner's
// write and search bits, top down. Symlinks are not followed.
func (e *Engine) makeWritable(dir string) error {
	mode, err := e.d.Files.Mode(dir)
	if err != nil || !mode.IsDir() {
		return err
	}
	if err := e.d.Files.Chmod(dir, mode.Perm()|0o700); err != nil {
		return err
	}
	if dr, ok := e.d.Files.(DirReader); ok {
		entries, err := dr.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, en := range entries {
			if en.Type().IsDir() {
				if err := e.makeWritable(filepath.Join(dir, en.Name())); err != nil {
					return err
				}
			}
		}
		return nil
	}
	entries, err := e.d.Dirs.Scan(dir)
	if err != nil {
		return err
	}
	for _, en := range entries {
		if en.Kind == ports.EntryDir {
			if err := e.makeWritable(filepath.Join(dir, en.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureParent creates the parent of path when it lies in one of the
// engine's own roots (the parking zone, restore/ and trash/) and is
// missing, or when it is the store itself: in a home where npx skills never
// installed anything, ~/.agents/skills is missing, and npx creates it too.
// Only that one level is created for the store, never a level below it or
// above it. Anywhere else nothing is created: a move into a missing foreign
// directory fails.
func (e *Engine) ensureParent(path string) error {
	parent := filepath.Dir(path)
	if store := e.d.Paths.StoreDir(); parent == store {
		return e.ensureDirFrom(store, store)
	}
	for _, root := range []string{e.d.Paths.ParkedDir(), e.restoreRoot(), e.trashRoot()} {
		if parent == root || within(parent, root) {
			return e.ensureDir(parent)
		}
	}
	return nil
}

// mkdirTemp is the temporary sibling ensureDir creates level as.
func mkdirTemp(level string) string {
	return filepath.Join(filepath.Dir(level), ".skillhub-mkdir-"+filepath.Base(level))
}

// ensureDir creates every missing level of dir from ~/.agents/.skillhub
// down (ensureDirFrom).
func (e *Engine) ensureDir(dir string) error {
	return e.ensureDirFrom(dir, filepath.Dir(e.d.Paths.ParkedDir()))
}

// ensureDirFrom creates every missing level of dir from base down, durably:
// each level is made as a temporary sibling and published with a rename,
// which syncs the parent. The directory above base must exist; the engine
// never creates it.
func (e *Engine) ensureDirFrom(dir, base string) error {
	var levels []string
	for d := dir; ; d = filepath.Dir(d) {
		levels = append(levels, d)
		if d == base {
			break
		}
		if !within(d, base) {
			return fmt.Errorf("%w: %s is not under %s", ErrInvalidStep, dir, base)
		}
	}
	if mode, err := e.d.Files.Mode(filepath.Dir(base)); err != nil || !mode.IsDir() {
		return fmt.Errorf("create %s: %s is not a folder: %v", dir, filepath.Dir(base), err)
	}
	for i := len(levels) - 1; i >= 0; i-- {
		level := levels[i]
		mode, err := e.d.Files.Mode(level)
		if err == nil {
			if !mode.IsDir() {
				return fmt.Errorf("create %s: %s is not a folder", dir, level)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("look at %s: %w", level, err)
		}
		tmp := mkdirTemp(level)
		if err := e.removeOwn(tmp); err != nil {
			return err
		}
		if err := e.d.Files.MkdirAll(tmp, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", level, err)
		}
		if err := e.d.Links.RenameNoReplace(tmp, level); err != nil {
			return errors.Join(fmt.Errorf("create %s: %w", level, err), e.d.Files.RemoveTree(tmp))
		}
	}
	return nil
}

// publish puts a verified copy of the folder copyDir at path, which must not
// exist: the copy is made next to path, checked against sha and every mode
// of copyDir, then renamed without replacing. A missing parent of path is
// created only where ensureParent creates one. Nothing is left on failure,
// except what a crash interrupts, which clearRestore removes, and an own
// level, which a rollback removes when the operation created it.
func (e *Engine) publish(copyDir, path, sha, staging string) error {
	if err := e.ensureParent(staging); err != nil {
		return err
	}
	// A parked folder goes back into the parking zone, which a later
	// operation may have removed once it was empty (removeEmptyOwnLevels).
	if err := e.ensureParent(path); err != nil {
		return err
	}
	if err := e.removeOwn(staging); err != nil {
		return err
	}
	if err := e.d.Files.CopyTree(copyDir, staging); err != nil {
		return fmt.Errorf("copy %s next to %s: %w", copyDir, path, err)
	}
	err := e.verifyCopy(copyDir, staging, sha)
	if err == nil {
		err = e.d.Links.RenameNoReplace(staging, path)
	}
	if err != nil {
		return errors.Join(err, e.d.Files.RemoveTree(staging))
	}
	return nil
}

// publishClone puts a verified whole-clone copy of copyDir at path, which must
// not exist: the copy is staged outside every harness, verified with
// verifyCloneCopy against cloneSHA, and then published via RenameNoReplace.
func (e *Engine) publishClone(copyDir, path, cloneSHA, staging string) error {
	if err := e.ensureParent(staging); err != nil {
		return err
	}
	if err := e.ensureParent(path); err != nil {
		return err
	}
	if err := e.removeOwn(staging); err != nil {
		return err
	}
	if err := e.d.Files.CopyTree(copyDir, staging); err != nil {
		return fmt.Errorf("copy %s next to %s: %w", copyDir, path, err)
	}
	err := e.verifyCloneCopy(copyDir, staging, cloneSHA)
	if err == nil {
		err = e.d.Links.RenameNoReplace(staging, path)
	}
	if err != nil {
		return errors.Join(err, e.removeOwn(staging))
	}
	return nil
}

// finalImages is, for every object the plan touches, the image its last
// step leaves, in the order the plan first touches them.
func finalImages(p Plan) []Image {
	var order []string
	last := map[string]Image{}
	for _, s := range p.Steps {
		for _, img := range s.Post {
			if _, ok := last[img.object()]; !ok {
				order = append(order, img.object())
			}
			last[img.object()] = img
		}
	}
	out := make([]Image, 0, len(order))
	for _, o := range order {
		out = append(out, last[o])
	}
	return out
}

// verify checks that every object is the plan's postimage: tree hashes, clone
// digests, link targets, entry bytes and the lock's absence.
func (e *Engine) verify(p Plan) error {
	if err := e.requireProjectGate(p, "verify"); err != nil {
		return err
	}
	v := e.newPlanView(p.Project)
	clones := cloneObjects(p)
	for _, want := range finalImages(p) {
		got, err := v.observe(want, clones[want.object()])
		if err != nil {
			return fmt.Errorf("%w: %w", ErrVerify, err)
		}
		if !got.same(want) {
			return fmt.Errorf("%w: %s is %s, the plan leaves %s", ErrVerify, want.object(), got.describe(), want.describe())
		}
	}
	return nil
}

// Journal details name the plan, the hash of the snapshot's snapshot.json
// and, for an undo, the operation it undoes.
const (
	detailPlan        = "plan="
	detailSnapshotSHA = "snapshot_sha="
	detailUndoes      = "undoes="
)

func journalDetail(rec Record) string {
	d := detailPlan + rec.Plan.ID + " " + detailSnapshotSHA + rec.SnapshotSHA
	if rec.Plan.Undoes != "" {
		d += " " + detailUndoes + rec.Plan.Undoes
	}
	return d
}

// detailField returns the value of key in a journal line's detail.
func detailField(h domain.HistoryEntry, key string) string {
	for _, f := range strings.Fields(h.Detail) {
		if v, ok := strings.CutPrefix(f, key); ok {
			return v
		}
	}
	return ""
}

// undoesOf returns the snapshot a journal line says it undoes.
func undoesOf(h domain.HistoryEntry) string { return detailField(h, detailUndoes) }

// journalTarget names a version 2 operation in the journal, apart from every
// version 1 target.
func JournalTarget(p Plan) string {
	if p.Undoes != "" {
		return "v2:undo"
	}
	return "v2:" + p.Action
}

// commitAction is the journal action that commits rec's operation.
func commitAction(p Plan) domain.HistoryAction {
	if p.Undoes != "" {
		return domain.ActionUndone
	}
	return domain.ActionApplied
}

// journaled reports whether the journal has a line of one of actions for
// snapshot id.
func (e *Engine) journaled(id string, actions ...domain.HistoryAction) (bool, error) {
	journal, err := e.d.State.Journal()
	if err != nil {
		return false, fmt.Errorf("read the journal: %w", err)
	}
	for _, h := range journal {
		if h.SnapshotID == id && slices.Contains(actions, h.Action) {
			return true, nil
		}
	}
	return false, nil
}

// appendOnce journals action for rec unless a line of one of also already
// does, so a close a crash interrupted does not journal twice.
func (e *Engine) appendOnce(rec Record, action domain.HistoryAction, also ...domain.HistoryAction) error {
	actions := append(slices.Clone(also), action)
	done, err := e.journaled(rec.SnapshotID, actions...)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	err = e.d.State.AppendJournal(domain.HistoryEntry{
		At: e.d.Clock.Now().UTC(), Action: action, Target: JournalTarget(rec.Plan),
		SnapshotID: rec.SnapshotID, Labels: []domain.Label{}, Detail: journalDetail(rec),
	})
	if err != nil {
		done, jerr := e.journaled(rec.SnapshotID, actions...)
		if jerr != nil {
			return errors.Join(fmt.Errorf("journal the operation: %w", err), jerr)
		}
		if done {
			return nil
		}
		return fmt.Errorf("%w: journal the operation: %w", errNotCommitted, err)
	}
	return nil
}

// closeCommitted closes an operation whose steps all ran and verified. The
// journal line is the commit point: once it is written, Recover finishes the
// close instead of rolling back. The state effect is applied before the
// record goes, so a crash cannot lose it; applying it again is harmless.
func (e *Engine) closeCommitted(rec Record, snap Snapshot) (bool, error) {
	if err := e.appendOnce(rec, commitAction(rec.Plan)); err != nil {
		return false, err
	}
	if err := e.clearRestore(); err != nil {
		return true, err
	}
	if err := e.removeUndoneDirs(rec); err != nil {
		return true, err
	}
	// Tidying is best effort: the close never fails over it (A2 of P4).
	_ = e.removeEmptyOwnLevels()
	if !rec.Plan.Effect.IsZero() {
		st, err := e.d.State.Load()
		if err != nil {
			return true, fmt.Errorf("read the state: %w", err)
		}
		if err := rec.Plan.Effect.apply(&st, snap, rec.Parked); err != nil {
			return true, err
		}
		if err := e.d.State.Save(st); err != nil {
			return true, fmt.Errorf("save the state: %w", err)
		}
	}
	if err := e.removeRecord(); err != nil {
		return true, err
	}
	return true, nil
}

// closeRolledBack closes an operation rolled back to its preimages: one
// journal line, the staging leftovers, then the record.
func (e *Engine) closeRolledBack(rec Record, snap Snapshot, action domain.HistoryAction) error {
	if err := e.appendOnce(rec, action, domain.ActionFailed, domain.ActionRecovered); err != nil {
		return err
	}
	if err := e.clearRestore(); err != nil {
		return err
	}
	if err := e.removeCreatedDirs(snap); err != nil {
		return err
	}
	return e.removeRecord()
}

// ownLevels are the levels a move may create, deepest first: the engine's
// own tree and the store.
func (e *Engine) ownLevels() []string {
	return []string{e.d.Paths.ParkedDir(), filepath.Dir(e.d.Paths.ParkedDir()), e.d.Paths.StoreDir()}
}

// removeEmptyOwnLevels removes, deepest first, the levels of
// ~/.agents/.skillhub the engine owns, the parking zone and
// ~/.agents/.skillhub itself, each only when it exists and is empty: an
// operation that leaves them empty, an enable of the last parked skill for
// one, leaves no empty level behind (D2). The store is npx's and never goes;
// a level holding anything, the install staging included, stays, and so does
// one that is not a real directory (removeIfEmpty). It runs in the close,
// after the commit, so Recover runs it again after a crash; the close goes on
// when it fails.
func (e *Engine) removeEmptyOwnLevels() error {
	for _, dir := range []string{e.d.Paths.ParkedDir(), filepath.Dir(e.d.Paths.ParkedDir())} {
		if err := e.removeIfEmpty(dir); err != nil {
			return err
		}
	}
	return nil
}

// removeIfEmpty removes dir when it is a real directory, never a symbolic
// link (the user may have linked a level elsewhere) nor a file, and holds
// nothing. Anything else is left as it is, with no error (B1, A2 of P4).
func (e *Engine) removeIfEmpty(dir string) error {
	mode, err := e.d.Files.Mode(dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look at %s: %w", dir, err)
	}
	if !mode.IsDir() {
		return nil
	}
	entries, err := e.d.Dirs.Scan(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	if len(entries) != 0 {
		return nil
	}
	if err := e.d.Files.RemoveTree(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}

// removeUndoneDirs, when rec undoes an operation, removes the levels that
// operation created and that are empty now, so after the undo the home is
// identical to before the operation, their absence included (L44). It runs in
// the close, after the commit, so Recover runs it again after a crash.
func (e *Engine) removeUndoneDirs(rec Record) error {
	if rec.Plan.Undoes == "" {
		return nil
	}
	orig, _, err := e.readSnapshot(rec.Plan.Undoes)
	if errors.Is(err, ErrSnapshotNotFound) {
		return nil // pruned: nothing tells which levels it created
	}
	if err != nil {
		return err
	}
	return e.removeCreatedDirs(orig)
}

// removeCreatedDirs removes, deepest first, every level of ownLevels that did
// not exist before the operation of snap and is empty after its rollback or
// its undo, so the home is exactly as it was.
func (e *Engine) removeCreatedDirs(snap Snapshot) error {
	for _, dir := range e.ownLevels() {
		if !slices.Contains(snap.MissingDirs, dir) {
			continue
		}
		if err := e.removeIfEmpty(dir); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) removeRecord() error {
	if err := e.d.Files.RemoveTree(e.recordPath()); err != nil {
		return fmt.Errorf("remove the operation record: %w", err)
	}
	return nil
}
