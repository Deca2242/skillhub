package app

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"
)

// The install staging (Paths.InstallStagingDir) holds the copies
// ResolveInstall prepares for review. A copy that is not applied is
// discarded, and the staging levels left empty are removed, so that a
// review, a dry run or a refusal leaves the home as it was (B2).
//
// While a version 2 record is pending, nothing here touches the staging:
// the copy and its directory belong to that operation, whose recover may
// move the copy back there. RecoverOperation tidies it afterwards. With no
// record pending, everything in the staging is an orphan, which recover,
// ResolveInstall and the next install remove (clearOrphanStaging).

// DiscardRevision removes the staged copy of rev, which must be an entry of
// the install staging, and the staging levels left empty. The entry points
// call it for a revision they resolved and did not apply. While an
// operation is pending it leaves everything to RecoverOperation.
func (a *App) DiscardRevision(rev Revision) error {
	staged := filepath.Clean(rev.Prepared.StagedPath)
	if rev.Prepared.StagedPath == "" || filepath.Dir(staged) != a.d.Paths.InstallStagingDir() {
		return fmt.Errorf("%w: %q is not an entry of the install staging", ErrInvalidRequest, rev.Prepared.StagedPath)
	}
	if busy, err := a.ManagedPending(); err != nil || busy {
		return err
	}
	if err := a.removeStaged(staged); err != nil {
		return err
	}
	return a.tidyStaging()
}

// tidyAfter empties the install staging after an operation ran: an install
// or a reinstall leaves its levels empty, and the undo of one moved the copy
// back into it.
func (a *App) tidyAfter(p Proposal) error {
	switch {
	case p.Request.Action == ActionInstall || p.Request.Action == ActionReinstall:
		return a.tidyStaging()
	case p.Undo == nil || !undoesInstall(p.Undo.Of):
		return nil
	}
	for _, s := range p.Steps {
		if s.Kind == "move" && filepath.Dir(s.Dst) == a.d.Paths.InstallStagingDir() {
			if err := a.removeStaged(s.Dst); err != nil {
				return err
			}
		}
	}
	return a.tidyStaging()
}

// undoesInstall reports whether action is one whose undo moves the copy
// back into the install staging.
func undoesInstall(action string) bool {
	return action == string(ActionInstall) || action == string(ActionReinstall)
}

// prepareUndo, before the undo of snapshot runs, recreates the install
// staging when that undo moves a copy back into it: tidyAfter removed the
// empty level after the install. It reports whether it did.
func (m *manager) prepareUndo(snapshot string) (bool, error) {
	snap, err := m.engine.ReadSnapshot(snapshot)
	if err != nil || !undoesInstall(snap.Plan.Action) {
		return false, nil // planning the undo reports a bad snapshot
	}
	if err := m.a.d.Files.MkdirAll(m.a.d.Paths.InstallStagingDir(), 0o755); err != nil {
		return false, fmt.Errorf("recreate the install staging for the undo: %w", err)
	}
	return true, nil
}

// OrphanStaging reports whether the install staging is a real directory
// while no version 2 record is pending: what recover would remove as
// leftovers. It only reads.
func (a *App) OrphanStaging() (bool, error) {
	if busy, err := a.ManagedPending(); err != nil || busy {
		return false, err
	}
	return a.realDir(a.d.Paths.InstallStagingDir())
}

// realDir reports whether path is a directory, never a symbolic link or a
// file, nor below one: a level the user made something else is left alone
// (B1, A2 of P4).
func (a *App) realDir(path string) (bool, error) {
	mode, err := a.d.Files.Mode(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	if err != nil {
		return false, stagingFailed("look at %s: %w", path, err)
	}
	return mode.IsDir(), nil
}

// clearOrphanStaging removes everything in the install staging, and the
// staging levels left empty, when no version 2 record is pending: then no
// operation owns any of it (D1). An install that crashed while it prepared
// its copy, before its record, or while it removed the copy at the end
// leaves such orphans; so does the undo of an install interrupted outside its
// record. Once recover resolved a record, a rolled back install or reinstall
// put its copy back there and a closed undo of one left its copy there, and
// those go too: the home ends as before the revision was resolved, or with
// the skill installed and no staging. While a record is pending it touches
// nothing. It reports whether there was anything to remove.
func (a *App) clearOrphanStaging() (bool, error) {
	found, err := a.OrphanStaging()
	if err != nil {
		return false, err
	}
	if found {
		if err := a.d.Files.RemoveTree(a.d.Paths.InstallStagingDir()); err != nil {
			return false, stagingFailed("remove the orphan install staging: %w", err)
		}
	}
	return found, a.tidyStaging()
}

// removeStaged removes one entry of the install staging, if it is there.
func (a *App) removeStaged(path string) error {
	ok, err := a.d.Files.Exists(path)
	if err != nil || !ok {
		return err
	}
	if err := a.d.Files.RemoveTree(path); err != nil {
		return stagingFailed("discard the staged copy %s: %w", path, err)
	}
	return nil
}

// tidyStaging removes the install staging, the parking zone and their
// parent, skillhub's own level of ~/.agents, each only when it is a real
// directory and empty. The parking zone is here because the engine's close
// never fails over removing it (A2 of P4): one it could not remove is left
// to the next recover, which always comes through here.
func (a *App) tidyStaging() error {
	if busy, err := a.ManagedPending(); err != nil || busy {
		return err
	}
	staging := a.d.Paths.InstallStagingDir()
	for _, dir := range []string{staging, a.d.Paths.ParkedDir(), filepath.Dir(staging)} {
		real, err := a.realDir(dir)
		if err != nil {
			return err
		}
		if !real {
			continue
		}
		entries, err := a.d.Dirs.Scan(dir)
		if err != nil {
			return stagingFailed("look into %s: %w", dir, err)
		}
		if len(entries) > 0 {
			continue
		}
		if err := a.d.Files.RemoveTree(dir); err != nil {
			return stagingFailed("remove the empty %s: %w", dir, err)
		}
	}
	return nil
}

// stagingFailed is a refusal of kind ErrStagingFailed: the install staging
// could not be read or emptied. The entry points explain it in the user's
// language (A3 of P4).
func stagingFailed(format string, args ...any) error {
	return &RefusalError{Kind: ErrStagingFailed, Err: fmt.Errorf(format, args...)}
}
