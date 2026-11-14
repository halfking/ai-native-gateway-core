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
	ctx := context.Background()
	tenantID := "t1"
	sessionID := "s1"
	largeBody := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", (1<<20)+1) + `"}]}`)
	for _, withDB := range []bool{false, true} {
		t.Run(map[bool]string{false: "miss_without_source", true: "reload_from_db"}[withDB], func(t *testing.T) {
			var db *rehydrateDB
			c := NewSessionCache(nil, nil)
			if withDB {
				db = &rehydrateDB{row: &LastOutboundRow{OutboundBody: largeBody, OutboundMsgCount: 1}}
				c.db = db
			}
			if err := c.Set(ctx, tenantID, sessionID, &SessionState{MsgCount: 5}, largeBody); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				st, body, err := c.GetOrLoad(ctx, tenantID, sessionID)
				if err != nil {
					t.Fatal(err)
				}
				if !withDB {
					if st != nil || body != nil {
						t.Fatalf("incomplete L1 pair must be a miss: state=%v, bodyLen=%d", st != nil, len(body))
					}
				} else if st == nil || st.MsgCount != 1 || string(body) != string(largeBody) {
					t.Fatalf("must return complete DB pair: state=%v, bodyLen=%d", st, len(body))
				}
			}
			if withDB && db.calls != 2 {
				t.Fatalf("oversized L1 must reload each time: calls=%d", db.calls)
			}
		})
	}
}
