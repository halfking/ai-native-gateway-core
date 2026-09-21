package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
	"github.com/pashagolub/pgxmock/v4"
)

// goalrunStubVerifier 是 KeyVerifier 测试桩（§0-F2：tenant 来自验证后的
// key，而非 X-Tenant-ID 头）。
type goalrunStubVerifier struct {
	tenant  string
	err     error
	enabled bool
}

func (s *goalrunStubVerifier) Enabled() bool { return s.enabled || s.tenant != "" || s.err != nil }
func (s *goalrunStubVerifier) Verify(_ context.Context, _ string) (GoalRunKeyInfo, error) {
	if s.err != nil {
		return GoalRunKeyInfo{}, s.err
	}
	return GoalRunKeyInfo{ID: 1, TenantID: s.tenant}, nil
}

func newAuthedGoalRunHandler(mock pgxmock.PgxPoolIface, tenant string) *GoalRunHandler {
	handler := NewGoalRunHandler(goalrun.NewStore(mock), slog.Default())
	handler.SetAuth(&goalrunStubVerifier{tenant: tenant})
	return handler
}

func TestGoalRunHandler_ServeHTTP_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	handler := newAuthedGoalRunHandler(mock, "tenant_abc")

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "api_key_id", "root_goal_id",
		"root_session_id", "current_session_id",
		"root_request_id", "last_request_id", "last_durable_task_id",
		"status", "policy_version", "policy_snapshot",
		"instruction_hash", "redacted_instruction_summary",
		"turn_count", "follow_up_count", "retry_count",
		"model_switch_count", "handoff_count", "tokens_used",
		"last_progress_hash",
		"deadline_at", "lease_owner", "lease_until", "version",
		"terminal_reason", "created_at", "updated_at", "completed_at",
	}).AddRow(
		"gr_test123", "tenant_abc", "key_123", "goal_1",
		"session_root", "session_current",
		"req_root", "req_last", "",
		"running", 1, []byte(`{"max_turns": 50}`),
		"hash_abc", "summary",
		5, 3, 1,
		0, 0, int64(10000),
		"",
		time.Now().Add(24*time.Hour), "gw_1", time.Now().Add(time.Hour), int64(1),
		"", time.Now().Add(-1*time.Hour), time.Now(), time.Time{},
	)

	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_test123", "tenant_abc").
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("X-Session-ID", "session_root")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp GoalRunStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.GoalRunID != "gr_test123" {
		t.Errorf("expected goal_run_id 'gr_test123', got '%s'", resp.GoalRunID)
	}

	if resp.Status != "running" {
		t.Errorf("expected status 'running', got '%s'", resp.Status)
	}

	if resp.TenantID != "tenant_abc" {
		t.Errorf("expected tenant_id 'tenant_abc', got '%s'", resp.TenantID)
	}

	if resp.TurnCount != 5 {
		t.Errorf("expected turn_count 5, got %d", resp.TurnCount)
	}

	if resp.PolicySnapshot == nil {
		t.Error("expected policy_snapshot to be parsed")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGoalRunHandler_ServeHTTP_MethodNotAllowed(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	handler := newAuthedGoalRunHandler(mock, "tenant_abc")

	req := httptest.NewRequest(http.MethodPost, "/v1/goal-runs/gr_test123", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestGoalRunHandler_ServeHTTP_MissingGoalRunID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	handler := newAuthedGoalRunHandler(mock, "tenant_abc")

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestGoalRunHandler_ServeHTTP_MissingTenantID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	handler := newAuthedGoalRunHandler(mock, "tenant_abc")

	// 无 key → InvalidKeyError → 401（§0-F2：tenant 来自验证后的 key）。
	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Code != "unauthorized" {
		t.Errorf("expected error code 'unauthorized', got '%s'", errResp.Code)
	}
}

// §0-F2：verifier 未配置 → fail-closed 503（绝不回退到信任 X-Tenant-ID）。
func TestGoalRunHandler_ServeHTTP_AuthUnavailable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	handler := NewGoalRunHandler(goalrun.NewStore(mock), slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	req.Header.Set("X-Tenant-ID", "tenant_abc") // 攻击者头：verifier 未配置时也无效
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 (fail-closed), got %d", w.Code)
	}
}

// §0-F2：store 为 nil → 503（原实现会 panic）。
func TestGoalRunHandler_ServeHTTP_NilStore(t *testing.T) {
	handler := NewGoalRunHandler(nil, slog.Default())
	handler.SetAuth(&goalrunStubVerifier{tenant: "tenant_abc"})

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestGoalRunHandler_ServeHTTP_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	handler := newAuthedGoalRunHandler(mock, "tenant_abc")

	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_notfound", "tenant_abc").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_notfound", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Code != "not_found" {
		t.Errorf("expected error code 'not_found', got '%s'", errResp.Code)
	}
}

func TestGoalRunHandler_ServeHTTP_TenantMismatch(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// 验证后的 key 属于 tenant_attacker，而 run 属于 tenant_owner → 403。
	handler := newAuthedGoalRunHandler(mock, "tenant_attacker")

	// R29：查询层带 tenant 谓词后，攻击者租户（tenant_attacker）在 SQL 层
	// 即 0 行 → 404（不泄露 run 存在性，与 hostedtask 约定一致）；
	// 归属不匹配的行根本不会返回给 handler 做 403 比对。
	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_test123", "tenant_attacker").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Code != "not_found" {
		t.Errorf("expected error code 'not_found', got '%s'", errResp.Code)
	}
}

func TestGoalRunHandler_ServeHTTP_InvalidKey(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	handler := NewGoalRunHandler(goalrun.NewStore(mock), slog.Default())
	handler.SetAuth(&goalrunStubVerifier{err: &authentication.InvalidKeyError{Message: "invalid"}})

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	req.Header.Set("Authorization", "Bearer sk-bad")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 (cross-package InvalidKeyError assertion), got %d", w.Code)
	}
}

func TestGoalRunHandler_ExtractGoalRunID(t *testing.T) {
	handler := &GoalRunHandler{}

	tests := []struct {
		path     string
		expected string
	}{
		{"/v1/goal-runs/gr_test123", "gr_test123"},
		{"/v1/goal-runs/gr_abc", "gr_abc"},
		{"/v1/goal-runs/", ""},
		{"/v1/goal-runs", ""},
		{"/api/v1/goal-runs/gr_test", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := handler.extractGoalRunID(tt.path)
			if result != tt.expected {
				t.Errorf("extractGoalRunID(%q) = %q, expected %q", tt.path, result, tt.expected)
			}
		})
	}
}
