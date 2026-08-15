// Package outbox implements the durable outbox pattern for Gateway → ASM event delivery.
//
// Design:
//   - OutboxWriter writes events to outbox_events table in the same transaction as business facts
//   - OutboxDispatcher polls outbox_events and delivers to ASM via HTTP
//   - Events use fixed event_id for idempotency across retries
//   - Payload is raw JSON bytes used for HMAC-SHA256 signature
//
// Phase 2 Step 2: OutboxWriter implementation
// Refs: docs/修订0811/06-下一阶段实施计划.md Phase 2
//
//	docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §2
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// EventEnvelope is the complete event envelope for Gateway → ASM delivery.
//
// This matches the structure defined in test/events/contract/request_completed_test.go
// and docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §2.
type EventEnvelope struct {
	EventID          string         `json:"event_id"`
	EventType        string         `json:"event_type"`
	SchemaVersion    int            `json:"schema_version"`
	TenantID         string         `json:"tenant_id"`
	AggregateID      string         `json:"aggregate_id"`
	AggregateVersion int            `json:"aggregate_version"`
	OccurredAt       time.Time      `json:"occurred_at"`
	Payload          map[string]any `json:"payload"`

	// V1 envelope fields (gateway-event-schema-v1.json, GW-1.2). They are
	// rendered at the top level of the wire envelope by RenderWireEnvelope and
	// are optional on the storage struct: session_id falls back to
	// AggregateID, request_id/correlation_id fall back to the payload values
	// so pre-v1 rows keep dispatching with correlation info.
	SessionID     string `json:"session_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	SourceSystem  string `json:"source_system,omitempty"`
}

// Writer writes events to the outbox_events table.
//
// It must be called within the same transaction as the business fact updates
// to ensure at-least-once delivery semantics.
type Writer struct {
	// tx is the current database transaction. Writer does not manage transaction
	// lifecycle - the caller must commit or rollback.
	tx *sql.Tx
}

// NewWriter creates a new OutboxWriter for the given transaction.
//
// The transaction must be active and not yet committed. The caller is responsible
// for committing or rolling back the transaction.
func NewWriter(tx *sql.Tx) *Writer {
	return &Writer{tx: tx}
}

// Write writes an event to the outbox_events table.
//
// The event is inserted with status='pending' and will be picked up by the
// OutboxDispatcher for delivery. If an event with the same event_id already
// exists, this returns an error (idempotency violation).
//
// Contract requirements (02-CROSS-REPO-EVENT-CONTRACT.md §2):
//   - event_id must be globally unique and fixed across retries
//   - aggregate_version must be monotonically increasing per aggregate
//   - payload is serialized to JSON bytes for HMAC signature
//   - occurred_at defaults to NOW() if zero
func (w *Writer) Write(ctx context.Context, env EventEnvelope) error {
	if err := w.validate(&env); err != nil {
		return err
	}

	// Serialize payload to JSONB
	payloadBytes, err := json.Marshal(env.Payload)
	if err != nil {
		return fmt.Errorf("outbox.Write: marshal payload: %w", err)
	}

	if w.tx == nil {
		// A Writer without a transaction cannot persist the event. Returning
		// nil here (the previous behaviour) silently dropped durability-
		// critical writes — a caller that forgot to pass the business tx would
		// see "success" and the event would never reach ASM. Fail loudly so the
		// bug surfaces at the call site instead of as a missing delivery.
		// (Unit tests that only exercise validation should call validate
		// directly rather than rely on a nil-tx Write being a no-op.)
		return fmt.Errorf("outbox.Writer.Write: no transaction provided (event_id=%s); pass an active *sql.Tx to NewWriter", env.EventID)
	}

	query := `
		INSERT INTO outbox_events (
			event_id, event_type, schema_version, tenant_id,
			aggregate_id, aggregate_version, occurred_at, payload,
			status, attempts
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			'pending', 0
		)
	`

	_, err = w.tx.ExecContext(ctx, query,
		env.EventID,
		env.EventType,
		env.SchemaVersion,
		env.TenantID,
		env.AggregateID,
		env.AggregateVersion,
		env.OccurredAt,
		payloadBytes,
	)
	if err != nil {
		return fmt.Errorf("outbox.Write: insert event_id=%s: %w", env.EventID, err)
	}

	return nil
}

// validate checks required fields and applies defaults in-place.
//
// Defaults:
//   - SchemaVersion = 1 when 0
//   - OccurredAt = time.Now() when zero
func (w *Writer) validate(env *EventEnvelope) error {
	if env.EventID == "" {
		return fmt.Errorf("outbox.Write: event_id is required")
	}
	if env.EventType == "" {
		return fmt.Errorf("outbox.Write: event_type is required")
	}
	if env.TenantID == "" {
		return fmt.Errorf("outbox.Write: tenant_id is required")
	}
	if env.AggregateID == "" {
		return fmt.Errorf("outbox.Write: aggregate_id is required")
	}
	if env.AggregateVersion <= 0 {
		return fmt.Errorf("outbox.Write: aggregate_version must be > 0, got %d", env.AggregateVersion)
	}
	if env.SchemaVersion == 0 {
		env.SchemaVersion = 1 // default to v1
	}
	if env.OccurredAt.IsZero() {
		env.OccurredAt = time.Now()
	}
	return nil
}

// WriteBatch writes multiple events in a single database round trip.
//
// All events are inserted with status='pending'. If any event fails validation
// or insertion, the entire batch is rejected (caller must handle transaction rollback).
func (w *Writer) WriteBatch(ctx context.Context, events []EventEnvelope) error {
	if len(events) == 0 {
		return nil
	}

	// Validate all events first
	for i, env := range events {
		if env.EventID == "" {
			return fmt.Errorf("outbox.WriteBatch: event[%d]: event_id is required", i)
		}
		if env.TenantID == "" {
			return fmt.Errorf("outbox.WriteBatch: event[%d]: tenant_id is required", i)
		}
		if env.AggregateVersion <= 0 {
			return fmt.Errorf("outbox.WriteBatch: event[%d]: aggregate_version must be > 0", i)
		}
	}

	// Insert batch (PostgreSQL-specific syntax with unnest)
	// For portability, fall back to individual inserts
	for _, env := range events {
		if err := w.Write(ctx, env); err != nil {
			return fmt.Errorf("outbox.WriteBatch: %w", err)
		}
	}

	return nil
}
