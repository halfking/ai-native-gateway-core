package credentialfpslot

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestActiveGateHolderRenewsOwnSlot(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()

	lease := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
	originalSlot := lease.SlotIndex
	for i := 0; i < 10; i++ {
		m.Release(ctx, lease)
		next := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
		if next.SlotIndex != originalSlot {
			t.Fatalf("iteration %d: slot drifted from %d to %d", i, originalSlot, next.SlotIndex)
		}
		lease = next
	}
	m.Release(ctx, lease)
}

func TestActiveGateDoesNotPreemptActiveHolder(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()

	first := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
	m.Release(ctx, first)
	acquireExpectSaturated(t, m, ctx, 1, nil, "bob", "default")
	aliceAgain := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
	if aliceAgain.SlotIndex != first.SlotIndex {
		t.Fatalf("alice must reacquire her own slot %d, got %d", first.SlotIndex, aliceAgain.SlotIndex)
	}
	m.Release(ctx, aliceAgain)
}

func TestActiveGatePreemptsIdleHolderAfterGateWindow(t *testing.T) {
	m, mr := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()

	first := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
	m.Release(ctx, first)
	mr.FastForward(6 * time.Second)

	bob := acquireSuccess(t, m, ctx, 1, nil, "bob", "default")
	if bob.SlotIndex != first.SlotIndex {
		t.Fatalf("bob should reuse the same slot %d, got %d", first.SlotIndex, bob.SlotIndex)
	}
	if bob.Holder != "bob" {
		t.Fatalf("bob must own the slot now, got holder %q", bob.Holder)
	}
	m.Release(ctx, bob)
}

func TestActiveGatePreemptResetsPinToNewHolder(t *testing.T) {
	m, mr := newTestManager(t, Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()

	first := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
	m.Release(ctx, first)
	mr.FastForward(6 * time.Second)
	bob := acquireSuccess(t, m, ctx, 1, nil, "bob", "default")
	m.Release(ctx, bob)

	if m.hasPin(ctx, "alice", 1) {
		t.Fatal("alice pin should be cleared after preemption")
	}
	if !m.hasPin(ctx, "bob", 1) {
		t.Fatal("bob pin should be present after preemption")
	}
	pinned, err := m.client.Get(ctx, pinRedisKey("bob", 1)).Int()
	if err != nil {
		t.Fatalf("expected redis pin for bob: %v", err)
	}
	if pinned != first.SlotIndex {
		t.Fatalf("bob pin should point at slot %d, got %d", first.SlotIndex, pinned)
	}
}

func TestActiveGateAllActiveNoPreempt(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 2, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()

	a := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
	b := acquireSuccess(t, m, ctx, 1, nil, "bob", "default")
	m.Release(ctx, a)
	m.Release(ctx, b)
	acquireExpectSaturated(t, m, ctx, 1, nil, "charlie", "default")
}

func TestLRUPreemptOldestIdleFirst(t *testing.T) {
	m, mr := newTestManager(t, Config{DefaultLimit: 3, Enabled: true, ActiveGateSeconds: 5})
	ctx := context.Background()

	alice := acquireSuccess(t, m, ctx, 1, nil, "alice", "default")
	m.Release(ctx, alice)
	mr.FastForward(10 * time.Second)
	bob := acquireSuccess(t, m, ctx, 1, nil, "bob", "default")
	m.Release(ctx, bob)
	mr.FastForward(4 * time.Second)
	carol := acquireSuccess(t, m, ctx, 1, nil, "carol", "default")
	m.Release(ctx, carol)

	dave := acquireSuccess(t, m, ctx, 1, nil, "dave", "default")
	if dave.SlotIndex != alice.SlotIndex {
		t.Fatalf("dave should preempt oldest idle slot %d, got %d", alice.SlotIndex, dave.SlotIndex)
	}
	m.Release(ctx, dave)
}

func TestActiveGateDefaultConfig(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 1, Enabled: true})
	if got := m.cfg.resolveActiveGateSeconds(); got != DefaultActiveGateSeconds {
		t.Fatalf("resolveActiveGateSeconds = %d, want %d", got, DefaultActiveGateSeconds)
	}
	if m.cfg.ActiveGateSeconds != DefaultActiveGateSeconds {
		t.Fatalf("ActiveGateSeconds = %d, want %d", m.cfg.ActiveGateSeconds, DefaultActiveGateSeconds)
	}
}

func TestReclaimConfigFromManagerUsesReclaimIdleSeconds(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true, ActiveGateSeconds: 300, ReclaimIdleSeconds: 1800})
	cfg := m.reclaimConfigFromManager()
	if cfg.idleAfter != 30*time.Minute {
		t.Fatalf("reclaim idleAfter = %v, want 30m", cfg.idleAfter)
	}
}

func TestReclaimConfigFromManagerDefaultsTo30Min(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true, ActiveGateSeconds: 300})
	cfg := m.reclaimConfigFromManager()
	want := time.Duration(DefaultReclaimIdleSeconds) * time.Second
	if cfg.idleAfter != want {
		t.Fatalf("reclaim idleAfter = %v, want %v", cfg.idleAfter, want)
	}
}

var _ redis.Cmdable
