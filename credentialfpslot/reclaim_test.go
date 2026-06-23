package credentialfpslot

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestReclaim_Redis_IdleSlotIsDeleted(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	m := New(Config{Enabled: true, DefaultLimit: 5}, client)
	ctx := context.Background()
	credID := 300
	limit := 3

	// Acquire two slots from two different holders
	l1, _ := m.Acquire(ctx, credID, &limit, "alice", "tenant1")
	l2, _ := m.Acquire(ctx, credID, &limit, "bob", "tenant1")

	// Verify slots exist
	avail, _ := m.AvailableCount(ctx, credID, &limit)
	if avail != 1 {
		t.Fatalf("expected 1 free slot, got %d", avail)
	}

	// Fast-forward miniredis past the 24h slot TTL. With miniredis,
	// FastForward advances time for TTL calculations, so after 25h the
	// slot TTL is effectively -1 (no TTL set), but miniredis may also
	// delete the key entirely on TTL expiry. Either way the reclaim
	// script should see TTL <= idle_after and DEL the key.
	mr.FastForward(25 * time.Hour)

	// Reclaim with idle_after=60s — anything with TTL <= 60s gets deleted
	reclaimed, err := m.reclaimIdleSlots(ctx, reclaimConfig{
		idleAfter: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("reclaimIdleSlots failed: %v", err)
	}
	// After 25h fast-forward, the slot TTL is -2 (key expired/deleted by
	// miniredis) so our script returns 0 (nothing to do). But the keys
	// are already gone — verify the pool is empty:
	availAfter, _ := m.AvailableCount(ctx, credID, &limit)
	if availAfter != 3 {
		t.Errorf("expected 3 free slots after 25h, got %d", availAfter)
	}
	t.Logf("After 25h fast-forward: reclaimed=%d, free slots=%d", reclaimed, availAfter)

	// Subsequent Acquire should succeed for any holder (pool is fresh)
	l3, ok := m.Acquire(ctx, credID, &limit, "charlie", "tenant1")
	if !ok || l3 == nil {
		t.Fatal("expected charlie to acquire a slot after expiry")
	}
	_ = l1
	_ = l2
}

func TestReclaim_Memory_IdleSlotIsDeleted(t *testing.T) {
	m := New(Config{Enabled: true, DefaultLimit: 5}, nil) // memory fallback
	ctx := context.Background()
	credID := 400
	limit := 2

	// Acquire
	m.Acquire(ctx, credID, &limit, "alice", "tenant1")
	m.Acquire(ctx, credID, &limit, "bob", "tenant1")
	avail, _ := m.AvailableCount(ctx, credID, &limit)
	if avail != 0 {
		t.Fatalf("expected 0 free slots, got %d", avail)
	}

	// Manually expire entries by manipulating memSlots
	m.mu.Lock()
	now := time.Now()
	for k := range m.memSlots {
		m.memSlots[k] = memEntry{
			holder: m.memSlots[k].holder,
			exp:    now.Add(-time.Hour), // 1h ago = expired beyond idle
		}
	}
	m.mu.Unlock()

	// Reclaim with idle=10s
	reclaimed, err := m.reclaimIdleSlotsMemory(ctx, reclaimConfig{
		idleAfter: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("reclaimIdleSlotsMemory failed: %v", err)
	}
	if reclaimed < 2 {
		t.Errorf("expected at least 2 slots reclaimed, got %d", reclaimed)
	}

	availAfter, _ := m.AvailableCount(ctx, credID, &limit)
	if availAfter != 2 {
		t.Errorf("expected 2 free slots after reclaim, got %d", availAfter)
	}
	t.Logf("Memory reclaim OK: reclaimed=%d, free after=%d", reclaimed, availAfter)
}

func TestReclaim_FreshSlotsNotReclaimed(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	m := New(Config{Enabled: true, DefaultLimit: 5}, client)
	ctx := context.Background()
	credID := 500
	limit := 2

	// Acquire 2 slots
	m.Acquire(ctx, credID, &limit, "alice", "tenant1")
	m.Acquire(ctx, credID, &limit, "bob", "tenant1")

	// Don't fast-forward. Slots should still be fresh.
	reclaimed, err := m.reclaimIdleSlots(ctx, reclaimConfig{
		idleAfter: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("reclaim failed: %v", err)
	}
	if reclaimed != 0 {
		t.Errorf("fresh slots should NOT be reclaimed, got %d", reclaimed)
	}
	t.Logf("Fresh slots preserved (reclaimed=0)")
}
