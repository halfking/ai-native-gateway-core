package credentialfpslot

import (
	"context"
	"testing"
)

func TestFingerprintReuse(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 2, Enabled: true})
	ctx := context.Background()
	credID := 123

	l1 := acquireSuccess(t, m, ctx, credID, nil, "sess-a", "tenant1")
	slot1 := l1.SlotIndex
	m.Release(ctx, l1)

	l2 := acquireSuccess(t, m, ctx, credID, nil, "sess-a", "tenant1")
	if l2.SlotIndex != slot1 {
		t.Fatalf("sess-a should reuse slot %d, got %d", slot1, l2.SlotIndex)
	}
	m.Release(ctx, l2)

	l3 := acquireSuccess(t, m, ctx, credID, nil, "sess-b", "tenant1")
	if l3.SlotIndex == slot1 {
		t.Fatalf("sess-b should not get sess-a's slot %d", slot1)
	}
	acquireExpectSaturated(t, m, ctx, credID, nil, "sess-c", "tenant1")
}

func TestLongTermOccupancy(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 3, Enabled: true})
	ctx := context.Background()
	credID := 456
	limit := 3

	avail, err := m.AvailableCount(ctx, credID, &limit)
	if err != nil || avail != 3 {
		t.Fatalf("expected 3 available, got avail=%d err=%v", avail, err)
	}

	l1 := acquireSuccess(t, m, ctx, credID, &limit, "sess-a", "tenant2")
	l2 := acquireSuccess(t, m, ctx, credID, &limit, "sess-b", "tenant2")
	l3 := acquireSuccess(t, m, ctx, credID, &limit, "sess-c", "tenant2")

	avail, _ = m.AvailableCount(ctx, credID, &limit)
	if avail != 0 {
		t.Fatalf("expected 0 available, got %d", avail)
	}

	m.Release(ctx, l1)
	m.Release(ctx, l2)
	m.Release(ctx, l3)

	avail, _ = m.AvailableCount(ctx, credID, &limit)
	if avail != 0 {
		t.Fatalf("expected 0 available after release, got %d", avail)
	}
	acquireExpectSaturated(t, m, ctx, credID, &limit, "sess-d", "tenant2")
}
