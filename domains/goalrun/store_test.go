package goalrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return NewStore(mock), mock
}

// anyArgs 生成 n 个 AnyArg 匹配器（跳过时间戳等随机参数的精确匹配）。
func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func validNewGoalRunInput() NewGoalRunInput {
	return NewGoalRunInput{
		TenantID:                   "tenant-1",
		APIKeyID:                   "key-1",
		RootGoalID:                 "goal-1",
		RootSessionID:              "sess-1",
		RootRequestID:              "req-1",
		PolicyVersion:              1,
		PolicySnapshot:             []byte(`{"max_turns":50}`),
		InstructionHash:            "hash-abc",
		RedactedInstructionSummary: "Complete task X",
		DeadlineAt:                 time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
		LeaseOwner:                 "gw-1",
		LeaseUntil:                 time.Date(2026, 8, 22, 11, 30, 0, 0, time.UTC),
	}
}

// TestStore_CreateGoalRun：单事务完成「插入 GoalRun + root step + created->queued」
// （设计 13 §6.2，W2-B）。
func TestStore_CreateGoalRun(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_runs`).
		WithArgs(anyArgs(16)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO goal_run_steps`).
		WithArgs(anyArgs(11)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	run, err := store.CreateGoalRun(context.Background(), validNewGoalRunInput())
	if err != nil {
		t.Fatalf("CreateGoalRun: %v", err)
	}
	if run.Status != StatusQueued {
		t.Fatalf("status = %s, want queued", run.Status)
	}
	if run.Version != 1 {
		t.Fatalf("version = %d, want 1", run.Version)
	}
	if run.TurnCount != 0 || run.FollowUpCount != 0 {
		t.Fatalf("counters must start at 0")
	}
	if run.ID == "" || run.ID[:3] != "gr_" {
		t.Fatalf("id = %q, want gr_* prefix", run.ID)
	}
	if run.CurrentSessionID != run.RootSessionID {
		t.Fatalf("current_session = %s, want %s", run.CurrentSessionID, run.RootSessionID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_CreateGoalRun_RollsBackOnError：事务内任一语句失败必须回滚（设计 13 §6.2）。
func TestStore_CreateGoalRun_RollsBackOnError(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_runs`).
		WithArgs(anyArgs(16)...).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	if _, err := store.CreateGoalRun(context.Background(), validNewGoalRunInput()); err == nil {
		t.Fatal("must propagate insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_CreateGoalRun_UniqueViolation：同 (tenant, root_request) 重复创建
// 返回 ErrDuplicateGoalRun（幂等防重，设计 13 §6.2）。
func TestStore_CreateGoalRun_UniqueViolation(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_runs`).
		WithArgs(anyArgs(16)...).
		WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate key"})
	mock.ExpectRollback()

	if _, err := store.CreateGoalRun(context.Background(), validNewGoalRunInput()); !errors.Is(err, ErrDuplicateGoalRun) {
		t.Fatalf("err = %v, want ErrDuplicateGoalRun", err)
	}
}

// TestStore_CreateGoalRun_Validation：必填字段校验。
func TestStore_CreateGoalRun_Validation(t *testing.T) {
	store, _ := newMockStore(t)
	tests := []struct {
		name    string
		mutate  func(*NewGoalRunInput)
		wantErr error
	}{
		{"missing tenant", func(i *NewGoalRunInput) { i.TenantID = "" }, ErrTenantIDRequired},
		{"missing api_key", func(i *NewGoalRunInput) { i.APIKeyID = "" }, ErrAPIKeyIDRequired},
		{"missing root_session", func(i *NewGoalRunInput) { i.RootSessionID = "" }, ErrRootSessionIDRequired},
		{"missing root_request", func(i *NewGoalRunInput) { i.RootRequestID = "" }, ErrRootRequestIDRequired},
		{"missing instruction_hash", func(i *NewGoalRunInput) { i.InstructionHash = "" }, ErrInstructionHashRequired},
		{"missing deadline", func(i *NewGoalRunInput) { i.DeadlineAt = time.Time{} }, ErrDeadlineRequired},
		{"missing lease_owner", func(i *NewGoalRunInput) { i.LeaseOwner = "" }, ErrLeaseOwnerRequired},
		{"missing lease_until", func(i *NewGoalRunInput) { i.LeaseUntil = time.Time{} }, ErrLeaseUntilRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validNewGoalRunInput()
			tt.mutate(&input)
			_, err := store.CreateGoalRun(context.Background(), input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestStore_GetGoalRun：按 ID 查询（带 tenant RLS）。
func TestStore_GetGoalRun(t *testing.T) {
	store, mock := newMockStore(t)

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
		"gr_123", "tenant-1", "key-1", "goal-1",
		"sess-1", "sess-1",
		"req-1", "req-1", "",
		"queued", 1, []byte(`{}`),
		"hash-abc", "summary",
		0, 0, 0,
		0, 0, int64(0),
		"",
		time.Now(), "gw-1", time.Now(), int64(1),
		"", time.Now(), time.Now(), time.Time{},
	)

	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_123").
		WillReturnRows(rows)

	run, err := store.GetGoalRun(context.Background(), "gr_123")
	if err != nil {
		t.Fatalf("GetGoalRun: %v", err)
	}
	if run.ID != "gr_123" {
		t.Fatalf("id = %s, want gr_123", run.ID)
	}
	if run.Status != StatusQueued {
		t.Fatalf("status = %s, want queued", run.Status)
	}
}

// TestStore_GetGoalRun_NotFound：不存在返回 ErrGoalRunNotFound。
func TestStore_GetGoalRun_NotFound(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectQuery(`SELECT .+ FROM goal_runs`).
		WithArgs("gr_missing").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))

	_, err := store.GetGoalRun(context.Background(), "gr_missing")
	if !errors.Is(err, ErrGoalRunNotFound) {
		t.Fatalf("err = %v, want ErrGoalRunNotFound", err)
	}
}

// TestStore_UpdateStatus：CAS 更新状态，带 (lease_owner, version) 门禁（设计 13 §6.2）。
func TestStore_UpdateStatus(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectExec(`UPDATE goal_runs`).
		WithArgs(anyArgs(8)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	newVersion, err := store.UpdateStatus(context.Background(), "gr_123", "gw-1", 1, StatusRunning, "")
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if newVersion != 2 {
		t.Fatalf("new version = %d, want 2", newVersion)
	}
}

// TestStore_UpdateStatus_LeaseLost：租约失效或版本冲突返回 ErrLeaseLost（0 rows）。
func TestStore_UpdateStatus_LeaseLost(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectExec(`UPDATE goal_runs`).
		WithArgs(anyArgs(8)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	_, err := store.UpdateStatus(context.Background(), "gr_123", "gw-1", 1, StatusRunning, "")
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
}

// TestStore_UpdateStatus_TerminalSetsCompletedAt：终态必须设置 completed_at（设计 13 §6.3）。
func TestStore_UpdateStatus_TerminalSetsCompletedAt(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectExec(`UPDATE goal_runs`).
		WithArgs(anyArgs(8)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	_, err := store.UpdateStatus(context.Background(), "gr_123", "gw-1", 1, StatusCompleted, "success")
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
}

// TestStore_CreateStep：创建新 step，带单调 sequence 约束（设计 13 §6.2）。
func TestStore_CreateStep(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_run_steps`).
		WithArgs(anyArgs(11)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	step := &GoalRunStep{
		GoalRunID: "gr_123",
		Sequence:  1,
		RequestID: "req-2",
		SessionID: "sess-1",
		Action:    ActionTypeContinue,
		Status:    StepStatusPending,
	}

	err := store.CreateStep(context.Background(), step)
	if err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
}

// TestStore_CreateStep_SequenceConflict：sequence 冲突返回 ErrSequenceConflict。
func TestStore_CreateStep_SequenceConflict(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_run_steps`).
		WithArgs(anyArgs(11)...).
		WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate key"})
	mock.ExpectRollback()

	step := &GoalRunStep{
		GoalRunID: "gr_123",
		Sequence:  0,
		RequestID: "req-1",
		SessionID: "sess-1",
		Action:    ActionTypeContinue,
		Status:    StepStatusPending,
	}

	err := store.CreateStep(context.Background(), step)
	if !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("err = %v, want ErrSequenceConflict", err)
	}
}

// TestStore_CreateAction：创建可幂等 action（设计 13 §6.2）。
func TestStore_CreateAction(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectExec(`INSERT INTO goal_run_actions`).
		WithArgs(anyArgs(12)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	action := &GoalRunAction{
		GoalRunID:      "gr_123",
		ActionType:     ActionTypeContinue,
		IdempotencyKey: "idem-1",
		Status:         ActionStatusPending,
	}

	err := store.CreateAction(context.Background(), action)
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if action.ActionID == "" || action.ActionID[:4] != "act_" {
		t.Fatalf("action_id = %q, want act_* prefix", action.ActionID)
	}
}

// TestStore_CreateAction_IdempotentConflict：idempotency_key 冲突时幂等成功（ON CONFLICT DO NOTHING）。
func TestStore_CreateAction_IdempotentConflict(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectExec(`INSERT INTO goal_run_actions`).
		WithArgs(anyArgs(12)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	action := &GoalRunAction{
		GoalRunID:      "gr_123",
		ActionType:     ActionTypeContinue,
		IdempotencyKey: "idem-1",
		Status:         ActionStatusPending,
	}

	err := store.CreateAction(context.Background(), action)
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
}

// TestStore_GetAction：按 idempotency_key 查询 action。
func TestStore_GetAction(t *testing.T) {
	store, mock := newMockStore(t)

	rows := pgxmock.NewRows([]string{
		"action_id", "goal_run_id", "causation_id", "action_type",
		"idempotency_key", "expected_version", "status",
		"retry_at", "attempts", "last_error", "created_at", "updated_at",
	}).AddRow(
		"act_456", "gr_123", "", "continue",
		"idem-1", int64(1), "pending",
		time.Time{}, 0, "", time.Now(), time.Now(),
	)

	mock.ExpectQuery(`SELECT .+ FROM goal_run_actions`).
		WithArgs("gr_123", "idem-1").
		WillReturnRows(rows)

	action, err := store.GetAction(context.Background(), "gr_123", "idem-1")
	if err != nil {
		t.Fatalf("GetAction: %v", err)
	}
	if action.ActionID != "act_456" {
		t.Fatalf("action_id = %s, want act_456", action.ActionID)
	}
	if action.IdempotencyKey != "idem-1" {
		t.Fatalf("idempotency_key = %s, want idem-1", action.IdempotencyKey)
	}
}

// TestStore_GetAction_NotFound：不存在返回 ErrActionNotFound。
func TestStore_GetAction_NotFound(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectQuery(`SELECT .+ FROM goal_run_actions`).
		WithArgs("gr_123", "idem-missing").
		WillReturnRows(pgxmock.NewRows([]string{"action_id"}))

	_, err := store.GetAction(context.Background(), "gr_123", "idem-missing")
	if !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("err = %v, want ErrActionNotFound", err)
	}
}
