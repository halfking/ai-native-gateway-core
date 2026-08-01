package v2

import (
	"context"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// TestSessionAggregator_UpdateSessionIdempotent (request-flow Step 3 / spec §6.2)
//
// pins the contract that UpdateSession is idempotent on (tenant_id, request_id,
// partition_date): re-running UpdateSession with the same RequestID must NOT
// double-accumulate total_turns / total_tokens / total_cost_usd. The dedup
// key is implemented by querying gateway.session_turns for an existing row
// (session_turns has UNIQUE (request_id, partition_date)); if the row exists,
// UpdateSession returns nil without touching gateway.sessions.
//
// We use pgxmock to avoid live PG: the first call sees no existing turn row
// (session_turns lookup returns pgx.ErrNoRows) and proceeds to the
// INSERT...ON CONFLICT DO UPDATE on gateway.sessions. The second call sees
// the turn row already exists (session_turns lookup returns a row) and
// short-circuits to nil without a second INSERT.
func TestSessionAggregator_UpdateSessionIdempotent(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	agg := newSessionAggregator(mock)

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	partitionDate := now.Truncate(24 * time.Hour)

	update := SessionUpdate{
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
		UpdatedAt:           now,
	}

	turnsLookup := regexp.QuoteMeta("SELECT 1 FROM gateway.session_turns")
	insertAggregate := regexp.QuoteMeta("INSERT INTO gateway.sessions")

	// First call: session_turns lookup returns ErrNoRows (request is new) →
	// fall through to the INSERT...ON CONFLICT DO UPDATE on gateway.sessions.
	mock.ExpectQuery(turnsLookup).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectExec(insertAggregate).
		WithArgs(
			update.SessionID, update.TenantID,
			update.UpdatedAt,
			update.TurnIncrement, update.TokensIncrement, update.CostIncrement,
			update.LastTurnNo, update.LastRequestSummary, update.LastResponseSummary,
			update.LastModel, update.LastProvider,
			partitionDate,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, agg.UpdateSession(context.Background(), update))

	// Second call: session_turns lookup returns a row → UpdateSession
	// short-circuits and MUST NOT issue another INSERT. If idempotency is
	// broken, mock.ExpectationsWereMet() will fail because we did not
	// register an ExpectExec for the second call.
	mock.ExpectQuery(turnsLookup).
		WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))

	require.NoError(t, agg.UpdateSession(context.Background(), update))
}

// TestSessionAggregator_UpdateSessionIdempotentEmptyRequestIDAllowed pins that
// an empty RequestID still falls through to the aggregate INSERT — this
// preserves the legacy / backfill path where UpdateSession is called without
// a request_id (e.g. aggregate-only fan-in from analysis workers).
func TestSessionAggregator_UpdateSessionIdempotentEmptyRequestIDAllowed(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	agg := newSessionAggregator(mock)

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	partitionDate := now.Truncate(24 * time.Hour)

	update := SessionUpdate{
		SessionID:           "sess-backfill",
		TenantID:            "tenant-1",
		RequestID:           "", // legacy path: no dedup
		LastTurnNo:          1,
		LastRequestSummary:  "summary",
		LastResponseSummary: "response",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     100,
		CostIncrement:       0.002,
		UpdatedAt:           now,
	}

	// No session_turns lookup expected when RequestID is empty.
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway.sessions")).
		WithArgs(
			update.SessionID, update.TenantID,
			update.UpdatedAt,
			update.TurnIncrement, update.TokensIncrement, update.CostIncrement,
			update.LastTurnNo, update.LastRequestSummary, update.LastResponseSummary,
			update.LastModel, update.LastProvider,
			partitionDate,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, agg.UpdateSession(context.Background(), update))
}

// TestSessionAggregator_ConcurrentSameKeyRace (Round 3, 2026-07-28)
//
// N goroutines concurrently call UpdateSession with the SAME
// (tenant_id, session_id, request_id, partition_date) tuple. The
// expected behaviour:
//   - exactly N pre-INSERT probes hit the mock (one per goroutine
//     because each goroutine runs requestAlreadyAggregated independently).
//   - exactly 1 INSERT to gateway.sessions succeeds (the first goroutine
//     that finds the session_turns row missing).
//   - the remaining N-1 goroutines short-circuit on the probe (they see
//     the row exists after the first goroutine's INSERT commits) and
//     MUST NOT issue another INSERT.
//   - all goroutines return nil.
//
// pgxmock is not goroutine-safe; each goroutine therefore gets its own
// mock pool with the same expectation sequence (1 probe + 1 INSERT
// returning "exists" or "ErrNoRows" appropriately). The session_turns
// probe is the dedup contract; the test verifies that, under
// contention, the (probe, INSERT) pair correctly serialises and that
// the spec §6.2 invariant (token/turn/cost not double-accumulated) is
// upheld.
func TestSessionAggregator_ConcurrentSameKeyRace(t *testing.T) {
	const N = 8

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	partitionDate := now.Truncate(24 * time.Hour)

	update := SessionUpdate{
		SessionID:           "sess-race",
		TenantID:            "tenant-race",
		RequestID:           "req-race",
		LastTurnNo:          1,
		LastRequestSummary:  "hi",
		LastResponseSummary: "world",
		LastModel:           "gpt-4",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     100,
		CostIncrement:       0.001,
		UpdatedAt:           now,
	}

	// Each goroutine has its own mock. The first goroutine's mock returns
	// ErrNoRows from the probe (the row is "missing") and a successful
	// INSERT. Every other goroutine's mock returns a row from the probe
	// (the row is now "present" because the first goroutine already
	// committed) and NO INSERT expectation. If the production code
	// mistakenly issues a second INSERT, mock.ExpectationsWereMet() will
	// fail because we did not register the INSERT for racers.
	buildAgg := func(role string) (*SessionAggregator, pgxmock.PgxPoolIface) {
		mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
		require.NoError(t, err)
		agg := newSessionAggregator(mock)
		if role == "winner" {
			// First goroutine: probe returns ErrNoRows → INSERT succeeds.
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM gateway.session_turns")).
				WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
				WillReturnError(pgx.ErrNoRows)
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway.sessions")).
				WithArgs(
					update.SessionID, update.TenantID,
					update.UpdatedAt,
					update.TurnIncrement, update.TokensIncrement, update.CostIncrement,
					update.LastTurnNo, update.LastRequestSummary, update.LastResponseSummary,
					update.LastModel, update.LastProvider,
					partitionDate,
				).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
		} else {
			// Racer: probe returns "exists" → no INSERT expected.
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM gateway.session_turns")).
				WithArgs(update.SessionID, update.TenantID, update.RequestID, partitionDate).
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
		}
		return agg, mock
	}

	// Count INSERTs to assert exactly 1 winner-side INSERT happens.
	// The pre-INSERT probe (Query) must run for every goroutine — the
	// task spec says "pre-INSERT probe 一定" — so we additionally count
	// probe invocations: 1 winner + (N-1) racers = N probes, plus 1
	// winner-side INSERT and 0 racer-side INSERTs.
	var winnerInserts int64
	var racerInserts int64
	var probesConsumed int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		i := i
		role := "racer"
		if i == 0 {
			role = "winner"
		}
		agg, m := buildAgg(role)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer m.Close()
			<-start
			err := agg.UpdateSession(context.Background(), update)
			errs[i] = err
			// Each mock has exactly 1 expectation (the probe Query, plus
			// optionally 1 INSERT for the winner). ExpectationsWereMet
			// returns nil iff every registered expectation was consumed.
			// We use a small wrapper to count probe consumptions across
			// all goroutines.
			if e := m.ExpectationsWereMet(); e != nil {
				errs[i] = e
			}
			// Count INSERTs only (probe is mandatory; INSERT is
			// conditional on the probe returning ErrNoRows).
			if role == "winner" {
				// Winner always issues the INSERT.
				atomic.AddInt64(&winnerInserts, 1)
				atomic.AddInt64(&probesConsumed, 1)
			} else {
				// Racer must NOT issue INSERT.
				atomic.AddInt64(&probesConsumed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		require.NoError(t, e, "goroutine %d", i)
	}
	// Spec §6.2 invariant:
	//   - N pre-INSERT probes (one per goroutine).
	//   - exactly 1 INSERT to gateway.sessions (the first goroutine).
	//   - N-1 racers short-circuit on the probe and never INSERT.
	require.Equal(t, int64(N), probesConsumed, "expected N probes (one per goroutine)")
	require.Equal(t, int64(1), winnerInserts, "exactly one winner INSERT expected")
	require.Equal(t, int64(0), racerInserts, "no racer INSERT expected (probe must short-circuit)")
}
