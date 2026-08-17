package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

type ursmResultRecorder struct {
	*metrics.NoopRecorder
	results map[string]int
}

func (r *ursmResultRecorder) RecordURSMv2ShadowResult(result string) {
	r.results[result]++
}

func TestRecordRequestReportsShadowOutcome(t *testing.T) {
	previous := metrics.Global()
	recorder := &ursmResultRecorder{NoopRecorder: metrics.NewNoopRecorder(), results: make(map[string]int)}
	metrics.SetGlobal(recorder)
	t.Cleanup(func() { metrics.SetGlobal(previous) })

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	outcome := api.RequestOutcome{TenantID: "tenant", CredentialID: 7, RawModel: "gpt-4", Success: true, RequestID: "request"}

	offCfg := DefaultConfig()
	offCfg.Mode = api.ModeOff
	off := New(Dependencies{Redis: rdb, Config: offCfg})
	if err := off.RecordRequest(context.Background(), outcome); err != nil {
		t.Fatalf("off record: %v", err)
	}

	quietCfg := DefaultConfig()
	quietCfg.Mode = api.ModeShadow
	quiet := New(Dependencies{Config: quietCfg})
	if err := quiet.RecordRequest(context.Background(), outcome); err != nil {
		t.Fatalf("quiet shadow record: %v", err)
	}

	activeCfg := DefaultConfig()
	activeCfg.Mode = api.ModeShadow
	activeCfg.ShadowDoubleWrite = true
	active := New(Dependencies{Redis: rdb, Config: activeCfg})
	t.Cleanup(active.Close)
	if err := active.RecordRequest(context.Background(), outcome); err != nil {
		t.Fatalf("double-write record: %v", err)
	}
	if exists := mr.Exists("ursm:v2:node:tenant:7:gpt-4"); !exists {
		t.Fatal("double-write did not create tenant-aware node state")
	}

	mr.Close()
	if err := active.RecordRequest(context.Background(), api.RequestOutcome{TenantID: "tenant", CredentialID: 8, RawModel: "gpt-4", RequestID: "redis-down"}); err == nil {
		t.Fatal("redis failure must be returned from sidecar write")
	}

	if got := recorder.results["skipped"]; got != 2 {
		t.Fatalf("skipped=%d, want 2", got)
	}
	if got := recorder.results["recorded"]; got != 1 {
		t.Fatalf("recorded=%d, want 1", got)
	}
	if got := recorder.results["failed"]; got != 1 {
		t.Fatalf("failed=%d, want 1", got)
	}
}

// TestFacadeOffModeDoesNotRecord confirms that the explicit rollback mode
// remains a no-op even though direct authoritative startup is the default.
func TestFacadeOffModeDoesNotRecord(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeOff
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := mgr.RecordRequest(context.Background(), api.RequestOutcome{
		CredentialID: 7, RawModel: "gpt-4", Success: true, LatencyMs: 80, RequestID: "r-x"}); err != nil {
		t.Fatalf("record: %v", err)
	}
}

// TestFacadeRecordsUnderShadow exercises the opt-in shadow double-write path.
// Routing remains on legacy, while RequestOutcome is copied to the v2 store.
func TestFacadeRecordsUnderShadow(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeShadow
	cfg.ShadowDoubleWrite = true
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

func TestRecordRequestTerminalOutcomeHasDistinctDedupNamespace(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeShadow
	cfg.ShadowDoubleWrite = true
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)

	ctx := context.Background()
	base := api.RequestOutcome{
		CredentialID: 7, RawModel: "gpt-4", TenantID: "tenant",
		RequestID: "request", DedupKey: "request:attempt:1",
	}
	failure := base
	failure.ErrorKind = "upstream_down"
	if err := mgr.RecordRequest(ctx, failure); err != nil {
		t.Fatalf("record intermediate failure: %v", err)
	}
	success := base
	success.Success = true
	success.Terminal = true
	if err := mgr.RecordRequest(ctx, success); err != nil {
		t.Fatalf("record terminal success: %v", err)
	}

	key := "ursm:v2:node:tenant:7:gpt-4"
	values, err := rdb.HMGet(ctx, key, "failure_count", "success_count", "fail_streak", "last_err").Result()
	if err != nil {
		t.Fatalf("read terminal state: %v", err)
	}
	if values[0] != "1" || values[1] != "1" || values[2] != "0" || values[3] != "" {
		t.Fatalf("terminal outcome competed with intermediate dedup: %#v", values)
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
