package credentialfpslot

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// R56 audit: the B14 watchdog must actually FREE the concurrency slot.
// The ordinary Release intentionally keeps the slot key alive (EXPIRE
// refresh, fingerprint-identity keepalive), so routing the watchdog through
// it left the slot counted as occupied and re-armed a fresh full TTL —
// the "forced release" freed nothing. ReleaseHard deletes the key.
func TestLeaseReleaseHardDeletesSlotKey(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()

	m := New(Config{Enabled: true, DefaultLimit: 2}, client)
	ctx := context.Background()

	lease, ok := m.Acquire(ctx, 900, intPtr(1), "holder-hard", "tenant-hard")
	if !ok || lease == nil {
		t.Fatal("Acquire failed")
	}
	key := tenantSlotRedisKey("tenant-hard", lease.CredentialID, lease.SlotIndex)
	if !mr.Exists(key) {
		t.Fatal("slot key must exist while held")
	}

	// Ordinary Release keeps the key (keepalive semantics)…
	m2 := New(Config{Enabled: true, DefaultLimit: 2}, client)
	l2, ok := m2.Acquire(ctx, 900, intPtr(2), "holder-soft", "tenant-hard")
	if !ok || l2 == nil {
		t.Fatal("second Acquire failed")
	}
	m2.Release(ctx, l2)
	softKey := tenantSlotRedisKey("tenant-hard", l2.CredentialID, l2.SlotIndex)
	if !mr.Exists(softKey) {
		t.Fatal("ordinary release must keep the slot key (keepalive)")
	}

	// …while ReleaseHard deletes it.
	if !m.ReleaseHard(ctx, lease) {
		t.Fatal("ReleaseHard must report performed when it deletes its own slot")
	}
	if mr.Exists(key) {
		t.Fatal("ReleaseHard must delete the slot key")
	}
	if !lease.Released() {
		t.Fatal("ReleaseHard must flip released=true")
	}

	// Idempotent + post-release calls report not-performed (no breach count).
	if m.ReleaseHard(ctx, lease) {
		t.Fatal("ReleaseHard on a released lease must be a no-op")
	}
}

// The breach attribution contract: when the slot was already freed by
// someone else (expired / preempted), ReleaseHard reports not-performed so
// the watchdog does not count it as a hard-cap breach.
func TestLeaseReleaseHardNotPerformedWhenSlotGone(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()

	m := New(Config{Enabled: true, DefaultLimit: 2}, client)
	ctx := context.Background()

	lease, ok := m.Acquire(ctx, 900, intPtr(1), "holder-gone", "tenant-gone")
	if !ok || lease == nil {
		t.Fatal("Acquire failed")
	}
	key := tenantSlotRedisKey("tenant-gone", lease.CredentialID, lease.SlotIndex)
	mr.Del(key) // simulate natural TTL expiry / takeover

	if m.ReleaseHard(ctx, lease) {
		t.Fatal("ReleaseHard must report not-performed when the slot is already gone")
	}
	if !lease.Released() {
		t.Fatal("lease must still be marked released (no watchdog retry)")
	}
}
