package v2

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBuildTurnLogsInsert_SingleStatementForWholeTurn pins the D-1 fix
// (12h audit round 15): a turn's stage rows must be rendered as ONE
// multi-row INSERT so they become visible atomically. The aggregator's
// flush merges turn_logs_summary by top-level turn_N key replacement
// (cmd/gateway flushMergeQuery); a per-row write loop allowed a 5-minute
// tick to land mid-turn, flushing a partial turn and letting the next flush
// replace the same turn_N key with only the remaining stages.
func TestBuildTurnLogsInsert_SingleStatementForWholeTurn(t *testing.T) {
	base := time.Now()
	expires := base.Add(24 * time.Hour)
	recs := make([]TurnLogRecord, 0, 7)
	for i, stage := range []string{"routing", "compression", "injection_check", "llm_call", "output_check", "response", "cache_update"} {
		recs = append(recs, TurnLogRecord{
			SessionID:   "sess_batch",
			TurnNo:      1,
			TenantID:    "tenant_batch",
			RequestID:   "req_batch",
			Stage:       stage,
			StageStatus: "success",
			EventData:   map[string]interface{}{"i": i},
			StartedAt:   base.Add(time.Duration(i) * time.Second),
			// Negative on the first row pins the latency clamp.
			CompletedAt: base.Add(time.Duration(i-1) * time.Second),
		})
	}

	query, args, err := buildTurnLogsInsert(recs, expires)
	if err != nil {
		t.Fatal(err)
	}

	// One statement for the whole turn.
	if got := strings.Count(strings.ToUpper(query), "VALUES"); got != 1 {
		t.Errorf("buildTurnLogsInsert must render exactly one VALUES clause (got %d) — per-row loops reintroduce the D-1 partial-turn race", got)
	}
	// One tuple per record.
	if got := strings.Count(query, "), ($"); got != len(recs)-1 {
		t.Errorf("expected %d tuple separators, got %d", len(recs)-1, got)
	}
	// 12 params per record, numbered contiguously.
	if len(args) != len(recs)*turnLogsInsertColumns {
		t.Errorf("expected %d args, got %d", len(recs)*turnLogsInsertColumns, len(args))
	}
	for i := 0; i < len(recs); i++ {
		b := i * turnLogsInsertColumns
		// event_data binds as string, not []byte (SimpleProtocol bytea hex
		// trap, doc §3.2).
		if _, ok := args[b+6].(string); !ok {
			t.Errorf("row %d: event_data arg must be string (got %T) — []byte binds as bytea hex under SimpleProtocol", i, args[b+6])
		}
		// Shared expiry: every row's expires_at is the same timestamp.
		if args[b+11] != expires {
			t.Errorf("row %d: expires_at must be the shared batch expiry", i)
		}
		// Latency clamp: negative deltas become 0.
		if i == 0 {
			if got := args[b+10].(int); got != 0 {
				t.Errorf("row 0: negative latency must clamp to 0, got %d", got)
			}
		}
	}
	// Param placeholders stay contiguous 1..N (a gap would make pgx bind the
	// wrong args silently under positional mismatch). The ::text::jsonb cast
	// suffix on the event_data placeholder is one of the accepted boundaries.
	for i := 1; i <= len(recs)*turnLogsInsertColumns; i++ {
		tok := "$" + strconv.Itoa(i)
		if !strings.Contains(query, tok+",") && !strings.Contains(query, tok+")") && !strings.Contains(query, tok+"::") {
			t.Errorf("query is missing placeholder $%d", i)
		}
	}
}

// TestWriteStages_EmptyIsNoop pins the early return: an empty batch must not
// touch the pool (a bare VALUES with zero tuples would be a SQL syntax error).
func TestWriteStages_EmptyIsNoop(t *testing.T) {
	w := NewTurnLogsWriter(nil)
	if err := w.WriteStages(context.Background(), nil); err != nil {
		t.Fatalf("empty batch must be a nil-error no-op, got %v", err)
	}
}

// TestSessionWriterUsesBatchedTurnLogsWrite pins the D-1 caller side: the
// production write path must go through one WriteStages call, not a per-row
// WriteStage loop. Textual, following the migration_753_test.go C2b precedent
// (source-pinned because the builder tests cannot see which path production
// takes, and pgxmock cannot observe statement count).
func TestSessionWriterUsesBatchedTurnLogsWrite(t *testing.T) {
	src, err := os.ReadFile("session_writer_v2.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "WriteStages(ctx, recs)") {
		t.Error("session_writer_v2.go step 5 must batch the turn's stage rows via WriteStages — a per-row WriteStage loop reintroduces the D-1 partial-turn race")
	}
	if strings.Contains(s, "WriteStage(ctx, TurnLogRecord{") {
		t.Error("session_writer_v2.go must not loop WriteStage per row (12h audit round 15, D-1)")
	}
}
