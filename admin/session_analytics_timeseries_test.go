package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestParseAnalyticsFilters_Success 测试过滤器解析成功
func TestParseAnalyticsFilters_Success(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/admin/session-analytics/activity?date_from=2026-07-01&date_to=2026-07-07&granularity=day", nil)
	
	filters, err := parseAnalyticsFilters(req)
	if err != nil {
		t.Fatalf("parseAnalyticsFilters failed: %v", err)
	}
	
	if filters.Granularity != "day" {
		t.Errorf("expected granularity=day, got %s", filters.Granularity)
	}
	
	expectedFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !filters.DateFrom.Equal(expectedFrom) {
		t.Errorf("expected date_from=%v, got %v", expectedFrom, filters.DateFrom)
	}
}

// TestParseAnalyticsFilters_AutoGranularity 测试自动粒度选择
func TestParseAnalyticsFilters_AutoGranularity(t *testing.T) {
	tests := []struct {
		name     string
		dateFrom string
		dateTo   string
		expected string
	}{
		{"7 days -> day", "2026-07-01", "2026-07-07", "day"},
		{"30 days -> day", "2026-06-01", "2026-06-30", "day"},
		{"60 days -> week", "2026-05-01", "2026-06-30", "week"},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/admin/session-analytics/activity?date_from="+tt.dateFrom+"&date_to="+tt.dateTo+"&granularity=auto", nil)
			filters, err := parseAnalyticsFilters(req)
			if err != nil {
				t.Fatalf("parseAnalyticsFilters failed: %v", err)
			}
			if filters.Granularity != tt.expected {
				t.Errorf("expected granularity=%s, got %s", tt.expected, filters.Granularity)
			}
		})
	}
}

// TestParseAnalyticsFilters_DateRangeValidation 测试日期范围校验
func TestParseAnalyticsFilters_DateRangeValidation(t *testing.T) {
	tests := []struct {
		name        string
		dateFrom    string
		dateTo      string
		expectError bool
		errorMsg    string
	}{
		{"missing date_from", "", "2026-07-07", true, "date_from and date_to are required"},
		{"missing date_to", "2026-07-01", "", true, "date_from and date_to are required"},
		{"date_from after date_to", "2026-07-07", "2026-07-01", true, "date_from must be before date_to"},
		{"range > 90 days", "2026-01-01", "2026-05-01", true, "date range cannot exceed 90 days"},
		{"valid range", "2026-07-01", "2026-07-07", false, ""},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/admin/session-analytics/activity?date_from="+tt.dateFrom+"&date_to="+tt.dateTo, nil)
			_, err := parseAnalyticsFilters(req)
			
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
		})
	}
}

// TestFillMissingDates 测试缺日补零逻辑
func TestFillMissingDates(t *testing.T) {
	dateFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	dateTo := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	
	series := []ActivityDataPoint{
		{Date: "2026-07-01", SessionCount: 10},
		{Date: "2026-07-03", SessionCount: 15},
	}
	
	filled := fillMissingDates(series, dateFrom, dateTo, "day")
	
	if len(filled) != 4 {
		t.Errorf("expected 4 data points (Jul 1-4), got %d", len(filled))
	}
	
	// 验证缺失日期被填充为 0
	if filled[1].Date != "2026-07-02" || filled[1].SessionCount != 0 {
		t.Errorf("expected 2026-07-02 with 0 sessions, got %s with %d", filled[1].Date, filled[1].SessionCount)
	}
}

// TestCalculateActivitySummary 测试活动汇总计算
func TestCalculateActivitySummary(t *testing.T) {
	series := []ActivityDataPoint{
		{Date: "2026-07-01", SessionCount: 10, RequestCount: 50},
		{Date: "2026-07-02", SessionCount: 20, RequestCount: 100},
		{Date: "2026-07-03", SessionCount: 15, RequestCount: 75},
	}
	
	summary := calculateActivitySummary(series)
	
	if summary.TotalSessions != 45 {
		t.Errorf("expected total_sessions=45, got %d", summary.TotalSessions)
	}
	
	if summary.TotalRequests != 225 {
		t.Errorf("expected total_requests=225, got %d", summary.TotalRequests)
	}
	
	if summary.PeakSessions != 20 {
		t.Errorf("expected peak_sessions=20, got %d", summary.PeakSessions)
	}
	
	if summary.PeakDate != "2026-07-02" {
		t.Errorf("expected peak_date=2026-07-02, got %s", summary.PeakDate)
	}
	
	expectedAvg := 15.0
	if summary.AvgDailySessions != expectedAvg {
		t.Errorf("expected avg_daily_sessions=%.1f, got %.1f", expectedAvg, summary.AvgDailySessions)
	}
}

// TestCalculateCostSummary_TrendDetection 测试成本趋势判定
func TestCalculateCostSummary_TrendDetection(t *testing.T) {
	tests := []struct {
		name         string
		series       []CostDataPoint
		expectedTrend string
	}{
		{
			name: "up trend",
			series: []CostDataPoint{
				{Date: "2026-07-01", TotalCostUSD: 10},
				{Date: "2026-07-02", TotalCostUSD: 12},
				{Date: "2026-07-03", TotalCostUSD: 20},
				{Date: "2026-07-04", TotalCostUSD: 25},
			},
			expectedTrend: "up",
		},
		{
			name: "down trend",
			series: []CostDataPoint{
				{Date: "2026-07-01", TotalCostUSD: 25},
				{Date: "2026-07-02", TotalCostUSD: 20},
				{Date: "2026-07-03", TotalCostUSD: 12},
				{Date: "2026-07-04", TotalCostUSD: 10},
			},
			expectedTrend: "down",
		},
		{
			name: "flat trend",
			series: []CostDataPoint{
				{Date: "2026-07-01", TotalCostUSD: 100},
				{Date: "2026-07-02", TotalCostUSD: 102},
				{Date: "2026-07-03", TotalCostUSD: 101},
				{Date: "2026-07-04", TotalCostUSD: 103},
			},
			expectedTrend: "flat",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := calculateCostSummary(tt.series)
			if summary.CostTrend != tt.expectedTrend {
				t.Errorf("expected trend=%s, got %s (pct=%.2f)", tt.expectedTrend, summary.CostTrend, summary.TrendPct)
			}
		})
	}
}

// TestHandleActivityTrend_MethodNotAllowed 测试方法不允许
func TestHandleActivityTrend_MethodNotAllowed(t *testing.T) {
	handler := &Handler{db: nil}
	
	req := httptest.NewRequest("POST", "/api/admin/session-analytics/activity", nil)
	w := httptest.NewRecorder()
	
	handler.HandleActivityTrend(w, req)
	
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

// TestHandleActivityTrend_DBNotAvailable 测试数据库不可用
func TestHandleActivityTrend_DBNotAvailable(t *testing.T) {
	handler := &Handler{db: nil}
	
	req := httptest.NewRequest("GET", "/api/admin/session-analytics/activity?date_from=2026-07-01&date_to=2026-07-07", nil)
	w := httptest.NewRecorder()
	
	handler.HandleActivityTrend(w, req)
	
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

// TestHandleActivityTrend_InvalidDateRange 测试无效日期范围
func TestHandleActivityTrend_InvalidDateRange(t *testing.T) {
	handler := &Handler{db: nil}
	
	req := httptest.NewRequest("GET", "/api/admin/session-analytics/activity?date_from=2026-07-07&date_to=2026-07-01", nil)
	w := httptest.NewRecorder()
	
	handler.HandleActivityTrend(w, req)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TestHandleCostTrend_MethodNotAllowed 测试成本趋势方法不允许
func TestHandleCostTrend_MethodNotAllowed(t *testing.T) {
	handler := &Handler{db: nil}
	
	req := httptest.NewRequest("POST", "/api/admin/session-analytics/cost-trend", nil)
	w := httptest.NewRecorder()
	
	handler.HandleCostTrend(w, req)
	
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

// TestHandleLatencyTrend_MethodNotAllowed 测试延迟趋势方法不允许
func TestHandleLatencyTrend_MethodNotAllowed(t *testing.T) {
	handler := &Handler{db: nil}
	
	req := httptest.NewRequest("POST", "/api/admin/session-analytics/latency-trend", nil)
	w := httptest.NewRecorder()
	
	handler.HandleLatencyTrend(w, req)
	
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

// TestHandleHealthTrend_MethodNotAllowed 测试健康趋势方法不允许
func TestHandleHealthTrend_MethodNotAllowed(t *testing.T) {
	handler := &Handler{db: nil}
	
	req := httptest.NewRequest("POST", "/api/admin/session-analytics/health-trend", nil)
	w := httptest.NewRecorder()
	
	handler.HandleHealthTrend(w, req)
	
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

// TestFillMissingCostDates 测试成本缺日补零
func TestFillMissingCostDates(t *testing.T) {
	dateFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	dateTo := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	
	series := []CostDataPoint{
		{Date: "2026-07-01", TotalCostUSD: 100},
		{Date: "2026-07-03", TotalCostUSD: 150},
	}
	
	filled := fillMissingCostDates(series, dateFrom, dateTo, "day")
	
	if len(filled) != 3 {
		t.Errorf("expected 3 data points, got %d", len(filled))
	}
	
	if filled[1].Date != "2026-07-02" || filled[1].TotalCostUSD != 0 {
		t.Errorf("expected 2026-07-02 with 0 cost, got %s with %.2f", filled[1].Date, filled[1].TotalCostUSD)
	}
}

// TestFillMissingLatencyDates 测试延迟缺日补零
func TestFillMissingLatencyDates(t *testing.T) {
	dateFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	dateTo := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	
	series := []LatencyDataPoint{
		{Date: "2026-07-01", P50LatencyMs: 100},
		{Date: "2026-07-03", P50LatencyMs: 150},
	}
	
	filled := fillMissingLatencyDates(series, dateFrom, dateTo, "day")
	
	if len(filled) != 3 {
		t.Errorf("expected 3 data points, got %d", len(filled))
	}
	
	if filled[1].Date != "2026-07-02" || filled[1].P50LatencyMs != 0 {
		t.Errorf("expected 2026-07-02 with 0 latency, got %s with %d", filled[1].Date, filled[1].P50LatencyMs)
	}
}

// TestFillMissingHealthDates 测试健康缺日补零
func TestFillMissingHealthDates(t *testing.T) {
	dateFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	dateTo := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	
	series := []HealthDataPoint{
		{Date: "2026-07-01", AvgHealthScore: 85},
		{Date: "2026-07-03", AvgHealthScore: 90},
	}
	
	filled := fillMissingHealthDates(series, dateFrom, dateTo, "day")
	
	if len(filled) != 3 {
		t.Errorf("expected 3 data points, got %d", len(filled))
	}
	
	if filled[1].Date != "2026-07-02" || filled[1].AvgHealthScore != 0 {
		t.Errorf("expected 2026-07-02 with 0 health score, got %s with %.2f", filled[1].Date, filled[1].AvgHealthScore)
	}
	
	// 验证 map 已初始化
	if filled[1].GradeDistribution == nil {
		t.Error("expected GradeDistribution to be initialized")
	}
}

// 集成测试需要真实数据库连接，这里提供框架
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Skip("Integration test requires database connection")
	
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://user:pass@localhost/testdb")
	if err != nil {
		t.Fatalf("failed to connect to test db: %v", err)
	}
	return pool
}

// TestHandleActivityTrend_Integration 集成测试（需要真实数据库）
func TestHandleActivityTrend_Integration(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()
	
	handler := &Handler{db: pool}
	
	req := httptest.NewRequest("GET", "/api/admin/session-analytics/activity?date_from=2026-07-01&date_to=2026-07-07&granularity=day", nil)
	w := httptest.NewRecorder()
	
	handler.HandleActivityTrend(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}
