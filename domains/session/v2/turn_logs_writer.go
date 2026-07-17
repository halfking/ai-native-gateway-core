package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TurnLogsWriter writes processing stage logs to gateway.session_turn_logs
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

// WriteStage writes a single processing stage log
func (w *TurnLogsWriter) WriteStage(ctx context.Context, rec TurnLogRecord) error {
	eventDataJSON, err := json.Marshal(rec.EventData)
	if err != nil {
		return fmt.Errorf("marshal event_data: %w", err)
	}

	latencyMs := int(rec.CompletedAt.Sub(rec.StartedAt).Milliseconds())
	if latencyMs < 0 {
		latencyMs = 0
	}

	_, err = w.db.Exec(ctx, `
		INSERT INTO gateway.session_turn_logs (
			session_id, turn_no, tenant_id, request_id,
			stage, stage_status, event_data, error_message,
			started_at, completed_at, latency_ms,
			expires_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12
		)
	`,
		rec.SessionID, rec.TurnNo, rec.TenantID, rec.RequestID,
		rec.Stage, rec.StageStatus, eventDataJSON, rec.ErrorMsg,
		rec.StartedAt, rec.CompletedAt, latencyMs,
		time.Now().Add(24*time.Hour), // TTL: 24 hours
	)

	if err != nil {
		return fmt.Errorf("insert turn log: %w", err)
	}

	return nil
}

// GetStageLogs retrieves all stage logs for a specific turn
func (w *TurnLogsWriter) GetStageLogs(ctx context.Context, tenantID, sessionID string, turnNo int) ([]TurnLogRecord, error) {
	query := `
		SELECT 
			session_id, turn_no, tenant_id, request_id,
			stage, stage_status, event_data, 
			COALESCE(error_message, ''),
			started_at, completed_at, latency_ms
		FROM gateway.session_turn_logs
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
		FROM gateway.session_turn_logs
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
		"total_turns": len(turnLogs),
		"turns":       turnLogs,
		"generated_at": time.Now().Format(time.RFC3339),
	}

	return summary, nil
}

// CleanupExpiredLogs deletes logs older than 24 hours
//
// This should be called by a background worker periodically.
func (w *TurnLogsWriter) CleanupExpiredLogs(ctx context.Context) (int64, error) {
	result, err := w.db.Exec(ctx, `
		DELETE FROM gateway.session_turn_logs
		WHERE expires_at < NOW()
	`)

	if err != nil {
		return 0, fmt.Errorf("cleanup expired logs: %w", err)
	}

	return result.RowsAffected(), nil
}
