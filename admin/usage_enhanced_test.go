package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ──────────────────────────────────────────────────────────────────────────
// Test 1: GET /api/admin/usage/cost-trend
// ──────────────────────────────────────────────────────────────────────────

func TestUsageCostTrend_ByModel(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}

	// 准备测试数据：插入一些使用记录
	ctx := context.Background()
	tenantID := "test-tenant-cost-trend"
	now := time.Now()

	// 创建租户
	_, _ = db.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $1) ON CONFLICT (id) DO NOTHING`, tenantID)

	// 插入测试数据到 usage_ledger（或 request_logs）
	testData := []struct {
		model   string
		cost    float64
		tokens  int
		success bool
	}{
		{"gpt-4o", 1.50, 10000, true},
		{"gpt-4o", 2.00, 15000, true},
		{"claude-3-5", 0.80, 8000, true},
		{"gpt-3.5-turbo", 0.20, 5000, true},
		{"gpt-3.5-turbo", 0.15, 4000, false},
	}

	for _, td := range testData {
		_, err := db.Exec(ctx, `
			INSERT INTO request_logs (
				request_id, tenant_id, ts, raw_model_name, outbound_model,
				cost_usd, total_tokens, prompt_tokens, completion_tokens, success
			) VALUES (
				gen_random_uuid()::text, $1, $2, $3, $3,
				$4, $5, $5/2, $5/2, $6
			)
		`, tenantID, now, td.model, td.cost, td.tokens, td.success)

		if err != nil {
			t.Logf("Warning: failed to insert test data: %v", err)
		}
	}

	// 测试请求
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/cost-trend?group_by=model&days=1", nil)
	req.Header.Set("X-Tenant-ID", tenantID)

	w := httptest.NewRecorder()
	h.usageCostTrend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CostTrendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证响应
	if resp.GroupBy != "model" {
		t.Errorf("expected group_by=model, got %s", resp.GroupBy)
	}

	if len(resp.Entries) == 0 {
		t.Error("expected at least one entry")
	}

	// 验证成本排序（降序）
	for i := 1; i < len(resp.Entries); i++ {
		if resp.Entries[i].TotalCostUSD > resp.Entries[i-1].TotalCostUSD {
			t.Errorf("entries not sorted by cost DESC: entry[%d]=%.2f > entry[%d]=%.2f",
				i, resp.Entries[i].TotalCostUSD, i-1, resp.Entries[i-1].TotalCostUSD)
		}
	}

	// 清理
	_, _ = db.Exec(ctx, `DELETE FROM request_logs WHERE tenant_id = $1`, tenantID)
	_, _ = db.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
}

func TestUsageCostTrend_ByProvider(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/cost-trend?group_by=provider&days=7", nil)
	w := httptest.NewRecorder()
	h.usageCostTrend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CostTrendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.GroupBy != "provider" {
		t.Errorf("expected group_by=provider, got %s", resp.GroupBy)
	}
}

func TestUsageCostTrend_InvalidGroupBy(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/cost-trend?group_by=invalid", nil)
	w := httptest.NewRecorder()
	h.usageCostTrend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	body := w.Body.String()
	if body == "" {
		t.Error("expected error message in response body")
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test 2: GET /api/admin/usage/period-compare
// ──────────────────────────────────────────────────────────────────────────

func TestUsagePeriodCompare_MonthOverMonth(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}
	ctx := context.Background()
	tenantID := "test-tenant-period-compare"

	// 创建租户
	_, _ = db.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $1) ON CONFLICT (id) DO NOTHING`, tenantID)

	// 插入当前月数据（2026-07）
	currentMonth, _ := time.Parse("2006-01", "2026-07")
	for i := 0; i < 5; i++ {
		_, _ = db.Exec(ctx, `
			INSERT INTO request_logs (
				request_id, tenant_id, ts, raw_model_name,
				cost_usd, total_tokens, prompt_tokens, completion_tokens, success
			) VALUES (
				gen_random_uuid()::text, $1, $2, 'gpt-4o',
				2.00, 10000, 5000, 5000, true
			)
		`, tenantID, currentMonth.Add(time.Duration(i)*24*time.Hour))
	}

	// 插入上月数据（2026-06）
	previousMonth, _ := time.Parse("2006-01", "2026-06")
	for i := 0; i < 3; i++ {
		_, _ = db.Exec(ctx, `
			INSERT INTO request_logs (
				request_id, tenant_id, ts, raw_model_name,
				cost_usd, total_tokens, prompt_tokens, completion_tokens, success
			) VALUES (
				gen_random_uuid()::text, $1, $2, 'gpt-4o',
				1.50, 8000, 4000, 4000, true
			)
		`, tenantID, previousMonth.Add(time.Duration(i)*24*time.Hour))
	}

	// 测试请求
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/period-compare?current=2026-07&previous=2026-06", nil)
	req.Header.Set("X-Tenant-ID", tenantID)

	w := httptest.NewRecorder()
	h.usagePeriodCompare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp PeriodCompareResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证当前周期
	if resp.Current.Period != "2026-07" {
		t.Errorf("expected current period 2026-07, got %s", resp.Current.Period)
	}

	// 验证对比周期
	if resp.Previous.Period != "2026-06" {
		t.Errorf("expected previous period 2026-06, got %s", resp.Previous.Period)
	}

	// 验证趋势判断（当前 5*2=10 > 之前 3*1.5=4.5，应该是 up）
	if resp.Current.TotalCostUSD <= resp.Previous.TotalCostUSD {
		t.Logf("Warning: expected current cost > previous cost, got current=%.2f, previous=%.2f",
			resp.Current.TotalCostUSD, resp.Previous.TotalCostUSD)
	}

	// 清理
	_, _ = db.Exec(ctx, `DELETE FROM request_logs WHERE tenant_id = $1`, tenantID)
	_, _ = db.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
}

func TestUsagePeriodCompare_MissingParams(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}

	// 缺少 previous 参数
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/period-compare?current=2026-07", nil)
	w := httptest.NewRecorder()
	h.usagePeriodCompare(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestUsagePeriodCompare_InvalidPeriodFormat(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}

	// 错误的日期格式
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/period-compare?current=2026-07-15&previous=2026-06", nil)
	w := httptest.NewRecorder()
	h.usagePeriodCompare(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test 3: GET /api/admin/usage/cache-economics
// ──────────────────────────────────────────────────────────────────────────

func TestUsageCacheEconomics_BasicCalculation(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}
	ctx := context.Background()
	tenantID := "test-tenant-cache-economics"

	// 创建租户
	_, _ = db.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $1) ON CONFLICT (id) DO NOTHING`, tenantID)

	now := time.Now()

	// 插入缓存命中的请求
	_, _ = db.Exec(ctx, `
		INSERT INTO request_logs (
			request_id, tenant_id, ts, raw_model_name,
			cost_usd, total_tokens, prompt_tokens, completion_tokens,
			cache_read_tokens, success
		) VALUES (
			gen_random_uuid()::text, $1, $2, 'gpt-4o',
			1.00, 10000, 3000, 5000, 2000, true
		)
	`, tenantID, now)

	// 插入普通请求（无缓存）
	_, _ = db.Exec(ctx, `
		INSERT INTO request_logs (
			request_id, tenant_id, ts, raw_model_name,
			cost_usd, total_tokens, prompt_tokens, completion_tokens,
			cache_read_tokens, success
		) VALUES (
			gen_random_uuid()::text, $1, $2, 'gpt-4o',
			1.50, 15000, 8000, 7000, 0, true
		)
	`, tenantID, now)

	// 测试请求
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/cache-economics?days=1", nil)
	req.Header.Set("X-Tenant-ID", tenantID)

	w := httptest.NewRecorder()
	h.usageCacheEconomics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CacheEconomicsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证基础字段
	if resp.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", resp.TotalRequests)
	}

	if resp.CacheReadTokens != 2000 {
		t.Errorf("expected 2000 cache_read_tokens, got %d", resp.CacheReadTokens)
	}

	if resp.PromptTokens != 11000 { // 3000 + 8000
		t.Errorf("expected 11000 prompt_tokens, got %d", resp.PromptTokens)
	}

	// 验证缓存命中率计算
	// cache_hit_ratio = 2000 / (2000 + 11000) = 0.1538...
	expectedRatio := 2000.0 / 13000.0
	if resp.CacheHitRatio < expectedRatio-0.01 || resp.CacheHitRatio > expectedRatio+0.01 {
		t.Errorf("expected cache_hit_ratio ~%.4f, got %.4f", expectedRatio, resp.CacheHitRatio)
	}

	// 验证节省金额（应该 > 0）
	if resp.DollarsSaved <= 0 {
		t.Errorf("expected dollars_saved > 0, got %.4f", resp.DollarsSaved)
	}

	// 验证有效成本占比（应该 < 1）
	if resp.EffectiveCostRatio >= 1.0 {
		t.Errorf("expected effective_cost_ratio < 1.0, got %.4f", resp.EffectiveCostRatio)
	}

	// 清理
	_, _ = db.Exec(ctx, `DELETE FROM request_logs WHERE tenant_id = $1`, tenantID)
	_, _ = db.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
}

func TestUsageCacheEconomics_NoCache(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}
	ctx := context.Background()
	tenantID := "test-tenant-no-cache"

	// 创建租户
	_, _ = db.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $1) ON CONFLICT (id) DO NOTHING`, tenantID)

	now := time.Now()

	// 插入无缓存的请求
	_, _ = db.Exec(ctx, `
		INSERT INTO request_logs (
			request_id, tenant_id, ts, raw_model_name,
			cost_usd, total_tokens, prompt_tokens, completion_tokens,
			cache_read_tokens, success
		) VALUES (
			gen_random_uuid()::text, $1, $2, 'gpt-3.5-turbo',
			0.50, 5000, 2000, 3000, 0, true
		)
	`, tenantID, now)

	// 测试请求
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/cache-economics?days=1", nil)
	req.Header.Set("X-Tenant-ID", tenantID)

	w := httptest.NewRecorder()
	h.usageCacheEconomics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CacheEconomicsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 无缓存时，命中率应该为 0
	if resp.CacheHitRatio != 0.0 {
		t.Errorf("expected cache_hit_ratio = 0.0 with no cache, got %.4f", resp.CacheHitRatio)
	}

	// 无缓存时，节省应该为 0
	if resp.DollarsSaved != 0.0 {
		t.Errorf("expected dollars_saved = 0.0 with no cache, got %.4f", resp.DollarsSaved)
	}

	// 清理
	_, _ = db.Exec(ctx, `DELETE FROM request_logs WHERE tenant_id = $1`, tenantID)
	_, _ = db.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
}

func TestUsageCacheEconomics_WithCompression(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}
	ctx := context.Background()
	tenantID := "test-tenant-compression"

	// 创建租户
	_, _ = db.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $1) ON CONFLICT (id) DO NOTHING`, tenantID)

	now := time.Now()

	// 插入带压缩的请求
	_, _ = db.Exec(ctx, `
		INSERT INTO request_logs (
			request_id, tenant_id, ts, raw_model_name,
			cost_usd, total_tokens, prompt_tokens, completion_tokens,
			cache_read_tokens, compression_strategy, success
		) VALUES (
			gen_random_uuid()::text, $1, $2, 'gpt-4o',
			1.20, 12000, 5000, 7000, 0, 'smart', true
		)
	`, tenantID, now)

	// 测试请求
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/cache-economics?days=1", nil)
	req.Header.Set("X-Tenant-ID", tenantID)

	w := httptest.NewRecorder()
	h.usageCacheEconomics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CacheEconomicsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证压缩请求数
	if resp.CompressedRequests != 1 {
		t.Errorf("expected 1 compressed request, got %d", resp.CompressedRequests)
	}

	// 验证压缩节省（应该 > 0）
	if resp.CompressionSaved <= 0 {
		t.Errorf("expected compression_saved > 0, got %.4f", resp.CompressionSaved)
	}

	// 验证总节省
	if resp.TotalSaved != resp.DollarsSaved+resp.CompressionSaved {
		t.Errorf("total_saved mismatch: expected %.4f, got %.4f",
			resp.DollarsSaved+resp.CompressionSaved, resp.TotalSaved)
	}

	// 清理
	_, _ = db.Exec(ctx, `DELETE FROM request_logs WHERE tenant_id = $1`, tenantID)
	_, _ = db.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
}

// ──────────────────────────────────────────────────────────────────────────
// Helper: testDB 返回测试数据库连接
// ──────────────────────────────────────────────────────────────────────────

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	// 尝试连接测试数据库
	dbURL := "postgres://localhost/llm_gateway_test?sslmode=disable"

	ctx := context.Background()
	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Logf("Skipping test: cannot connect to test database: %v", err)
		return nil
	}

	// 测试连接
	if err := db.Ping(ctx); err != nil {
		t.Logf("Skipping test: cannot ping test database: %v", err)
		db.Close()
		return nil
	}

	return db
}
