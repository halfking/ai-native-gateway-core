// Package compressor - session_cache_lru_test.go (rtk borrowing, 2026-07-06)
//
// Tests proving the L1 tier of SessionCache is a TRUE LRU: hot sessions are
// retained across capacity churn while cold sessions are evicted. These
// properties did NOT hold under the previous first-seen-first-evicted
// heuristic (an active session could be evicted purely because it was
// inserted early, forcing an L2/L3 round-trip on its next turn).

package compression

import (
	"context"
	"strconv"
	"testing"
)

// stateOf builds a minimal SessionState for tests.
func stateOf(tag string) *SessionState {
	return &SessionState{
		SchemaVersion: schemaVersion,
		MsgCount:      1,
		SystemPrompt:  tag,
	}
}

func TestSessionCache_LRU_EvictsLeastRecentlyUsed(t *testing.T) {
	// Create a cache whose L1 we fill exactly to capacity, then touch an
	// "old-but-active" session to make it MRU, then insert one more entry
	// — the evicted entry must be the genuinely-coldest one, not the
	// earliest-inserted.
	//
	// We can't shrink the package-level l1MaxSessions (it's a const), so
	// this test instead verifies the ORDERING invariant directly via the
	// internal list: after N inserts + a re-touch, the back element is the
	// expected coldest. We do that by reading GetOrLoad (which only returns
	// present entries) and asserting which key survives churn.
	c := NewSessionCache(nil, nil)
	ctx := context.Background()

	// Fill L1 to capacity.
	for i := 0; i < l1MaxSessions; i++ {
		key := sessKey(i)
		if err := c.Set(ctx, "t", key, stateOf(key), []byte(key)); err != nil {
			t.Fatalf("Set(%s): %v", key, err)
		}
	}

	// Re-touch key 0 (the earliest-inserted) so it becomes MRU. Under the
	// OLD pseudo-LRU this would still be the first evicted; under true LRU
	// it must now survive.
	if _, _, err := c.GetOrLoad(ctx, "t", sessKey(0)); err != nil {
		t.Fatalf("GetOrLoad(sess0): %v", err)
	}

	// Insert one beyond capacity → exactly one eviction.
	if err := c.Set(ctx, "t", sessKey(l1MaxSessions), stateOf("overflow"), []byte("overflow")); err != nil {
		t.Fatalf("Set(overflow): %v", err)
	}

	// Key 0 must STILL be resident (it was touched), key 1 (the coldest
	// after re-touching key 0) must have been evicted.
	if st, _, _ := c.GetOrLoad(ctx, "t", sessKey(0)); st == nil {
		t.Fatalf("LRU regression: sess0 was evicted despite being the most-recently-used; expected it retained")
	}
	if st, _, _ := c.GetOrLoad(ctx, "t", sessKey(1)); st != nil {
		t.Fatalf("expected sess1 (coldest) evicted, but it is still resident")
	}
}

func TestSessionCache_LRU_UpdateInPlacePromotesToFront(t *testing.T) {
	// Re-Setting an existing key should NOT create a duplicate list node
	// (which would corrupt eviction). Verify by filling to capacity with a
	// duplicate Set on the middle key, then overflowing by one: only the
	// true coldest is evicted.
	c := NewSessionCache(nil, nil)
	ctx := context.Background()

	for i := 0; i < l1MaxSessions; i++ {
		_ = c.Set(ctx, "t", sessKey(i), stateOf(sessKey(i)), []byte(sessKey(i)))
	}

	// Update key 500 in place (promotes to front).
	_ = c.Set(ctx, "t", sessKey(500), stateOf("updated"), []byte("updated"))
	// Internal list length must NOT exceed capacity after the in-place update.
	c.mu.Lock()
	listLen := c.ll.Len()
	c.mu.Unlock()
	if listLen != l1MaxSessions {
		t.Fatalf("after in-place Set, list len = %d, want %d (in-place update must not duplicate)", listLen, l1MaxSessions)
	}

	// Overflow by one → key 0 (now coldest, since 500 was promoted and 1..499
	// are ordered behind it) should be evicted; key 500 retained.
	_ = c.Set(ctx, "t", sessKey(l1MaxSessions), stateOf("overflow"), []byte("overflow"))
	if st, _, _ := c.GetOrLoad(ctx, "t", sessKey(500)); st == nil || st.SystemPrompt != "updated" {
		t.Fatalf("expected updated sess500 retained, got st=%v", st)
	}
}

func TestSessionCache_LRU_InvalidateRemovesEntry(t *testing.T) {
	c := NewSessionCache(nil, nil)
	ctx := context.Background()

	_ = c.Set(ctx, "t", "a", stateOf("a"), []byte("a"))
	_ = c.Set(ctx, "t", "b", stateOf("b"), []byte("b"))
	c.Invalidate(ctx, "t", "a")

	if st, _, _ := c.GetOrLoad(ctx, "t", "a"); st != nil {
		t.Fatalf("expected a removed after Invalidate, still resident")
	}
	// list + map must agree on size.
	c.mu.Lock()
	if c.ll.Len() != 1 || len(c.l1) != 1 {
		t.Fatalf("expected 1 entry after invalidate, list=%d map=%d", c.ll.Len(), len(c.l1))
	}
	c.mu.Unlock()
}

func TestSessionCache_LRU_BoundedUnderChurn(t *testing.T) {
	// Insert far beyond capacity and assert the L1 never grows unbounded.
	c := NewSessionCache(nil, nil)
	ctx := context.Background()

	for i := 0; i < l1MaxSessions*3; i++ {
		_ = c.Set(ctx, "t", sessKey(i), stateOf(sessKey(i)), []byte(sessKey(i)))
	}
	c.mu.Lock()
	got := c.ll.Len()
	c.mu.Unlock()
	if got != l1MaxSessions {
		t.Fatalf("L1 grew unbounded: len=%d, want cap=%d", got, l1MaxSessions)
	}
	// The most-recently-inserted must be resident (it's MRU).
	if st, _, _ := c.GetOrLoad(ctx, "t", sessKey(l1MaxSessions*3-1)); st == nil {
		t.Fatalf("MRU entry missing after churn")
	}
}

func TestSessionCache_LRU_ConcurrentSafe(t *testing.T) {
	// Smoke-test the LRU under concurrency: no data races / panics. Run with
	// `go test -race` to actually exercise the detector.
	c := NewSessionCache(nil, nil)
	ctx := context.Background()
	done := make(chan struct{})
	const workers = 8
	for w := 0; w < workers; w++ {
		go func(off int) {
			for i := 0; i < 200; i++ {
				k := sessKey(off*1000 + i)
				_ = c.Set(ctx, "t", k, stateOf(k), []byte(k))
				_, _, _ = c.GetOrLoad(ctx, "t", k)
			}
			done <- struct{}{}
		}(w)
	}
	for w := 0; w < workers; w++ {
		<-done
	}
	c.mu.Lock()
	got := c.ll.Len()
	c.mu.Unlock()
	if got > l1MaxSessions {
		t.Fatalf("L1 exceeded capacity under concurrency: %d > %d", got, l1MaxSessions)
	}
}

// sessKey turns an int into a deterministic session id.
func sessKey(i int) string {
	return "sess-" + strconv.Itoa(i)
}
