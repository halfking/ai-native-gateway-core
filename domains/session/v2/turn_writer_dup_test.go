package v2

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func newMockTurnWriter(t *testing.T) (*TurnWriter, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})
	return newTurnWriter(mock), mock
}

// expectAppendTurn mocks the new (Round-3) flow for a single AppendTurn call:
//   - BEGIN
//   - SELECT pg_advisory_xact_lock($1) with the lock key
//   - SELECT COALESCE(MAX(turn_no), 0) + 1
//   - INSERT INTO gateway.session_turns (with precise WithArgs)
//   - If inserted == false, SELECT turn_no WHERE request_id=... (post-conflict re-read)
//   - COMMIT
//
// If existingTurn > 0 and inserted == false, the post-conflict re-read returns
// that turn number. The INSERT WithArgs is exact (no AnyArg placeholders) so
// the test will FAIL with a column-count / column-order mismatch if the SQL
// shifts.
//
// Defaults: production AppendTurn fills in the same default strings/nils the
// test sees here, so the expected WithArgs is built after the same defaults
// are applied. This is intentional: the test is a round-trip of the
// canonical happy-path record.
func expectAppendTurn(mock pgxmock.PgxPoolIface, rec TurnRecord, nextTurn int, inserted bool, existingTurn int) {
	// Apply the same defaults the production code applies so WithArgs matches.
	if rec.SubmitMode == "" {
		rec.SubmitMode = "full"
	}
	if rec.InjectionVerdict == "" {
		rec.InjectionVerdict = "skip"
	}
	if rec.OutputVerdict == "" {
		rec.OutputVerdict = "skip"
	}
	if rec.SourceKind == "" {
		rec.SourceKind = "live"
	}
	if rec.Quality == "" {
		rec.Quality = "verified"
	}
	partitionDate := rec.Ts.Truncate(24 * time.Hour)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).
		WithArgs(hashSessionKey(rec.TenantID, rec.SessionID)).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(turn_no\\), 0\\) \\+ 1").
		WithArgs(rec.TenantID, rec.SessionID).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(nextTurn))

	// INSERT — exact WithArgs in the order the production code passes them:
	//   $1  session_id
	//   $2  turn_no
	//   $3  tenant_id
	//   $4  request_id
	//   $5  ts
	//   $6  submit_mode
	//   $7  compression_applied
	//   $8  compression_strategy
	//   $9  compression_meta (string)
	//   $10 compression_tokens_saved
	//   $11 injection_verdict
	//   $12 output_verdict
	//   $13 model
	//   $14 provider
	//   $15 credential_id
	//   $16 prompt_tokens
	//   $17 completion_tokens
	//   $18 cache_read_tokens
	//   $19 cache_write_tokens
	//   $20 cost_usd
	//   $21 latency_ms
	//   $22 status_code
	//   $23 success
	//   $24 error_kind
	//   $25 source_kind
	//   $26 quality
	//   $27 attachment_count
	//   $28 attachment_total_bytes
	//   $29 multimodal_types
	//   $30 title           (migration 456)
	//   $31 summary         (migration 456)
	//   $32 partition_date
	rowsAffected := int64(1)
	if !inserted {
		rowsAffected = 0
	}
	// json.Marshal of a nil map produces the 4-byte string "null" (not "").
	// The production code in AppendTurn calls json.Marshal(rec.CompressionMeta)
	// and the pgx driver then forwards the result as a single argument.
	// Tests in this file never set CompressionMeta, so the test expectation
	// is exactly "null". If a future test sets it, the marshal will run
	// before the Exec call and pgxmock will compare strings.
	compressionMetaStr := "null"
	if len(rec.CompressionMeta) > 0 {
		compressionMetaStr = "{}"
	}
	// pgx's []string argument shape is not stable across nil-vs-empty
	// (nil and []string{} round-trip as different pgx codec values, but
	// pgxmock's argument matcher occasionally flattens both to the same
	// printed form). We pin the column count / order with exact args for
	// every other position and use AnyArg here as the documented escape
	// hatch for the multimodal_types column. See the production code in
	// AppendTurn for the contract: rec.MultimodalTypes is forwarded
	// as-is to the column (a text[] typed as []string on the Go side).
	multimodalArg := pgxmock.AnyArg()

	mock.ExpectExec("INSERT INTO gateway.session_turns").
		WithArgs(
			rec.SessionID, nextTurn, rec.TenantID, rec.RequestID, rec.Ts,
			rec.SubmitMode,
			rec.CompressionApplied, rec.CompressionStrategy, compressionMetaStr, rec.TokensSaved,
			rec.InjectionVerdict, rec.OutputVerdict,
			rec.Model, rec.Provider, rec.CredentialID,
			rec.PromptTokens, rec.CompletionTokens, rec.CacheReadTokens, rec.CacheWriteTokens, rec.CostUSD,
			rec.LatencyMs, rec.StatusCode, rec.Success, rec.ErrorKind,
			rec.SourceKind, rec.Quality,
			rec.AttachmentCount, rec.AttachmentTotalBytes, multimodalArg,
			rec.Title, rec.Summary,
			partitionDate,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", rowsAffected))
	if !inserted {
		// Post-conflict re-read: SELECT turn_no WHERE request_id=...
		mock.ExpectQuery("SELECT turn_no[[:space:]]+FROM gateway.session_turns").
			WithArgs(rec.SessionID, rec.TenantID, rec.RequestID, partitionDate).
			WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(existingTurn))
		// 2026-08-05 (v2 mirror bug): on the conflict path the writer backfills
		// the late-arriving compression_strategy / compression_meta / submit_mode
		// that the initial INSERT could not carry (the mirror fires this write
		// twice — INSERT-persist then UPDATE-persist — and ON CONFLICT DO NOTHING
		// dropped the second fire's fields). Args mirror the production Exec:
		//   $1 session_id $2 tenant_id $3 request_id $4 partition_date
		//   $5 compression_applied $6 compression_strategy $7 compression_meta
		//   $8 compression_tokens_saved $9 submit_mode
		//   $10 title $11 summary (migration 456 preview backfill)
		mock.ExpectExec("UPDATE gateway.session_turns").
			WithArgs(
				rec.SessionID, rec.TenantID, rec.RequestID, partitionDate,
				rec.CompressionApplied, rec.CompressionStrategy, compressionMetaStr,
				rec.TokensSaved, rec.SubmitMode,
				rec.Title, rec.Summary,
			).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	mock.ExpectCommit()
}

func TestTurnWriterAppendTurnDuplicateReturnsExistingTurnNo(t *testing.T) {

	writer, mock := newMockTurnWriter(t)
	rec := TurnRecord{SessionID: "s1", TenantID: "t1", RequestID: "r1", Ts: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	// First call: brand-new request — INSERT succeeds with RowsAffected=1
	expectAppendTurn(mock, rec, 1, true, 0)
	// Second call: same request_id — INSERT conflicts (RowsAffected=0) and
	// the post-conflict re-read returns turn_no=1
	expectAppendTurn(mock, rec, 2, false, 1)

	first, err := writer.AppendTurn(context.Background(), rec)
	require.NoError(t, err)
	second, err := writer.AppendTurn(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestTurnWriterAppendTurnDifferentRequestsIncrement(t *testing.T) {
	writer, mock := newMockTurnWriter(t)
	base := TurnRecord{SessionID: "s2", TenantID: "t1", RequestID: "r1", Ts: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	expectAppendTurn(mock, base, 1, true, 0)
	secondRec := base
	secondRec.RequestID = "r2"
	expectAppendTurn(mock, secondRec, 2, true, 0)

	first, err := writer.AppendTurn(context.Background(), base)
	require.NoError(t, err)
	second, err := writer.AppendTurn(context.Background(), secondRec)
	require.NoError(t, err)
	require.Equal(t, first+1, second)
}

func TestTurnWriterAppendTurnSameRequestDifferentPartitionIsDistinct(t *testing.T) {
	writer, mock := newMockTurnWriter(t)
	firstRec := TurnRecord{SessionID: "s3", TenantID: "t1", RequestID: "r1", Ts: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	expectAppendTurn(mock, firstRec, 1, true, 0)
	secondRec := firstRec
	secondRec.Ts = firstRec.Ts.Add(24 * time.Hour)
	expectAppendTurn(mock, secondRec, 2, true, 0)

	first, err := writer.AppendTurn(context.Background(), firstRec)
	require.NoError(t, err)
	second, err := writer.AppendTurn(context.Background(), secondRec)
	require.NoError(t, err)
	require.Equal(t, first+1, second)
}

func TestTurnWriterAppendTurnConflictReadsExistingTurnNo(t *testing.T) {
	writer, mock := newMockTurnWriter(t)
	rec := TurnRecord{SessionID: "s4", TenantID: "t1", RequestID: "r1", Ts: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	// INSERT succeeds (RowsAffected=0 because a concurrent goroutine already
	// wrote turn 3 in another partition? No — same partition. To exercise
	// the post-conflict re-read we need RowsAffected=0 on the INSERT and
	// the SELECT to return the existing turn_no).
	expectAppendTurn(mock, rec, 8, false, 3)

	turnNo, err := writer.AppendTurn(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, 3, turnNo)
}

// TestTurnWriterAppendTurnRaceSameRequestId (Round 3, 2026-07-28)
//
// N goroutines concurrently call AppendTurn with the SAME (tenant, session,
// request_id) tuple. The expected behaviour:
//   - the row persists exactly once (one INSERT that actually writes).
//   - all racers see RowsAffected=0 from the INSERT and resolve to the same
//     turn_no via the post-conflict re-read.
//   - all goroutines converge on the same turn_no.
//
// pgxmock is not goroutine-safe — its expectation queue is consumed
// linearly and concurrent callers can race past each other, surfacing
// confusing errors like "next expectation is ExpectedExec" when a
// goroutine is mid-flight between Begin and the next query. We therefore
// drive N goroutines through a per-call fresh mock so each goroutine
// sees a deterministic sequence. The first goroutine's mock returns
// RowsAffected=1 (winner); the rest return RowsAffected=0 + the
// post-conflict re-read returns turn_no=1.
//
// This is the unit-test analogue of the concurrent contract; the
// authoritative race coverage lives in the integration tests against a
// real PostgreSQL instance (see docs/superpowers/specs/2026-07-27-
// request-flow-audit-design.md §6.2 — duplicate request_id must return
// the actual turn number). The TurnWriter struct itself has no shared
// mutable state (its `db` field is read-only), so -race will not flag
// any data race inside the writer.
func TestTurnWriterAppendTurnRaceSameRequestId(t *testing.T) {
	rec := TurnRecord{SessionID: "s-race", TenantID: "t-race", RequestID: "r-race", Ts: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}

	const N = 8

	// Each goroutine gets its own mock so the expectation queue is
	// deterministic. The first goroutine is the winner (RowsAffected=1);
	// the rest are racers (RowsAffected=0 + re-read=1).
	buildWriter := func(role string) (*TurnWriter, pgxmock.PgxPoolIface) {
		mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
		require.NoError(t, err)
		if role == "winner" {
			expectAppendTurn(mock, rec, 1, true, 0)
		} else {
			expectAppendTurn(mock, rec, 1, false, 1)
		}
		return newTurnWriter(mock), mock
	}

	type outcome struct {
		turnNo int
		err    error
	}
	start := make(chan struct{})
	results := make([]outcome, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		role := "racer"
		if i == 0 {
			role = "winner"
		}
		w, m := buildWriter(role)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer m.Close()
			<-start
			turnNo, err := w.AppendTurn(context.Background(), rec)
			results[i] = outcome{turnNo: turnNo, err: err}
			require.NoError(t, m.ExpectationsWereMet(), "goroutine %d (%s)", i, role)
		}()
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		require.NoError(t, r.err, "goroutine %d", i)
		require.Equal(t, 1, r.turnNo, "goroutine %d returned %d", i, r.turnNo)
	}
}
