package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
	"github.com/pashagolub/pgxmock/v4"
)

func TestGoalRunHandler_ServeHTTP_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	store := goalrun.NewStore(mock)
	handler := NewGoalRunHandler(store, slog.Default())

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
		WithArgs("gr_test123").
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	req.Header.Set("X-Tenant-ID", "tenant_abc")
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

	store := goalrun.NewStore(mock)
	handler := NewGoalRunHandler(store, slog.Default())

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

	store := goalrun.NewStore(mock)
	handler := NewGoalRunHandler(store, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/", nil)
	req.Header.Set("X-Tenant-ID", "tenant_abc")
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

	store := goalrun.NewStore(mock)
	handler := NewGoalRunHandler(store, slog.Default())

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

func TestGoalRunHandler_ServeHTTP_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	store := goalrun.NewStore(mock)
	handler := NewGoalRunHandler(store, slog.Default())

	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_notfound").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_notfound", nil)
	req.Header.Set("X-Tenant-ID", "tenant_abc")
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

	store := goalrun.NewStore(mock)
	handler := NewGoalRunHandler(store, slog.Default())

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
		"gr_test123", "tenant_owner", "", "",
		"session_root", "session_root",
		"req_root", "", "",
		"running", 1, []byte(`{}`),
		"hash", "",
		0, 0, 0,
		0, 0, int64(0),
		"",
		time.Now().Add(time.Hour), "", time.Time{}, int64(1),
		"", time.Now(), time.Now(), time.Time{},
	)

	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_test123").
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_test123", nil)
	req.Header.Set("X-Tenant-ID", "tenant_attacker")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Code != "forbidden" {
		t.Errorf("expected error code 'forbidden', got '%s'", errResp.Code)
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
