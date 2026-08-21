package goalrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// actionRowCols 是 store_action_claim.go 的完整 RETURNING 列。
var actionRowCols = []string{
	"action_id", "goal_run_id", "causation_id", "action_type",
	"idempotency_key", "expected_version", "status",
	"retry_at", "attempts", "last_error",
	"lease_owner", "lease_until", "fencing_token", "claimed_at",
	"created_at", "updated_at",
}

// TestStore_ClaimRunnableActions_Success：单事务批量 claim 多个 action
//（设计 13 §6.2，Wave 3-A）。
func TestStore_ClaimRunnableActions_Success(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(60 * time.Second)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
		WithArgs(4, now).
		WillReturnRows(
			pgxmock.NewRows([]string{"action_id"}).
				AddRow("act_a").
				AddRow("act_b"),
		)

	// 第一个 UPDATE 命中
	mock.ExpectQuery(`UPDATE goal_run_actions`).
		WithArgs("act_a", "gw-1", leaseUntil, now).
		WillReturnRows(pgxmock.NewRows(actionRowCols).AddRow(
			"act_a", "gr_1", "parent_1", "continue",
			"idem_a", int64(1), "running",
			now, 1, "",
			"gw-1", leaseUntil, int64(1), now,
			now, now,
		))
	mock.ExpectQuery(`UPDATE goal_run_actions`).
		WithArgs("act_b", "gw-1", leaseUntil, now).
		WillReturnRows(pgxmock.NewRows(actionRowCols).AddRow(
			"act_b", "gr_1", "parent_1", "handoff",
			"idem_b", int64(1), "running",
			now, 1, "",
			"gw-1", leaseUntil, int64(1), now,
			now, now,
		))

	mock.ExpectCommit()

	actions, err := store.ClaimRunnableActions(context.Background(), ClaimActionOptions{
		Owner: "gw-1",
		Now:   now,
	})
	if err != nil {
		t.Fatalf("ClaimRunnableActions: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("claimed = %d, want 2", len(actions))
	}
	for _, a := range actions {
		if a.LeaseOwner != "gw-1" {
			t.Fatalf("lease_owner = %s, want gw-1", a.LeaseOwner)
		}
		if a.FencingToken != 1 {
			t.Fatalf("token = %d, want 1", a.FencingToken)
		}
		if a.Status != ActionStatusRunning {
			t.Fatalf("status = %s, want running", a.Status)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestStore_ClaimRunnableActions_NoRunnable：无可执行 action 时返回空
// 切片（正常分支）。
func TestStore_ClaimRunnableActions_NoRunnable(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
		WithArgs(4, now).
		WillReturnRows(pgxmock.NewRows([]string{"action_id"}))
	mock.ExpectCommit()

	actions, err := store.ClaimRunnableActions(context.Background(), ClaimActionOptions{
		Owner: "gw-1",
		Now:   now,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("len = %d, want 0", len(actions))
	}
}

// TestStore_ClaimRunnableActions_RaceLosesRow：SELECT 命中但 UPDATE 因
// lease 被夺返回 0 行（pgx.ErrNoRows）时，跳过该候选继续下一行。
func TestStore_ClaimRunnableActions_RaceLosesRow(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(60 * time.Second)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
		WithArgs(4, now).
		WillReturnRows(pgxmock.NewRows([]string{"action_id"}).
			AddRow("act_a").
			AddRow("act_b"))

	// 第一个 UPDATE 因并发抢占返回 0 行（pgx.ErrNoRows）
	mock.ExpectQuery(`UPDATE goal_run_actions`).
		WithArgs("act_a", "gw-1", leaseUntil, now).
		WillReturnError(pgx.ErrNoRows)
	// 第二个 UPDATE 命中
	mock.ExpectQuery(`UPDATE goal_run_actions`).
		WithArgs("act_b", "gw-1", leaseUntil, now).
		WillReturnRows(pgxmock.NewRows(actionRowCols).AddRow(
			"act_b", "gr_1", "", "continue",
			"idem_b", int64(1), "running",
			now, 1, "",
			"gw-1", leaseUntil, int64(1), now,
			now, now,
		))
	mock.ExpectCommit()

	actions, err := store.ClaimRunnableActions(context.Background(), ClaimActionOptions{
		Owner: "gw-1",
		Now:   now,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("len = %d, want 1 (only act_b should remain)", len(actions))
	}
	if actions[0].ActionID != "act_b" {
		t.Fatalf("action_id = %s, want act_b", actions[0].ActionID)
	}
}

// TestStore_RenewActionLease_Success：续租成功。
func TestStore_RenewActionLease_Success(t *testing.T) {
	store, mock := newMockStore(t)
	until := time.Now().Add(60 * time.Second)

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs("act_a", "gw-1", int64(3), until, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := store.RenewActionLease(context.Background(), "act_a", "gw-1", 3, until); err != nil {
		t.Fatalf("renew: %v", err)
	}
}

// TestStore_RenewActionLease_Fence：续租失败返回 ErrActionLeaseLost。
func TestStore_RenewActionLease_Fence(t *testing.T) {
	store, mock := newMockStore(t)
	until := time.Now().Add(60 * time.Second)

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs("act_a", "gw-1", int64(3), until, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := store.RenewActionLease(context.Background(), "act_a", "gw-1", 3, until)
	if !errors.Is(err, ErrActionLeaseLost) {
		t.Fatalf("err = %v, want ErrActionLeaseLost", err)
	}
}

// TestStore_CompleteAction_Success：正常完成释放 lease。
func TestStore_CompleteAction_Success(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now()

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), "", now, "act_a", "gw-1", int64(1)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.CompleteAction(context.Background(), CompleteActionParams{
		ActionID:     "act_a",
		LeaseOwner:   "gw-1",
		FencingToken: 1,
		NewStatus:    ActionStatusCompleted,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// TestStore_CompleteAction_Fence：迟到 worker 完成被拒。
func TestStore_CompleteAction_Fence(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now()

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), "", now, "act_a", "gw-stale", int64(1)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := store.CompleteAction(context.Background(), CompleteActionParams{
		ActionID:     "act_a",
		LeaseOwner:   "gw-stale",
		FencingToken: 1,
		NewStatus:    ActionStatusCompleted,
		Now:          now,
	})
	if !errors.Is(err, ErrActionLeaseLost) {
		t.Fatalf("err = %v, want ErrActionLeaseLost", err)
	}
}

// TestStore_CompleteAction_RejectsNonTerminal：终态以外的 NewStatus 必须拒绝。
func TestStore_CompleteAction_RejectsNonTerminal(t *testing.T) {
	store, _ := newMockStore(t)
	err := store.CompleteAction(context.Background(), CompleteActionParams{
		ActionID:     "act_a",
		LeaseOwner:   "gw-1",
		FencingToken: 1,
		NewStatus:    ActionStatusPending,
	})
	if err == nil {
		t.Fatal("must reject non-terminal status")
	}
}

// TestStore_RequeueAction：失败重排回 pending，写入 retry_at + last_error。
func TestStore_RequeueAction(t *testing.T) {
	store, mock := newMockStore(t)
	retryAt := time.Now().Add(5 * time.Second)

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs("boom", retryAt, pgxmock.AnyArg(), "act_a", "gw-1", int64(2)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.RequeueAction(context.Background(), RequeueActionParams{
		ActionID:     "act_a",
		LeaseOwner:   "gw-1",
		FencingToken: 2,
		RetryAt:      retryAt,
		LastError:    "boom",
	})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
}

// TestStore_ExpireLeases_RunningOnly：reaper 清理 status='running' 且
// lease_until < now 的行，返回受影响行数。
func TestStore_ExpireLeases_RunningOnly(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now()

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(256, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 7))

	n, err := store.ExpireLeases(context.Background(), now, 256)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 7 {
		t.Fatalf("n = %d, want 7", n)
	}
}
