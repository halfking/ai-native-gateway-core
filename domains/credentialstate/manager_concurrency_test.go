package credentialstate

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestManager_ConcurrentStateUpdate_NoDataRace guards the 2026-07-27 fix for
// C-1 (the credentialstate data race / lost-update on *State).
//
// Before the fix, getFromMemCache returned the shared *State pointer stored
// in the sync.Map; UpdateOnSuccess/UpdateOnFailure mutated it in place
// (state.ConsecutiveFails++) and stored it back. Two concurrent updates on
// the same (credID, model) therefore:
//   1. raced on the shared struct fields (caught by `go test -race`), and
//   2. lost updates (both loaded the same value, both incremented to N+1
//      instead of N+2).
//
// The fix is copy-on-write in getFromMemCache (returns a clone) plus a
// per-key mutex (lockFor) serializing the read-modify-write. This test runs
// N goroutines doing the RMW loop concurrently and asserts the final counter
// equals N exactly (no lost updates). Run under -race to also catch the
// memory race.
func TestManager_ConcurrentStateUpdate_NoDataRace(t *testing.T) {
	m := &Manager{
		memCache:    &sync.Map{},
		memCacheTTL: time.Minute,
	}

	const credID, model = 42, "gpt-test"
	key := m.cacheKey(credID, model)

	// Seed an initial state.
	m.setToMemCache(key, &State{
		CredentialID:     credID,
		Model:            model,
		Available:        true,
		ConsecutiveFails: 0,
	})

	const n = 200
	var wg sync.WaitGroup
	var lost atomic.Int64 // counts increments that found no state (should be 0)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			// Mirror UpdateOnFailure's RMW: take the per-key lock, load the
			// (cloned) state, mutate, store. The lock + COW together must
			// make every increment observable.
			unlock := m.lockFor(key)
			defer unlock()

			s, _ := m.getFromMemCache(key)
			if s == nil {
				lost.Add(1)
				return
			}
			s.ConsecutiveFails++
			m.setToMemCache(key, s)
		}()
	}
	wg.Wait()

	if lost.Load() != 0 {
		t.Fatalf("%d goroutines found no state (entry lost mid-test)", lost.Load())
	}
	final, ok := m.getFromMemCache(key)
	if !ok || final == nil {
		t.Fatalf("final state missing")
	}
	if final.ConsecutiveFails != n {
		t.Errorf("lost-update bug: ConsecutiveFails = %d, want exactly %d (each increment must be preserved by the per-key lock + COW)",
			final.ConsecutiveFails, n)
	}
}

// TestManager_GetStateReturnsClone verifies the copy-on-write contract of
// getFromMemCache: callers must receive a private copy so that mutating the
// returned *State never corrupts the cached entry.
func TestManager_GetStateReturnsClone(t *testing.T) {
	m := &Manager{
		memCache:    &sync.Map{},
		memCacheTTL: time.Minute,
	}
	key := m.cacheKey(7, "claude")
	m.setToMemCache(key, &State{CredentialID: 7, Model: "claude", ConsecutiveFails: 5})

	first, _ := m.getFromMemCache(key)
	if first.ConsecutiveFails != 5 {
		t.Fatalf("first read = %d, want 5", first.ConsecutiveFails)
	}
	// Mutate the returned clone — the cache must be unaffected.
	first.ConsecutiveFails = 999

	second, _ := m.getFromMemCache(key)
	if second.ConsecutiveFails != 5 {
		t.Errorf("COW contract broken: mutating returned *State corrupted the cache; cached ConsecutiveFails = %d, want 5",
			second.ConsecutiveFails)
	}
}

