package stats

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// maxPersistRetries bounds how many consecutive flush attempts a single batch
// survives before being counted as dead-letter. The queue is bounded, so a
// persistent DB failure must not silently lose telemetry, but it also must
// not stall the writer indefinitely.
const maxPersistRetries = 5

type EventWriter struct {
	db            *pgxpool.Pool
	queue         chan Event
	cancel        context.CancelFunc
	done          chan struct{}
	stopOnce      sync.Once
	dropped       atomic.Uint64 // queue full → event never enqueued
	persistFailed atomic.Uint64 // individual flush attempts that returned err
	deadLettered  atomic.Uint64 // batches dropped after maxPersistRetries
}

func NewEventWriter(db *pgxpool.Pool, queueSize int) *EventWriter {
	if queueSize < 1 {
		queueSize = 4096
	}
	return &EventWriter{db: db, queue: make(chan Event, queueSize), done: make(chan struct{})}
}

// Stats exposes internal counters for tests and future metrics export.
func (w *EventWriter) Stats() (dropped, persistFailed, deadLettered uint64) {
	if w == nil {
		return 0, 0, 0
	}
	return w.dropped.Load(), w.persistFailed.Load(), w.deadLettered.Load()
}

func (w *EventWriter) Start(ctx context.Context) {
	if w == nil || w.db == nil || w.cancel != nil {
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(cctx)
}

func (w *EventWriter) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if w.cancel != nil {
		<-w.done
	}
}

func (w *EventWriter) Record(entry *telemetry.RequestLogEntry) {
	e, ok := EventFromTelemetry(entry, time.Now().UTC())
	if !ok || w == nil {
		return
	}
	select {
	case w.queue <- e:
	default:
		w.dropped.Add(1)
		slog.Warn("stats event queue full", "request_id", e.RequestID, "event_type", e.EventType, "dropped", w.dropped.Load())
	}
}

func (w *EventWriter) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]Event, 0, 64)
	retries := 0
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := w.persist(flushCtx, batch)
		cancel()
		if err != nil {
			w.persistFailed.Add(1)
			retries++
			slog.Warn("stats event persist failed",
				"error", err,
				"events", len(batch),
				"retry", retries,
				"max_retries", maxPersistRetries)
			if retries >= maxPersistRetries {
				w.deadLettered.Add(uint64(len(batch)))
				slog.Error("stats event batch dead-lettered after max retries",
					"events", len(batch),
					"last_error", err)
				batch = batch[:0]
				retries = 0
			}
			return false
		}
		batch = batch[:0]
		retries = 0
		return true
	}
	for {
		select {
		case e := <-w.queue:
			batch = append(batch, e)
			if len(batch) >= 64 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case e := <-w.queue:
					batch = append(batch, e)
				default:
					// On shutdown, do one best-effort flush; if it still
					// fails the batch becomes dead-letter to avoid hanging
					// the process on a stuck DB.
					if !flush() {
						w.deadLettered.Add(uint64(len(batch)))
						slog.Error("stats event batch dead-lettered on shutdown",
							"events", len(batch))
						batch = batch[:0]
					}
					return
				}
			}
		}
	}
}

func (w *EventWriter) persist(ctx context.Context, events []Event) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, e := range events {
		if err := e.Valid(); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `
			INSERT INTO stats_event_dedup (event_id, occurred_at)
			VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`, e.EventID, e.OccurredAt)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			// The global dedup table is authoritative even though the inbox is
			// partitioned and cannot enforce a cross-partition event_id key.
			continue
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO stats_event_inbox (
				event_id, occurred_at, request_id, event_type, traffic_class,
				attempt_no, tenant_id, provider_id, credential_id, canonical_id,
				raw_model_name, api_key_id, application_id, end_user_id, person_hash,
				client_profile, agent_name, virtual_client_id, identity_hash, status,
				error_kind, error_class, error_code, failure_stage, attribution_owner,
				http_status, retryable, prompt_tokens, completion_tokens, cache_read_tokens,
				cache_write_tokens, reasoning_tokens, image_tokens, audio_tokens, video_tokens,
				provider_tokens, total_tokens, cost_usd, cost_currency, credits_charged,
				usage_source, pricing_version, latency_ms, ttft_ms, source, payload_version
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46)
			ON CONFLICT (event_id, occurred_at) DO NOTHING`,
			e.EventID, e.OccurredAt, e.RequestID, e.EventType, e.Traffic, e.AttemptNo,
			e.TenantID, e.ProviderID, e.CredentialID, e.CanonicalID, e.RawModelName,
			e.APIKeyID, e.ApplicationID, e.EndUserID, e.PersonHash, e.ClientProfile,
			e.AgentName, e.VirtualClient, e.IdentityHash, e.Status, e.ErrorKind,
			e.ErrorClass, e.ErrorCode, e.FailureStage, e.AttributionOwner, e.HTTPStatus,
			e.Retryable, e.PromptTokens, e.CompletionTokens, e.CacheReadTokens, e.CacheWriteTokens,
			e.ReasoningTokens, e.ImageTokens, e.AudioTokens, e.VideoTokens, e.ProviderTokens,
			e.TotalTokens, e.CostUSD, e.CostCurrency, e.CreditsCharged, e.UsageSource,
			e.PricingVersion, e.LatencyMs, e.TTFTMs, e.Source, e.PayloadVersion)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO usage_facts (
				event_id, request_id, revision, occurred_at, finalized_at, tenant_id,
				traffic_class, status, provider_id, credential_id, canonical_id, raw_model_name,
				api_key_id, application_id, end_user_id, person_hash, prompt_tokens,
				completion_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
				image_tokens, audio_tokens, video_tokens, provider_tokens, total_tokens,
				cost_amount, cost_currency, credits_charged, usage_source, pricing_version,
				latency_ms, ttft_ms, error_kind, error_class, failure_stage, source
			) VALUES ($1,$2,1,$3,now(),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35)
			ON CONFLICT (event_id, revision, occurred_at) DO NOTHING`,
			e.EventID, e.RequestID, e.OccurredAt, e.TenantID, e.Traffic, e.Status,
			e.ProviderID, e.CredentialID, e.CanonicalID, e.RawModelName, e.APIKeyID,
			e.ApplicationID, e.EndUserID, e.PersonHash, e.PromptTokens, e.CompletionTokens,
			e.CacheReadTokens, e.CacheWriteTokens, e.ReasoningTokens, e.ImageTokens,
			e.AudioTokens, e.VideoTokens, e.ProviderTokens, e.TotalTokens, e.CostUSD,
			e.CostCurrency, e.CreditsCharged, e.UsageSource, e.PricingVersion, e.LatencyMs,
			e.TTFTMs, e.ErrorKind, e.ErrorClass, e.FailureStage, "stats_event_inbox")
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
