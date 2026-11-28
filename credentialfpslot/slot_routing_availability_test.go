package credentialfpslot

import (
	"context"
	"testing"
	"time"
)

// 2026-11-28（glm-5.3-flash 单候选 429 事故）：路由预过滤读到的必须是
// 「Acquire 现在能做什么」，而不是「槽键是否存在」。本文件把
// RoutingAvailabilityForTenant 的每一档都钉到 acquireRedis 的真实行为上：
// 判定说能拿到槽，Acquire 就必须真拿到；判定说拿不到，Acquire 就必须饱和。

func TestRoutingAvailability_FreePool(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 2, Enabled: true, ActiveGateSeconds: 5})
	got := m.RoutingAvailabilityForTenant(context.Background(), 57, nil, "newcomer", "default")
	if got != SlotFree {
		t.Fatalf("empty pool availability = %v, want SlotFree", got)
	}
}

func TestRoutingAvailability_PoolHeldByActiveHolderIsNoneAvailable(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()
	m.Release(ctx, acquireSuccess(t, m, ctx, 57, nil, "alice", "default"))

	if got := m.RoutingAvailabilityForTenant(ctx, 57, nil, "bob", "default"); got != SlotNoneAvailable {
		t.Fatalf("pool held by an active holder availability = %v, want SlotNoneAvailable", got)
	}
	acquireExpectSaturated(t, m, ctx, 57, nil, "bob", "default")
}

func TestRoutingAvailability_IdleHolderMakesPoolPreemptableAndAcquirePreempts(t *testing.T) {
	m, mr := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()
	m.Release(ctx, acquireSuccess(t, m, ctx, 57, nil, "alice", "default"))

	// Before the gate the slot reads as taken — the 5-minute rule protects alice.
	mr.FastForward(4 * time.Second)
	if got := m.RoutingAvailabilityForTenant(ctx, 57, nil, "bob", "default"); got != SlotNoneAvailable {
		t.Fatalf("inside the active gate availability = %v, want SlotNoneAvailable", got)
	}

	// Past the gate the slot is preemptable…
	mr.FastForward(2 * time.Second)
	if got := m.RoutingAvailabilityForTenant(ctx, 57, nil, "bob", "default"); got != SlotPreemptable {
		t.Fatalf("past the active gate availability = %v, want SlotPreemptable", got)
	}

	// …and Acquire really does take it (the verdict must never over-promise).
	lease := acquireSuccess(t, m, ctx, 57, nil, "bob", "default")
	if lease.Holder != "bob" {
		t.Fatalf("preempted lease holder = %q, want bob", lease.Holder)
	}
	m.Release(ctx, lease)
}

func TestRoutingAvailability_PinnedHolderStaysFreeOnFullPool(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()
	m.Release(ctx, acquireSuccess(t, m, ctx, 57, nil, "alice", "default"))

	if got := m.RoutingAvailabilityForTenant(ctx, 57, nil, "alice", "default"); got != SlotFree {
		t.Fatalf("pinned holder availability = %v, want SlotFree", got)
	}
	acquireSuccess(t, m, ctx, 57, nil, "alice", "default")
}

func TestRoutingAvailability_UnlimitedAndDisabledPoolsAreFree(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	m.Release(ctx, acquireSuccess(t, m, ctx, 57, nil, "alice", "default"))

	unlimited := 0
	if got := m.RoutingAvailabilityForTenant(ctx, 57, &unlimited, "bob", "default"); got != SlotFree {
		t.Fatalf("unlimited pool availability = %v, want SlotFree", got)
	}

	var nilManager *Manager
	if got := nilManager.RoutingAvailabilityForTenant(ctx, 57, nil, "bob", "default"); got != SlotFree {
		t.Fatalf("nil manager availability = %v, want SlotFree", got)
	}
}
