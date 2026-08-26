package admin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestPopularModelsHotSQL_TargetsHotTable pins the SQL constant to the
// correct source table. Regression guard for the 2026-08-26 fix that
// switched the "凭据路由模型" picker away from
// request_logs_with_current_month (which dragged every ATTACHED columnar
// partition into the scan) to request_logs_hot (heap, ~7d retention).
func TestPopularModelsHotSQL_TargetsHotTable(t *testing.T) {
	if !strings.Contains(popularModelsHotSQL, "FROM request_logs_hot rl") {
		t.Fatalf("popularModelsHotSQL must target request_logs_hot; got:\n%s", popularModelsHotSQL)
	}
	if strings.Contains(popularModelsHotSQL, "request_logs_with_current_month") {
		t.Fatalf("popularModelsHotSQL must not scan the partitioned view (slow columnar scan):\n%s", popularModelsHotSQL)
	}
	if strings.Contains(popularModelsHotSQL, "NOW() - INTERVAL") {
		t.Fatalf("popularModelsHotSQL must use a plan-time literal ($1), not NOW() (which prevents partition pruning):\n%s", popularModelsHotSQL)
	}
	if !strings.Contains(popularModelsHotSQL, "$1") {
		t.Fatalf("popularModelsHotSQL must parameterize the cutoff timestamp for plan-time binding:\n%s", popularModelsHotSQL)
	}
	if got, want := popularModelsLookupWindow(), 7*24*time.Hour; got != want {
		t.Fatalf("popularModelsLookupWindow() = %s, want %s", got, want)
	}
	if !strings.Contains(popularModelsHotSQL, "rl.tenant_id = $2") {
		t.Fatalf("popularModelsHotSQL must tenant-filter scoped lookups:\n%s", popularModelsHotSQL)
	}
}

// TestRecentlyUsedPopularModels_ReadsZSET verifies the dedicated Redis
// ZSET fast path: writes from the request hot path become visible to
// the admin dashboard immediately, without touching the request_logs
// tables. Uses miniredis to validate the round trip end-to-end.
func TestRecentlyUsedPopularModels_ReadsZSET(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()

	// Simulate three recent successful requests hitting different
	// canonical models. gpt-4o is bumped twice to confirm ZINCRBY.
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "gpt-4o", false)
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "gpt-4o", false)
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "claude-3-5-sonnet", false)
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "gemini-2.5-pro", false)

	got := recentlyUsedPopularModels(ctx, rdb, "tenant-a", 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(got), got)
	}
	// Top by score: gpt-4o=2, then any tie between claude and gemini
	// (sort is stable on name).
	if got[0].CanonicalName != "gpt-4o" || got[0].Source != "recent" || got[0].Count == nil || *got[0].Count != 2 {
		t.Fatalf("expected gpt-4o with count=2 at index 0, got %+v", got[0])
	}
	if got[0].DisplayName != "gpt-4o" {
		t.Fatalf("DisplayName should default to CanonicalName, got %q", got[0].DisplayName)
	}
}

// TestRecordRecentlyUsedModel_ProbeGate verifies that probe / selfcheck
// calls cannot pollute the dashboard ranking even if they hit the
// function. This is the key reason the recently-used ZSET exists in
// addition to the live-stream dim queues.
func TestRecordRecentlyUsedModel_ProbeGate(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()

	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "gpt-4o", true)       // probe - must not write
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "", false)            // empty - must not write
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "  unknown  ", false) // "unknown" - must not write
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "claude-3-5-sonnet", false)

	got := recentlyUsedPopularModels(ctx, rdb, "tenant-a", 10)
	if len(got) != 1 || got[0].CanonicalName != "claude-3-5-sonnet" {
		t.Fatalf("probe / empty / unknown calls must be filtered out, got %+v", got)
	}
}

// TestRecordRecentlyUsedModel_NilClientSafe verifies the bump is
// nil-safe so deployments without Redis still ingest telemetry rows
// without panicking.
func TestRecordRecentlyUsedModel_NilClientSafe(t *testing.T) {
	ctx := context.Background()
	// Both must not panic.
	RecordRecentlyUsedModel(ctx, nil, "tenant-a", "gpt-4o", false)
	if got := recentlyUsedPopularModels(ctx, nil, "tenant-a", 10); got != nil {
		t.Fatalf("recentlyUsedPopularModels(nil) must return nil, got %+v", got)
	}
}

// TestRecordRecentlyUsedModel_TTLRefreshed verifies each write refreshes
// the key TTL so an idle key expires exactly 7d after the LAST request,
// not after the first. miniredis clamps TTL to whole seconds; both reads
// should be in the (0, RecentlyUsedModelsTTL] window and the post-refresh
// read should be at the full TTL.
func TestRecordRecentlyUsedModel_TTLRefreshed(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "gpt-4o", false)

	ttl1, err := rdb.TTL(ctx, recentlyUsedModelsKey("tenant-a")).Result()
	if err != nil {
		t.Fatalf("TTL read: %v", err)
	}
	if ttl1 <= 0 || ttl1 > RecentlyUsedModelsTTL {
		t.Fatalf("first TTL must be in (0, %s], got %s", RecentlyUsedModelsTTL, ttl1)
	}

	// Advance miniredis clock and write again; TTL should reset to max.
	mr.FastForward(2 * time.Hour)
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "gpt-4o", false)

	ttl2, err := rdb.TTL(ctx, recentlyUsedModelsKey("tenant-a")).Result()
	if err != nil {
		t.Fatalf("TTL read after refresh: %v", err)
	}
	if ttl2 <= 0 || ttl2 > RecentlyUsedModelsTTL {
		t.Fatalf("TTL after refresh must be in (0, %s], got %s", RecentlyUsedModelsTTL, ttl2)
	}
	// miniredis reports the TTL rounded down to whole seconds; after a
	// refresh the value should equal the configured window.
	if ttl2 != RecentlyUsedModelsTTL {
		t.Fatalf("TTL after refresh should equal %s, got %s", RecentlyUsedModelsTTL, ttl2)
	}
}

func TestRecentlyUsedPopularModels_IsolatesTenants(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	RecordRecentlyUsedModel(ctx, rdb, "tenant-a", "gpt-4o", false)
	RecordRecentlyUsedModel(ctx, rdb, "tenant-b", "claude-3-5-sonnet", false)
	RecordRecentlyUsedModel(ctx, rdb, "", "must-not-write", false)

	gotA := recentlyUsedPopularModels(ctx, rdb, "tenant-a", 10)
	gotB := recentlyUsedPopularModels(ctx, rdb, "tenant-b", 10)
	if len(gotA) != 1 || gotA[0].CanonicalName != "gpt-4o" {
		t.Fatalf("tenant-a models = %+v", gotA)
	}
	if len(gotB) != 1 || gotB[0].CanonicalName != "claude-3-5-sonnet" {
		t.Fatalf("tenant-b models = %+v", gotB)
	}
	if got := recentlyUsedPopularModels(ctx, rdb, "", 10); got != nil {
		t.Fatalf("all-tenant recent models must not use a global key: %+v", got)
	}
}

func TestPopularModelsLookupWindow(t *testing.T) {
	t.Setenv(popularModelsLookupHoursEnv, "24")
	if got := popularModelsLookupWindow(); got != 24*time.Hour {
		t.Fatalf("24h override = %s", got)
	}
	t.Setenv(popularModelsLookupHoursEnv, "invalid")
	if got := popularModelsLookupWindow(); got != defaultPopularModelsLookupWindow {
		t.Fatalf("invalid override = %s", got)
	}
	t.Setenv(popularModelsLookupHoursEnv, "0")
	if got := popularModelsLookupWindow(); got != defaultPopularModelsLookupWindow {
		t.Fatalf("zero override = %s", got)
	}
}

// TestLivePopularModels_DoesNotCoverProbeOrEmpty verifies the dim queue
// ZCARD path returns one entry per active lane with a positive count.
// (The probe gate lives upstream of RecordRecentlyUsedModel; live lanes
// mix probes and real requests so this test only asserts structure.)
func TestLivePopularModels_NilAndEmpty(t *testing.T) {
	ctx := context.Background()
	if got := livePopularModels(ctx, nil, 5); got != nil {
		t.Fatalf("livePopularModels(nil) must return nil, got %+v", got)
	}
}

// TestListTopModelsSQL_TargetsHotTable pins the listTopModels SQL body
// (extracted via reflection-style text search of admin/logs.go) to the
// hot table. The dashboard widget was the second-half of the 2026-08-26
// fix; a regression that switches it back to the partitioned view would
// reintroduce the 5s+ columnar scan.
func TestListTopModelsSQL_TargetsHotTable(t *testing.T) {
	src, err := os.ReadFile("logs.go")
	if err != nil {
		t.Fatalf("read logs.go: %v", err)
	}
	body := string(src)

	// Find the listTopModels handler.
	start := strings.Index(body, "func (h *Handler) listTopModels(")
	if start == -1 {
		t.Fatalf("listTopModels handler not found")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end == -1 {
		t.Fatalf("listTopModels handler end not found")
	}
	handler := body[start : start+end+2]

	// Source table must be the hot standalone table.
	if !strings.Contains(handler, "FROM request_logs_hot rl") {
		t.Fatalf("listTopModels must target request_logs_hot; full handler:\n%s", handler)
	}
	// Source must NOT be the partitioned view (columnar scan).
	// Use the SQL-shaped token to ignore the historical comment that
	// mentions the old view by name.
	if strings.Contains(handler, "FROM request_logs_with_current_month rl") {
		t.Fatalf("listTopModels must not scan the partitioned view; full handler:\n%s", handler)
	}
	// Must keep an explicit context timeout.
	if !strings.Contains(handler, "context.WithTimeout") {
		t.Fatalf("listTopModels must keep an explicit context.WithTimeout")
	}
}
