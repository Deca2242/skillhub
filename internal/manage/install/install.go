package install

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"skillhub/internal/data"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/shared/paths"
)

const (
	// ActionInstall is the plan's Meta.Action of an install.
	ActionInstall = "install"
	// ActionReinstall is the plan's Meta.Action of a reinstall.
	ActionReinstall = "reinstall"
)

// Plans builds engine plans; *engine.Engine satisfies it. A planner never
// calls Apply.
type Plans interface {
	NewBuilder(meta engine.Meta) *engine.Builder
}

// StateReader reads skillhub's persisted state.
type StateReader interface {
	Load() (domain.PersistedState, error)
}

// LockReader reads the npx skills global lock.
type LockReader interface {
	Read() (domain.SkillsLock, error)
}

// Stat tells what is at a path without following a final symlink; it fails
// with fs.ErrNotExist when nothing is there.
type Stat interface {
	Mode(path string) (iofs.FileMode, error)
}

// Clock tells the time the lock entry records.
type Clock interface {
	Now() time.Time
}

// Deps are what the install planner reads.
type Deps struct {
	Paths     paths.Paths
	Harnesses []data.Harness
	Plans     Plans
	State     StateReader
	Lock      LockReader
	Stat      Stat
	Clock     Clock
	// Dirs reads the real path of each link directory (toggle.Resolver);
	// the app passes its SkillDirScanner.
	Dirs toggle.DirLister
}

// Planner plans installs. It only reads.
type Planner struct {
	d Deps
}

// NewPlanner builds a Planner.
func NewPlanner(d Deps) *Planner { return &Planner{d: d} }

// LinkChange is a symlink the plan creates, with its exact content and the
// harnesses whose link directory holds it.
type LinkChange struct {
	Path      string
	Target    string
	Harnesses []string
}

// Preview is an install as the user sees it, as typed data; the texts in
// Spanish belong to the entry points.
type Preview struct {
	PlanID string
	// Key is the store folder and the lock key; Name, the frontmatter name,
	// is always equal to it.
	Key  string
	Name string
	// Source is owner/repo; SourceURL, Commit and SkillPath are as the lock
	// entry records them.
	Source    string
	SourceURL string
	Commit    string
	SkillPath string
	// TreeSHA is the reviewed tree: the staged copy must hash to it, and it
	// is the lock entry's skillFolderHash.
	TreeSHA    string
	StagedPath string
	StorePath  string
	// Targets are the harnesses the plan links the skill in, sorted.
	Targets []string
	Links   []LinkChange
	// LockEntry is the exact entry the plan adds to the lock, or, for a
	// reinstall, puts in place of ReplacedEntry.
	LockEntry []byte
	// LockCreated is true when there is no lock yet and the plan creates it.
	LockCreated bool
	// ReplacedEntry is, for a reinstall, the exact entry the plan replaces
	// in its place with LockEntry; nil for an install.
	ReplacedEntry []byte
	// FollowsDefaultBranch is true for every GitHub install: the entry has
	// no ref, so npx skills update follows the repository's default branch
	// (docs/research/skills-cli-internals.md, fact 17). A website has no
	// branch: false.
	FollowsDefaultBranch bool
	// Site is the website of a well-known install, the entry's
	// sourceBaseUrl; "" for GitHub. Source is then the site's host.
	Site string
}

// Proposal is a plan with its preview. Running it is engine.Apply(Plan).
type Proposal struct {
	Plan    engine.Plan
	Preview Preview
}

var (
	// ErrKeyExists is the error every *KeyExistsError matches.
	ErrKeyExists = errors.New("a skill with that key already exists")
	// ErrNameMismatch reports a frontmatter name that is not its store key
	// (L39): npx would key the lock by one and the folder by the other.
	ErrNameMismatch = errors.New("the skill's name is not its store key")
	// ErrPinnedRef reports a source that pins a ref: an install never
	// creates a ref (CLAUDE.md, lock rules; L32).
	ErrPinnedRef = errors.New("the source pins a ref and an install never writes one")
	// ErrUnknownHarness reports a target that is not in the harness table.
	ErrUnknownHarness = errors.New("unknown harness")
	// ErrNotAdministrable reports a target without a link directory.
	ErrNotAdministrable = errors.New("the harness has no link directory skillhub can manage")
	// ErrMissingDir reports a store or link directory that is not there.
	ErrMissingDir = errors.New("the directory does not exist")
	// ErrPathTaken reports a link path where something already exists.
	ErrPathTaken = errors.New("something already exists at the link path")
	// ErrInvalidReview reports a review that is not what Prepare returns:
	// a staged path outside the staging root or a tree that is not the
	// resolved folder's.
	ErrInvalidReview = errors.New("the review is not a prepared install")
	// ErrStagingChanged reports a staged copy that no longer hashes to the
	// reviewed tree: the approval binds that exact tree.
	ErrStagingChanged = errors.New("the staged copy changed since it was reviewed")
	// ErrNoEntryToReinstall reports a reinstall of a key the lock has no
	// entry for: that is a plain install.
	ErrNoEntryToReinstall = errors.New("the lock has no entry with that key to reinstall")
	// ErrEntryPinsRef reports a reinstall of an entry with a ref: the ref of
	// an active entry is never removed (CLAUDE.md, lock rules; L32).
	ErrEntryPinsRef = errors.New("the lock entry pins a ref, which is never removed")
	// ErrSourceMismatch reports a reinstall from a source other than the
	// one the entry records.
	ErrSourceMismatch = errors.New("the lock entry records another source")
)

// KeyExistsError tells where the key is taken. It matches ErrKeyExists.
type KeyExistsError struct {
	Key string
	// Store and Parked are the paths that exist; empty when they do not.
	Store  string
	Parked string
	// LockEntry is true when the lock has an entry with the key; Managed
	// and ParkedRecord when skillhub's state records it.
	LockEntry    bool
	Managed      bool
	ParkedRecord bool
}

func (e *KeyExistsError) Error() string {
	return fmt.Sprintf("%v: %s (store %q, parked %q, lock entry %t, managed %t, parked record %t)",
		ErrKeyExists, e.Key, e.Store, e.Parked, e.LockEntry, e.Managed, e.ParkedRecord)
}

func (e *KeyExistsError) Unwrap() error { return ErrKeyExists }

// Install plans publishing review (docs/design-v2.md section 7, step 3):
//  1. move the staged copy to StoreDir()/<key>; the step's precondition is
//     the staged tree as it is now, which must be the reviewed TreeSHA, so
//     Apply refuses a copy that changed after planning;
//  2. in each target, the link npx skills writes (toggle.StoreLink); targets
//     that share a link directory share the link;
//  3. add the lock entry npx skills 1.9.0 writes (lockEntry), never a ref.
//
// Its effect is Managed[key] = {key, targets}. It writes nothing: every
// refusal is a typed error and leaves everything as it was. A missing store
// is not a refusal: the engine creates it.
func (p *Planner) Install(review Review, targets []string) (Proposal, error) {
	return p.plan(review, targets, false)
}

// Reinstall plans installing again a skill whose lock entry has no folder,
// the "reinstall" repair of Doctor (docs/design-v2.md section 8, L49): in one
// plan, everything Install plans, except that the entry is replaced in its
// place in the lock (LockReplace, from its exact bytes) instead of added, the
// way npx skills' addSkillToLock replaces an existing entry
// (reinstalledEntry). It
// refuses, with a typed error and writing nothing, a key the lock has no
// entry for (ErrNoEntryToReinstall), an entry with a ref, since a ref is
// never removed (ErrEntryPinsRef), an entry whose source is not the
// reviewed owner/repo (ErrSourceMismatch), and a key that still has a folder
// in the store or the parking zone, or a parked record (ErrKeyExists).
//
// The app runs it as an install whose origin is the entry's: repair's
// Proposal.Reinstall carries Input, the source to give ResolveInstall; the
// app resolves and prepares it exactly as an install (Resolve, then
// Prepare, and the user reviews it), then calls Reinstall with that Review
// and the targets instead of Install, and applies the plan like any other.
func (p *Planner) Reinstall(review Review, targets []string) (Proposal, error) {
	return p.plan(review, targets, true)
}

// plan is Install, or Reinstall when reinstall is set.
func (p *Planner) plan(review Review, targets []string, reinstall bool) (Proposal, error) {
	r := review.Resolved
	key := r.Key
	if err := p.checkReview(review); err != nil {
		return Proposal{}, err
	}
	if reinstall && r.Site != "" {
		// Doctor reinstalls GitHub entries only; a website never replaces one.
		return Proposal{}, fmt.Errorf("%w: a reinstall comes from GitHub, not from %s", ErrSourceMismatch, r.Site)
	}
	links, ids, err := p.links(key, targets)
	if err != nil {
		return Proposal{}, err
	}
	store := filepath.Join(p.d.Paths.StoreDir(), key)
	if err := p.checkStore(); err != nil {
		return Proposal{}, err
	}
	var old *domain.LockEntry
	if reinstall {
		if old, err = p.replaced(r); err != nil {
			return Proposal{}, err
		}
	}
	if err := p.checkFree(key, store, old != nil); err != nil {
		return Proposal{}, err
	}
	gone, err := p.missing(review.StagedPath)
	if err != nil {
		return Proposal{}, err
	}
	if gone {
		return Proposal{}, fmt.Errorf("%w: %s is gone", ErrStagingChanged, review.StagedPath)
	}
	lockCreated, err := p.missing(p.d.Paths.SkillsLockFile())
	if err != nil {
		return Proposal{}, err
	}
	var entry []byte
	if old != nil {
		entry, err = reinstalledEntry(old.Raw, r, review.TreeSHA, p.d.Clock.Now())
	} else {
		entry, err = lockEntry(r, review.TreeSHA, p.d.Clock.Now())
	}
	if err != nil {
		return Proposal{}, err
	}

	action := ActionInstall
	if reinstall {
		action = ActionReinstall
	}
	b := p.d.Plans.NewBuilder(engine.Meta{
		Action: action, Targets: ids, Canonical: store,
		Source: r.SourceURL, ReviewedTreeSHA: review.TreeSHA,
	})
	_ = b.Move(review.StagedPath, store) // Build reports a refused step
	for _, l := range links {
		_ = b.Link(l.Path, l.Target)
	}
	var replaced []byte
	if old != nil {
		replaced = slices.Clone(old.Raw)
		_ = b.LockReplace(old.Name, old.Raw, entry)
	} else {
		_ = b.LockAdd(r.Name, entry)
	}
	if err := b.SetEffect(engine.Effect{SetManaged: []domain.ManagedSkill{{Key: key, Targets: slices.Clone(ids)}}}); err != nil {
		return Proposal{}, err
	}
	plan, err := b.Build()
	if err != nil {
		return Proposal{}, fmt.Errorf("plan %s of %s: %w", action, key, err)
	}
	move := slices.IndexFunc(plan.Steps, func(s engine.Step) bool { return s.Kind == engine.StepMove })
	if got := plan.Steps[move].Pre[0].TreeSHA; got != review.TreeSHA {
		return Proposal{}, fmt.Errorf("%w: %s is %s, the review approved %s", ErrStagingChanged, review.StagedPath, got, review.TreeSHA)
	}
	return Proposal{Plan: plan, Preview: Preview{
		PlanID: plan.ID, Key: key, Name: r.Name,
		Source: r.Repo, SourceURL: r.SourceURL, Commit: r.Commit, SkillPath: r.SkillPath,
		TreeSHA: review.TreeSHA, StagedPath: review.StagedPath, StorePath: store,
		Targets: ids, Links: links, LockEntry: entry, LockCreated: lockCreated,
		ReplacedEntry: replaced, FollowsDefaultBranch: r.Site == "", Site: r.Site,
	}}, nil
}

// replaced is the lock entry a reinstall of r removes: the entry of r's key,
// with no ref and recording r's owner/repo as its source.
func (p *Planner) replaced(r Resolved) (*domain.LockEntry, error) {
	lock, err := p.d.Lock.Read()
	if err != nil {
		return nil, fmt.Errorf("read the skills lock: %w", err)
	}
	i := slices.IndexFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == r.Key })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoEntryToReinstall, r.Key)
	}
	old := lock.Entries[i]
	if old.Ref != "" {
		return nil, fmt.Errorf("%w: %s@%s", ErrEntryPinsRef, r.Key, old.Ref)
	}
	if old.Source != r.Repo {
		return nil, fmt.Errorf("%w: %s records %q, the source is %q", ErrSourceMismatch, r.Key, old.Source, r.Repo)
	}
	return &old, nil
}

// checkReview refuses a review that is not one Prepare returned for a
// skill install can publish: a key that is not a store key, a name that is
// not its key (L39), a pinned ref (L32), a tree that is not the resolved
// folder's, or a staged path other than the one Prepare stages at.
func (p *Planner) checkReview(review Review) error {
	r := review.Resolved
	if r.Key == "" || r.Key != StoreKey(r.Key) || filepath.Base(r.Key) != r.Key {
		return fmt.Errorf("%w: store key %q", ErrInvalidReview, r.Key)
	}
	if r.Name != r.Key {
		return fmt.Errorf("%w: name %q, store key %q", ErrNameMismatch, r.Name, r.Key)
	}
	if r.Ref != "" {
		return fmt.Errorf("%w: %s@%s", ErrPinnedRef, r.Repo, r.Ref)
	}
	if review.TreeSHA == "" || review.TreeSHA != r.FolderSHA {
		return fmt.Errorf("%w: tree %q, resolved folder %q", ErrInvalidReview, review.TreeSHA, r.FolderSHA)
	}
	if want := filepath.Join(StagingRoot(p.d.Paths), r.Key+"-"+review.TreeSHA); review.StagedPath != want {
		return fmt.Errorf("%w: staged at %q, not %q", ErrInvalidReview, review.StagedPath, want)
	}
	return nil
}

// links resolves the targets to the links the plan creates, one per link
// directory, sorted by path, and returns the target ids sorted.
func (p *Planner) links(key string, targets []string) ([]LinkChange, []string, error) {
	ids := slices.Clone(targets)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if ids == nil {
		ids = []string{}
	}
	byPath := map[string]*LinkChange{}
	for _, id := range ids {
		i := slices.IndexFunc(p.d.Harnesses, func(h data.Harness) bool { return h.ID == id })
		if i < 0 {
			return nil, nil, fmt.Errorf("%w: %s", ErrUnknownHarness, id)
		}
		h := p.d.Harnesses[i]
		if h.LinkDir == "" {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotAdministrable, id)
		}
		dir, err := toggle.LinkDir(p.d.Paths, h)
		if err != nil {
			return nil, nil, err
		}
		path := filepath.Join(dir, key)
		if l, ok := byPath[path]; ok {
			l.Harnesses = append(l.Harnesses, id)
			continue
		}
		if err := p.checkDir(dir); err != nil {
			return nil, nil, err
		}
		path, target, err := toggle.StoreLink(p.resolver(), dir, key)
		if err != nil {
			return nil, nil, err
		}
		free, err := p.missing(path)
		if err != nil {
			return nil, nil, err
		}
		if !free {
			return nil, nil, fmt.Errorf("%w: %s", ErrPathTaken, path)
		}
		byPath[path] = &LinkChange{Path: path, Target: target, Harnesses: []string{id}}
	}
	out := []LinkChange{}
	for _, l := range byPath {
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b LinkChange) int { return strings.Compare(a.Path, b.Path) })
	return out, ids, nil
}

// resolver reads the real path of a link directory.
func (p *Planner) resolver() toggle.Resolver {
	return toggle.Resolver{Paths: p.d.Paths, Dirs: p.d.Dirs, Stat: p.d.Stat}
}

// checkFree refuses a key that is taken: a store or parked path, a lock
// entry, or a managed or parked record in skillhub's state. A reinstall
// replaces the lock entry and the managed record, so they do not count.
func (p *Planner) checkFree(key, store string, reinstall bool) error {
	ke := &KeyExistsError{Key: key}
	parked := filepath.Join(p.d.Paths.ParkedDir(), key)
	for path, field := range map[string]*string{store: &ke.Store, parked: &ke.Parked} {
		free, err := p.missing(path)
		if err != nil {
			return err
		}
		if !free {
			*field = path
		}
	}
	lock, err := p.d.Lock.Read()
	if err != nil {
		return fmt.Errorf("read the skills lock: %w", err)
	}
	ke.LockEntry = slices.ContainsFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == key })
	st, err := p.d.State.Load()
	if err != nil {
		return fmt.Errorf("load the state: %w", err)
	}
	_, ke.Managed = st.Managed[key]
	_, ke.ParkedRecord = st.Parked[key]
	if reinstall {
		ke.LockEntry, ke.Managed = false, false
	}
	if ke.Store != "" || ke.Parked != "" || ke.LockEntry || ke.Managed || ke.ParkedRecord {
		return ke
	}
	return nil
}

// missing reports whether nothing is at path, not following a final link.
func (p *Planner) missing(path string) (bool, error) {
	_, err := p.d.Stat.Mode(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("look at %s: %w", path, err)
	}
	return false, nil
}

// checkStore accepts a missing store, which the engine creates as its own
// level (npx skills creates it too), when the directory above it is there;
// a store that is not a directory is refused like any other.
func (p *Planner) checkStore() error {
	store := p.d.Paths.StoreDir()
	missing, err := p.missing(store)
	if err != nil {
		return err
	}
	if missing {
		return p.checkDir(filepath.Dir(store))
	}
	return p.checkDir(store)
}

// checkDir refuses a store or link directory that is not a real directory:
// the move and the links would fail, and the engine creates only its own
// roots and the store, never a harness's link directory.
func (p *Planner) checkDir(dir string) error {
	mode, err := p.d.Stat.Mode(dir)
	if errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrMissingDir, dir)
	}
	if err != nil {
		return fmt.Errorf("look at %s: %w", dir, err)
	}
	if !mode.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrMissingDir, dir)
	}
	return nil
}

// reinstalledEntry is the entry old becomes when r is reinstalled, under the
// lock rules (docs/research/skills-cli-internals.md, rule 2): every key keeps
// its place, installedAt and every key skillhub does not write are kept, and
// source, sourceType, sourceUrl, commit, skillPath, skillFolderHash and
// updatedAt get the values of lockEntry; a key old lacks goes at the end, and
// installedAt only when old has none, as addSkillToLock does
// (existing?.installedAt ?? now, remove.mjs:4496-4500). old never has a ref
// (Planner.replaced), and none is added.
func reinstalledEntry(old []byte, r Resolved, treeSHA string, now time.Time) ([]byte, error) {
	entry, err := orderedjson.ParseValue(old)
	if err != nil || entry.Kind() != orderedjson.KindObject {
		return nil, fmt.Errorf("lock entry %s: not an object: %v", r.Key, err)
	}
	ts := now.UTC().Format(isoTime)
	for _, kv := range [][2]string{
		{"source", r.Repo},
		{"sourceType", "github"},
		{"sourceUrl", r.SourceURL},
		{"commit", r.Commit},
		{"skillPath", r.SkillPath},
		{"skillFolderHash", treeSHA},
	} {
		if err := entry.Set(kv[0], orderedjson.String(kv[1])); err != nil {
			return nil, fmt.Errorf("lock entry %s: %w", kv[0], err)
		}
	}
	if _, ok := entry.Get("installedAt"); !ok {
		if err := entry.Set("installedAt", orderedjson.String(ts)); err != nil {
			return nil, fmt.Errorf("lock entry installedAt: %w", err)
		}
	}
	if err := entry.Set("updatedAt", orderedjson.String(ts)); err != nil {
		return nil, fmt.Errorf("lock entry updatedAt: %w", err)
	}
	return entry.Bytes(), nil
}

// isoTime is Date.prototype.toISOString, which addSkillToLock uses
// (remove.mjs:4494).
const isoTime = "2006-01-02T15:04:05.000Z"

// lockEntry is the entry npx skills 1.9.0 writes for a GitHub install with
// no ref, key by key in its order. recordGlobalLock (remove.mjs:6680-6691)
// passes to addSkillToLock (:4492-4501):
//
//   - source: getLockSource(parsed.url, owner/repo) (:6681, :5437-5445),
//     which is owner/repo for github.com (getOwnerRepo, :564-588);
//   - sourceType: parsed.type, "github" (:6682);
//   - sourceUrl: parsed.url, https://github.com/<owner>/<repo>.git (:6683,
//     parseSource :843, :853, :897-902);
//   - ref: parsed.ref (:6684); JSON.stringify leaves it out when it is
//     undefined, and an install with a ref is refused, so never written;
//   - commit: resolved.commit, written whenever it is set (:6685); for a
//     GitHub source it always is: the commit of the blob download (:6308) or
//     the head of the clone (:6318, :6326). Here it is Resolved.Commit;
//   - skillPath: the folder's SKILL.md relative to the repository root
//     (:6686, getSkillRepoPaths :6430-6437);
//   - skillName: only for a renamed skill (lockedSourceName, :6366-6368),
//     which skillhub never does: left out;
//   - skillFolderHash: getSkillFolderHashFromTree (:6671, :4668-4675), the
//     git tree SHA of the folder, or of the root tree for a skill at the
//     root: the reviewed TreeSHA;
//   - pluginName (:6689) and installMode (:6690, only "copy", :7143): unset
//     for a symlinked skill outside a plugin, so left out;
//   - installedAt and updatedAt: the time now, installedAt kept only for an
//     existing entry, which an install never has (:4496-4500).
//
// A website skill gets the entry of globalWellKnownLockEntry (add.ts:1985-
// 1997) through addSkillToLock (skill-lock.ts:186-200), in its order:
// source (the host, getSourceIdentifier), sourceType "well-known",
// sourceUrl, an empty skillFolderHash (npx hashes no source folder),
// sourceBaseUrl (the URL given to npx skills add), wellKnownDigest, then
// installedAt and updatedAt. No commit, skillPath or ref.
func lockEntry(r Resolved, treeSHA string, now time.Time) ([]byte, error) {
	ts := now.UTC().Format(isoTime)
	obj := orderedjson.NewObject()
	fields := [][2]string{
		{"source", r.Repo},
		{"sourceType", "github"},
		{"sourceUrl", r.SourceURL},
		{"commit", r.Commit},
		{"skillPath", r.SkillPath},
		{"skillFolderHash", treeSHA},
		{"installedAt", ts},
		{"updatedAt", ts},
	}
	if r.Site != "" {
		fields = [][2]string{
			{"source", r.Repo},
			{"sourceType", sourceTypeWellKnown},
			{"sourceUrl", r.SourceURL},
			{"skillFolderHash", ""},
			{"sourceBaseUrl", r.Site},
			{"wellKnownDigest", r.Digest},
			{"installedAt", ts},
			{"updatedAt", ts},
		}
	}
	for _, kv := range fields {
		if err := obj.Set(kv[0], orderedjson.String(kv[1])); err != nil {
			return nil, fmt.Errorf("lock entry %s: %w", kv[0], err)
		}
	}
	return obj.Bytes(), nil
}
