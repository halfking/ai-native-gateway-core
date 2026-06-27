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

func TestAcquireReleaseRedis(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 2, Enabled: true})
	ctx := context.Background()

	l1 := acquireSuccess(t, m, ctx, 9, nil, "sess-a", "default")
	l2 := acquireSuccess(t, m, ctx, 9, nil, "sess-b", "default")
	acquireExpectSaturated(t, m, ctx, 9, nil, "sess-c", "default")

	m.Release(ctx, l1)
	acquireExpectSaturated(t, m, ctx, 9, nil, "sess-c", "default")

	l3 := acquireSuccess(t, m, ctx, 9, nil, "sess-a", "default")
	if l3.SlotIndex != l1.SlotIndex {
		t.Fatalf("sess-a should get same slot %d, got %d", l1.SlotIndex, l3.SlotIndex)
	}
	m.Release(ctx, l2)
	m.Release(ctx, l3)
}

func TestRoutingEligible(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true})
	ctx := context.Background()
	if !m.RoutingEligible(ctx, 3, nil, "new") {
		t.Fatal("should be eligible")
	}
	lease := acquireSuccess(t, m, ctx, 3, nil, "only", "default")
	if m.RoutingEligible(ctx, 3, nil, "other") {
		t.Fatal("should be saturated")
	}
	if !m.RoutingEligible(ctx, 3, nil, "only") {
		t.Fatal("pinned holder should stay eligible")
	}
	m.Release(ctx, lease)
}

func TestReleaseKeepsPinForNextAcquire(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true})
	ctx := context.Background()

	first := acquireSuccess(t, m, ctx, 7, nil, "sess-a", "default")
	originalSlot := first.SlotIndex
	m.Release(ctx, first)

	if !m.hasPin(ctx, "sess-a", 7) {
		t.Fatal("pin should be retained after release")
	}

	second := acquireSuccess(t, m, ctx, 7, nil, "sess-a", "default")
	if second.SlotIndex != originalSlot {
		t.Fatalf("slot should be stable across release+reacquire: got %d, want %d", second.SlotIndex, originalSlot)
	}
	if first.Egress == nil || second.Egress == nil {
		t.Fatal("expected egress identity on both leases")
	}
	if first.Egress.EgressSeed != second.Egress.EgressSeed {
		t.Fatalf("egress seed should match: first=%s second=%s", first.Egress.EgressSeed, second.Egress.EgressSeed)
	}
	m.Release(ctx, second)
}

func TestReleaseLongTermOccupancyPreventsSteal(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true})
	ctx := context.Background()

	first := acquireSuccess(t, m, ctx, 7, nil, "sess-a", "default")
	originalSlot := first.SlotIndex
	m.Release(ctx, first)

	acquireExpectSaturated(t, m, ctx, 7, nil, "sess-b", "default")

	migrated := acquireSuccess(t, m, ctx, 7, nil, "sess-a", "default")
	if migrated.SlotIndex != originalSlot {
		t.Fatalf("sess-a should reuse slot %d, got %d", originalSlot, migrated.SlotIndex)
	}
	m.Release(ctx, migrated)
}

func TestForceUnpinRemovesPinForNewAcquire(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true})
	ctx := context.Background()

	first := acquireSuccess(t, m, ctx, 11, nil, "sess-x", "default")
	m.Release(ctx, first)

	if !m.hasPin(ctx, "sess-x", 11) {
		t.Fatal("pin should be present before ForceUnpin")
	}
	m.ForceUnpin(ctx, "sess-x", 11)
	if m.hasPin(ctx, "sess-x", 11) {
		t.Fatal("pin should be gone after ForceUnpin")
	}

	next := acquireSuccess(t, m, ctx, 11, nil, "sess-x", "default")
	m.Release(ctx, next)
}

func TestAcquireStickyAcrossReleases(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true})
	ctx := context.Background()

	lease := acquireSuccess(t, m, ctx, 21, nil, "steady", "default")
	expectedSlot := lease.SlotIndex

	for i := 0; i < 20; i++ {
		m.Release(ctx, lease)
		next := acquireSuccess(t, m, ctx, 21, nil, "steady", "default")
		if next.SlotIndex != expectedSlot {
			t.Fatalf("iteration %d: slot drifted from %d to %d", i, expectedSlot, next.SlotIndex)
		}
		lease = next
	}
	m.Release(ctx, lease)
}
