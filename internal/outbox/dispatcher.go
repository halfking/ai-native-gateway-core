// Package outbox implements the durable outbox pattern for Gateway → ASM event delivery.
//
// Phase 2 Step 4: OutboxDispatcher implementation
// Refs: docs/修订0811/06-下一阶段实施计划.md Phase 2
//
//	docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §4
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const gaugeRefreshInterval = 30 * time.Second

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
	deliverer    *HTTPDeliverer
	logger       *slog.Logger

	// lastGaugeAt throttles updateGaugeMetrics: the two COUNT(*) probes run
	// on the dispatch goroutine, so polling them every 5s cycle doubles the
	// outbox_events read load for a monitoring gauge. 30s staleness is fine.
	gaugeMu     sync.Mutex
	lastGaugeAt time.Time

	// 背压熔断器（2026-10-28 生产审计）：asm 的 503 ingress_unavailable /
	// event_retryable 是「目的地全局关门」的信号，与单条事件无关。没有熔断
	// 时，积压的每条事件仍按各自 64s 退避独立重试 ⇒ N 条积压 ⇒ N/64 次/秒的
	// 送达风暴（211 实测 4191 条积压 ⇒ ~65 req/s、24h 29.4 万条 WARN，而 asm
	// 的 inbox 一条也没消化掉）。熔断语义：批内**第一条**背压即停批 + 暂停
	// 整个派发循环一个 cooldown，之后每个 cooldown 只发**一次**探测请求；
	// 门一开，下一个批照常把积压全部排空。至少一次语义不变：事件仍留在
	// failed/pending，不落 DLQ、不丢数据。
	bpMu          sync.Mutex
	bpPausedUntil time.Time
	bpCooldown    time.Duration
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
	// BackpressureCooldown is how long the dispatcher pauses the whole loop
	// after the first destination-backpressure failure in a batch. Defaults
	// to backpressureProbeCooldown (64s). ★ 刻意**不**复用
	// backpressureRetryCap：那是「单条事件的退避上限」（指数封顶 10min），
	// 而这是「整个派发循环的探测节奏」—— 探测是单条廉价请求，用 10min 的
	// 盲区换不来任何额外减压（暂停期间本来就是 0 req/s），只会把「门已重开」
	// 的发现延迟最多 10 分钟。两个常数语义不同，解耦就是防漂移。
	BackpressureCooldown time.Duration
	Logger               *slog.Logger
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
	if cfg.BackpressureCooldown == 0 {
		cfg.BackpressureCooldown = backpressureProbeCooldown
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	return &Dispatcher{
		db:           cfg.DB,
		asmEndpoint:  cfg.ASMEndpoint,
		hmacSecret:   cfg.HMACSecret,
		pollInterval: cfg.PollInterval,
		maxAttempts:  cfg.MaxAttempts,
		bpCooldown:   cfg.BackpressureCooldown,
		deliverer: NewHTTPDeliverer(HTTPDelivererConfig{
			Endpoint:        cfg.ASMEndpoint,
			Secret:          cfg.HMACSecret,
			AcceptDuplicate: true,

			HTTPClient: httpClient,
		}),
		logger: cfg.Logger,
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
			if d.backpressurePaused(time.Now()) {
				// 目的地关门中：不 claim、不送达，一个 cooldown 只留一次
				// 探测（由 cooldown 到期后的第一个 tick 承担）。仪表照常
				// 刷新，让积压读数在暂停期间仍然可见。
				d.updateGaugeMetrics(ctx)
				continue
			}
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
		case dispatchOutcomeBackpressure:
			// 目的地关门（503 + asm 背压码）。剩下可 claim 的事件会以同样的
			// 方式失败——它们与这条事件的唯一共同点是同一个目的地。立刻停
			// 批：熔断已在 dispatchOne 里挂起整个循环，这里不再逐条敲打。
			// 事件已按背压口径落账（failed + 64s 退避，不落 DLQ），语义与
			// 逐条重试完全一致，只是不再制造 N/64 次/秒的送达风暴。
			d.updateGaugeMetrics(ctx)
			return nil
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
	dispatchOutcomeNone dispatchOutcome = iota // no claimable event remaining
	dispatchOutcomeSent                        // delivered + marked sent
	dispatchOutcomeFailed                      // delivery failed + marked failed/dlq
	// dispatchOutcomeBackpressure: the destination gate is closed (503 +
	// asm backpressure code). The event was marked with backpressure
	// semantics; the batch must stop and the loop pauses for one cooldown.
	dispatchOutcomeBackpressure
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
		// Poison-pill event (corrupt payload): markDLQ directly, no retry —
		// a malformed payload can never succeed, so retrying would only
		// block the queue head. Count "failed" but skip the duration
		// histogram since the failure never reached ASM.
		// (R43: 注释曾误称"max_attempts 后进 DLQ"——实际一步直呼 markDLQ。)
		d.logger.Error("unmarshal payload failed", "event_id", eventID, "error", err)
		RecordEventFailed("validation")
		d.markDLQ(ctx, tx, id, attempts+1, fmt.Sprintf("unmarshal error: %v", err))
		if err := tx.Commit(); err != nil {
			return dispatchOutcomeNone, fmt.Errorf("commit (unmarshal-fail mark): %w", err)
		}
		return dispatchOutcomeFailed, nil
	}

	// Time only the HTTP roundtrip; the rest of dispatchOne is in-process
	// overhead that the dispatcher loop batches.
	start := time.Now()
	err = d.deliverer.Deliver(ctx, env)
	duration := time.Since(start).Seconds()
	ObserveDeliveryDuration(duration, err == nil)

	if err != nil {
		d.logger.Warn("dispatch failed", "event_id", eventID, "attempts", attempts+1, "error", err)
		RecordEventFailed(classifyError(err))
		switch {
		case IsBackpressure(err):
			// ★ 下游背压（asm 就绪门抖动 ⇒ 503 ingress_unavailable / event_retryable）
			//   不是「这条事件有问题」，是「目的地暂时关门」⇒ 永不 DLQ：
			//   2026-10-28 生产审计实测：asm runner 就绪门的抖动以分钟计，而
			//   maxAttempts=5 × 指数退避（≈31s 窗口）会把**完全合法的事件**永久打进
			//   DLQ（acc-pg17 实测 dlq=34,793 且以 ~170/小时增长，26,079 条
			//   ingress_unavailable + 8,699 条 event_retryable）⇒ 数据白白丢掉。
			//   ⇒ 背压类改为**不计数转 DLQ**、64s 封顶退避无限重试（asm 恢复就绪
			//   即送达；若 asm 永久宕机，行以 failed 累积而非 DLQ —— 同样占表，
			//   但可恢复，且与 DLQ 一样可被运维裁决）。
			d.markBackpressure(ctx, tx, id, attempts+1, err.Error())
			if err := tx.Commit(); err != nil {
				return dispatchOutcomeNone, fmt.Errorf("commit (dispatch-fail mark): %w", err)
			}
			// 背压是目的地的全局属性：第一条就足以断定「门关了」。挂起整个
			// 派发循环并停批（返回值告诉 dispatchBatch 别再 claim 下一条）。
			// 211 实测（2026-10-28）：4191 条积压各自独立重试（当时固定 64s
			// 退避），合成 ~65 req/s 的送达风暴 + 24h 29.4 万条 WARN；asm
			// inbox 排空速率为 0——风暴没有换回任何进展，只是给关门的入口
			// 持续加压。停批后即使单条退避再怎么衰减，整循环也只有每个
			// cooldown 一次探测。
			d.tripBackpressurePause(time.Now())
			return dispatchOutcomeBackpressure, nil
		case Retryable(err):
			d.markFailed(ctx, tx, id, attempts+1, err.Error())
		default:
			d.markDLQ(ctx, tx, id, attempts+1, err.Error())
		}
		if err := tx.Commit(); err != nil {
			return dispatchOutcomeNone, fmt.Errorf("commit (dispatch-fail mark): %w", err)
		}
		if Retryable(err) && attempts+1 < d.maxAttempts {
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

// markSent updates the event to status='sent'. ex is the tx that holds the
// claim lock (or d.db for non-transactional callers).
func (d *Dispatcher) markSent(ctx context.Context, ex execer, id int64) {
	const query = `
		UPDATE outbox_events
		SET status = 'sent', last_attempt_at = NOW(), last_error = NULL,
		    next_retry_at = NULL, updated_at = NOW()
		WHERE id = $1
	`
	if _, err := ex.ExecContext(ctx, query, id); err != nil {
		d.logger.Error("mark sent failed", "id", id, "error", err)
	}
}

// markFailed updates the event status to 'failed' or 'dlq'. ex is the tx
// that holds the claim lock (or d.db for non-transactional callers).//
// Exponential backoff:
//   - Attempt 1: retry after 1s
//   - Attempt 2: retry after 2s
//   - Attempt 3: retry after 4s
//   - Attempt 4: retry after 8s
//   - Attempt 5+: move to DLQ
func (d *Dispatcher) markFailed(ctx context.Context, ex execer, id int64, newAttempts int, errMsg string) {
	if newAttempts >= d.maxAttempts {
		d.markDLQ(ctx, ex, id, newAttempts, errMsg)
		return
	}
	// Exponential backoff: 2^(attempts-1) seconds, shift clamped (2026-09-09
	// audit round 3) — maxAttempts is operator-configurable and a value ≥ 33
	// would overflow time.Duration into a negative backoff, making
	// next_retry_at "now" and triggering a tight-retry storm. Mirrors the
	// webhook.go clamp.
	shift := uint(newAttempts - 1)
	if shift > 6 {
		shift = 6 // cap at 64s
	}
	backoff := time.Duration(1<<shift) * time.Second
	nextRetry := time.Now().Add(backoff)
	const query = `
		UPDATE outbox_events
		SET status = 'failed', attempts = $1, last_error = $2,
		    last_attempt_at = NOW(), next_retry_at = $3, updated_at = NOW()
		WHERE id = $4
	`
	if _, err := ex.ExecContext(ctx, query, newAttempts, errMsg, nextRetry, id); err != nil {
		d.logger.Error("mark failed failed", "id", id, "error", err)
	}
}

// 背压退避：8s 起、每次 ×2、封顶 10 分钟。
//
// 2026-10-28 生产实测（acc-pg17）修订：固定 64s 在长窗口背压下会变成
// 「永久锤击」——asm 连续 503 约 10 小时后积压 ~3.9k 条，3904/64s ≈
// 61 req/s 的 HTTP + 每条一个 DB 事务 + 每秒 ~60 条 WARN 日志，全部打在
// 一个本就在拒绝的目的地上；而成功速率（~880/h）低于事件产生速率
// （~1400/h），积压还在涨。指数退避把稳态压力从 61 req/s 衰减到
// ~6.5 req/s（3904/600s），asm 真正恢复后整个积压 ≤ 10 分钟内排空
// （每条事件的下一次重试至多落后一个 cap），对「事件到达即消费」的
// request.completed 语义足够及时。
const (
	backpressureRetryFloor = 8 * time.Second
	backpressureRetryCap   = 10 * time.Minute
)

// backpressureProbeCooldown 是背压熔断器的探测节奏（与单条事件的退避上限
// backpressureRetryCap **刻意解耦**）：暂停期间整循环本来就是 0 req/s，
// 探测又只是单条廉价请求，64s 的节奏让「门已重开」最多 64s 就被发现，
// 而把 10min 级的盲区留给它只会白白拖慢恢复。（2026-10-29 合并防漂移：
// backpressureRetryCap 曾从 64s 改为 10min，若默认值继续引用它，探测节奏
// 会被动跟着变成 10 分钟。）
const backpressureProbeCooldown = 64 * time.Second

// backpressureBackoff 由 attempts 推导本次退避时长：floor × 2^(attempts-1)
// 截到 cap。attempts 是历史累计值（含固定 64s 时代），老积压事件首拍即落
// cap —— 正是想要的「长背压下先安静」；移位先钳位防大 attempts 溢出。
func backpressureBackoff(attempts int) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 20 {
		shift = 20
	}
	backoff := backpressureRetryFloor << uint(shift)
	if backoff > backpressureRetryCap || backoff <= 0 {
		return backpressureRetryCap
	}
	return backoff
}

// markBackpressure 是 markFailed 的「永不 DLQ」变体：目的地背压（asm 就绪门
// 抖动 503）不消耗 maxAttempts 预算 —— 事件本身完全合法，唯一的问题是
// 「目的地暂时关门」。status 保持 'failed'、按 attempts 指数退避（封顶
// backpressureRetryCap）无限重试；attempts 仍递增（可观测性），但本函数
// **绝不**调 markDLQ。
// 若 asm 永久宕机，行以 failed 累积（与 DLQ 同样占表、同样可见），
// 但 asm 恢复后自动排空 —— 这正是 outbox「至少一次」该有的语义。
// （asm 的 Retry-After: 5 是「单事件可立即重试」的地板提示，不构成对
// 数千条积压的重放时间表 —— 按它重试等于把锤击恢复到 61 req/s。）
func (d *Dispatcher) markBackpressure(ctx context.Context, ex execer, id int64, newAttempts int, errMsg string) {
	backoff := backpressureBackoff(newAttempts)
	const query = `
		UPDATE outbox_events
		SET status = 'failed', attempts = $1, last_error = $2,
		    last_attempt_at = NOW(), next_retry_at = $3, updated_at = NOW()
		WHERE id = $4
	`
	if _, err := ex.ExecContext(ctx, query, newAttempts, errMsg, time.Now().Add(backoff), id); err != nil {
		d.logger.Error("mark backpressure failed", "id", id, "error", err)
	}
}

// tripBackpressurePause 挂起整个派发循环一个 cooldown。已在 paused 期间内的
// 重复触发只延长窗口，不再重复打日志（每 episodes 一条 WARN）。
func (d *Dispatcher) tripBackpressurePause(now time.Time) {
	d.bpMu.Lock()
	defer d.bpMu.Unlock()
	until := now.Add(d.bpCooldown)
	if now.Before(d.bpPausedUntil) {
		d.bpPausedUntil = until
		return
	}
	d.bpPausedUntil = until
	d.logger.Warn("outbox dispatch paused: destination backpressure",
		"cooldown", d.bpCooldown.String(),
		"action", "batch aborted; one probe per cooldown until asm reopens",
	)
}

// backpressurePaused reports whether the dispatch loop is currently paused
// by the backpressure breaker.
func (d *Dispatcher) backpressurePaused(now time.Time) bool {
	d.bpMu.Lock()
	defer d.bpMu.Unlock()
	return now.Before(d.bpPausedUntil)
}

func (d *Dispatcher) markDLQ(ctx context.Context, ex execer, id int64, attempts int, errMsg string) {
	const query = `
		UPDATE outbox_events
		SET status = 'dlq', attempts = $1, last_error = $2,
		    last_attempt_at = NOW(), next_retry_at = NULL, updated_at = NOW()
		WHERE id = $3
	`
	if _, err := ex.ExecContext(ctx, query, attempts, errMsg, id); err != nil {
		d.logger.Error("move to dlq failed", "id", id, "error", err)
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
// Called after each poll cycle but throttled to gaugeRefreshInterval for
// accurate-enough monitoring without scanning outbox_events every 5s.
func (d *Dispatcher) updateGaugeMetrics(ctx context.Context) {
	d.gaugeMu.Lock()
	if time.Since(d.lastGaugeAt) < gaugeRefreshInterval {
		d.gaugeMu.Unlock()
		return
	}
	d.lastGaugeAt = time.Now()
	d.gaugeMu.Unlock()
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
