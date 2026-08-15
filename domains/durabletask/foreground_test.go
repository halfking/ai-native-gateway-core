package durabletask

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestBeginForegroundCreatesAndClaims(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)
	snapshot := testSnapshot()

	leaseUntil := pgxmock.AnyArg()
	expectBypassBegin(mock)
	mock.ExpectExec(`INSERT INTO durable_llm_tasks`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			"gateway/request-1", leaseUntil, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	fg, err := BeginForeground(context.Background(), NewStore(mock, kr), snapshot, ForegroundConfig{})
	require.NoError(t, err)
	require.Equal(t, snapshot.TaskID, fg.Lease.TaskID)
	require.Equal(t, "gateway/request-1", fg.Lease.Owner)
	require.Equal(t, int64(1), fg.Lease.FencingToken)
	require.False(t, fg.DeadlineAt.IsZero())
	require.True(t, fg.ResultExpiresAt.After(fg.DeadlineAt))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBeginForegroundGeneratesTaskID(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)
	snapshot := testSnapshot()
	snapshot.TaskID = "" // caller did not pre-generate

	expectBypassBegin(mock)
	mock.ExpectExec(`INSERT INTO durable_llm_tasks`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO durable_llm_task_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	fg, err := BeginForeground(context.Background(), NewStore(mock, kr), snapshot, ForegroundConfig{})
	require.NoError(t, err)
	require.NotEmpty(t, fg.Lease.TaskID, "a fresh task id must be generated")
	require.NotEmpty(t, fg.Snapshot.TaskID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestForegroundContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, ok := ForegroundFromContext(ctx)
	require.False(t, ok)

	fg := &Foreground{}
	ctx2 := fg.Attach(ctx)
	got, ok := ForegroundFromContext(ctx2)
	require.True(t, ok)
	require.Same(t, fg, got)

	// nil-safe attach keeps the context unchanged.
	require.Equal(t, ctx, (*Foreground)(nil).Attach(ctx))
}

func TestForegroundNilSafeOperations(t *testing.T) {
	var fg *Foreground
	require.NoError(t, fg.Checkpoint(context.Background(), CommitContent))
	_, err := fg.Complete(context.Background(), []byte("x"), "application/json")
	require.NoError(t, err)
	_, err = fg.Fail(context.Background(), FailureParams{Status: StatusExpired})
	require.NoError(t, err)
	require.NoError(t, fg.Reschedule(context.Background(), RescheduleParams{}))
	require.NoError(t, fg.RenewNow(context.Background()))
}

func TestForegroundCheckpointFencesOff(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	fg := &Foreground{store: NewStore(mock, kr), Lease: Lease{TaskID: "018f-task", Owner: "gateway/request-1", FencingToken: 1}}
	// 0 rows updated → ErrLeaseLost.
	expectBypassBegin(mock)
	mock.ExpectExec("UPDATE durable_llm_tasks SET commit_state").
		WithArgs("018f-task", "gateway/request-1", int64(1), CommitContent, true).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()
	err = fg.Checkpoint(context.Background(), CommitContent)
	require.ErrorIs(t, err, ErrLeaseLost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCommitStateFromString(t *testing.T) {
	for name, want := range map[string]CommitState{
		"none": CommitNone, "metadata": CommitMetadata, "content": CommitContent,
		"tool_call": CommitToolCall, "terminal": CommitTerminal,
	} {
		got, ok := CommitStateFromString(name)
		require.True(t, ok, name)
		require.Equal(t, want, got)
	}
	_, ok := CommitStateFromString("bogus")
	require.False(t, ok, "unknown states must not map (fail closed upstream)")
}

func TestHashRequestBody(t *testing.T) {
	h1 := HashRequestBody([]byte(`{"a":1}`))
	require.Len(t, h1, 64)
	require.Equal(t, h1, HashRequestBody([]byte(`{"a":1}`)))
	require.NotEqual(t, h1, HashRequestBody([]byte(`{"a":2}`)))
}

func TestForegroundRenewNow(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	fg := &Foreground{store: NewStore(mock, kr), Lease: Lease{TaskID: "018f-task", Owner: "gateway/request-1", FencingToken: 1}}
	expectBypassBegin(mock)
	mock.ExpectExec("UPDATE durable_llm_tasks SET lease_until").
		WithArgs("018f-task", "gateway/request-1", int64(1), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	require.NoError(t, fg.RenewNow(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}
