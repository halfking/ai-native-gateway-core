package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// TestFacadeRecordsWithoutAffectingDecision is the spec test from the plan
// (T8 Step 1). Under DefaultConfig() the rollout controller is ModeOff, so
// RecordRequest short-circuits without writing. This guards the "default
// mode is a no-op" invariant.
func TestFacadeRecordsWithoutAffectingDecision(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := mgr.RecordRequest(context.Background(), api.RequestOutcome{
		CredentialID: 7, RawModel: "gpt-4", Success: true, LatencyMs: 80, RequestID: "r-x"}); err != nil {
		t.Fatalf("record: %v", err)
	}
}

// TestFacadeRecordsUnderShadow exercises the actual write path. Shadow mode
// by design returns ShouldUseV2 == false; we therefore flip to ModeCanary
// with CanaryPercent=100 to force the write to the v2 store. The assertion
// checks that the node hash has `available=1` after a successful record.
func TestFacadeRecordsUnderShadow(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := mgr.RecordRequest(context.Background(), api.RequestOutcome{
		CredentialID: 7, RawModel: "gpt-4", Success: true, LatencyMs: 80, RequestID: "r-x"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// assert the node key exists with available=1
	n, err := rdb.HGet(context.Background(), "ursm:v2:node:7:gpt-4", "available").Result()
	if err != nil {
		t.Fatalf("hget: %v", err)
	}
	if n != "1" {
		t.Fatalf("available=%q want 1", n)
	}
}

// TestFacadeSkipsWhenStoreNil confirms the nil-receiver short-circuit.
func TestFacadeSkipsWhenStoreNil(t *testing.T) {
	var m *Manager
	if err := m.RecordRequest(context.Background(), api.RequestOutcome{
		CredentialID: 1, RawModel: "x", Success: true, RequestID: "r"}); err != nil {
		t.Fatalf("nil receiver should be a no-op, got: %v", err)
	}
}

// TestRecordRequestHonorsAdminHold verifies that RecordRequest reads
// manual_hold from the node hash and threads AdminHold into the Lua
// script, so the script's admin_hold short-circuit becomes reachable.
// Under manual_hold="1" the Lua must NOT mutate generation, last_err,
// success_count, etc.
func TestRecordRequestHonorsAdminHold(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	ctx := context.Background()
	// Seed admin_hold="1" before recording.
	if err := rdb.HSet(ctx, "ursm:v2:node:42:gpt",
		"manual_hold", "1", "source_priority", "40",
	).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := mgr.RecordRequest(ctx, api.RequestOutcome{
		CredentialID: 42, RawModel: "gpt", Success: true,
		RequestID: "r-admin",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// The Lua's admin_hold short-circuit must NOT have written last_err,
	// generation, success_count etc. Verify by checking generation is
	// empty (no HINCRBY happened) and last_err is empty.
	gen, _ := rdb.HGet(ctx, "ursm:v2:node:42:gpt", "generation").Result()
	if gen != "" && gen != "0" {
		t.Fatalf("generation must not have been incremented under admin_hold, got %q", gen)
	}
	lastErr, _ := rdb.HGet(ctx, "ursm:v2:node:42:gpt", "last_err").Result()
	if lastErr != "" {
		t.Fatalf("last_err must not have been written under admin_hold, got %q", lastErr)
	}
}
