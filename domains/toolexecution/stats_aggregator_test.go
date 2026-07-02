package toolexecution

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────
// percentile 单元测试
// ──────────────────────────────────────────────────────────────────

func TestPercentile(t *testing.T) {
	sorted := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

	cases := []struct {
		name string
		p    float64
		want int64
	}{
		{"p0", 0.0, 10},
		{"p50", 0.50, 50},
		{"p95", 0.95, 90},
		{"p99", 0.99, 90},
		{"p100", 1.0, 100},
	}
	for _, c := range cases {
		got := percentile(sorted, c.p)
		if got != c.want {
			t.Errorf("%s: percentile(%.2f)=%d, want %d", c.name, c.p, got, c.want)
		}
	}

	// 空切片
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("empty: got %d, want 0", got)
	}
	if got := percentile([]int64{}, 0.5); got != 0 {
		t.Errorf("empty slice: got %d, want 0", got)
	}
}

// ──────────────────────────────────────────────────────────────────
// computeDurationStats
// ──────────────────────────────────────────────────────────────────

func TestComputeDurationStats_Empty(t *testing.T) {
	stats := &ToolUsageStats{}
	computeDurationStats(stats, nil)
	if stats.AvgDurationMs != 0 || stats.P50DurationMs != 0 {
		t.Errorf("expected zeros, got %+v", stats)
	}
}

func TestComputeDurationStats_Typical(t *testing.T) {
	stats := &ToolUsageStats{}
	durations := []int64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}
	computeDurationStats(stats, durations)

	wantAvg := 550.0
	if stats.AvgDurationMs != wantAvg {
		t.Errorf("Avg=%.2f, want %.2f", stats.AvgDurationMs, wantAvg)
	}
	if stats.P50DurationMs != 500 {
		t.Errorf("P50=%d, want 500", stats.P50DurationMs)
	}
	if stats.P95DurationMs != 900 {
		t.Errorf("P95=%d, want 900", stats.P95DurationMs)
	}
	if stats.P99DurationMs != 900 {
		t.Errorf("P99=%d, want 900", stats.P99DurationMs)
	}
}

// ──────────────────────────────────────────────────────────────────
// StatsAggregator 集成测试（基于 memoryStore）
// ──────────────────────────────────────────────────────────────────

func TestStatsAggregator_AggregateDaily_Empty(t *testing.T) {
	store := newMemoryStore()
	agg := NewStatsAggregator(store, quietLogger())

	date := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := agg.AggregateDaily(context.Background(), date); err != nil {
		t.Fatal(err)
	}

	stats, err := store.ListStats(context.Background(), "", time.Time{}, date.Add(48*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Errorf("len(stats)=%d, want 0", len(stats))
	}
}

func TestStatsAggregator_AggregateDaily_MultiTool(t *testing.T) {
	store := newMemoryStore()
	agg := NewStatsAggregator(store, quietLogger())
	ctx := context.Background()

	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	start := day

	// tool "search": 5 success, 1 error
	for i := 0; i < 5; i++ {
		e := &ToolExecution{
			ExecutionID:  "search-" + string(rune('0'+i)),
			SessionID:    "s" + string(rune('a'+i%2)),
			IdentityHash: "h1",
			ToolName:     "search",
			Status:       StatusSuccess,
			StartedAt:    start.Add(time.Duration(i) * time.Hour),
			CompletedAt:  start.Add(time.Duration(i)*time.Hour + 100*time.Millisecond),
			DurationMs:   100 + int64(i*10),
			Arguments:    json.RawMessage(`{}`),
		}
		_ = store.Save(ctx, e)
	}
	_ = store.Save(ctx, &ToolExecution{
		ExecutionID:  "search-err",
		SessionID:    "sx",
		IdentityHash: "h2",
		ToolName:     "search",
		Status:       StatusError,
		ErrorType:    ErrorTypeNetwork,
		StartedAt:    start.Add(6 * time.Hour),
		ErrorMessage: "boom",
	})

	// tool "fetch": 3 success, 1 timeout
	for i := 0; i < 3; i++ {
		_ = store.Save(ctx, &ToolExecution{
			ExecutionID:  "fetch-" + string(rune('0'+i)),
			SessionID:    "sy",
			IdentityHash: "h1",
			ToolName:     "fetch",
			Status:       StatusSuccess,
			StartedAt:    start.Add(time.Duration(i) * time.Hour),
			CompletedAt:  start.Add(time.Duration(i)*time.Hour + 50*time.Millisecond),
			DurationMs:   200,
		})
	}
	_ = store.Save(ctx, &ToolExecution{
		ExecutionID:  "fetch-timeout",
		SessionID:    "sy",
		IdentityHash: "h3",
		ToolName:     "fetch",
		Status:       StatusTimeout,
		StartedAt:    start.Add(3 * time.Hour),
	})

	if err := agg.AggregateDaily(ctx, day); err != nil {
		t.Fatalf("AggregateDaily: %v", err)
	}

	// 验证 search 统计
	searchStats, err := store.GetStats(ctx, "search", day)
	if err != nil {
		t.Fatalf("GetStats(search): %v", err)
	}
	if searchStats.TotalCalls != 6 {
		t.Errorf("search TotalCalls=%d, want 6", searchStats.TotalCalls)
	}
	if searchStats.SuccessCalls != 5 {
		t.Errorf("search SuccessCalls=%d, want 5", searchStats.SuccessCalls)
	}
	if searchStats.FailedCalls != 1 {
		t.Errorf("search FailedCalls=%d, want 1", searchStats.FailedCalls)
	}
	if searchStats.UniqueUsers != 2 {
		t.Errorf("search UniqueUsers=%d, want 2", searchStats.UniqueUsers)
	}
	if searchStats.UniqueSessions != 3 { // sa, sb, sx
		t.Errorf("search UniqueSessions=%d, want 3", searchStats.UniqueSessions)
	}
	if len(searchStats.TopUsers) == 0 {
		t.Error("search TopUsers is empty")
	}
	// h1 在 search 中调用 5 次，h2 调用 1 次
	if searchStats.TopUsers[0].IdentityHash != "h1" {
		t.Errorf("Top user=%q, want h1", searchStats.TopUsers[0].IdentityHash)
	}
	if searchStats.TopUsers[0].CallCount != 5 {
		t.Errorf("Top count=%d, want 5", searchStats.TopUsers[0].CallCount)
	}

	// 平均值
	if searchStats.AvgDurationMs <= 0 {
		t.Errorf("search AvgDurationMs=%.2f, want > 0", searchStats.AvgDurationMs)
	}

	// 验证 fetch 统计
	fetchStats, _ := store.GetStats(ctx, "fetch", day)
	if fetchStats.TotalCalls != 4 {
		t.Errorf("fetch TotalCalls=%d, want 4", fetchStats.TotalCalls)
	}
	if fetchStats.TimeoutCalls != 1 {
		t.Errorf("fetch TimeoutCalls=%d, want 1", fetchStats.TimeoutCalls)
	}
	if fetchStats.SuccessCalls != 3 {
		t.Errorf("fetch SuccessCalls=%d, want 3", fetchStats.SuccessCalls)
	}
}

func TestStatsAggregator_AggregateDaily_OnlyFailedDurationsIgnored(t *testing.T) {
	// 失败的 duration 不会污染 p50/p95（仅 success 进入分位计算）
	store := newMemoryStore()
	agg := NewStatsAggregator(store, quietLogger())
	ctx := context.Background()

	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	start := day

	// 1 success 100ms, 1 error 1ms
	_ = store.Save(ctx, &ToolExecution{
		ExecutionID: "x-1",
		ToolName:    "x",
		Status:      StatusSuccess,
		StartedAt:   start,
		CompletedAt: start.Add(100 * time.Millisecond),
		DurationMs:  100,
	})
	_ = store.Save(ctx, &ToolExecution{
		ExecutionID: "x-2",
		ToolName:    "x",
		Status:      StatusError,
		StartedAt:   start.Add(time.Hour),
		CompletedAt: start.Add(time.Hour + time.Millisecond),
		DurationMs:  1,
	})

	if err := agg.AggregateDaily(ctx, day); err != nil {
		t.Fatal(err)
	}
	s, _ := store.GetStats(ctx, "x", day)
	if s.AvgDurationMs != 100 {
		t.Errorf("AvgDurationMs=%.2f, want 100", s.AvgDurationMs)
	}
	if s.P50DurationMs != 100 {
		t.Errorf("P50DurationMs=%d, want 100 (failed duration ignored)", s.P50DurationMs)
	}
}

func TestStatsAggregator_AggregateDaily_TopUsersCapped(t *testing.T) {
	store := newMemoryStore()
	agg := NewStatsAggregator(store, quietLogger())
	ctx := context.Background()

	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	start := day

	// 注入 15 个不同用户，每个 1 次
	for i := 0; i < 15; i++ {
		hash := string(rune('a' + i))
		_ = store.Save(ctx, &ToolExecution{
			ExecutionID:  "u-" + hash,
			ToolName:     "x",
			IdentityHash: hash,
			Status:       StatusSuccess,
			StartedAt:    start.Add(time.Duration(i) * time.Minute),
		})
	}
	if err := agg.AggregateDaily(ctx, day); err != nil {
		t.Fatal(err)
	}
	s, _ := store.GetStats(ctx, "x", day)
	if len(s.TopUsers) > TopUsersLimit {
		t.Errorf("len(TopUsers)=%d, want <= %d", len(s.TopUsers), TopUsersLimit)
	}
	if s.UniqueUsers != 15 {
		t.Errorf("UniqueUsers=%d, want 15", s.UniqueUsers)
	}
}

func TestStatsAggregator_AggregateDaily_Idempotent(t *testing.T) {
	store := newMemoryStore()
	agg := NewStatsAggregator(store, quietLogger())
	ctx := context.Background()
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	start := day

	for i := 0; i < 3; i++ {
		_ = store.Save(ctx, &ToolExecution{
			ExecutionID: "x-" + string(rune('0'+i)),
			ToolName:    "x",
			Status:      StatusSuccess,
			StartedAt:   start.Add(time.Duration(i) * time.Hour),
			DurationMs:  100,
		})
	}

	// 多次调用结果一致
	if err := agg.AggregateDaily(ctx, day); err != nil {
		t.Fatal(err)
	}
	first, _ := store.GetStats(ctx, "x", day)
	if err := agg.AggregateDaily(ctx, day); err != nil {
		t.Fatal(err)
	}
	second, _ := store.GetStats(ctx, "x", day)
	if first.TotalCalls != second.TotalCalls {
		t.Errorf("non-idempotent: %d vs %d", first.TotalCalls, second.TotalCalls)
	}
	if first.ID != second.ID {
		t.Errorf("ID changed: %d vs %d (upsert should preserve)", first.ID, second.ID)
	}
}

func TestStatsAggregator_AggregateDaily_DefaultYesterday(t *testing.T) {
	// 零值 date -> 自动用昨日 UTC
	store := newMemoryStore()
	agg := NewStatsAggregator(store, quietLogger())
	if err := agg.AggregateDaily(context.Background(), time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestNewStatsAggregator_NilStorePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	NewStatsAggregator(nil, nil)
}
