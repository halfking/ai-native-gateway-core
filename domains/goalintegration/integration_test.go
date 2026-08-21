package goalintegration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
)

// anyArgs 生成 n 个 AnyArg 匹配器（跳过时间戳等随机参数的精确匹配）。
func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func TestIntegrator_ParseAndCreate_NoGoal_ReturnsErrNoGoal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	integrator := New(Config{Store: goalrun.NewStore(mock), LeaseOwner: "gw_test"})

	body := []byte(`{"model":"gpt-4","messages":[]}`)
	_, err = integrator.ParseAndCreate(context.Background(), body, "tenant_a", 7, "sess_1", "req_1")
	if !errors.Is(err, ErrNoGoal) {
		t.Fatalf("expected ErrNoGoal, got %v", err)
	}
}

func TestIntegrator_ParseAndCreate_NullGoal_ReturnsErrNoGoal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	integrator := New(Config{Store: goalrun.NewStore(mock), LeaseOwner: "gw_test"})

	body := []byte(`{"model":"gpt-4","goal":null}`)
	_, err = integrator.ParseAndCreate(context.Background(), body, "tenant_a", 7, "sess_1", "req_1")
	if !errors.Is(err, ErrNoGoal) {
		t.Fatalf("expected ErrNoGoal for explicit null, got %v", err)
	}
}

func TestIntegrator_ParseAndCreate_EmptyBody_ReturnsErrNoGoal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	integrator := New(Config{Store: goalrun.NewStore(mock), LeaseOwner: "gw_test"})

	_, err = integrator.ParseAndCreate(context.Background(), []byte{}, "tenant_a", 7, "sess_1", "req_1")
	if !errors.Is(err, ErrNoGoal) {
		t.Fatalf("expected ErrNoGoal for empty body, got %v", err)
	}
}

func TestIntegrator_ParseAndCreate_InvalidGoal_ReturnsErrInvalidGoal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	integrator := New(Config{Store: goalrun.NewStore(mock), LeaseOwner: "gw_test"})

	// 缺 version=1
	body := []byte(`{"model":"gpt-4","goal":{"enabled":true,"instruction":"x"}}`)
	_, err = integrator.ParseAndCreate(context.Background(), body, "tenant_a", 7, "sess_1", "req_1")
	if !errors.Is(err, ErrInvalidGoal) {
		t.Fatalf("expected ErrInvalidGoal for unsupported version, got %v", err)
	}
}

func TestIntegrator_ParseAndCreate_StoreNil_ReturnsErrInvalidGoal(t *testing.T) {
	integrator := New(Config{Store: nil})

	body := []byte(`{"model":"gpt-4","goal":{"version":1,"enabled":true,"instruction":"x","execution_mode":"continuous","durability":"durable","completion_policy":{"detector":"d","min_confidence":0.5},"limits":{"max_wall_time_seconds":60,"max_turns":10,"max_follow_ups":5,"max_model_switches":1,"max_handoffs":1},"delivery":{"mode":"poll"}}}`)
	_, err := integrator.ParseAndCreate(context.Background(), body, "tenant_a", 7, "sess_1", "req_1")
	if !errors.Is(err, ErrInvalidGoal) {
		t.Fatalf("expected ErrInvalidGoal when store is nil, got %v", err)
	}
}

func TestIntegrator_ParseAndCreate_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Expect Begin + INSERT goal_runs + INSERT goal_run_steps + Commit.
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_runs`).
		WithArgs(anyArgs(16)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO goal_run_steps`).
		WithArgs(anyArgs(11)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := goalrun.NewStore(mock)
	integrator := New(Config{Store: store, LeaseOwner: "gw_test"})

	body := []byte(`{"model":"gpt-4","goal":{"version":1,"enabled":true,"root_goal_id":"client-goal-7","instruction":"Refactor module","execution_mode":"continuous","durability":"durable","completion_policy":{"detector":"goal_v2","min_confidence":0.8,"require_terminal_evidence":true},"limits":{"max_wall_time_seconds":3600,"max_turns":50,"max_follow_ups":20,"max_model_switches":3,"max_handoffs":2},"delivery":{"mode":"poll"}}}`)

	res, err := integrator.ParseAndCreate(context.Background(), body, "tenant_a", 7, "sess_1", "req_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.GoalRunID == "" || res.GoalRunID[:3] != "gr_" {
		t.Errorf("expected gr_-prefixed ID, got %q", res.GoalRunID)
	}
	if res.StatusURL != "/v1/goal-runs/"+res.GoalRunID {
		t.Errorf("expected status_url=/v1/goal-runs/%s, got %s", res.GoalRunID, res.StatusURL)
	}
	if res.Status != string(goalrun.StatusQueued) {
		t.Errorf("expected status=queued, got %s", res.Status)
	}
	if res.RootGoalID != "client-goal-7" {
		t.Errorf("expected root_goal_id from client, got %q", res.RootGoalID)
	}
	if res.InstructionHash == "" || len(res.InstructionHash) != 64 {
		t.Errorf("expected 64-char sha256 hex, got %q", res.InstructionHash)
	}
	if res.EffectiveLimits.MaxTurns != 50 {
		t.Errorf("expected MaxTurns=50, got %d", res.EffectiveLimits.MaxTurns)
	}
	if len(res.PolicySnapshot) == 0 {
		t.Error("expected non-empty policy_snapshot bytes")
	} else {
		var snap map[string]any
		if err := json.Unmarshal(res.PolicySnapshot, &snap); err != nil {
			t.Errorf("policy_snapshot not valid JSON: %v", err)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestIntegrator_ParseAndCreate_DefaultsRootGoalID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO goal_runs`).
		WithArgs(anyArgs(16)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO goal_run_steps`).
		WithArgs(anyArgs(11)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := goalrun.NewStore(mock)
	integrator := New(Config{Store: store, LeaseOwner: "gw_test"})

	// 没有 root_goal_id 字段 → 使用 rootRequestID 作为 fallback
	body := []byte(`{"model":"gpt-4","goal":{"version":1,"enabled":true,"instruction":"x","execution_mode":"single_shot","durability":"durable","completion_policy":{"detector":"d","min_confidence":0.5},"limits":{"max_wall_time_seconds":60,"max_turns":10,"max_follow_ups":5,"max_model_switches":1,"max_handoffs":1},"delivery":{"mode":"poll"}}}`)

	res, err := integrator.ParseAndCreate(context.Background(), body, "tenant_a", 7, "sess_1", "req_root_fallback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RootGoalID != "req_root_fallback" {
		t.Errorf("expected root_goal_id fallback to root_request_id, got %q", res.RootGoalID)
	}
}

func TestIntegrator_StatusBase_CustomPrefix(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	integrator := New(Config{Store: goalrun.NewStore(mock), StatusBase: "/api/v2/goal-runs"})
	if integrator.statusBase != "/api/v2/goal-runs" {
		t.Fatalf("expected custom statusBase, got %q", integrator.statusBase)
	}
}

func TestIntegrator_LeaseTTL_Default(t *testing.T) {
	integrator := New(Config{Store: nil})
	if integrator.leaseTTL != 60*time.Second {
		t.Fatalf("expected default 60s lease, got %s", integrator.leaseTTL)
	}
}
