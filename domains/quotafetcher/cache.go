package quotafetcher

import (
	"sync"
	"time"
)

// cacheTTL is how long a fetched QuotaInfo is considered fresh. Set inside
// OpenRouter's documented 30-60s window (mirrors OmniRoute's 45s).
const cacheTTL = 45 * time.Second

// cacheGCInterval is how often the background GC sweeps stale entries.
const cacheGCInterval = 5 * time.Minute

// quotaCache holds per-credential fetched quota, keyed by credential_id.
// Each fetcher owns its own cache (OmniRoute model) so a cache miss on one
// vendor doesn't evict another's entries.
//
// Concurrency: all ops under mu. Get is hot-path (preflight per candidate);
// Set runs only on cache miss (~1/credential per 45s). Fail-open by design —
// a stale/missing entry just triggers a refetch.
type quotaCache struct {
	mu      sync.Mutex
	entries map[int64]cacheEntry
	stop    chan struct{}
}

type cacheEntry struct {
	value     *QuotaInfo
	fetchedAt time.Time
}

func newQuotaCache() *quotaCache {
	c := &quotaCache{
		entries: make(map[int64]cacheEntry),
		stop:    make(chan struct{}),
	}
	go c.gc()
	return c
}

// Get returns the cached QuotaInfo if present and within TTL.
func (c *quotaCache) Get(credID int64) (*QuotaInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[credID]
	if !ok {
		return nil, false
	}
	if time.Since(e.fetchedAt) > cacheTTL {
		return nil, false
	}
	return e.value, true
}

// Set stores a fetched QuotaInfo.
func (c *quotaCache) Set(credID int64, qi *QuotaInfo) {
	c.mu.Lock()
	c.entries[credID] = cacheEntry{value: qi, fetchedAt: time.Now()}
	c.mu.Unlock()
}

// Invalidate drops a cached entry — called on 401/403 (revoked/rotated key)
// so a stale "still has quota" entry isn't served.
func (c *quotaCache) Invalidate(credID int64) {
	c.mu.Lock()
	delete(c.entries, credID)
	c.mu.Unlock()
}

// gc periodically drops stale entries to bound memory under heavy credential
// churn. Mirrors OmniRoute's 5-min unref'd cleanup interval.
func (c *quotaCache) gc() {
	t := time.NewTicker(cacheGCInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.sweep()
		}
	}
}

func (c *quotaCache) sweep() {
	cutoff := time.Now().Add(-cacheTTL * 5) // keep up to 5x TTL for revalidation
	c.mu.Lock()
	for k, e := range c.entries {
		if e.fetchedAt.Before(cutoff) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// stop stops the background GC. Safe to call multiple times.
func (c *quotaCache) Stop() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
}
