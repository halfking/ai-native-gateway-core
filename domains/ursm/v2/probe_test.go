package v2

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestApplyProbeSuccessUpdatesAvailable(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	_ = mgr.SetReady(context.Background(), true)
	if err := mgr.ApplyProbe(context.Background(), api.ProbeOutcome{
		CredentialID: 1, RawModel: "m", Success: true,
	}); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

func TestApplyProbeRefreshesNodeTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.NodeTTL = time.Hour
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	ctx := context.Background()
	if err := mgr.ApplyProbe(ctx, api.ProbeOutcome{CredentialID: 1, RawModel: "m", Success: true}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	ttl, err := rdb.TTL(ctx, "ursm:v2:node:1:m").Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 || ttl > time.Hour {
		t.Fatalf("probe node ttl=%s, want positive value no greater than configured hour", ttl)
	}
}

func TestApplyProbeRespectsExistingAdminHold(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	_ = mgr.SetReady(context.Background(), true)
	ctx := context.Background()
	// Seed a manual_hold="1" on the target node first.
	if err := rdb.HSet(ctx, "ursm:v2:node:1:m",
		"manual_hold", "1", "source_priority", "40",
	).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := mgr.ApplyProbe(ctx, api.ProbeOutcome{
		CredentialID: 1, RawModel: "m", Success: true, LatencyMs: 100,
	}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	// After probe with admin_hold present, last_probe_at_ms should NOT have been written.
	v, err := rdb.HGet(ctx, "ursm:v2:node:1:m", "last_probe_at_ms").Result()
	if err != nil && err != redis.Nil {
		t.Fatalf("hget: %v", err)
	}
	if v != "" {
		t.Fatalf("probe must not write fields while admin_hold=1, got last_probe_at_ms=%q", v)
	}
}
