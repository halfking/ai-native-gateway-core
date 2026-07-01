package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// PGDB 是 PG poll/mark 所需的最小 DB 接口。
type PGDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// NewPGPollFunc 返回基于 analysis_events 的 PollFunc。
func NewPGPollFunc(db PGDB, subscribed []analysis.EventType, defaultBatchSize int) PollFunc {
	if defaultBatchSize <= 0 {
		defaultBatchSize = 10
	}
	return func(ctx context.Context, requestedBatchSize int) ([]analysis.AnalysisEvent, error) {
		if db == nil || len(subscribed) == 0 {
			return nil, nil
		}
		batch := defaultBatchSize
		if requestedBatchSize > 0 {
			batch = requestedBatchSize
		}
		types := make([]string, 0, len(subscribed))
		for _, typ := range subscribed {
			types = append(types, string(typ))
		}
		rows, err := db.Query(ctx, `
			SELECT event_id, type, tenant_id, session_id, request_id, payload, occurred_at
			FROM analysis_events
			WHERE processed_at IS NULL
			  AND type = ANY($1)
			ORDER BY occurred_at
			LIMIT $2
		`, types, batch)
		if err != nil {
			return nil, fmt.Errorf("analysis: poll events: %w", err)
		}
		defer rows.Close()
		events := make([]analysis.AnalysisEvent, 0, batch)
		for rows.Next() {
			var evt analysis.AnalysisEvent
			var typ string
			var payloadRaw []byte
			var sessionID, requestID pgtype.Text
			if err := rows.Scan(
				&evt.EventID, &typ, &evt.TenantID,
				&sessionID, &requestID, &payloadRaw, &evt.OccurredAt,
			); err != nil {
				return nil, fmt.Errorf("analysis: scan: %w", err)
			}
			evt.Type = analysis.EventType(typ)
			if sessionID.Valid {
				evt.SessionID = sessionID.String
			}
			if requestID.Valid {
				evt.RequestID = requestID.String
			}
			if len(payloadRaw) > 0 {
				if err := json.Unmarshal(payloadRaw, &evt.Payload); err != nil {
					return nil, fmt.Errorf("analysis: decode payload: %w", err)
				}
			}
			events = append(events, evt)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("analysis: iterate events: %w", err)
		}
		return events, nil
	}
}

// NewPGMarkFunc 返回回写 analysis_events 状态的 MarkFunc。
func NewPGMarkFunc(db PGDB, logger *slog.Logger) MarkFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, eventID, workerName string, handleErr error) error {
		if db == nil || eventID == "" {
			return nil
		}
		if handleErr == nil {
			_, err := db.Exec(ctx, `
				UPDATE analysis_events
				SET processed_at = NOW(), worker = $2
				WHERE event_id = $1
			`, eventID, workerName)
			return err
		}
		_, err := db.Exec(ctx, `
			UPDATE analysis_events
			SET attempts = attempts + 1, last_error = $2, worker = $3
			WHERE event_id = $1
		`, eventID, truncateErr(handleErr.Error(), 1000), workerName)
		if err != nil {
			logger.Warn("analysis: mark failed", "event_id", eventID, "worker", workerName, "error", err)
		}
		return err
	}
}

func truncateErr(msg string, max int) string {
	if len(msg) <= max {
		return msg
	}
	return msg[:max] + "..."
}

// AsPGDB 让 *pgxpool.Pool 直接适配 PGDB。
func AsPGDB(pool *pgxpool.Pool) PGDB { return pool }
