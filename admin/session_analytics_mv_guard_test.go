package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestWriteAnalyticsQueryErr_MissingViewGuidance (R33 P-2)：42P01 缺视图
// 必须返回 503 + analytics_view_missing + 引导文案（视图名+迁移 357），
// 而不是裸 500。
func TestWriteAnalyticsQueryErr_MissingViewGuidance(t *testing.T) {
	rec := httptest.NewRecorder()
	err := &pgconn.PgError{
		Code:       "42P01",
		Message:    `relation "session_task_stats" does not exist`,
		TableName:  "session_task_stats",
		SchemaName: "public",
	}
	writeAnalyticsQueryErr(rec, "query failed", err)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for missing view, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error.Code != "analytics_view_missing" {
		t.Fatalf("expected code analytics_view_missing, got %q", body.Error.Code)
	}
	if !strings.Contains(body.Error.Detail, "session_task_stats") || !strings.Contains(body.Error.Detail, "357") {
		t.Fatalf("guidance should name the relation and migration 357, got %q", body.Error.Detail)
	}
}

// TestWriteAnalyticsQueryErr_OtherErrorsStay500：非 42P01 保持
// writeInternalErr 原语义（500 + op 文案），错误细节不外泄。
func TestWriteAnalyticsQueryErr_OtherErrorsStay500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAnalyticsQueryErr(rec, "query failed", errors.New("connection refused"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for non-42P01, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("internal error detail must not leak: %s", rec.Body.String())
	}
}

// TestWriteAnalyticsQueryErr_MessageFallbackRelationName：TableName 为空时
// 从错误 message 里抽取关系名（ExtractMissingRelationName 的 regex 兜底）。
func TestWriteAnalyticsQueryErr_MessageFallbackRelationName(t *testing.T) {
	rec := httptest.NewRecorder()
	err := &pgconn.PgError{
		Code:    "42P01",
		Message: `relation "session_client_task_matrix" does not exist`,
	}
	writeAnalyticsQueryErr(rec, "count failed", err)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "session_client_task_matrix") {
		t.Fatalf("fallback relation name missing: %s", rec.Body.String())
	}
}
