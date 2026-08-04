package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestApplyAdminTargetsTenantAndInvalidatesMirror(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	mgr.nodeMirror.ApplyFromAPI(api.NodeView{
		TenantID: "tenant-a", CredentialID: 1, RawModel: "m", Available: true, Generation: 1,
	})
	disabled := true
	if err := mgr.ApplyAdmin(context.Background(), api.AdminAction{
		TenantID: "tenant-a", CredentialID: 1, RawModel: "m", ManualDisabled: &disabled, Actor: "admin", IssuedAtMs: 1,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := mgr.nodeMirror.PeekForTenant("tenant-a", 1, "m"); ok {
		t.Fatal("admin write must immediately invalidate local mirror entry")
	}
	if got := mr.HGet("ursm:v2:node:tenant-a:1:m", "manual_hold"); got != "1" {
		t.Fatalf("tenant key manual_hold=%q, want 1", got)
	}
	if mr.Exists("ursm:v2:node:1:m") {
		t.Fatal("tenant action must not mutate legacy non-tenant key")
	}
}

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
		TenantID: "t", CredentialID: 1, RawModel: "m", ManualDisabled: &disabled, Actor: "admin", IssuedAtMs: 1,
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
