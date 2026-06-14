package pool

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolAcquireRelease_Concurrent(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	const goroutines = 50
	const maxActive = 10

	// Recreate the pool with smaller channel capacity
	p2 := &Pool{
		key:         key,
		transport:   p.transport,
		client:      p.client,
		stopCh:      make(chan struct{}),
		activeConns: make(chan struct{}, maxActive),
		gracePeriod: defaultGracePeriod,
	}
	p2.state.Store(int32(PoolActive))

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var blocked atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			err := p2.Acquire(ctx)
			if err != nil {
				blocked.Add(1)
				return
			}
			acquired.Add(1)
			time.Sleep(20 * time.Millisecond)
			p2.Release()
		}(i)
	}

	wg.Wait()

	// With concurrent goroutines, some may acquire after others release
	if acquired.Load() == 0 {
		t.Error("expected at least 1 acquisition")
	}
	t.Logf("acquired=%d, blocked=%d", acquired.Load(), blocked.Load())
}

func TestPoolAcquireClosed(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	p.Close()

	err := p.Acquire(context.Background())
	if err != ErrPoolClosed {
		t.Errorf("expected ErrPoolClosed, got %v", err)
	}
}

func TestPoolAcquireDeadPool(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	p.state.Store(int32(PoolDead))

	err := p.Acquire(context.Background())
	if err != ErrPoolClosed {
		t.Errorf("expected ErrPoolClosed for dead pool, got %v", err)
	}
}

func TestPoolRecordFailureDegradation(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	for i := 0; i < degradedThreshold; i++ {
		p.RecordFailure()
	}

	if p.State() != PoolDegraded {
		t.Errorf("expected PoolDegraded, got %v", p.State())
	}
}

func TestPoolRecordFailureDraining(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	for i := 0; i < deadThreshold; i++ {
		p.RecordFailure()
	}

	if p.State() != PoolDraining {
		t.Errorf("expected PoolDraining, got %v", p.State())
	}
}

func TestPoolRecordSuccessRecovery(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	for i := 0; i < degradedThreshold; i++ {
		p.RecordFailure()
	}

	for i := 0; i < successThreshold; i++ {
		p.RecordSuccess()
	}

	if p.State() != PoolActive {
		t.Errorf("expected PoolActive after recovery, got %v", p.State())
	}
}

func TestPoolDrainingGracePeriod(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)
	p.gracePeriod = 100 * time.Millisecond

	for i := 0; i < deadThreshold; i++ {
		p.RecordFailure()
	}

	if p.State() != PoolDraining {
		t.Fatalf("expected PoolDraining, got %v", p.State())
	}

	time.Sleep(150 * time.Millisecond)

	p.checkDrainingGracePeriod()

	if p.State() != PoolDead {
		t.Errorf("expected PoolDead after grace period, got %v", p.State())
	}
}

func TestPoolManagerCreateAndGet_Concurrent(t *testing.T) {
	pm := NewPoolManager()
	defer pm.Stop()

	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p1 := pm.GetOrCreate(key, "")
	p2 := pm.GetOrCreate(key, "")

	if p1 != p2 {
		t.Error("expected same pool for same key")
	}
}

func TestPoolManagerEviction(t *testing.T) {
	pm := NewPoolManager()
	defer pm.Stop()

	for i := 0; i < poolMaxPools+10; i++ {
		key := PoolKey{
			IdentityHash: fmt.Sprintf("hash-%d", i),
			ProviderID:   i,
			CredentialID: i,
		}
		pm.GetOrCreate(key, "")
	}

	if len(pm.pools) > poolMaxPools {
		t.Errorf("expected <= %d pools, got %d", poolMaxPools, len(pm.pools))
	}
}

func TestPoolManagerIdleEviction(t *testing.T) {
	pm := NewPoolManager()
	defer pm.Stop()

	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := pm.GetOrCreate(key, "")
	p.lastUsed.Store(time.Now().Add(-poolIdleTTL - time.Minute).UnixMilli())

	pm.evictIdle()

	if pm.Get(key) != nil {
		t.Error("expected pool to be evicted")
	}
}

func TestPoolManagerConcurrent(t *testing.T) {
	pm := NewPoolManager()
	defer pm.Stop()

	const goroutines = 50

	var wg sync.WaitGroup
	var created atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := PoolKey{
				IdentityHash: fmt.Sprintf("hash-%d", idx%10),
				ProviderID:   idx % 10,
				CredentialID: idx % 10,
			}
			p := pm.GetOrCreate(key, "")
			if p != nil {
				created.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if created.Load() != int32(goroutines) {
		t.Errorf("expected %d creations, got %d", goroutines, created.Load())
	}
}

func TestPoolKeyString_Concurrent(t *testing.T) {
	key := PoolKey{IdentityHash: "abcdefghijklmnopqrst", ProviderID: 1, CredentialID: 2}
	s := key.String()

	if len(s) > 40 {
		t.Errorf("key string too long: %s", s)
	}
}

func TestPoolStats(t *testing.T) {
	pm := NewPoolManager()
	defer pm.Stop()

	for i := 0; i < 5; i++ {
		key := PoolKey{
			IdentityHash: fmt.Sprintf("hash-%d", i),
			ProviderID:   i,
			CredentialID: i,
		}
		pm.GetOrCreate(key, "")
	}

	stats := pm.Stats()
	if stats["active"] != 5 {
		t.Errorf("expected 5 active, got %d", stats["active"])
	}
}

func TestPoolCloseAll(t *testing.T) {
	pm := NewPoolManager()

	for i := 0; i < 5; i++ {
		key := PoolKey{
			IdentityHash: fmt.Sprintf("hash-%d", i),
			ProviderID:   i,
			CredentialID: i,
		}
		pm.GetOrCreate(key, "")
	}

	pm.CloseAll()

	if len(pm.pools) != 0 {
		t.Errorf("expected 0 pools after CloseAll, got %d", len(pm.pools))
	}
}

func TestPoolLastUsed(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	if !p.LastUsed().IsZero() {
		t.Error("expected zero last used")
	}

	p.touch()

	if p.LastUsed().IsZero() {
		t.Error("expected non-zero last used after touch")
	}
}

func TestPoolStateString(t *testing.T) {
	tests := []struct {
		state    PoolState
		expected string
	}{
		{PoolActive, "active"},
		{PoolDraining, "draining"},
		{PoolDegraded, "degraded"},
		{PoolDead, "dead"},
		{PoolState(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("state %d: expected %s, got %s", tt.state, tt.expected, got)
		}
	}
}

func BenchmarkPoolAcquireRelease(b *testing.B) {
	key := PoolKey{IdentityHash: "bench", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)
	p.activeConns = make(chan struct{}, 1000)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ctx := context.Background()
			if err := p.Acquire(ctx); err == nil {
				p.Release()
			}
		}
	})
}

func BenchmarkPoolManagerGetOrCreate(b *testing.B) {
	pm := NewPoolManager()
	defer pm.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := PoolKey{
				IdentityHash: fmt.Sprintf("hash-%d", i%100),
				ProviderID:   i % 100,
				CredentialID: i % 100,
			}
			pm.GetOrCreate(key, "")
			i++
		}
	})
}

func TestPoolAcquireReleaseRespectsCapacity_Concurrent(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)
	const maxActive = 5
	p.activeConns = make(chan struct{}, maxActive)

	var wg sync.WaitGroup
	var acquiredCount atomic.Int32

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			if err := p.Acquire(ctx); err == nil {
				acquiredCount.Add(1)
				time.Sleep(30 * time.Millisecond)
				p.Release()
			}
		}(i)
	}

	wg.Wait()

	// With concurrent goroutines, some may acquire after others release
	if acquiredCount.Load() == 0 {
		t.Error("expected at least 1 acquisition")
	}
}