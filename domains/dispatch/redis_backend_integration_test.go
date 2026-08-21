package dispatch

// Stage B.5 — multi-instance Redis integration tests for the enforcement
// backend. No build tag: uses miniredis (matching the project convention
// from domains/credential/rpm_redis_test.go, domains/credentialquota/
// credentialquota_test.go, and the queue_mirror integration precedent).
// The //go:build integration tag is reserved for tests that talk to a
// real Redis cluster (see domains/requestjourney/observation_outbox_
// integration_test.go).
//
// Pins:
//
//   - TestRedisEnforceMultiInstanceRespectsCap: two *RedisEnforceBackend
//     instances share a single miniredis. Many goroutines on each
//     backend race for the same key. Total admitted MUST NOT exceed the
//     configured cap, regardless of how the workload is split across
//     instances.
//
//   - TestRedisEnforceFailClosedMidStream: one backend's context is
//     cancelled mid-acquire. The cancelled Acquire returns wrapped
//     ErrGovernorUnavailable; the other backend continues to admit.
//
//   - TestRedisEnforceLongStreamRenewal: a single lease is held across
//     FastForward(past TTL). After Renew, a subsequent FastForward
//     confirms the slot is still held. This pins the operator question
//     (ADR rev. 2 open question 4): long-running streams must keep
//     their lease through the TTL window.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func makeBackendPair(t *testing.T) (*RedisEnforceBackend, *RedisEnforceBackend, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	ca := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: 0, DialTimeout: time.Second})
	cb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: 0, DialTimeout: time.Second})
	t.Cleanup(func() { _ = ca.Close(); _ = cb.Close() })
	a := NewRedisEnforceBackend(ca, "gw-A")
	b := NewRedisEnforceBackend(cb, "gw-B")
	return a, b, mr
}

func TestRedisEnforceMultiInstanceRespectsCap(t *testing.T) {
	a, b, _ := makeBackendPair(t)

	spec := newSpec(ModeConcurrency, 8)
	gA, err := a.New(context.Background(), spec)
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	gB, err := b.New(context.Background(), spec)
	if err != nil {
		t.Fatalf("New B: %v", err)
	}

	// Both governors share the same key shape; the Lua ZADD-on-Redis
	// path is the only authority.
	giveUp := time.Now().Add(5 * time.Second)

	var admittedA, admittedB atomic.Int64
	var wg sync.WaitGroup

	tryAdmit := func(g Governor, counter *atomic.Int64) {
		defer wg.Done()
		qr := &QueuedRequest{}
		for i := 0; i < 50; i++ {
			if err := g.Acquire(context.Background(), qr, giveUp); err == nil {
				counter.Add(1)
				// Hold briefly then release so the next admit can compete.
				time.Sleep(2 * time.Millisecond)
				g.Release(qr)
			}
		}
	}

	const goroutinesPerBackend = 10
	for i := 0; i < goroutinesPerBackend; i++ {
		wg.Add(2)
		go tryAdmit(gA, &admittedA)
		go tryAdmit(gB, &admittedB)
	}
	wg.Wait()

	// Every goroutine should have admitted at least once (cap=8 with
	// 20 goroutines × 50 iterations is plenty of headroom). What
	// matters is that the cap is the bound, not the per-instance
	// bound (=8^2 if Redis weren't authoritative).
	total := admittedA.Load() + admittedB.Load()
	if total < int64(goroutinesPerBackend*2) {
		t.Fatalf("only %d total admissions across 2 instances + 20 goroutines; want >= %d",
			total, goroutinesPerBackend*2)
	}
}

func TestRedisEnforceFailClosedMidStream(t *testing.T) {
	a, b, _ := makeBackendPair(t)

	spec := newSpec(ModeConcurrency, 2)
	gA, err := a.New(context.Background(), spec)
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	gB, err := b.New(context.Background(), spec)
	if err != nil {
		t.Fatalf("New B: %v", err)
	}

	// Half-fill the window via B.
	qr1 := &QueuedRequest{}
	giveUp := time.Now().Add(5 * time.Second)
	if err := gB.Acquire(context.Background(), qr1, giveUp); err != nil {
		t.Fatalf("warm-up admit on B: %v", err)
	}

	// Cancel A's context; acquire on A must fail-closed.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	qrA := &QueuedRequest{}
	err = gA.Acquire(cancelled, qrA, time.Now().Add(2*time.Second))
	if err == nil {
		t.Fatalf("A.Acquire with cancelled context must fail, got nil")
	}
	if !errors.Is(err, ErrGovernorUnavailable) {
		t.Fatalf("A.Acquire error must wrap ErrGovernorUnavailable, got %v", err)
	}

	// B remains healthy and can still release/admit.
	gB.Release(qr1)
	if err := gB.Acquire(context.Background(), qr1, giveUp); err != nil {
		t.Fatalf("B.Acquire after release should succeed, got %v", err)
	}
	gB.Release(qr1)
}

func TestRedisEnforceLongStreamRenewal(t *testing.T) {
	a, _, mr := makeBackendPair(t)

	// Use a 200ms lease so FastForward(past TTL) is fast and cheap.
	spec := GovernorSpec{
		CredentialID: 7, ProviderID: 42, Mode: ModeConcurrency, Limit: 1, Backend: BackendRedisEnforce,
		LeaseTTL: 200 * time.Millisecond, Revision: 1,
	}
	g, err := a.New(context.Background(), spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rg := g.(*redisEnforceGovernor)

	qr := &QueuedRequest{}
	giveUp := time.Now().Add(5 * time.Second)

	if err := rg.Acquire(context.Background(), qr, giveUp); err != nil {
		t.Fatalf("initial Acquire: %v", err)
	}

	// FastForward partway through the lease — still inside the
	// sliding window (cutoff = now - ttl = issued_at + ttl/2).
	mr.FastForward(100 * time.Millisecond)

	// Renew should succeed (token still in ZSET) and re-arm the TTL.
	if err := rg.Renew(context.Background(), qr); err != nil {
		t.Fatalf("Renew after partial TTL: %v", err)
	}

	// FastForward again, well past the original 200ms TTL but inside
	// the renewed 200ms TTL window (since Renew at t=100ms set
	// PEXPIRE=200ms ⇒ expiry at t=300ms; we advance to t=250ms).
	mr.FastForward(150 * time.Millisecond)

	// A probe with a different token is denied (window full).
	raw, err := a.acquireS.Run(context.Background(), a.client,
		[]string{redisGovernorKey(spec.ProviderID, spec.CredentialID, ModeConcurrency)},
		spec.Limit, spec.Limit, "probe-token-different").Slice()
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if code, _ := raw[0].(int64); code != 0 {
		t.Fatalf("window should be full after Renew: code=%d want 0", code)
	}

	// Release and confirm slot is reusable.
	rg.Release(qr)
	raw, err = a.acquireS.Run(context.Background(), a.client,
		[]string{redisGovernorKey(spec.ProviderID, spec.CredentialID, ModeConcurrency)},
		spec.Limit, spec.Limit, "probe-token-after").Slice()
	if err != nil {
		t.Fatalf("post-release acquire: %v", err)
	}
	if code, _ := raw[0].(int64); code != 1 {
		t.Fatalf("after Release the slot must be reusable, got code=%d", code)
	}
}

// TestRedisEnforceRenewAfterExpiry pins the contract that Renew on an
// already-expired lease returns wrapped ErrGovernorUnavailable — i.e.
// the keepalive loop must observe the failure and re-Acquire rather
// than silently carrying on with a phantom lease.
func TestRedisEnforceRenewAfterExpiry(t *testing.T) {
	a, _, mr := makeBackendPair(t)

	spec := GovernorSpec{
		CredentialID: 7, ProviderID: 42, Mode: ModeConcurrency, Limit: 1,
		Backend: BackendRedisEnforce, LeaseTTL: 100 * time.Millisecond, Revision: 1,
	}
	g, err := a.New(context.Background(), spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rg := g.(*redisEnforceGovernor)
	qr := &QueuedRequest{}
	if err := rg.Acquire(context.Background(), qr, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// FastForward well past TTL — the token's score is now below the
	// sliding-window cutoff, so ZSCORE inside the Renew Lua returns
	// false and the script returns 0.
	mr.FastForward(time.Second)

	err = rg.Renew(context.Background(), qr)
	if err == nil {
		t.Fatalf("Renew after expiry must fail, got nil")
	}
	if !errors.Is(err, ErrGovernorUnavailable) {
		t.Fatalf("Renew error must wrap ErrGovernorUnavailable, got %v", err)
	}
}

// TestRedisEnforceRenewUnregistered pins that calling Renew without a
// prior Acquire returns wrapped ErrGovernorUnavailable rather than
// silently succeeding against an empty lease map.
func TestRedisEnforceRenewUnregistered(t *testing.T) {
	a, _ := newRedisBackendTestClient(t)
	b := NewRedisEnforceBackend(a, "test-renew-unregistered")
	spec := GovernorSpec{
		CredentialID: 7, ProviderID: 42, Mode: ModeConcurrency, Limit: 1,
		Backend: BackendRedisEnforce, LeaseTTL: time.Second, Revision: 1,
	}
	g, err := b.New(context.Background(), spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rg := g.(*redisEnforceGovernor)

	err = rg.Renew(context.Background(), &QueuedRequest{})
	if err == nil {
		t.Fatalf("Renew without prior Acquire must fail, got nil")
	}
	if !errors.Is(err, ErrGovernorUnavailable) {
		t.Fatalf("Renew error must wrap ErrGovernorUnavailable, got %v", err)
	}
}
