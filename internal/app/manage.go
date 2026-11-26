package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"skillhub/internal/data"
	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/inventory"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/manage/remove"
	"skillhub/internal/manage/repair"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// The version 2 operations: the composition of the engine and the planners,
// and the plan-bound approval (docs/design-v2.md sections 11, point 8, 14
// and 15, point 7). The entry points of version 2 reach the engine, the
// planners, the graph and Doctor only through these methods.

// ManagedLock is the lock store of version 2. The engine takes it both as
// its lock and as the one that deletes a lock left empty, so the two are the
// same store.
type ManagedLock interface {
	engine.Lock
	engine.LockDropper
}

// manager is the version 2 composition for one call.
type manager struct {
	a         *App
	ctx       context.Context
	harnesses []data.Harness
	profiles  []data.DiagnosticProfile
	engine    *engine.Engine
	toggle    *toggle.Planner
	remove    *remove.Remover
	repair    *repair.Planner
}

// observer is the graph source the repair planner observes through.
type observer struct{ m *manager }

func (o observer) Observe() (graph.Graph, error) { return o.m.observe() }

// v2 composes the engine and the planners over the ports the App holds.
func (a *App) v2(ctx context.Context) (*manager, error) {
	var missing []string
	if a.d.Links == nil {
		missing = append(missing, "Links")
	}
	if a.d.ManagedLock == nil {
		missing = append(missing, "ManagedLock")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrMissingDependency, strings.Join(missing, ", "))
	}
	harnesses, err := data.LoadHarnesses(a.d.Paths, a.d.Files)
	if err != nil {
		return nil, fmt.Errorf("load the harness table: %w", err)
	}
	profiles, err := data.DiagnosticProfiles()
	if err != nil {
		return nil, fmt.Errorf("load the diagnostic profiles: %w", err)
	}
	m := &manager{a: a, ctx: ctx, harnesses: harnesses, profiles: profiles}
	m.engine = engine.New(engine.Deps{
		Paths: a.d.Paths, Files: a.d.Files, Links: a.d.Links,
		Lock: a.d.ManagedLock, DropLock: a.d.ManagedLock,
		State: a.d.State, Dirs: a.d.Dirs, Clock: a.d.Clock, HashDir: a.d.HashDir,
		ProjectDir: a.projectDir, ProjectFS: a.d.Project,
	})
	m.toggle = toggle.New(toggle.Deps{
		Paths: a.d.Paths, Harnesses: harnesses, Profiles: profiles,
		Plans: m.engine, State: a.d.State, Lock: a.d.ManagedLock, Stat: a.d.Files, Dirs: a.d.Dirs,
	})
	m.remove = remove.New(remove.Deps{
		Paths: a.d.Paths, Planner: m.engine, Lock: a.d.ManagedLock, State: a.d.State,
		Observe: m.observe, Harnesses: harnesses, Profiles: profiles,
		Dirs: a.d.Dirs, Links: a.d.Links, Files: a.d.Files, HashDir: a.d.HashDir,
		ProjectDir: a.projectDir, Project: a.d.Project,
	})
	m.repair = repair.New(repair.Deps{
		Paths: a.d.Paths, Harnesses: harnesses, Profiles: profiles,
		Observe: observer{m}, Plans: m.engine, Toggle: m.toggle,
		State: a.d.State, Lock: a.d.ManagedLock, Stat: a.d.Files, HashDir: a.d.HashDir, Dirs: a.d.Dirs,
	})
	return m, nil
}

// observe builds the graph from the inventory without the ignore list: an
// ignored skill is still on disk and still managed (L37).
func (m *manager) observe() (graph.Graph, error) {
	a := m.a
	skills, err := inventory.Scan(m.ctx, inventory.Deps{
		Paths: a.d.Paths, Agents: a.d.Agents, Dirs: a.d.Dirs, Files: a.d.Files,
		Lock: a.d.Lock, Git: a.d.Git, HashDir: a.d.HashDir,
	}, nil)
	if err != nil {
		return graph.Graph{}, fmt.Errorf("scan the skills: %w", err)
	}
	g, err := graph.Build(graph.Deps{
		Paths: a.d.Paths, Harnesses: m.harnesses, Profiles: m.profiles, Inventory: skills,
		Dirs: a.d.Dirs, Files: a.d.Files, Links: a.d.Links, HashDir: a.d.HashDir,
	})
	if err != nil {
		return graph.Graph{}, fmt.Errorf("build the skills graph: %w", err)
	}
	return g, nil
}

// Plan plans req. It only reads.
func (a *App) Plan(ctx context.Context, req Request) (Proposal, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return Proposal{}, err
	}
	return m.plan(req)
}

// PlanDisable plans disabling the skill key in every managed target.
func (a *App) PlanDisable(ctx context.Context, key string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionDisable, Key: key})
}

// PlanEnable plans enabling the disabled skill key with the links it had.
func (a *App) PlanEnable(ctx context.Context, key string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionEnable, Key: key})
}

// PlanUnlink plans removing the link of skill key from harness.
func (a *App) PlanUnlink(ctx context.Context, key, harness string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionUnlink, Key: key, Harness: harness})
}

// PlanLink plans linking skill key in harness.
func (a *App) PlanLink(ctx context.Context, key, harness string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionLink, Key: key, Harness: harness})
}

// PlanAdoptOff plans parking the skills of ~/.agents/skills/OFF/.
func (a *App) PlanAdoptOff(ctx context.Context) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionAdoptOff})
}

// PlanRemove plans removing the skill key, active or disabled.
func (a *App) PlanRemove(ctx context.Context, key string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionRemove, Key: key})
}

// PlanRemoveGroup plans removing all eligible global copies and links
// attributed to the scanner-observed graph skillID.
func (a *App) PlanRemoveGroup(ctx context.Context, skillID string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionRemove, Key: skillID, Group: true})
}

// PlanUndo plans undoing the version 2 operation of snapshot.
func (a *App) PlanUndo(ctx context.Context, snapshot string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionUndo, Snapshot: snapshot})
}

// PlanRepair plans the repair of the finding with that id; canonical is the
// folder the user chose when the repair needs one.
func (a *App) PlanRepair(ctx context.Context, finding, repairKind, canonical string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionRepair, Finding: finding, Repair: repairKind, Canonical: canonical})
}

// PlanInstall plans installing a prepared revision in targets. The lock
// entry's time is fixed now and travels in the request.
func (a *App) PlanInstall(ctx context.Context, rev Revision, targets []string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionInstall, Install: &InstallRequest{
		Revision: rev, Targets: slices.Clone(targets), At: a.d.Clock.Now().UTC().Truncate(time.Millisecond),
	}})
}

// PlanReinstall plans installing again a prepared revision of a skill whose
// lock entry has no folder (Doctor's reinstall repair): one plan removes
// that entry and installs rev in targets. rev comes from ResolveInstall of
// the Input that PlanRepair's Reinstall carries. The lock entry's time is
// fixed now and travels in the request, as for an install.
func (a *App) PlanReinstall(ctx context.Context, rev Revision, targets []string) (Proposal, error) {
	return a.Plan(ctx, Request{Action: ActionReinstall, Install: &InstallRequest{
		Revision: rev, Targets: slices.Clone(targets), At: a.d.Clock.Now().UTC().Truncate(time.Millisecond),
	}})
}

// ResolveInstall resolves a GitHub source, or a website that publishes
// /.well-known/agent-skills/ (install.go), to one skill and prepares it for
// review against nothing, as an update is prepared. It writes only the
// install staging, a cache, and refuses with nothing written, and nothing
// read, while an operation is pending.
func (a *App) ResolveInstall(ctx context.Context, input string) (Revision, error) {
	// An operation pending owns the staging: refuse before writing anything
	// there, as every other command does (A1 of P4).
	if err := a.CheckPending(); err != nil {
		return Revision{}, err
	}
	src, err := install.Parse(input)
	if err != nil {
		return Revision{}, refusal(err)
	}
	if src.Site != "" && a.d.Web == nil {
		return Revision{}, missingWeb()
	}
	// What an earlier install left in the staging and no operation owns goes
	// before a new copy is prepared (D1).
	if _, err := a.clearOrphanStaging(); err != nil {
		return Revision{}, err
	}
	var res install.Resolved
	if src.Site != "" {
		res, err = a.resolveWeb(ctx, src)
	} else {
		res, err = a.resolveGitHub(ctx, src)
	}
	if err != nil {
		return Revision{}, err
	}
	rev, err := install.Preparer{Paths: a.d.Paths, Files: a.d.Files, GitHub: a.d.GitHub, HashDir: a.hashDir}.Prepare(ctx, res)
	if err != nil {
		if res.Site != "" {
			return Revision{}, webRefusal(err)
		}
		return Revision{}, refusal(err)
	}
	return Revision{
		Input: input, Key: res.Key, Name: res.Name, Repo: res.Repo, Commit: res.Commit,
		SkillPath: res.SkillPath, SourceURL: res.SourceURL, TreeSHA: rev.TreeSHA, StagedPath: rev.StagedPath,
		FollowsDefaultBranch: res.Ref == "" && res.Site == "", FileChanges: rev.FileChanges,
		FrontmatterDelta: rev.FrontmatterDelta, Labels: rev.Labels, Prepared: rev, Site: res.Site,
	}, nil
}

// resolveGitHub resolves a GitHub source. Resolving only reads GitHub: an
// error that is no known refusal is GitHub that could not be read (no
// network, gh failing).
func (a *App) resolveGitHub(ctx context.Context, src install.Source) (install.Resolved, error) {
	res, err := install.Resolver{GitHub: a.d.GitHub}.Resolve(ctx, src)
	if err != nil {
		if r := refusal(err); isRefusal(r) {
			return install.Resolved{}, r
		}
		return install.Resolved{}, &RefusalError{Kind: ErrGitHubUnavailable, Err: err}
	}
	return res, nil
}

// ProposeTargets proposes the install targets: the detected administrable
// harnesses and the saved preference. It is a proposal, never a permission.
func (a *App) ProposeTargets(ctx context.Context) ([]string, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return nil, err
	}
	g, err := m.observe()
	if err != nil {
		return nil, err
	}
	st, err := a.d.State.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	return install.ProposeTargets(g, m.harnesses, st.Preferences), nil
}

// Doctor lists the findings. It only reads: no counter, no state.
func (a *App) Doctor(ctx context.Context) ([]Finding, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return nil, err
	}
	g, err := m.observe()
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, f := range doctor.Detect(g) {
		out = append(out, findingView(f))
	}
	return out, nil
}

// Repairs tells what Doctor can do with the finding with that id now.
func (a *App) Repairs(ctx context.Context, finding string) (Offer, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return Offer{}, err
	}
	g, f, err := m.finding(finding)
	if err != nil {
		return Offer{}, err
	}
	o, err := m.repair.Repairs(g, f)
	if err != nil {
		return Offer{}, refusal(err)
	}
	out := Offer{Reason: string(o.Reason)}
	for _, k := range o.Repairs {
		out.Repairs = append(out.Repairs, string(k))
	}
	return out, nil
}

// finding observes and finds the finding with that id.
func (m *manager) finding(id string) (graph.Graph, doctor.Finding, error) {
	g, err := m.observe()
	if err != nil {
		return graph.Graph{}, doctor.Finding{}, err
	}
	findings := doctor.Detect(g)
	i := slices.IndexFunc(findings, func(f doctor.Finding) bool { return f.ID == id })
	if i < 0 {
		return graph.Graph{}, doctor.Finding{}, refusal(&repair.StaleError{ID: id})
	}
	return g, findings[i], nil
}

// plan runs the planner of req.Action and turns its proposal into the
// app's, with the running harnesses and the reload list.
func (m *manager) plan(req Request) (Proposal, error) {
	p, err := m.planOnly(req)
	if err != nil {
		return Proposal{}, err
	}
	if err := m.decorate(&p); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// planOnly is plan without the running harnesses and the reload list: what
// ApplyPlan compares, with nothing slow between planning again and Begin
// (A1 of L51).
func (m *manager) planOnly(req Request) (Proposal, error) {
	p, err := m.planRaw(req)
	if err != nil {
		// A pending operation may be what the planner tripped over (an
		// enable of what it disables finds nothing parked): recover comes
		// first, as for every other command (L65).
		if perr := m.a.CheckPending(); errors.Is(perr, ErrOperationPending) {
			return Proposal{}, perr
		}
		return Proposal{}, refusal(err)
	}
	p.Request = req
	if p.PlanID == "" {
		return p, nil
	}
	p.Steps = steps(p.plan)
	p.Operation = engine.JournalTarget(p.plan)
	p.Targets = slices.Clone(p.plan.Targets)
	return p, nil
}

// decorate adds to a plan the harness processes running now and the
// harnesses to reload. Both are warnings, never part of the plan.
func (m *manager) decorate(p *Proposal) error {
	if p.PlanID == "" {
		return nil
	}
	p.Running = m.running()
	reload, err := m.reload(p.plan)
	if err != nil {
		return err
	}
	p.Reload = reload
	return nil
}

func (m *manager) planRaw(req Request) (Proposal, error) {
	switch req.Action {
	case ActionDisable, ActionUnlink, ActionLink, ActionAdoptOff:
		g, err := m.observe()
		if err != nil {
			return Proposal{}, err
		}
		var tp toggle.Proposal
		switch req.Action {
		case ActionDisable:
			tp, err = m.toggle.Disable(g, req.Key)
		case ActionUnlink:
			tp, err = m.toggle.Unlink(g, req.Key, req.Harness)
		case ActionLink:
			tp, err = m.toggle.Link(g, req.Key, req.Harness)
		default:
			tp, err = m.toggle.AdoptOff(g)
		}
		if err != nil {
			return Proposal{}, err
		}
		return Proposal{PlanID: tp.Plan.ID, plan: tp.Plan, Toggle: toggleView(tp.Preview)}, nil
	case ActionEnable:
		tp, err := m.toggle.Enable(req.Key)
		if err != nil {
			return Proposal{}, err
		}
		return Proposal{PlanID: tp.Plan.ID, plan: tp.Plan, Toggle: toggleView(tp.Preview)}, nil
	case ActionRemove:
		var rp remove.Plan
		var err error
		if req.Group {
			rp, err = m.remove.RemoveGroup(req.Key)
		} else {
			rp, err = m.remove.Remove(req.Key)
		}
		if err != nil {
			return Proposal{}, err
		}
		return Proposal{PlanID: rp.Plan.ID, plan: rp.Plan, Remove: removeView(rp.Preview)}, nil
	case ActionInstall:
		return m.planInstall(req)
	case ActionReinstall:
		return m.planInstall(req)
	case ActionRepair:
		return m.planRepair(req)
	case ActionUndo:
		plan, err := m.engine.UndoPlan(req.Snapshot)
		if err != nil {
			return Proposal{}, err
		}
		snap, err := m.engine.ReadSnapshot(req.Snapshot)
		if err != nil {
			return Proposal{}, err
		}
		return Proposal{PlanID: plan.ID, plan: plan, Undo: &UndoPreview{Snapshot: req.Snapshot, Of: snap.Plan.Action}}, nil
	}
	return Proposal{}, fmt.Errorf("%w: unknown action %q", ErrInvalidRequest, req.Action)
}

// planInstall plans an install, or a reinstall when req.Action is
// ActionReinstall.
func (m *manager) planInstall(req Request) (Proposal, error) {
	if req.Install == nil {
		return Proposal{}, fmt.Errorf("%w: an %s needs its revision", ErrInvalidRequest, req.Action)
	}
	a := m.a
	planner := install.NewPlanner(install.Deps{
		Paths: a.d.Paths, Harnesses: m.harnesses, Plans: m.engine, State: a.d.State,
		Lock: a.d.ManagedLock, Stat: a.d.Files, Clock: fixedClock(req.Install.At), Dirs: a.d.Dirs,
	})
	plan := planner.Install
	if req.Action == ActionReinstall {
		plan = planner.Reinstall
	}
	ip, err := plan(req.Install.Revision.Prepared, req.Install.Targets)
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{PlanID: ip.Plan.ID, plan: ip.Plan, Install: installView(ip.Preview, req.Install.Revision)}, nil
}

func (m *manager) planRepair(req Request) (Proposal, error) {
	_, seen, err := m.finding(req.Finding)
	if err != nil {
		return Proposal{}, err
	}
	if req.Canonical != "" && repair.Kind(req.Repair) != repair.Adopt {
		// Only adopt chooses a canonical folder: never ignore the choice.
		return Proposal{}, &RefusalError{Kind: ErrCanonicalNotUsed,
			Err: fmt.Errorf("%w: %s takes no canonical folder (%s)", ErrCanonicalNotUsed, req.Repair, req.Canonical)}
	}
	rp, err := m.repair.Plan(seen, repair.Kind(req.Repair), repair.Options{Canonical: req.Canonical})
	if err != nil {
		return Proposal{}, err
	}
	pv := repairView(rp.Preview)
	if rp.Reinstall != nil {
		pv.Reinstall = reinstallView(*rp.Reinstall)
		return Proposal{Repair: pv}, nil
	}
	return Proposal{PlanID: rp.Plan.ID, plan: rp.Plan, Repair: pv}, nil
}

// reinstallView derives the install source of a lock entry: owner/repo, and
// the folder of its SKILL.md when it is not the root.
func reinstallView(r repair.ReinstallRef) *Reinstall {
	out := &Reinstall{Key: r.Key, Source: r.Source, SourceType: r.SourceType, SourceURL: r.SourceURL,
		SkillPath: r.SkillPath, Ref: r.Ref}
	if r.SourceType != "github" || r.Source == "" {
		return out
	}
	input := r.Source
	if dir := path.Dir(r.SkillPath); dir != "." && dir != "/" && dir != "" {
		input += "/" + dir
	}
	if _, err := install.Parse(input); err == nil {
		out.Input = input
	}
	return out
}

// fixedClock is the time an install request carries.
type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

// reload are the harnesses that read what plan changes: the ones it
// reaches; every harness whose link directory or a read root holds a link
// the plan creates or removes, or anything else it changes outside the
// store, so a directory two harnesses read names both (opencode reads
// ~/.claude/skills too) and the list never has fewer than the plan shows
// (L65); and every detected harness that reads the store when a step
// changes something in it.
func (m *manager) reload(plan engine.Plan) ([]string, error) {
	out := slices.Clone(plan.Targets)
	store := m.a.d.Paths.StoreDir()
	in := func(p string) bool { return p != "" && (p == store || strings.HasPrefix(p, store+"/")) }
	for _, s := range plan.Steps {
		for _, p := range []string{s.Src, s.Dst, s.Path} {
			if p == "" || (in(p) && s.Kind != engine.StepLink && s.Kind != engine.StepUnlink) {
				continue
			}
			out = append(out, m.readersOf(filepath.Dir(p))...)
		}
	}
	touches := slices.ContainsFunc(plan.Steps, func(s engine.Step) bool { return in(s.Src) || in(s.Dst) || in(s.Path) })
	if touches {
		detected, err := m.detected()
		if err != nil {
			return nil, err
		}
		for _, h := range m.harnesses {
			if h.ReadsStore == data.Yes && slices.Contains(detected, h.ID) {
				out = append(out, h.ID)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// readersOf are the harnesses whose link directory or a read root is dir
// or holds it.
func (m *manager) readersOf(dir string) []string {
	var out []string
	for _, h := range m.harnesses {
		dirs := []string{h.LinkDir}
		for _, r := range h.ReadRoots {
			dirs = append(dirs, r.Dir)
		}
		for _, rel := range dirs {
			if rel == "" {
				continue
			}
			abs, err := m.a.d.Paths.Join(filepath.FromSlash(rel))
			if err == nil && (dir == abs || strings.HasPrefix(dir, abs+string(filepath.Separator))) {
				out = append(out, h.ID)
				break
			}
		}
	}
	return out
}

// detected are the harnesses the graph sees on this machine.
func (m *manager) detected() ([]string, error) {
	g, err := m.observe()
	if err != nil {
		return nil, err
	}
	return g.Harnesses, nil
}

// ApplyPlan runs what req asks for, only if planning it again gives the
// identifier the user approved: a plan that changed is refused with
// ErrPlanChanged and nothing runs, and a dry run stops after that check
// without taking the state lock. After a remove, still under the state lock,
// each open update candidate of the skill becomes superseded.
//
// Nothing but the comparison runs between planning again and Begin: the
// running harnesses and the reload list of the result are read after the
// operation (A1 of L51). Once the operation ran, Result.SnapshotID is set
// and any error returned with it is a warning about what came after.
func (a *App) ApplyPlan(ctx context.Context, req Request, approvedID string) (res Result, err error) {
	m, err := a.v2(ctx)
	if err != nil {
		return Result{}, err
	}
	if req.Action == ActionUndo && !req.DryRun {
		staged, err := m.prepareUndo(req.Snapshot)
		if err != nil {
			return Result{}, err
		}
		if staged {
			// Whatever happens, an undo that did not run leaves no empty
			// staging behind; one that ran is tidied below.
			defer func() {
				if res.SnapshotID == "" {
					err = errors.Join(err, a.tidyStaging())
				}
			}()
		}
	}
	p, err := m.planOnly(req)
	if err != nil {
		return Result{}, err
	}
	if p.PlanID == "" {
		return Result{Proposal: p}, fmt.Errorf("%w: %s %s", ErrNoPlan, req.Action, req.Repair)
	}
	if p.PlanID != approvedID {
		return Result{Proposal: p}, refusal(fmt.Errorf("%w: approved %q, the plan is now %s", engine.ErrPlanChanged, approvedID, p.PlanID))
	}
	if req.DryRun {
		// A dry run tells what the run would refuse first (B5).
		if err := a.CheckPending(); err != nil {
			return Result{Proposal: p}, err
		}
		return Result{Proposal: p, DryRun: true}, nil
	}
	run, err := m.engine.Begin(ctx, p.plan)
	if err != nil {
		return Result{Proposal: p}, refusal(err)
	}
	out, err := run.Execute()
	if err != nil {
		return Result{Proposal: p}, errors.Join(refusal(err), run.Release())
	}
	superseded, err := a.supersede(p)
	if serr := a.tidyAfter(p); serr != nil {
		err = errors.Join(err, fmt.Errorf("the operation ran; empty the install staging: %w", serr))
	}
	err = errors.Join(err, run.Release())
	if derr := m.decorate(&p); derr != nil {
		err = errors.Join(err, fmt.Errorf("the operation ran; list the harnesses to reload: %w", derr))
	}
	return Result{Proposal: p, SnapshotID: out.SnapshotID, Superseded: superseded}, err
}

// ManagedPending reports whether a version 2 operation record is pending:
// RecoverOperation would resolve it. It only reads.
func (a *App) ManagedPending() (bool, error) {
	pending, err := a.d.Files.Exists(engine.RecordFile(a.d.Paths))
	if err != nil {
		return false, fmt.Errorf("look for the version 2 record: %w", err)
	}
	return pending, nil
}

// CheckPending refuses, as Begin would, while an interrupted operation of
// version 1 or a version 2 record is pending: a dry run tells it too. It only
// reads.
func (a *App) CheckPending() error {
	if _, pending, err := a.d.State.Operation(); err != nil {
		return fmt.Errorf("read the version 1 operation: %w", err)
	} else if pending {
		return refusal(engine.ErrV1OperationPending)
	}
	pending, err := a.ManagedPending()
	if err != nil {
		return err
	}
	if pending {
		return refusal(engine.ErrOperationPending)
	}
	return nil
}

// supersede marks superseded the open candidates a remove listed. The
// caller holds the state lock.
func (a *App) supersede(p Proposal) ([]string, error) {
	if p.Remove == nil || len(p.Remove.PendingCandidates) == 0 {
		return nil, nil
	}
	st, err := a.d.State.Load()
	if err != nil {
		return nil, fmt.Errorf("%w: load state: %w", ErrCandidatesNotSuperseded, err)
	}
	var done []string
	for i := range st.Candidates {
		c := &st.Candidates[i]
		if !slices.Contains(p.Remove.PendingCandidates, c.ID) {
			continue
		}
		switch c.State {
		case domain.CandidateApplied, domain.CandidateSkipped, domain.CandidateSuperseded:
			continue
		}
		c.State = domain.CandidateSuperseded
		done = append(done, c.ID)
	}
	if len(done) == 0 {
		return nil, nil
	}
	if err := a.d.State.Save(st); err != nil {
		return nil, fmt.Errorf("%w: save state: %w", ErrCandidatesNotSuperseded, err)
	}
	return done, nil
}

// Recovery is what RecoverOperation resolved: Version 2 when a version 2
// record was pending, 1 otherwise.
type Recovery struct {
	Version int
	V1      updates.RecoverResult
	V2      ManagedRecovery
	// Leftovers is whether recover removed an orphan install staging: copies
	// an install left there, crashed before its record or while it removed
	// them, that no pending operation owned.
	Leftovers bool
}

// ManagedRecovery is the engine's recover result.
type ManagedRecovery struct {
	Found      bool
	Committed  bool
	RolledBack bool
	PlanID     string
	SnapshotID string
}

// RecoverOperation resolves an interrupted operation: a version 2 one
// while operation-v2.json exists, the version 1 one otherwise. Only a user
// action calls it.
func (a *App) RecoverOperation(ctx context.Context) (Recovery, error) {
	pending, err := a.d.Files.Exists(engine.RecordFile(a.d.Paths))
	if err != nil {
		return Recovery{}, fmt.Errorf("look for the version 2 record: %w", err)
	}
	if !pending {
		cleared, err := a.clearOrphanStaging()
		if err != nil {
			return Recovery{Version: 1}, err
		}
		res, err := a.upd.Recover(ctx)
		if cleared && errors.Is(err, updates.ErrNoOperation) {
			err = nil
		}
		return Recovery{Version: 1, V1: res, Leftovers: cleared}, err
	}
	m, err := a.v2(ctx)
	if err != nil {
		return Recovery{Version: 2}, err
	}
	res, err := m.engine.Recover(ctx)
	out := Recovery{Version: 2, V2: ManagedRecovery{
		Found: res.Found, Committed: res.Committed, RolledBack: res.RolledBack,
		PlanID: res.PlanID, SnapshotID: res.SnapshotID,
	}}
	if err != nil {
		return out, refusal(err)
	}
	// Once the record is resolved, nothing owns the staging any more.
	if _, terr := a.clearOrphanStaging(); terr != nil {
		return out, fmt.Errorf("the operation was recovered; empty the install staging: %w", terr)
	}
	return out, nil
}

// UndoLine names the journal line to undo. Target and SnapshotID are the
// line's; Confirm is version 1's confirmation of a changed folder; PlanID is
// the undo plan the user approved for a version 2 line (PlanUndo).
type UndoLine struct {
	Target     string
	SnapshotID string
	Confirm    bool
	PlanID     string
	DryRun     bool
}

// Undone is what UndoOperation did, by version.
type Undone struct {
	Version int
	V1      updates.UndoResult
	V2      Result
}

// UndoOperation undoes a journal line: a version 2 one (target v2:...)
// through the engine, bound to the approved undo plan; any other through
// the version 1 flow.
func (a *App) UndoOperation(ctx context.Context, req UndoLine) (Undone, error) {
	if strings.HasPrefix(req.Target, "v2:") {
		res, err := a.ApplyPlan(ctx, Request{Action: ActionUndo, Snapshot: req.SnapshotID, DryRun: req.DryRun}, req.PlanID)
		return Undone{Version: 2, V2: res}, err
	}
	res, err := a.Undo(ctx, updates.UndoRequest{Target: req.Target, SnapshotID: req.SnapshotID, Confirm: req.Confirm})
	return Undone{Version: 1, V1: res}, err
}
