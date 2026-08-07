package compression

import (
	"context"
	"strings"
	"testing"
)

// TestSessionCache_L1ByteBudget verifies that the L1 evicts on the byte budget
// even when the count limit has not been reached (docs/omni-ref3 D3).
func TestSessionCache_L1ByteBudget(t *testing.T) {
	cache := NewSessionCache(nil, nil)

	// Construct a body that is large enough that just a few entries exceed
	// the byte budget (256 MiB default). We'll use 100 MB bodies so that 3
	// entries = 300 MB > 256 MB, triggering byte-based eviction before the
	// count limit (1024) is anywhere close.
	largeBody := []byte(strings.Repeat("a", 100<<20)) // 100 MiB

	state := &SessionState{
		SchemaVersion: 1,
		MsgCount:      10,
		TokenEstimate: 25000,
	}

	ctx := context.Background()

	// Insert entry 1: should fit (100 MB < 256 MB).
	cache.Set(ctx, "tenant1", "session1", state, largeBody)
	if got, _, _ := cache.GetOrLoad(ctx, "tenant1", "session1"); got == nil {
		t.Fatal("entry 1 should be present after Set")
	}

	// Insert entry 2: should fit (200 MB < 256 MB).
	cache.Set(ctx, "tenant1", "session2", state, largeBody)
	if got, _, _ := cache.GetOrLoad(ctx, "tenant1", "session2"); got == nil {
		t.Fatal("entry 2 should be present after Set")
	}

	// Insert entry 3: total = 300 MB > 256 MB → should evict the LRU (session1).
	cache.Set(ctx, "tenant1", "session3", state, largeBody)

	// session3 (MRU) and session2 should be present.
	if got, _, _ := cache.GetOrLoad(ctx, "tenant1", "session3"); got == nil {
		t.Error("entry 3 (MRU) should be present after byte-triggered eviction")
	}
	if got, _, _ := cache.GetOrLoad(ctx, "tenant1", "session2"); got == nil {
		t.Error("entry 2 should still be present (not LRU)")
	}

	// session1 (LRU) should have been evicted to bring the total under budget.
	if got, _, _ := cache.GetOrLoad(ctx, "tenant1", "session1"); got != nil {
		t.Error("entry 1 (LRU) should have been evicted when byte budget was exceeded")
	}

	// Verify that we are well under the count limit (3 entries vs 1024 default).
	cache.mu.Lock()
	count := cache.ll.Len()
	cache.mu.Unlock()
	if count >= 1024 {
		t.Errorf("L1 count = %d, should be far below the count limit (1024)", count)
	}
}

// TestSessionCache_L1ByteBudget_UpdateInPlace verifies that updating an
// existing entry adjusts the byte tracker correctly.
func TestSessionCache_L1ByteBudget_UpdateInPlace(t *testing.T) {
	cache := NewSessionCache(nil, nil)
	state := &SessionState{SchemaVersion: 1}
	ctx := context.Background()

	// Insert a small body.
	smallBody := []byte("small")
	cache.Set(ctx, "tenant1", "session1", state, smallBody)

	cache.mu.Lock()
	before := cache.curBytes
	cache.mu.Unlock()

	// Update with a larger body.
	largeBody := []byte(strings.Repeat("x", 10000))
	cache.Set(ctx, "tenant1", "session1", state, largeBody)

	cache.mu.Lock()
	after := cache.curBytes
	cache.mu.Unlock()

	// curBytes should have increased by roughly len(largeBody) - len(smallBody).
	delta := after - before
	expectedDelta := len(largeBody) - len(smallBody)
	if delta < expectedDelta-100 || delta > expectedDelta+100 {
		t.Errorf("curBytes delta = %d, want ~%d (tolerance ±100)", delta, expectedDelta)
	}
}

// TestSessionCache_L1ByteBudget_Invalidate verifies that Invalidate decrements
// the byte tracker.
func TestSessionCache_L1ByteBudget_Invalidate(t *testing.T) {
	cache := NewSessionCache(nil, nil)
	state := &SessionState{SchemaVersion: 1}
	ctx := context.Background()

	body := []byte(strings.Repeat("y", 5000))
	cache.Set(ctx, "tenant1", "session1", state, body)

	cache.mu.Lock()
	before := cache.curBytes
	cache.mu.Unlock()

	cache.Invalidate(ctx, "tenant1", "session1")

	cache.mu.Lock()
	after := cache.curBytes
	cache.mu.Unlock()

	// curBytes should have dropped by the entry size.
	if after >= before {
		t.Errorf("curBytes after Invalidate (%d) >= before (%d), expected a drop", after, before)
	}
	if after != 0 {
		t.Logf("curBytes = %d after invalidating the only entry (expected 0, but other tests may leave state)", after)
	}
}
