// Package outbox implements the durable outbox pattern for Gateway → ASM event delivery.
//
// Phase 2 Step 4: OutboxDispatcher implementation
// Refs: docs/修订0811/06-下一阶段实施计划.md Phase 2
//
//	docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §4
package outbox

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Dispatcher polls outbox_events and delivers them to ASM via HTTP.
//
// Responsibilities:
//   - Poll outbox_events WHERE status IN ('pending','failed') AND next_retry_at <= NOW()
//   - Sign payload with HMAC-SHA256
//   - POST to ASM /internal/v1/events
//   - Exponential backoff retry (1s → 2s → 4s → 8s ... max 5 attempts)
//   - Move to DLQ after max_attempts
type Dispatcher struct {
	db           *sql.DB
	asmEndpoint  string        // ASM internal endpoint (e.g., http://asm:8080/internal/v1/events)
	hmacSecret   string        // Shared HMAC secret for signing
	pollInterval time.Duration // How often to poll (default: 5s)
	maxAttempts  int           // Max retry attempts before DLQ (default: 5)
	httpClient   *http.Client  // Per-dispatcher client with a bounded timeout
	logger       *slog.Logger
}

// DispatcherConfig holds configuration for Dispatcher.
type DispatcherConfig struct {
	DB           *sql.DB
	ASMEndpoint  string
	HMACSecret   string
	PollInterval time.Duration
	MaxAttempts  int
	// HTTPTimeout bounds each ASM POST. Defaults to 10s. A dedicated client
	// (rather than http.DefaultClient, which has no timeout) prevents a single
	// stalled ASM connection from blocking the serial dispatch loop forever.
	HTTPTimeout time.Duration
	Logger      *slog.Logger
}

// NewDispatcher creates a new OutboxDispatcher.
//
// Defaults:
//   - PollInterval: 5s
//   - MaxAttempts: 5
//   - HTTPTimeout: 10s
//   - Logger: slog.Default()
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Dispatcher{
		db:           cfg.DB,
		asmEndpoint:  cfg.ASMEndpoint,
		hmacSecret:   cfg.HMACSecret,
		pollInterval: cfg.PollInterval,
		maxAttempts:  cfg.MaxAttempts,
		httpClient:   &http.Client{Timeout: cfg.HTTPTimeout},
		logger:       cfg.Logger,
	}
}

// Start begins the dispatcher loop. Blocks until ctx is cancelled.
//
// Usage:
//
//	dispatcher := NewDispatcher(cfg)
//	go dispatcher.Start(ctx)
func (d *Dispatcher) Start(ctx context.Context) error {
	d.logger.Info("outbox.Dispatcher started",
		"poll_interval", d.pollInterval,
		"max_attempts", d.maxAttempts,
		"asm_endpoint", d.asmEndpoint,
	)

	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("outbox.Dispatcher stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := d.dispatchBatch(ctx); err != nil {
				d.logger.Error("dispatch batch failed", "error", err)
				// Continue polling despite errors
			}
		}
	}
}

// dispatchBatch claims pending/failed events and delivers them one at a
// time, each inside its own short transaction.
//
// Why per-event transactions: SELECT ... FOR UPDATE SKIP LOCKED only
// serialises concurrent workers while the row lock is held, i.e. for the
// duration of an open transaction. The previous implementation ran the
// claim SELECT and every markSent/markFailed UPDATE in autocommit, so the
// lock was released the moment the SELECT statement finished — two
// dispatcher instances could both claim and re-deliver the same event.
// Wrapping claim + delivery-result + mark in one tx per event makes
// SKIP LOCKED actually skip rows that a peer is currently delivering.
//
// Crash safety is unchanged (at-least-once): if the process dies after a
// successful HTTP POST but before COMMIT, the tx rolls back, the event
// stays pending, and the next poll redelivers it. ASM dedups by the fixed
// event_id, so a duplicate delivery is absorbed by the consumer.
func (d *Dispatcher) dispatchBatch(ctx context.Context) error {
	dispatched := 0
	failed := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		outcome, err := d.dispatchOne(ctx)
		if err != nil {
			// Structural error (DB unavailable, commit failed). Stop the
			// batch and let the next poll retry; per-event delivery errors
			// are classified inside dispatchOne and never reach here.
			if dispatched > 0 || failed > 0 {
				d.logger.Info("dispatch batch interrupted",
					"dispatched", dispatched, "failed", failed, "error", err)
			}
			d.updateGaugeMetrics(ctx)
			return err
		}
		switch outcome {
		case dispatchOutcomeSent:
			dispatched++
		case dispatchOutcomeFailed:
			failed++
		case dispatchOutcomeNone:
			if dispatched > 0 || failed > 0 {
				d.logger.Info("dispatch batch complete",
					"dispatched", dispatched, "failed", failed)
			}
			d.updateGaugeMetrics(ctx)
			return nil
		}
	}
}

// dispatchOutcome is the result of attempting one event.
type dispatchOutcome int

const (
	dispatchOutcomeNone   dispatchOutcome = iota // no claimable event remaining
	dispatchOutcomeSent                          // delivered + marked sent
	dispatchOutcomeFailed                        // delivery failed + marked failed/dlq
)

// execer is satisfied by both *sql.DB and *sql.Tx, letting markSent/markFailed
// run on whichever the caller holds.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// dispatchOne claims a single pending/failed event within a transaction,
// delivers it, and commits the resulting status. The row is locked for the
// duration of the HTTP delivery so concurrent dispatchers skip it. Returns
// dispatchOutcomeNone when no event is claimable.
func (d *Dispatcher) dispatchOne(ctx context.Context) (dispatchOutcome, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return dispatchOutcomeNone, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback() // no-op after Commit
	}()

	// Claim one event. LIMIT 1 + FOR UPDATE SKIP LOCKED inside the tx makes
	// the lock effective for the whole delivery window.
	const claimQuery = `
		SELECT id, event_id, event_type, schema_version, tenant_id,
		       aggregate_id, aggregate_version, occurred_at, payload,
		       status, attempts, last_error, next_retry_at
		FROM outbox_events
		WHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))
		ORDER BY occurred_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`
	var (
		id                              int64
		eventID, eventType, tenantID    string
		aggregateID                     string
		schemaVersion, aggregateVersion int
		occurredAt                      time.Time
		payloadBytes                    []byte
		status                          string
		attempts                        int
		lastError                       sql.NullString
		nextRetryAt                     sql.NullTime
	)
	err = tx.QueryRowContext(ctx, claimQuery).Scan(
		&id, &eventID, &eventType, &schemaVersion, &tenantID,
		&aggregateID, &aggregateVersion, &occurredAt, &payloadBytes,
		&status, &attempts, &lastError, &nextRetryAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return dispatchOutcomeNone, nil
	}
	if err != nil {
		return dispatchOutcomeNone, fmt.Errorf("claim event: %w", err)
	}

	env := EventEnvelope{
		EventID:          eventID,
		EventType:        eventType,
		SchemaVersion:    schemaVersion,
		TenantID:         tenantID,
		AggregateID:      aggregateID,
		AggregateVersion: aggregateVersion,
		OccurredAt:       occurredAt,
	}

	if err := json.Unmarshal(payloadBytes, &env.Payload); err != nil {
		// Poison-pill event (corrupt payload). markFailed will move it
		// straight to DLQ after max_attempts, so count both "failed" and
		// the retry attempt — but skip the duration histogram since the
		// failure never reached ASM.
		d.logger.Error("unmarshal payload failed", "event_id", eventID, "error", err)
		RecordEventFailed("validation")
		d.markFailed(ctx, tx, id, attempts+1, fmt.Sprintf("unmarshal error: %v", err))
		if err := tx.Commit(); err != nil {
			return dispatchOutcomeNone, fmt.Errorf("commit (unmarshal-fail mark): %w", err)
		}
		if attempts+1 < d.maxAttempts {
			RecordEventRetried()
		}
		return dispatchOutcomeFailed, nil
	}

	// Time only the HTTP roundtrip; the rest of dispatchOne is in-process
	// overhead that the dispatcher loop batches.
	start := time.Now()
	err = d.dispatch(ctx, env, payloadBytes)
	duration := time.Since(start).Seconds()
	ObserveDeliveryDuration(duration, err == nil)

	if err != nil {
		d.logger.Warn("dispatch failed", "event_id", eventID, "attempts", attempts+1, "error", err)
		RecordEventFailed(classifyError(err))
		d.markFailed(ctx, tx, id, attempts+1, err.Error())
		if err := tx.Commit(); err != nil {
			return dispatchOutcomeNone, fmt.Errorf("commit (dispatch-fail mark): %w", err)
		}
		// Retry if markFailed scheduled a future next_retry_at; skip when
		// the event has hit max_attempts and was moved to DLQ.
		if attempts+1 < d.maxAttempts {
			RecordEventRetried()
		}
		return dispatchOutcomeFailed, nil
	}

	d.logger.Info("dispatch succeeded", "event_id", eventID, "attempts", attempts+1)
	RecordEventSent()
	d.markSent(ctx, tx, id)
	if err := tx.Commit(); err != nil {
		return dispatchOutcomeNone, fmt.Errorf("commit (sent mark): %w", err)
	}
	return dispatchOutcomeSent, nil
}

// dispatch sends a single event to ASM via HTTP POST.
func (d *Dispatcher) dispatch(ctx context.Context, env EventEnvelope, payloadBytes []byte) error {
	// Sign the complete envelope (reconstructed JSON)
	envelopeJSON, err := json.Marshal(struct {
		EventID          string         `json:"event_id"`
		EventType        string         `json:"event_type"`
		SchemaVersion    int            `json:"schema_version"`
		TenantID         string         `json:"tenant_id"`
		AggregateID      string         `json:"aggregate_id"`
		AggregateVersion int            `json:"aggregate_version"`
		OccurredAt       time.Time      `json:"occurred_at"`
		Payload          map[string]any `json:"payload"`
	}{
		EventID:          env.EventID,
		EventType:        env.EventType,
		SchemaVersion:    env.SchemaVersion,
		TenantID:         env.TenantID,
		AggregateID:      env.AggregateID,
		AggregateVersion: env.AggregateVersion,
		OccurredAt:       env.OccurredAt,
		Payload:          env.Payload,
	})
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	// Compute HMAC-SHA256 signature
	signature := computeHMAC(envelopeJSON, d.hmacSecret)

	// Build HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.asmEndpoint, bytes.NewReader(envelopeJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", env.TenantID)
	req.Header.Set("X-Event-Signature", signature)

	// Send (dedicated client with a bounded timeout — see NewDispatcher).
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("asm returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// markSent updates the event to status='sent'. ex is the tx that holds the
// claim lock (or d.db for non-transactional callers).
func (d *Dispatcher) markSent(ctx context.Context, ex execer, id int64) {
	const query = `
		UPDATE outbox_events
		SET status = 'sent', updated_at = NOW()
		WHERE id = $1
	`
	if _, err := ex.ExecContext(ctx, query, id); err != nil {
		d.logger.Error("mark sent failed", "id", id, "error", err)
	}
}

// markFailed updates the event status to 'failed' or 'dlq'. ex is the tx
// that holds the claim lock (or d.db for non-transactional callers).
//
// Exponential backoff:
//   - Attempt 1: retry after 1s
//   - Attempt 2: retry after 2s
//   - Attempt 3: retry after 4s
//   - Attempt 4: retry after 8s
//   - Attempt 5+: move to DLQ
func (d *Dispatcher) markFailed(ctx context.Context, ex execer, id int64, newAttempts int, errMsg string) {
	if newAttempts >= d.maxAttempts {
		// Move to DLQ
		const query = `
			UPDATE outbox_events
			SET status = 'dlq', attempts = $1, last_error = $2, updated_at = NOW()
			WHERE id = $3
		`
		if _, err := ex.ExecContext(ctx, query, newAttempts, errMsg, id); err != nil {
			d.logger.Error("move to dlq failed", "id", id, "error", err)
		}
		return
	}
	// Exponential backoff: 2^(attempts-1) seconds
	backoff := time.Duration(1<<uint(newAttempts-1)) * time.Second
	nextRetry := time.Now().Add(backoff)
	const query = `
		UPDATE outbox_events
		SET status = 'failed', attempts = $1, last_error = $2, next_retry_at = $3, updated_at = NOW()
		WHERE id = $4
	`
	if _, err := ex.ExecContext(ctx, query, newAttempts, errMsg, nextRetry, id); err != nil {
		d.logger.Error("mark failed failed", "id", id, "error", err)
	}
}

// classifyError categorizes delivery errors into metric labels.
// Helps diagnose failure modes: network issues, HTTP errors, validation, etc.
func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}

	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "context deadline exceeded"), strings.Contains(errStr, "timeout"):
		return "timeout"
	case strings.Contains(errStr, "connection refused"), strings.Contains(errStr, "no such host"):
		return "network"
	case strings.Contains(errStr, "401"), strings.Contains(errStr, "signature"):
		return "hmac_mismatch"
	case strings.Contains(errStr, "403"), strings.Contains(errStr, "tenant"):
		return "tenant_mismatch"
	case strings.Contains(errStr, "422"), strings.Contains(errStr, "validation"):
		return "validation"
	case strings.Contains(errStr, "409"), strings.Contains(errStr, "duplicate"):
		return "duplicate"
	case strings.Contains(errStr, "5"), strings.Contains(errStr, "Internal Server Error"):
		return "http_5xx"
	case strings.Contains(errStr, "4"):
		return "http_4xx"
	default:
		return "http_error"
	}
}

// updateGaugeMetrics queries current pending and DLQ counts and updates Prometheus gauges.
// Should be called periodically (e.g., after each poll cycle) for accurate monitoring.
func (d *Dispatcher) updateGaugeMetrics(ctx context.Context) {
	var pendingCount, dlqCount int

	// Count pending events
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE status = 'pending'`).Scan(&pendingCount); err != nil {
		d.logger.Warn("query pending count failed", "error", err)
	} else {
		SetPendingCount(pendingCount)
	}

	// Count DLQ events
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE status = 'dlq'`).Scan(&dlqCount); err != nil {
		d.logger.Warn("query dlq count failed", "error", err)
	} else {
		SetDLQCount(dlqCount)
	}
}
