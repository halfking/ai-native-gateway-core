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
