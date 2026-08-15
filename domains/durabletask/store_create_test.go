package durabletask

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestStoreCreateAndClaimIsAtomic(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()

	leaseUntil := time.Date(2026, 8, 15, 12, 1, 0, 0, time.UTC)
	deadline := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO durable_llm_tasks`).
		WithArgs(
			snapshot.TaskID, snapshot.TenantID, snapshot.RequestID, snapshot.ParentRequestID,
			snapshot.SessionID, snapshot.ClientProtocol, snapshot.Endpoint, pgxmock.AnyArg(),
			SnapshotVersionV1, "current", snapshot.RequestHash, deadline, "gateway/request-1",
			leaseUntil, snapshot.Policy, deadline.Add(24*time.Hour),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(snapshot.TaskID, snapshot.TenantID, snapshot.RequestID, snapshot.SessionID,
			1, StatusAccepted, StatusRunning, ReasonCreated, int64(1)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := NewStore(mock, testKeyring(t))
	lease, err := store.CreateAndClaim(context.Background(), CreateAndClaimParams{
		Snapshot:        snapshot,
		LeaseOwner:      "gateway/request-1",
		LeaseUntil:      leaseUntil,
		DeadlineAt:      deadline,
		ResultExpiresAt: deadline.Add(24 * time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, Lease{
		TaskID:       snapshot.TaskID,
		Owner:        "gateway/request-1",
		FencingToken: 1,
		LeaseUntil:   leaseUntil,
	}, lease)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStoreCreateAndClaimRollsBackWhenEventAppendFails(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()

	snapshot := testSnapshot()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO durable_llm_tasks`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()

	store := NewStore(mock, testKeyring(t))
	_, err = store.CreateAndClaim(context.Background(), CreateAndClaimParams{
		Snapshot:        snapshot,
		LeaseOwner:      "worker-1",
		LeaseUntil:      time.Now().Add(time.Minute),
		DeadlineAt:      time.Now().Add(time.Hour),
		ResultExpiresAt: time.Now().Add(2 * time.Hour),
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func testSnapshot() DurableRequestSnapshotV1 {
	return DurableRequestSnapshotV1{
		Version:         SnapshotVersionV1,
		TaskID:          "018f0000-0000-7000-8000-000000000001",
		TenantID:        "tenant-text-id",
		RequestID:       "request-1",
		ParentRequestID: "parent-1",
		SessionID:       "session-1",
		ClientProtocol:  "openai-responses",
		Endpoint:        "/v1/responses",
		RequestHash:     "sha256:request",
		NormalizedBody:  json.RawMessage(`{"model":"gpt-5"}`),
		Policy:          json.RawMessage(`{"max_attempts":4}`),
	}
}
