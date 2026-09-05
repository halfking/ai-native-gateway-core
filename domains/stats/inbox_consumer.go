package stats

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultInboxBatchSize = 32
	defaultInboxInterval  = 500 * time.Millisecond
	defaultInboxLease     = 2 * time.Minute
	defaultInboxMaxTries  = 5
)

// InboxDB is the minimal database surface required by InboxConsumer.
type InboxDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

type pgxInboxDB struct{ pool *pgxpool.Pool }

func (d pgxInboxDB) Begin(ctx context.Context) (pgx.Tx, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("stats inbox: nil database")
	}
	return d.pool.Begin(ctx)
}

// InboxConfig controls claim, retry and worker-loop behavior.
type InboxConfig struct {
	Owner       string
	BatchSize   int
	Interval    time.Duration
	Lease       time.Duration
	MaxAttempts int
	Logger      *slog.Logger
}

func (c *InboxConfig) applyDefaults() {
	if strings.TrimSpace(c.Owner) == "" {
		c.Owner = "stats-inbox-" + fmt.Sprint(time.Now().UnixNano())
	}
	if c.BatchSize <= 0 {
		c.BatchSize = defaultInboxBatchSize
	}
	if c.Interval <= 0 {
		c.Interval = defaultInboxInterval
	}
	if c.Lease <= 0 {
		c.Lease = defaultInboxLease
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultInboxMaxTries
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// InboxEvent is an event claimed by a particular worker generation.
type InboxEvent struct {
	Event           Event
	FencingToken    int64
	ProcessAttempts int
}

// ReplayFilter limits a dead-letter replay. Empty fields mean no filter.
type ReplayFilter struct {
	EventID  string
	TenantID string
	From     time.Time
	To       time.Time
}

// InboxStats exposes durable consumer counters for metrics adapters.
type InboxStats struct {
	Claimed      uint64
	Processed    uint64
	Retried      uint64
	Failed       uint64
	DeadLettered uint64
	Replayed     uint64
	LeaseLost    uint64
}

// InboxConsumer claims stats_event_inbox rows and projects them into
// usage_facts with fenced, retry-safe state transitions.
type InboxConsumer struct {
	db  InboxDB
	cfg InboxConfig

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool

	claimed      atomic.Uint64
	processed    atomic.Uint64
	retried      atomic.Uint64
	failed       atomic.Uint64
	deadLettered atomic.Uint64
	replayed     atomic.Uint64
	leaseLost    atomic.Uint64
}

// NewInboxConsumer constructs the production consumer using a pgx pool.
func NewInboxConsumer(pool *pgxpool.Pool, cfg InboxConfig) *InboxConsumer {
	return newInboxConsumer(pgxInboxDB{pool: pool}, cfg)
}

// NewInboxConsumerWithDB is intended for unit tests and custom DB adapters.
func NewInboxConsumerWithDB(db InboxDB, cfg InboxConfig) *InboxConsumer {
	return newInboxConsumer(db, cfg)
}

func newInboxConsumer(db InboxDB, cfg InboxConfig) *InboxConsumer {
	cfg.applyDefaults()
	return &InboxConsumer{db: db, cfg: cfg, done: make(chan struct{})}
}

func (c *InboxConsumer) Stats() InboxStats {
	if c == nil {
		return InboxStats{}
	}
	return InboxStats{
		Claimed: c.claimed.Load(), Processed: c.processed.Load(), Retried: c.retried.Load(),
		Failed: c.failed.Load(), DeadLettered: c.deadLettered.Load(), Replayed: c.replayed.Load(),
		LeaseLost: c.leaseLost.Load(),
	}
}

// Start launches one polling loop. Repeated Start calls are harmless.
func (c *InboxConsumer) Start(ctx context.Context) {
	if c == nil || c.db == nil {
		return
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.cancel = cancel
	c.done = done
	c.started = true
	c.mu.Unlock()

	go c.run(workerCtx, done)
}

// Stop cancels the polling loop and is safe before Start.
func (c *InboxConsumer) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return
	}
	cancel := c.cancel
	done := c.done
	c.mu.Unlock()

	cancel()
	<-done
}

func (c *InboxConsumer) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
				c.cfg.Logger.Warn("stats inbox poll failed", "owner", c.cfg.Owner, "error", err)
			}
		}
	}
}

// RunOnce claims and handles up to the configured batch size.
func (c *InboxConsumer) RunOnce(ctx context.Context) error {
	if c == nil || c.db == nil {
		return nil
	}
	events, err := c.claim(ctx, c.cfg.BatchSize)
	if err != nil {
		return err
	}
	var firstErr error
	for _, claimed := range events {
		if err := c.projectAndMarkProcessed(ctx, claimed); err != nil {
			if markErr := c.markFailed(ctx, claimed, err); markErr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("stats inbox project %s: %w; record failure: %v", claimed.Event.EventID, err, markErr)
				}
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("stats inbox project %s: %w", claimed.Event.EventID, err)
			}
		}
	}
	return firstErr
}

func (c *InboxConsumer) claim(ctx context.Context, limit int) ([]InboxEvent, error) {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("stats inbox begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, claimSQL, c.cfg.Owner, c.cfg.Lease.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("stats inbox claim: %w", err)
	}
	defer rows.Close()
	claimed := make([]InboxEvent, 0, limit)
	for rows.Next() {
		event, token, attempts, err := scanInboxEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("stats inbox scan claim: %w", err)
		}
		claimed = append(claimed, InboxEvent{Event: event, FencingToken: token, ProcessAttempts: attempts})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats inbox claim rows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("stats inbox commit claim: %w", err)
	}
	c.claimed.Add(uint64(len(claimed)))
	return claimed, nil
}

func (c *InboxConsumer) projectAndMarkProcessed(ctx context.Context, claimed InboxEvent) error {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("stats inbox begin projection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentToken int64
	if err := tx.QueryRow(ctx, projectLeaseSQL, claimed.Event.EventID, claimed.Event.OccurredAt, c.cfg.Owner, claimed.FencingToken).Scan(&currentToken); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("stats inbox projection lost lease for %s", claimed.Event.EventID)
		}
		return fmt.Errorf("stats inbox verify lease: %w", err)
	}
	if err := insertUsageFactTx(ctx, tx, claimed.Event); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, markProcessedSQL,
		claimed.Event.EventID, claimed.Event.OccurredAt, c.cfg.Owner, currentToken)
	if err != nil {
		return fmt.Errorf("stats inbox mark projected: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("stats inbox mark projected lost lease for %s", claimed.Event.EventID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("stats inbox commit projection: %w", err)
	}
	c.processed.Add(1)
	return nil
}

func (c *InboxConsumer) markFailed(ctx context.Context, claimed InboxEvent, projectErr error) error {
	if claimed.ProcessAttempts <= 0 {
		return fmt.Errorf("stats inbox invalid attempt count for %s", claimed.Event.EventID)
	}
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("stats inbox begin failure update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	status := "retryable"
	if claimed.ProcessAttempts >= c.cfg.MaxAttempts {
		status = "dead_letter"
	}
	result, err := tx.Exec(ctx, markFailedSQL,
		claimed.Event.EventID,
		claimed.Event.OccurredAt,
		c.cfg.Owner,
		claimed.FencingToken,
		status,
		truncateInboxError(projectErr),
	)
	if err != nil {
		return fmt.Errorf("stats inbox mark failure: %w", err)
	}
	if result.RowsAffected() != 1 {
		c.leaseLost.Add(1)
		return fmt.Errorf("stats inbox mark failure lost lease for %s", claimed.Event.EventID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("stats inbox commit failure update: %w", err)
	}
	c.failed.Add(1)
	if status == "dead_letter" {
		c.deadLettered.Add(1)
	} else {
		c.retried.Add(1)
	}
	return nil
}

func truncateInboxError(err error) string {
	const maxErrorBytes = 1024
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) <= maxErrorBytes {
		return message
	}
	return message[:maxErrorBytes]
}

// ReplayDLQ makes selected dead-letter rows eligible for another claim.
func (c *InboxConsumer) ReplayDLQ(ctx context.Context, filter ReplayFilter) (int64, error) {
	if c == nil || c.db == nil {
		return 0, nil
	}
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("stats inbox begin replay: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, replaySQL, nullableString(filter.EventID), nullableString(filter.TenantID), nullableTime(filter.From), nullableTime(filter.To))
	if err != nil {
		return 0, fmt.Errorf("stats inbox replay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("stats inbox commit replay: %w", err)
	}
	count := result.RowsAffected()
	c.replayed.Add(uint64(count))
	return count, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

const projectLeaseSQL = `
SELECT fencing_token FROM stats_event_inbox
WHERE event_id = $1 AND occurred_at = $2
  AND processing_status = 'processing'
  AND processing_owner = $3 AND fencing_token = $4
  AND lease_until > now()
FOR UPDATE`

const markProcessedSQL = `
UPDATE stats_event_inbox
SET processing_status = 'processed', processed_at = now(),
    processing_owner = $3, lease_until = NULL, last_error = NULL,
    retryable = false, next_attempt_at = now()
WHERE event_id = $1 AND occurred_at = $2
  AND processing_status = 'processing'
  AND processing_owner = $3 AND fencing_token = $4`

const markFailedSQL = `
UPDATE stats_event_inbox
SET processing_status = $5,
    processing_owner = NULL,
    lease_until = NULL,
    retryable = ($5 = 'retryable'),
    next_attempt_at = CASE WHEN $5 = 'retryable' THEN now() + INTERVAL '1 minute' ELSE now() END,
    last_error = $6,
    dead_lettered_at = CASE WHEN $5 = 'dead_letter' THEN now() ELSE NULL END,
    dead_letter_reason = CASE WHEN $5 = 'dead_letter' THEN $6 ELSE NULL END
WHERE event_id = $1 AND occurred_at = $2
  AND processing_status = 'processing'
  AND processing_owner = $3 AND fencing_token = $4
  AND lease_until > now()`

const claimSQL = `
WITH claimable AS (
    SELECT event_id, occurred_at
    FROM stats_event_inbox
    WHERE processed_at IS NULL
      AND (
        processing_status IN ('pending', 'retryable')
        OR (processing_status = 'processing' AND lease_until < now())
      )
      AND next_attempt_at <= now()
      AND (lease_until IS NULL OR lease_until < now())
    ORDER BY next_attempt_at, occurred_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT $3
)
UPDATE stats_event_inbox AS event
SET processing_status = 'processing', processing_owner = $1,
    lease_until = now() + $2::interval,
    fencing_token = event.fencing_token + 1,
    process_attempts = event.process_attempts + 1
FROM claimable
WHERE event.event_id = claimable.event_id AND event.occurred_at = claimable.occurred_at
RETURNING event.event_id, event.occurred_at, event.request_id, event.event_type,
 event.traffic_class, event.attempt_no, event.tenant_id, event.provider_id,
 event.credential_id, event.canonical_id, event.raw_model_name, event.api_key_id,
 event.application_id, event.end_user_id, event.person_hash, event.client_profile,
 event.agent_name, event.virtual_client_id, event.identity_hash, event.status,
 event.error_kind, event.error_class, event.error_code, event.failure_stage,
 event.attribution_owner, event.http_status, event.retryable, event.prompt_tokens,
 event.completion_tokens, event.cache_read_tokens, event.cache_write_tokens,
 event.reasoning_tokens, event.image_tokens, event.audio_tokens, event.video_tokens,
 event.provider_tokens, event.total_tokens, event.cost_usd, event.cost_currency,
 event.credits_charged, event.usage_source, event.pricing_version, event.latency_ms,
	 event.ttft_ms, event.source, event.payload_version, event.process_attempts, event.fencing_token`

const replaySQL = `
UPDATE stats_event_inbox
SET processing_status = 'retryable', processed_at = NULL, processing_owner = NULL,
    lease_until = NULL, process_attempts = 0, next_attempt_at = now(),
    retryable = true, last_error = NULL, dead_lettered_at = NULL, dead_letter_reason = NULL
WHERE processing_status = 'dead_letter'
  AND ($1::text IS NULL OR event_id = $1)
  AND ($2::text IS NULL OR tenant_id = $2)
  AND ($3::timestamptz IS NULL OR occurred_at >= $3)
  AND ($4::timestamptz IS NULL OR occurred_at < $4)`

func scanInboxEvent(rows pgx.Rows) (Event, int64, int, error) {
	var (
		e                                                                                                                           Event
		typ, traffic, tenant, rawModel, endUser, personHash, clientProfile, agentName, virtualClient, identityHash                  pgtype.Text
		status, errorKind, errorClass, errorCode, failureStage, attributionOwner, costCurrency, usageSource, pricingVersion, source pgtype.Text
		providerID, credentialID, canonicalID, apiKeyID, applicationID                                                              pgtype.Int8
		httpStatus, payloadVersion, processAttempts                                                                                 pgtype.Int4
		token                                                                                                                       int64
	)
	err := rows.Scan(
		&e.EventID, &e.OccurredAt, &e.RequestID, &typ, &traffic, &e.AttemptNo, &tenant,
		&providerID, &credentialID, &canonicalID, &rawModel, &apiKeyID, &applicationID,
		&endUser, &personHash, &clientProfile, &agentName, &virtualClient, &identityHash,
		&status, &errorKind, &errorClass, &errorCode, &failureStage, &attributionOwner,
		&httpStatus, &e.Retryable, &e.PromptTokens, &e.CompletionTokens, &e.CacheReadTokens,
		&e.CacheWriteTokens, &e.ReasoningTokens, &e.ImageTokens, &e.AudioTokens, &e.VideoTokens,
		&e.ProviderTokens, &e.TotalTokens, &e.CostUSD, &costCurrency, &e.CreditsCharged,
		&usageSource, &pricingVersion, &e.LatencyMs, &e.TTFTMs, &source, &payloadVersion, &processAttempts, &token)
	if err != nil {
		return Event{}, 0, 0, err
	}
	e.EventType = EventType(typ.String)
	e.Traffic = TrafficClass(traffic.String)
	e.TenantID, e.RawModelName, e.EndUserID = tenant.String, rawModel.String, endUser.String
	e.PersonHash, e.ClientProfile, e.AgentName = personHash.String, clientProfile.String, agentName.String
	e.VirtualClient, e.IdentityHash = virtualClient.String, identityHash.String
	e.Status, e.ErrorKind, e.ErrorClass = status.String, errorKind.String, errorClass.String
	e.ErrorCode, e.FailureStage, e.AttributionOwner = errorCode.String, failureStage.String, attributionOwner.String
	e.CostCurrency, e.UsageSource, e.PricingVersion, e.Source = costCurrency.String, usageSource.String, pricingVersion.String, source.String
	e.PayloadVersion = int(payloadVersion.Int32)
	if providerID.Valid {
		v := int(providerID.Int64)
		e.ProviderID = &v
	}
	if credentialID.Valid {
		v := int(credentialID.Int64)
		e.CredentialID = &v
	}
	if canonicalID.Valid {
		v := int(canonicalID.Int64)
		e.CanonicalID = &v
	}
	if apiKeyID.Valid {
		v := int(apiKeyID.Int64)
		e.APIKeyID = &v
	}
	if applicationID.Valid {
		v := int(applicationID.Int64)
		e.ApplicationID = &v
	}
	if httpStatus.Valid {
		v := int(httpStatus.Int32)
		e.HTTPStatus = &v
	}
	return e, token, int(processAttempts.Int32), nil
}

func insertUsageFactTx(ctx context.Context, tx pgx.Tx, e Event) error {
	_, err := tx.Exec(ctx, `
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
		return fmt.Errorf("stats usage fact insert: %w", err)
	}
	return nil
}
