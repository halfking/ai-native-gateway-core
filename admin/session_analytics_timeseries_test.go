package admin

// Tests for the timeseries analytics handlers (Task T1.1).
//
// NOTE: an earlier version of this file (written by a parallel agent) called
// parseAnalyticsFilters (which belongs to the breakdown package and returns
// *analyticsFilters) while asserting fields of *timeseriesFilters, and
// referenced exported field names that do not exist on either struct. It never
// compiled. This file was rewritten to test the real parseTimeseriesFilters and
// the fillMissing* helpers, plus handler-level DB-not-available / validation
// guards, which can run without a live database.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestParseTimeseriesFilters_Success 验证过滤器解析成功
func TestParseTimeseriesFilters_Success(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/activity?date_from=2026-07-01&date_to=2026-07-07&granularity=day", nil)

	filters, err := parseTimeseriesFilters(req)
	if err != nil {
		t.Fatalf("parseTimeseriesFilters failed: %v", err)
	}

	if filters.granularity != "day" {
		t.Errorf("expected granularity=day, got %s", filters.granularity)
	}

	// date_from 应为 2026-07-01 00:00:00 UTC
	expectedFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !filters.dateFrom.Equal(expectedFrom) {
		t.Errorf("expected date_from=%v, got %v", expectedFrom, filters.dateFrom)
	}
}

// TestParseTimeseriesFilters_AutoGranularity 测试自动粒度选择
func TestParseTimeseriesFilters_AutoGranularity(t *testing.T) {
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
			req := httptest.NewRequest(http.MethodGet,
				"/api/admin/session-analytics/activity?date_from="+tt.dateFrom+"&date_to="+tt.dateTo+"&granularity=auto", nil)
			filters, err := parseTimeseriesFilters(req)
			if err != nil {
				t.Fatalf("parseTimeseriesFilters failed: %v", err)
			}
			if filters.granularity != tt.expected {
				t.Errorf("expected granularity=%s, got %s", tt.expected, filters.granularity)
			}
		})
	}
}

// TestParseTimeseriesFilters_DateRangeValidation 测试日期范围校验
func TestParseTimeseriesFilters_DateRangeValidation(t *testing.T) {
	tests := []struct {
		name        string
		dateFrom    string
		dateTo      string
		expectError bool
	}{
		{"missing date_from", "", "2026-07-07", true},
		{"missing date_to", "2026-07-01", "", true},
		{"date_from after date_to", "2026-07-07", "2026-07-01", true},
		{"range > 90 days", "2026-01-01", "2026-05-01", true},
		{"valid range", "2026-07-01", "2026-07-07", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				"/api/admin/session-analytics/activity?date_from="+tt.dateFrom+"&date_to="+tt.dateTo, nil)
			_, err := parseTimeseriesFilters(req)
			if tt.expectError && err == nil {
				t.Errorf("expected error for %s, got nil", tt.name)
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error for %s: %v", tt.name, err)
			}
		})
	}
}

// TestParseTimeseriesFilters_ModelProvider 测试 model/provider 数组解析
func TestParseTimeseriesFilters_ModelProvider(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/activity?date_from=2026-07-01&date_to=2026-07-07&model=gpt-4o,claude-3&provider=openai", nil)

	filters, err := parseTimeseriesFilters(req)
	if err != nil {
		t.Fatalf("parseTimeseriesFilters failed: %v", err)
	}

	if len(filters.model) != 2 {
		t.Errorf("expected 2 models, got %d", len(filters.model))
	}
	if len(filters.provider) != 1 {
		t.Errorf("expected 1 provider, got %d", len(filters.provider))
	}
}

// TestFillMissingDates 验证缺日补零逻辑
// fillMissingActivityDates 遍历 [from, to) 半开区间（用 Before(to)），
// 因此 to 当天不包含在内。
func TestFillMissingDates(t *testing.T) {
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) // 区间 [7/1, 7/4) = 7/1,7/2,7/3 共 3 天

	// 只有 7月2日 的数据，应补出 7月1日 和 7月3日
	existing := []ActivityDataPoint{
		{Date: "2026-07-02", SessionCount: 5, RequestCount: 20},
	}

	filled := fillMissingActivityDates(existing, from, to, "day")
	if len(filled) != 3 {
		t.Fatalf("expected 3 days after fill, got %d", len(filled))
	}

	// 验证补零的日期
	byDate := make(map[string]int)
	for _, p := range filled {
		byDate[p.Date] = p.SessionCount
	}
	if byDate["2026-07-01"] != 0 {
		t.Errorf("expected 0 sessions for missing day 2026-07-01, got %d", byDate["2026-07-01"])
	}
	if byDate["2026-07-02"] != 5 {
		t.Errorf("expected 5 sessions for 2026-07-02, got %d", byDate["2026-07-02"])
	}
	if byDate["2026-07-03"] != 0 {
		t.Errorf("expected 0 sessions for missing day 2026-07-03, got %d", byDate["2026-07-03"])
	}
}

// TestFillMissingDates_EmptyInput 空输入直接返回（不补零）
func TestFillMissingDates_EmptyInput(t *testing.T) {
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	filled := fillMissingActivityDates(nil, from, to, "day")
	if len(filled) != 0 {
		t.Errorf("expected 0 for empty input, got %d", len(filled))
	}
}

// TestHandleActivityTrend_DBNotAvailable 无 DB 时应返回 503
func TestHandleActivityTrend_DBNotAvailable(t *testing.T) {
	h := &Handler{} // db == nil
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/activity?date_from=2026-07-01&date_to=2026-07-07", nil)
	req = setTestTenantContext(req, "tnt_test")
	w := httptest.NewRecorder()

	h.HandleActivityTrend(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when db nil, got %d", w.Code)
	}
}

// TestHandleCostTrend_DBNotAvailable 无 DB 时应返回 503
func TestHandleCostTrend_DBNotAvailable(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/cost-trend?date_from=2026-07-01&date_to=2026-07-07", nil)
	req = setTestTenantContext(req, "tnt_test")
	w := httptest.NewRecorder()

	h.HandleCostTrend(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when db nil, got %d", w.Code)
	}
}

// TestHandleLatencyTrend_DBNotAvailable 无 DB 时应返回 503
func TestHandleLatencyTrend_DBNotAvailable(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/latency-trend?date_from=2026-07-01&date_to=2026-07-07", nil)
	req = setTestTenantContext(req, "tnt_test")
	w := httptest.NewRecorder()

	h.HandleLatencyTrend(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when db nil, got %d", w.Code)
	}
}

// TestHandleHealthTrend_DBNotAvailable 无 DB 时应返回 503
func TestHandleHealthTrend_DBNotAvailable(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/health-trend?date_from=2026-07-01&date_to=2026-07-07", nil)
	req = setTestTenantContext(req, "tnt_test")
	w := httptest.NewRecorder()

	h.HandleHealthTrend(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when db nil, got %d", w.Code)
	}
}

// TestHandleActivityTrend_InvalidDateRange 日期校验由 parseTimeseriesFilters 负责。
// handler 在 db=nil 时会先返回 503，所以这里直接测过滤器解析层。
func TestHandleActivityTrend_InvalidDateRange(t *testing.T) {
	// date_from > date_to 应报错
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/activity?date_from=2026-07-07&date_to=2026-07-01", nil)
	_, err := parseTimeseriesFilters(req)
	if err == nil {
		t.Error("expected error for date_from > date_to, got nil")
	}
}

// TestHandleActivityTrend_MethodNotAllowed 非 GET 应返回 405
func TestHandleActivityTrend_MethodNotAllowed(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost,
		"/api/admin/session-analytics/activity?date_from=2026-07-01&date_to=2026-07-07", nil)
	req = setTestTenantContext(req, "tnt_test")
	w := httptest.NewRecorder()

	h.HandleActivityTrend(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST, got %d", w.Code)
	}
}
