package v2

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestFilterAndScoreReadyGatePrecedesEmptyTenant(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 32
	mgr := New(Dependencies{Redis: deadRedisClient(), Config: cfg})
	t.Cleanup(mgr.Close)

	views, source, err := mgr.FilterAndScoreReadyWithSource(context.Background(), []CandidateSeed{{
		ProviderID: 1, CredentialID: 7, RawModel: "model-a",
	}}, false)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("ready=false must win over empty tenant, got views=%v source=%q err=%v", views, source, err)
	}
	if len(views) != 0 || source != "" {
		t.Fatalf("ready-gate rejection must not return views or source: views=%v source=%q", views, source)
	}
}

func TestFilterAndScoreRejectsEmptyTenantBeforeMirrorOrRedis(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 32
	mgr := New(Dependencies{Redis: deadRedisClient(), Config: cfg})
	t.Cleanup(mgr.Close)

	views, source, err := mgr.FilterAndScoreReadyWithSource(context.Background(), []CandidateSeed{{
		ProviderID: 1, CredentialID: 7, RawModel: "model-a",
	}}, true)
	if err == nil || !strings.Contains(err.Error(), "tenant_id is required") {
		t.Fatalf("empty tenant must reject before routing, got views=%v source=%q err=%v", views, source, err)
	}
	if len(views) != 0 || source != "" {
		t.Fatalf("empty tenant rejection must not return views or source: views=%v source=%q", views, source)
	}
}

func TestNonAuthoritativeFilterAllowsLegacyEmptyTenant(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeShadow
	cfg.LRUMirrorSize = 0
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)

	views, err := mgr.FilterAndScoreReady(context.Background(), []CandidateSeed{{
		ProviderID: 1, CredentialID: 7, RawModel: "model-a",
	}}, true)
	if err != nil || len(views) != 1 {
		t.Fatalf("shadow empty-tenant compatibility = views=%+v err=%v", views, err)
	}
}

func TestManagerUsesConfiguredNodeMirrorPrefix(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.RedisKeyPrefix = "contract:manager:"
	cfg.LRUMirrorSize = 8
	mgr := New(Dependencies{Config: cfg})
	t.Cleanup(mgr.Close)
	if mgr.nodeMirror == nil || mgr.nodeMirror.Prefix() != cfg.RedisKeyPrefix {
		t.Fatalf("manager mirror prefix = %q, want %q", mgr.nodeMirror.Prefix(), cfg.RedisKeyPrefix)
	}
}

func TestFilterAndScoreTenantIsolation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 32
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("set ready: %v", err)
	}

	tenantA := CandidateSeed{ProviderID: 1, CredentialID: 7, RawModel: "model-a", TenantID: "tenant-a"}
	tenantB := CandidateSeed{ProviderID: 1, CredentialID: 7, RawModel: "model-a", TenantID: "tenant-b"}
	if err := mgr.SetSeedForTest(context.Background(), tenantA); err != nil {
		t.Fatalf("seed tenant-a: %v", err)
	}

	views, err := mgr.FilterAndScore(context.Background(), []CandidateSeed{tenantB})
	if err != nil {
		t.Fatalf("score tenant-b: %v", err)
	}
	if len(views) != 1 || views[0].Available {
		t.Fatalf("tenant-b must not inherit tenant-a state: %+v", views)
	}
	if _, ok := mgr.nodeMirror.GetForTenant("tenant-b", 7, "model-a"); !ok {
		t.Fatal("tenant-b lookup should populate only its own unavailable mirror entry")
	}
	if _, ok := mgr.nodeMirror.GetForTenant("tenant-a", 7, "model-a"); ok {
		t.Fatal("tenant-a must not appear in mirror before tenant-a is read")
	}
}

func TestRecordRequestAuthoritativeRejectsEmptyTenant(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)

	err := mgr.RecordRequest(context.Background(), api.RequestOutcome{
		CredentialID: 7, RawModel: "model-a", RequestID: "request-a", Success: true,
	})
	if err == nil || !strings.Contains(err.Error(), "tenant_id is required") {
		t.Fatalf("authoritative empty tenant write must reject, got %v", err)
	}
	if mr.Exists("ursm:v2:node:7:model-a") {
		t.Fatal("authoritative empty tenant write must not create the legacy key")
	}
}
