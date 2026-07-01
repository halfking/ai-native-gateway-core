package interception

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domain"               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestApprovalManagerCreator_NilManager(t *testing.T) {
	c := &ApprovalManagerCreator{mgr: nil}
	if _, err := c.Create(context.Background(), &domain.PipelineRequest{TenantID: "t1"}, &governance.ApprovalRequest{}); err == nil {
		t.Fatal("nil manager should error")
	}
}

func TestApprovalManagerCreator_NilOrEmptyEnv(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	c := NewApprovalManagerCreator(mgr)
	if _, err := c.Create(context.Background(), nil, &governance.ApprovalRequest{}); err == nil {
		t.Fatal("nil env should error")
	}
	if _, err := c.Create(context.Background(), &domain.PipelineRequest{}, &governance.ApprovalRequest{}); err == nil {
		t.Fatal("empty tenant should error")
	}
}

func TestApprovalManagerCreator_BuildsSessionauditRequest(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SET LOCAL app.current_tenant`).
		WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectExec(`INSERT INTO approval_queue`).
		WithArgs(
			pgxmock.AnyArg(),
			"sess-1",
			"tenant-1",
			"req-99",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			sessionaudit.ApprovalPending,
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	c := NewApprovalManagerCreator(mgr)

	env := &domain.PipelineRequest{
		TenantID:  "tenant-1",
		SessionID: "sess-1",
		Metadata: map[string]any{
			AuditResultKey: &sessionaudit.DetectResult{Score: 8, Decision: sessionaudit.DecisionNeedApproval, Reason: "jailbreak attempt"},
		},
	}
	id, err := c.Create(context.Background(), env, &governance.ApprovalRequest{
		Reason: "critical", RiskLevel: "high", RequestID: "req-99", SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty approval id")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet mock expectations: %v", err)
	}
}

func TestPickDetectResult(t *testing.T) {
	dr := &sessionaudit.DetectResult{Score: 9}
	env := &domain.PipelineRequest{Metadata: map[string]any{AuditResultKey: dr}}
	if pickDetectResult(env) != dr {
		t.Fatal("expected dr back")
	}
	if pickDetectResult(nil) != nil {
		t.Fatal("nil env should be nil")
	}
	if pickDetectResult(&domain.PipelineRequest{}) != nil {
		t.Fatal("empty metadata should be nil")
	}
}

func TestRiskLevelToDetectResult(t *testing.T) {
	cases := map[string]int{"low": 2, "medium": 5, "high": 7, "critical": 9, "unknown": 0}
	for lvl, want := range cases {
		got := riskLevelToDetectResult(lvl, "r")
		if got.Score != want {
			t.Errorf("level=%s score=%d, want %d", lvl, got.Score, want)
		}
	}
}

func TestPickSnapshot(t *testing.T) {
	snap := &sessionaudit.RequestSnapshot{SessionID: "s"}
	if pickSnapshot(snap) != snap {
		t.Fatal("typed snapshot should pass through")
	}
	if pickSnapshot("not-snapshot") != nil {
		t.Fatal("wrong type should be nil")
	}
	if pickSnapshot(nil) != nil {
		t.Fatal("nil should be nil")
	}
}
