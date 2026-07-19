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

	// Mock 质量画像查询
	now := time.Now()
	mock.ExpectQuery(`SELECT.*FROM provider_quality_profiles.*WHERE provider_id = \$1.*ORDER BY quality_score DESC`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "stability_score", "cost_efficiency_score",
			"updated_at",
		}).
			AddRow("claude-3-opus", 95.5, "S", 98.0, 92.0, 94.0, 85.0, now).
			AddRow("claude-3-sonnet", 88.5, "A", 95.0, 88.0, 90.0, 80.0, now))

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
	// Mock 排行榜查询
	mock.ExpectQuery(`SELECT.*FROM provider_quality_profiles p.*LEFT JOIN providers pr.*ORDER BY.*LIMIT`).
		WithArgs("", 0.0, 20).
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

// TestHandleGetProviderQuality_WithModelFilter 测试按模型名称过滤
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

	// Mock 质量画像查询（带模型过滤）
	now := time.Now()
	mock.ExpectQuery(`SELECT.*FROM provider_quality_profiles.*WHERE provider_id = \$1 AND model_name = \$2`).
		WithArgs(int64(1), "claude-3-opus").
		WillReturnRows(sqlmock.NewRows([]string{
			"model_name", "quality_score", "quality_grade",
			"availability_score", "performance_score", "stability_score", "cost_efficiency_score",
			"updated_at",
		}).
			AddRow("claude-3-opus", 95.5, "S", 98.0, 92.0, 94.0, 85.0, now))

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

	// Mock 质量画像查询（无数据）
	mock.ExpectQuery(`SELECT.*FROM provider_quality_profiles.*WHERE provider_id = \$1.*ORDER BY quality_score DESC`).
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
	// Mock 排行榜查询（带过滤）
	mock.ExpectQuery(`SELECT.*FROM provider_quality_profiles p.*LEFT JOIN providers pr.*ORDER BY.*LIMIT`).
		WithArgs("claude-3-opus", 90.0, 10).
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

	// Mock 排行榜查询（无结果）
	mock.ExpectQuery(`SELECT.*FROM provider_quality_profiles p.*LEFT JOIN providers pr.*ORDER BY.*LIMIT`).
		WithArgs("non-existent-model", 99.0, 20).
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
