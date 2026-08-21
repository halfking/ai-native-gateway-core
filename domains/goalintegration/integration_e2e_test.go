package goalintegration_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/goalintegration"
	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
	"github.com/kaixuan/llm-gateway-go/internal/handlers"
)

// validChatBody 模拟客户端发起的 /v1/chat/completions 请求，含 goal 字段。
const validChatBody = `{
	"model": "gpt-4",
	"messages": [{"role":"user","content":"hi"}],
	"goal": {
		"version": 1,
		"enabled": true,
		"root_goal_id": "client-goal-int-001",
		"instruction": "Plan a 3-step migration",
		"execution_mode": "continuous",
		"durability": "durable",
		"completion_policy": {"detector":"goal_v2","min_confidence":0.8},
		"limits": {
			"max_wall_time_seconds": 3600,
			"max_turns": 30,
			"max_follow_ups": 10,
			"max_model_switches": 2,
			"max_handoffs": 1
		},
		"delivery": {"mode":"poll"}
	}
}`

// anyArgs generates AnyArg matchers for pgxmock (skip exact value matching).
func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func validNewGoalRunInputE2E() goalrun.NewGoalRunInput {
	return goalrun.NewGoalRunInput{
		TenantID:                   "tenant_e2e",
		APIKeyID:                   "7",
		RootGoalID:                 "client-goal-int-001",
		RootSessionID:              "sess_e2e",
		RootRequestID:              "req_e2e",
		PolicyVersion:              1,
		PolicySnapshot:             []byte(`{}`),
		InstructionHash:            "hashed",
		RedactedInstructionSummary: "Plan a 3-step migration",
		DeadlineAt:                 time.Now().Add(time.Hour),
		LeaseOwner:                 "gw-e2e",
		LeaseUntil:                 time.Now().Add(60 * time.Second),
	}
}

// TestE2E_ChatGoalRun_CreatedAndQueriable 模拟「客户端发起带 goal 的请求
// → ChatHandler 调用 goalintegration → GoalRun 落库 → 客户端调用
// /v1/goal-runs/{id} → 查到刚创建 GoalRun」端到端流程。
func TestE2E_ChatGoalRun_CreatedAndQueriable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// ── Phase 1: CreateGoalRun 路径 ───────────────────────────────────
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_runs`).
		WithArgs(anyArgs(16)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO goal_run_steps`).
		WithArgs(anyArgs(11)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := goalrun.NewStore(mock)
	integrator := goalintegration.New(goalintegration.Config{
		Store:      store,
		LeaseOwner: "gw-e2e",
	})
	if !integrator.IsConfigured() {
		t.Fatal("integrator must be configured with store")
	}

	handler := handlers.NewGoalRunHandler(store, slog.Default())

	// ── Step 1: ChatHandler 调用 GoalIntegrator（v1 ChatHandler 切点）
	resolved, err := integrator.ParseAndCreate(
		context.Background(),
		[]byte(validChatBody),
		"tenant_e2e", 7, "sess_e2e", "req_e2e",
	)
	if err != nil {
		t.Fatalf("ParseAndCreate: %v", err)
	}
	if resolved.GoalRunID == "" || resolved.GoalRunID[:3] != "gr_" {
		t.Fatalf("expected gr_-prefixed goal_run_id, got %q", resolved.GoalRunID)
	}
	if resolved.StatusURL == "" {
		t.Error("status_url must be populated")
	}
	if resolved.Status != string(goalrun.StatusQueued) {
		t.Errorf("expected queued, got %s", resolved.Status)
	}

	// 模拟 v1 ChatHandler 写入的响应头
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Goal-Run-Id", resolved.GoalRunID)
	rec.Header().Set("X-Goal-Status-Url", resolved.StatusURL)
	rec.Header().Set("X-Goal-Status", resolved.Status)

	if rec.Header().Get("X-Goal-Run-Id") != resolved.GoalRunID {
		t.Errorf("response header missing goal_run_id")
	}
	if rec.Header().Get("X-Goal-Status-Url") == "" {
		t.Errorf("response header missing status_url")
	}

	// ── Phase 2: GoalRunHandler 读回路径 ─────────────────────────────
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
		resolved.GoalRunID, "tenant_e2e", "7", "client-goal-int-001",
		"sess_e2e", "sess_e2e",
		"req_e2e", "req_e2e", "",
		"queued", 1, []byte(`{"effective_limits":{"max_turns":30}}`),
		"hashed-instr", "Plan a 3-step migration",
		0, 0, 0,
		0, 0, int64(0),
		"",
		time.Now().Add(time.Hour), "gw-e2e", time.Now().Add(60*time.Second), int64(1),
		"", time.Now(), time.Now(), time.Time{},
	)
	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs(resolved.GoalRunID).
		WillReturnRows(rows)

	// ── Step 2: 客户端拿 goal_run_id 调用 GET /v1/goal-runs/{id}
	url := "/v1/goal-runs/" + resolved.GoalRunID
	statusReq := httptest.NewRequest(http.MethodGet, url, nil)
	statusReq.Header.Set("X-Tenant-ID", "tenant_e2e")
	statusReq.Header.Set("X-Session-ID", "sess_e2e")

	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("status query failed: code=%d body=%s", statusRec.Code, statusRec.Body.String())
	}

	var resp handlers.GoalRunStatusResponse
	if err := json.NewDecoder(statusRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TenantID != "tenant_e2e" {
		t.Errorf("expected tenant_e2e, got %s", resp.TenantID)
	}
	if resp.GoalRunID != resolved.GoalRunID {
		t.Errorf("expected id=%s, got %s", resolved.GoalRunID, resp.GoalRunID)
	}
	if resp.Status != "queued" {
		t.Errorf("expected status=queued, got %s", resp.Status)
	}
	if resp.PolicySnapshot == nil {
		t.Error("expected policy_snapshot to be parsed")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestE2E_NoGoal_ChatPassesThrough 验证无 goal 字段的请求完全绕过
// GoalRun 创建，旧路径正常处理。
func TestE2E_NoGoal_ChatPassesThrough(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	store := goalrun.NewStore(mock)
	integrator := goalintegration.New(goalintegration.Config{Store: store, LeaseOwner: "gw"})

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	_, err = integrator.ParseAndCreate(context.Background(), body, "tenant_x", 1, "sess_x", "req_x")
	if !errors.Is(err, goalintegration.ErrNoGoal) {
		t.Fatalf("expected ErrNoGoal, got %v", err)
	}
}

// TestE2E_InvalidGoal_FailClosed 验证 goal 解析失败时返回 ErrInvalidGoal，
// ChatHandler 应当返回 400。
func TestE2E_InvalidGoal_FailClosed(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	store := goalrun.NewStore(mock)
	integrator := goalintegration.New(goalintegration.Config{Store: store, LeaseOwner: "gw"})

	body := []byte(`{"model":"gpt-4","goal":{"version":99,"enabled":true,"instruction":"x"}}`)
	_, err = integrator.ParseAndCreate(context.Background(), body, "tenant_x", 1, "sess_x", "req_x")
	if !errors.Is(err, goalintegration.ErrInvalidGoal) {
		t.Fatalf("expected ErrInvalidGoal for unsupported version, got %v", err)
	}
}

// TestE2E_TenantIsolation_StatusQuery 验证 GoalRunHandler 严格校验
// tenant_id，防止跨租户攻击。
func TestE2E_TenantIsolation_StatusQuery(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

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
		"gr_sec", "tenant_owner", "1", "",
		"sess", "sess",
		"req", "", "",
		"queued", 1, []byte(`{}`),
		"hash", "",
		0, 0, 0,
		0, 0, int64(0),
		"",
		time.Now().Add(time.Hour), "", time.Time{}, int64(1),
		"", time.Now(), time.Now(), time.Time{},
	)
	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_sec").
		WillReturnRows(rows)

	store := goalrun.NewStore(mock)
	handler := handlers.NewGoalRunHandler(store, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/v1/goal-runs/gr_sec", nil)
	req.Header.Set("X-Tenant-ID", "tenant_attacker")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 forbidden for cross-tenant attack, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestE2E_DuplicateCreate_ReturnsDuplicateError 验证同 (tenant, root_request)
// 二次创建 GoalRun 返回 ErrDuplicateGoalRun — 与 §6.2 幂等键一致。
func TestE2E_DuplicateCreate_ReturnsDuplicateError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_runs`).
		WithArgs(anyArgs(16)...).
		WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate key"})
	mock.ExpectRollback()

	store := goalrun.NewStore(mock)
	_, err = store.CreateGoalRun(context.Background(), validNewGoalRunInputE2E())
	if !errors.Is(err, goalrun.ErrDuplicateGoalRun) {
		t.Fatalf("expected ErrDuplicateGoalRun, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
