// Package kv — in-memory implementation of Store (B2 default backend).
//
// This is the default Store used until a Redis-backed implementation lands.
// It is a simple map[string]entry protected by sync.RWMutex. Lookup is the
// hot path (read-locked); Store/Invalidate take the write lock.
//
// Memory bound: entries expire by TTL and are removed lazily (on Lookup) or
// eagerly (via SweepExpired). For long-running processes with very high
// key churn, call SweepExpired periodically from a bg worker.
//
// Concurrency: All public methods are safe for concurrent use.
package kv

import (
	"context"
	"sync"
	"time"
)

// entry is one cached key → payload pair.
type entry struct {
	payload   []byte
	expiresAt time.Time
}

// InMemoryStore is a map[string]entry implementing Store.
// Construction is via NewInMemoryStore; the zero value is NOT usable
// (data map is nil).
type InMemoryStore struct {
	mu    sync.RWMutex
	data  map[string]entry
	stats Stats
}

// NewInMemoryStore creates an empty in-memory Store. Callers typically
// construct it once at startup and inject it via dependency injection.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		data: make(map[string]entry),
	}
}

// Lookup implements Store.Lookup with TTL-based eviction.
// Expired entries are treated as misses (counter incremented) but are NOT
// evicted here — call SweepExpired for eager cleanup, or rely on next miss.
func (s *InMemoryStore) Lookup(ctx context.Context, key string) ([]byte, bool, error) {
	if key == "" {
		return nil, false, ErrEmptyKey
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.stats.Lookups++

	e, ok := s.data[key]
	if !ok {
		s.stats.Misses++
		return nil, false, nil
	}
	if time.Now().After(e.expiresAt) {
		s.stats.Expirations++
		s.stats.Misses++
		return nil, false, nil
	}
	s.stats.Hits++
	// Defensive copy: callers can mutate the returned slice without
	// corrupting storage.
	out := make([]byte, len(e.payload))
	copy(out, e.payload)
	return out, true, nil
}

// Store implements Store.Store. Overwrites are idempotent.
func (s *InMemoryStore) Store(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if key == "" {
		return ErrEmptyKey
	}
	if len(payload) > MaxPayloadBytes {
		return ErrTooLarge
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Stores++

	// Defensive copy: caller may mutate the input after Store returns.
	stored := make([]byte, len(payload))
	copy(stored, payload)
	s.data[key] = entry{
		payload:   stored,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

// Invalidate removes the entry for key. Missing key is a no-op.
func (s *InMemoryStore) Invalidate(ctx context.Context, key string) error {
	if key == "" {
		return ErrEmptyKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Invalidates++
	delete(s.data, key)
	return nil
}

// Stats returns a snapshot of cumulative counters.
func (s *InMemoryStore) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

// Size returns the number of stored entries (for telemetry / debugging).
// Walks the full map; do not call on the hot path.
func (s *InMemoryStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// SweepExpired evicts entries past their TTL. Returns the number removed.
// Call periodically from a background worker to bound memory in
// long-running processes. The map is iterated under the write lock; for
// very large maps, prefer calling it from a single dedicated goroutine.
func (s *InMemoryStore) SweepExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	removed := 0
	for k, e := range s.data {
		if now.After(e.expiresAt) {
			delete(s.data, k)
			removed++
		}
	}
	return removed
}
