package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

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
