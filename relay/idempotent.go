package relay

import (
	"sync"
	"time"
)

// IdempotentCache (Track C C5, 2026-06-18) is a small in-memory
// LRU that tracks pending request IDs for the current session.
// When a client re-sends the same request (network retry, double-
// click), the handler can check this cache and return 202 +
// X-Gw-Pending immediately rather than re-executing the full
// routing + vendor path.
//
// Design:
//   - Key: sessionID + ":" + requestID
//   - Value: timestamp (for TTL-based expiry)
//   - Cap: 100 entries (env-gated, default 100)
//   - TTL: 5 minutes (enough for a slow vendor to finish)
//   - Thread-safe via mutex
//
// Why in-memory (not Redis): the hit rate is low (most requests
// are unique), and the cost of a miss is just a normal request.
// Redis I/O for every request would add ~1ms latency to the hot
// path for a rare benefit. The cache is per-pod; a miss on pod B
// after a hit on pod A just means the request runs twice — the
// pending store deduplicates at the Redis level anyway.
type IdempotentCache struct {
	mu    sync.Mutex
	items map[string]time.Time
	order []string
	cap   int
	ttl   time.Duration
}

// NewIdempotentCache creates a cache with the given capacity
// and TTL. cap <= 0 defaults to 100; ttl <= 0 defaults to 5 min.
func NewIdempotentCache(cap int, ttl time.Duration) *IdempotentCache {
	if cap <= 0 {
		cap = 100
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &IdempotentCache{
		items: make(map[string]time.Time),
		cap:   cap,
		ttl:   ttl,
	}
}

// CheckAndMark returns true if the (sessionID, requestID) pair
// is already in the cache and has not expired. Regardless of
// the result, the pair is inserted/refreshed — so a second
// call within the TTL window will see a hit.
//
// The caller should:
//   - On hit: return 202 + X-Gw-Pending immediately
//   - On miss: proceed with normal request processing
func (c *IdempotentCache) CheckAndMark(sessionID, requestID string) bool {
	if c == nil || sessionID == "" || requestID == "" {
		return false
	}
	key := sessionID + ":" + requestID
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if ts, ok := c.items[key]; ok && now.Sub(ts) < c.ttl {
		// Refresh the timestamp so the TTL window resets.
		c.items[key] = now
		return true
	}
	// New entry: insert and track in order for LRU eviction.
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	c.items[key] = now
	c.evictIfNeededLocked(now)
	return false
}

// evictIfNeededLocked removes expired entries and enforces the
// capacity cap. Caller must hold c.mu.
func (c *IdempotentCache) evictIfNeededLocked(now time.Time) {
	// First pass: remove expired entries.
	for k, ts := range c.items {
		if now.Sub(ts) >= c.ttl {
			delete(c.items, k)
		}
	}
	// Second pass: enforce cap by removing oldest.
	for len(c.items) > c.cap && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}
}

// Len returns the number of entries. For observability.
func (c *IdempotentCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
