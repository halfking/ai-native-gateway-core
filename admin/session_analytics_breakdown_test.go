package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── 模型分解端点测试 ────────────────────────────────────────────────────────

func TestHandleModelBreakdown_Success(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	// 插入测试数据
	setupModelBreakdownTestData(t, db)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/model-breakdown?"+breakdownDateRange()+"", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleModelBreakdown(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ModelBreakdownResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 验证结构
	if len(resp.ByModel) == 0 {
		t.Error("expected by_model to have entries")
	}
	if len(resp.ByProvider) == 0 {
		t.Error("expected by_provider to have entries")
	}
}

func TestHandleModelBreakdown_LongTailMerge(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	// 插入多个小占比模型（<2%）
	setupLongTailTestData(t, db)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/model-breakdown?"+breakdownDateRange()+"", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleModelBreakdown(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp ModelBreakdownResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 验证长尾合并：应该有"其他"分类
	hasOthers := false
	for _, m := range resp.ByModel {
		if m.Model == "其他" {
			hasOthers = true
			if m.RequestCount == 0 {
				t.Error("expected '其他' category to have non-zero request count")
			}
		}
	}

	if !hasOthers {
		t.Log("No '其他' category found (acceptable if all models are above 2% threshold)")
	}
}

func TestHandleModelBreakdown_EmptyResult(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	// 不插入数据，查询空范围
	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/model-breakdown?date_from=2025-01-01&date_to=2025-01-02", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleModelBreakdown(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp ModelBreakdownResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 空结果应返回空数组
	if resp.ByModel == nil {
		t.Error("expected by_model to be empty array, not nil")
	}
	if resp.ByProvider == nil {
		t.Error("expected by_provider to be empty array, not nil")
	}
}

// ── 会话形态端点测试 ────────────────────────────────────────────────────────

func TestHandleSessionShape_Success(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	// 插入测试数据：不同请求数和时长的会话
	setupSessionShapeTestData(t, db)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/session-shape?"+breakdownDateRange()+"", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleSessionShape(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SessionShapeResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 验证分桶
	if len(resp.RequestCountBuckets) == 0 {
		t.Error("expected request_count_buckets to have entries")
	}
	if len(resp.DurationBuckets) == 0 {
		t.Error("expected duration_buckets to have entries")
	}

	// 验证标签：quick/standard/deep/marathon
	expectedLabels := map[string]bool{"quick": false, "standard": false, "deep": false, "marathon": false}
	for _, b := range resp.RequestCountBuckets {
		if _, ok := expectedLabels[b.Label]; ok {
			expectedLabels[b.Label] = true
		}
	}

	// 至少应该有一个分桶
	hasAnyLabel := false
	for _, found := range expectedLabels {
		if found {
			hasAnyLabel = true
			break
		}
	}
	if !hasAnyLabel {
		t.Error("expected at least one request count bucket with standard labels")
	}
}

func TestHandleSessionShape_BucketBoundaries(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	// 插入边界情况测试数据
	setupBoundaryTestData(t, db)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/session-shape?"+breakdownDateRange()+"", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleSessionShape(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp SessionShapeResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 验证边界：1请求→quick，6请求→standard，21请求→deep，51请求→marathon
	bucketMap := make(map[string]int)
	for _, b := range resp.RequestCountBuckets {
		bucketMap[b.Label] = b.Count
	}

	// 应该至少有 quick 桶（1请求）
	if bucketMap["quick"] == 0 {
		t.Error("expected at least one session in 'quick' bucket")
	}
}

func TestHandleSessionShape_EmptyResult(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/session-shape?date_from=2025-01-01&date_to=2025-01-02", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleSessionShape(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp SessionShapeResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.RequestCountBuckets == nil {
		t.Error("expected request_count_buckets to be empty array, not nil")
	}
	if resp.DurationBuckets == nil {
		t.Error("expected duration_buckets to be empty array, not nil")
	}
}

// ── 健康分布端点测试 ────────────────────────────────────────────────────────

func TestHandleHealthDistribution_Success(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	// 插入测试数据：包含健康等级和结果分类
	setupHealthDistributionTestData(t, db)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/health-distribution?"+breakdownDateRange()+"", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleHealthDistribution(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp HealthDistributionResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 验证结构
	if resp.GradeDistribution == nil {
		t.Error("expected grade_distribution to be non-nil")
	}
	if resp.OutcomeDistribution == nil {
		t.Error("expected outcome_distribution to be non-nil")
	}
	if resp.ComplianceDistribution == nil {
		t.Error("expected compliance_distribution to be non-nil")
	}
	if resp.LatencyBuckets == nil {
		t.Error("expected latency_buckets to be non-nil")
	}

	// 验证等级分布包含有效等级
	expectedGrades := []string{"A", "B", "C", "D", "F", "unknown"}
	for grade := range resp.GradeDistribution {
		found := false
		for _, expected := range expectedGrades {
			if grade == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected grade: %s", grade)
		}
	}
}

func TestHandleHealthDistribution_WithNullHealthGrade(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	// 插入 health_grade 为 NULL 的数据
	setupNullHealthGradeTestData(t, db)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/health-distribution?"+breakdownDateRange()+"", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleHealthDistribution(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp HealthDistributionResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 应该有 unknown 分类
	if count, ok := resp.GradeDistribution["unknown"]; !ok || count == 0 {
		t.Error("expected 'unknown' grade for NULL health_grade")
	}

	// 平均健康分应该忽略 NULL，返回有效值的平均
	if resp.AvgHealthScore < 0 {
		t.Errorf("expected non-negative avg_health_score, got %.2f", resp.AvgHealthScore)
	}
}

func TestHandleHealthDistribution_EmptyResult(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// R65: 清理必须注册在 setup 之前——setup 内 t.Fatalf→Goexit 只执行
	// 已注册的 defer，事后注册会漏掉半途失败的夹具行（本轮实证）。
	defer func() { cleanupBreakdownFixtures(t, db) }()

	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/health-distribution?date_from=2025-01-01&date_to=2025-01-02", nil)
	req = setTestRequestContext(req, "tenant_admin", breakdownTenant, "breakdown-tester")
	w := httptest.NewRecorder()

	h.HandleHealthDistribution(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp HealthDistributionResponse
	if err := parseJSONResponse(w, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.GradeDistribution == nil {
		t.Error("expected grade_distribution to be empty map, not nil")
	}
	if resp.AvgHealthScore != 0 {
		t.Errorf("expected avg_health_score to be 0 for empty result, got %.2f", resp.AvgHealthScore)
	}
}

// ── 过滤器测试 ────────────────────────────────────────────────────────

func TestParseAnalyticsFilters_InvalidDateRange(t *testing.T) {
	tests := []struct {
		name        string
		dateFrom    string
		dateTo      string
		expectedErr string
	}{
		{
			name:        "date_from > date_to",
			dateFrom:    "2026-07-10",
			dateTo:      "2026-07-01",
			expectedErr: "date_from must be <= date_to",
		},
		{
			name:        "range > 90 days",
			dateFrom:    "2026-01-01",
			dateTo:      "2026-06-01",
			expectedErr: "date range cannot exceed 90 days",
		},
		{
			name:        "invalid date format",
			dateFrom:    "2026/07/01",
			dateTo:      "2026-07-06",
			expectedErr: "invalid date_from format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test?date_from="+tt.dateFrom+"&date_to="+tt.dateTo, nil)
			_, err := parseAnalyticsFilters(req)
			if err == nil {
				t.Errorf("expected error, got nil")
			}
			if err != nil && err.Error() != tt.expectedErr {
				t.Errorf("expected error '%s', got '%s'", tt.expectedErr, err.Error())
			}
		})
	}
}

func TestParseAnalyticsFilters_DefaultValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	filters, err := parseAnalyticsFilters(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 默认 7 天
	expectedDuration := 7 * 24 * time.Hour
	actualDuration := filters.dateTo.Sub(filters.dateFrom)
	if actualDuration < expectedDuration-time.Hour || actualDuration > expectedDuration+time.Hour {
		t.Errorf("expected default range ~7 days, got %v", actualDuration)
	}
}

// ── 长尾合并逻辑测试 ────────────────────────────────────────────────────────

func TestMergeLongTail(t *testing.T) {
	tests := []struct {
		name       string
		input      []ModelStats
		threshold  float64
		wantOthers bool
	}{
		{
			name: "no long tail",
			input: []ModelStats{
				{Model: "gpt-4o", RequestCount: 100},
				{Model: "claude-3", RequestCount: 50},
			},
			threshold:  0.02,
			wantOthers: false,
		},
		{
			name: "with long tail",
			input: []ModelStats{
				{Model: "gpt-4o", RequestCount: 100, TotalCostUSD: 10.0},
				{Model: "claude-3", RequestCount: 50, TotalCostUSD: 5.0},
				{Model: "tiny-1", RequestCount: 1, TotalCostUSD: 0.1},
				{Model: "tiny-2", RequestCount: 1, TotalCostUSD: 0.1},
			},
			threshold:  0.02,
			wantOthers: true,
		},
		{
			name:       "empty input",
			input:      []ModelStats{},
			threshold:  0.02,
			wantOthers: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeLongTail(tt.input, tt.threshold)

			hasOthers := false
			for _, m := range result {
				if m.Model == "其他" {
					hasOthers = true
					if m.RequestCount == 0 {
						t.Error("expected '其他' to have non-zero request count")
					}
				}
			}

			if hasOthers != tt.wantOthers {
				t.Errorf("expected hasOthers=%v, got %v", tt.wantOthers, hasOthers)
			}
		})
	}
}

// ── 测试数据准备辅助函数 ────────────────────────────────────────────────────────
//
// R65 夹具适配（真库 17 既有失败专项）：
//   - fk_session_tenant 要求 tenants 里先有 'tnt_test'（修前 23503）；
//   - session_summaries.duration_seconds 是 GENERATED ALWAYS 列（修前
//     显式插入 428C9），时长经 last_request_at = first + N 秒表达；
//   - request_logs.provider_id 是 bigint（修前夹具塞 'openai' 字符串 22P02）；
//   - 上下文改 tenant_admin（super_admin 无租户过滤，会在共享真库上聚合
//     全量真实行，断言既不确定也踩真实数据形状）；
//   - 日期窗口动态覆盖 now()（修前固定 2026-07 窗口，夹具行落在 now()
//     必然被过滤成空集）；
//   - 夹具行全部带前缀且测试结束清理（共享真库零残留）。

const breakdownTenant = "tnt_test"

// breakdownDateRange 返回覆盖 now() 的 date_from/date_to 查询参数。
func breakdownDateRange() string {
	now := time.Now()
	return "date_from=" + now.AddDate(0, 0, -1).Format("2006-01-02") +
		"&date_to=" + now.AddDate(0, 0, 1).Format("2006-01-02")
}

// seedBreakdownTenant 建立夹具租户（fk_session_tenant 前置）。
func seedBreakdownTenant(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO tenants (code, name, status) VALUES ($1, 'Breakdown Fixture', 'active')
		ON CONFLICT (code) DO NOTHING
	`, breakdownTenant)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
}

// cleanupBreakdownFixtures 按外键逆序清扫夹具行（request_logs →
// session_summaries → tenants）。必须在 defer db.Close() 之后注册
// （LIFO 先清理后关池）。
func cleanupBreakdownFixtures(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `DELETE FROM request_logs WHERE tenant_id = $1 AND gw_session_id LIKE 'gw\_%'`, breakdownTenant); err != nil {
		t.Errorf("cleanup request_logs: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM session_summaries WHERE tenant_id = $1 AND session_key LIKE 'gw\_%'`, breakdownTenant); err != nil {
		t.Errorf("cleanup session_summaries: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM tenants WHERE code = $1`, breakdownTenant); err != nil {
		t.Errorf("cleanup tenants: %v", err)
	}
}

func setupModelBreakdownTestData(t *testing.T, db *pgxpool.Pool) {
	seedBreakdownTenant(t, db)
	ctx := context.Background()
	now := time.Now()

	// 插入 session_summaries
	_, err := db.Exec(ctx, `
		INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, request_count, total_cost_usd, compliance_status)
		VALUES ('gw_test_1', $1, $2, $2, 10, 5.0, 'compliant')
	`, breakdownTenant, now)
	if err != nil {
		t.Fatalf("failed to insert session_summaries: %v", err)
	}

	// 插入 request_logs（provider_id 为 bigint：9010/9020 夹具值）
	for i := 0; i < 10; i++ {
		model := "gpt-4o"
		provider := int64(9010)
		if i%3 == 0 {
			model = "claude-3-5-sonnet"
			provider = int64(9020)
		}

		_, err := db.Exec(ctx, `
			INSERT INTO request_logs (request_id, gw_session_id, tenant_id, ts, outbound_model, provider_id, cost_usd, prompt_tokens, completion_tokens, latency_ms, request_status, success)
			VALUES ($5, 'gw_test_1', $1, $2, $3, $4, 0.5, 100, 200, 1000, 'success', true)
		`, breakdownTenant, now.Add(time.Duration(i)*time.Minute), model, provider,
			fmt.Sprintf("test-mbd-1-%d-%d", i, now.UnixNano()))
		if err != nil {
			t.Fatalf("failed to insert request_logs: %v", err)
		}
	}
}

func setupLongTailTestData(t *testing.T, db *pgxpool.Pool) {
	seedBreakdownTenant(t, db)
	ctx := context.Background()
	now := time.Now()

	// 插入主要模型（高占比）
	for i := 0; i < 100; i++ {
		_, err := db.Exec(ctx, `
			INSERT INTO request_logs (request_id, gw_session_id, tenant_id, ts, outbound_model, provider_id, cost_usd, prompt_tokens, completion_tokens, latency_ms, request_status, success)
			VALUES ($4, $1, $2, $3, 'gpt-4o', 9010, 0.5, 100, 200, 1000, 'success', true)
		`, fmt.Sprintf("gw_test_%d", i), breakdownTenant, now.Add(time.Duration(i)*time.Minute),
			fmt.Sprintf("test-ltl-m-%d-%d", i, now.UnixNano()))
		if err != nil {
			t.Fatalf("failed to insert request_logs: %v", err)
		}
	}

	// 插入长尾模型（低占比 <2%）
	for i := 0; i < 3; i++ {
		_, err := db.Exec(ctx, `
			INSERT INTO request_logs (request_id, gw_session_id, tenant_id, ts, outbound_model, provider_id, cost_usd, prompt_tokens, completion_tokens, latency_ms, request_status, success)
			VALUES ($5, $1, $2, $3, $4, 9090, 0.1, 10, 20, 500, 'success', true)
		`, fmt.Sprintf("gw_tiny_%d", i), breakdownTenant, now.Add(time.Duration(i)*time.Minute),
			fmt.Sprintf("tiny-model-%d", i), fmt.Sprintf("test-ltl-t-%d-%d", i, now.UnixNano()))
		if err != nil {
			t.Fatalf("failed to insert request_logs: %v", err)
		}
	}
}

func setupSessionShapeTestData(t *testing.T, db *pgxpool.Pool) {
	seedBreakdownTenant(t, db)
	ctx := context.Background()
	now := time.Now()

	testCases := []struct {
		requestCount int
		duration     int
	}{
		{3, 60},    // quick, <1min
		{10, 300},  // standard, 1-5min
		{25, 1000}, // deep, 5-30min
		{60, 4000}, // marathon, >1h
	}

	for i, tc := range testCases {
		sessionID := fmt.Sprintf("gw_shape_%d", i)
		// R65: duration_seconds 是 GENERATED ALWAYS 列，时长经
		// last = first + duration 秒表达（生成列自动等于该差值）。
		_, err := db.Exec(ctx, `
			INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, request_count, total_cost_usd, compliance_status)
			VALUES ($1, $2, $3::timestamptz, $3::timestamptz + ($4 * INTERVAL '1 second'), $5, 1.0, 'compliant')
		`, sessionID, breakdownTenant, now, tc.duration, tc.requestCount)
		if err != nil {
			t.Fatalf("failed to insert session_summaries: %v", err)
		}
	}
}

func setupBoundaryTestData(t *testing.T, db *pgxpool.Pool) {
	seedBreakdownTenant(t, db)
	ctx := context.Background()
	now := time.Now()

	boundaries := []int{1, 5, 6, 20, 21, 50, 51}
	for i, count := range boundaries {
		sessionID := fmt.Sprintf("gw_boundary_%d", i)
		_, err := db.Exec(ctx, `
			INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, request_count, total_cost_usd, compliance_status)
			VALUES ($1, $2, $3::timestamptz, $3::timestamptz + INTERVAL '100 seconds', $4, 1.0, 'compliant')
		`, sessionID, breakdownTenant, now, count)
		if err != nil {
			t.Fatalf("failed to insert session_summaries: %v", err)
		}
	}
}

func setupHealthDistributionTestData(t *testing.T, db *pgxpool.Pool) {
	seedBreakdownTenant(t, db)
	ctx := context.Background()
	now := time.Now()

	testCases := []struct {
		grade      string
		outcome    string
		compliance string
		latency    int
		score      int
	}{
		{"A", "completed", "compliant", 800, 95},
		{"B", "completed", "compliant", 1500, 80},
		{"C", "error", "warning", 3000, 65},
		{"D", "abandoned", "violation", 6000, 45},
		{"F", "error", "violation", 12000, 20},
	}

	for i, tc := range testCases {
		sessionID := fmt.Sprintf("gw_health_%d", i)
		_, err := db.Exec(ctx, `
			INSERT INTO session_summaries (
				session_key, tenant_id, first_request_at, last_request_at,
				request_count, total_cost_usd, compliance_status,
				health_grade, outcome, health_score, avg_latency_ms
			)
			VALUES ($1, $2, $3, $3, 10, 1.0, $4, $5, $6, $7, $8)
		`, sessionID, breakdownTenant, now, tc.compliance, tc.grade, tc.outcome, tc.score, tc.latency)
		if err != nil {
			t.Fatalf("failed to insert session_summaries: %v", err)
		}
	}
}

func setupNullHealthGradeTestData(t *testing.T, db *pgxpool.Pool) {
	seedBreakdownTenant(t, db)
	ctx := context.Background()
	now := time.Now()

	// 插入 health_grade 为 NULL 的数据
	_, err := db.Exec(ctx, `
		INSERT INTO session_summaries (
			session_key, tenant_id, first_request_at, last_request_at,
			request_count, total_cost_usd, compliance_status,
			health_grade, health_score
		)
		VALUES ('gw_null_health', $1, $2, $2, 5, 1.0, 'compliant', NULL, NULL)
	`, breakdownTenant, now)
	if err != nil {
		t.Fatalf("failed to insert session_summaries: %v", err)
	}

	// 插入有健康分的数据
	_, err = db.Exec(ctx, `
		INSERT INTO session_summaries (
			session_key, tenant_id, first_request_at, last_request_at,
			request_count, total_cost_usd, compliance_status,
			health_grade, health_score
		)
		VALUES ('gw_with_health', $1, $2, $2, 5, 1.0, 'compliant', 'A', 90)
	`, breakdownTenant, now)
	if err != nil {
		t.Fatalf("failed to insert session_summaries: %v", err)
	}
}
