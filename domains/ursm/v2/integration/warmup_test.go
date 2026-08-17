// Package integration exercises the cross-package v2 path end to end:
// recovery.WarmupFromSeed → Manager.Ready → Manager.FilterAndScore.
//
// The test stands up an in-process miniredis (no real network), seeds
// a node via the recovery warmup, then asks the Manager to resolve
// that node through the regular read pipeline.
//
// The test uses ModeCanary with CanaryPercent: 100 to exercise cohort
// planning as well as the normal read path.
package integration

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery"
)

func TestWarmupThenFilter(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// Flip ModeOff → ModeCanary so FilterAndScore does not short-circuit.
	cfg := v2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100

	mgr := v2.New(v2.Dependencies{Redis: rdb, Config: cfg})
	rm := recovery.New(rdb, "ursm:v2:")

	if err := rm.WarmupFromSeed(context.Background(), []recovery.Seed{
		{ProviderID: 1, CredentialID: 1, RawModel: "m", TenantID: "t"},
	}); err != nil {
		t.Fatalf("warmup: %v", err)
	}

	if !mgr.Ready(context.Background()) {
		t.Fatalf("manager must be ready after warmup")
	}

	views, err := mgr.FilterAndScore(context.Background(), []v2.CandidateSeed{
		{ProviderID: 1, CredentialID: 1, RawModel: "m", TenantID: "t"},
	})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	if !views[0].Available {
		t.Fatalf("warmup must produce an available candidate, got view=%+v", views[0])
	}
	if views[0].CredentialID != 1 || views[0].RawModel != "m" {
		t.Fatalf("view resolved wrong node: %+v", views[0])
	}
}
