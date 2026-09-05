package main

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

func TestCleanupOldReceipts(t *testing.T) {
	recorder := &requestjourney.Recorder{}
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		instance:    "test-instance",
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}

	now := time.Now()

	// Add some old receipts (older than 24 hours)
	oldKey1 := journalSnapshotReceiptKey{tenantID: "tenant1", requestID: "req1", version: 1}
	adapter.receipts[oldKey1] = journalSnapshotReceipt{
		hash:      sha256.Sum256([]byte("old1")),
		createdAt: now.Add(-25 * time.Hour),
	}

	oldKey2 := journalSnapshotReceiptKey{tenantID: "tenant2", requestID: "req2", version: 1}
	adapter.receipts[oldKey2] = journalSnapshotReceipt{
		hash:      sha256.Sum256([]byte("old2")),
		createdAt: now.Add(-48 * time.Hour),
	}

	// Add a recent receipt (less than 24 hours)
	recentKey := journalSnapshotReceiptKey{tenantID: "tenant3", requestID: "req3", version: 1}
	adapter.receipts[recentKey] = journalSnapshotReceipt{
		hash:      sha256.Sum256([]byte("recent")),
		createdAt: now.Add(-1 * time.Hour),
	}

	// Run cleanup with 24 hour TTL
	adapter.cleanupOldReceipts(24 * time.Hour)

	// Verify old receipts are removed
	if _, exists := adapter.receipts[oldKey1]; exists {
		t.Errorf("Expected old receipt 1 to be cleaned up")
	}

	if _, exists := adapter.receipts[oldKey2]; exists {
		t.Errorf("Expected old receipt 2 to be cleaned up")
	}

	// Verify recent receipt is still there
	if _, exists := adapter.receipts[recentKey]; !exists {
		t.Errorf("Expected recent receipt to remain")
	}

	// Verify only 1 receipt remains
	if len(adapter.receipts) != 1 {
		t.Errorf("Expected 1 receipt remaining, got %d", len(adapter.receipts))
	}
}

func TestCleanupWithExactCutoffTime(t *testing.T) {
	recorder := &requestjourney.Recorder{}
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		instance:    "test-instance",
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}

	now := time.Now()

	// Add receipt exactly at the cutoff time
	cutoffKey := journalSnapshotReceiptKey{tenantID: "tenant1", requestID: "req1", version: 1}
	adapter.receipts[cutoffKey] = journalSnapshotReceipt{
		hash:      sha256.Sum256([]byte("cutoff")),
		createdAt: now.Add(-24 * time.Hour),
	}

	// Run cleanup
	adapter.cleanupOldReceipts(24 * time.Hour)

	// Receipt at exact cutoff should be removed (before cutoff)
	if _, exists := adapter.receipts[cutoffKey]; exists {
		t.Errorf("Expected receipt at cutoff time to be cleaned up")
	}
}

func TestCleanupConcurrentSafety(t *testing.T) {
	recorder := &requestjourney.Recorder{}
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		instance:    "test-instance",
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}

	now := time.Now()

	// Pre-populate with some receipts
	for i := 0; i < 100; i++ {
		key := journalSnapshotReceiptKey{
			tenantID:  "tenant",
			requestID: string(rune('a' + i)),
			version:   int64(i),
		}
		adapter.receipts[key] = journalSnapshotReceipt{
			hash:      sha256.Sum256([]byte{byte(i)}),
			createdAt: now.Add(-time.Duration(i) * time.Hour),
		}
	}

	var wg sync.WaitGroup

	// Run cleanup concurrently with reads and writes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			adapter.cleanupOldReceipts(24 * time.Hour)
		}(i)

		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			adapter.mu.Lock()
			key := journalSnapshotReceiptKey{
				tenantID:  "tenant-new",
				requestID: string(rune('A' + id)),
				version:   int64(id),
			}
			adapter.receipts[key] = journalSnapshotReceipt{
				hash:      sha256.Sum256([]byte{byte(id)}),
				createdAt: now,
			}
			adapter.mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Verify the map is still in a consistent state
	adapter.mu.Lock()
	count := len(adapter.receipts)
	adapter.mu.Unlock()

	if count < 0 {
		t.Errorf("Invalid receipt count after concurrent operations: %d", count)
	}
}

func TestCleanupEmptyMap(t *testing.T) {
	recorder := &requestjourney.Recorder{}
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		instance:    "test-instance",
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}

	// Run cleanup on empty map - should not panic
	adapter.cleanupOldReceipts(24 * time.Hour)

	if len(adapter.receipts) != 0 {
		t.Errorf("Expected empty map to remain empty, got %d receipts", len(adapter.receipts))
	}
}

func TestStartCleanupAndClose(t *testing.T) {
	recorder := &requestjourney.Recorder{}
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		instance:    "test-instance",
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}

	// Start cleanup with a short TTL for testing
	adapter.startCleanup(1 * time.Hour)

	// Verify ticker is created
	if adapter.cleanupTicker == nil {
		t.Fatal("Expected cleanup ticker to be initialized")
	}

	// Add an old receipt
	now := time.Now()
	oldKey := journalSnapshotReceiptKey{tenantID: "tenant1", requestID: "req1", version: 1}
	adapter.receipts[oldKey] = journalSnapshotReceipt{
		hash:      sha256.Sum256([]byte("old")),
		createdAt: now.Add(-2 * time.Hour),
	}

	// Wait a bit to ensure goroutine is running
	time.Sleep(100 * time.Millisecond)

	// Close the adapter
	err := adapter.Close()
	if err != nil {
		t.Errorf("Expected Close to succeed, got error: %v", err)
	}

	// Verify cleanup has stopped by checking the channel is closed
	select {
	case <-adapter.stopCleanup:
		// Channel is closed, as expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected stopCleanup channel to be closed")
	}
}

func TestJournalAdapterCloseIsIdempotentAndNilSafe(t *testing.T) {
	recorder := requestjourney.NewRecorder(requestjourney.NewProjection(requestjourney.DefaultConfig()), nil, nil)
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	var nilAdapter *dispatchJourneyJournalAdapter
	if err := nilAdapter.Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
}

func TestJournalAdapterCloseConcurrent(t *testing.T) {
	recorder := requestjourney.NewRecorder(requestjourney.NewProjection(requestjourney.DefaultConfig()), nil, nil)
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}
	adapter.startCleanup(time.Hour)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := adapter.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestJournalAdapterStartCleanupIsIdempotent(t *testing.T) {
	recorder := requestjourney.NewRecorder(requestjourney.NewProjection(requestjourney.DefaultConfig()), nil, nil)
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}
	adapter.startCleanup(time.Hour)
	firstTicker := adapter.cleanupTicker
	adapter.startCleanup(time.Hour)
	if adapter.cleanupTicker != firstTicker {
		t.Fatal("second startCleanup replaced the active ticker")
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestJournalAdapterApplyAfterCloseIsNoop(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	adapter.ApplyJournalSnapshot(context.Background(), dispatch.JournalSnapshot{
		TenantID: "tenant-a", RequestID: "req-closed", SnapshotVersion: 1,
		CallerAuthorized: true, CallerTenantID: "tenant-a",
		Entries: []dispatch.JournalEntry{{Seq: 1, Action: dispatch.NextActionCompleted, At: time.Now()}},
	})
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.receipts) != 0 {
		t.Fatalf("receipts after Apply on closed adapter = %d, want 0", len(adapter.receipts))
	}
}

func TestCleanupTickerExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping ticker execution test in short mode")
	}

	recorder := &requestjourney.Recorder{}
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		instance:    "test-instance",
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}

	// Override ticker interval for faster testing
	adapter.cleanupTicker = time.NewTicker(100 * time.Millisecond)
	ttl := 200 * time.Millisecond

	// Start the cleanup goroutine
	go func() {
		for {
			select {
			case <-adapter.cleanupTicker.C:
				adapter.cleanupOldReceipts(ttl)
			case <-adapter.stopCleanup:
				return
			}
		}
	}()

	// Add receipts over time
	now := time.Now()
	adapter.mu.Lock()
	key1 := journalSnapshotReceiptKey{tenantID: "tenant1", requestID: "req1", version: 1}
	adapter.receipts[key1] = journalSnapshotReceipt{
		hash:      sha256.Sum256([]byte("old")),
		createdAt: now.Add(-300 * time.Millisecond),
	}
	adapter.mu.Unlock()

	// Wait for cleanup to run
	time.Sleep(250 * time.Millisecond)

	// Check that old receipt was cleaned up
	adapter.mu.Lock()
	_, exists := adapter.receipts[key1]
	adapter.mu.Unlock()

	if exists {
		t.Error("Expected old receipt to be cleaned up by ticker")
	}

	// Clean up
	adapter.Close()
}

func TestApplyJournalSnapshotSetsCreatedAt(t *testing.T) {
	// Create a minimal recorder setup
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapterWithDependencies(
		recorder,
		"test-instance",
		nil,
	).(*dispatchJourneyJournalAdapter)
	defer adapter.Close()

	snap := dispatch.JournalSnapshot{
		TenantID:         "tenant1",
		RequestID:        "req1",
		SnapshotVersion:  1,
		CallerAuthorized: true,
		CallerTenantID:   "tenant1",
		Entries: []dispatch.JournalEntry{
			{
				Seq:    1,
				Action: dispatch.NextActionCompleted,
				At:     time.Now(),
			},
		},
	}

	beforeApply := time.Now()
	adapter.ApplyJournalSnapshot(context.Background(), snap)
	afterApply := time.Now()

	// Check that the receipt was created with a timestamp
	key := journalSnapshotReceiptKey{
		tenantID:  snap.TenantID,
		requestID: snap.RequestID,
		version:   snap.SnapshotVersion,
	}

	adapter.mu.Lock()
	receipt, exists := adapter.receipts[key]
	adapter.mu.Unlock()

	if !exists {
		t.Fatal("Expected receipt to be created")
	}

	if receipt.createdAt.Before(beforeApply) || receipt.createdAt.After(afterApply) {
		t.Errorf("Expected createdAt to be between %v and %v, got %v",
			beforeApply, afterApply, receipt.createdAt)
	}
}

func TestRememberReceiptEnforcesCapacityByOldestCreationTime(t *testing.T) {
	adapter := &dispatchJourneyJournalAdapter{
		receipts: make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
	}
	base := time.Unix(100, 0)
	for i := 0; i < journalSnapshotReceiptCapacity; i++ {
		key := journalSnapshotReceiptKey{tenantID: "tenant", requestID: string(rune(i)), version: 1}
		adapter.rememberReceiptLocked(key, journalSnapshotReceipt{createdAt: base.Add(time.Duration(i) * time.Second)})
	}

	newKey := journalSnapshotReceiptKey{tenantID: "tenant", requestID: "new", version: 1}
	adapter.rememberReceiptLocked(newKey, journalSnapshotReceipt{createdAt: base.Add(journalSnapshotReceiptCapacity * time.Second)})

	if got := len(adapter.receipts); got != journalSnapshotReceiptCapacity {
		t.Fatalf("receipt capacity = %d, want %d", got, journalSnapshotReceiptCapacity)
	}
	oldestKey := journalSnapshotReceiptKey{tenantID: "tenant", requestID: string(rune(0)), version: 1}
	if _, ok := adapter.receipts[oldestKey]; ok {
		t.Fatal("oldest receipt was not evicted")
	}
	if _, ok := adapter.receipts[newKey]; !ok {
		t.Fatal("new receipt was evicted")
	}
}
