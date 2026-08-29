package dispatch

import (
	"context"
	"sync"
)

// InMemoryJournalStore is a simple in-memory implementation of
// AuthorizedJournalConsumer for testing and demonstration purposes.
// Production implementations should use durable storage (e.g., via
// requestjourney.QueryService integration or a dedicated journal store).
//
// This implementation satisfies ADR 2026-08-28 §Decision point 4:
// authorization checking with not-found-shaped errors for cross-tenant access.
type InMemoryJournalStore struct {
	mu        sync.RWMutex
	snapshots map[string]JournalSnapshot // key: tenantID + ":" + requestID
}

// NewInMemoryJournalStore creates an empty in-memory journal store.
func NewInMemoryJournalStore() *InMemoryJournalStore {
	return &InMemoryJournalStore{
		snapshots: make(map[string]JournalSnapshot),
	}
}

// Store persists a snapshot for later authorized retrieval. This is a test
// helper; production paths would integrate with requestjourney.Recorder or
// a dedicated durable store.
func (s *InMemoryJournalStore) Store(snap JournalSnapshot) {
	if s == nil || snap.TenantID == "" || snap.RequestID == "" {
		return
	}
	key := snap.TenantID + ":" + snap.RequestID
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[key] = snap
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
func (s *InMemoryJournalStore) ConsumeSnapshot(ctx context.Context, callerTenant, requestID string) (JournalSnapshot, error) {
	if s == nil {
		return JournalSnapshot{}, ErrJournalNotFound
	}
	if callerTenant == "" || requestID == "" {
		return JournalSnapshot{}, ErrJournalNotFound
	}

	key := callerTenant + ":" + requestID
	s.mu.RLock()
	snap, found := s.snapshots[key]
	s.mu.RUnlock()

	if !found {
		return JournalSnapshot{}, ErrJournalNotFound
	}

	if snap.TenantID != callerTenant {
		return JournalSnapshot{}, ErrJournalNotFound
	}

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

// Clear removes all stored snapshots (test helper).
func (s *InMemoryJournalStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = make(map[string]JournalSnapshot)
}
