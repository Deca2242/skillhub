// Package app is the use-case facade the CLI and the TUI share
// (docs/design.md section 5). It composes the inventory, the update flow and
// the background check over the ports, and holds no terminal UI: the entry
// points turn its results into Spanish text.
//
// Nothing here applies an update on its own: Check only detects, stages and
// labels, and every call that changes a skills folder (Apply, ApplyAll, Undo,
// Recover) is a user action the entry point asked for.
package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"skillhub/internal/inventory"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/updates"
)

var (
	// ErrMissingDependency reports Deps without a port the facade needs.
	ErrMissingDependency = errors.New("app: missing dependency")
	// ErrSkillNotFound reports a name or id that matches no skill.
	ErrSkillNotFound = errors.New("skill not found")
	// ErrNoCandidate reports a skill with no update waiting for the user.
	ErrNoCandidate = errors.New("skill has no pending update")
)

// AmbiguousError reports a name that matches more than one row; the command
// refuses and lists the ids (docs/design.md section 9).
type AmbiguousError struct {
	Ref string
	// IDs are the matching rows' ids, sorted.
	IDs []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches several skills: %s", e.Ref, strings.Join(e.IDs, ", "))
}

// Deps are the ports the facade goes through.
type Deps struct {
	Paths paths.Paths
	// Agents is the agents table: the embedded one plus the user's overlay.
	Agents     []domain.AgentProfile
	Files      ports.FileOps
	Dirs       ports.SkillDirScanner
	Lock       ports.SkillsLockStore
	State      ports.StateStore
	Settings   ports.SettingsStore
	GitHub     ports.GitHubPort
	Git        ports.GitPort
	Notifier   ports.Notifier
	Background ports.BackgroundService
	Clock      ports.Clock
	// HashDir hashes a folder on disk; nil means treehash.Dir.
	HashDir func(dir string) (treehash.Tree, error)

	// The version 2 operations (manage.go) also need these; without them
	// those calls fail with ErrMissingDependency and version 1 is unchanged.
	//
	// Links creates and removes symlinks and moves folders without
	// replacing anything.
	Links ports.LinkOps
	// ManagedLock moves whole entries of the skills lock. It is one store,
	// the same as Lock in the real composition, handed to the engine both as
	// its lock and as the one that deletes an empty lock.
	ManagedLock ManagedLock
	// ProcDir is where running processes are listed, read through Dirs and
	// Files; "" means /proc.
	ProcDir string

	// Catalog searches skills.sh (catalog.go, S14). It is optional: without
	// it the catalog calls fail with ErrMissingDependency.
	Catalog ports.Catalog

	// Web reads the https URLs of a website that publishes
	// /.well-known/agent-skills/ (install.go, S14, L84). It is optional:
	// without it a website install fails with ErrMissingDependency.
	Web ports.HTTPSFetcher

	// ProjectDir and Project enable exact inspection, removal, and same-project
	// undo for real skill directories in the startup project directory.
	ProjectDir string
	Project    ProjectPort
}

func (d Deps) validate() error {
	var missing []string
	for name, port := range map[string]any{
		"Files": d.Files, "Dirs": d.Dirs, "Lock": d.Lock, "State": d.State, "Settings": d.Settings,
		"GitHub": d.GitHub, "Git": d.Git, "Notifier": d.Notifier, "Background": d.Background, "Clock": d.Clock,
	} {
		if port == nil {
			missing = append(missing, name)
		}
	}
	if d.Paths.Home() == "" {
		missing = append(missing, "Paths")
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("%w: %s", ErrMissingDependency, strings.Join(missing, ", "))
	}
	return nil
}

// App is the use-case facade.
type App struct {
	d          Deps
	upd        *updates.Service
	projectDir string
}

// New builds an App.
func New(d Deps) (*App, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	a := &App{d: d, projectDir: normalizeProjectDir(d.Paths, d.ProjectDir)}
	upd, err := updates.New(updates.Deps{
		Paths: d.Paths, Files: d.Files, Dirs: d.Dirs, Lock: d.Lock, State: d.State,
		GitHub: d.GitHub, Clock: d.Clock, HashDir: d.HashDir,
		Git: d.Git, Clones: a.clones,
	})
	if err != nil {
		return nil, err
	}
	a.upd = upd
	return a, nil
}

// scan lists the skills as inventory.Scan does, without what the update flow
// knows.
func (a *App) scan(ctx context.Context) ([]domain.Skill, error) {
	settings, err := a.d.Settings.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	skills, err := inventory.Scan(ctx, inventory.Deps{
		Paths: a.d.Paths, Agents: a.d.Agents, Dirs: a.d.Dirs, Files: a.d.Files,
		Lock: a.d.Lock, Git: a.d.Git, HashDir: a.d.HashDir,
	}, settings.Ignore)
	if err != nil {
		return nil, fmt.Errorf("scan the skills: %w", err)
	}
	return skills, nil
}

// clones lists the git clones the inventory finds, with the ids of their
// skills, for the update flow to check. A clone whose skills the user all
// ignored is not checked.
func (a *App) clones(ctx context.Context) ([]updates.Clone, error) {
	skills, err := a.scan(ctx)
	if err != nil {
		return nil, err
	}
	var out []updates.Clone
	for _, s := range skills {
		if s.Origin.Kind != domain.OriginGitClone || s.Origin.GitClone == nil {
			continue
		}
		root := s.Origin.GitClone.Root
		i := slices.IndexFunc(out, func(c updates.Clone) bool { return c.Root == root })
		if i < 0 {
			out = append(out, updates.Clone{Root: root})
			i = len(out) - 1
		}
		out[i].Skills = append(out[i].Skills, s.ID)
	}
	return out, nil
}

// cloneTarget is the update target of a clone skill: its clone, git:<root>.
// Any other skill is its own target.
func (a *App) cloneTarget(s domain.Skill) string {
	if s.Origin.Kind != domain.OriginGitClone || s.Origin.GitClone == nil {
		return s.ID
	}
	rel, ok := a.d.Paths.HomeRel(s.Origin.GitClone.Root)
	if !ok {
		return s.ID
	}
	return domain.GitID(rel)
}

// Skills lists every skill the user installed, as inventory.Scan does, with
// what the update flow knows on top. A skill the inventory leaves unchecked
// takes the state of its last check: update_available when a candidate is
// pending, whatever that check stored; up_to_date, update_available or
// check_failed otherwise; and removed_upstream when its folder is gone
// upstream. The skills of a git clone take the result of their clone, and
// carry locally_modified when the check blocked the clone for local changes
// or commits. Disabled, missing and origin-less skills keep their state. The
// target of an interrupted operation carries interrupted_update, and so do
// all the skills of an interrupted clone.
func (a *App) Skills(ctx context.Context) ([]domain.Skill, error) {
	skills, err := a.scan(ctx)
	if err != nil {
		return nil, err
	}
	open, err := a.upd.Candidates()
	if err != nil {
		return nil, err
	}
	op, hasOp, err := a.upd.Interrupted()
	if err != nil {
		return nil, fmt.Errorf("read the operation in progress: %w", err)
	}
	st, err := a.d.State.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	for i := range skills {
		s := &skills[i]
		tgt := a.cloneTarget(*s)
		if s.State == domain.StateUnchecked {
			applyResult(s, st.Results[tgt], slices.ContainsFunc(open, func(c domain.UpdateCandidate) bool {
				return c.State == domain.CandidatePending && targets(c, s.ID)
			}))
		}
		if slices.ContainsFunc(open, func(c domain.UpdateCandidate) bool { return blockedByLocalChanges(c) && targets(c, s.ID) }) &&
			!slices.Contains(s.Issues, domain.IssueLocallyModified) {
			// locally_modified comes first in the domain's order.
			s.Issues = append([]domain.Issue{domain.IssueLocallyModified}, s.Issues...)
		}
		if hasOp && (op.Target == s.ID || op.Target == tgt) && !slices.Contains(s.Issues, domain.IssueInterruptedUpdate) {
			s.Issues = append(s.Issues, domain.IssueInterruptedUpdate)
		}
	}
	return skills, nil
}

// blockedByLocalChanges reports a clone candidate blocked by the user's own
// changes or commits.
func blockedByLocalChanges(c domain.UpdateCandidate) bool {
	return c.State == domain.CandidateBlocked &&
		(c.BlockReason == domain.BlockDirtyWorktree || c.BlockReason == domain.BlockLocalCommits)
}

// applyResult sets the state of an unchecked skill from its last check
// result r, which is the zero value when it was never checked.
func applyResult(s *domain.Skill, r domain.TargetResult, pending bool) {
	switch {
	case pending:
		s.State = domain.StateUpdateAvailable
	case r.State == domain.StateUpToDate, r.State == domain.StateUpdateAvailable, r.State == domain.StateCheckFailed:
		s.State = r.State
	}
	if r.RemovedUpstream && !slices.Contains(s.Issues, domain.IssueRemovedUpstream) {
		s.Issues = append(s.Issues, domain.IssueRemovedUpstream)
	}
}

// targets reports whether candidate c changes the skill id.
func targets(c domain.UpdateCandidate, id string) bool {
	return c.Target == id || slices.Contains(c.Skills, id)
}

// Resolve turns what the user typed, a skill's name or its id, into an id.
// An id always wins. A name, or an alias, that matches more than one row is
// refused with an *AmbiguousError listing the ids, since no write may pick
// its target by name alone (docs/design.md sections 2 and 9). Targets the
// update flow still knows but the inventory no longer lists, such as an
// ignored skill with a pending update or an applied one to undo, are matched
// by id and by lock name.
func (a *App) Resolve(ctx context.Context, ref string) (string, error) {
	skills, err := a.Skills(ctx)
	if err != nil {
		return "", err
	}
	known, err := a.knownTargets()
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(skills, func(s domain.Skill) bool { return s.ID == ref }) || slices.Contains(known, ref) {
		return ref, nil
	}
	var ids []string
	for _, s := range skills {
		if s.Name == ref || slices.Contains(s.Aliases, ref) {
			ids = append(ids, s.ID)
		}
	}
	if slices.Contains(known, domain.LockID(ref)) {
		ids = append(ids, domain.LockID(ref))
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("%q: %w", ref, ErrSkillNotFound)
	case 1:
		return ids[0], nil
	default:
		return "", &AmbiguousError{Ref: ref, IDs: ids}
	}
}

// knownTargets are the targets of the open candidates and of the journal.
func (a *App) knownTargets() ([]string, error) {
	open, err := a.upd.Candidates()
	if err != nil {
		return nil, err
	}
	journal, err := a.History()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range open {
		out = append(out, c.Target)
	}
	for _, e := range journal {
		out = append(out, e.Target)
	}
	return out, nil
}

// Interrupted returns the operation that did not finish, if any.
func (a *App) Interrupted() (domain.Operation, bool, error) { return a.upd.Interrupted() }

// Candidates returns the candidates waiting for the user: pending and
// blocked ones.
func (a *App) Candidates() ([]domain.UpdateCandidate, error) { return a.upd.Candidates() }

// CandidateFor returns the candidate waiting for the user for the skill id.
func (a *App) CandidateFor(id string) (domain.UpdateCandidate, error) {
	cands, err := a.upd.Candidates()
	if err != nil {
		return domain.UpdateCandidate{}, err
	}
	for _, c := range cands {
		if targets(c, id) {
			return c, nil
		}
	}
	return domain.UpdateCandidate{}, fmt.Errorf("%s: %w", id, ErrNoCandidate)
}

// Apply applies one approved candidate.
func (a *App) Apply(ctx context.Context, ap updates.Approval) (updates.ApplyResult, error) {
	return a.upd.Apply(ctx, ap)
}

// ApplyAll applies the approved candidates one by one; locally modified ones
// are left out.
func (a *App) ApplyAll(ctx context.Context, aps []updates.Approval) []updates.ApplyResult {
	return a.upd.ApplyAll(ctx, aps)
}

// Skip skips a candidate's version.
func (a *App) Skip(ctx context.Context, ap updates.Approval) error { return a.upd.Skip(ctx, ap) }

// Undo puts back what an apply replaced. The id of a skill inside a git
// clone undoes the apply of its clone, which changed all of its skills.
func (a *App) Undo(ctx context.Context, req updates.UndoRequest) (updates.UndoResult, error) {
	target, err := a.undoTarget(req.Target)
	if err != nil {
		return updates.UndoResult{Target: req.Target}, err
	}
	req.Target = target
	return a.upd.Undo(ctx, req)
}

// undoTarget maps the id of a skill inside a clone to the clone the journal
// recorded an apply of: the deepest git:<root> whose folder holds it. Any
// other id is its own target.
func (a *App) undoTarget(id string) (string, error) {
	if !strings.HasPrefix(id, domain.GitID("")) {
		return id, nil
	}
	journal, err := a.History()
	if err != nil {
		return "", err
	}
	best := id
	for _, e := range journal {
		if e.Action != domain.ActionApplied || !strings.HasPrefix(e.Target, domain.GitID("")) {
			continue
		}
		if e.Target == id {
			return id, nil
		}
		if strings.HasPrefix(id, e.Target+"/") && (best == id || len(e.Target) > len(best)) {
			best = e.Target
		}
	}
	return best, nil
}

// Snapshots returns the snapshots of a target, newest first.
func (a *App) Snapshots(target string) ([]domain.Snapshot, error) { return a.upd.Snapshots(target) }

// Recover resolves an interrupted operation; only a user action calls it.
func (a *App) Recover(ctx context.Context) (updates.RecoverResult, error) { return a.upd.Recover(ctx) }

// History returns the journal, oldest first.
func (a *App) History() ([]domain.HistoryEntry, error) {
	entries, err := a.d.State.Journal()
	if err != nil {
		return nil, fmt.Errorf("read the journal: %w", err)
	}
	return entries, nil
}

// Paths returns where skillhub reads and writes, for the entry points to
// show.
func (a *App) Paths() paths.Paths { return a.d.Paths }

// LastSuccessfulCheck returns when the last successful check finished; zero
// when none did.
func (a *App) LastSuccessfulCheck() (time.Time, error) {
	st, err := a.d.State.Load()
	if err != nil {
		return time.Time{}, fmt.Errorf("load state: %w", err)
	}
	return st.Checks.LastSuccess, nil
}
