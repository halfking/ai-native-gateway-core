package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestScorePreferLowerPriceAndLatency(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	// Use Canary with 100% to exercise cohort planning and the read + score path.
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	_ = mgr.SetSeedForTest(context.Background(), CandidateSeed{ProviderID: 1, CredentialID: 1, RawModel: "a", TenantID: "t", PriceIn: 1, PriceOut: 1})
	_ = mgr.SetSeedForTest(context.Background(), CandidateSeed{ProviderID: 1, CredentialID: 2, RawModel: "a", TenantID: "t", PriceIn: 100, PriceOut: 100})
	views, err := mgr.FilterAndScore(context.Background(), []CandidateSeed{
		{ProviderID: 1, CredentialID: 1, RawModel: "a", TenantID: "t", PriceIn: 1, PriceOut: 1},
		{ProviderID: 1, CredentialID: 2, RawModel: "a", TenantID: "t", PriceIn: 100, PriceOut: 100},
	})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(views) < 2 {
		t.Fatalf("expected 2 views, got %d", len(views))
	}
	if views[0].Score >= views[1].Score {
		t.Fatalf("lower-price candidate must score better, got %f vs %f", views[0].Score, views[1].Score)
	}
}
