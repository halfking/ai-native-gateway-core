// outbox.go — SR-W3 Wave 2 PendingStore projection deliverer (doc 18 §12.2).
//
// The outbox deliverer drains durable_pending_outbox and projects terminal
// task outcomes into the PendingStore (Redis projection) via ProjectCAS.
// Exactly-once effect at the PendingStore is guaranteed by the
// (task_id, result_version) unique constraint on the outbox row plus the
// ProjectCAS Lua guard (same task, monotone result_version): duplicate
// deliveries are naturally idempotent, so at-least-once claiming is safe.
//
// Fail-closed policy: any result decryption or result-hash mismatch aborts
// the projection (never writes garbage to the PendingStore) and parks the
// outbox row in failed with backoff — visible via
// durable_pending_projection_errors_total and retried until an operator
// resolves the key/corruption issue.
package durabletask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// PendingProjector is the PendingStore surface the deliverer needs. The
// Redis-backed *pending.Store satisfies it; tests substitute a fake.
type PendingProjector interface {
	ProjectCAS(ctx context.Context, r *pending.Response) (bool, error)
}

// Default deliverer pacing.
const (
	DefaultOutboxPollInterval = time.Second
	DefaultOutboxLease        = time.Minute
	DefaultOutboxBaseBackoff  = 5 * time.Second
	DefaultOutboxMaxBackoff   = 5 * time.Minute
)

// OutboxDelivererConfig sizes the deliverer loops and retry backoff.
type OutboxDelivererConfig struct {
	Owner        string
	BatchLimit   int
	Lease        time.Duration
	PollInterval time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
}

func (c OutboxDelivererConfig) withDefaults() OutboxDelivererConfig {
	if c.BatchLimit <= 0 {
		c.BatchLimit = 100
	}
	if c.Lease <= 0 {
		c.Lease = DefaultOutboxLease
	}
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultOutboxPollInterval
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = DefaultOutboxBaseBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = DefaultOutboxMaxBackoff
	}
	return c
}

// OutboxDeliverer projects terminal outbox rows into the PendingStore.
type OutboxDeliverer struct {
	store   *Store
	kr      *secret.Keyring
	pending PendingProjector
	cfg     OutboxDelivererConfig
}

// NewOutboxDeliverer builds a deliverer over the durable store and the
// PendingStore projection.
func NewOutboxDeliverer(store *Store, kr *secret.Keyring, projector PendingProjector, cfg OutboxDelivererConfig) *OutboxDeliverer {
	return &OutboxDeliverer{store: store, kr: kr, pending: projector, cfg: cfg.withDefaults()}
}

// Run polls the outbox until ctx is done.
func (d *OutboxDeliverer) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.DeliverOnce(ctx)
		}
	}
}

// DeliverOnce claims and delivers one batch of due outbox rows.
func (d *OutboxDeliverer) DeliverOnce(ctx context.Context) {
	items, err := d.store.ClaimOutbox(ctx, d.cfg.Owner, d.cfg.BatchLimit, d.cfg.Lease)
	if err != nil {
		slog.Warn("durabletask: claim outbox failed", "error", err, "owner", d.cfg.Owner)
		return
	}
	for _, item := range items {
		d.deliver(ctx, item)
	}
}

// deliver projects one claimed outbox row. Every failure path re-parks the
// row with backoff so delivery is retried; success marks it delivered.
func (d *OutboxDeliverer) deliver(ctx context.Context, item OutboxItem) {
	body, err := d.decryptResult(item)
	if err != nil {
		// Fail closed: never project an unverifiable result.
		metrics.DurablePendingProjectionErrorsTotal.WithLabelValues("decrypt").Inc()
		d.markFailed(ctx, item, "decrypt: "+err.Error())
		return
	}
	response, err := buildProjection(item, body)
	if err != nil {
		metrics.DurablePendingProjectionErrorsTotal.WithLabelValues("result_hash").Inc()
		d.markFailed(ctx, item, "result_hash: "+err.Error())
		return
	}
	applied, err := d.pending.ProjectCAS(ctx, response)
	if err != nil {
		metrics.DurablePendingProjectionErrorsTotal.WithLabelValues("project_cas").Inc()
		d.markFailed(ctx, item, "project_cas: "+err.Error())
		return
	}
	if !applied {
		// A same-or-newer projection already exists (duplicate delivery):
		// the outbox effect is achieved; mark delivered.
		slog.Debug("durabletask: projection superseded", "task_id", item.TaskID, "result_version", item.ResultVersion)
	}
	if err := d.store.MarkOutboxDelivered(ctx, item.ID, d.cfg.Owner); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			// Another deliverer took the row lease mid-flight; it owns the
			// completion. Not an error.
			slog.Warn("durabletask: outbox row lease lost before delivered-mark", "task_id", item.TaskID)
			return
		}
		metrics.DurablePendingProjectionErrorsTotal.WithLabelValues("mark_delivered").Inc()
		slog.Error("durabletask: mark outbox delivered failed", "task_id", item.TaskID, "error", err)
		return
	}
	metrics.DurablePendingProjectionsTotal.WithLabelValues(string(item.Status)).Inc()
}

// decryptResult decrypts the durable-result envelope with the task's AAD
// binding. Terminal failures carry no result ciphertext.
func (d *OutboxDeliverer) decryptResult(item OutboxItem) ([]byte, error) {
	if item.ResultCiphertext == "" {
		return nil, nil
	}
	if d.kr == nil {
		return nil, errors.New("durabletask: no keyring configured for result decryption")
	}
	plaintext, _, err := secret.DecryptWithAAD(item.ResultCiphertext, d.kr, secret.AADDomainDurableResult,
		secret.AADBinding{TenantID: item.TenantID, TaskID: item.TaskID, RequestHash: item.RequestHash})
	if err != nil {
		return nil, fmt.Errorf("durabletask: decrypt result: %w", err)
	}
	return plaintext, nil
}

// buildProjection maps a terminal outbox item onto the PendingStore
// projection shape, verifying result integrity fail-closed.
func buildProjection(item OutboxItem, body []byte) (*pending.Response, error) {
	if item.ResultHash != "" && len(body) > 0 {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != item.ResultHash {
			return nil, errors.New("durabletask: decrypted result hash mismatch")
		}
	}
	response := &pending.Response{
		SessionID:     item.SessionID,
		TenantID:      item.TenantID,
		RequestID:     item.RequestID,
		TaskID:        item.TaskID,
		RequestHash:   item.RequestHash,
		FencingToken:  item.FencingToken,
		ResultVersion: item.ResultVersion,
		ResultHash:    item.ResultHash,
		AttemptCount:  item.AttemptCount,
		Durable:       true,
		ContentType:   item.ContentType,
	}
	if item.Status == StatusCompleted {
		if len(body) == 0 {
			return nil, errors.New("durabletask: completed projection has no result body")
		}
		response.Status = pending.StatusCompleted
		response.Body = string(body)
		return response, nil
	}
	response.Status = pending.StatusFailed
	response.ErrorMessage = fmt.Sprintf("%s: %s", item.Status, item.ReasonCode)
	if len(body) > 0 {
		response.Body = string(body)
	}
	return response, nil
}

// markFailed re-parks a failed delivery with exponential backoff bounded by
// MaxBackoff (doc 18 §13.1). Losing the row lease is benign (another
// deliverer owns the retry).
func (d *OutboxDeliverer) markFailed(ctx context.Context, item OutboxItem, message string) {
	delay := d.backoff(item.AttemptCount)
	if err := d.store.MarkOutboxFailed(ctx, item.ID, d.cfg.Owner, message, time.Now().Add(delay)); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			return
		}
		slog.Error("durabletask: mark outbox failed errored", "task_id", item.TaskID, "error", err)
	}
	slog.Warn("durabletask: outbox delivery failed; backed off",
		"task_id", item.TaskID, "retry_in", delay.String(), "reason", message)
}

// backoff returns the retry delay for a delivery attempt: exponential in the
// attempt count, capped at MaxBackoff.
func (d *OutboxDeliverer) backoff(attempt int) time.Duration {
	shift := attempt
	if shift > 16 {
		shift = 16
	}
	if scaled := d.cfg.BaseBackoff << uint(shift); scaled > 0 && scaled < d.cfg.MaxBackoff {
		return scaled
	}
	return d.cfg.MaxBackoff
}
