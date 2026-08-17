package v2

import (
	"context"
	"fmt"
	"os"
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

// -----------------------------------------------------------------------------
// 会话优化 v4 T5 / R4.3 — UT-UR-08: apply_probe.lua priority parameterization.
// The entry point previously hard-coded source priority=Probe(20); the 36h
// lookback scan writes recovery evidence at Recover(30). Legacy callers keep
// Probe=20 byte-for-byte.
// -----------------------------------------------------------------------------

// TestApplyProbeDefaultPriorityStillProbe pins the compatibility contract:
// ApplyProbeForTenant (no explicit priority) writes source_priority=20.
func TestApplyProbeDefaultPriorityStillProbe(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	if err := mgr.ApplyProbeForTenant(context.Background(), "t1", api.ProbeOutcome{
		CredentialID: 5, RawModel: "pm", Success: true, LatencyMs: 42,
	}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	pri, err := rdb.HGet(context.Background(), "ursm:v2:node:t1:5:pm", "source_priority").Result()
	if err != nil || pri != "20" {
		t.Fatalf("source_priority=%q err=%v, want 20 (legacy Probe default)", pri, err)
	}
}

// TestApplyProbeRecoverPriorityWrites30 pins the extension: an explicit
// Recover(30) priority lands in the hash, and the cool-window recovery
// semantics of the existing state machine are preserved.
func TestApplyProbeRecoverPriorityWrites30(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	ctx := context.Background()

	// Node sitting in a cooling window (disabled + future cool_until).
	key := "ursm:v2:node:t1:6:rm"
	if err := rdb.HSet(ctx, key, map[string]interface{}{
		"disabled": "1", "available": "0",
		"cool_until_ms": "9999999999999", "fail_streak": "3",
	}).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := mgr.ApplyProbeForTenantWithSource(ctx, "t1", api.ProbeOutcome{
		CredentialID: 6, RawModel: "rm", Success: true, LatencyMs: 77,
	}, api.SourcePriorityRecover); err != nil {
		t.Fatalf("probe with recover priority: %v", err)
	}
	for field, want := range map[string]string{
		"source_priority": "30",
		"available":       "1",
		"disabled":        "0",
		"cool_until_ms":   "0",
		"fail_streak":     "0",
	} {
		got, err := rdb.HGet(ctx, key, field).Result()
		if err != nil || got != want {
			t.Fatalf("%s=%q err=%v, want %q", field, got, err, want)
		}
	}
}

// TestApplyProbeNonPositivePriorityFallsBackToProbe pins the defensive
// default: an invalid (<=0) priority keeps Probe=20.
func TestApplyProbeNonPositivePriorityFallsBackToProbe(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	if err := mgr.ApplyProbeForTenantWithSource(context.Background(), "t1", api.ProbeOutcome{
		CredentialID: 8, RawModel: "nm", Success: true,
	}, -7); err != nil {
		t.Fatalf("probe: %v", err)
	}
	pri, err := rdb.HGet(context.Background(), "ursm:v2:node:t1:8:nm", "source_priority").Result()
	if err != nil || pri != "20" {
		t.Fatalf("source_priority=%q err=%v, want 20 (fallback on invalid priority)", pri, err)
	}
}

// TestRecoverPriorityGuardsAgainstRequestSelfHeal pins the "priority guard
// 仍拦自愈回 active" half of UT-UR-08 (UT-CR-10 semantics): after a
// Recover(30) probe FAILURE marks the node unavailable, an ordinary request
// record (priority 10) must NOT flip availability back — the >10 guard in
// record_request.lua keeps the routing fields owned by the recovery source.
func TestRecoverPriorityGuardsAgainstRequestSelfHeal(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	ctx := context.Background()
	key := "ursm:v2:node:t1:9:gm"

	// Recovery probe FAILED → node marked unavailable at Recover(30).
	if err := mgr.ApplyProbeForTenantWithSource(ctx, "t1", api.ProbeOutcome{
		CredentialID: 9, RawModel: "gm", Success: false,
	}, api.SourcePriorityRecover); err != nil {
		t.Fatalf("probe: %v", err)
	}

	// An ordinary request success must not self-heal the node back to
	// available while a >10 source owns it.
	if err := mgr.RecordRequest(ctx, api.RequestOutcome{
		CredentialID: 9, RawModel: "gm", TenantID: "t1", RequestID: "r-1",
		Success: true, LatencyMs: 30,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	avail, _ := rdb.HGet(ctx, key, "available").Result()
	if avail != "0" {
		t.Fatalf("available=%q, want 0 — request traffic must not override a Recover-priority write", avail)
	}
	pri, _ := rdb.HGet(ctx, key, "source_priority").Result()
	if pri != "30" {
		t.Fatalf("source_priority=%q, want 30 (guard must keep the recovery owner)", pri)
	}
}

// TestApplyProbeRecoverPriorityOnRealRedis runs the priority-parameterized
// apply_probe.lua against a REAL Redis (TEST_REDIS_URL, default
// 127.0.0.1:6379; skips when unreachable) — the lua dialect check for the
// UT-UR-08 extension (会话优化 v4 T5 lua 改动验收要求).
func TestApplyProbeRecoverPriorityOnRealRedis(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_URL")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 500 * time.Millisecond})
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("TEST_REDIS_URL (%s) unreachable: %v", addr, err)
	}
	prefix := fmt.Sprintf("ursm:v2:test:realsmoke:%d:", time.Now().UnixNano())
	cfg := DefaultConfig()
	cfg.RedisKeyPrefix = prefix
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	if err := mgr.ApplyProbeForTenantWithSource(ctx, "t1", api.ProbeOutcome{
		CredentialID: 3, RawModel: "rm", Success: true, LatencyMs: 5,
	}, api.SourcePriorityRecover); err != nil {
		t.Fatalf("apply probe on real redis: %v", err)
	}
	pri, err := rdb.HGet(ctx, prefix+"node:t1:3:rm", "source_priority").Result()
	if err != nil || pri != "30" {
		t.Fatalf("source_priority=%q err=%v, want 30 on real redis", pri, err)
	}
}
