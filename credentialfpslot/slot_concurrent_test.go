package credentialfpslot

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAcquireReleaseConcurrent(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true})
	ctx := context.Background()

	const goroutines = 100
	const credentialID = 99

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var saturated atomic.Int32

	leases := make([]*Lease, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			holder := fmt.Sprintf("sess-%d", idx)
			lease, ok := m.Acquire(ctx, credentialID, nil, holder, "default")
			if !ok {
				saturated.Add(1)
				return
			}
			acquired.Add(1)
			leases[idx] = lease
		}(i)
	}

	wg.Wait()

	if acquired.Load() != 5 {
		t.Fatalf("expected 5 acquired, got %d", acquired.Load())
	}
	if saturated.Load() != 95 {
		t.Fatalf("expected 95 saturated, got %d", saturated.Load())
	}

	for _, lease := range leases {
		if lease != nil {
			m.Release(ctx, lease)
		}
	}

	acquireExpectSaturated(t, m, ctx, credentialID, nil, "new-sess", "default")
	for i, lease := range leases {
		if lease == nil {
			continue
		}
		reacquired := acquireSuccess(t, m, ctx, credentialID, nil, fmt.Sprintf("sess-%d", i), "default")
		if reacquired.SlotIndex != lease.SlotIndex {
			t.Fatalf("holder %d should reacquire slot %d, got %d", i, lease.SlotIndex, reacquired.SlotIndex)
		}
		m.Release(ctx, reacquired)
		break
	}
}

func TestStats(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 3, Enabled: true})
	ctx := context.Background()

	const credentialID = 300

	l, u, f := m.Stats(ctx, credentialID, nil)
	if l == nil || *l != 3 || u == nil || *u != 0 || f == nil || *f != 3 {
		t.Fatalf("unexpected empty stats: limit=%v used=%v free=%v", l, u, f)
	}

	lease := acquireSuccess(t, m, ctx, credentialID, nil, "h", "default")
	l, u, f = m.Stats(ctx, credentialID, nil)
	if u == nil || *u != 1 || f == nil || *f != 2 {
		t.Fatalf("unexpected occupied stats: limit=%v used=%v free=%v", l, u, f)
	}
	m.Release(ctx, lease)
}

func TestPinReuse(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 2, Enabled: true})
	ctx := context.Background()

	lease1 := acquireSuccess(t, m, ctx, 500, nil, "holder-1", "default")
	lease2 := acquireSuccess(t, m, ctx, 500, nil, "holder-1", "default")

	if lease1.SlotIndex != lease2.SlotIndex {
		t.Fatalf("expected same slot for pin reuse, got %d and %d", lease1.SlotIndex, lease2.SlotIndex)
	}

	m.Release(ctx, lease1)
	m.Release(ctx, lease2)
}

func TestNilManager(t *testing.T) {
	var m *Manager
	ctx := context.Background()

	if m.Enabled() {
		t.Error("nil manager should not be enabled")
	}
	if m.DefaultLimit() != 5 {
		t.Error("nil manager default limit should be 5")
	}
	lease, ok := m.Acquire(ctx, 1, nil, "h", "default")
	if !ok || lease == nil || !lease.Unlimited {
		t.Error("nil manager should return unlimited lease")
	}
	m.Release(ctx, lease)
}

func TestLeaseUnlimited(t *testing.T) {
	l := &Lease{Unlimited: true, CredentialID: 1, Holder: "h"}
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true})
	m.Release(context.Background(), l)
}
