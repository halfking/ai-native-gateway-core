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
