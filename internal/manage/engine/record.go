package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// The durable record of a version 2 operation: its own file in the state
// directory, apart from state.json, written with FileOps.WriteFileAtomic
// before anything changes and after every step.
const (
	recordFormat  = "skillhub.operation"
	recordVersion = 2
	recordFile    = "operation-v2.json"
)

// RecordFile is where the version 2 operation record lives. Version 1 must
// refuse to write while it exists (docs/design-v2.md section 15, point 5).
func RecordFile(p paths.Paths) string { return filepath.Join(p.StateDir(), recordFile) }

// Record is a version 2 operation in progress: the plan with every step's
// preimage and postimage, the snapshot that brings everything back, and the
// progress.
type Record struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	PlanID     string `json:"plan_id"`
	Plan       Plan   `json:"plan"`
	SnapshotID string `json:"snapshot_id"`
	// SnapshotSHA is the SHA-256 of the snapshot's snapshot.json, so the
	// snapshot cannot be edited under a pending record.
	SnapshotSHA string    `json:"snapshot_sha"`
	StartedAt   time.Time `json:"started_at"`
	// LastCompleted is the index of the last step that completed; -1 before
	// the first.
	LastCompleted int `json:"last_completed"`
	// Parked is, for every lockPark or lockRemove step that completed, the
	// entry the lock store took out, with its neighbours.
	Parked map[int]domain.ParkedEntry `json:"parked,omitempty"`
	// Diagnostic tells why the operation stopped on foreign state, so
	// recover can explain it.
	Diagnostic string `json:"diagnostic,omitempty"`
}

func (e *Engine) recordPath() string { return RecordFile(e.d.Paths) }

func (e *Engine) writeRecord(rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize operation record: %w", err)
	}
	if err := e.d.Files.WriteFileAtomic(e.recordPath(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write operation record: %w", err)
	}
	return nil
}

// PendingRecord reads the version 2 operation record, if any. A record of an
// unknown format or version, with unknown fields, or whose plan does not hash
// to its identifier or does not hold together, is ErrUnsupportedRecord.
func (e *Engine) PendingRecord() (Record, bool, error) {
	data, err := e.d.Files.ReadFile(e.recordPath())
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("read operation record: %w", err)
	}
	rec, err := decodeRecord(data)
	if err != nil {
		return Record{}, false, fmt.Errorf("operation record %s: %w", e.recordPath(), err)
	}
	if rec.Plan.HasProjectScope() && e.d.ProjectFS == nil {
		return Record{}, false, fmt.Errorf("operation record %s: %w: %w", e.recordPath(), ErrUnsupportedRecord, ErrProjectScopeUnsupported)
	}
	return rec, true, nil
}

func decodeRecord(data []byte) (Record, error) {
	var rec Record
	if err := decodeStrict(data, &rec); err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrUnsupportedRecord, err)
	}
	if rec.Format != recordFormat || rec.Version != recordVersion {
		return Record{}, fmt.Errorf("%w: format %q version %d", ErrUnsupportedRecord, rec.Format, rec.Version)
	}
	if rec.PlanID != rec.Plan.ID {
		return Record{}, fmt.Errorf("%w: plan %s recorded as %s", ErrUnsupportedRecord, rec.Plan.ID, rec.PlanID)
	}
	if err := rec.Plan.verifyID(); err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrUnsupportedRecord, err)
	}
	if err := rec.Plan.validate(); err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrUnsupportedRecord, err)
	}
	if !validSHA256(rec.SnapshotSHA) {
		return Record{}, fmt.Errorf("%w: snapshot hash %q", ErrUnsupportedRecord, rec.SnapshotSHA)
	}
	if !validSnapshotID(rec.SnapshotID) {
		return Record{}, fmt.Errorf("%w: snapshot id %q", ErrUnsupportedRecord, rec.SnapshotID)
	}
	if rec.LastCompleted < -1 || rec.LastCompleted >= len(rec.Plan.Steps) {
		return Record{}, fmt.Errorf("%w: last completed step %d of %d", ErrUnsupportedRecord, rec.LastCompleted, len(rec.Plan.Steps))
	}
	for i, entry := range rec.Parked {
		if i < 0 || i > rec.LastCompleted {
			return Record{}, fmt.Errorf("%w: an entry recorded for step %d, which did not complete", ErrUnsupportedRecord, i)
		}
		s := rec.Plan.Steps[i]
		if (s.Kind != StepLockPark && s.Kind != StepLockRemove) || entry.Name != s.Name || !bytes.Equal(entry.Raw, s.Pre[0].Raw) {
			return Record{}, fmt.Errorf("%w: the entry recorded for step %d is not the one it took", ErrUnsupportedRecord, i)
		}
	}
	return rec, nil
}

// Run is an operation that began: the state lock is held until Release, the
// snapshot is complete and the record is on disk.
type Run struct {
	e       *Engine
	rec     Record
	release func() error
}

// Begin starts an operation for an approved plan, under the state lock that
// version 1 also takes. It refuses while a version 1 operation or a version 2
// record is pending, revalidates the plan against the disk, takes and
// verifies the snapshot, and only then writes the record. On any failure the
// lock is released and nothing is left: no record, and no snapshot unless
// removing it failed too, which Orphans reports.
func (e *Engine) Begin(ctx context.Context, p Plan) (*Run, error) {
	if err := p.verifyAndCheckProjectScope(e, "begin"); err != nil {
		return nil, err
	}
	release, err := e.d.State.Lock(ctx)
	if err != nil {
		return nil, fmt.Errorf("take the state lock: %w", err)
	}
	run, err := e.begin(p)
	if err != nil {
		return nil, errors.Join(err, release())
	}
	run.release = release
	return run, nil
}

func (e *Engine) begin(p Plan) (*Run, error) {
	if err := p.verifyAndCheckProjectScope(e, "begin"); err != nil {
		return nil, err
	}
	if _, pending, err := e.d.State.Operation(); err != nil {
		return nil, fmt.Errorf("read the version 1 operation: %w", err)
	} else if pending {
		return nil, ErrV1OperationPending
	}
	if exists, err := e.d.Files.Exists(e.recordPath()); err != nil {
		return nil, fmt.Errorf("look for the operation record: %w", err)
	} else if exists {
		return nil, ErrOperationPending
	}
	if err := e.Check(p); err != nil {
		return nil, err
	}
	if err := e.clearRestore(); err != nil {
		return nil, err
	}
	snap, sum, err := e.takeSnapshot(p)
	if err != nil {
		return nil, err
	}
	rec := Record{Format: recordFormat, Version: recordVersion, PlanID: p.ID, Plan: p,
		SnapshotID: snap.SnapshotID, SnapshotSHA: sum, StartedAt: e.d.Clock.Now().UTC(), LastCompleted: -1}
	if err := e.writeRecord(rec); err != nil {
		// No record existed under the lock, so one there now may be this
		// one, written before the error: its snapshot must stay.
		exists, existsErr := e.d.Files.Exists(e.recordPath())
		if existsErr != nil || exists {
			return nil, errors.Join(err, existsErr)
		}
		return nil, errors.Join(err, e.removeOwn(e.SnapshotDir(snap.SnapshotID)))
	}
	return &Run{e: e, rec: rec}, nil
}

// Record returns the operation's record as it is on disk.
func (r *Run) Record() Record {
	rec := r.rec
	rec.Plan.Steps = slices.Clone(rec.Plan.Steps)
	return rec
}

// StepDone records that step i completed. Steps complete in order: i must be
// the step after the last completed one. On failure the record on disk and
// in memory keeps the previous progress.
func (r *Run) StepDone(i int) error {
	return r.stepDone(i, nil)
}

// Release releases the state lock. The record stays until Execute closes the
// operation or Recover resolves it.
func (r *Run) Release() error {
	if r.release == nil {
		return nil
	}
	release := r.release
	r.release = nil
	return release()
}

// Orphan is a version 2 snapshot directory nothing points to: incomplete (an
// interrupted snapshot) or complete but referenced by neither the pending
// record nor the journal (an interruption between the snapshot and the
// record).
type Orphan struct {
	SnapshotID string
	Complete   bool
}

// Orphans lists the version 2 snapshots nothing points to, so they are
// reported rather than silently kept.
func (e *Engine) Orphans() ([]Orphan, error) {
	entries, err := e.d.Dirs.Scan(e.d.Paths.SnapshotsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []Orphan{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	referenced := map[string]bool{}
	rec, pending, err := e.PendingRecord()
	if err != nil {
		return nil, err
	}
	if pending {
		referenced[rec.SnapshotID] = true
	}
	journal, err := e.d.State.Journal()
	if err != nil {
		return nil, fmt.Errorf("read the journal: %w", err)
	}
	for _, h := range journal {
		referenced[h.SnapshotID] = true
	}
	out := []Orphan{}
	for _, entry := range entries {
		if entry.Kind != ports.EntryDir || !strings.HasPrefix(entry.Name, snapshotPrefix) {
			continue
		}
		_, err := e.ReadSnapshot(entry.Name)
		complete := err == nil
		if complete && referenced[entry.Name] {
			continue
		}
		out = append(out, Orphan{SnapshotID: entry.Name, Complete: complete})
	}
	return out, nil
}

// validSHA256 accepts a lowercase hex SHA-256.
func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
