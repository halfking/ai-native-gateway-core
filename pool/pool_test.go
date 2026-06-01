package pool

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPoolKeyString(t *testing.T) {
	key := PoolKey{
		IdentityHash: "abcdef1234567890",
		ProviderID:   100,
		CredentialID: 5,
	}
	s := key.String()
	if s != "abcdef1234567890/100/5" {
		t.Fatalf("unexpected key: %q", s)
	}
}

func TestNewPoolDefaultsToActive(t *testing.T) {
	key := PoolKey{IdentityHash: "a", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "")
	if p.State() != PoolActive {
		t.Fatal("new pool should be active")
	}
}

func TestPoolFailureDegradation(t *testing.T) {
	key := PoolKey{IdentityHash: "b", ProviderID: 2, CredentialID: 2}
	p := NewPool(key, "")

	for i := 0; i < degradedThreshold; i++ {
		p.RecordFailure()
	}
	if p.State() != PoolDegraded {
		t.Fatal("pool should be degraded after 3 failures")
	}

	p.RecordSuccess()
	p.RecordSuccess()
	p.RecordSuccess()
	if p.State() != PoolActive {
		t.Fatal("pool should be active after enough consecutive successes")
	}
}

func TestPoolManagerCreateAndGet(t *testing.T) {
	pm := NewPoolManager()
	key := PoolKey{IdentityHash: "c", ProviderID: 3, CredentialID: 3}

	p1 := pm.GetOrCreate(key, "")
	p2 := pm.GetOrCreate(key, "")
	if p1 != p2 {
		t.Fatal("GetOrCreate should return same instance")
	}
}

func TestPoolManagerStats(t *testing.T) {
	pm := NewPoolManager()
	pm.GetOrCreate(PoolKey{IdentityHash: "x", ProviderID: 1, CredentialID: 1}, "")
	pm.GetOrCreate(PoolKey{IdentityHash: "y", ProviderID: 2, CredentialID: 2}, "")

	stats := pm.Stats()
	if stats["active"] != 2 {
		t.Fatalf("expected 2 active pools, got %d", stats["active"])
	}
}

func TestPoolClose(t *testing.T) {
	p := NewPool(PoolKey{IdentityHash: "d", ProviderID: 4, CredentialID: 4}, "")
	p.Close()
	// Should not panic on double close
	p.Close()
}

func TestPoolManagerStopPreventsNewPools(t *testing.T) {
	pm := NewPoolManager()
	pm.Stop()
	if p := pm.GetOrCreate(PoolKey{IdentityHash: "stop", ProviderID: 9, CredentialID: 9}, ""); p != nil {
		t.Fatal("GetOrCreate should return nil after Stop")
	}
	// Should stay safe on repeated stop calls.
	pm.Stop()
}

func TestPoolAcquireReleaseRespectsCapacity(t *testing.T) {
	p := NewPool(PoolKey{IdentityHash: "cap", ProviderID: 7, CredentialID: 7}, "")
	ctx := context.Background()
	for i := 0; i < poolMaxActiveConns; i++ {
		if err := p.Acquire(ctx); err != nil {
			t.Fatalf("acquire %d failed: %v", i, err)
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := p.Acquire(timeoutCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded when pool is full, got %v", err)
	}
	p.Release()
	if err := p.Acquire(ctx); err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
}

func TestPoolAcquireFailsAfterClose(t *testing.T) {
	p := NewPool(PoolKey{IdentityHash: "closed", ProviderID: 8, CredentialID: 8}, "")
	p.Close()
	if err := p.Acquire(context.Background()); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("expected ErrPoolClosed, got %v", err)
	}
}
