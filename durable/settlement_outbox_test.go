package durable

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func TestSettlementMigration(t *testing.T) {
	body := readFile(t, "../sql/migrations/startup/520_durable_task_settlement_intents.sql")
	for _, want := range []string{"durable_task_settlement_intents", "result_ciphertext", "source_fencing_token", "claim_fencing_token"} {
		if !strings.Contains(body, want) {
			t.Errorf("migration missing %q", want)
		}
	}
	down := readFile(t, "../sql/migrations/startup/520_durable_task_settlement_intents.down.sql")
	if !strings.Contains(down, "DROP TABLE IF EXISTS durable_task_settlement_intents") {
		t.Error("down migration missing drop")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSettlementIntentPersistsEncryptedBody(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`INSERT INTO durable_task_settlement_intents`).WithArgs(anyArgs(16)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := store.PersistSettlementIntent(context.Background(), TerminalCommit{Task: baseTask(), Outcome: StatusCompleted, Body: []byte(`{"ok":true}`), ContentType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSettlementIntentRejectsLostSourceFence(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`INSERT INTO durable_task_settlement_intents`).WithArgs(anyArgs(16)...).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	if err := store.PersistSettlementIntent(context.Background(), TerminalCommit{Task: baseTask(), Outcome: StatusFailed}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("PersistSettlementIntent error = %v, want ErrLeaseLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSettlementRetryReleasesClaim(t *testing.T) {
	store, mock := newMockStore(t)
	next := time.Date(2026, 8, 16, 12, 1, 0, 0, time.UTC)
	claim := ClaimedSettlement{SettlementIntent: SettlementIntent{TaskID: "task-1"}, ClaimOwner: "repair-1", ClaimFencingToken: 4}
	mock.ExpectExec(`UPDATE durable_task_settlement_intents`).WithArgs("task-1", "repair-1", int64(4), "db down", next).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.RetrySettlementIntent(context.Background(), claim, next, errors.New("db down")); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeSettlementDiscardsStaleSourceFence(t *testing.T) {
	store, mock := newMockStore(t)
	claim := ClaimedSettlement{
		SettlementIntent: SettlementIntent{TaskID: "task-1", SourceOwner: "worker-1", SourceFencingToken: 3, Outcome: StatusFailed},
		ClaimOwner:       "repair-1", ClaimFencingToken: 4,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT task_id, tenant_id, request_id`).WithArgs("task-1", "repair-1", int64(4)).WillReturnRows(pgxmock.NewRows([]string{"task_id", "tenant_id", "request_id", "session_id", "request_hash", "source_lease_owner", "source_fencing_token", "outcome", "result_ciphertext", "encryption_key_id", "content_type", "reason_code", "error_kind", "attempt", "result_hash", "attempts"}).AddRow("task-1", "tenant-1", "req-1", "sess-1", "hash-1", "worker-1", int64(3), "failed", nil, nil, "", "", "", 0, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", 1))
	mock.ExpectQuery(`UPDATE durable_llm_tasks SET status=`).WithArgs("task-1", "worker-1", int64(3), "failed", nil, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "", pgxmock.AnyArg(), "", "", 0, pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	mock.ExpectExec(`DELETE FROM durable_task_settlement_intents`).WithArgs("task-1", "repair-1", int64(4)).WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()
	if _, err := store.FinalizeSettlement(context.Background(), claim); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("FinalizeSettlement error = %v, want ErrLeaseLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSettlementClaimUsesIndependentLease(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{"task_id", "tenant_id", "request_id", "session_id", "request_hash", "source_lease_owner", "source_fencing_token", "outcome", "result_ciphertext", "encryption_key_id", "content_type", "reason_code", "error_kind", "attempt", "result_hash", "claim_fencing_token", "attempts"}).AddRow("task-1", "tenant-1", "req-1", "sess-1", "hash-1", "worker-1", int64(3), "failed", "malformed-ciphertext", "key-1", "", "retry", "", 1, "", int64(7), 0)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM durable_task_settlement_intents.*FOR UPDATE SKIP LOCKED`).WithArgs(8, now).WillReturnRows(rows)
	mock.ExpectQuery(`UPDATE durable_task_settlement_intents`).WithArgs("task-1", "outbox-1", now.Add(time.Minute), int64(8), now, int64(7)).WillReturnRows(pgxmock.NewRows([]string{"claim_fencing_token", "attempts"}).AddRow(int64(8), 1))
	mock.ExpectCommit()
	got, err := store.ClaimSettlementIntents(context.Background(), "outbox-1", time.Minute, 8, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceOwner != "worker-1" || got[0].ClaimFencingToken != 8 || got[0].Attempts != 1 {
		t.Fatalf("got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSettlementClaimTargetsOnlyRequestedTask(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{"task_id", "tenant_id", "request_id", "session_id", "request_hash", "source_lease_owner", "source_fencing_token", "outcome", "result_ciphertext", "encryption_key_id", "content_type", "reason_code", "error_kind", "attempt", "result_hash", "claim_fencing_token", "attempts"}).AddRow("task-2", "tenant-1", "req-2", "sess-2", "hash-2", "worker-2", int64(4), "failed", nil, nil, "", "retry", "", 1, "", int64(1), 0)
	mock.ExpectBegin()
	mock.ExpectQuery(`task_id=\$3.*FOR UPDATE SKIP LOCKED`).WithArgs(1, now, "task-2").WillReturnRows(rows)
	mock.ExpectQuery(`UPDATE durable_task_settlement_intents`).WithArgs("task-2", "front-2", now.Add(time.Minute), int64(2), now, int64(1)).WillReturnRows(pgxmock.NewRows([]string{"claim_fencing_token", "attempts"}).AddRow(int64(2), 1))
	mock.ExpectCommit()
	claim, err := store.ClaimSettlementIntent(context.Background(), "task-2", "front-2", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil || claim.TaskID != "task-2" || claim.ClaimOwner != "front-2" {
		t.Fatalf("claim = %+v", claim)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
