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
	// Use Canary with 100% so this test exercises cohort planning and
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

// TestClearStateForTenant_MissingKeyIsSuccess verifies that clearing state for
// a node URSM v2 has never seen (no Redis key) returns nil instead of an
// error. Emergency-repair force_enable/clear_circuit call this for freshly
// added or idle nodes; a hard error previously caused the routing-v2 UI to
// warn "URSM 状态未清理" even though PostgreSQL was updated correctly.
func TestClearStateForTenant_MissingKeyIsSuccess(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)

	// No prior write — the node key does not exist in Redis.
	if mr.Exists("ursm:v2:node:t:9:never-seen") {
		t.Fatal("precondition: key must not exist")
	}
	if err := mgr.ClearStateForTenant(context.Background(), "t", 9, "never-seen"); err != nil {
		t.Fatalf("clear on missing key must succeed, got %v", err)
	}
}
