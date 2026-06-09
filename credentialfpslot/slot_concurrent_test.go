package credentialfpslot

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireReleaseMemory_Concurrent(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()

	const goroutines = 100
	const credentialID = 99

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var saturated atomic.Int32
	var failed atomic.Int32

	leases := make([]*Lease, goroutines)
	acquiredFlags := make([]bool, goroutines)

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
			if lease == nil {
				failed.Add(1)
				return
			}
			acquired.Add(1)
			leases[idx] = lease
			acquiredFlags[idx] = true
		}(i)
	}

	wg.Wait()

	if acquired.Load() != 5 {
		t.Errorf("expected 5 acquired, got %d", acquired.Load())
	}
	if saturated.Load() != 95 {
		t.Errorf("expected 95 saturated, got %d", saturated.Load())
	}

	for i := 0; i < goroutines; i++ {
		if acquiredFlags[i] {
			m.Release(ctx, leases[i])
		}
	}

	lease, ok := m.Acquire(ctx, credentialID, nil, "new-sess", "default")
	if !ok || lease == nil {
		t.Error("expected successful acquire after release")
	}
	m.Release(ctx, lease)
}

func TestRoutingEligible_Concurrent(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil)
	ctx := context.Background()

	const credentialID = 100
	const goroutines = 50

	var wg sync.WaitGroup
	eligible := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			holder := fmt.Sprintf("holder-%d", idx)
			eligible[idx] = m.RoutingEligible(ctx, credentialID, nil, holder)
		}(i)
	}

	wg.Wait()

	eligibleCount := 0
	for _, e := range eligible {
		if e {
			eligibleCount++
		}
	}

	if eligibleCount < 1 {
		t.Error("expected at least 1 eligible")
	}
}

func TestAcquireReleaseRedis_Mock(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil)
	ctx := context.Background()

	const credentialID = 200

	lease1, ok1 := m.Acquire(ctx, credentialID, nil, "holder-1", "default")
	if !ok1 || lease1 == nil {
		t.Fatal("expected lease 1")
	}

	lease2, ok2 := m.Acquire(ctx, credentialID, nil, "holder-2", "default")
	if !ok2 || lease2 == nil {
		t.Fatal("expected lease 2")
	}

	lease3, ok3 := m.Acquire(ctx, credentialID, nil, "holder-3", "default")
	if !ok3 || lease3 == nil {
		t.Fatal("expected lease 3")
	}

	_, ok4 := m.Acquire(ctx, credentialID, nil, "holder-4", "default")
	if ok4 {
		t.Error("expected saturated")
	}

	m.Release(ctx, lease1)
	m.Release(ctx, lease2)
	m.Release(ctx, lease3)

	lease5, ok5 := m.Acquire(ctx, credentialID, nil, "holder-5", "default")
	if !ok5 || lease5 == nil {
		t.Fatal("expected lease after release")
	}
	m.Release(ctx, lease5)
}

func TestEffectiveLimit_EdgeCases(t *testing.T) {
	def := 5

	if r := EffectiveLimit(nil, def); r == nil || *r != 5 {
		t.Errorf("nil limit: expected 5, got %v", r)
	}

	zero := 0
	if r := EffectiveLimit(&zero, def); r != nil {
		t.Errorf("zero limit: expected nil, got %v", r)
	}

	neg := -1
	if r := EffectiveLimit(&neg, def); r != nil {
		t.Errorf("negative limit: expected nil, got %v", r)
	}

	seven := 7
	if r := EffectiveLimit(&seven, def); r == nil || *r != 7 {
		t.Errorf("explicit limit: expected 7, got %v", r)
	}
}

func TestStats(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil)
	ctx := context.Background()

	const credentialID = 300

	l, u, f := m.Stats(ctx, credentialID, nil)
	if l == nil || *l != 3 {
		t.Errorf("expected limit 3, got %v", l)
	}
	if u == nil || *u != 0 {
		t.Errorf("expected used 0, got %v", u)
	}
	if f == nil || *f != 3 {
		t.Errorf("expected free 3, got %v", f)
	}

	lease, _ := m.Acquire(ctx, credentialID, nil, "h", "default")
	l, u, f = m.Stats(ctx, credentialID, nil)
	if u == nil || *u != 1 {
		t.Errorf("expected used 1, got %v", u)
	}
	if f == nil || *f != 2 {
		t.Errorf("expected free 2, got %v", f)
	}

	m.Release(ctx, lease)
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
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()
	m.Release(ctx, l)
}

func BenchmarkAcquireRelease(b *testing.B) {
	m := New(Config{DefaultLimit: 100, Enabled: true}, nil)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lease, ok := m.Acquire(ctx, i%10, nil, fmt.Sprintf("h-%d", i), "default")
		if ok && lease != nil {
			m.Release(ctx, lease)
		}
	}
}

func BenchmarkAcquireRelease_Parallel(b *testing.B) {
	m := New(Config{DefaultLimit: 1000, Enabled: true}, nil)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			lease, ok := m.Acquire(ctx, i%10, nil, fmt.Sprintf("h-%d", i), "default")
			if ok && lease != nil {
				m.Release(ctx, lease)
			}
			i++
		}
	})
}

func TestMemoryFallback_Parallel(t *testing.T) {
	m := New(Config{DefaultLimit: 5, Enabled: true}, nil)
	ctx := context.Background()

	const credentialID = 400
	const goroutines = 20
	const iterations = 100

	var wg sync.WaitGroup
	var totalAcquired atomic.Int32
	var totalFailed atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(gIdx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				holder := fmt.Sprintf("g%d-h%d", gIdx, j)
				lease, ok := m.Acquire(ctx, credentialID, nil, holder, "default")
				if ok && lease != nil {
					totalAcquired.Add(1)
					time.Sleep(time.Microsecond)
					m.Release(ctx, lease)
				} else {
					totalFailed.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	if totalAcquired.Load() == 0 {
		t.Error("expected some acquisitions")
	}

	t.Logf("acquired=%d, failed=%d", totalAcquired.Load(), totalFailed.Load())
}

func TestPinReuse(t *testing.T) {
	m := New(Config{DefaultLimit: 2, Enabled: true}, nil)
	ctx := context.Background()

	const credentialID = 500

	lease1, ok1 := m.Acquire(ctx, credentialID, nil, "holder-1", "default")
	if !ok1 || lease1 == nil {
		t.Fatal("expected lease 1")
	}

	lease2, ok2 := m.Acquire(ctx, credentialID, nil, "holder-1", "default")
	if !ok2 || lease2 == nil {
		t.Fatal("expected lease 2 with same holder (pin reuse)")
	}

	if lease1.SlotIndex != lease2.SlotIndex {
		t.Errorf("expected same slot for pin reuse, got %d and %d", lease1.SlotIndex, lease2.SlotIndex)
	}

	m.Release(ctx, lease1)
	m.Release(ctx, lease2)
}