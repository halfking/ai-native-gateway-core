package credentialfpslot

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Wave 3 B14 read-side: Released() must flip only through the real release
// path so the dispatch watchdog can distinguish a live hold from a queued
// normal release.
func TestLeaseReleasedTracksReleasePath(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()

	m := New(Config{Enabled: true, DefaultLimit: 2}, client)
	ctx := context.Background()

	lease, ok := m.Acquire(ctx, 900, intPtr(1), "holder-b14", "tenant-b14")
	if !ok || lease == nil {
		t.Fatal("Acquire failed")
	}
	if lease.Released() {
		t.Fatal("fresh lease must not read as released")
	}

	m.Release(ctx, lease)
	if !lease.Released() {
		t.Fatal("released lease must read as released")
	}

	// Idempotency: a second Release stays a no-op and keeps the flag true.
	m.Release(ctx, lease)
	if !lease.Released() {
		t.Fatal("second release must keep released=true")
	}
}
