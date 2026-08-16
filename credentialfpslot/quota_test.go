package credentialfpslot

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newQuotaMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func TestAcquireWithQuotaUnlimitedWhenBelowLimit(t *testing.T) {
	_, client := newQuotaMiniredis(t)
	limit := 4
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 60, ReclaimIdleSeconds: 1800}, client)

	holder := "alice|cursor"
	lease, status := mgr.AcquireWithQuota(context.Background(), 71, &limit, holder, "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 2})
	if status != Acquired {
		t.Fatalf("status = %s, want %s", status, Acquired)
	}
	if lease == nil {
		t.Fatal("lease is nil")
	}
	if lease.CredentialID != 71 {
		t.Fatalf("lease cred = %d, want 71", lease.CredentialID)
	}

	// Second acquire by the same holder must reuse the slot without
	// increasing the per-client count.
	_, status = mgr.AcquireWithQuota(context.Background(), 71, &limit, holder, "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 2})
	if status != Acquired {
		t.Fatalf("reused status = %s, want %s", status, Acquired)
	}

	active, err := mgr.ActiveSlotCount(context.Background(), 71, "cursor")
	if err != nil {
		t.Fatalf("active count: %v", err)
	}
	if active != 1 {
		t.Fatalf("active = %d, want 1", active)
	}
}

func TestAcquireWithQuotaEnforceExceedsBlocksCandidate(t *testing.T) {
	_, client := newQuotaMiniredis(t)
	limit := 4
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 60, ReclaimIdleSeconds: 1800}, client)

	holderA := "alice|cursor"
	holderB := "bob|cursor"

	if _, status := mgr.AcquireWithQuota(context.Background(), 91, &limit, holderA, "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("first acquire status = %s", status)
	}

	_, status := mgr.AcquireWithQuota(context.Background(), 91, &limit, holderB, "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1})
	if status != ClientQuotaExceeded {
		t.Fatalf("second acquire status = %s, want %s", status, ClientQuotaExceeded)
	}
}

func TestAcquireWithQuotaShadowAllowsButStillRecords(t *testing.T) {
	_, client := newQuotaMiniredis(t)
	limit := 4
	// Shadow mode records the would-block decision but still returns
	// Acquired, leaving the active count to grow past max_fp_slots. The
	// counter is observable so operators can see the policy in action.
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 0, ReclaimIdleSeconds: 1800}, client)

	holderA := "alice|cursor"
	holderB := "bob|cursor"

	if _, status := mgr.AcquireWithQuota(context.Background(), 81, &limit, holderA, "default", Quota{Mode: QuotaModeShadow, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("first shadow acquire status = %s", status)
	}

	_, status := mgr.AcquireWithQuota(context.Background(), 81, &limit, holderB, "default", Quota{Mode: QuotaModeShadow, MaxFPSlots: 1})
	if status != Acquired {
		t.Fatalf("shadow second acquire status = %s, want %s", status, Acquired)
	}
	active, err := mgr.ActiveSlotCount(context.Background(), 81, "cursor")
	if err != nil {
		t.Fatalf("active count: %v", err)
	}
	if active < 1 {
		t.Fatalf("shadow active count = %d, want >= 1", active)
	}
}

func TestAcquireWithQuotaFailsOpenOnRedisError(t *testing.T) {
	server, client := newQuotaMiniredis(t)
	limit := 4
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 60, ReclaimIdleSeconds: 1800}, client)
	server.Close()

	lease, status := mgr.AcquireWithQuota(context.Background(), 73, &limit, "alice|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1})
	if status != RedisFailOpen {
		t.Fatalf("status = %s, want %s", status, RedisFailOpen)
	}
	if lease == nil || !lease.Unlimited {
		t.Fatalf("expected Unlimited lease on redis failure, got %+v", lease)
	}
}
