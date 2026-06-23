package credentialfpslot

import (
	"context"
	"testing"
)

// TestFingerprintReuseMemory verifies that the same session reuses its fingerprint slot
// after release, and different sessions get different slots.
func TestFingerprintReuseMemory(t *testing.T) {
	m := New(Config{DefaultLimit: 2, Enabled: true}, nil)
	ctx := context.Background()
	credID := 123

	l1, ok := m.Acquire(ctx, credID, nil, "sess-a", "tenant1")
	if !ok {
		t.Fatal("sess-a should acquire slot")
	}
	slot1 := l1.SlotIndex
	t.Logf("sess-a acquired slot %d", slot1)

	m.Release(ctx, l1)

	l2, ok := m.Acquire(ctx, credID, nil, "sess-a", "tenant1")
	if !ok {
		t.Fatal("sess-a should reacquire slot")
	}
	if l2.SlotIndex != slot1 {
		t.Errorf("sess-a should reuse slot %d, got %d", slot1, l2.SlotIndex)
	}
	t.Logf("sess-a reacquired slot %d ✓", l2.SlotIndex)

	m.Release(ctx, l2)

	l3, ok := m.Acquire(ctx, credID, nil, "sess-b", "tenant1")
	if !ok {
		t.Fatal("sess-b should acquire slot")
	}
	if l3.SlotIndex == slot1 {
		t.Errorf("sess-b should NOT get sess-a's slot %d, got %d", slot1, l3.SlotIndex)
	}
	t.Logf("sess-b acquired slot %d ✓", l3.SlotIndex)

	_, ok = m.Acquire(ctx, credID, nil, "sess-c", "tenant1")
	if ok {
		t.Fatal("sess-c should fail (all slots occupied)")
	}
	t.Logf("sess-c failed to acquire (expected) ✓")
}

// TestLongTermOccupancy verifies that slots remain occupied after release
// for the duration of slotTTLSeconds (30 min).
func TestLongTermOccupancy(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil)
	ctx := context.Background()
	credID := 456
	limit := 3

	// All 3 slots free initially
	avail, _ := m.AvailableCount(ctx, credID, &limit)
	if avail != 3 {
		t.Fatalf("expected 3 available, got %d", avail)
	}

	// 3 different sessions acquire 3 different slots
	l1, _ := m.Acquire(ctx, credID, &limit, "sess-a", "tenant2")
	l2, _ := m.Acquire(ctx, credID, &limit, "sess-b", "tenant2")
	l3, _ := m.Acquire(ctx, credID, &limit, "sess-c", "tenant2")

	// All slots occupied
	avail, _ = m.AvailableCount(ctx, credID, &limit)
	if avail != 0 {
		t.Errorf("expected 0 available, got %d", avail)
	}

	// All release (but slots remain occupied for 30 min — long-term occupancy)
	m.Release(ctx, l1)
	m.Release(ctx, l2)
	m.Release(ctx, l3)

	// Still 0 available (long-term occupancy)
	avail, _ = m.AvailableCount(ctx, credID, &limit)
	if avail != 0 {
		t.Errorf("expected 0 available after release (long-term occupancy), got %d", avail)
	}

	// New session cannot acquire
	_, ok := m.Acquire(ctx, credID, &limit, "sess-d", "tenant2")
	if ok {
		t.Fatal("sess-d should fail (all slots long-term occupied)")
	}
	t.Logf("sess-d correctly rejected ✓")
}
