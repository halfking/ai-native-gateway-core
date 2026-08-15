package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrReplaySelectorRequired = errors.New("outbox replay: at least one selector is required")

const replaySelectionQuery = `
	SELECT id, event_id
	FROM outbox_events
	WHERE status = 'dlq'
	  AND ($1 = '' OR event_id = $1)
	  AND ($2 = '' OR tenant_id = $2)
	  AND ($3::timestamptz IS NULL OR occurred_at >= $3)
	  AND ($4::timestamptz IS NULL OR occurred_at < $4)
	ORDER BY occurred_at ASC, event_id ASC
	LIMIT $5
`

const replayClaimQuery = `
	SELECT id, event_id, event_type, schema_version, tenant_id,
	       aggregate_id, aggregate_version, occurred_at, payload
	FROM outbox_events
	WHERE id = $1 AND status = 'dlq'
	FOR UPDATE
`

const replayMarkSentQuery = `
	UPDATE outbox_events
	SET status = 'sent', last_attempt_at = NOW(), last_error = NULL,
	    next_retry_at = NULL, updated_at = NOW()
	WHERE id = $1 AND status = 'dlq'
`

const replayMarkFailedQuery = `
	UPDATE outbox_events
	SET last_attempt_at = NOW(), last_error = $1, updated_at = NOW()
	WHERE id = $2 AND status = 'dlq'
`

// EventDeliverer sends one outbox event without changing persistence state.
type EventDeliverer interface {
	Deliver(ctx context.Context, env EventEnvelope) error
}

// ReplayOptions filters the DLQ replay set. At least one selector is required.
type ReplayOptions struct {
	EventID  string
	TenantID string
	From     *time.Time
	To       *time.Time
	Limit    int
	DryRun   bool
}

// ReplayResult summarizes one replay invocation.
type ReplayResult struct {
	Selected int      `json:"selected"`
	Replayed int      `json:"replayed"`
	Failed   int      `json:"failed"`
	Skipped  int      `json:"skipped"`
	EventIDs []string `json:"event_ids"`
}

// Replayer selects and redelivers DLQ events while preserving event_id.
type Replayer struct {
	db        *sql.DB
	deliverer EventDeliverer
}

func NewReplayer(db *sql.DB, deliverer EventDeliverer) *Replayer {
	return &Replayer{db: db, deliverer: deliverer}
}

func (r *Replayer) Replay(ctx context.Context, options ReplayOptions) (ReplayResult, error) {
	if options.EventID == "" && options.TenantID == "" && options.From == nil && options.To == nil {
		return ReplayResult{}, ErrReplaySelectorRequired
	}
	if options.Limit <= 0 {
		options.Limit = 100
	}
	if options.Limit > 1000 {
		return ReplayResult{}, fmt.Errorf("outbox replay: limit %d exceeds maximum 1000", options.Limit)
	}
	if options.From != nil && options.To != nil && !options.From.Before(*options.To) {
		return ReplayResult{}, errors.New("outbox replay: from must be before to")
	}

	ids, eventIDs, err := r.selectDLQ(ctx, options)
	if err != nil {
		return ReplayResult{}, err
	}
	result := ReplayResult{Selected: len(ids), EventIDs: eventIDs}
	if options.DryRun {
		return result, nil
	}
	for _, id := range ids {
		outcome, err := r.replayOne(ctx, id)
		if err != nil {
			return result, err
		}
		switch outcome {
		case replayOutcomeSent:
			result.Replayed++
		case replayOutcomeFailed:
			result.Failed++
		case replayOutcomeSkipped:
			result.Skipped++
		}
	}
	return result, nil
}

func (r *Replayer) selectDLQ(ctx context.Context, options ReplayOptions) ([]int64, []string, error) {
	rows, err := r.db.QueryContext(ctx, replaySelectionQuery,
		options.EventID, options.TenantID, nullableTime(options.From), nullableTime(options.To), options.Limit)
	if err != nil {
		return nil, nil, fmt.Errorf("outbox replay: select dlq: %w", err)
	}
	defer rows.Close()

	var ids []int64
	var eventIDs []string
	for rows.Next() {
		var id int64
		var eventID string
		if err := rows.Scan(&id, &eventID); err != nil {
			return nil, nil, fmt.Errorf("outbox replay: scan selection: %w", err)
		}
		ids = append(ids, id)
		eventIDs = append(eventIDs, eventID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("outbox replay: selection rows: %w", err)
	}
	return ids, eventIDs, nil
}

type replayOutcome int

const (
	replayOutcomeSkipped replayOutcome = iota
	replayOutcomeSent
	replayOutcomeFailed
)

func (r *Replayer) replayOne(ctx context.Context, id int64) (replayOutcome, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return replayOutcomeSkipped, fmt.Errorf("outbox replay: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var env EventEnvelope
	var payload []byte
	err = tx.QueryRowContext(ctx, replayClaimQuery, id).Scan(
		&id, &env.EventID, &env.EventType, &env.SchemaVersion, &env.TenantID,
		&env.AggregateID, &env.AggregateVersion, &env.OccurredAt, &payload,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return replayOutcomeSkipped, nil
	}
	if err != nil {
		return replayOutcomeSkipped, fmt.Errorf("outbox replay: claim id=%d: %w", id, err)
	}
	if err := json.Unmarshal(payload, &env.Payload); err != nil {
		if _, updateErr := tx.ExecContext(ctx, replayMarkFailedQuery, "unmarshal payload: "+err.Error(), id); updateErr != nil {
			return replayOutcomeSkipped, fmt.Errorf("outbox replay: record invalid payload id=%d: %w", id, updateErr)
		}
		if err := tx.Commit(); err != nil {
			return replayOutcomeSkipped, fmt.Errorf("outbox replay: commit invalid payload id=%d: %w", id, err)
		}
		return replayOutcomeFailed, nil
	}
	if err := r.deliverer.Deliver(ctx, env); err != nil {
		if _, updateErr := tx.ExecContext(ctx, replayMarkFailedQuery, err.Error(), id); updateErr != nil {
			return replayOutcomeSkipped, fmt.Errorf("outbox replay: record delivery error id=%d: %w", id, updateErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return replayOutcomeSkipped, fmt.Errorf("outbox replay: commit failure id=%d: %w", id, commitErr)
		}
		return replayOutcomeFailed, nil
	}
	if _, err := tx.ExecContext(ctx, replayMarkSentQuery, id); err != nil {
		return replayOutcomeSkipped, fmt.Errorf("outbox replay: mark sent id=%d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return replayOutcomeSkipped, fmt.Errorf("outbox replay: commit sent id=%d: %w", id, err)
	}
	return replayOutcomeSent, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}
