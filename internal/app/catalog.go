package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"skillhub/internal/manage/install"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// The skills.sh catalog (S14): search, and a "popular" view built from fixed
// topic searches, since the official ranking needs a Vercel token (L76).
// Searching only reads: installing a result is still skills install, with
// its review and its plan.

const (
	// DefaultCatalogLimit is the number of results a search asks for when
	// the caller gives none.
	DefaultCatalogLimit = 20
	// maxCatalogLimit is the most results skills.sh returns for one search.
	maxCatalogLimit = 100
	// popularPerTopic is how many results each popular topic asks for.
	popularPerTopic = 50
	// catalogTTL is how long a cached search answers without asking again.
	catalogTTL = time.Hour
	// catalogKeep is how long an expired search stays in the cache, as the
	// fallback when skills.sh cannot be reached.
	catalogKeep = 7 * 24 * time.Hour
)

// PopularTopics are the searches the popular view combines, asked one after
// another to respect skills.sh's rate limit.
var PopularTopics = []string{"react", "testing", "python", "docs", "design", "git", "typescript", "security", "data", "agent"}

var (
	// ErrCatalogQueryTooShort reports a search shorter than two characters,
	// which skills.sh refuses.
	ErrCatalogQueryTooShort = errors.New("catalog query is shorter than two characters")
	// ErrCatalogCurlMissing reports a curl that could not be started.
	ErrCatalogCurlMissing = errors.New("curl is not available")
	// ErrCatalogOffline reports skills.sh not reachable: no network, an
	// unknown host or a timeout.
	ErrCatalogOffline = errors.New("skills.sh is not reachable")
	// ErrCatalogRateLimited reports skills.sh's rate limit (HTTP 429).
	ErrCatalogRateLimited = errors.New("skills.sh rate limit reached")
	// ErrCatalogHTTP reports any other HTTP error from skills.sh.
	ErrCatalogHTTP = errors.New("skills.sh answered with an error")
	// ErrCatalogBadResponse reports an answer that is not the expected JSON.
	ErrCatalogBadResponse = errors.New("skills.sh sent an unexpected answer")
	// ErrCatalogFailed reports any other failed search.
	ErrCatalogFailed = errors.New("skills.sh search failed")
)

// CatalogError is a failed catalog search. It matches its Kind, one of the
// ErrCatalog errors, with errors.Is.
type CatalogError struct {
	Kind error
	// Status is the HTTP status skills.sh sent, or 0.
	Status int
	Err    error
}

func (e *CatalogError) Error() string {
	msg := e.Kind.Error()
	if e.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *CatalogError) Is(target error) bool { return target == e.Kind }

func (e *CatalogError) Unwrap() error { return e.Err }

// catalogFailure is what a catalog adapter's error reports: a stable reason
// code and the HTTP status. The adapter is never imported here.
type catalogFailure interface {
	error
	CatalogReason() string
	HTTPStatus() int
}

var catalogReasons = map[string]error{
	"curl_missing": ErrCatalogCurlMissing,
	"offline":      ErrCatalogOffline,
	"rate_limited": ErrCatalogRateLimited,
	"http":         ErrCatalogHTTP,
	"bad_response": ErrCatalogBadResponse,
	"bad_query":    ErrCatalogQueryTooShort,
}

// classifyCatalog turns an adapter error into a *CatalogError; a canceled
// context stays as it is.
func classifyCatalog(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	ce := &CatalogError{Kind: ErrCatalogFailed, Err: err}
	if f, ok := errors.AsType[catalogFailure](err); ok {
		if kind, known := catalogReasons[f.CatalogReason()]; known {
			ce.Kind = kind
		}
		ce.Status = f.HTTPStatus()
	}
	return ce
}

// CatalogEntry is one skill of skills.sh, with what this machine knows of it.
type CatalogEntry struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	SkillID  string `json:"skill_id"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
	// Installable is true when skills install accepts InstallSource: Source
	// is a GitHub owner/repo, or a website host, whose
	// /.well-known/agent-skills/ index the install reads (L84).
	Installable bool `json:"installable"`
	// InstallSource is what skills install takes: Source@SkillID for
	// GitHub, https://Source@SkillID for a website.
	InstallSource string `json:"install_source"`
	// Installed is true when SkillID matches an installed skill from the
	// same origin (a lock entry with the same repository/site and skill, or
	// a git clone whose remote points to the same repository).
	Installed bool `json:"installed"`
	// InstalledSource is the normalized origin of the installed match when
	// Installed is true.
	InstalledSource string `json:"installed_source,omitempty"`
	// PossibleDuplicate is true when SkillID matches an installed skill (or
	// one of its aliases) from another or unknown origin.
	PossibleDuplicate bool `json:"possible_duplicate,omitempty"`
	// DuplicateOf is the installed skill name that matched when
	// PossibleDuplicate is true.
	DuplicateOf string `json:"duplicate_of,omitempty"`
	// DuplicateSource is the normalized origin of DuplicateOf, or empty
	// when the installed skill has no recorded origin.
	DuplicateSource string `json:"duplicate_source,omitempty"`
}

// CatalogOptions tune a catalog call.
type CatalogOptions struct {
	// Refresh asks skills.sh even when the cache is fresh.
	Refresh bool
}

// CatalogResult is what a catalog call returns.
type CatalogResult struct {
	Entries []CatalogEntry
	// Stale is set when skills.sh failed and the entries, or some of them,
	// come from an expired cache; it is the failure, a *CatalogError.
	Stale error
	// CachedAt is when the oldest cached answer used was fetched; zero when
	// every answer came from skills.sh now.
	CachedAt time.Time
	// CacheErr is set when the answer could not be written to the cache;
	// the entries are still good.
	CacheErr error
}

// HasCatalog reports whether the optional skills.sh port is configured.
func (a *App) HasCatalog() bool { return a != nil && a.d.Catalog != nil }

// SearchCatalog searches skills.sh. limit 0 or less is DefaultCatalogLimit,
// and at most 100. Each query is cached for an hour; when skills.sh fails
// and the cache has an older answer, that answer comes back with Stale set.
func (a *App) SearchCatalog(ctx context.Context, query string, limit int, opts CatalogOptions) (CatalogResult, error) {
	if a.d.Catalog == nil {
		return CatalogResult{}, fmt.Errorf("%w: Catalog", ErrMissingDependency)
	}
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) < 2 {
		return CatalogResult{}, &CatalogError{Kind: ErrCatalogQueryTooShort}
	}
	if limit <= 0 {
		limit = DefaultCatalogLimit
	}
	limit = min(limit, maxCatalogLimit)
	var res CatalogResult
	skills, err := a.catalogSearch(ctx, query, limit, opts, &res)
	if err != nil {
		return CatalogResult{}, err
	}
	res.Entries, err = a.catalogEntries(ctx, skills)
	if err != nil {
		return CatalogResult{}, err
	}
	return res, nil
}

// PopularCatalog searches every PopularTopics topic, one after another,
// merges the results by id and sorts them by installs, most first. The
// first topic that fails with no cached answer stops the series.
func (a *App) PopularCatalog(ctx context.Context, opts CatalogOptions) (CatalogResult, error) {
	if a.d.Catalog == nil {
		return CatalogResult{}, fmt.Errorf("%w: Catalog", ErrMissingDependency)
	}
	var res CatalogResult
	byID := map[string]ports.CatalogSkill{}
	var order []string
	for _, topic := range PopularTopics {
		skills, err := a.catalogSearch(ctx, topic, popularPerTopic, opts, &res)
		if err != nil {
			return CatalogResult{}, err
		}
		for _, s := range skills {
			prev, seen := byID[s.ID]
			if !seen {
				order = append(order, s.ID)
			}
			if !seen || s.Installs > prev.Installs {
				byID[s.ID] = s
			}
		}
	}
	merged := make([]ports.CatalogSkill, 0, len(order))
	for _, id := range order {
		merged = append(merged, byID[id])
	}
	slices.SortStableFunc(merged, func(x, y ports.CatalogSkill) int {
		return cmp.Or(cmp.Compare(y.Installs, x.Installs), cmp.Compare(x.ID, y.ID))
	})
	entries, err := a.catalogEntries(ctx, merged)
	if err != nil {
		return CatalogResult{}, err
	}
	res.Entries = entries
	return res, nil
}

// catalogSearch answers one query from the cache or from skills.sh, and
// records in res whether a cached or stale answer was used.
func (a *App) catalogSearch(ctx context.Context, query string, limit int, opts CatalogOptions, res *CatalogResult) ([]ports.CatalogSkill, error) {
	now := a.d.Clock.Now()
	key := fmt.Sprintf("%d %s", limit, query)
	cache := a.loadCatalogCache()
	cached, hasCached := cache.Queries[key]
	useCached := func() []ports.CatalogSkill {
		if res.CachedAt.IsZero() || cached.FetchedAt.Before(res.CachedAt) {
			res.CachedAt = cached.FetchedAt
		}
		return cached.skills()
	}
	if hasCached && !opts.Refresh && !now.Before(cached.FetchedAt) && now.Sub(cached.FetchedAt) < catalogTTL {
		return useCached(), nil
	}
	skills, err := a.d.Catalog.Search(ctx, query, limit)
	if err != nil {
		err = classifyCatalog(err)
		if _, ok := errors.AsType[*CatalogError](err); ok && hasCached {
			if res.Stale == nil {
				res.Stale = err
			}
			return useCached(), nil
		}
		return nil, err
	}
	if err := a.storeCatalogQuery(key, now, skills); err != nil && res.CacheErr == nil {
		res.CacheErr = err
	}
	return skills, nil
}

// catalogEntries adds what this machine knows to each skill.
func (a *App) catalogEntries(ctx context.Context, skills []ports.CatalogSkill) ([]CatalogEntry, error) {
	installed, err := a.installedCatalog(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CatalogEntry, 0, len(skills))
	for _, s := range skills {
		e := CatalogEntry{
			ID: s.ID, Source: s.Source, SkillID: s.SkillID, Name: s.Name, Installs: s.Installs,
			InstallSource: s.Source + "@" + s.SkillID,
		}
		applyInstalledMatch(&e, installed)
		site := ""
		if isHost(s.Source) {
			// A website: installed from its /.well-known/agent-skills/ (L84).
			site = "https://" + s.Source
			e.InstallSource = site + "@" + s.SkillID
		}
		src, err := install.Parse(e.InstallSource)
		e.Installable = err == nil && src.Site == site && src.Skill == s.SkillID &&
			src.Ref == "" && src.Subpath == "" && src.Tree == "" &&
			(site != "" || src.Repo == s.Source)
		out = append(out, e)
	}
	return out, nil
}

// isHost reports whether a skills.sh source is a website host, such as
// mintlify.com, rather than a GitHub owner/repo: one name with a dot and no
// slash.
func isHost(source string) bool {
	return !strings.Contains(source, "/") && strings.Contains(source, ".")
}

type installedSkill struct {
	displayName string
	origin      string
	names       []string
}

// installedCatalog collects every installed skill with its normalized origin
// and matching names (name, aliases, and lock skillName).
func (a *App) installedCatalog(ctx context.Context) ([]installedSkill, error) {
	skills, err := a.scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]installedSkill, 0, len(skills))
	for _, s := range skills {
		origin := ""
		switch s.Origin.Kind {
		case domain.OriginLock:
			if s.Origin.Lock != nil {
				origin = normalizeCatalogOrigin(s.Origin.Lock.Source)
			}
		case domain.OriginGitClone:
			if s.Origin.GitClone != nil {
				origin = normalizeCatalogOrigin(s.Origin.GitClone.Remote)
			}
		}
		names := appendUniqueLower(nil, s.Name)
		for _, alias := range s.Aliases {
			names = appendUniqueLower(names, alias)
		}
		out = append(out, installedSkill{
			displayName: s.Name,
			origin:      origin,
			names:       names,
		})
	}
	lock, err := a.d.Lock.Read()
	if err != nil {
		return nil, fmt.Errorf("read the skills lock: %w", err)
	}
	for _, e := range lock.Entries {
		origin := normalizeCatalogOrigin(e.Source)
		if origin == "" {
			origin = normalizeCatalogOrigin(e.SourceURL)
		}
		origName := lockEntrySkillName(e.Raw)
		matched := false
		for i := range out {
			if out[i].origin == origin && strings.EqualFold(out[i].displayName, e.Name) {
				out[i].names = appendUniqueLower(out[i].names, origName)
				matched = true
				break
			}
		}
		if !matched {
			names := appendUniqueLower(nil, e.Name)
			names = appendUniqueLower(names, origName)
			out = append(out, installedSkill{
				displayName: e.Name,
				origin:      origin,
				names:       names,
			})
		}
	}
	return out, nil
}

func appendUniqueLower(dst []string, s string) []string {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" || slices.Contains(dst, v) {
		return dst
	}
	return append(dst, v)
}

func lockEntrySkillName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		SkillName string `json:"skillName"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	return obj.SkillName
}

func applyInstalledMatch(e *CatalogEntry, installed []installedSkill) {
	skillName := strings.ToLower(strings.TrimSpace(e.SkillID))
	if skillName == "" {
		skillName = strings.ToLower(strings.TrimSpace(e.Name))
	}
	if skillName == "" {
		return
	}
	catalogOrigin := normalizeCatalogOrigin(e.Source)
	if catalogOrigin != "" {
		for _, inst := range installed {
			if inst.origin == catalogOrigin && slices.Contains(inst.names, skillName) {
				e.Installed = true
				e.InstalledSource = inst.origin
				return
			}
		}
	}
	for _, inst := range installed {
		if slices.Contains(inst.names, skillName) {
			e.PossibleDuplicate = true
			e.DuplicateOf = inst.displayName
			e.DuplicateSource = inst.origin
			return
		}
	}
}

// normalizeCatalogOrigin normalizes a repository or remote URL (owner/repo,
// https://github.com/owner/repo.git, git@github.com:owner/repo.git, etc.) to
// a canonical lowercase identifier so equivalent origins compare equal.
func normalizeCatalogOrigin(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		if before, after, ok := strings.Cut(s, ":"); ok && !strings.Contains(before, "/") {
			host := before
			if at := strings.LastIndex(host, "@"); at >= 0 {
				host = host[at+1:]
			}
			return normalizeHostAndPath(host, after)
		}
	}
	if _, rest, ok := strings.Cut(s, "://"); ok {
		host, path, _ := strings.Cut(rest, "/")
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		host, _, _ = strings.Cut(host, ":")
		return normalizeHostAndPath(host, path)
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "github.com/") || strings.HasPrefix(lower, "www.github.com/") {
		host, path, _ := strings.Cut(s, "/")
		return normalizeHostAndPath(host, path)
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.ToLower(s)
}

func normalizeHostAndPath(host, path string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "www.")
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "github.com" {
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return strings.ToLower(parts[0] + "/" + parts[1])
		}
		return strings.ToLower(path)
	}
	if path == "" {
		return host
	}
	return host + "/" + strings.ToLower(path)
}

// catalogCacheVersion is the format of catalog.json.
const catalogCacheVersion = 1

type catalogCache struct {
	Version int                          `json:"version"`
	Queries map[string]catalogCacheQuery `json:"queries"`
}

type catalogCacheQuery struct {
	FetchedAt time.Time          `json:"fetched_at"`
	Skills    []catalogCacheItem `json:"skills"`
}

type catalogCacheItem struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	SkillID  string `json:"skill_id"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
}

func (q catalogCacheQuery) skills() []ports.CatalogSkill {
	out := make([]ports.CatalogSkill, 0, len(q.Skills))
	for _, s := range q.Skills {
		out = append(out, ports.CatalogSkill(s))
	}
	return out
}

// loadCatalogCache reads the cache. A missing, unreadable or corrupt cache
// is an empty one: it is only a cache, and the next answer rewrites it.
func (a *App) loadCatalogCache() catalogCache {
	empty := catalogCache{Version: catalogCacheVersion, Queries: map[string]catalogCacheQuery{}}
	data, err := a.d.Files.ReadFile(a.d.Paths.CatalogCacheFile())
	if err != nil {
		return empty
	}
	var c catalogCache
	if json.Unmarshal(data, &c) != nil || c.Version != catalogCacheVersion || c.Queries == nil {
		return empty
	}
	return c
}

// storeCatalogQuery writes one answer to the cache through Files, and drops
// the answers older than catalogKeep.
func (a *App) storeCatalogQuery(key string, now time.Time, skills []ports.CatalogSkill) error {
	c := a.loadCatalogCache()
	for k, q := range c.Queries {
		if now.Sub(q.FetchedAt) > catalogKeep {
			delete(c.Queries, k)
		}
	}
	items := make([]catalogCacheItem, 0, len(skills))
	for _, s := range skills {
		items = append(items, catalogCacheItem(s))
	}
	c.Queries[key] = catalogCacheQuery{FetchedAt: now.UTC(), Skills: items}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the catalog cache: %w", err)
	}
	if err := a.d.Files.WriteFileAtomic(a.d.Paths.CatalogCacheFile(), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write the catalog cache: %w", err)
	}
	return nil
}
