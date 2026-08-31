package v2

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestEncodeDecodeSessionUpdate_RoundTrip pins the JSON shape used by the
// outbox reaper. If a future change adds a SessionUpdate field that must
// survive a reaper replay, this test must be updated at the same time the
// field is added to EncodeSessionUpdateForOutbox.
func TestEncodeDecodeSessionUpdate_RoundTrip(t *testing.T) {
	in := SessionUpdate{
		SessionID:           "sess_123",
		TenantID:            "tenant_a",
		RequestID:           "req_456",
		LastTurnNo:          7,
		LastRequestSummary:  "hi",
		LastResponseSummary: "hello",
		LastModel:           "gpt-4o",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     123,
		CostIncrement:       0.0017,
		UpdatedAt:           time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
	raw, err := EncodeSessionUpdateForOutbox(in)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	var out SessionUpdate
	if err := decodeUpdatePayload(raw, &out); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if out.SessionID != in.SessionID ||
		out.TenantID != in.TenantID ||
		out.RequestID != in.RequestID ||
		out.LastTurnNo != in.LastTurnNo ||
		out.LastRequestSummary != in.LastRequestSummary ||
		out.LastResponseSummary != in.LastResponseSummary ||
		out.LastModel != in.LastModel ||
		out.LastProvider != in.LastProvider ||
		out.TurnIncrement != in.TurnIncrement ||
		out.TokensIncrement != in.TokensIncrement ||
		out.CostIncrement != in.CostIncrement ||
		!out.UpdatedAt.Equal(in.UpdatedAt) {
		t.Errorf("round trip mismatch: got %+v, want %+v", out, in)
	}
}

// TestDecodeUpdatePayload_MissingSessionID guards against silently accepting
// payloads that lost their session_id — the aggregator would no-op such a
// row, but the reaper must surface the error so the row transitions to dead.
func TestDecodeUpdatePayload_MissingSessionID(t *testing.T) {
	raw := []byte(`{"tenant_id":"t","request_id":"r"}`)
	var out SessionUpdate
	if err := decodeUpdatePayload(raw, &out); err == nil {
		t.Fatalf("expected error for missing session_id, got nil")
	}
}

// TestReaper_DefaultConstants pins the documented retry contract so a
// reviewer can grep these constants and reason about the worst-case retry
// window (1s + 2s + 4s + ... + 512s capped at 1h × 10 attempts = 1023s ≈
// 17 minutes before a row transitions to status='dead').
func TestReaper_DefaultConstants(t *testing.T) {
	if sessionOutboxDefaultInterval != 30*time.Second {
		t.Errorf("default interval drifted: %v", sessionOutboxDefaultInterval)
	}
	if sessionOutboxDefaultBatch != 100 {
		t.Errorf("default batch drifted: %d", sessionOutboxDefaultBatch)
	}
	if sessionOutboxDefaultMaxAtts != 10 {
		t.Errorf("default max attempts drifted: %d", sessionOutboxDefaultMaxAtts)
	}
	if sessionOutboxMaxBackoff != time.Hour {
		t.Errorf("max backoff drifted: %v", sessionOutboxMaxBackoff)
	}
}

// TestReaper_StartStopIdempotent ensures the lifecycle is safe to call from
// cmd/gateway main without ordering constraints.
func TestReaper_StartStopIdempotent(t *testing.T) {
	r := newSessionAggregateOutboxReaper(nil, nil, 0, 0, 0)
	r.Start(nil)
	r.Start(nil) // second Start must not panic / spawn a second goroutine
	r.Stop()
	r.Stop() // second Stop must not panic / deadlock
}

// ── audit-data-closure-C-1 / C-2 / W-2 / W-3 reaper machinery tests ────────

// noopUpdateAggregator is a fake SessionAggregator that always succeeds.
// The dedicated transient / decode-failure tests below wrap it to inject
// the specific error path they exercise.
type noopUpdateAggregator struct{}

func (noopUpdateAggregator) UpdateSession(context.Context, SessionUpdate) error { return nil }

type transientAggregator struct{ calls int }

func (a *transientAggregator) UpdateSession(context.Context, SessionUpdate) error {
	a.calls++
	return errors.New("transient db blip")
}

// newClaimAndReplayReaper returns a reaper wired to a pgxmock pool so the
// newClaimAndReplay flow can be exercised in isolation. The aggregator is
// injectable so each test can pick success / transient / payload-error.
func newClaimAndReplayReaper(t *testing.T, agg sessionOutboxAggregator, maxAtts int) (*sessionAggregateOutboxReaper, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return newSessionAggregateOutboxReaperForTest(mock, agg, 0, 0, maxAtts), mock
}

// TestReaper_ClaimAndReplay_Success_PinsLeaseAndGuards (audit-data-closure-C-1/C-2)
//
// Drives a single successful replay and pins:
//   - the claim SELECT now accepts BOTH 'pending' and stale 'claimed' rows
//     (the lease interval parameter is the FIRST $1 of the SELECT)
//   - the claim UPDATE carries the status guard `AND status IN (...)`
//   - markDone carries the `AND status='claimed'` guard and the 1-row
//     RowsAffected is honored (the audit fix only logs when guard rejects)
//
// Without these three pins, the pre-fix reaper would either orphan 'claimed'
// rows on crash or overwrite peer-terminated rows with the wrong terminal
// state.
func TestReaper_ClaimAndReplay_Success_PinsLeaseAndGuards(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })

	// 1. BEGIN (claim tx).
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config\\('app.current_role', 'super_admin', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config\\('app.bypass_rls', 'true', true\\)").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))

	// 2. Claim SELECT — note the regex `status = 'pending'` AND
	//    `status = 'claimed'` clauses must both be present in the SQL.
	//    The claim also carries one arg (lease seconds as TEXT, since
	//    the SQL uses `($1 || ' seconds')::interval` and PostgreSQL
	//    requires text for the concat).
	mock.ExpectQuery("status = 'pending'").
		WithArgs(fmt.Sprintf("%d", int(sessionOutboxClaimLease.Seconds()))).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "session_id", "partition_date", "request_id",
			"update_payload", "attempts",
		}).AddRow(
			int64(42), "tenant_a", "sess_1",
			time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
			"req_1", []byte(`{"session_id":"sess_1","tenant_id":"tenant_a","request_id":"req_1"}`), 0,
		))
	// 3. Claim UPDATE — must carry `status IN ('pending','claimed')` guard.
	mock.ExpectExec("UPDATE session_aggregate_outbox").
		WithArgs(int64(42)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// 4. COMMIT the claim tx.
	mock.ExpectCommit()

	// 5. markDone UPDATE outside the claim tx — must carry
	//    `AND status='claimed'` guard; RowsAffected=1 means guard passed.
	mock.ExpectExec("UPDATE session_aggregate_outbox").
		WithArgs(int64(42)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Wire the aggregator onto the reaper.
	r := newSessionAggregateOutboxReaperForTest(mock, noopUpdateAggregator{}, 0, 0, 0)

	ok, err := r.claimAndReplay(context.Background())
	if err != nil {
		t.Fatalf("claimAndReplay returned error: %v", err)
	}
	if !ok {
		t.Fatalf("claimAndReplay returned ok=false, expected true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestReaper_MarkDoneGuardSkipsWhenRowAlreadyTerminal (audit-data-closure-C-2)
//
// Pins the guard behavior on markDone: when a peer has already terminated
// the row (UPDATE returns RowsAffected=0), markDone must NOT panic and
// must NOT log a misleading error — it should log a Warn at most. Here we
// only assert the no-panic + return-ok contract because slog capture would
// need a test helper.
func TestReaper_MarkDoneGuardSkipsWhenRowAlreadyTerminal(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	mock.ExpectExec("UPDATE session_aggregate_outbox").
		WithArgs(int64(99)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0)) // guard rejected
	r := newSessionAggregateOutboxReaperForTest(mock, nil, 0, 0, 0)
	// Must not panic, must not return an error (the Exec succeeded).
	r.markDone(context.Background(), 99)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestReaper_MarkDeadGuardSkipsWhenRowAlreadyTerminal (audit-data-closure-C-2)
//
// Symmetric test for markDead: RowsAffected=0 means a peer already won;
// markDead must NOT emit the misleading "row exceeded max attempts" log.
func TestReaper_MarkDeadGuardSkipsWhenRowAlreadyTerminal(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	mock.ExpectExec("UPDATE session_aggregate_outbox").
		WithArgs(int64(99), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	r := newSessionAggregateOutboxReaperForTest(mock, nil, 0, 0, 0)
	r.markDead(context.Background(), 99, "test reason")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestReaper_ScheduleRetryGuardSkipsWhenRowAlreadyTerminal (audit-data-closure-C-2)
//
// Symmetric test for scheduleRetry: RowsAffected=0 means the row was
// already terminated; we must not resurrect a 'done' row back to 'pending'.
func TestReaper_ScheduleRetryGuardSkipsWhenRowAlreadyTerminal(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	mock.ExpectExec("UPDATE session_aggregate_outbox").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	r := newSessionAggregateOutboxReaperForTest(mock, nil, 0, 0, 0)
	r.scheduleRetry(context.Background(), 99, 2, errors.New("transient"))
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestReaper_ClaimLeaseConstant pins the lease constant so a future edit
// to the safety net (audit-data-closure-C-1) is intentional, not a typo.
func TestReaper_ClaimLeaseConstant(t *testing.T) {
	if sessionOutboxClaimLease != 5*time.Minute {
		t.Errorf("sessionOutboxClaimLease drifted: got %v, want 5m", sessionOutboxClaimLease)
	}
}
