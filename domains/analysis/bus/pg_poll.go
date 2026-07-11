package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

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
	claimant := "analysis-" + uuid.NewString()
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
			WITH claimable AS (
				SELECT id
				FROM analysis_events
				WHERE processed_at IS NULL
				  AND type = ANY($1)
				  AND attempts < 5
				  AND (claimed_at IS NULL OR claimed_at < NOW() - INTERVAL '10 minutes')
				ORDER BY occurred_at
				FOR UPDATE SKIP LOCKED
				LIMIT $2
			)
			UPDATE analysis_events AS event
			SET claimed_at = NOW(), claimed_by = $3
			FROM claimable
			WHERE event.id = claimable.id
			RETURNING event.event_id, event.type, event.tenant_id, event.session_id,
			          event.request_id, event.payload, event.occurred_at
		`, types, batch, claimant)
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
			evt.ClaimID = claimant
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
	return func(ctx context.Context, eventID, claimID, workerName string, handleErr error) error {
		if db == nil || eventID == "" {
			return nil
		}
		if handleErr == nil {
			_, err := db.Exec(ctx, `
				UPDATE analysis_events
				SET processed_at = NOW(), worker = $2, claimed_at = NULL, claimed_by = NULL
				WHERE event_id = $1 AND ($3 = '' OR claimed_by = $3)
			`, eventID, workerName, claimID)
			return err
		}
		_, err := db.Exec(ctx, `
			UPDATE analysis_events
			SET attempts = attempts + 1,
			    last_error = $2,
			    worker = $3,
			    processed_at = CASE WHEN attempts + 1 >= 5 THEN NOW() ELSE processed_at END,
			    claimed_at = NULL,
			    claimed_by = NULL
			WHERE event_id = $1 AND ($4 = '' OR claimed_by = $4)
		`, eventID, truncateErr(handleErr.Error(), 1000), workerName, claimID)
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
