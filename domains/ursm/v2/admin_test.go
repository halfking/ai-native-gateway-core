package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestApplyAdminSetsManualHold(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	// DefaultConfig() is ModeOff, which short-circuits FilterAndScore.
	// Flip to Canary with 100% so the read path actually runs and
	// observes the manual_hold the script just wrote.
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	disabled := true
	if err := mgr.ApplyAdmin(context.Background(), api.AdminAction{
		CredentialID: 1, RawModel: "m", ManualDisabled: &disabled, Actor: "admin", IssuedAtMs: 1,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	views, _ := mgr.FilterAndScore(context.Background(), []CandidateSeed{{
		ProviderID: 1, CredentialID: 1, RawModel: "m", TenantID: "t",
	}})
	if len(views) == 0 || views[0].Available {
		t.Fatalf("manual hold must mark unavailable")
	}
}
