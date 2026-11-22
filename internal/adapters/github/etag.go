package github

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"skillhub/internal/shared/paths"
)

type cachedEntry struct {
	ETag string `json:"etag"`
	Body []byte `json:"body"`
}

type etagCache struct {
	mu      sync.Mutex
	path    string
	guard   paths.Guard
	loaded  bool
	entries map[string]cachedEntry
}

func newETagCache(path string, guard paths.Guard) *etagCache {
	return &etagCache{
		path:    path,
		guard:   guard,
		entries: map[string]cachedEntry{},
	}
}

// isETagCachedEndpoint reports whether endpoint is mutable and should use
// conditional ETag caching. Immutable objects addressed by SHA (trees, blobs)
// are never stored in etags.json.
func isETagCachedEndpoint(endpoint string) bool {
	if !strings.HasPrefix(endpoint, "repos/") {
		return false
	}
	rest := strings.TrimPrefix(endpoint, "repos/")
	parts := strings.Split(rest, "/")
	if len(parts) == 2 {
		return true
	}
	return len(parts) >= 4 && parts[2] == "commits"
}

func (c *etagCache) get(key string) (cachedEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	e, ok := c.entries[key]
	return e, ok
}

func (c *etagCache) set(key, etag string, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	c.entries[key] = cachedEntry{
		ETag: etag,
		Body: append([]byte(nil), body...),
	}
	return c.saveLocked()
}

func (c *etagCache) loadLocked() {
	if c.loaded {
		return
	}
	c.loaded = true
	if err := c.guard.Check(c.path); err != nil {
		return
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return
		}
		return
	}
	var decoded map[string]cachedEntry
	if err := json.Unmarshal(data, &decoded); err != nil || decoded == nil {
		return
	}
	c.entries = decoded
}

func (c *etagCache) saveLocked() error {
	if err := c.guard.Check(c.path); err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(c.entries)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "etags-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
