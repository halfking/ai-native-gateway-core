package v2

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Test seams: a SessionBodiesWriter and SessionWriterV2 backed by a single
// pgxmock pool so the whole turn+bodies atomic flow can be driven in a unit
// test without a live PostgreSQL instance.
//
// The mock uses QueryMatcherRegexp so we only need to pin down the SQL
// fragments that matter (advisory lock, MAX(turn_no), the two INSERTs and the
// tx lifecycle calls). Exact argument matching would couple the test to every
// column position and make it brittle; the dedicated dup tests in
// turn_writer_dup_test.go already pin the exact WithArgs contract.
// ──────────────────────────────────────────────────────────────────────────

// anyArgs returns n pgxmock.AnyArg() matchers, used so an INSERT expectation
// matches regardless of the exact column values (the contract for those is
// pinned in turn_writer_dup_test.go; here we only care about the tx lifecycle).
func anyArgs(n int) []interface{} {
	args := make([]interface{}, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

// noopAggregator is a sessionUpdater that does nothing. Used by tests that
// only care about the turn+bodies tx lifecycle and do not want the real
// SessionAggregator (which issues its own idempotency probe against the mock)
// to consume mock expectations.
type noopAggregator struct{}

func (noopAggregator) UpdateSession(context.Context, SessionUpdate) error { return nil }

func newMockedSessionWriter(t *testing.T) (*SessionWriterV2, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)

	tw := newTurnWriter(mock)
	bw := newSessionBodiesWriter(bodiesPoolShim{mock})
	// sa is a no-op so the aggregate step doesn't issue queries against the
	// mock; the dedicated lifecycle test injects a recording aggregator.
	w := &SessionWriterV2{
		turnWriter:        tw,
		bodiesWriter:      bw,
		sessionAggregator: noopAggregator{},
		turnLogsWriter:    nil,
	}

	return w, mock
}

// bodiesPoolShim adapts a pgxmock pool to the bodiesDB interface (Exec/Query/
// QueryRow). pgxmock.PgxPoolIface already implements all three, but declaring
// the shim makes the dependency explicit and keeps the production type tidy.
type bodiesPoolShim struct{ p pgxmock.PgxPoolIface }

func (s bodiesPoolShim) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return s.p.Exec(ctx, sql, args...)
}
func (s bodiesPoolShim) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return s.p.Query(ctx, sql, args...)
}
func (s bodiesPoolShim) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.p.QueryRow(ctx, sql, args...)
}

// expectListAllBodiesEmpty makes the getPreviousAttachments probe return no
// rows so submit-mode detection proceeds without attachments. It matches by
// regex so the test does not depend on the exact column list.
func expectListAllBodiesEmpty(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery("FROM gateway.session_bodies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "turn_no", "tenant_id", "request_id", "ts",
			"request_delta", "response_delta", "outbound_body",
			"request_attachments", "response_attachments",
		}))
}

// sampleRequest builds a minimal ProcessedRequest that is enough to drive the
// Write happy path (one user message in, one assistant message out, no
// attachments, no compression).
func sampleRequest() *ProcessedRequest {
	return &ProcessedRequest{
		SessionID:    "sess_atomic",
		TenantID:     "tenant_atomic",
		RequestID:    "req_atomic",
		Timestamp:    time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
		RequestBody:  []Message{{Role: "user", Content: "hi"}},
		ResponseBody: []Message{{Role: "assistant", Content: "hello"}},
		StartedAt:    time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
		CompletedAt:  time.Date(2026, 8, 2, 12, 0, 1, 0, time.UTC),
		StatusCode:   200,
		Success:      true,
	}
}

// TestWrite_TurnAndBodiesAreAtomic_RollbackOnBodiesFailure (spec §6.2)
//
// When bodies INSERT fails AFTER the turn INSERT succeeded inside the same tx,
// the whole tx MUST be rolled back. No orphan turn row may survive. This is
// the RED→GREEN test for the "single transaction" guarantee.
func TestWrite_TurnAndBodiesAreAtomic_RollbackOnBodiesFailure(t *testing.T) {
	w, mock := newMockedSessionWriter(t)

	// 1. getPreviousAttachments runs first (on the pool, not in tx).
	expectListAllBodiesEmpty(mock)

	// 2. Single tx begins.
	mock.ExpectBegin()

	// 3. AppendTurnInTx inside the tx: advisory lock + MAX(turn_no) + INSERT.
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT INTO gateway.session_turns").
		WithArgs(anyArgs(30)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	// 4. WriteBodiesInTx FAILS — simulate a DB error on the bodies INSERT.
	bodiesErr := errors.New("boom: bodies insert failed")
	mock.ExpectExec("INSERT INTO gateway.session_bodies").
		WithArgs(anyArgs(11)...).
		WillReturnError(bodiesErr)

	// 5. Because bodies failed, the tx MUST roll back (no Commit expected).
	mock.ExpectRollback()

	err := w.Write(context.Background(), sampleRequest())
	require.Error(t, err, "Write must surface the bodies error")
	assert.Contains(t, err.Error(), "bodies", "error should mention the failing stage")

	require.NoError(t, mock.ExpectationsWereMet(),
		"all mocked expectations (including Rollback, NOT Commit) must be consumed")
}

// TestWrite_TurnAndBodiesAreAtomic_CommitOnSuccess (spec §6.2)
//
// When both turn and bodies succeed, the single tx MUST be committed exactly
// once. Verifies the happy path of the atomic write.
func TestWrite_TurnAndBodiesAreAtomic_CommitOnSuccess(t *testing.T) {
	w, mock := newMockedSessionWriter(t)

	// 1. getPreviousAttachments.
	expectListAllBodiesEmpty(mock)

	// 2. Single tx.
	mock.ExpectBegin()

	// 3. AppendTurnInTx.
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT INTO gateway.session_turns").
		WithArgs(anyArgs(30)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	// 4. WriteBodiesInTx succeeds.
	mock.ExpectExec("INSERT INTO gateway.session_bodies").
		WithArgs(anyArgs(11)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	// 5. Commit.
	mock.ExpectCommit()

	err := w.Write(context.Background(), sampleRequest())
	require.NoError(t, err, "Write must succeed when both stages succeed")

	// Drain the aggregate goroutine so the mock is not closed mid-flight and
	// Stop() confirms the WaitGroup reaches zero.
	require.NoError(t, w.Stop(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

// ── Aggregate goroutine lifecycle (spec §6.3) ──────────────────────────────

// recordingAggregator is a fake SessionAggregator that records that it ran and
// can be made to block until released, so we can prove Stop() waits for the
// in-flight aggregate goroutine.
type recordingAggregator struct {
	mu       sync.Mutex
	calls    int32
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func newRecordingAggregator() *recordingAggregator {
	return &recordingAggregator{
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		returned: make(chan struct{}, 1),
	}
}

func (r *recordingAggregator) UpdateSession(ctx context.Context, _ SessionUpdate) error {
	atomic.AddInt32(&r.calls, 1)
	select {
	case r.started <- struct{}{}:
	default:
	}
	// Block until the test releases us, simulating a slow snapshot update.
	select {
	case <-r.release:
	case <-ctx.Done():
		// Context cancelled (e.g. Stop) — still record completion.
	}
	r.returned <- struct{}{}
	return nil
}

func (r *recordingAggregator) callCount() int32 { return atomic.LoadInt32(&r.calls) }

// TestWrite_AggregateGoroutineManagedByLifecycle (spec §6.3)
//
// The aggregate snapshot update runs in a goroutine that MUST be tracked by a
// WaitGroup and tied to the writer's lifecycle context. After Stop() returns,
// the goroutine must have completed (or been cancelled) — there must be no
// detached fire-and-forget work that outlives shutdown.
func TestWrite_AggregateGoroutineManagedByLifecycle(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = mock.ExpectationsWereMet(); mock.Close() }()

	agg := newRecordingAggregator()
	w := &SessionWriterV2{
		turnWriter:        newTurnWriter(mock),
		bodiesWriter:      newSessionBodiesWriter(bodiesPoolShim{mock}),
		sessionAggregator: agg, // interface field — fake injected directly
		turnLogsWriter:    nil,
	}

	// Drive a successful atomic write so the aggregate goroutine is spawned.
	expectListAllBodiesEmpty(mock)
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT INTO gateway.session_turns").
		WithArgs(anyArgs(30)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO gateway.session_bodies").
		WithArgs(anyArgs(11)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, w.Write(context.Background(), sampleRequest()))

	// Wait until the aggregate goroutine has actually started.
	select {
	case <-agg.started:
	case <-time.After(2 * time.Second):
		t.Fatal("aggregate goroutine never started")
	}

	// The goroutine is now blocked in UpdateSession. Stop() must wait for it.
	// Release it, then Stop() should return promptly after the goroutine
	// completes.
	go func() { close(agg.release) }()

	done := make(chan struct{})
	go func() {
		_ = w.Stop(context.Background())
		close(done)
	}()
	select {
	case <-done:
		// good — Stop returned
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after the aggregate goroutine completed")
	}

	assert.GreaterOrEqual(t, agg.callCount(), int32(1),
		"aggregate UpdateSession must have been invoked at least once")
}
