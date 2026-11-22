package fakes

import (
	"context"
	"slices"
	"sync"
	"time"

	"skillhub/internal/shared/ports"
)

// CatalogCall is one Search the fake catalog received.
type CatalogCall struct {
	Query string
	Limit int
}

// Catalog is an in-memory ports.Catalog. A query it was not given answers
// no skills. It records every call and the most calls that overlapped, so a
// test can tell whether searches ran one after another.
type Catalog struct {
	mu          sync.Mutex
	results     map[string][]ports.CatalogSkill
	errs        map[string]error
	calls       []CatalogCall
	inFlight    int
	maxInFlight int
	delay       time.Duration
}

// NewCatalog builds a Catalog that knows no query.
func NewCatalog() *Catalog {
	return &Catalog{results: map[string][]ports.CatalogSkill{}, errs: map[string]error{}}
}

// Set makes query answer skills, and clears a failure set by Fail.
func (c *Catalog) Set(query string, skills ...ports.CatalogSkill) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[query] = slices.Clone(skills)
	delete(c.errs, query)
}

// SetDelay makes every Search take d, so searches run in parallel would
// overlap and show in MaxInFlight.
func (c *Catalog) SetDelay(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delay = d
}

// Fail makes query fail with err until Set.
func (c *Catalog) Fail(query string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs[query] = err
}

// Search implements ports.Catalog.
func (c *Catalog) Search(ctx context.Context, query string, limit int) ([]ports.CatalogSkill, error) {
	c.mu.Lock()
	c.calls = append(c.calls, CatalogCall{Query: query, Limit: limit})
	c.inFlight++
	c.maxInFlight = max(c.maxInFlight, c.inFlight)
	delay := c.delay
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
	}()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.errs[query]; err != nil {
		return nil, err
	}
	out := slices.Clone(c.results[query])
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Calls returns every Search received, in order.
func (c *Catalog) Calls() []CatalogCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// Queries returns the query of every Search received, in order.
func (c *Catalog) Queries() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.calls))
	for _, call := range c.calls {
		out = append(out, call.Query)
	}
	return out
}

// MaxInFlight is the most searches that ran at the same time.
func (c *Catalog) MaxInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInFlight
}
