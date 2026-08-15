package durabletask

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// gaugeTenantValue reads one durable_tasks_active series through the default
// gatherer (testutil is not vendored).
func gaugeTenantValue(t *testing.T, tenant string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "durable_tasks_active" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "tenant" && l.GetValue() == tenant {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return 0
}

// claimColumns mirrors the ClaimRunnable SELECT in store.go.
func claimColumns() []string {
	return []string{"id", "tenant_id", "request_id", "session_id", "request_hash",
		"request_snapshot_ciphertext", "snapshot_version", "encryption_key_id",
		"attempt_count", "deadline_at", "commit_state", "fencing_token", "from_status"}
}

// expectClaim returns a claimed task row built from a real encrypted snapshot
// so the worker's decrypt path succeeds.
func expectClaim(t *testing.T, mock pgxmock.PgxPoolIface, snapshot DurableRequestSnapshotV1, kr *secret.Keyring, owner string) ClaimedTask {
	ciphertext, _, err := EncryptRequestSnapshotV1(snapshot, kr)
	require.NoError(t, err)
	deadline := time.Now().Add(time.Hour)
	task := ClaimedTask{Lease: Lease{TaskID: snapshot.TaskID, Owner: owner, FencingToken: 2,
		LeaseUntil: time.Now().Add(90 * time.Second)},
		TenantID: snapshot.TenantID, RequestID: snapshot.RequestID, SessionID: snapshot.SessionID,
		RequestHash: snapshot.RequestHash, SnapshotCiphertext: ciphertext, SnapshotVersion: SnapshotVersionV1,
		EncryptionKeyID: "current", AttemptCount: 2, DeadlineAt: deadline, CommitState: CommitNone}
	rows := pgxmock.NewRows(claimColumns()).AddRow(task.TaskID, task.TenantID, task.RequestID,
		task.SessionID, task.RequestHash, ciphertext, SnapshotVersionV1, "current",
		task.AttemptCount, task.DeadlineAt, string(CommitNone), int64(2), StatusRetryScheduled)
	mock.ExpectBegin()
	mock.ExpectQuery("WITH picked AS").
		WithArgs(10, owner, pgxmock.AnyArg()).
		WillReturnRows(rows)
	mock.ExpectExec("INSERT INTO durable_llm_task_events").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	return task
}

func TestWorkerRunCycleReschedulesNoopExecution(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)
	snapshot := testSnapshot()

	_ = expectClaim(t, mock, snapshot, kr, "worker-1")
	// Reschedule transaction: UPDATE ... RETURNING then event insert.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE durable_llm_tasks SET status").
		WithArgs(snapshot.TaskID, "worker-1", int64(2), StatusWaitingRecovery,
			"", ReasonRecoveryExecutorUnavailable, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "request_id", "session_id",
			"attempt_count", "fencing_token"}).
			AddRow(snapshot.TaskID, snapshot.TenantID, snapshot.RequestID, snapshot.SessionID, 2, int64(2)))
	mock.ExpectExec("INSERT INTO durable_llm_task_events").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	worker := NewRecoveryWorker(NewStore(mock, kr), kr, nil, WorkerConfig{
		Owner: "worker-1", RenewInterval: -1,
	})
	worker.RunCycle(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkerRunCycleDiscardsOutcomeWhenFencedOff(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)
	snapshot := testSnapshot()

	_ = expectClaim(t, mock, snapshot, kr, "worker-1")
	// Reschedule UPDATE matches zero rows → ErrLeaseLost; worker must
	// swallow it without further store calls.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE durable_llm_tasks SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "request_id", "session_id",
			"attempt_count", "fencing_token"}))
	mock.ExpectRollback()

	worker := NewRecoveryWorker(NewStore(mock, kr), kr, nil, WorkerConfig{
		Owner: "worker-1", RenewInterval: -1,
	})
	require.NotPanics(t, func() { worker.RunCycle(context.Background()) })
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkerRenewLossCancelsExecutionAndDiscardsOutcome(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)
	snapshot := testSnapshot()

	_ = expectClaim(t, mock, snapshot, kr, "worker-1")
	// First renew tick loses the fence (0 rows updated).
	mock.ExpectExec("UPDATE durable_llm_tasks SET lease_until").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	executor := ExecutorFunc(func(ctx context.Context, task ClaimedTask, snap DurableRequestSnapshotV1) Execution {
		<-ctx.Done() // blocked until renew loss cancels the attempt
		return Execution{Outcome: OutcomeCompleted, Body: []byte(`{"ok":true}`)}
	})
	worker := NewRecoveryWorker(NewStore(mock, kr), kr, executor, WorkerConfig{
		Owner: "worker-1", RenewInterval: 5 * time.Millisecond,
	})
	worker.RunCycle(context.Background())
	// No Complete/Reschedule/Fail was issued: the outcome was discarded.
	mock.ExpectationsWereMet()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkerFailClosedOnUndecryptableSnapshot(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)
	snapshot := testSnapshot()

	// Claim a row whose ciphertext is garbage: decryption must fail closed
	// and the worker must terminalize the task as permanent_failed.
	mock.ExpectBegin()
	mock.ExpectQuery("WITH picked AS").
		WithArgs(10, "worker-1", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(claimColumns()).
			AddRow(snapshot.TaskID, snapshot.TenantID, snapshot.RequestID, snapshot.SessionID,
				snapshot.RequestHash, "v2|llm-gateway:durable-request:v1|current|AAAA",
				SnapshotVersionV1, "current", 2, time.Now().Add(time.Hour), string(CommitNone), int64(2), StatusRetryScheduled))
	mock.ExpectExec("INSERT INTO durable_llm_task_events").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	// Fail transaction: UPDATE ... RETURNING then event insert then outbox.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE durable_llm_tasks SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "request_id", "session_id",
			"attempt_count", "fencing_token", "result_version"}).
			AddRow(snapshot.TaskID, snapshot.TenantID, snapshot.RequestID, snapshot.SessionID, 2, int64(2), int64(1)))
	mock.ExpectExec("INSERT INTO durable_llm_task_events").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO durable_pending_outbox").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectCommit()

	worker := NewRecoveryWorker(NewStore(mock, kr), kr, nil, WorkerConfig{
		Owner: "worker-1", RenewInterval: -1,
	})
	worker.RunCycle(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkerRunReapersForwardsOutboxItems(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	reapedColumns := []string{"id", "tenant_id", "request_id", "session_id",
		"attempt_count", "fencing_token", "result_version", "from_status"}
	for _, reason := range []string{ReasonSurvivalExpired, ReasonResumeSafetyBlocked} {
		mock.ExpectBegin()
		mock.ExpectQuery("WITH picked AS").
			WithArgs(100, pgxmock.AnyArg(), reason).
			WillReturnRows(pgxmock.NewRows(reapedColumns).
				AddRow("018f-task", "tenant-text-id", "request-1", "session-1", 3, int64(4), int64(1), StatusRunning))
		mock.ExpectExec("INSERT INTO durable_llm_task_events").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectQuery("INSERT INTO durable_pending_outbox").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(1)))
		mock.ExpectCommit()
	}

	worker := NewRecoveryWorker(NewStore(mock, kr), kr, nil, WorkerConfig{Owner: "worker-1"})
	var forwarded []OutboxItem
	worker.OutboxHandler = func(_ context.Context, item OutboxItem) { forwarded = append(forwarded, item) }
	worker.RunReapers(context.Background())
	require.Len(t, forwarded, 2)
	require.Equal(t, StatusExpired, forwarded[0].Status)
	require.Equal(t, StatusResumeSafetyBlocked, forwarded[1].Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRefreshActiveGaugePublishesAndResets(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	mock.ExpectQuery("SELECT tenant_id,count").
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "count"}).
			AddRow("tenant-a", int64(3)).AddRow("tenant-b", int64(1)))
	worker := NewRecoveryWorker(NewStore(mock, kr), kr, nil, WorkerConfig{Owner: "worker-1"})
	worker.RefreshActiveGauge(context.Background())
	require.Equal(t, float64(3), gaugeTenantValue(t, "tenant-a"))
	require.Equal(t, float64(1), gaugeTenantValue(t, "tenant-b"))

	// tenant-a drains: its series must reset to zero, not freeze at 3.
	mock.ExpectQuery("SELECT tenant_id,count").
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "count"}).AddRow("tenant-b", int64(1)))
	worker.RefreshActiveGauge(context.Background())
	require.Equal(t, float64(0), gaugeTenantValue(t, "tenant-a"))
	require.Equal(t, float64(1), gaugeTenantValue(t, "tenant-b"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLeaseLossSignal(t *testing.T) {
	s := newLeaseLossSignal()
	go func() {
		defer s.finish()
		require.False(t, s.lost())
		s.trigger()
		s.trigger() // idempotent
	}()
	s.wait() // returns once the renew goroutine exits
	require.True(t, s.lost())
}
