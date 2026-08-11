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
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// dispatchBatch fetches pending/failed events and dispatches them.
func (d *Dispatcher) dispatchBatch(ctx context.Context) error {
	// Fetch pending events (status=pending OR (status=failed AND next_retry_at <= NOW()))
	query := `
		SELECT id, event_id, event_type, schema_version, tenant_id,
		       aggregate_id, aggregate_version, occurred_at, payload,
		       status, attempts, last_error, next_retry_at
		FROM outbox_events
		WHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))
		ORDER BY occurred_at ASC
		LIMIT 100
		FOR UPDATE SKIP LOCKED
	`

	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("query outbox_events: %w", err)
	}
	defer rows.Close()

	dispatched := 0
	failed := 0

	for rows.Next() {
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

		if err := rows.Scan(
			&id, &eventID, &eventType, &schemaVersion, &tenantID,
			&aggregateID, &aggregateVersion, &occurredAt, &payloadBytes,
			&status, &attempts, &lastError, &nextRetryAt,
		); err != nil {
			d.logger.Error("scan row failed", "error", err)
			continue
		}

		// Reconstruct EventEnvelope
		env := EventEnvelope{
			EventID:          eventID,
			EventType:        eventType,
			SchemaVersion:    schemaVersion,
			TenantID:         tenantID,
			AggregateID:      aggregateID,
			AggregateVersion: aggregateVersion,
			OccurredAt:       occurredAt,
		}

		// Deserialize payload
		if err := json.Unmarshal(payloadBytes, &env.Payload); err != nil {
			d.logger.Error("unmarshal payload failed", "event_id", eventID, "error", err)
			d.markFailed(ctx, id, attempts+1, fmt.Sprintf("unmarshal error: %v", err))
			failed++
			continue
		}

		// Dispatch
		if err := d.dispatch(ctx, env, payloadBytes); err != nil {
			d.logger.Warn("dispatch failed", "event_id", eventID, "attempts", attempts+1, "error", err)
			d.markFailed(ctx, id, attempts+1, err.Error())
			failed++
		} else {
			d.logger.Info("dispatch succeeded", "event_id", eventID, "attempts", attempts+1)
			d.markSent(ctx, id)
			dispatched++
		}
	}

	if dispatched > 0 || failed > 0 {
		d.logger.Info("dispatch batch complete", "dispatched", dispatched, "failed", failed)
	}

	return rows.Err()
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

// markSent updates the event to status='sent'.
func (d *Dispatcher) markSent(ctx context.Context, id int64) {
	query := `
		UPDATE outbox_events
		SET status = 'sent', updated_at = NOW()
		WHERE id = $1
	`
	if _, err := d.db.ExecContext(ctx, query, id); err != nil {
		d.logger.Error("mark sent failed", "id", id, "error", err)
	}
}

// markFailed updates the event status to 'failed' or 'dlq'.
//
// Exponential backoff:
//   - Attempt 1: retry after 1s
//   - Attempt 2: retry after 2s
//   - Attempt 3: retry after 4s
//   - Attempt 4: retry after 8s
//   - Attempt 5+: move to DLQ
func (d *Dispatcher) markFailed(ctx context.Context, id int64, newAttempts int, errMsg string) {
	if newAttempts >= d.maxAttempts {
		// Move to DLQ
		query := `
			UPDATE outbox_events
			SET status = 'dlq', attempts = $1, last_error = $2, updated_at = NOW()
			WHERE id = $3
		`
		if _, err := d.db.ExecContext(ctx, query, newAttempts, errMsg, id); err != nil {
			d.logger.Error("move to dlq failed", "id", id, "error", err)
		}
	} else {
		// Exponential backoff: 2^(attempts-1) seconds
		backoff := time.Duration(1<<uint(newAttempts-1)) * time.Second
		nextRetry := time.Now().Add(backoff)

		query := `
			UPDATE outbox_events
			SET status = 'failed', attempts = $1, last_error = $2, next_retry_at = $3, updated_at = NOW()
			WHERE id = $4
		`
		if _, err := d.db.ExecContext(ctx, query, newAttempts, errMsg, nextRetry, id); err != nil {
			d.logger.Error("mark failed failed", "id", id, "error", err)
		}
	}
}
