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
	// Installed is true when SkillID is already a skill of the inventory or
	// an entry of the skills lock.
	Installed bool `json:"installed"`
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
	installed, err := a.installedKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CatalogEntry, 0, len(skills))
	for _, s := range skills {
		e := CatalogEntry{
			ID: s.ID, Source: s.Source, SkillID: s.SkillID, Name: s.Name, Installs: s.Installs,
			InstallSource: s.Source + "@" + s.SkillID,
			Installed:     installed[s.SkillID],
		}
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

// installedKeys is every name of the inventory and every key of the lock.
func (a *App) installedKeys(ctx context.Context) (map[string]bool, error) {
	keys := map[string]bool{}
	skills, err := a.scan(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range skills {
		keys[s.Name] = true
		for _, alias := range s.Aliases {
			keys[alias] = true
		}
	}
	lock, err := a.d.Lock.Read()
	if err != nil {
		return nil, fmt.Errorf("read the skills lock: %w", err)
	}
	for _, e := range lock.Entries {
		keys[e.Name] = true
	}
	return keys, nil
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
