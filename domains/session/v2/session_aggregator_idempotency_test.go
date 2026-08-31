package v2

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func aggregateTestUpdate() SessionUpdate {
	return SessionUpdate{
		SessionID:           "sess-idem",
		TenantID:            "tenant-1",
		RequestID:           "req-123",
		LastTurnNo:          1,
		LastRequestSummary:  "hello",
		LastResponseSummary: "world",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     150,
		CostIncrement:       0.003,
		UpdatedAt:           time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	}
}

func expectAggregateSessionLock(mock pgxmock.PgxPoolIface, update SessionUpdate) {
	mock.ExpectExec("session_turns_advisory_lock_key").
		WithArgs(update.TenantID, update.SessionID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
}

func expectNoParentTurn(mock pgxmock.PgxPoolIface, update SessionUpdate, partitionDate time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE public.session_turns")).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(update.TenantID, update.RequestID, partitionDate).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
}

func expectAggregateUpsert(mock pgxmock.PgxPoolIface, update SessionUpdate, partitionDate time.Time) {
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO public.sessions")).
		WithArgs(
			update.SessionID, update.TenantID,
			update.UpdatedAt,
			update.TurnIncrement, update.TokensIncrement, update.CostIncrement,
			update.LastTurnNo, update.LastRequestSummary, update.LastResponseSummary,
			update.LastModel, update.LastProvider,
			update.ClientType,
			partitionDate,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

// The first update claims the persisted turn and applies the snapshot. A replay
// cannot claim aggregate_applied_at again and therefore skips the counter upsert.
func TestSessionAggregator_UpdateSessionIdempotent(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	agg := newSessionAggregator(mock)
	update := aggregateTestUpdate()
	partitionDate := update.UpdatedAt.Truncate(24 * time.Hour)
	hotClaim := regexp.QuoteMeta("UPDATE public.session_turns_hot")

	mock.ExpectBegin()
	expectAggregateSessionLock(mock, update)
	expectNoParentTurn(mock, update, partitionDate)
	mock.ExpectQuery(hotClaim).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnRows(pgxmock.NewRows([]string{"claimed"}).AddRow(1))
	expectAggregateUpsert(mock, update, partitionDate)
	mock.ExpectCommit()
	require.NoError(t, agg.UpdateSession(context.Background(), update))

	mock.ExpectBegin()
	expectAggregateSessionLock(mock, update)
	expectNoParentTurn(mock, update, partitionDate)
	mock.ExpectQuery(hotClaim).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectCommit()
	require.NoError(t, agg.UpdateSession(context.Background(), update))
}

func TestSessionAggregator_ParentDuplicatePreventsHiddenHotClaim(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	agg := newSessionAggregator(mock)
	update := aggregateTestUpdate()
	partitionDate := update.UpdatedAt.Truncate(24 * time.Hour)
	mock.ExpectBegin()
	expectAggregateSessionLock(mock, update)
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE public.session_turns")).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(update.TenantID, update.RequestID, partitionDate).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()

	require.NoError(t, agg.UpdateSession(context.Background(), update))
}

// A failed snapshot upsert rolls the claim back. The next call can claim and
// apply the same turn, so asynchronous aggregate failures remain retryable.
func TestSessionAggregator_UpdateSessionClaimRollsBackOnFailure(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	agg := newSessionAggregator(mock)
	update := aggregateTestUpdate()
	partitionDate := update.UpdatedAt.Truncate(24 * time.Hour)
	claim := regexp.QuoteMeta("UPDATE public.session_turns_hot")
	insert := regexp.QuoteMeta("INSERT INTO public.sessions")

	mock.ExpectBegin()
	expectAggregateSessionLock(mock, update)
	expectNoParentTurn(mock, update, partitionDate)
	mock.ExpectQuery(claim).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnRows(pgxmock.NewRows([]string{"claimed"}).AddRow(1))
	mock.ExpectExec(insert).
		WithArgs(
			update.SessionID, update.TenantID,
			update.UpdatedAt,
			update.TurnIncrement, update.TokensIncrement, update.CostIncrement,
			update.LastTurnNo, update.LastRequestSummary, update.LastResponseSummary,
			update.LastModel, update.LastProvider,
			update.ClientType,
			partitionDate,
		).
		WillReturnError(errors.New("snapshot write failed"))
	mock.ExpectRollback()
	require.Error(t, agg.UpdateSession(context.Background(), update))

	mock.ExpectBegin()
	expectAggregateSessionLock(mock, update)
	expectNoParentTurn(mock, update, partitionDate)
	mock.ExpectQuery(claim).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnRows(pgxmock.NewRows([]string{"claimed"}).AddRow(1))
	expectAggregateUpsert(mock, update, partitionDate)
	mock.ExpectCommit()
	require.NoError(t, agg.UpdateSession(context.Background(), update))
}

func TestSessionAggregator_SetSessionMetadataBackfillsTaskTypeAcrossStores(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	agg := newSessionAggregator(mock)
	meta := SessionMetadata{TaskType: "code"}
	mock.ExpectBegin()
	mock.ExpectExec("session_turns_advisory_lock_key").
		WithArgs("tenant-1", "session-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE public.sessions")).
		WithArgs("tenant-1", "session-1", meta.TaskType, "", "", "", "", []string(nil)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE public.session_turns_hot")).
		WithArgs("tenant-1", "session-1", meta.TaskType).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE public.session_turns")).
		WithArgs("tenant-1", "session-1", meta.TaskType).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mock.ExpectCommit()

	require.NoError(t, agg.SetSessionMetadata(context.Background(), "tenant-1", "session-1", meta))
}

// aggregate upsert without requiring a session_turns claim.
func TestSessionAggregator_UpdateSessionIdempotentEmptyRequestIDAllowed(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	agg := newSessionAggregator(mock)
	update := aggregateTestUpdate()
	update.SessionID = "sess-backfill"
	update.RequestID = ""
	partitionDate := update.UpdatedAt.Truncate(24 * time.Hour)

	expectAggregateUpsert(mock, update, partitionDate)
	require.NoError(t, agg.UpdateSession(context.Background(), update))
}
