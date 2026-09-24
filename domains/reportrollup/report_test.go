package reportrollup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// report_test.go —— BuildRangeReport 区间汇总的 pgxmock 回归：
// 折叠正确性（总计=Σ分组行）、过滤口径一致性（模型过滤时总计从模型行折叠
// 而非 daily_total）、双视角 scope 选择、错误透视跨行合并。
// SQL 正确性（GROUPING/jsonb 聚合等）由 scratch 库 E2E 兜底（协调者执行）。

// mustSnapshotRows 构造 loadSnapshots 查询的 mock 行集。列序与
// loadSnapshots 的 SELECT 严格一致（23 列）；nArgs = 查询实参数
// （3 基础 + 过滤维度），全部以 AnyArg 匹配。
func mustSnapshotRows(t *testing.T, mock pgxmock.PgxPoolIface, snaps []Snapshot, nArgs int) {
	t.Helper()
	rows := pgxmock.NewRows([]string{
		"scope", "scope_key", "report_date", "raw_model_name",
		"request_count", "success_count", "error_count",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"error_kind_breakdown", "cache_hit_ratio",
		"estimated_cost_cents", "currency", "price_snapshot",
		"provider_id", "canonical_id", "tenant_id",
		"credits_charged", "latency_p50_ms", "latency_p95_ms", "updated_at",
	})
	for _, s := range snaps {
		var ratio any
		if s.CacheHitRatio != nil {
			ratio = *s.CacheHitRatio
		}
		var provider any
		if s.ProviderID != nil {
			provider = *s.ProviderID
		}
		var tenant any
		if s.TenantID != nil {
			tenant = *s.TenantID
		}
		price := []byte("{}")
		if len(s.PriceSnapshot) > 0 {
			price = mustJSON(t, s.PriceSnapshot)
		}
		rows.AddRow(string(s.Scope), s.ScopeKey, s.ReportDate, s.RawModelName,
			s.RequestCount, s.SuccessCount, s.ErrorCount,
			s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens,
			mustJSON(t, s.ErrorKindBreakdown), ratio,
			s.EstimatedCostCents, s.Currency, price,
			provider, nil, tenant,
			s.CreditsCharged, s.LatencyP50Ms, s.LatencyP95Ms, time.Now().UTC())
	}
	args := make([]any, nArgs)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	mock.ExpectQuery(`report_snapshots`).WithArgs(args...).WillReturnRows(rows)
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parse day: %v", err)
	}
	return d
}

// TestBuildRangeReport_ProviderView —— 无过滤 provider 视角：总计走
// daily_total，模型行按请求量排序，跨天模型行折叠，错误透视合并。
func TestBuildRangeReport_ProviderView(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	pid1, pid2 := int64(1), int64(2)
	snaps := []Snapshot{
		// daily_total：两天两行（D1 10 请求 / D2 5 请求）。
		mkSnap(ScopeDailyTotal, "all", "2026-09-01", "", 10, 8, map[string]int64{"timeout": 2}, nil, nil),
		mkSnap(ScopeDailyTotal, "all", "2026-09-02", "", 5, 4, map[string]int64{"auth": 1}, nil, nil),
		// by_provider。
		mkSnap(ScopeDailyByProvider, "1", "2026-09-01", "", 10, 8, map[string]int64{"timeout": 2}, &pid1, nil),
		mkSnap(ScopeDailyByProvider, "2", "2026-09-02", "", 5, 4, map[string]int64{"auth": 1}, &pid2, nil),
		// by_model：provider1×gpt-x 两天两行 → 折叠为一行（15 请求）。
		mkSnap(ScopeDailyByModel, "1", "2026-09-01", "gpt-x", 10, 8, map[string]int64{"timeout": 2}, &pid1, nil),
		mkSnap(ScopeDailyByModel, "1", "2026-09-02", "gpt-x", 5, 4, map[string]int64{"auth": 1}, &pid1, nil),
	}
	mustSnapshotRows(t, mock, snaps, 3)

	rep, err := BuildRangeReport(context.Background(), mock,
		day(t, "2026-09-01"), day(t, "2026-09-02"), ViewProvider, RangeFilter{},
		map[int64]string{1: "Provider One", 2: "Provider Two"})
	if err != nil {
		t.Fatalf("BuildRangeReport: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}

	// 总计 = daily_total 两天折叠。
	if rep.Totals.RequestCount != 15 || rep.Totals.SuccessCount != 12 || rep.Totals.ErrorCount != 3 {
		t.Errorf("totals = %d/%d/%d, want 15/12/3",
			rep.Totals.RequestCount, rep.Totals.SuccessCount, rep.Totals.ErrorCount)
	}
	if rep.Totals.ErrorRate == 0 {
		t.Errorf("error_rate not derived")
	}
	if rep.ErrorBreakdown["timeout"] != 2 || rep.ErrorBreakdown["auth"] != 1 {
		t.Errorf("error breakdown = %v", rep.ErrorBreakdown)
	}

	// 供应商分组 + 名称解析。
	if len(rep.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(rep.Providers))
	}
	if rep.Providers[0].ProviderID != 1 || rep.Providers[0].ProviderName != "Provider One" {
		t.Errorf("providers[0] = %+v (sorted by requests desc: p1 has 10 > p2 has 5)", rep.Providers[0])
	}

	// 模型行跨天折叠。
	if len(rep.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(rep.Models))
	}
	if rep.Models[0].RawModelName != "gpt-x" || rep.Models[0].Totals.RequestCount != 15 {
		t.Errorf("models[0] = %+v", rep.Models[0])
	}

	// 按天两天。
	if len(rep.Days) != 2 || rep.Days[0].Date != "2026-09-01" || rep.Days[1].Date != "2026-09-02" {
		t.Errorf("days = %+v", rep.Days)
	}
	if len(rep.SnapshotDates) != 2 {
		t.Errorf("snapshot_dates = %v", rep.SnapshotDates)
	}
}

// TestBuildRangeReport_InternalView —— internal 视角：总计从 internal_tenant
// 折叠（business 口径），人员/租户/模型分组齐全，冻结价折算内部金额。
func TestBuildRangeReport_InternalView(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	tenant := "acme"
	snaps := []Snapshot{
		mkSnapPriced(ScopeInternalTenant, "acme", "2026-09-01", "", 10, 9, map[string]int64{"rate_limited": 1}, &tenant, 1000, 0.1),
		mkSnapPriced(ScopeInternalTenant, "acme", "2026-09-02", "", 6, 6, nil, &tenant, 600, 0.1),
		// internal_person 行 scope_key = 人员标识（end_user_id / person:hash）。
		mkSnapPriced(ScopeInternalPerson, "alice", "2026-09-01", "", 7, 7, nil, &tenant, 700, 0.1),
		mkSnapPriced(ScopeInternalPerson, "person:abcd1234", "2026-09-01", "", 3, 2, map[string]int64{"rate_limited": 1}, &tenant, 300, 0.1),
		mkSnapPriced(ScopeInternalModel, "acme", "2026-09-01", "gpt-x", 10, 9, map[string]int64{"rate_limited": 1}, &tenant, 1000, 0.1),
	}
	mustSnapshotRows(t, mock, snaps, 3)

	rep, err := BuildRangeReport(context.Background(), mock,
		day(t, "2026-09-01"), day(t, "2026-09-02"), ViewInternal, RangeFilter{}, nil)
	if err != nil {
		t.Fatalf("BuildRangeReport: %v", err)
	}

	// 总计 = 两 tenant 行折叠：16 请求 / credits 1600 / 内部金额 = 160 分。
	if rep.Totals.RequestCount != 16 || rep.Totals.CreditsCharged != 1600 {
		t.Errorf("totals = req %d credits %d, want 16/1600", rep.Totals.RequestCount, rep.Totals.CreditsCharged)
	}
	if rep.Totals.InternalCostCents != 160 {
		t.Errorf("internal cost cents = %v, want 160", rep.Totals.InternalCostCents)
	}
	if rep.Totals.InternalCurrency != "CNY" {
		t.Errorf("internal currency = %q", rep.Totals.InternalCurrency)
	}
	if len(rep.Tenants) != 1 || rep.Tenants[0].TenantID != "acme" {
		t.Errorf("tenants = %+v", rep.Tenants)
	}
	if len(rep.Persons) != 2 {
		t.Fatalf("persons = %d, want 2", len(rep.Persons))
	}
	// 按请求量排序：alice(7) 在前。
	if rep.Persons[0].Person != "alice" {
		t.Errorf("persons[0] = %+v", rep.Persons[0])
	}
	if len(rep.Models) != 1 || rep.Models[0].RawModelName != "gpt-x" {
		t.Errorf("models = %+v", rep.Models)
	}
	// Days = 两 tenant 行按日折叠。
	if len(rep.Days) != 2 || rep.Days[0].Totals.RequestCount != 10 || rep.Days[1].Totals.RequestCount != 6 {
		t.Errorf("days = %+v", rep.Days)
	}
}

// TestBuildRangeReport_ModelFilter —— 模型过滤：总计改从模型粒度行折叠
// （不使用 daily_total），供应商分组从模型行二次聚合（无双计）。
func TestBuildRangeReport_ModelFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	pid1 := int64(1)
	// 模拟 SQL 行为：raw_model_name = 'target-model' 过滤下只有该模型行
	// 会返回（daily_total / by_provider 行的 raw_model_name = '' 被排除）。
	snaps := []Snapshot{
		mkSnap(ScopeDailyByModel, "1", "2026-09-01", "target-model", 12, 10, map[string]int64{"timeout": 2}, &pid1, nil),
	}
	mustSnapshotRows(t, mock, snaps, 4)

	rep, err := BuildRangeReport(context.Background(), mock,
		day(t, "2026-09-01"), day(t, "2026-09-01"), ViewProvider,
		RangeFilter{Model: "target-model"}, map[int64]string{1: "P1"})
	if err != nil {
		t.Fatalf("BuildRangeReport: %v", err)
	}

	// 总计 = target-model 一行（12），不是 daily_total 的 100。
	if rep.Totals.RequestCount != 12 {
		t.Errorf("filtered totals = %d, want 12", rep.Totals.RequestCount)
	}
	// 供应商分组来自模型行，无双计。
	if len(rep.Providers) != 1 || rep.Providers[0].Totals.RequestCount != 12 {
		t.Errorf("filtered providers = %+v", rep.Providers)
	}
	if len(rep.Models) != 1 || rep.Models[0].RawModelName != "target-model" {
		t.Errorf("filtered models = %+v", rep.Models)
	}
	if len(rep.Days) != 1 || rep.Days[0].Totals.RequestCount != 12 {
		t.Errorf("filtered days = %+v", rep.Days)
	}
}

// TestBuildRangeReport_LatencyWeighted —— 跨天折叠延迟按成功数加权均值。
func TestBuildRangeReport_LatencyWeighted(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	pid1 := int64(1)
	d1 := mkSnap(ScopeDailyByModel, "1", "2026-09-01", "m", 10, 10, nil, &pid1, nil)
	d1.LatencyP50Ms, d1.LatencyP95Ms = 100, 200
	d2 := mkSnap(ScopeDailyByModel, "1", "2026-09-02", "m", 30, 30, nil, &pid1, nil)
	d2.LatencyP50Ms, d2.LatencyP95Ms = 400, 800
	mustSnapshotRows(t, mock, []Snapshot{d1, d2}, 3)

	rep, err := BuildRangeReport(context.Background(), mock,
		day(t, "2026-09-01"), day(t, "2026-09-02"), ViewProvider, RangeFilter{}, nil)
	if err != nil {
		t.Fatalf("BuildRangeReport: %v", err)
	}
	if len(rep.Models) != 1 {
		t.Fatalf("models = %d", len(rep.Models))
	}
	// 加权 p50 = (100×10 + 400×30)/40 = 325；p95 = (200×10+800×30)/40 = 650。
	if rep.Models[0].Totals.LatencyP50Ms != 325 || rep.Models[0].Totals.LatencyP95Ms != 650 {
		t.Errorf("weighted latency = %v/%v, want 325/650",
			rep.Models[0].Totals.LatencyP50Ms, rep.Models[0].Totals.LatencyP95Ms)
	}
}

// TestBuildRangeReport_EmptyRange —— 无快照区间：空报表非错误。
func TestBuildRangeReport_EmptyRange(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mustSnapshotRows(t, mock, nil, 3)

	rep, err := BuildRangeReport(context.Background(), mock,
		day(t, "2026-08-01"), day(t, "2026-08-07"), ViewInternal, RangeFilter{}, nil)
	if err != nil {
		t.Fatalf("empty range should not error: %v", err)
	}
	if rep.Totals.RequestCount != 0 || len(rep.Days) != 0 {
		t.Errorf("expected zero report, got %+v", rep)
	}
	if rep.ErrorBreakdown == nil {
		t.Errorf("error_breakdown must be non-nil map for JSON")
	}
}

// ---- helpers ----

func mkSnap(scope Scope, key, date, model string, req, ok int64, br map[string]int64, pid *int64, tenant *string) Snapshot {
	d, _ := time.ParseInLocation("2006-01-02", date, time.UTC)
	s := Snapshot{
		Scope: scope, ScopeKey: key, ReportDate: d, RawModelName: model,
		RequestCount: req, SuccessCount: ok, ErrorCount: req - ok,
		InputTokens: req * 100, OutputTokens: req * 50,
		CacheReadTokens: req * 10, CacheWriteTokens: req,
		EstimatedCostCents: req * 7, Currency: "USD",
		CreditsCharged: req * 10, ProviderID: pid, TenantID: tenant,
		PriceSnapshot: map[string]any{},
	}
	if br != nil {
		s.ErrorKindBreakdown = br
	} else {
		s.ErrorKindBreakdown = map[string]int64{}
	}
	return s
}

func mkSnapPriced(scope Scope, key, date, model string, req, ok int64, br map[string]int64, tenant *string, credits int64, centsPerCredit float64) Snapshot {
	s := mkSnap(scope, key, date, model, req, ok, br, nil, tenant)
	s.CreditsCharged = credits
	s.PriceSnapshot = map[string]any{"cents_per_credit": centsPerCredit}
	return s
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
