package v2

import (
	"context"
	"math"
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

func TestScorePreferHigherSuccessRateWhenLowerScoreWins(t *testing.T) {
	views := []api.NodeView{
		{ProviderID: 1, CredentialID: 1, RawModel: "m", SR5m: 0.99, Samples5m: 20},
		{ProviderID: 2, CredentialID: 2, RawModel: "m", SR5m: 0.60, Samples5m: 20},
	}
	seeds := []CandidateSeed{
		{ProviderID: 1, CredentialID: 1, RawModel: "m", BaseURLMs: 100},
		{ProviderID: 2, CredentialID: 2, RawModel: "m", BaseURLMs: 100},
	}

	scoreAndSort(views, seeds, DefaultScoringWeights())

	if views[0].ProviderID != 1 {
		t.Fatalf("higher success rate must rank first under ascending score: %+v", views)
	}
	if views[0].Score >= views[1].Score {
		t.Fatalf("higher success rate must have lower score, got %f vs %f", views[0].Score, views[1].Score)
	}
}

func TestScoreSR5mMissingAndOutOfRangeUseSafePenalty(t *testing.T) {
	tests := []struct {
		name    string
		sr5m    float64
		samples int
		want    float64
	}{
		{name: "missing", sr5m: 0, samples: 0, want: 0.5},
		{name: "below_zero", sr5m: -0.25, samples: 10, want: 0},
		{name: "above_one", sr5m: 1.25, samples: 10, want: 1},
		{name: "nan", sr5m: math.NaN(), samples: 10, want: 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			views := []api.NodeView{{SR5m: tt.sr5m, Samples5m: tt.samples}}
			seeds := []CandidateSeed{{}}
			scoreAndSort(views, seeds, ScoringWeights{Stability: 1})
			wantScore := (1 - tt.want) * 1000
			if math.Abs(views[0].Score-wantScore) > 1e-9 {
				t.Fatalf("score=%f, want %f for sr5m=%v samples=%d", views[0].Score, wantScore, tt.sr5m, tt.samples)
			}
		})
	}
}
