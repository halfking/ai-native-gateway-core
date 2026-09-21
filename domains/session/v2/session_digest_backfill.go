// Package v2 — session_digest_backfill.go
//
// turn-digest 第二阶段（2026-09-04）：后台回填 session_turns.digest 的 jsonb
// envelope。migration 456 之前落库的轮次 digest 列为 NULL，admin 读路径
// （persistedDigestOrFallback）只能对它们做 on-the-fly 重建，既重复解析正文
// JSON，也让 digestFallbackTotal{reason="missing"} 长期非零、真假故障不可辨。
// 本 job 扫描 digest IS NULL 的轮次，用与写路径（session_writer_v2.Write）完全
// 相同的 sessiondigest.Build 输入形状重建 envelope 并写回，让存量行与新行
// 走同一条持久化读路径。
//
// 正确性模型：
//   - 幂等：回写 UPDATE 一律带 `AND digest IS NULL` 守卫。重复调度、多副本
//     并发、job 重启都只会让其中一个 UPDATE 生效（0 行受影响按 skipped 计数），
//     不会二次覆盖。
//   - 确定性：generatedAt 使用行自身的 ts（写路径用 req.Timestamp，同一来源），
//     同一行重复回填产出逐字节相同的 envelope。
//   - 空转退出：sessiondigest.Build 在 meta 键齐备时恒返回非 nil envelope
//     （hasMetrics 按键存在性判定），因此正常情况下每行都会被写上 digest；
//     若未来 Build 规则变化出现 nil envelope，写入 'null'::jsonb 标记——
//     jsonb null 不等于 SQL NULL，行不会再被选中，避免死循环。
//   - RLS：session_turns/session_bodies 启用行级安全，跨租户扫描必须带
//     bypass GUC。set_config(..., true) 是事务级的，因此整批在一个事务里
//     执行（与 session_aggregate_outbox_reaper 相同模式）。
//
// 节流与空闲退避：
//   - 写回按 env 配置限速（默认 100 rows/s，ticker 间隔 1s/rate）。
//   - 批间连续处理直到清空候选（每批一个事务，受 tick 超时约束）。
//   - 空批后按 30s → 60s → … → 30min 指数退避，避免每 30 秒对 UNION ALL
//     视图做一次空扫描。
//
// Shutdown：与 reaper 相同的 Start/Stop 幂等语义，Stop 等待 in-flight 批
// 返回（rate 等待与批间检查都监听 stopCh，最迟一个批次超时内退出）。
package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
)

// digestBackfillTotal 按低基数 result 分桶统计回填结果：
//
//	filled  — envelope 写回成功（RowsAffected>0）
//	empty   — Build 返回 nil，写入 'null'::jsonb 标记（防御路径，当前不可达）
//	skipped — UPDATE 双表均 0 行（并发副本已填 / 行刚被 promote 移动后落位）
//	error   — 批级 DB 错误（整批回滚，行保持 NULL 等待下轮重试）
var digestBackfillTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_session_turn_digest_backfill_total",
		Help: "Session turns whose NULL digest column was backfilled with a sessiondigest envelope (label = outcome)",
	},
	[]string{"result"},
)

// digestBackfillDuration 观测非空批的处理耗时（SELECT + N 次限速 UPDATE +
// COMMIT）。空批（空闲探测）不计入，避免直方图被空闲扫描稀释。
var digestBackfillDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "llmgw_session_turn_digest_backfill_duration_seconds",
		Help:    "Wall-clock duration of a non-empty session turn digest backfill batch (SELECT through COMMIT)",
		Buckets: prometheus.DefBuckets,
	},
)

const (
	sessionDigestBackfillDefaultInterval = 30 * time.Second
	sessionDigestBackfillDefaultBatch    = 100
	sessionDigestBackfillDefaultRate     = 100
	// sessionDigestBackfillMaxIdle caps the exponential idle backoff so a
	// newly-NULLed row (writer outage) is still picked up within 30 minutes.
	sessionDigestBackfillMaxIdle = 30 * time.Minute
	// sessionDigestBackfillBatchTimeout bounds one batch transaction so a
	// stuck DB cannot wedge the goroutine past Stop's doneCh wait.
	sessionDigestBackfillBatchTimeout = 5 * time.Minute
	// sessionDigestBackfillMaxRate guards against a fat-fingered env value
	// turning the job into an unthrottled UPDATE storm.
	sessionDigestBackfillMaxRate = 1000
)

// digestBackfillDB is the minimal pool surface the backfill needs. Declared
// as an interface so unit tests inject pgxmock (same seam as outboxDB).
type digestBackfillDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// digestBackfillRow is one candidate turn materialized from the batch SELECT.
// Nullable columns are COALESCE'd in the SELECT to the same zero values the
// admin read path derives (intPtrValue etc.), so the rebuilt meta map mirrors
// what the live writer would have passed (key always present).
type digestBackfillRow struct {
	id                                int64
	tenantID, sessionID, requestID    string
	turnNo                            int
	ts                                time.Time
	promptTokens, completionTokens    int
	cacheReadTokens, cacheWriteTokens int
	costUSD                           float64
	latencyMs, statusCode             int
	success                           bool
	errorKind                         string
	injectionVerdict, outputVerdict   string
	compressionApplied                bool
	compressionTokensSaved            int
	requestDelta, responseDelta       []byte
}

// sessionDigestBackfill is the background job that drains NULL digest rows.
type sessionDigestBackfill struct {
	db         digestBackfillDB
	interval   time.Duration
	batchSize  int
	ratePerSec int
	limiter    *time.Ticker
	stopCh     chan struct{}
	doneCh     chan struct{}
	mu         sync.Mutex
	started    bool
	stopped    bool
}

func newSessionDigestBackfill(db *pgxpool.Pool, interval time.Duration, batchSize, ratePerSec int) *sessionDigestBackfill {
	return newSessionDigestBackfillForTest(db, interval, batchSize, ratePerSec)
}

// newSessionDigestBackfillForTest is the interface-accepting seam used by
// unit tests with pgxmock. Production callers must use
// StartSessionDigestBackfill (concrete *pgxpool.Pool enforces the real pool).
func newSessionDigestBackfillForTest(db digestBackfillDB, interval time.Duration, batchSize, ratePerSec int) *sessionDigestBackfill {
	if interval <= 0 {
		interval = sessionDigestBackfillDefaultInterval
	}
	if batchSize <= 0 {
		batchSize = sessionDigestBackfillDefaultBatch
	}
	if ratePerSec < 0 {
		ratePerSec = 0 // 0 = unlimited (tests / explicit opt-out of throttling)
	}
	if ratePerSec > sessionDigestBackfillMaxRate {
		ratePerSec = sessionDigestBackfillMaxRate
	}
	return &sessionDigestBackfill{
		db:         db,
		interval:   interval,
		batchSize:  batchSize,
		ratePerSec: ratePerSec,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// StartSessionDigestBackfill starts the backfill job in the background.
// Idempotent: a second Start on the same handle is a no-op. Returns the
// started handle so the shutdown path can drain it.
func StartSessionDigestBackfill(ctx context.Context, pool *pgxpool.Pool, batchSize, ratePerSec int) *sessionDigestBackfill {
	b := newSessionDigestBackfill(pool, 0, batchSize, ratePerSec)
	b.Start(ctx)
	return b
}

// Start launches the backfill goroutine.
func (b *sessionDigestBackfill) Start(ctx context.Context) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.started || b.stopped {
		b.mu.Unlock()
		return
	}
	b.started = true
	b.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if b.ratePerSec > 0 {
		b.limiter = time.NewTicker(time.Second / time.Duration(b.ratePerSec))
	}
	go b.run(ctx)
}

// Stop halts the job and waits for the in-flight batch to return.
func (b *sessionDigestBackfill) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.stopped = true
	close(b.stopCh)
	started := b.started
	b.mu.Unlock()
	if started {
		<-b.doneCh
	}
}

func (b *sessionDigestBackfill) run(ctx context.Context) {
	defer close(b.doneCh)
	if b.limiter != nil {
		defer b.limiter.Stop()
	}
	if !isUsablePool(b.db) {
		// Same defensive posture as the reaper: production wires a real pool,
		// but a nil/typed-nil (tests) must not panic the goroutine.
		slog.Warn("session digest backfill: nil pool, job not started")
		return
	}

	wait := b.interval
	for {
		processed, err := b.drain(ctx)
		if err != nil {
			slog.Error("session digest backfill: drain error", "error", err)
		}
		if processed > 0 {
			// Backlog found: loop immediately and keep draining at the
			// throttled rate. The next drain's first SELECT is cheap when the
			// backlog just emptied.
			wait = b.interval
			continue
		}
		// Idle: exponential backoff so the empty-candidate SELECT (a scan
		// over the UNION ALL view — digest IS NULL has no index) does not
		// run every interval forever.
		if wait > sessionDigestBackfillMaxIdle {
			wait = sessionDigestBackfillMaxIdle
		}
		timer := time.NewTimer(wait)
		wait *= 2
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-b.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// drain processes consecutive batches until the candidates are exhausted, a
// batch errors, or stop/ctx fires. Returns the number of candidate rows seen
// (selected), so an empty drain is distinguishable from a stalled one.
func (b *sessionDigestBackfill) drain(ctx context.Context) (int, error) {
	total := 0
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-b.stopCh:
			return total, nil
		default:
		}
		batchCtx, cancel := context.WithTimeout(ctx, sessionDigestBackfillBatchTimeout)
		selected, err := b.processBatch(batchCtx)
		cancel()
		total += selected
		if err != nil {
			return total, err
		}
		// A short batch means the NULL-digest candidate set is drained.
		if selected < b.batchSize {
			return total, nil
		}
	}
}

// sessionDigestBackfillSelectSQL mirrors the admin turn readers' JOIN keys
// (tenant/session/turn/request) against the same unified views so the backfill
// sees exactly the bodies an admin request would, regardless of whether the
// row still sits in the hot table or an attached partition.
const sessionDigestBackfillSelectSQL = `
	SELECT t.id, t.tenant_id, t.session_id, t.turn_no, t.request_id, t.ts,
	       COALESCE(t.prompt_tokens, 0), COALESCE(t.completion_tokens, 0),
	       COALESCE(t.cache_read_tokens, 0), COALESCE(t.cache_write_tokens, 0),
	       COALESCE(t.cost_usd, 0), COALESCE(t.latency_ms, 0),
	       COALESCE(t.status_code, 0), COALESCE(t.success, FALSE), COALESCE(t.error_kind, ''),
	       COALESCE(t.injection_verdict, ''), COALESCE(t.output_verdict, ''),
	       COALESCE(t.compression_applied, FALSE), COALESCE(t.compression_tokens_saved, 0),
	       b.request_delta, b.response_delta
	FROM public.session_turns_with_current_month t
	LEFT JOIN public.session_bodies_unified b
	  ON b.tenant_id = t.tenant_id AND b.session_id = t.session_id
	 AND b.turn_no = t.turn_no AND b.request_id = t.request_id
	WHERE t.digest IS NULL
	ORDER BY t.ts, t.id
	LIMIT $1`

// processBatch claims up to batchSize NULL-digest turns in one transaction,
// rebuilds each envelope, and writes it back. The UPDATE targets both the hot
// table and the partitioned parent: promote_session_turns_hot_to_partition is
// a move (DELETE + INSERT in one tx, id preserved), so exactly one of the two
// guarded UPDATEs matches; the digest IS NULL predicate makes the write
// idempotent under concurrent replicas.
func (b *sessionDigestBackfill) processBatch(ctx context.Context) (int, error) {
	tx, err := b.db.Begin(ctx)
	if err != nil {
		digestBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		digestBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("set super-admin role GUC: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		digestBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("set RLS bypass GUC: %w", err)
	}

	rows, err := tx.Query(ctx, sessionDigestBackfillSelectSQL, b.batchSize)
	if err != nil {
		digestBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("select candidates: %w", err)
	}
	batch := make([]digestBackfillRow, 0, b.batchSize)
	for rows.Next() {
		var r digestBackfillRow
		if err := rows.Scan(&r.id, &r.tenantID, &r.sessionID, &r.turnNo, &r.requestID, &r.ts,
			&r.promptTokens, &r.completionTokens, &r.cacheReadTokens, &r.cacheWriteTokens,
			&r.costUSD, &r.latencyMs, &r.statusCode, &r.success, &r.errorKind,
			&r.injectionVerdict, &r.outputVerdict, &r.compressionApplied, &r.compressionTokensSaved,
			&r.requestDelta, &r.responseDelta); err != nil {
			rows.Close()
			digestBackfillTotal.WithLabelValues("error").Inc()
			return 0, fmt.Errorf("scan candidate: %w", err)
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		digestBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("iterate candidates: %w", err)
	}
	rows.Close()
	if len(batch) == 0 {
		return 0, nil
	}

	start := time.Now()
	stopped := false
	for i := range batch {
		// Throttle before every write (the limiter's buffered tick lets the
		// first row of a fresh batch go through immediately).
		if !b.wait(ctx) {
			stopped = true
			break
		}
		if err := b.backfillRow(ctx, tx, &batch[i]); err != nil {
			digestBackfillTotal.WithLabelValues("error").Inc()
			// A failed UPDATE poisons the transaction; abort the batch. The
			// rows stay NULL and are retried on the next drain.
			return len(batch), fmt.Errorf("backfill turn %s/%d: %w", batch[i].requestID, batch[i].turnNo, err)
		}
	}
	if stopped {
		// Shutdown mid-batch: commit what completed, leave the rest NULL.
		if err := tx.Commit(ctx); err != nil {
			return len(batch), fmt.Errorf("commit partial batch: %w", err)
		}
		return len(batch), nil
	}
	if err := tx.Commit(ctx); err != nil {
		digestBackfillTotal.WithLabelValues("error").Inc()
		return len(batch), fmt.Errorf("commit: %w", err)
	}
	digestBackfillDuration.Observe(time.Since(start).Seconds())
	return len(batch), nil
}

// wait consumes one limiter tick. Returns false when the job must stop.
func (b *sessionDigestBackfill) wait(ctx context.Context) bool {
	if b.limiter == nil {
		// Unthrottled mode still honours cancellation.
		select {
		case <-ctx.Done():
			return false
		case <-b.stopCh:
			return false
		default:
			return true
		}
	}
	select {
	case <-ctx.Done():
		return false
	case <-b.stopCh:
		return false
	case <-b.limiter.C:
		return true
	}
}

// backfillRow rebuilds and persists one turn's digest envelope. The UPDATE
// hits hot and parent in a fixed order; the guards (id + tenant + request +
// digest IS NULL) make exactly one of them a no-op for any given row.
func (b *sessionDigestBackfill) backfillRow(ctx context.Context, tx pgx.Tx, r *digestBackfillRow) error {
	envelope := sessiondigest.Build(
		decodeDigestBackfillBody(r.requestID, r.requestDelta),
		decodeDigestBackfillBody(r.requestID, r.responseDelta),
		digestBackfillMeta(r),
		digestBackfillGovernance(r),
		r.ts,
	)
	digestJSON := "null"
	if envelope != nil {
		raw, err := sessiondigest.Marshal(envelope)
		if err != nil {
			return fmt.Errorf("marshal digest: %w", err)
		}
		digestJSON = string(raw)
	}

	affected, err := execDigestUpdate(ctx, tx, "public.session_turns_hot", r, digestJSON)
	if err != nil {
		return err
	}
	if affected == 0 {
		if affected, err = execDigestUpdate(ctx, tx, "public.session_turns", r, digestJSON); err != nil {
			return err
		}
	}
	if affected > 0 {
		if envelope == nil {
			digestBackfillTotal.WithLabelValues("empty").Inc()
		} else {
			digestBackfillTotal.WithLabelValues("filled").Inc()
		}
		return nil
	}
	// Both UPDATEs matched zero rows: a concurrent replica filled the digest
	// (or promote moved the row between our SELECT and UPDATE). Either way
	// the row no longer needs this replica's work.
	digestBackfillTotal.WithLabelValues("skipped").Inc()
	return nil
}

func execDigestUpdate(ctx context.Context, tx pgx.Tx, table string, r *digestBackfillRow, digestJSON string) (int64, error) {
	tag, err := tx.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET digest = $3::jsonb WHERE id = $1 AND tenant_id = $2 AND request_id = $4 AND digest IS NULL", table),
		r.id, r.tenantID, digestJSON, r.requestID)
	if err != nil {
		return 0, fmt.Errorf("update %s: %w", table, err)
	}
	return tag.RowsAffected(), nil
}

// digestBackfillMeta mirrors session_writer_v2.Write's digestMeta key-for-key
// (including presence with zero values), so Build's hasMetrics branch behaves
// identically to the live write path and every row yields a non-nil envelope.
func digestBackfillMeta(r *digestBackfillRow) map[string]any {
	return map[string]any{
		"prompt_tokens":      r.promptTokens,
		"completion_tokens":  r.completionTokens,
		"cost_usd":           r.costUSD,
		"latency_ms":         r.latencyMs,
		"status_code":        r.statusCode,
		"success":            r.success,
		"error_kind":         r.errorKind,
		"cache_read_tokens":  r.cacheReadTokens,
		"cache_write_tokens": r.cacheWriteTokens,
	}
}

// digestBackfillGovernance mirrors session_writer_v2.Write's digestGovernance.
func digestBackfillGovernance(r *digestBackfillRow) map[string]any {
	return map[string]any{
		"injection_verdict":        r.injectionVerdict,
		"output_verdict":           r.outputVerdict,
		"compression_applied":      r.compressionApplied,
		"compression_tokens_saved": r.compressionTokensSaved,
	}
}

// decodeDigestBackfillBody turns a stored jsonb delta into the any-shaped
// body sessiondigest.Build walks. NULL/empty yields nil; corrupt JSON also
// yields nil (logged) so one bad row cannot abort the batch — the row still
// gets a metrics-only envelope instead of staying NULL forever.
func decodeDigestBackfillBody(requestID string, raw []byte) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		slog.Warn("session digest backfill: undecodable body delta, digest built without it",
			"request_id", requestID, "bytes", len(raw), "error", err.Error())
		return nil
	}
	return body
}

// compile-time assertion that the production pool satisfies the seam.
var _ digestBackfillDB = (*pgxpool.Pool)(nil)
