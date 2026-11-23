package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// sourceTypeWellKnown is the sourceType of a lock entry whose skill comes
// from a website's /.well-known/agent-skills/ index.
const sourceTypeWellKnown = "well-known"

// CheckOutcome is what a check found for one target.
type CheckOutcome string

const (
	// CheckUpToDate: upstream matches the lock entry.
	CheckUpToDate CheckOutcome = "up_to_date"
	// CheckUpdateAvailable: a pending candidate waits for the user.
	CheckUpdateAvailable CheckOutcome = "update_available"
	// CheckBlocked: upstream changed but the candidate cannot be applied; see
	// the candidate's block reason.
	CheckBlocked CheckOutcome = "blocked"
	// CheckSkipped: the user skipped exactly this upstream version.
	CheckSkipped CheckOutcome = "skipped"
	// CheckFailed: the target could not be checked; see Err.
	CheckFailed CheckOutcome = "failed"
	// CheckNotChecked: the target is not checked: its folder is missing,
	// disabled, a link outside the store or a git clone the lock names, its
	// source is not GitHub, it is a clone whose HEAD is not on a branch that
	// tracks origin, or it has an interrupted operation.
	CheckNotChecked CheckOutcome = "not_checked"
)

// TargetCheck is the result of checking one lock entry or one clone.
type TargetCheck struct {
	Target  string
	Outcome CheckOutcome
	// CandidateID is the candidate of CheckUpdateAvailable and CheckBlocked.
	CandidateID string
	// RemovedUpstream is true for CheckUpToDate when the skill's folder no
	// longer exists upstream: nothing to update, and not a failure.
	RemovedUpstream bool
	// WebSource is true for CheckNotChecked when the entry's source is a
	// website (sourceType well-known, L84): its updates are not checked yet,
	// which is no failure.
	WebSource bool
	Err       error
}

// CheckReport is the result of a check.
type CheckReport struct {
	// Targets has one result per lock entry, in the lock's order, then one
	// per git clone, in the order Deps.Clones lists them.
	Targets []TargetCheck
	// NewCandidates are the ids of the candidates this check created.
	NewCandidates []string
}

// Failed reports whether any target failed.
func (r CheckReport) Failed() bool {
	return slices.ContainsFunc(r.Targets, func(t TargetCheck) bool { return t.Outcome == CheckFailed })
}

// Candidates returns the candidates that wait for the user: pending and
// blocked ones.
func (s *Service) Candidates() ([]domain.UpdateCandidate, error) {
	st, err := s.d.State.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	out := []domain.UpdateCandidate{}
	for _, c := range st.Candidates {
		if isOpen(c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func isOpen(c domain.UpdateCandidate) bool {
	return c.State == domain.CandidatePending || c.State == domain.CandidateBlocked
}

// checkItem is one lock entry to check.
type checkItem struct {
	entry  domain.LockEntry
	folder string
}

// checkGroup is the lock entries of one repository at one ref; they share
// one head lookup and one tree.
type checkGroup struct {
	repo, ref string
	items     []checkItem
}

func (g checkGroup) key() string { return g.repo + "@" + g.ref }

// checkRun is the state of one Check.
type checkRun struct {
	s       *Service
	ctx     context.Context
	now     time.Time
	st      *domain.PersistedState
	heads   headsCache
	results map[string]TargetCheck
	created []string
}

// LockEntryCheckable reports whether a lock entry's source can be checked for
// updates: it must be a valid owner/name GitHub repository. The second return
// value is true when the entry comes from a website's well-known index.
func LockEntryCheckable(e domain.LockEntry) (checkable bool, webSource bool) {
	if e.SourceType != "github" || !validRepo(e.Source) {
		return false, e.SourceType == sourceTypeWellKnown
	}
	return true, false
}

// CloneCheckable reports whether a git clone's origin remote points at a
// GitHub repository that skillhub can check.
func CloneCheckable(remote string) bool {
	_, ok := githubRepo(remote)
	return ok
}

// Check detects upstream changes of the lock skills and the git clones,
// stages and labels new candidates, and records the check moments. It never
// changes a skills folder, a clone or the lock, and never recovers an
// interrupted operation.
func (s *Service) Check(ctx context.Context) (CheckReport, error) {
	release, err := s.d.State.Lock(ctx)
	if err != nil {
		return CheckReport{}, fmt.Errorf("take the state lock: %w", err)
	}
	defer release() //nolint:errcheck // releasing an advisory lock; the process exit releases it too.

	if err := s.GuardV2Operation(); err != nil {
		return CheckReport{}, err
	}
	st, err := s.d.State.Load()
	if err != nil {
		return CheckReport{}, fmt.Errorf("load state: %w", err)
	}
	op, hasOp, err := s.d.State.Operation()
	if err != nil {
		return CheckReport{}, fmt.Errorf("read the operation in progress: %w", err)
	}
	lock, err := s.d.Lock.Read()
	if err != nil {
		return CheckReport{}, fmt.Errorf("read the skills lock: %w", err)
	}
	clones, err := s.listClones(ctx)
	if err != nil {
		return CheckReport{}, err
	}

	run := &checkRun{s: s, ctx: ctx, now: s.d.Clock.Now(), st: &st, heads: s.loadHeads(), results: map[string]TargetCheck{}}
	var groups []*checkGroup
	for _, e := range lock.Entries {
		tgt := domain.LockID(e.Name)
		if hasOp && op.Target == tgt {
			run.results[tgt] = TargetCheck{Target: tgt, Outcome: CheckNotChecked, Err: ErrInterrupted}
			continue
		}
		if ok, web := LockEntryCheckable(e); !ok {
			run.results[tgt] = TargetCheck{Target: tgt, Outcome: CheckNotChecked, WebSource: web}
			continue
		}
		folder, err := s.storeFolder(e.Name)
		if err != nil {
			run.results[tgt] = TargetCheck{Target: tgt, Outcome: CheckNotChecked, Err: err}
			continue
		}
		i := slices.IndexFunc(groups, func(g *checkGroup) bool { return g.repo == e.Source && g.ref == e.Ref })
		if i < 0 {
			groups = append(groups, &checkGroup{repo: e.Source, ref: e.Ref})
			i = len(groups) - 1
		}
		groups[i].items = append(groups[i].items, checkItem{entry: e, folder: folder})
	}
	for _, g := range groups {
		run.checkGroup(g)
	}

	report := CheckReport{Targets: []TargetCheck{}}
	for _, e := range lock.Entries {
		report.Targets = append(report.Targets, run.results[domain.LockID(e.Name)])
	}
	for _, cl := range clones {
		tgt, err := s.cloneTarget(cl.Root)
		switch {
		case err != nil:
			report.Targets = append(report.Targets, TargetCheck{Target: domain.GitID(filepath.ToSlash(cl.Root)), Outcome: CheckNotChecked, Err: err})
		case hasOp && op.Target == tgt:
			report.Targets = append(report.Targets, TargetCheck{Target: tgt, Outcome: CheckNotChecked, Err: ErrInterrupted})
		default:
			report.Targets = append(report.Targets, run.checkClone(tgt, cl))
		}
	}
	report.NewCandidates = run.created
	st.Results = recordResults(st.Results, report.Targets, run.now)
	st.Checks.LastRun = run.now
	st.Checks.LastFailed = report.Failed()
	if !st.Checks.LastFailed {
		st.Checks.LastSuccess = run.now
	}
	if err := s.d.State.Save(st); err != nil {
		return CheckReport{}, fmt.Errorf("save state: %w", err)
	}
	s.saveHeads(run.heads)
	return report, nil
}

// checkGroup resolves the head of one repository and checks its entries.
func (r *checkRun) checkGroup(g *checkGroup) {
	fail := func(err error) {
		for _, it := range g.items {
			tgt := domain.LockID(it.entry.Name)
			r.results[tgt] = TargetCheck{Target: tgt, Outcome: CheckFailed, Err: err}
		}
	}
	gh := r.s.d.GitHub
	branch := g.ref
	if branch == "" {
		var err error
		if branch, err = gh.DefaultBranch(r.ctx, g.repo); err != nil {
			fail(fmt.Errorf("default branch of %s: %w", g.repo, err))
			return
		}
	}
	commit, err := gh.ResolveCommit(r.ctx, g.repo, branch)
	if err != nil {
		fail(fmt.Errorf("resolve %s@%s: %w", g.repo, branch, err))
		return
	}

	// The tree is fetched only when the head moved since the last check or
	// when a candidate must be staged (docs/design.md section 7, step 1).
	var root *treehash.Tree
	getRoot := func() (treehash.Tree, error) {
		if root == nil {
			tree, err := gh.Tree(r.ctx, g.repo, commit, "")
			if err != nil {
				return treehash.Tree{}, fmt.Errorf("tree of %s@%s: %w", g.repo, commit, err)
			}
			root = &tree
		}
		return *root, nil
	}
	recorded := r.heads[g.key()]
	next := headRecord{Commit: commit, Trees: map[string]string{}}
	complete := true
	for _, it := range g.items {
		tgt := domain.LockID(it.entry.Name)
		dir := skillDir(it.entry.SkillPath)
		upstream, ok := "", false
		if recorded.Commit == commit {
			upstream, ok = recorded.Trees[dir]
		}
		if !ok {
			tree, err := getRoot()
			if err != nil {
				r.results[tgt] = TargetCheck{Target: tgt, Outcome: CheckFailed, Err: err}
				complete = false
				continue
			}
			upstream = subtreeSHA(tree, dir)
		}
		next.Trees[dir] = upstream
		if upstream == "" {
			// The folder is gone upstream: nothing can be updated, and
			// failing here would fail every later check too (L27).
			r.results[tgt] = TargetCheck{Target: tgt, Outcome: CheckUpToDate, RemovedUpstream: true}
			continue
		}
		r.results[tgt] = r.evaluate(it, g.repo, commit, dir, upstream, getRoot)
	}
	if complete {
		r.heads[g.key()] = next
	}
}

// evaluate compares one entry with upstream and stages a candidate when
// needed.
func (r *checkRun) evaluate(it checkItem, repo, commit, dir, upstream string, getRoot func() (treehash.Tree, error)) TargetCheck {
	e := it.entry
	tgt := domain.LockID(e.Name)
	if upstream == e.SkillFolderHash {
		r.supersede(tgt)
		return TargetCheck{Target: tgt, Outcome: CheckUpToDate}
	}
	if slices.ContainsFunc(r.st.Skips, func(sk domain.Skip) bool { return sk.Target == tgt && sk.ToSHA == upstream }) {
		r.supersede(tgt)
		return TargetCheck{Target: tgt, Outcome: CheckSkipped}
	}
	installed, err := r.s.d.HashDir(it.folder)
	if err != nil {
		return TargetCheck{Target: tgt, Outcome: CheckFailed, Err: fmt.Errorf("hash the installed folder: %w", err)}
	}
	for _, c := range r.st.Candidates {
		if c.Target != tgt || !isOpen(c) || c.ToSHA != upstream || c.FromSHA != e.SkillFolderHash ||
			c.InstalledSHAAtStage != installed.SHA {
			continue
		}
		if c.State == domain.CandidateBlocked {
			return TargetCheck{Target: tgt, Outcome: CheckBlocked, CandidateID: c.ID}
		}
		if meta, err := r.s.stagedMeta(c); err != nil || meta.Ref != e.Ref {
			continue
		}
		if staged, err := r.s.d.HashDir(c.StagedPath); err == nil && staged.SHA == c.ToSHA {
			return TargetCheck{Target: tgt, Outcome: CheckUpdateAvailable, CandidateID: c.ID}
		}
	}

	// An open candidate is superseded only once its replacement is staged,
	// so a check that fails here keeps it pending.
	root, err := getRoot()
	if err != nil {
		return TargetCheck{Target: tgt, Outcome: CheckFailed, Err: err}
	}
	c := domain.UpdateCandidate{
		ID:                  r.newCandidateID(tgt, upstream, installed.SHA),
		Target:              tgt,
		Skills:              []string{tgt},
		FromSHA:             e.SkillFolderHash,
		InstalledSHAAtStage: installed.SHA,
		ToCommit:            commit,
		ToSHA:               upstream,
		FirstSeenAt:         r.now,
		State:               domain.CandidatePending,
	}
	st := stager{s: r.s, ctx: r.ctx, repo: repo, folder: it.folder, installed: installed}
	if err := st.stage(&c, subtree(root, dir)); err != nil {
		return TargetCheck{Target: tgt, Outcome: CheckFailed, Err: err}
	}
	if c.State == domain.CandidatePending {
		if err := r.s.saveStagedMeta(c, stagedMeta{Ref: e.Ref}); err != nil {
			r.s.discardStaging(c)
			return TargetCheck{Target: tgt, Outcome: CheckFailed, Err: err}
		}
	}
	r.supersede(tgt)
	r.st.Candidates = append(r.st.Candidates, c)
	r.created = append(r.created, c.ID)
	if c.State == domain.CandidateBlocked {
		return TargetCheck{Target: tgt, Outcome: CheckBlocked, CandidateID: c.ID}
	}
	return TargetCheck{Target: tgt, Outcome: CheckUpdateAvailable, CandidateID: c.ID}
}

// supersede marks every open candidate of tgt as superseded
// and removes its staging.
func (r *checkRun) supersede(tgt string) {
	for i := range r.st.Candidates {
		c := &r.st.Candidates[i]
		if c.Target != tgt || !isOpen(*c) {
			continue
		}
		c.State = domain.CandidateSuperseded
		r.s.discardStaging(*c)
	}
}

// discardStaging removes a candidate's staging folder. Staging is a cache
// that is rebuilt on demand, so a failed removal only leaves disk space used.
func (s *Service) discardStaging(c domain.UpdateCandidate) {
	if c.StagedPath == "" || filepath.Dir(c.StagedPath) != s.d.Paths.StagingDir() {
		return
	}
	_ = s.d.Files.RemoveTree(c.StagedPath)       //nolint:errcheck // see the doc comment.
	_ = s.d.Files.RemoveTree(s.stagedRefFile(c)) //nolint:errcheck // see the doc comment.
}

// stagedRefFile records what the staging saw, next to the staged folder: the
// lock entry's ref, or the clone's head, branch, upstream and origin.
// UpdateCandidate has no field for them, and the staged folder itself must
// hash to to_sha.
func (s *Service) stagedRefFile(c domain.UpdateCandidate) string {
	return filepath.Join(s.d.Paths.StagingDir(), c.ID+".json")
}

// errNoStagedRef reports a candidate whose staging record is missing or
// unreadable.
var errNoStagedRef = errors.New("the staging record is missing")

// stagedMeta is the staging record of a candidate.
type stagedMeta struct {
	// Ref is the lock entry's ref at staging.
	Ref string `json:"ref"`
	// Clone is the clone at staging; zero for a lock skill.
	Clone cloneMeta `json:"clone,omitzero"`
}

func (s *Service) saveStagedMeta(c domain.UpdateCandidate, meta stagedMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := s.d.Files.WriteFileAtomic(s.stagedRefFile(c), data, 0o644); err != nil {
		return fmt.Errorf("record the staging: %w", err)
	}
	return nil
}

// stagedMeta returns what the candidate's staging recorded.
func (s *Service) stagedMeta(c domain.UpdateCandidate) (stagedMeta, error) {
	data, err := s.d.Files.ReadFile(s.stagedRefFile(c))
	if errors.Is(err, fs.ErrNotExist) {
		return stagedMeta{}, errNoStagedRef
	}
	if err != nil {
		return stagedMeta{}, fmt.Errorf("read the staging record: %w", err)
	}
	var meta stagedMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return stagedMeta{}, fmt.Errorf("%w: %w", errNoStagedRef, err)
	}
	return meta, nil
}

// newCandidateID returns an id no candidate in the state has.
func (r *checkRun) newCandidateID(tgt, toSHA, installedSHA string) string {
	for n := 0; ; n++ {
		sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%s\x00%s\x00%d", tgt, toSHA, installedSHA, r.now.Format(time.RFC3339Nano), n))
		id := hex.EncodeToString(sum[:8])
		if !slices.ContainsFunc(r.st.Candidates, func(c domain.UpdateCandidate) bool { return c.ID == id }) {
			return id
		}
	}
}

// storeFolder resolves the folder lock entry name installs to: the store
// folder of that name, or the sibling a store alias links to. A folder that
// is missing, parked in OFF/, a link resolving anywhere but a sibling, or a
// git clone is not updatable (docs/design.md section 2).
func (s *Service) storeFolder(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("%q: %w: not a folder name", name, ErrNotUpdatable)
	}
	entries, err := s.d.Dirs.Scan(s.d.Paths.StoreDir())
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s: %w: the store does not exist", name, ErrNotUpdatable)
	}
	if err != nil {
		return "", fmt.Errorf("scan the store: %w", err)
	}
	i := slices.IndexFunc(entries, func(e ports.DirEntry) bool { return e.Name == name })
	if i < 0 {
		return "", fmt.Errorf("%s: %w: no folder in the store", name, ErrNotUpdatable)
	}
	entry := entries[i]
	switch entry.Kind {
	case ports.EntryDir:
	case ports.EntrySymlink:
		j := slices.IndexFunc(entries, func(e ports.DirEntry) bool {
			return e.Kind == ports.EntryDir && e.ResolvedPath != "" && e.ResolvedPath == entry.ResolvedPath
		})
		if j < 0 || entries[j].Name == offDirName {
			return "", fmt.Errorf("%s: %w: the link resolves outside the store", name, ErrNotUpdatable)
		}
		entry = entries[j]
	default:
		return "", fmt.Errorf("%s: %w: not a folder", name, ErrNotUpdatable)
	}
	if entry.Name == offDirName {
		return "", fmt.Errorf("%s: %w: not a skill folder", name, ErrNotUpdatable)
	}
	hasGit, err := s.d.Files.Exists(filepath.Join(entry.Path, ".git"))
	if err != nil {
		return "", fmt.Errorf("look for .git in %s: %w", entry.Path, err)
	}
	if entry.HasGit || hasGit {
		return "", fmt.Errorf("%s: %w: a git clone", name, ErrNotUpdatable)
	}
	return entry.Path, nil
}

// offDirName is where skills are parked by hand (docs/design.md section 2).
const offDirName = "OFF"

// validRepo accepts owner/name.
func validRepo(source string) bool {
	owner, name, ok := strings.Cut(source, "/")
	return ok && owner != "" && name != "" && !strings.ContainsAny(name, "/\\ ") && validName(owner) && validName(name)
}

// skillDir is the skill folder of a lock skillPath, which points at
// SKILL.md; "" is the repository root.
func skillDir(skillPath string) string {
	dir := path.Dir(strings.TrimPrefix(skillPath, "/"))
	if dir == "." {
		return ""
	}
	return dir
}

// subtreeSHA returns the SHA of the folder dir in root, "" when it is not a
// folder there.
func subtreeSHA(root treehash.Tree, dir string) string {
	if dir == "" {
		return root.SHA
	}
	for _, n := range root.Nodes {
		if n.Path == dir && n.IsTree() {
			return n.SHA
		}
	}
	return ""
}

// subtree returns the nodes below dir, with paths relative to it.
func subtree(root treehash.Tree, dir string) []treehash.Node {
	out := []treehash.Node{}
	for _, n := range root.Nodes {
		rel := n.Path
		if dir != "" {
			var ok bool
			if rel, ok = strings.CutPrefix(n.Path, dir+"/"); !ok {
				continue
			}
		}
		out = append(out, n)
		out[len(out)-1].Path = rel
	}
	return out
}

// headsCache remembers, per repository and ref, the head commit of the last
// check and the tree SHA of every skill folder at it, so an unchanged head
// needs no tree (docs/design.md section 7, step 1). It lives in the cache
// directory: losing it only costs one tree request.
type headsCache map[string]headRecord

type headRecord struct {
	Commit string            `json:"commit"`
	Trees  map[string]string `json:"trees"`
}

func (s *Service) headsFile() string { return filepath.Join(s.d.Paths.CacheDir(), "heads.json") }

// loadHeads reads the cache; a missing or unreadable one is empty.
func (s *Service) loadHeads() headsCache {
	heads := headsCache{}
	data, err := s.d.Files.ReadFile(s.headsFile())
	if err != nil || json.Unmarshal(data, &heads) != nil {
		return headsCache{}
	}
	return heads
}

// saveHeads writes the cache. A failed write only costs a tree request on
// the next check.
func (s *Service) saveHeads(heads headsCache) {
	data, err := json.MarshalIndent(heads, "", "  ")
	if err != nil {
		return
	}
	_ = s.d.Files.WriteFileAtomic(s.headsFile(), append(data, '\n'), 0o644) //nolint:errcheck // see the doc comment.
}
