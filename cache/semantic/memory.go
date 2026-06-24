// Package semantic — in-memory implementation of Cache (C2-1).
//
// This is the default Cache used until a Redis-backed implementation lands.
// It supports exact-match (L1) only — no embedding/semantic matching.
// Embedding-based L2 matching is a follow-up task (C2-1 follow-on).
//
// Production notes (NOT v1):
//   - Bounded by an LRU eviction policy (not yet implemented —
//     the C2-2 follow-up). For now, entries expire only by TTL.
//   - Thread-safe via a sync.RWMutex. Lookup is hot, Store is cold.
package semantic

import (
	"sync"
	"time"
)

// entry is one cached prompt → payload pair.
type entry struct {
	payload   []byte
	expiresAt time.Time
}

// InMemoryCache is a per-tenant, per-model map of promptHash → entry.
// It satisfies Cache. All public methods are safe for concurrent use.
type InMemoryCache struct {
	mu sync.RWMutex
	// data[tenantID][model][promptHash] = entry
	data map[string]map[string]map[string]entry
	s    Stats
}

// NewInMemoryCache creates an empty cache. Callers typically construct it
// once at startup and pass it to relay via dependency injection.
func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{
		data: make(map[string]map[string]map[string]entry),
	}
}

// Lookup implements Cache.Lookup with exact-match semantics.
func (c *InMemoryCache) Lookup(ctx Context, tenantID, model, promptHash, _ string) ([]byte, bool, error) {
	if tenantID == "" || model == "" || promptHash == "" {
		return nil, false, ErrInvalidInput
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.s.Lookups++

	models, ok := c.data[tenantID]
	if !ok {
		c.s.Misses++
		return nil, false, nil
	}
	entries, ok := models[model]
	if !ok {
		c.s.Misses++
		return nil, false, nil
	}
	e, ok := entries[promptHash]
	if !ok {
		c.s.Misses++
		return nil, false, nil
	}
	if time.Now().After(e.expiresAt) {
		// expired; treat as miss (don't evict here — caller or TTL sweep)
		c.s.Misses++
		return nil, false, nil
	}
	c.s.ExactHits++
	// Return a copy so callers can't mutate our storage.
	out := make([]byte, len(e.payload))
	copy(out, e.payload)
	return out, true, nil
}

// Store implements Cache.Store.
func (c *InMemoryCache) Store(ctx Context, tenantID, model, promptHash, _ string, payload []byte, ttl time.Duration) error {
	if tenantID == "" || model == "" || promptHash == "" {
		return ErrInvalidInput
	}
	if len(payload) > MaxPayloadBytes {
		return ErrTooLarge
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s.Stores++

	models, ok := c.data[tenantID]
	if !ok {
		models = make(map[string]map[string]entry)
		c.data[tenantID] = models
	}
	entries, ok := models[model]
	if !ok {
		entries = make(map[string]entry)
		models[model] = entries
	}
	// Defensive copy: callers can mutate their slice after Store returns.
	stored := make([]byte, len(payload))
	copy(stored, payload)
	entries[promptHash] = entry{
		payload:   stored,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

// Invalidate removes all entries for (tenantID, model). Returns the count.
func (c *InMemoryCache) Invalidate(ctx Context, tenantID, model string) (int, error) {
	if tenantID == "" || model == "" {
		return 0, ErrInvalidInput
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s.Invalidates++

	models, ok := c.data[tenantID]
	if !ok {
		return 0, nil
	}
	entries, ok := models[model]
	if !ok {
		return 0, nil
	}
	n := len(entries)
	delete(models, model)
	return n, nil
}

// Stats returns a snapshot of cumulative counters.
func (c *InMemoryCache) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.s
}

// Size returns the number of cached entries (for telemetry / debugging).
// It traverses the full map; do not call on the hot path.
func (c *InMemoryCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, models := range c.data {
		for _, entries := range models {
			n += len(entries)
		}
	}
	return n
}

// SweepExpired evicts entries past their TTL. Returns the number removed.
// Call periodically (e.g. from a bg worker) to bound memory in long-running
// processes that see highly variable traffic.
func (c *InMemoryCache) SweepExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	removed := 0
	for _, models := range c.data {
		for model, entries := range models {
			for hash, e := range entries {
				if now.After(e.expiresAt) {
					delete(entries, hash)
					removed++
				}
			}
			if len(entries) == 0 {
				delete(models, model)
			}
		}
	}
	return removed
}
