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
// Concurrency: All public methods are safe for concurrent use. Stats
// counters use sync/atomic to avoid data races exposed by -race detector
// (Bug fix from B3 concurrent testing — see cache/delta/store_test.go).
package kv

import (
	"context"
	"sync"
	"sync/atomic"
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
	mu   sync.RWMutex
	data map[string]entry
	// Stats counters are accessed via atomic ops; never read the struct
	// field directly except through Stats() which does atomic loads.
	lookups     int64
	hits        int64
	misses      int64
	stores      int64
	invalidates int64
	expirations int64
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
	atomic.AddInt64(&s.lookups, 1)

	e, ok := s.data[key]
	if !ok {
		atomic.AddInt64(&s.misses, 1)
		return nil, false, nil
	}
	if time.Now().After(e.expiresAt) {
		atomic.AddInt64(&s.expirations, 1)
		atomic.AddInt64(&s.misses, 1)
		return nil, false, nil
	}
	atomic.AddInt64(&s.hits, 1)
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
	atomic.AddInt64(&s.stores, 1)

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
	atomic.AddInt64(&s.invalidates, 1)
	delete(s.data, key)
	return nil
}

// Stats returns a snapshot of cumulative counters.
func (s *InMemoryStore) Stats() Stats {
	return Stats{
		Lookups:     atomic.LoadInt64(&s.lookups),
		Hits:        atomic.LoadInt64(&s.hits),
		Misses:      atomic.LoadInt64(&s.misses),
		Stores:      atomic.LoadInt64(&s.stores),
		Invalidates: atomic.LoadInt64(&s.invalidates),
		Expirations: atomic.LoadInt64(&s.expirations),
	}
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
