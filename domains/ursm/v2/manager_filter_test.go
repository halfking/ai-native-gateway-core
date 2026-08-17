package v2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// realRedisClient connects to the real Redis named by TEST_REDIS_URL
// (default 127.0.0.1:6379) and skips the test when it is unreachable.
// The UT-CO contract tests (会话优化 v4 测试方案 G0) must run against real
// Redis — miniredis diverges from real Redis Lua/pubsub behavior, and a
// false-green here would unfreeze the P0 gate.
func realRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_URL")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 500 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		t.Skipf("TEST_REDIS_URL (%s) unreachable, skipping real-Redis contract test: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// deadRedisClient returns a client pointed at a closed port so every
// command fails fast (connection refused). Used for the UT-CO failure
// injection branches — no real Redis needed.
func deadRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 150 * time.Millisecond,
		MaxRetries:  -1,
		ReadTimeout: 150 * time.Millisecond,
	})
}

// uniqueTestPrefix returns a per-test Redis key prefix so real-Redis
// contract tests never collide with production keys or each other.
func uniqueTestPrefix(name string) string {
	return fmt.Sprintf("ursm:v2:test:%s:%d:", name, time.Now().UnixNano())
}

func TestFilterAndScoreRedisErrorProtectsRejection(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// Use authoritative mode so FilterAndScore exercises the Redis read path.
	// Explicit ModeOff returns (nil, nil) directly; use a real mode plus an
	// empty mirror (cold start) so the miss path requires Redis and the
	// protection-rejection invariant (miss + Redis down → error) is honored.
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 0 // no mirror → every read must hit Redis
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	mr.Close() // 强制 redis 错误
	_, err := mgr.FilterAndScore(context.Background(), []CandidateSeed{{
		ProviderID: 1, CredentialID: 1, RawModel: "m", TenantID: "t",
	}})
	if err == nil {
		t.Fatalf("redis error must surface")
	}
}

func TestFilterAndScoreColdMissBackfillsSeedIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 0
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)

	seed := CandidateSeed{
		ProviderID: 17, CredentialID: 29, RawModel: "provider-model",
		Canonical: "canonical-model", TenantID: "tenant-a",
		PriceIn: 1.25, PriceOut: 2.5, BillingMode: "pay_as_you_go",
		Trust: 0.88, BaseURLMs: 123,
	}
	if err := mgr.SetSeedForTest(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	views, err := mgr.FilterAndScore(context.Background(), []CandidateSeed{seed})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views=%d, want 1", len(views))
	}
	got := views[0]
	if got.ProviderID != seed.ProviderID || got.CredentialID != seed.CredentialID || got.RawModel != seed.RawModel ||
		got.CanonicalName != seed.Canonical || got.TenantID != seed.TenantID {
		t.Fatalf("cold-miss identity not restored from seed: got=%+v seed=%+v", got, seed)
	}
	if got.PriceIn != seed.PriceIn || got.PriceOut != seed.PriceOut || got.BillingMode != seed.BillingMode ||
		got.Trust != seed.Trust || got.BaseURLMs != seed.BaseURLMs {
		t.Fatalf("cold-miss seed metadata not restored: got=%+v seed=%+v", got, seed)
	}
}

func TestFilterAndScoreColdMissKeepsProvidersDistinctForSameCredentialModel(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 0
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)

	seeds := []CandidateSeed{
		{ProviderID: 101, CredentialID: 7, RawModel: "shared", Canonical: "canon-a", TenantID: "tenant-a"},
		{ProviderID: 202, CredentialID: 7, RawModel: "shared", Canonical: "canon-b", TenantID: "tenant-a"},
	}
	if err := mgr.SetSeedForTest(context.Background(), seeds[0]); err != nil {
		t.Fatalf("seed: %v", err)
	}

	views, err := mgr.FilterAndScore(context.Background(), seeds)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("views=%d, want 2", len(views))
	}
	if views[0].ProviderID != 101 || views[0].CanonicalName != "canon-a" ||
		views[1].ProviderID != 202 || views[1].CanonicalName != "canon-b" {
		t.Fatalf("same credential/model providers collapsed or crossed: %+v", views)
	}
}

// -----------------------------------------------------------------------------
// 会话优化 v4 T0-lite — P0-1 mirror-first gear contract (spec §14.3).
// UT-CO-02 / UT-CO-03 (测试方案 G0): these are P0 gate cases and run against
// a REAL Redis (TEST_REDIS_URL, skip when unreachable). Failure injection
// swaps the store onto a dead-address client — no live Redis involved for
// the failure branch itself.
// -----------------------------------------------------------------------------

// warmMirrorOnRealRedis builds an authoritative manager with a unique key
// prefix against real Redis, seeds one node, and performs one read so the
// NodeMirror holds a fresh full hit for the seed. Returns the manager and
// the seed used.
func warmMirrorOnRealRedis(t *testing.T, softTTL time.Duration) (*Manager, CandidateSeed) {
	t.Helper()
	rdb := realRedisClient(t)
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.RedisKeyPrefix = uniqueTestPrefix("utco")
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = softTTL
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	seed := CandidateSeed{
		ProviderID: 91, CredentialID: int(time.Now().UnixNano() % 100000), RawModel: "gear-contract", TenantID: "t",
	}
	if err := mgr.SetSeedForTest(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := mgr.FilterAndScore(context.Background(), []CandidateSeed{seed}); err != nil {
		t.Fatalf("warm-up read: %v", err)
	}
	if _, ok := mgr.nodeMirror.PeekForTenant(seed.TenantID, seed.CredentialID, seed.RawModel); !ok {
		t.Fatalf("mirror must be warm after the first read")
	}
	return mgr, seed
}

// TestUT_CO_02_ReadyGateNotBypassedByMirror pins the afb13c9ea regression:
// a false readiness snapshot must ALWAYS produce an error — including the
// "every seed is a fresh full mirror hit" scenario that used to short-circuit
// before the gate.
func TestUT_CO_02_ReadyGateNotBypassedByMirror(t *testing.T) {
	mgr, seed := warmMirrorOnRealRedis(t, time.Minute)
	seeds := []CandidateSeed{seed}

	// Full mirror hit + ready=false → error, even though Redis is alive and
	// the mirror alone could answer.
	_, _, err := mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, false)
	if err == nil {
		t.Fatalf("ready=false must reject even with a full fresh mirror hit")
	}
	if want := "not ready"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error must carry %q, got %v", want, err)
	}

	// Same scenario with Redis dead (dead-address store swap): still the
	// ready-gate rejection — the gate fires before any Redis IO.
	mgr.SetRedisForTest(deadRedisClient())
	_, _, err = mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, false)
	if err == nil {
		t.Fatalf("ready=false must reject with full mirror hit and dead Redis")
	}
	if want := "not ready"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error must carry %q (ready gate precedes Redis IO), got %v", want, err)
	}
}

// TestUT_CO_03_DefaultGearRejectsFullMirrorHitWhenRedisDown pins the §14.3
// target gear: ready=true + Redis unreachable + FULL mirror hit → protective
// rejection (empty candidates + error). This closes the P0-1 residual where
// a warm mirror kept routing while Redis was down.
func TestUT_CO_03_DefaultGearRejectsFullMirrorHitWhenRedisDown(t *testing.T) {
	mgr, seed := warmMirrorOnRealRedis(t, time.Minute)
	mgr.SetRedisForTest(deadRedisClient())

	views, src, err := mgr.FilterAndScoreReadyWithSource(context.Background(), []CandidateSeed{seed}, true)
	if err == nil {
		t.Fatalf("default gear: Redis down + full mirror hit must protectively reject, got views=%v", views)
	}
	if len(views) != 0 {
		t.Fatalf("protective rejection must return zero candidates, got %d", len(views))
	}
	if src != "" {
		t.Fatalf("no routing source may be recorded on a protective rejection, got %q", src)
	}
	if !errors.Is(err, store.ErrRedisUnavailable) {
		t.Fatalf("error must wrap store.ErrRedisUnavailable so callers errors.Is it, got %v", err)
	}
}

// TestUT_CO_03_GraceGearServesSoftTTLMirrorHit pins the §14.3 optional gear:
// with llmgw_ursm_mirror_grace_enabled=true, a FULL mirror hit within the
// soft TTL serves degraded read-only routing with NO Redis IO and reports
// StateSourceNodeMirrorHit; once the entry soft-expires the call falls back
// to the Redis read path and is rejected when that path fails (过期即拒).
func TestUT_CO_03_GraceGearServesSoftTTLMirrorHit(t *testing.T) {
	mgr, seed := warmMirrorOnRealRedis(t, 250*time.Millisecond)
	mgr.SetHotConfig(stubHotSource{bools: map[string]bool{HotKeyMirrorGrace: true}})

	// Fresh entry (within soft TTL): served from the mirror with zero Redis
	// IO — prove it by pointing the store at a dead client; the call must
	// still succeed and report the mirror-hit source.
	mgr.SetRedisForTest(deadRedisClient())
	views, src, err := mgr.FilterAndScoreReadyWithSource(context.Background(), []CandidateSeed{seed}, true)
	if err != nil {
		t.Fatalf("grace gear: fresh mirror hit must serve without Redis IO, got %v", err)
	}
	if len(views) != 1 || !views[0].Available {
		t.Fatalf("grace gear: expected 1 available mirror view, got %+v", views)
	}
	if src != statesource.StateSourceNodeMirrorHit {
		t.Fatalf("grace gear source = %q, want %q", src, statesource.StateSourceNodeMirrorHit)
	}

	// Soft-TTL expiry: the mirror entry goes stale → the Redis read path
	// must run; against the dead client that path rejects (过期即拒).
	time.Sleep(300 * time.Millisecond)
	_, src, err = mgr.FilterAndScoreReadyWithSource(context.Background(), []CandidateSeed{seed}, true)
	if err == nil {
		t.Fatalf("grace gear: soft-expired entry must fall back to Redis and be rejected when Redis is down")
	}
	if src != "" {
		t.Fatalf("no source may be recorded on rejection, got %q", src)
	}
	if !errors.Is(err, store.ErrRedisUnavailable) {
		t.Fatalf("soft-expiry rejection must wrap store.ErrRedisUnavailable, got %v", err)
	}
}
