package credentialfpslot

import (
	"context"
	"testing"
)

func TestEffectiveLimit(t *testing.T) {
	def := 5
	if EffectiveLimit(nil, def) == nil || *EffectiveLimit(nil, def) != 5 {
		t.Fatal("nil limit should default to 5")
	}
	zero := 0
	if EffectiveLimit(&zero, def) != nil {
		t.Fatal("0 should mean unlimited")
	}
	seven := 7
	if EffectiveLimit(&seven, def) == nil || *EffectiveLimit(&seven, def) != 7 {
		t.Fatal("explicit limit")
	}
}

func TestAcquireReleaseMemory(t *testing.T) {
	m := New(Config{DefaultLimit: 2, Enabled: true}, nil)
	ctx := context.Background()

	l1, ok := m.Acquire(ctx, 9, nil, "sess-a", "default")
	if !ok || l1 == nil {
		t.Fatal("expected lease 1")
	}
	l2, ok := m.Acquire(ctx, 9, nil, "sess-b", "default")
	if !ok || l2 == nil {
		t.Fatal("expected lease 2")
	}
	_, ok = m.Acquire(ctx, 9, nil, "sess-c", "default")
	if ok {
		t.Fatal("expected saturated")
	}
	m.Release(ctx, l1)
	l3, ok := m.Acquire(ctx, 9, nil, "sess-c", "default")
	if !ok || l3 == nil {
		t.Fatal("expected lease after release")
	}
	m.Release(ctx, l2)
	m.Release(ctx, l3)
}

func TestRoutingEligible(t *testing.T) {
	m := New(Config{DefaultLimit: 1, Enabled: true}, nil)
	ctx := context.Background()
	if !m.RoutingEligible(ctx, 3, nil, "new") {
		t.Fatal("should be eligible")
	}
	lease, _ := m.Acquire(ctx, 3, nil, "only", "default")
	if m.RoutingEligible(ctx, 3, nil, "other") {
		t.Fatal("should be saturated")
	}
	if !m.RoutingEligible(ctx, 3, nil, "only") {
		t.Fatal("pinned holder should stay eligible")
	}
	m.Release(ctx, lease)
}

// TestRelease_KeepsPin_ForNextAcquire pins the new behaviour: a holder that
// releases its lease should be able to re-acquire the SAME slot on the next
// call (within the 30-min pin TTL). This is the core invariant for
// "连接后保持稳定" (stable after connection).
func TestRelease_KeepsPin_ForNextAcquire(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()

	first, ok := m.Acquire(ctx, 7, nil, "sess-a", "default")
	if !ok || first == nil {
		t.Fatal("expected first lease")
	}
	originalSlot := first.SlotIndex

	m.Release(ctx, first)

	// Pin must survive release.
	if !m.hasPin(ctx, "sess-a", 7) {
		t.Fatal("pin should be retained after release")
	}

	second, ok := m.Acquire(ctx, 7, nil, "sess-a", "default")
	if !ok || second == nil {
		t.Fatal("expected second lease")
	}
	if second.SlotIndex != originalSlot {
		t.Fatalf("slot should be stable across release+reacquire: got %d, want %d",
			second.SlotIndex, originalSlot)
	}
	// Egress identity must be byte-equal (same credentialID + same slot).
	if first.Egress == nil || second.Egress == nil {
		t.Fatal("expected egress identity on both leases")
	}
	if first.Egress.EgressSeed != second.Egress.EgressSeed {
		t.Fatalf("egress seed should match: first=%s second=%s",
			first.Egress.EgressSeed, second.Egress.EgressSeed)
	}
	if first.Egress.VirtualClientID != second.Egress.VirtualClientID {
		t.Fatalf("virtual client id should match: first=%s second=%s",
			first.Egress.VirtualClientID, second.Egress.VirtualClientID)
	}
	m.Release(ctx, second)
}

// TestRelease_AllowsMigration_WhenContended: if another holder has taken the
// pinned slot, the original holder migrates to a new free slot and its pin
// is updated to the new index. This protects against permanently dead slots.
func TestRelease_AllowsMigration_WhenContended(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()

	first, ok := m.Acquire(ctx, 7, nil, "sess-a", "default")
	if !ok || first == nil {
		t.Fatal("expected first lease")
	}
	originalSlot := first.SlotIndex
	m.Release(ctx, first)

	// sess-b takes the slot sess-a had pinned.
	steal, ok := m.Acquire(ctx, 7, nil, "sess-b", "default")
	if !ok || steal == nil {
		t.Fatal("expected steal lease")
	}
	if steal.SlotIndex != originalSlot {
		t.Fatalf("sess-b should take sess-a's freed slot: got %d, want %d",
			steal.SlotIndex, originalSlot)
	}

	// sess-a re-acquires: pinned slot is taken, must migrate to a new slot.
	migrated, ok := m.Acquire(ctx, 7, nil, "sess-a", "default")
	if !ok || migrated == nil {
		t.Fatal("expected migrated lease")
	}
	if migrated.SlotIndex == originalSlot {
		t.Fatalf("sess-a should NOT take the stolen slot back: got %d", migrated.SlotIndex)
	}
	if migrated.SlotIndex == steal.SlotIndex {
		// Same as stealer (only 2 free + 1 taken = conflict). This is
		// allowed only if the limit was 1; with limit=5 it shouldn't happen.
		t.Fatalf("sess-a migrated onto sess-b's slot %d, expected new slot", migrated.SlotIndex)
	}
	// Pin should now point to the new slot.
	if !m.hasPin(ctx, "sess-a", 7) {
		t.Fatal("pin should be updated to migrated slot")
	}

	m.Release(ctx, steal)
	m.Release(ctx, migrated)
}

// TestForceUnpin_RemovesPin_ForNewAcquire: after ForceUnpin, the holder's
// next Acquire behaves as a fresh acquisition (scan, no pin preference).
func TestForceUnpin_RemovesPin_ForNewAcquire(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()

	first, ok := m.Acquire(ctx, 11, nil, "sess-x", "default")
	if !ok || first == nil {
		t.Fatal("expected first lease")
	}
	m.Release(ctx, first)

	if !m.hasPin(ctx, "sess-x", 11) {
		t.Fatal("pin should be present before ForceUnpin")
	}
	m.ForceUnpin(ctx, "sess-x", 11)

	if m.hasPin(ctx, "sess-x", 11) {
		t.Fatal("pin should be gone after ForceUnpin")
	}

	// Without a pin, the next acquire takes the first free slot (slot 0
	// with limit=5, since the previous one was released). The point of
	// this test is that Acquire still works without a pin; the specific
	// slot index is determined by the scan order.
	next, ok := m.Acquire(ctx, 11, nil, "sess-x", "default")
	if !ok || next == nil {
		t.Fatal("expected lease after force-unpin")
	}
	m.Release(ctx, next)
}

// TestAcquire_Sticky_AcrossReleases: a holder that repeatedly releases and
// re-acquires should stay on the same slot as long as no other holder
// takes it in between. Simulates the steady-state "healthy session" case.
func TestAcquire_Sticky_AcrossReleases(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()

	first, ok := m.Acquire(ctx, 21, nil, "steady", "default")
	if !ok || first == nil {
		t.Fatal("expected initial lease")
	}
	expectedSlot := first.SlotIndex

	for i := 0; i < 50; i++ {
		m.Release(ctx, first)
		next, ok := m.Acquire(ctx, 21, nil, "steady", "default")
		if !ok || next == nil {
			t.Fatalf("iteration %d: expected lease", i)
		}
		if next.SlotIndex != expectedSlot {
			t.Fatalf("iteration %d: slot drifted from %d to %d (expected no contention)",
				i, expectedSlot, next.SlotIndex)
		}
		first = next
	}
	m.Release(ctx, first)
}
