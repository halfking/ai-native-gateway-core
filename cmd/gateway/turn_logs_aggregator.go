package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StageLog is one per-stage entry from public.session_turn_logs.
type StageLog struct {
	Stage     string    `json:"stage"`
	Status    string    `json:"status"`
	LatencyMs int       `json:"latency_ms"`
	StartedAt time.Time `json:"started_at"`
	Error     string    `json:"error,omitempty"`
}

// LogSummary is the aggregated JSON we write into public.sessions.turn_logs_summary.
type LogSummary struct {
	Stages  []StageLog `json:"stages"`
	TurnNo  int        `json:"turn_no"`
	BuiltAt time.Time  `json:"built_at"`
}

// TurnLogsAggregator reads per-stage logs for a (tenant, session), aggregates
// them by turn_no into a JSON summary, and writes the result into
// public.sessions.turn_logs_summary. After a successful flush the rows are
// deleted so they don't get re-aggregated.
type TurnLogsAggregator struct {
	db *pgxpool.Pool
}

func NewTurnLogsAggregator(db *pgxpool.Pool) *TurnLogsAggregator {
	return &TurnLogsAggregator{db: db}
}

// SessionKey identifies one (tenant, session) whose stage logs are pending
// aggregation.
type SessionKey struct {
	TenantID  string
	SessionID string
}

// pendingSessionsQuery is the poll query, hoisted to a package-level const so
// its ORDER BY can be asserted by tests (see turn_logs_aggregator_poll_test.go).
// pgxmock does not execute SQL and cannot observe row ordering, so pinning the
// text is the only available check — which is acceptable only because the
// assertion is mutation-proven to fail when the ORDER BY is removed.
const pendingSessionsQuery = `
		SELECT tenant_id, session_id
		FROM public.session_turn_logs
		WHERE expires_at > NOW()
		GROUP BY tenant_id, session_id
		ORDER BY MIN(started_at) ASC, tenant_id ASC, session_id ASC
		LIMIT $1
	`

// normalizePendingSessionLimit keeps a nonsensical bound from becoming a SQL
// error at runtime (LIMIT 0 / LIMIT -1).
func normalizePendingSessionLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	return limit
}

// PendingSessions returns up to limit (tenant, session) pairs that still have
// unexpired stage logs waiting to be flushed into sessions.turn_logs_summary.
//
// 2026-09-27 (critical audit): this query used to live inline in main.go's
// aggregator goroutine as a bare
//
//	SELECT tenant_id, session_id ... WHERE expires_at > NOW()
//	GROUP BY tenant_id, session_id LIMIT 100
//
// with **no ORDER BY**, so PostgreSQL was free to return any 100 rows. Two
// problems, both silent:
//
//  1. Non-deterministic selection. A quiet session could lose the arbitrary
//     cut repeatedly while busier sessions kept re-entering the candidate set
//     (a session with fresh traffic always has unexpired rows).
//  2. The cut-off is silent data loss, not just delay. PendingSessions only
//     ever returns rows with expires_at > NOW(), and the TTL sweep deletes
//     rows once they expire. A session that never wins the cut therefore has
//     its stage logs deleted before they are ever aggregated — the turn never
//     appears in turn_logs_summary, with no error anywhere.
//
// ORDER BY MIN(started_at) makes selection oldest-first, which turns the
// candidate set into a FIFO queue: rows are removed as they are flushed, so
// the oldest unflushed work is always next and quiet sessions cannot be
// indefinitely crowded out. tenant_id/session_id are tie-breakers so the
// order is total and reproducible rather than merely non-decreasing.
//
// Throughput note (not a correctness issue, but do the arithmetic before
// raising the limit): one page per tick at the 5-minute interval is
// 100 sessions / 5 min = 1200 sessions/hour. Backlog only builds above that,
// and a session has the full TTL (24h default) of unexpired rows to be picked
// up in, i.e. ~28800 sessions of headroom. Draining more pages per tick
// multiplies the GROUP BY cost per tick, so it should be a measured change,
// not a guess.
func (a *TurnLogsAggregator) PendingSessions(ctx context.Context, limit int) ([]SessionKey, error) {
	limit = normalizePendingSessionLimit(limit)
	rows, err := a.db.Query(ctx, pendingSessionsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("pending sessions query: %w", err)
	}
	defer rows.Close()

	out := make([]SessionKey, 0, limit)
	for rows.Next() {
		var k SessionKey
		if err := rows.Scan(&k.TenantID, &k.SessionID); err != nil {
			return nil, fmt.Errorf("pending sessions scan: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending sessions rows: %w", err)
	}
	return out, nil
}

// The three flush statements, hoisted to consts so their exact shape is
// assertable (pgxmock does not execute SQL and cannot observe jsonb merge or
// row-identity semantics). See turn_logs_aggregator_flush_test.go.
const (
	flushSelectQuery = `
		SELECT id, turn_no, stage, stage_status, latency_ms, started_at, COALESCE(error_message,'')
		FROM public.session_turn_logs
		WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()
		ORDER BY turn_no ASC, started_at ASC
	`

	// Merge, never replace — see the long note at the call site (§17 F-9).
	flushMergeQuery = `
		UPDATE public.sessions
		SET turn_logs_summary = COALESCE(turn_logs_summary, '{}'::jsonb) || $1::jsonb
		WHERE tenant_id=$2 AND session_id=$3
	`

	// Delete exactly the read set, by primary key (§17 F-10).
	flushDeleteQuery = `
		DELETE FROM public.session_turn_logs
		WHERE id = ANY($1)
	`
)

// AggregateAndFlush reads non-expired session_turn_logs for the given
// (tenant, session), groups them by turn_no, writes a JSON summary to
// public.sessions.turn_logs_summary, and deletes exactly the rows it
// aggregated.
//
// Concurrency and race notes (2026-09-27 critical audit, §17 F-10):
//
//   - The delete is keyed on the primary keys captured during the SELECT, not
//     on a re-derived predicate. The old form was
//     `DELETE ... WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()`,
//     which re-evaluates the predicate as a *new* statement. A stage row
//     written for an in-flight turn in the gap between the SELECT and the
//     DELETE was therefore deleted without ever being aggregated — silent
//     loss, no error. Deleting by id makes the flush exactly cover what it
//     read.
//   - That old form also made the empty-session branch a no-op: when the
//     SELECT found nothing with expires_at > NOW(), the identically-filtered
//     DELETE could not match anything either. With id-based deletion the
//     branch needs no DELETE at all, so it is gone rather than kept as a
//     misleading "trim stray rows" step.
//   - Two flushes for the same (tenant, session) can still interleave. The
//     summary merge is keyed by turn_N, so an interleaving converges rather
//     than losing a turn: worst case a turn is written twice with an
//     equivalent value.
func (a *TurnLogsAggregator) AggregateAndFlush(ctx context.Context, tenantID, sessionID string) error {
	rows, err := a.db.Query(ctx, flushSelectQuery, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	byTurn := map[int][]StageLog{}
	var ids []int64
	for rows.Next() {
		var id int64
		var turnNo int
		var sl StageLog
		if err := rows.Scan(&id, &turnNo, &sl.Stage, &sl.Status, &sl.LatencyMs, &sl.StartedAt, &sl.Error); err != nil {
			rows.Close()
			return fmt.Errorf("scan: %w", err)
		}
		ids = append(ids, id)
		byTurn[turnNo] = append(byTurn[turnNo], sl)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows.Err: %w", err)
	}

	// Nothing to aggregate. The previous implementation ran a DELETE here
	// under the comment "trim any stray rows that already expired", but its
	// predicate was expires_at > NOW() — the same filter the SELECT just used
	// to conclude there was nothing. It could never match. Removed rather
	// than left in place as a comment that describes the opposite of what the
	// code does.
	if len(byTurn) == 0 {
		return nil
	}

	summary := make(map[string]LogSummary, len(byTurn))
	builtAt := time.Now()
	for turnNo, stages := range byTurn {
		summary[fmt.Sprintf("turn_%d", turnNo)] = LogSummary{
			Stages:  stages,
			TurnNo:  turnNo,
			BuiltAt: builtAt,
		}
	}
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// Merge, do not overwrite.
	//
	// 2026-09-27 (critical audit, §17 F-9): this used to be
	// `SET turn_logs_summary = $1::jsonb`, a full replacement. Because the
	// flush then DELETEs the rows it aggregated, a session with continuing
	// traffic is re-polled on the very next tick, finds only the *new* rows,
	// and replaces the whole column — destroying every earlier turn's entry.
	// The final summary for a long-lived active session was whatever the last
	// 5-minute batch happened to contain, not the session's turn history.
	//
	// jsonb `||` is a shallow merge over top-level keys, and the keys are
	// `turn_N`, which are distinct per batch, so a merge is exactly right.
	// It also makes a re-flush idempotent: if the process dies between the
	// UPDATE and the DELETE, the same turn is aggregated again next tick and
	// overwrites its own key with an equivalent value instead of wiping the
	// rest. COALESCE is required because `NULL || x` is NULL in SQL.
	if _, err := a.db.Exec(ctx, flushMergeQuery, string(payload), tenantID, sessionID); err != nil {
		return fmt.Errorf("update sessions: %w", err)
	}

	// Delete exactly the rows aggregated above, by primary key.
	//
	// The previous form re-derived the predicate (`… AND expires_at > NOW()`)
	// as a separate statement, so it could also delete rows inserted after the
	// SELECT — losing a stage that was never aggregated. Keying on the ids we
	// read makes the flush cover precisely its own read set.
	if _, err := a.db.Exec(ctx, flushDeleteQuery, ids); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// aggregate is the pure helper used by tests. It builds a LogSummary from a
// slice of StageLog rows and timestamps the build at "now".
func aggregate(logs []StageLog) (*LogSummary, error) {
	if logs == nil {
		return nil, nil
	}
	s := &LogSummary{Stages: logs, BuiltAt: time.Now()}
	return s, nil
}
