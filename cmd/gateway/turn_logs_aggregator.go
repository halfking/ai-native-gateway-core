package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
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

// The flush statements, hoisted to consts so their exact shape is
// assertable (pgxmock does not execute SQL and cannot observe merge or
// row-identity semantics). See turn_logs_aggregator_flush_test.go.
const (
	// Row lock on the sessions row serializes concurrent flushes for the
	// same (tenant, session) — see the concurrency note on
	// AggregateAndFlush. Reading the current column value in the same
	// statement is what makes the Go-side read-modify-write safe.
	flushLockQuery = `
		SELECT turn_logs_summary
		FROM public.sessions
		WHERE tenant_id=$1 AND session_id=$2
		FOR UPDATE
	`

	flushSelectQuery = `
		SELECT id, turn_no, stage, stage_status, latency_ms, started_at, COALESCE(error_message,'')
		FROM public.session_turn_logs
		WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()
		ORDER BY turn_no ASC, started_at ASC
	`

	// Full replacement is correct here because the value written is the
	// merge of the locked row's current value with this flush's rows —
	// the merge happens in Go (mergeSummaries), not in SQL.
	flushUpdateQuery = `
		UPDATE public.sessions
		SET turn_logs_summary = $1::jsonb
		WHERE tenant_id=$2 AND session_id=$3
	`

	// Delete exactly the read set, by primary key (§17 F-10).
	flushDeleteQuery = `
		DELETE FROM public.session_turn_logs
		WHERE id = ANY($1)
	`
)

// AggregateAndFlush reads non-expired session_turn_logs for the given
// (tenant, session), groups them by turn_no, merges the result into
// public.sessions.turn_logs_summary, and deletes exactly the rows it
// aggregated — all inside one transaction.
//
// Concurrency and loss notes (2026-09-27 critical audit §17 F-9/F-10;
// R72 audit round fixes the remaining per-turn replacement hole):
//
//   - The delete is keyed on the primary keys captured during the SELECT, not
//     on a re-derived predicate. A predicate-derived DELETE re-evaluated as a
//     *new* statement would also match rows written between the SELECT and
//     the DELETE — deleting a stage that was never aggregated. Keying on the
//     ids we read makes the flush exactly cover its own read set.
//   - The whole flush runs in one transaction with a `FOR UPDATE` lock on the
//     sessions row. Two flushes for the same (tenant, session) therefore
//     serialize, and the second one re-reads the column value the first one
//     committed — a concurrent read-modify-write cannot drop the other's
//     payload. (Without the lock, two whole-column SETs race and the loser's
//     rows are already deleted — permanent loss.)
//   - The merge itself is a per-turn stages-array UNION with dedup, not a
//     per-turn key replacement. The SQL `||` operator merges top-level keys
//     only, so it replaced the whole `turn_N` entry: a tick landing inside
//     the writer's per-stage INSERT loop (session_writer_v2.go) aggregated
//     the first half of a turn's rows, the next tick aggregated the rest, and
//     the second merge overwrote `turn_N` with only the later rows — the
//     early stages were gone. Union-dedup also makes the flush idempotent
//     against a crash between UPDATE and DELETE: the same rows re-merge to
//     the same value.
//
// If the sessions row does not exist the flush is a no-op and the stage rows
// are left in place (the TTL sweep reclaims them); deleting them here would
// silently drop stages that a later snapshot upsert could still have
// summarized.
func (a *TurnLogsAggregator) AggregateAndFlush(ctx context.Context, tenantID, sessionID string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var existingRaw []byte
	err = tx.QueryRow(ctx, flushLockQuery, tenantID, sessionID).Scan(&existingRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		// Orphan stage rows for a session with no snapshot row: leave them
		// for the TTL sweep rather than deleting unaggregated data.
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock sessions row: %w", err)
	}

	rows, err := tx.Query(ctx, flushSelectQuery, tenantID, sessionID)
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

	// Nothing to aggregate: no DELETE — the read set is empty by definition.
	if len(byTurn) == 0 {
		return nil
	}

	summary := mergeSummaries(existingRaw, byTurn, time.Now())
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if _, err := tx.Exec(ctx, flushUpdateQuery, string(payload), tenantID, sessionID); err != nil {
		return fmt.Errorf("update sessions: %w", err)
	}

	if _, err := tx.Exec(ctx, flushDeleteQuery, ids); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return tx.Commit(ctx)
}

// mergeSummaries unions the freshly-read stage rows into the existing
// turn_logs_summary value. Per turn key the stages arrays are concatenated
// and deduplicated (by the full stage identity — stage name, status, latency,
// timestamp, error), then sorted by start time so the display order is
// deterministic no matter which flush contributed which rows. Turn keys
// present only in the existing summary are carried over untouched.
//
// An existing value that fails to parse is treated as empty (payload wins) —
// that can only happen if some other writer stored non-summary JSON, and
// reverting to the pre-R72 replacement behavior for that one column value is
// better than failing the flush forever.
func mergeSummaries(existing []byte, byTurn map[int][]StageLog, builtAt time.Time) map[string]LogSummary {
	merged := make(map[string]LogSummary, len(byTurn))
	if len(existing) > 0 {
		var prev map[string]LogSummary
		if err := json.Unmarshal(existing, &prev); err == nil && prev != nil {
			merged = prev
		}
	}

	for turnNo, stages := range byTurn {
		key := fmt.Sprintf("turn_%d", turnNo)
		combined := append(append([]StageLog{}, merged[key].Stages...), stages...)
		combined = dedupStages(combined)
		sort.Slice(combined, func(i, j int) bool {
			if !combined[i].StartedAt.Equal(combined[j].StartedAt) {
				return combined[i].StartedAt.Before(combined[j].StartedAt)
			}
			return combined[i].Stage < combined[j].Stage
		})
		merged[key] = LogSummary{
			Stages:  combined,
			TurnNo:  turnNo,
			BuiltAt: builtAt,
		}
	}
	return merged
}

// dedupStages removes exact-duplicate stage entries. The identity is the
// formatted UTC timestamp (not raw time.Time equality): a stage row read back
// from the stored summary has been through a JSON round-trip, and == on
// time.Time compares location pointers that do not survive it.
func dedupStages(stages []StageLog) []StageLog {
	seen := make(map[string]struct{}, len(stages))
	out := stages[:0]
	for _, s := range stages {
		id := s.Stage + "|" + s.Status + "|" + strconv.Itoa(s.LatencyMs) + "|" +
			s.StartedAt.UTC().Format(time.RFC3339Nano) + "|" + s.Error
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, s)
	}
	return out
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
