package compression

import (
	"context"
	"strings"
	"testing"
)

func TestSessionCache_LargeBodyNotRetainedInL1(t *testing.T) {
	c := NewSessionCache(nil, nil)
	
	ctx := context.Background()
	tenantID := "tenant1"
	sessionID := "session1"
	
	// Small body should be cached
	smallBody := []byte(strings.Repeat("x", 512<<10)) // 512KB
	smallState := &SessionState{
		LastOutboundHash: "hash1",
		MsgCount:         5,
	}
	
	if err := c.Set(ctx, tenantID, sessionID, smallState, smallBody); err != nil {
		t.Fatal(err)
	}
	
	// Verify small body is in L1
	st, body, err := c.getLocal(ctx, tenantID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || len(body) != len(smallBody) {
		t.Errorf("Small body not cached: state=%v, bodyLen=%d", st != nil, len(body))
	}
	
	// Large body (>1MB) should NOT be fully cached
	largeBody := []byte(strings.Repeat("y", 2<<20)) // 2MB
	largeState := &SessionState{
		LastOutboundHash: "hash2",
		MsgCount:         10,
	}
	
	if err := c.Set(ctx, tenantID, sessionID, largeState, largeBody); err != nil {
		t.Fatal(err)
	}
	
	// Check L1 entry directly
	c.mu.Lock()
	entry, ok := c.l1[l1Key(tenantID, sessionID)]
	c.mu.Unlock()
	
	if !ok {
		t.Fatal("Large body entry not in L1 map")
	}
	
	// The body field should be empty (not retained in memory)
	if len(entry.body) != 0 {
		t.Errorf("Large body retained in L1: got %d bytes, want 0", len(entry.body))
	}
	
	// State metadata should still be cached
	if entry.state == nil || entry.state.MsgCount != 10 {
		t.Error("Large body state metadata not cached")
	}
	
	// entry.bytes accounts for original size for eviction/budget purposes,
	// but actual memory footprint is just metadata
	t.Logf("L1 entry: bytes=%d (accounting), body retained=%d (actual memory)", 
		entry.bytes, len(entry.body))
	
	if !entry.oversized {
		t.Error("Large body entry should be marked oversized")
	}
}

func TestSessionCache_L1ByteBudgetWithMixedSizes(t *testing.T) {
	c := NewSessionCache(nil, nil)
	
	ctx := context.Background()
	
	// Add several small entries
	for i := 0; i < 10; i++ {
		sessionID := string(rune('a' + i))
		body := []byte(strings.Repeat("x", 100<<10)) // 100KB each
		state := &SessionState{MsgCount: i}
		
		if err := c.Set(ctx, "t1", sessionID, state, body); err != nil {
			t.Fatal(err)
		}
	}
	
	// Add one large entry (>1MB)
	largeBody := []byte(strings.Repeat("y", 5<<20)) // 5MB
	largeState := &SessionState{MsgCount: 100}
	
	if err := c.Set(ctx, "t1", "large", largeState, largeBody); err != nil {
		t.Fatal(err)
	}
	
	c.mu.Lock()
	totalBytes := c.curBytes
	entryCount := len(c.l1)
	largeEntry := c.l1[l1Key("t1", "large")]
	c.mu.Unlock()
	
	// Verify large body not actually stored
	if largeEntry != nil && len(largeEntry.body) != 0 {
		t.Errorf("Large body retained in memory: %d bytes", len(largeEntry.body))
	}
	
	// Byte budget still accounts for original size for eviction purposes
	// Total: (10 * 100KB) + 5MB ≈ 6MB
	t.Logf("L1: %d entries, %d bytes (accounting for original sizes)", entryCount, totalBytes)
	
	// The key optimization: actual memory footprint is much smaller
	// since the 5MB body is not stored
	if largeEntry != nil && largeEntry.oversized {
		t.Logf("Large entry marked oversized: body not retained in memory")
	}
}

func TestSessionCache_LargeBodyFallbackToLowerTiers(t *testing.T) {
	// This test documents that when L1 has empty body for a large request,
	// getLocal returns empty body, and caller must fall back to other sources
	// (L2 Redis, cold DB, or large archive file).
	
	c := NewSessionCache(nil, nil)
	
	ctx := context.Background()
	tenantID := "t1"
	sessionID := "s1"
	
	largeBody := []byte(strings.Repeat("x", 3<<20)) // 3MB
	state := &SessionState{MsgCount: 5}
	
	if err := c.Set(ctx, tenantID, sessionID, state, largeBody); err != nil {
		t.Fatal(err)
	}
	
	// getLocal should return state but empty body
	st, body, err := c.getLocal(ctx, tenantID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatal("State not found")
	}
	if len(body) != 0 {
		t.Errorf("Expected empty body from L1 for large request, got %d bytes", len(body))
	}
	
	// Caller should detect empty body and fall back to:
	// - Redis L2 (if configured)
	// - Cold DB (if configured)
	// - Large archive file (via context.Value(largeArchiveKey{}))
	//
	// This test just verifies L1 behavior; full fallback chain is integration-tested elsewhere.
}
