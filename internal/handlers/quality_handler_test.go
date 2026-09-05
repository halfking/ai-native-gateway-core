package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestQualityHandlerRouteBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		statusCode int
		code       int
	}{
		{name: "missing provider id", method: http.MethodGet, path: "/api/quality/providers/", statusCode: http.StatusBadRequest, code: 40001},
		{name: "non numeric provider id", method: http.MethodGet, path: "/api/quality/providers/not-a-number", statusCode: http.StatusBadRequest, code: 40001},
		{name: "wrong method for provider", method: http.MethodPost, path: "/api/quality/providers/1", statusCode: http.StatusMethodNotAllowed, code: 40501},
		{name: "unknown path", method: http.MethodGet, path: "/api/providers/1", statusCode: http.StatusNotFound, code: 40404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewQualityHandler(nil, nil)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.statusCode {
				t.Fatalf("expected HTTP %d, got %d", tt.statusCode, w.Code)
			}

			var resp Response
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Code != tt.code {
				t.Errorf("expected code %d, got %d", tt.code, resp.Code)
			}
		})
	}
}

// TestHandleGetProviderQuality_Success 测试成功获取供应商质量画像
func TestHandleGetProviderQuality_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	// Mock 供应商名称查询
	mock.ExpectQuery(`SELECT display_name FROM providers WHERE id = \$1`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"display_name"}).
			AddRow("Anthropic"))

	// Mock 质量画像查询 (BRIDGE: reads from provider_profile_daily, model_name ignored)
	now := time.Now()
	mock.ExpectQuery(`SELECT.*FROM provider_profile_daily.*WHERE provider_id = \$1.*ORDER BY profile_date DESC, total_score DESC`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "stability_score", "cost_efficiency_score",
			"updated_at",
		}).
			AddRow("claude-3-opus", 95.5, "S", 98.0, 92.0, 94.0, 85.0, now).
			AddRow("claude-3-sonnet", 88.5, "A", 95.0, 88.0, 90.0, 80.0, now))

	// Mock 请求统计查询（usage_ledger_with_current_month，近 30 天窗口）
	mock.ExpectQuery(`SELECT.*FROM usage_ledger_with_current_month.*WHERE provider_id = \$1.*INTERVAL '30 days'`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "month_requests", "week_requests", "day_requests",
			"success_count", "failure_count", "total_tokens",
		}).
			AddRow(int64(100), int64(30), int64(10), int64(5), int64(95), int64(5), int64(12345)))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/1", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %T", resp.Data)
	}
	stats, ok := data["request_stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected request_stats map, got %T", data["request_stats"])
	}
	if v, _ := stats["total_requests"].(float64); v != 100 {
		t.Errorf("expected total_requests 100, got %v", stats["total_requests"])
	}
	if v, _ := stats["total_tokens"].(float64); v != 12345 {
		t.Errorf("expected total_tokens 12345, got %v", stats["total_tokens"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetProviderQuality_ProviderNotFound 测试供应商不存在
func TestHandleGetProviderQuality_ProviderNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	// Mock 供应商不存在
	mock.ExpectQuery(`SELECT display_name FROM providers WHERE id = \$1`).
		WithArgs(int64(999)).
		WillReturnError(sql.ErrNoRows)

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/999", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 40401 {
		t.Errorf("expected code 40401, got %d", resp.Code)
	}

	if resp.Message != "供应商不存在" {
		t.Errorf("expected message '供应商不存在', got '%s'", resp.Message)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetRanking_Success 测试成功获取排行榜
func TestHandleGetRanking_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	now := time.Now()
	// Mock 排行榜查询 (BRIDGE: reads from provider_profile_daily, model_name ignored)
	mock.ExpectQuery(`SELECT.*FROM provider_profile_daily d.*LEFT JOIN providers pr.*ORDER BY.*LIMIT`).
		WithArgs(0.0, 20).
		WillReturnRows(sqlmock.NewRows([]string{
			"provider_id", "provider_name", "model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "updated_at",
		}).
			AddRow(1, "Anthropic", "claude-3-opus", 95.5, "S", 98.0, 92.0, now).
			AddRow(2, "OpenAI", "gpt-4", 88.5, "A", 92.0, 88.0, now).
			AddRow(3, "Azure", "gpt-35-turbo", 75.0, "B", 85.0, 80.0, now))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/ranking", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetProviderQuality_WithModelFilter 测试 model_name 过滤参数。
// BRIDGE: provider_profile_daily 数据源无 model 列，model_name 参数被显式忽略，
// 查询退化为不带模型过滤的供应商级汇总（与 Success 用例相同的单条 SQL）。
func TestHandleGetProviderQuality_WithModelFilter(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	// Mock 供应商名称查询
	mock.ExpectQuery(`SELECT display_name FROM providers WHERE id = \$1`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"display_name"}).
			AddRow("Anthropic"))

	// Mock 质量画像查询（model_name 参数被忽略，查询不带 AND model_name = $2）
	now := time.Now()
	mock.ExpectQuery(`SELECT.*FROM provider_profile_daily.*WHERE provider_id = \$1.*ORDER BY profile_date DESC, total_score DESC`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "stability_score", "cost_efficiency_score",
			"updated_at",
		}).
			AddRow("claude-3-opus", 95.5, "S", 98.0, 92.0, 94.0, 85.0, now))

	// Mock 请求统计查询（usage_ledger_with_current_month，近 30 天窗口）
	mock.ExpectQuery(`SELECT.*FROM usage_ledger_with_current_month.*WHERE provider_id = \$1.*INTERVAL '30 days'`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "month_requests", "week_requests", "day_requests",
			"success_count", "failure_count", "total_tokens",
		}).
			AddRow(int64(50), int64(50), int64(20), int64(3), int64(48), int64(2), int64(6000)))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/1?model_name=claude-3-opus", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetProviderQuality_NoQualityData 测试供应商存在但无质量数据
func TestHandleGetProviderQuality_NoQualityData(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	// Mock 供应商名称查询
	mock.ExpectQuery(`SELECT display_name FROM providers WHERE id = \$1`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"display_name"}).
			AddRow("Test Provider"))

	// Mock 质量画像查询（无数据，BRIDGE: reads from provider_profile_daily）
	mock.ExpectQuery(`SELECT.*FROM provider_profile_daily.*WHERE provider_id = \$1.*ORDER BY profile_date DESC, total_score DESC`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "stability_score", "cost_efficiency_score",
			"updated_at",
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/1", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 40402 {
		t.Errorf("expected code 40402, got %d", resp.Code)
	}

	if resp.Message != "暂无质量数据" {
		t.Errorf("expected message '暂无质量数据', got '%s'", resp.Message)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetProviderQuality_InvalidProviderID 测试无效的 provider_id
func TestHandleGetProviderQuality_InvalidProviderID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/invalid", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 40001 {
		t.Errorf("expected code 40001, got %d", resp.Code)
	}
}

// TestHandleGetRanking_WithFilters 测试带过滤条件的排行榜
func TestHandleGetRanking_WithFilters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	now := time.Now()
	// Mock 排行榜查询（BRIDGE: model_name 被忽略，args 仅为 minScore, limit）
	mock.ExpectQuery(`SELECT.*FROM provider_profile_daily d.*LEFT JOIN providers pr.*ORDER BY.*LIMIT`).
		WithArgs(90.0, 10).
		WillReturnRows(sqlmock.NewRows([]string{
			"provider_id", "provider_name", "model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "updated_at",
		}).
			AddRow(1, "Anthropic", "claude-3-opus", 95.5, "S", 98.0, 92.0, now))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/ranking?model_name=claude-3-opus&min_score=90&limit=10", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestServeHTTP_MethodNotAllowed 测试不支持的 HTTP 方法
func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/quality/providers/1", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 40501 {
		t.Errorf("expected code 40501, got %d", resp.Code)
	}
}

// TestHandleGetRanking_EmptyResult 测试排行榜无结果
func TestHandleGetRanking_EmptyResult(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	// Mock 排行榜查询（无结果，BRIDGE: model_name 被忽略，limit 默认 20）
	mock.ExpectQuery(`SELECT.*FROM provider_profile_daily d.*LEFT JOIN providers pr.*ORDER BY.*LIMIT`).
		WithArgs(99.0, 20).
		WillReturnRows(sqlmock.NewRows([]string{
			"provider_id", "provider_name", "model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "updated_at",
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/ranking?model_name=non-existent-model&min_score=99", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	// 验证返回成功（即使是空结果）
	if resp.Data == nil {
		t.Error("expected data not to be nil")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetSummary_Success 测试供应商级品质汇总
func TestHandleGetSummary_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)
	now := time.Now()

	mock.ExpectQuery(`SELECT DISTINCT ON \(d\.provider_id\).*FROM provider_profile_daily d.*LEFT JOIN providers pr.*ORDER BY d\.provider_id`).
		WillReturnRows(sqlmock.NewRows([]string{
			"provider_id", "provider_name", "model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "total_requests_24h", "updated_at",
		}).
			AddRow(1, "Anthropic", "claude-3-opus", 95.5, "S", 98.0, 92.0, int64(1200), now).
			AddRow(2, "OpenAI", nil, 88.0, "A", 90.0, 85.0, int64(500), now))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/summary", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %T", resp.Data)
	}
	if total, _ := data["total"].(float64); total != 2 {
		t.Errorf("expected total 2, got %v", data["total"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestHandleGetSummary_WrongMethod(t *testing.T) {
	handler := NewQualityHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/quality/summary", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// TestHandleGetProviderRequestStats_Success 测试仅返回近 30 天请求统计的轻量接口。
func TestHandleGetProviderRequestStats_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	mock.ExpectQuery(`SELECT 1 FROM providers WHERE id = \$1`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	// Mock 请求统计查询（usage_ledger_with_current_month，近 30 天窗口）
	mock.ExpectQuery(`SELECT.*FROM usage_ledger_with_current_month.*WHERE provider_id = \$1.*INTERVAL '30 days'`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "month_requests", "week_requests", "day_requests",
			"success_count", "failure_count", "total_tokens",
		}).
			AddRow(int64(77), int64(22), int64(6), int64(2), int64(70), int64(7), int64(9999)))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/1/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %T", resp.Data)
	}
	if v, _ := data["total_requests"].(float64); v != 77 {
		t.Errorf("expected total_requests 77, got %v", data["total_requests"])
	}
	if v, _ := data["total_tokens"].(float64); v != 9999 {
		t.Errorf("expected total_tokens 9999, got %v", data["total_tokens"])
	}
	if v, _ := data["success_count"].(float64); v != 70 {
		t.Errorf("expected success_count 70, got %v", data["success_count"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetProviderRequestStats_ModelFilter 测试按模型粒度统计（?model= 追加 raw_model_name 条件）。
func TestHandleGetProviderRequestStats_ModelFilter(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	mock.ExpectQuery(`SELECT 1 FROM providers WHERE id = \$1`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))

	// 断言查询含 provider_id=$1 + 30 天窗口 + 末尾追加的 raw_model_name = $2 且双参数绑定
	mock.ExpectQuery(`SELECT.*FROM usage_ledger_with_current_month.*WHERE provider_id = \$1.*INTERVAL '30 days'.*AND raw_model_name = \$2`).
		WithArgs(int64(1), "claude-3-opus").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "month_requests", "week_requests", "day_requests",
			"success_count", "failure_count", "total_tokens",
		}).
			AddRow(int64(10), int64(4), int64(1), int64(0), int64(9), int64(1), int64(1234)))

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/1/stats?model=claude-3-opus", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
	if data, ok := resp.Data.(map[string]interface{}); ok {
		if v, _ := data["total_requests"].(float64); v != 10 {
			t.Errorf("expected total_requests 10, got %v", data["total_requests"])
		}
	} else {
		t.Fatalf("expected data map, got %T", resp.Data)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestHandleGetProviderRequestStats_NotFound 测试供应商不存在时返回 404。
func TestHandleGetProviderRequestStats_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock db: %v", err)
	}
	defer db.Close()

	handler := NewQualityHandler(db, nil)

	mock.ExpectQuery(`SELECT 1 FROM providers WHERE id = \$1`).
		WithArgs(int64(999)).
		WillReturnError(sql.ErrNoRows)

	req := httptest.NewRequest(http.MethodGet, "/api/quality/providers/999/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", w.Code, w.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
