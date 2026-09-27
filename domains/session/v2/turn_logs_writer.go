package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// TurnLogsWriter writes processing stage logs to public.session_turn_logs
//
// These logs are temporary (24h TTL) and used for debugging and diagnostics.
// When a session closes, they can be aggregated into the sessions table's
// turn_logs_summary JSON field.
type TurnLogsWriter struct {
	db *pgxpool.Pool
}

// NewTurnLogsWriter creates a new TurnLogsWriter instance
func NewTurnLogsWriter(db *pgxpool.Pool) *TurnLogsWriter {
	return &TurnLogsWriter{db: db}
}

// TurnLogRecord represents a single processing stage log entry
type TurnLogRecord struct {
	SessionID string
	TurnNo    int
	TenantID  string
	RequestID string

	Stage       string // routing | compression | injection_check | llm_call | output_check | response | cache_update
	StageStatus string // pending | running | success | failed | skipped
	EventData   map[string]interface{}
	ErrorMsg    string

	StartedAt   time.Time
	CompletedAt time.Time
}

// sessionTurnLogsTTL resolves how long a freshly written stage log should
// live, from the platform setting lifecycle.session_turn_logs_ttl_hours.
//
// 2026-09-27 (R67 session-storage audit subtask 2, handoff §4; semantics
// corrected by critical audit): this used to be a literal
// `time.Now().Add(24*time.Hour)` at the call site. That literal — not the
// 430 column DEFAULT, which this INSERT always overrides — was the only
// live TTL in the system, so making retention configurable had to happen
// here or not at all.
//
// expires_at is the single source of truth for retention: it is baked at
// write time, and migration 753's cleanup only sweeps `expires_at < NOW()`.
// Consequence worth knowing: changing the setting affects newly written
// rows; rows already in the table keep the expiry they were written with
// and will still be swept at that time.
//
// The clamp mirrors the spec entry (Min 1 / Max 168) and the SQL-side
// interlock in cleanup_session_turn_logs_by_ttl. A 0 would make every row
// immediately expired, so the floor is a data-safety requirement, not a
// style choice.
func sessionTurnLogsTTL() time.Duration {
	hours := settings.GetPlatformInt("lifecycle.session_turn_logs_ttl_hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 168 {
		hours = 168
	}
	return time.Duration(hours) * time.Hour
}

// WriteStage writes a single processing stage log.
//
// Compatibility wrapper: production callers batch a whole turn's stages
// through WriteStages (one statement, atomic visibility — see its doc
// comment). New code should prefer WriteStages.
func (w *TurnLogsWriter) WriteStage(ctx context.Context, rec TurnLogRecord) error {
	return w.WriteStages(ctx, []TurnLogRecord{rec})
}

// WriteStages writes a batch of stage rows in ONE multi-row INSERT.
//
// 2026-09-27 (12h audit round 15, D-1): the production caller used to write
// a turn's stages in a per-row loop. One statement makes the whole turn
// visible atomically, which is what the aggregator's per-turn jsonb merge
// relies on: cmd/gateway's flush merges
// `COALESCE(turn_logs_summary,'{}'::jsonb) || $1::jsonb` — a top-level-key
// replacement keyed by turn_N. If a 5-minute aggregator tick ever landed
// inside the per-row write loop, that flush saw a partial turn, deleted
// those rows, and the NEXT flush re-emitted the same turn_N key holding only
// the remaining stages — silently dropping the earlier stages from the
// summary. The window was only as wide as the write loop (milliseconds per
// turn), but the batched form also collapses N round trips into 1.
//
// All rows share ONE expires_at (a single Now()+TTL): a turn's rows are
// produced together right after the turn transaction commits, so they expire
// together too — the aggregator's candidate query never observes a turn's
// rows half-expired.
func (w *TurnLogsWriter) WriteStages(ctx context.Context, recs []TurnLogRecord) error {
	if len(recs) == 0 {
		return nil
	}
	query, args, err := buildTurnLogsInsert(recs, time.Now().Add(sessionTurnLogsTTL()))
	if err != nil {
		return err
	}
	if _, err := w.db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("insert turn logs: %w", err)
	}
	return nil
}

// turnLogsInsertColumns matches the column list below; tuples reference
// these positions, so the two must move together.
const turnLogsInsertColumns = 12

// buildTurnLogsInsert renders one multi-row INSERT for the given records
// with a shared expiry. Extracted so the shape is assertable without a
// database (pgxmock cannot observe statement count; the real-DB tests
// cannot see the shape at all): single statement, one tuple per row,
// turnLogsInsertColumns params per tuple, one shared expires_at, and
// event_data bound as string with an explicit ::text::jsonb cast.
func buildTurnLogsInsert(recs []TurnLogRecord, expiresAt time.Time) (string, []interface{}, error) {
	tuples := make([]string, 0, len(recs))
	args := make([]interface{}, 0, len(recs)*turnLogsInsertColumns)
	for i, rec := range recs {
		eventDataJSON, err := json.Marshal(rec.EventData)
		if err != nil {
			return "", nil, fmt.Errorf("marshal event_data (stage %s): %w", rec.Stage, err)
		}

		latencyMs := int(rec.CompletedAt.Sub(rec.StartedAt).Milliseconds())
		if latencyMs < 0 {
			latencyMs = 0
		}

		b := i * turnLogsInsertColumns
		// string() + $7::text::jsonb (not []byte): the pool forces pgx
		// SimpleProtocol, which binds []byte as bytea hex and any jsonb cast
		// then fails with 22P02 (doc §3.2; internal/dbx/jsonb.go).
		tuples = append(tuples, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d, $%d::text::jsonb, $%d, $%d, $%d, $%d, $%d)",
			b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8, b+9, b+10, b+11, b+12))
		args = append(args,
			rec.SessionID, rec.TurnNo, rec.TenantID, rec.RequestID,
			rec.Stage, rec.StageStatus, string(eventDataJSON), rec.ErrorMsg,
			rec.StartedAt, rec.CompletedAt, latencyMs,
			expiresAt,
		)
	}
	query := `
		INSERT INTO public.session_turn_logs (
			session_id, turn_no, tenant_id, request_id,
			stage, stage_status, event_data, error_message,
			started_at, completed_at, latency_ms,
			expires_at
		) VALUES ` + strings.Join(tuples, ", ")
	return query, args, nil
}

// GetStageLogs retrieves all stage logs for a specific turn
func (w *TurnLogsWriter) GetStageLogs(ctx context.Context, tenantID, sessionID string, turnNo int) ([]TurnLogRecord, error) {
	query := `
		SELECT 
			session_id, turn_no, tenant_id, request_id,
			stage, stage_status, event_data, 
			COALESCE(error_message, ''),
			started_at, completed_at, latency_ms
		FROM public.session_turn_logs
		WHERE tenant_id = $1 AND session_id = $2 AND turn_no = $3
		ORDER BY started_at ASC
	`

	rows, err := w.db.Query(ctx, query, tenantID, sessionID, turnNo)
	if err != nil {
		return nil, fmt.Errorf("query stage logs: %w", err)
	}
	defer rows.Close()

	var logs []TurnLogRecord
	for rows.Next() {
		var rec TurnLogRecord
		var eventDataJSON []byte
		var latencyMs int

		err := rows.Scan(
			&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID,
			&rec.Stage, &rec.StageStatus, &eventDataJSON,
			&rec.ErrorMsg,
			&rec.StartedAt, &rec.CompletedAt, &latencyMs,
		)
		if err != nil {
			return nil, fmt.Errorf("scan stage log: %w", err)
		}

		// Parse event data
		if len(eventDataJSON) > 0 {
			json.Unmarshal(eventDataJSON, &rec.EventData)
		}

		logs = append(logs, rec)
	}

	return logs, rows.Err()
}

// GetAllSessionLogs retrieves all logs for a session (all turns)
func (w *TurnLogsWriter) GetAllSessionLogs(ctx context.Context, tenantID, sessionID string) ([]TurnLogRecord, error) {
	query := `
		SELECT 
			session_id, turn_no, tenant_id, request_id,
			stage, stage_status, event_data,
			COALESCE(error_message, ''),
			started_at, completed_at, latency_ms
		FROM public.session_turn_logs
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no ASC, started_at ASC
	`

	rows, err := w.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session logs: %w", err)
	}
	defer rows.Close()

	var logs []TurnLogRecord
	for rows.Next() {
		var rec TurnLogRecord
		var eventDataJSON []byte
		var latencyMs int

		err := rows.Scan(
			&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID,
			&rec.Stage, &rec.StageStatus, &eventDataJSON,
			&rec.ErrorMsg,
			&rec.StartedAt, &rec.CompletedAt, &latencyMs,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session log: %w", err)
		}

		// Parse event data
		if len(eventDataJSON) > 0 {
			json.Unmarshal(eventDataJSON, &rec.EventData)
		}

		logs = append(logs, rec)
	}

	return logs, rows.Err()
}

// AggregateSessionLogs aggregates all turn logs for a session into a summary JSON
//
// This is called when a session closes, to persist the logs into the
// sessions.turn_logs_summary field before they expire (24h TTL).
func (w *TurnLogsWriter) AggregateSessionLogs(ctx context.Context, tenantID, sessionID string) (map[string]interface{}, error) {
	logs, err := w.GetAllSessionLogs(ctx, tenantID, sessionID)
	if err != nil {
		return nil, err
	}

	// Group by turn
	turnLogs := make(map[int][]map[string]interface{})
	for _, log := range logs {
		turnLog := map[string]interface{}{
			"stage":        log.Stage,
			"status":       log.StageStatus,
			"started_at":   log.StartedAt.Format(time.RFC3339),
			"completed_at": log.CompletedAt.Format(time.RFC3339),
			"latency_ms":   int(log.CompletedAt.Sub(log.StartedAt).Milliseconds()),
		}

		if log.ErrorMsg != "" {
			turnLog["error"] = log.ErrorMsg
		}

		if len(log.EventData) > 0 {
			turnLog["data"] = log.EventData
		}

		turnLogs[log.TurnNo] = append(turnLogs[log.TurnNo], turnLog)
	}

	summary := map[string]interface{}{
		"total_turns":  len(turnLogs),
		"turns":        turnLogs,
		"generated_at": time.Now().Format(time.RFC3339),
	}

	return summary, nil
}

// CleanupExpiredLogs deletes logs older than 24 hours
//
// This should be called by a background worker periodically.
//
// Deprecated (2026-09-27, critical audit of Subtask 2): nothing calls this
// — the only reference in the tree is its own test, so it has never run in
// production. The predicate here was in fact the correct one
// (`expires_at < NOW()`), which is what made the missing caller worth
// naming: this dead method plus the dead SQL cleanup_expired_session_turn_logs()
// (migration 430:336) are why session_turn_logs grew unbounded.
//
// The live sweep is now bg.PartitionManager.cleanupSessionTurnLogsByTTL →
// cleanup_session_turn_logs_by_ttl (migration 753), which uses the same
// predicate. Kept (not deleted) because it is an exported method: an
// out-of-tree caller may exist, and removal is a wider blast radius than
// this subtask should take. Scheduled for removal — see handoff §15/§16.
func (w *TurnLogsWriter) CleanupExpiredLogs(ctx context.Context) (int64, error) {
	result, err := w.db.Exec(ctx, `
		DELETE FROM public.session_turn_logs
		WHERE expires_at < NOW()
	`)

	if err != nil {
		return 0, fmt.Errorf("cleanup expired logs: %w", err)
	}

	return result.RowsAffected(), nil
}
