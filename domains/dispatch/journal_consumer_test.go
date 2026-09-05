package dispatch

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestInMemoryJournalStoreStoresDetachedEntries(t *testing.T) {
	store := NewInMemoryJournalStore(0) // 0 means use default capacity
	entries := []JournalEntry{{Seq: 1, Action: NextActionCompleted}}
	store.Store(JournalSnapshot{TenantID: "tenant-a", RequestID: "request-1", Entries: entries})
	entries[0].Seq = 99

	snapshot, err := store.ConsumeSnapshot(context.Background(), JournalSnapshotQuery{
		CallerTenantID: "tenant-a",
		TargetTenantID: "tenant-a",
		RequestID:      "request-1",
	})
	if err != nil {
		t.Fatalf("ConsumeSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Seq != 1 {
		t.Fatalf("stored entries were not detached: %+v", snapshot.Entries)
	}
}

func TestInMemoryJournalStore_CapacityDefault(t *testing.T) {
	// Test that capacity <= 0 defaults to 10000
	tests := []struct {
		name     string
		capacity int
		expected int
	}{
		{"zero capacity", 0, 10000},
		{"negative capacity", -1, 10000},
		{"explicit capacity", 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewInMemoryJournalStore(tt.capacity)
			if store.capacity != tt.expected {
				t.Errorf("expected capacity %d, got %d", tt.expected, store.capacity)
			}
		})
	}
}

func TestInMemoryJournalStore_CapacityLimit(t *testing.T) {
	capacity := 3
	store := NewInMemoryJournalStore(capacity)

	// Store capacity+1 snapshots
	for i := 0; i < capacity+1; i++ {
		snap := JournalSnapshot{
			TenantID:  "tenant1",
			RequestID: fmt.Sprintf("req-%d", i),
			Entries:   []JournalEntry{{Seq: i, Action: NextActionCompleted}},
		}
		store.Store(snap)
	}

	// Should only have 'capacity' snapshots
	if store.Size() != capacity {
		t.Errorf("expected size %d, got %d", capacity, store.Size())
	}

	// The oldest (req-0) should have been evicted
	query := JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-0",
	}
	_, err := store.ConsumeSnapshot(context.Background(), query)
	if err != ErrJournalNotFound {
		t.Errorf("expected req-0 to be evicted, got error: %v", err)
	}

	// The newest entries should still be present
	for i := 1; i <= capacity; i++ {
		query := JournalSnapshotQuery{
			CallerTenantID: "tenant1",
			TargetTenantID: "tenant1",
			RequestID:      fmt.Sprintf("req-%d", i),
		}
		_, err := store.ConsumeSnapshot(context.Background(), query)
		if err != nil {
			t.Errorf("expected req-%d to exist, got error: %v", i, err)
		}
	}
}

func TestInMemoryJournalStore_LRUEviction(t *testing.T) {
	capacity := 3
	store := NewInMemoryJournalStore(capacity)

	// Store 3 snapshots (filling capacity)
	for i := 0; i < capacity; i++ {
		snap := JournalSnapshot{
			TenantID:  "tenant1",
			RequestID: fmt.Sprintf("req-%d", i),
			Entries:   []JournalEntry{{Seq: i, Action: NextActionCompleted}},
		}
		store.Store(snap)
	}

	// Access req-0 to make it recently used
	query := JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-0",
	}
	_, err := store.ConsumeSnapshot(context.Background(), query)
	if err != nil {
		t.Fatalf("failed to access req-0: %v", err)
	}

	// Now store a new snapshot, which should evict req-1 (least recently used)
	snap := JournalSnapshot{
		TenantID:  "tenant1",
		RequestID: "req-3",
		Entries:   []JournalEntry{{Seq: 3, Action: NextActionCompleted}},
	}
	store.Store(snap)

	// req-1 should be evicted (it was the oldest unused)
	query = JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-1",
	}
	_, err = store.ConsumeSnapshot(context.Background(), query)
	if err != ErrJournalNotFound {
		t.Errorf("expected req-1 to be evicted, got error: %v", err)
	}

	// req-0 should still be present (we accessed it)
	query = JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-0",
	}
	_, err = store.ConsumeSnapshot(context.Background(), query)
	if err != nil {
		t.Errorf("expected req-0 to exist, got error: %v", err)
	}

	// req-2 and req-3 should be present
	for _, reqID := range []string{"req-2", "req-3"} {
		query = JournalSnapshotQuery{
			CallerTenantID: "tenant1",
			TargetTenantID: "tenant1",
			RequestID:      reqID,
		}
		_, err = store.ConsumeSnapshot(context.Background(), query)
		if err != nil {
			t.Errorf("expected %s to exist, got error: %v", reqID, err)
		}
	}
}

func TestInMemoryJournalStore_UpdateMovesToFront(t *testing.T) {
	capacity := 3
	store := NewInMemoryJournalStore(capacity)

	// Store 3 snapshots
	for i := 0; i < capacity; i++ {
		snap := JournalSnapshot{
			TenantID:        "tenant1",
			RequestID:       fmt.Sprintf("req-%d", i),
			SnapshotVersion: 1,
			Entries:         []JournalEntry{{Seq: i, Action: NextActionCompleted}},
		}
		store.Store(snap)
	}

	// Update req-0 (should move to front)
	snap := JournalSnapshot{
		TenantID:        "tenant1",
		RequestID:       "req-0",
		SnapshotVersion: 2,
		Entries:         []JournalEntry{{Seq: 0, Action: NextActionCompleted}},
	}
	store.Store(snap)

	// Store a new snapshot, which should evict req-1 (now the oldest)
	snap = JournalSnapshot{
		TenantID:  "tenant1",
		RequestID: "req-3",
		Entries:   []JournalEntry{{Seq: 3, Action: NextActionCompleted}},
	}
	store.Store(snap)

	// req-1 should be evicted
	query := JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-1",
	}
	_, err := store.ConsumeSnapshot(context.Background(), query)
	if err != ErrJournalNotFound {
		t.Errorf("expected req-1 to be evicted, got error: %v", err)
	}

	// req-0 should still be present
	query = JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-0",
	}
	result, err := store.ConsumeSnapshot(context.Background(), query)
	if err != nil {
		t.Errorf("expected req-0 to exist, got error: %v", err)
	}
	if result.SnapshotVersion != 2 {
		t.Errorf("expected updated version 2, got %d", result.SnapshotVersion)
	}
}

func TestInMemoryJournalStore_ConcurrentAccess(t *testing.T) {
	capacity := 100
	store := NewInMemoryJournalStore(capacity)
	concurrency := 10
	operationsPerGoroutine := 50

	var wg sync.WaitGroup
	wg.Add(concurrency)

	// Concurrent Store operations
	for i := 0; i < concurrency; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				snap := JournalSnapshot{
					TenantID:  fmt.Sprintf("tenant-%d", workerID),
					RequestID: fmt.Sprintf("req-%d-%d", workerID, j),
					Entries:   []JournalEntry{{Seq: j, Action: NextActionCompleted}},
				}
				store.Store(snap)
			}
		}(i)
	}

	wg.Wait()

	// Verify capacity is respected
	size := store.Size()
	if size > capacity {
		t.Errorf("expected size <= %d, got %d", capacity, size)
	}

	// Concurrent reads and writes
	wg.Add(concurrency * 2)
	for i := 0; i < concurrency; i++ {
		// Readers
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				query := JournalSnapshotQuery{
					CallerTenantID: fmt.Sprintf("tenant-%d", workerID),
					TargetTenantID: fmt.Sprintf("tenant-%d", workerID),
					RequestID:      fmt.Sprintf("req-%d-%d", workerID, j),
				}
				store.ConsumeSnapshot(context.Background(), query)
			}
		}(i)

		// Writers
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				snap := JournalSnapshot{
					TenantID:  fmt.Sprintf("tenant-%d", workerID),
					RequestID: fmt.Sprintf("req-new-%d-%d", workerID, j),
					Entries:   []JournalEntry{{Seq: j, Action: NextActionCompleted}},
				}
				store.Store(snap)
			}
		}(i)
	}

	wg.Wait()

	// Final capacity check
	finalSize := store.Size()
	if finalSize > capacity {
		t.Errorf("expected final size <= %d, got %d", capacity, finalSize)
	}
}

func TestInMemoryJournalStore_CapacityOne(t *testing.T) {
	store := NewInMemoryJournalStore(1)

	// Store first snapshot
	snap1 := JournalSnapshot{
		TenantID:  "tenant1",
		RequestID: "req-1",
		Entries:   []JournalEntry{{Seq: 1, Action: NextActionCompleted}},
	}
	store.Store(snap1)

	if store.Size() != 1 {
		t.Errorf("expected size 1, got %d", store.Size())
	}

	// Store second snapshot (should evict first)
	snap2 := JournalSnapshot{
		TenantID:  "tenant1",
		RequestID: "req-2",
		Entries:   []JournalEntry{{Seq: 2, Action: NextActionCompleted}},
	}
	store.Store(snap2)

	if store.Size() != 1 {
		t.Errorf("expected size 1, got %d", store.Size())
	}

	// req-1 should be evicted
	query := JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-1",
	}
	_, err := store.ConsumeSnapshot(context.Background(), query)
	if err != ErrJournalNotFound {
		t.Errorf("expected req-1 to be evicted, got error: %v", err)
	}

	// req-2 should be present
	query = JournalSnapshotQuery{
		CallerTenantID: "tenant1",
		TargetTenantID: "tenant1",
		RequestID:      "req-2",
	}
	_, err = store.ConsumeSnapshot(context.Background(), query)
	if err != nil {
		t.Errorf("expected req-2 to exist, got error: %v", err)
	}
}

func TestInMemoryJournalStore_CapacityZeroDefaultsBehavior(t *testing.T) {
	store := NewInMemoryJournalStore(0)

	// Should default to 10000 capacity
	if store.capacity != 10000 {
		t.Errorf("expected capacity 10000, got %d", store.capacity)
	}

	// Store 100 snapshots to verify it works
	for i := 0; i < 100; i++ {
		snap := JournalSnapshot{
			TenantID:  "tenant1",
			RequestID: fmt.Sprintf("req-%d", i),
			Entries:   []JournalEntry{{Seq: i, Action: NextActionCompleted}},
		}
		store.Store(snap)
	}

	if store.Size() != 100 {
		t.Errorf("expected size 100, got %d", store.Size())
	}
}

func TestInMemoryJournalStore_LRUOrderAfterMultipleAccesses(t *testing.T) {
	capacity := 5
	store := NewInMemoryJournalStore(capacity)

	// Store 5 snapshots (filling capacity)
	for i := 0; i < capacity; i++ {
		snap := JournalSnapshot{
			TenantID:  "tenant1",
			RequestID: fmt.Sprintf("req-%d", i),
			Entries:   []JournalEntry{{Seq: i, Action: NextActionCompleted}},
		}
		store.Store(snap)
	}

	// Access in specific order: req-2, req-0, req-4
	// This makes req-1 and req-3 the oldest
	for _, reqID := range []string{"req-2", "req-0", "req-4"} {
		query := JournalSnapshotQuery{
			CallerTenantID: "tenant1",
			TargetTenantID: "tenant1",
			RequestID:      reqID,
		}
		_, err := store.ConsumeSnapshot(context.Background(), query)
		if err != nil {
			t.Fatalf("failed to access %s: %v", reqID, err)
		}
	}

	// Store 2 new snapshots, should evict req-1 and req-3 (oldest)
	for i := 5; i < 7; i++ {
		snap := JournalSnapshot{
			TenantID:  "tenant1",
			RequestID: fmt.Sprintf("req-%d", i),
			Entries:   []JournalEntry{{Seq: i, Action: NextActionCompleted}},
		}
		store.Store(snap)
	}

	// req-1 and req-3 should be evicted
	for _, reqID := range []string{"req-1", "req-3"} {
		query := JournalSnapshotQuery{
			CallerTenantID: "tenant1",
			TargetTenantID: "tenant1",
			RequestID:      reqID,
		}
		_, err := store.ConsumeSnapshot(context.Background(), query)
		if err != ErrJournalNotFound {
			t.Errorf("expected %s to be evicted, got error: %v", reqID, err)
		}
	}

	// req-0, req-2, req-4, req-5, req-6 should be present
	for _, reqID := range []string{"req-0", "req-2", "req-4", "req-5", "req-6"} {
		query := JournalSnapshotQuery{
			CallerTenantID: "tenant1",
			TargetTenantID: "tenant1",
			RequestID:      reqID,
		}
		_, err := store.ConsumeSnapshot(context.Background(), query)
		if err != nil {
			t.Errorf("expected %s to exist, got error: %v", reqID, err)
		}
	}
}

func TestInMemoryJournalStore_ClearResetsLRU(t *testing.T) {
	store := NewInMemoryJournalStore(10)

	// Store some snapshots
	for i := 0; i < 5; i++ {
		snap := JournalSnapshot{
			TenantID:  "tenant1",
			RequestID: fmt.Sprintf("req-%d", i),
			Entries:   []JournalEntry{{Seq: i, Action: NextActionCompleted}},
		}
		store.Store(snap)
	}

	if store.Size() != 5 {
		t.Errorf("expected size 5, got %d", store.Size())
	}

	// Clear the store
	store.Clear()

	if store.Size() != 0 {
		t.Errorf("expected size 0 after clear, got %d", store.Size())
	}

	if len(store.lru) != 0 {
		t.Errorf("expected lru length 0 after clear, got %d", len(store.lru))
	}

	// Verify we can store new snapshots after clear
	snap := JournalSnapshot{
		TenantID:  "tenant1",
		RequestID: "req-new",
		Entries:   []JournalEntry{{Seq: 99, Action: NextActionCompleted}},
	}
	store.Store(snap)

	if store.Size() != 1 {
		t.Errorf("expected size 1 after new store, got %d", store.Size())
	}
}
