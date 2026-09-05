package credentialfpslot

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

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

// A metadata mapping whose physical slot key was deleted out-of-band
// (reclaim, reset) must be pruned so the same client type is not blocked
// forever by a ghost count.
func TestAcquireWithQuotaPrunesStaleMetadataWhenPhysicalSlotMissing(t *testing.T) {
	_, client := newQuotaMiniredis(t)
	limit := 4
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 60, ReclaimIdleSeconds: 1800}, client)
	ctx := context.Background()

	lease, status := mgr.AcquireWithQuota(ctx, 95, &limit, "alice|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1})
	if status != Acquired {
		t.Fatalf("first acquire status = %s, want %s", status, Acquired)
	}

	// Simulate reclaim/reset deleting the physical slot key while the
	// credential-global metadata hash keeps the mapping.
	slotKey := fmt.Sprintf("%s:%d", tenantSlotRedisPrefix("default", 95), lease.SlotIndex)
	if err := client.Del(ctx, slotKey).Err(); err != nil {
		t.Fatalf("del physical slot key: %v", err)
	}

	// The stale same-client-type mapping must be pruned, not block enforce.
	if _, status := mgr.AcquireWithQuota(ctx, 95, &limit, "bob|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("second acquire status = %s, want %s (stale metadata should be pruned)", status, Acquired)
	}

	active, err := mgr.ActiveSlotCount(ctx, 95, "cursor")
	if err != nil {
		t.Fatalf("active count: %v", err)
	}
	if active != 1 {
		t.Fatalf("active = %d, want 1", active)
	}
}

// Pruning is per-mapping: removing one client type's physical slot must
// free that type's quota without disturbing another type's live mapping.
func TestAcquireWithQuotaPruneOnlyRemovesMissingPhysicalSlots(t *testing.T) {
	_, client := newQuotaMiniredis(t)
	limit := 4
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 60, ReclaimIdleSeconds: 1800}, client)
	ctx := context.Background()

	codexLease, status := mgr.AcquireWithQuota(ctx, 96, &limit, "carol|codex", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1})
	if status != Acquired {
		t.Fatalf("codex acquire status = %s, want %s", status, Acquired)
	}
	if _, status := mgr.AcquireWithQuota(ctx, 96, &limit, "alice|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("cursor acquire status = %s, want %s", status, Acquired)
	}

	// Delete only the codex physical slot; the cursor mapping stays live.
	codexKey := fmt.Sprintf("%s:%d", tenantSlotRedisPrefix("default", 96), codexLease.SlotIndex)
	if err := client.Del(ctx, codexKey).Err(); err != nil {
		t.Fatalf("del codex slot key: %v", err)
	}

	// cursor quota (still at max) keeps blocking a second holder.
	if _, status := mgr.AcquireWithQuota(ctx, 96, &limit, "bob|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != ClientQuotaExceeded {
		t.Fatalf("cursor second acquire status = %s, want %s", status, ClientQuotaExceeded)
	}
	// codex slot freed out-of-band → its ghost mapping is pruned and a new
	// codex holder is admitted.
	if _, status := mgr.AcquireWithQuota(ctx, 96, &limit, "dave|codex", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("codex second acquire status = %s, want %s", status, Acquired)
	}
}

// A live physical slot keeps its metadata mapping even when the recorded
// exp has passed: dropping it would under-count the client type and let an
// enforce acquire bypass max_fp_slots.
func TestAcquireWithQuotaKeepsExpiredMetadataWhilePhysicalSlotLives(t *testing.T) {
	_, client := newQuotaMiniredis(t)
	limit := 4
	// The gate must exceed any idle time so the preempt path cannot mask
	// the count check below.
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 9999, ReclaimIdleSeconds: 1800}, client)
	ctx := context.Background()

	lease, status := mgr.AcquireWithQuota(ctx, 97, &limit, "alice|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1})
	if status != Acquired {
		t.Fatalf("first acquire status = %s, want %s", status, Acquired)
	}

	// Backdate the metadata expiry while the physical slot key remains.
	slotKey := fmt.Sprintf("%s:%d", tenantSlotRedisPrefix("default", 97), lease.SlotIndex)
	past := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	if err := client.HSet(ctx, globalMetadataKey(97), slotKey+":exp", past).Err(); err != nil {
		t.Fatalf("backdate exp: %v", err)
	}

	// The live physical slot keeps the count honest: enforce still blocks a
	// second holder instead of admitting over-limit.
	if _, status := mgr.AcquireWithQuota(ctx, 97, &limit, "bob|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != ClientQuotaExceeded {
		t.Fatalf("second acquire status = %s, want %s (live mapping must not be pruned)", status, ClientQuotaExceeded)
	}
}

// Natural TTL expiry removes the physical slot key; the next enforce
// acquire must prune the leftover metadata and succeed.
func TestAcquireWithQuotaRecoversAfterNaturalExpiry(t *testing.T) {
	server, client := newQuotaMiniredis(t)
	limit := 4
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 60, ReclaimIdleSeconds: 1800}, client)
	ctx := context.Background()

	if _, status := mgr.AcquireWithQuota(ctx, 98, &limit, "alice|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("first acquire status = %s, want %s", status, Acquired)
	}

	// Let the physical slot TTL lapse.
	server.FastForward(time.Duration(slotTTLSeconds+60) * time.Second)

	if _, status := mgr.AcquireWithQuota(ctx, 98, &limit, "bob|cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("post-expiry acquire status = %s, want %s", status, Acquired)
	}
}

// ActiveSlotCount normalizes the client type with the same whitelist as
// the acquire path, so raw token forms address the same counter.
func TestActiveSlotCountNormalizesClientType(t *testing.T) {
	_, client := newQuotaMiniredis(t)
	limit := 4
	mgr := New(Config{Enabled: true, DefaultLimit: 4, ActiveGateSeconds: 60, ReclaimIdleSeconds: 1800}, client)
	ctx := context.Background()

	// Holder token carries the mixed-case form; the acquire path records
	// under the normalized "cursor".
	if _, status := mgr.AcquireWithQuota(ctx, 99, &limit, "alice|Cursor", "default", Quota{Mode: QuotaModeEnforce, MaxFPSlots: 1}); status != Acquired {
		t.Fatalf("acquire status = %s, want %s", status, Acquired)
	}

	lower, err := mgr.ActiveSlotCount(ctx, 99, "cursor")
	if err != nil {
		t.Fatalf("active count (lower): %v", err)
	}
	mixed, err := mgr.ActiveSlotCount(ctx, 99, "Cursor")
	if err != nil {
		t.Fatalf("active count (mixed): %v", err)
	}
	if lower != 1 || mixed != 1 {
		t.Fatalf("active = (%d, %d), want (1, 1)", lower, mixed)
	}

	// Unrecognized types collapse to "unknown", which has no slots here.
	unknown, err := mgr.ActiveSlotCount(ctx, 99, "definitely-not-a-client")
	if err != nil {
		t.Fatalf("active count (unknown): %v", err)
	}
	if unknown != 0 {
		t.Fatalf("unknown active = %d, want 0", unknown)
	}
}
