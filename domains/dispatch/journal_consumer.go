package dispatch

import (
	"context"
	"sync"
)

// JournalSnapshotStore accepts detached terminal snapshots for a diagnostic
// read model. Implementations must not make snapshot persistence part of request
// settlement; the dispatch sink treats this path as best-effort.
type JournalSnapshotStore interface {
	Store(JournalSnapshot)
}

// InMemoryJournalStore is a simple in-memory implementation of
// AuthorizedJournalConsumer for testing and demonstration purposes.
// Production implementations should use durable storage (e.g., via
// requestjourney.QueryService integration or a dedicated journal store).
//
// This implementation satisfies ADR 2026-08-28 §Decision point 4:
// authorization checking with not-found-shaped errors for cross-tenant access.
type journalSnapshotKey struct {
	tenantID  string
	requestID string
}

type InMemoryJournalStore struct {
	mu        sync.RWMutex
	snapshots map[journalSnapshotKey]JournalSnapshot
	capacity  int
	lru       []journalSnapshotKey // most recently used at front
}

// NewInMemoryJournalStore creates an empty, instance-local journal read model.
// It is not a durable history store; production callers must treat entries as ephemeral diagnostics.
// capacity sets the maximum number of snapshots to retain; when capacity is reached,
// the least recently used snapshot is evicted. If capacity <= 0, defaults to 10000.
func NewInMemoryJournalStore(capacity int) *InMemoryJournalStore {
	if capacity <= 0 {
		capacity = 10000
	}
	return &InMemoryJournalStore{
		snapshots: make(map[journalSnapshotKey]JournalSnapshot),
		capacity:  capacity,
		lru:       make([]journalSnapshotKey, 0, capacity),
	}
}

// Store persists a snapshot for later authorized retrieval. This is a test
// helper; production paths would integrate with requestjourney.Recorder or
// a dedicated durable store.
// Implements LRU eviction: when capacity is reached, the least recently used snapshot is removed.
func (s *InMemoryJournalStore) Store(snap JournalSnapshot) {
	if s == nil || snap.TenantID == "" || snap.RequestID == "" {
		return
	}
	key := journalSnapshotKey{tenantID: snap.TenantID, requestID: snap.RequestID}
	snap.Entries = append([]JournalEntry(nil), snap.Entries...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found := s.snapshots[key]; found && existing.SnapshotVersion >= snap.SnapshotVersion {
		return
	}

	// If key already exists, update and move to front
	if _, found := s.snapshots[key]; found {
		s.snapshots[key] = snap
		s.moveToFrontLocked(key)
		return
	}

	// If at capacity, evict least recently used (tail of lru)
	if len(s.snapshots) >= s.capacity {
		if len(s.lru) > 0 {
			oldestKey := s.lru[len(s.lru)-1]
			delete(s.snapshots, oldestKey)
			s.lru = s.lru[:len(s.lru)-1]
		}
	}

	// Add new entry to map and front of lru
	s.snapshots[key] = snap
	s.lru = append([]journalSnapshotKey{key}, s.lru...)
}

// ConsumeSnapshot retrieves a journal snapshot for the specified request,
// verifying that callerTenant matches the snapshot's TenantID.
//
// Returns ErrJournalNotFound when:
//   - The snapshot does not exist
//   - callerTenant does not match the snapshot's TenantID (cross-tenant access)
//   - callerTenant is empty (no authorization context)
//
// This not-found-shaped error prevents existence leaks: an unauthorized caller
// cannot distinguish "does not exist" from "exists but unauthorized" (ADR DP4).
// Accessing a snapshot updates its position in the LRU order.
func (s *InMemoryJournalStore) ConsumeSnapshot(_ context.Context, query JournalSnapshotQuery) (JournalSnapshot, error) {
	if s == nil || query.CallerTenantID == "" || query.TargetTenantID == "" || query.RequestID == "" {
		return JournalSnapshot{}, ErrJournalNotFound
	}
	if !query.Privileged && query.CallerTenantID != query.TargetTenantID {
		return JournalSnapshot{}, ErrJournalNotFound
	}

	key := journalSnapshotKey{tenantID: query.TargetTenantID, requestID: query.RequestID}
	s.mu.Lock()
	snap, found := s.snapshots[key]
	if found {
		s.moveToFrontLocked(key)
	}
	s.mu.Unlock()

	if !found || snap.TenantID != query.TargetTenantID || snap.RequestID != query.RequestID {
		return JournalSnapshot{}, ErrJournalNotFound
	}

	snap.Entries = append([]JournalEntry(nil), snap.Entries...)
	return snap, nil
}

// Count returns the number of stored snapshots (test helper).
func (s *InMemoryJournalStore) Count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshots)
}

// Size returns the number of stored snapshots (alias for Count, used in tests).
func (s *InMemoryJournalStore) Size() int {
	return s.Count()
}

// Clear removes all stored snapshots (test helper).
func (s *InMemoryJournalStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = make(map[journalSnapshotKey]JournalSnapshot)
	s.lru = s.lru[:0]
}

// moveToFrontLocked moves the specified key to the front of the LRU list.
// Caller must hold s.mu write lock.
func (s *InMemoryJournalStore) moveToFrontLocked(key journalSnapshotKey) {
	// Find and remove the key from its current position
	for i, k := range s.lru {
		if k.tenantID == key.tenantID && k.requestID == key.requestID {
			// Remove from current position
			s.lru = append(s.lru[:i], s.lru[i+1:]...)
			break
		}
	}
	// Add to front
	s.lru = append([]journalSnapshotKey{key}, s.lru...)
}
