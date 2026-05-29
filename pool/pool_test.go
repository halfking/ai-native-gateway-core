package pool

import (
	"testing"
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
