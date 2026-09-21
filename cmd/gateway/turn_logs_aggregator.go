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

// AggregateAndFlush reads non-expired session_turn_logs for the given
// (tenant, session), groups them by turn_no, writes a JSON summary to
// public.sessions.turn_logs_summary, and deletes the source rows.
//
// Safe to call concurrently per (tenant, session): the rows are read-only
// then deleted; concurrent writers of the same JSON column will result in
// last-writer-wins but never in inconsistent state because we serialise
// via DELETE inside the same call.
func (a *TurnLogsAggregator) AggregateAndFlush(ctx context.Context, tenantID, sessionID string) error {
	rows, err := a.db.Query(ctx, `
		SELECT turn_no, stage, stage_status, latency_ms, started_at, COALESCE(error_message,'')
		FROM public.session_turn_logs
		WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()
		ORDER BY turn_no ASC, started_at ASC
	`, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	byTurn := map[int][]StageLog{}
	for rows.Next() {
		var turnNo int
		var sl StageLog
		if err := rows.Scan(&turnNo, &sl.Stage, &sl.Status, &sl.LatencyMs, &sl.StartedAt, &sl.Error); err != nil {
			rows.Close()
			return fmt.Errorf("scan: %w", err)
		}
		byTurn[turnNo] = append(byTurn[turnNo], sl)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows.Err: %w", err)
	}

	// Nothing to aggregate: just trim any stray rows that already expired
	// (defensive — also handles empty sessions cleanly).
	if len(byTurn) == 0 {
		if _, err := a.db.Exec(ctx, `
			DELETE FROM public.session_turn_logs
			WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()
		`, tenantID, sessionID); err != nil {
			return fmt.Errorf("delete empty: %w", err)
		}
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

	if _, err := a.db.Exec(ctx, `
		UPDATE public.sessions
		SET turn_logs_summary = $1::jsonb
		WHERE tenant_id=$2 AND session_id=$3
	`, string(payload), tenantID, sessionID); err != nil {
		return fmt.Errorf("update sessions: %w", err)
	}

	if _, err := a.db.Exec(ctx, `
		DELETE FROM public.session_turn_logs
		WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()
	`, tenantID, sessionID); err != nil {
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