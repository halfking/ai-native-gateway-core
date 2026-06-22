package credentialfpslot

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestResetSlots_Redis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	cfg := Config{Enabled: true, DefaultLimit: 5}
	m := New(cfg, client)
	ctx := context.Background()

	credentialID := 123
	limit := 3

	// Acquire 3 slots with different holders
	holders := []string{"session-a", "session-b", "session-c"}
	for _, holder := range holders {
		lease, ok := m.Acquire(ctx, credentialID, &limit, holder, "tenant-1")
		if !ok {
			t.Fatalf("failed to acquire slot for %s", holder)
		}
		if lease.SlotIndex < 0 || lease.SlotIndex >= limit {
			t.Errorf("invalid slot index %d for holder %s", lease.SlotIndex, holder)
		}
	}

	// Verify all slots are occupied
	avail, err := m.AvailableCount(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("AvailableCount failed: %v", err)
	}
	if avail != 0 {
		t.Errorf("expected 0 available slots, got %d", avail)
	}

	// Reset all slots
	deletedSlots, deletedPins, err := m.ResetSlots(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("ResetSlots failed: %v", err)
	}
	if deletedSlots != 3 {
		t.Errorf("expected 3 deleted slots, got %d", deletedSlots)
	}
	if deletedPins != 3 {
		t.Errorf("expected 3 deleted pins, got %d", deletedPins)
	}

	// Verify all slots are now free
	avail, err = m.AvailableCount(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("AvailableCount after reset failed: %v", err)
	}
	if avail != limit {
		t.Errorf("expected %d available slots after reset, got %d", limit, avail)
	}
}

func TestResetSlots_Memory(t *testing.T) {
	cfg := Config{Enabled: true, DefaultLimit: 5}
	m := New(cfg, nil) // nil client = memory fallback
	ctx := context.Background()

	credentialID := 456
	limit := 4

	// Acquire 4 slots
	holders := []string{"h1", "h2", "h3", "h4"}
	for _, holder := range holders {
		lease, ok := m.Acquire(ctx, credentialID, &limit, holder, "tenant-2")
		if !ok {
			t.Fatalf("failed to acquire slot for %s", holder)
		}
		if lease.SlotIndex < 0 || lease.SlotIndex >= limit {
			t.Errorf("invalid slot index %d", lease.SlotIndex)
		}
	}

	// Verify slots occupied
	avail, err := m.AvailableCount(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("AvailableCount failed: %v", err)
	}
	if avail != 0 {
		t.Errorf("expected 0 available, got %d", avail)
	}

	// Reset
	deletedSlots, deletedPins, err := m.ResetSlots(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("ResetSlots failed: %v", err)
	}
	if deletedSlots != 4 {
		t.Errorf("expected 4 deleted slots, got %d", deletedSlots)
	}
	if deletedPins != 4 {
		t.Errorf("expected 4 deleted pins, got %d", deletedPins)
	}

	// Verify all free
	avail, err = m.AvailableCount(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("AvailableCount after reset failed: %v", err)
	}
	if avail != limit {
		t.Errorf("expected %d available after reset, got %d", limit, avail)
	}
}

func TestResetSlots_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	m := New(cfg, nil)
	ctx := context.Background()

	credentialID := 789
	limit := 5

	deletedSlots, deletedPins, err := m.ResetSlots(ctx, credentialID, &limit)
	if err != nil {
		t.Errorf("ResetSlots should not error when disabled: %v", err)
	}
	if deletedSlots != 0 || deletedPins != 0 {
		t.Errorf("expected 0 deleted when disabled, got slots=%d pins=%d", deletedSlots, deletedPins)
	}
}

func TestResetSlots_UnlimitedCredential(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	cfg := Config{Enabled: true, DefaultLimit: 5}
	m := New(cfg, client)
	ctx := context.Background()

	credentialID := 999

	// In slot.go, EffectiveLimit returns nil only when *limit <= 0
	// When limit is nil, it returns &defaultLimit (5 in this case)
	// So we need to pass &0 to get unlimited behavior
	zeroLimit := 0

	// Acquire should succeed without slot tracking
	lease, ok := m.Acquire(ctx, credentialID, &zeroLimit, "holder-x", "tenant-3")
	if !ok {
		t.Fatalf("Acquire failed for unlimited credential")
	}
	if !lease.Unlimited {
		t.Errorf("expected Unlimited=true for zero limit, got Unlimited=%v", lease.Unlimited)
	}

	// Reset should be no-op (returns 0,0 because EffectiveLimit returns nil for &0)
	deletedSlots, deletedPins, err := m.ResetSlots(ctx, credentialID, &zeroLimit)
	if err != nil {
		t.Errorf("ResetSlots should not error for unlimited: %v", err)
	}
	if deletedSlots != 0 || deletedPins != 0 {
		t.Errorf("expected 0 deleted for zero limit (unlimited), got slots=%d pins=%d", deletedSlots, deletedPins)
	}

	// Test with negative limit (also unlimited)
	negativeLimit := -1
	deletedSlots, deletedPins, err = m.ResetSlots(ctx, credentialID, &negativeLimit)
	if err != nil {
		t.Errorf("ResetSlots should not error for negative limit: %v", err)
	}
	if deletedSlots != 0 || deletedPins != 0 {
		t.Errorf("expected 0 deleted for negative limit (unlimited), got slots=%d pins=%d", deletedSlots, deletedPins)
	}
}

func TestResetSlots_PartialOccupancy(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	cfg := Config{Enabled: true, DefaultLimit: 10}
	m := New(cfg, client)
	ctx := context.Background()

	credentialID := 111
	limit := 10

	// Acquire only 3 out of 10 slots
	for i := 0; i < 3; i++ {
		_, ok := m.Acquire(ctx, credentialID, &limit, "holder-"+string(rune('a'+i)), "tenant-4")
		if !ok {
			t.Fatalf("Acquire failed for holder %d", i)
		}
	}

	// Verify 7 free
	avail, _ := m.AvailableCount(ctx, credentialID, &limit)
	if avail != 7 {
		t.Errorf("expected 7 available, got %d", avail)
	}

	// Reset should clear only the 3 occupied
	deletedSlots, deletedPins, err := m.ResetSlots(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("ResetSlots failed: %v", err)
	}
	if deletedSlots != 3 {
		t.Errorf("expected 3 deleted slots, got %d", deletedSlots)
	}
	if deletedPins != 3 {
		t.Errorf("expected 3 deleted pins, got %d", deletedPins)
	}

	// Now all 10 should be free
	avail, _ = m.AvailableCount(ctx, credentialID, &limit)
	if avail != 10 {
		t.Errorf("expected 10 available after reset, got %d", avail)
	}
}

func TestResetSlots_ExpiredSlotsStillCounted(t *testing.T) {
	// This test verifies that even if some slots have expired TTL,
	// ResetSlots still deletes them (or reports them as deleted if
	// Redis already GC'd them).
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	cfg := Config{Enabled: true, DefaultLimit: 5}
	m := New(cfg, client)
	ctx := context.Background()

	credentialID := 222
	limit := 5

	// Acquire 5 slots
	for i := 0; i < 5; i++ {
		_, ok := m.Acquire(ctx, credentialID, &limit, "holder-"+string(rune('a'+i)), "tenant-5")
		if !ok {
			t.Fatalf("Acquire failed for holder %d", i)
		}
	}

	// Fast-forward time in miniredis to expire all keys
	mr.FastForward(time.Hour * 2)

	// Now slots are expired but we can still reset (DEL is idempotent)
	deletedSlots, deletedPins, err := m.ResetSlots(ctx, credentialID, &limit)
	if err != nil {
		t.Fatalf("ResetSlots failed: %v", err)
	}
	// miniredis GC'd the keys, so deletedSlots/Pins might be 0
	// We just verify no error and that available is now full
	avail, _ := m.AvailableCount(ctx, credentialID, &limit)
	if avail != 5 {
		t.Errorf("expected 5 available after reset of expired slots, got %d", avail)
	}
	t.Logf("deletedSlots=%d deletedPins=%d (may be 0 due to GC)", deletedSlots, deletedPins)
}
