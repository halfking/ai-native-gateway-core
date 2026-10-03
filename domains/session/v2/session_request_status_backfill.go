// Package v2 — session_request_status_backfill.go
//
// request_status 存量回填（migration 823 之后，2026-10-04）。
//
// 背景：migration 823 给 session_turns / session_turns_hot 加了 request_status
// TEXT 列，写入路径（turn_writer / sessionv2mirror）已透传，所以**新增行 100%
// 正确**。但迁移明确「不回填」，历史 1,688,629 行全部为 NULL。而这批标签的
// 唯一真源是 v1 request_logs——它一旦退役，rate_limited（394,614 行）与
// success/failure/in_progress 的区分就永久不可恢复（ResolveRequestStatus
// 永远不会返回 rate_limited，无法从 success/error_kind 推导）。
//
// 本 job 做的事：把 request_logs.request_status 复制到
// session_turns.request_status。仅此一件事——不重算、不猜测、不改其他列。
//
// 为什么必须是后台 job 而不是迁移：
//
//	session_turns 全部分区 6.79 GB，实测 20,000 行 UPDATE 耗时 26.3s ⇒ 全量
//	≈33 分钟，并写 152 万个行版本。放进迁移会把启动路径拖垮。
//
// join 键只用 request_id：
//
//	实测 request_logs.request_id 唯一（2,175,834 行 = 2,175,834 distinct），
//	所以 join 是 1:1。**不要**再加 ts 之类的冗余条件——那只会让本可命中的行
//	落空。实测 1,520,630 / 1,688,629（90.04%）行在 request_logs 里有孪生行。
//
// 剩下 ~168,106 行没有 v1 孪生（v1 停写窗口的产物），结构性无法回填，只能靠
// 前向写入路径覆盖。本 job 不试图为它们编造标签——把 NULL 猜成 'success' 比
// 留着 NULL 更危险（NULL 可被审计识别为「未知」，错值不可）。
//
// 正确性模型：
//   - 幂等：回写 UPDATE 一律带 `AND request_status IS NULL` 守卫。重复调度、
//     多副本并发、job 重启都只会让其中一个 UPDATE 生效。
//   - 不用 session_turns_hot：实测 hot 侧 111 行 request_status IS NULL，
//     孪生行 0 个（hot 行都是 823 之后的新行，v1 早已停写）。且
//     promote_session_turns_hot_to_partition 按列名派生清单 INSERT，新列自动
//     流经 promote。为 0 收益额外扫一张表不划算。
//   - 分区裁剪：候选 SELECT 与 UPDATE 都带 partition_date，让 6 个分区各走
//     自己的 pkey（session_turns_pkey = (id, partition_date)），不锁全表。
//   - 限速：批内按 row 计费消费 limiter tick，ratePerSec 的语义与
//     session_digest_backfill 一致（rows/s），只是把 N 行的写合并成 1 条
//     UPDATE 语句。默认 100 rows/s ⇒ 1.5M 行约 4.2 小时。
//   - 游标：request_status IS NULL 上**没有**部分索引（不同于 digest 有
//     idx_session_turns_digest_null），所以候选 SELECT 用 id 键集分页 +
//     ORDER BY id，单调前进，避免每批从头重扫。
//
// 退役终态（request_logs 被删）：
//
//	request_logs 退役后本 job 无源可抄，属**正常终态**而非故障。probe 检测到
//	to_regclass('public.request_logs') IS NULL 时，记一条 Info 日志 + 一个
//	retired 计数，然后**退出 run 循环**——不再重试、不再每 tick 刷 ERROR。
//	同理，823 未跑（session_turns 无该列）也安静退出。
package v2

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// requestStatusBackfillTotal 按低基数 result 分桶统计回填结果：
//
//	filled  — request_status 写回成功（UPDATE RowsAffected>0）
//	skipped — UPDATE 0 行（并发副本已填 / 行在 SELECT 与 UPDATE 之间被 promote）
//	error   — 批级 DB 错误（整批回滚，行保持 NULL 等待下轮重试）
//	retired — 源已退役或目标列不存在，job 安静退出（终态，非故障）
var requestStatusBackfillTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_session_turn_request_status_backfill_total",
		Help: "Session turns whose NULL request_status was copied from the retired request_logs (label = outcome)",
	},
	[]string{"result"},
)

// requestStatusBackfillDuration 观测非空批的处理耗时（SELECT + 限速等待 +
// UPDATE + COMMIT）。空批（空闲探测）不计入。
var requestStatusBackfillDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "llmgw_session_turn_request_status_backfill_duration_seconds",
		Help:    "Wall-clock duration of a non-empty session turn request_status backfill batch (SELECT through COMMIT)",
		Buckets: prometheus.DefBuckets,
	},
)

const (
	sessionRequestStatusBackfillDefaultInterval = 30 * time.Second
	sessionRequestStatusBackfillDefaultBatch    = 100
	sessionRequestStatusBackfillDefaultRate     = 100
	// sessionRequestStatusBackfillMaxIdle caps the exponential idle backoff so
	// a late-arriving v1 twin is still picked up within 30 minutes.
	sessionRequestStatusBackfillMaxIdle = 30 * time.Minute
	// sessionRequestStatusBackfillBatchTimeout bounds one batch transaction so
	// a stuck DB cannot wedge the goroutine past Stop's doneCh wait.
	sessionRequestStatusBackfillBatchTimeout = 5 * time.Minute
	// sessionRequestStatusBackfillMaxRate guards against a fat-fingered env
	// value turning the job into an unthrottled UPDATE storm.
	sessionRequestStatusBackfillMaxRate = 1000
)

// errRequestStatusSourceRetired marks the terminal state: request_logs is gone
// (retirement landed) or session_turns lacks request_status (migration 823
// absent). Neither is a retryable failure, so run() stops instead of looping.
var errRequestStatusSourceRetired = errors.New("request_status backfill source retired")

// requestStatusBackfillDB is the minimal pool surface the backfill needs.
// Declared as an interface so unit tests inject pgxmock (same seam as
// digestBackfillDB).
type requestStatusBackfillDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// requestStatusBackfillRow is one candidate turn: the id/partition_date pair
// that addresses the row in the partitioned parent, plus the request_id whose
// twin row in request_logs carries the authoritative label.
type requestStatusBackfillRow struct {
	id            int64
	partitionDate time.Time
	tenantID      string
	requestID     string
}

// sessionRequestStatusBackfill is the background job that drains NULL
// request_status rows by copying the label out of request_logs.
type sessionRequestStatusBackfill struct {
	db         requestStatusBackfillDB
	interval   time.Duration
	batchSize  int
	ratePerSec int
	limiter    *time.Ticker
	stopCh     chan struct{}
	doneCh     chan struct{}
	mu         sync.Mutex
	started    bool
	stopped    bool
	// cursor is the id keyset watermark within the current drain; 0 = start of
	// the table. Reset per drain so a short batch (candidates exhausted) always
	// terminates the loop and the next drain re-scans from the top.
	cursor int64
}

func newSessionRequestStatusBackfill(db *pgxpool.Pool, interval time.Duration, batchSize, ratePerSec int) *sessionRequestStatusBackfill {
	return newSessionRequestStatusBackfillForTest(db, interval, batchSize, ratePerSec)
}

// newSessionRequestStatusBackfillForTest is the interface-accepting seam used
// by unit tests with pgxmock. Production callers must use
// StartSessionRequestStatusBackfill (concrete *pgxpool.Pool enforces the real
// pool).
func newSessionRequestStatusBackfillForTest(db requestStatusBackfillDB, interval time.Duration, batchSize, ratePerSec int) *sessionRequestStatusBackfill {
	if interval <= 0 {
		interval = sessionRequestStatusBackfillDefaultInterval
	}
	if batchSize <= 0 {
		batchSize = sessionRequestStatusBackfillDefaultBatch
	}
	if ratePerSec < 0 {
		ratePerSec = 0 // 0 = unlimited (tests / explicit opt-out of throttling)
	}
	if ratePerSec > sessionRequestStatusBackfillMaxRate {
		ratePerSec = sessionRequestStatusBackfillMaxRate
	}
	return &sessionRequestStatusBackfill{
		db:         db,
		interval:   interval,
		batchSize:  batchSize,
		ratePerSec: ratePerSec,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// StartSessionRequestStatusBackfill starts the backfill job in the background.
// Idempotent: a second Start on the same handle is a no-op. Returns the
// started handle so the shutdown path can drain it.
func StartSessionRequestStatusBackfill(ctx context.Context, pool *pgxpool.Pool, batchSize, ratePerSec int) *sessionRequestStatusBackfill {
	b := newSessionRequestStatusBackfill(pool, 0, batchSize, ratePerSec)
	b.Start(ctx)
	return b
}

// Start launches the backfill goroutine.
func (b *sessionRequestStatusBackfill) Start(ctx context.Context) {
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
func (b *sessionRequestStatusBackfill) Stop() {
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

func (b *sessionRequestStatusBackfill) run(ctx context.Context) {
	defer close(b.doneCh)
	if b.limiter != nil {
		defer b.limiter.Stop()
	}
	if !isUsablePool(b.db) {
		// Same defensive posture as the digest backfill: production wires a real
		// pool, but a nil/typed-nil (tests) must not panic the goroutine.
		slog.Warn("session request_status backfill: nil pool, job not started")
		return
	}

	wait := b.interval
	for {
		// Probe before every drain: this is what makes the retirement terminal
		// state quiet. Once the source is gone we stop retrying entirely.
		available, err := b.sourceAvailable(ctx)
		if err != nil {
			slog.Warn("session request_status backfill: source probe failed, will retry", "error", err)
		} else if !available {
			requestStatusBackfillTotal.WithLabelValues("retired").Inc()
			slog.Info("session request_status backfill: source retired or target column absent, backfill stopped (terminal state, not an error)")
			return
		}

		processed, err := b.drain(ctx)
		if errors.Is(err, errRequestStatusSourceRetired) {
			requestStatusBackfillTotal.WithLabelValues("retired").Inc()
			slog.Info("session request_status backfill: source retired mid-drain, backfill stopped (terminal state, not an error)")
			return
		}
		if err != nil {
			slog.Error("session request_status backfill: drain error", "error", err)
		}
		if processed > 0 {
			// Backlog found: loop immediately and keep draining at the
			// throttled rate.
			wait = b.interval
			continue
		}
		// Idle: exponential backoff so the empty-candidate SELECT (request_status
		// IS NULL has no partial index, so it walks the pkey index until it
		// proves there is nothing left) does not run every interval forever.
		if wait > sessionRequestStatusBackfillMaxIdle {
			wait = sessionRequestStatusBackfillMaxIdle
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

// sourceAvailable reports whether there is still something to copy FROM and TO:
// request_logs must exist and session_turns must carry request_status (823).
// A false return is the terminal retirement state; an error is retryable.
func (b *sessionRequestStatusBackfill) sourceAvailable(ctx context.Context) (bool, error) {
	tx, err := b.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin source probe: %w", err)
	}
	defer tx.Rollback(ctx)
	var hasSource, hasTarget bool
	// to_regclass (not ::regclass) so a dropped table yields NULL instead of
	// raising — the whole point is to detect absence without an ERROR.
	if err := tx.QueryRow(ctx, sessionRequestStatusSourceProbeSQL).Scan(&hasSource, &hasTarget); err != nil {
		return false, fmt.Errorf("probe source/target: %w", err)
	}
	return hasSource && hasTarget, nil
}

// drain processes consecutive batches until the candidates are exhausted, a
// batch errors, or stop/ctx fires. Returns the number of candidate rows seen
// (selected), so an empty drain is distinguishable from a stalled one.
func (b *sessionRequestStatusBackfill) drain(ctx context.Context) (int, error) {
	b.cursor = 0
	total := 0
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-b.stopCh:
			return total, nil
		default:
		}
		batchCtx, cancel := context.WithTimeout(ctx, sessionRequestStatusBackfillBatchTimeout)
		selected, err := b.processBatch(batchCtx)
		cancel()
		total += selected
		if errors.Is(err, errRequestStatusSourceRetired) {
			return total, err
		}
		if err != nil {
			return total, err
		}
		// A short batch means the joinable candidate set is drained. Rows with
		// no v1 twin are never candidates, so they never keep the loop alive.
		if selected < b.batchSize {
			return total, nil
		}
	}
}

// sessionRequestStatusSourceProbeSQL checks both ends of the copy. to_regclass
// yields NULL for an absent relation instead of raising, which is what turns
// retirement into a quiet exit rather than an ERROR per tick.
const sessionRequestStatusSourceProbeSQL = `
	SELECT to_regclass('public.request_logs') IS NOT NULL,
	       EXISTS (SELECT 1 FROM pg_attribute
	                WHERE attrelid = to_regclass('public.session_turns')
	                  AND attname = 'request_status'
	                  AND NOT attisdropped)`

// sessionRequestStatusCandidateSQL selects the next batch of joinable NULL
// turns. Keyset pagination on id (monotonic within a drain) keeps each batch
// near O(batch) instead of rescanning 1.69M rows, because request_status IS NULL
// has no partial index. partition_date rides along so the UPDATE prunes to the
// owning partition via session_turns_pkey.
//
// The join is on request_id ONLY — request_logs.request_id is unique (measured
// 2,175,834 = 2,175,834 distinct), so the join cannot fan out. request_status
// IS NOT NULL on the source side keeps the 46 NULL-label v1 rows out of the
// candidate set (writing NULL would be a no-op that never satisfies the guard
// and would spin the cursor forever).
const sessionRequestStatusCandidateSQL = `
	SELECT t.id, t.partition_date, t.tenant_id, t.request_id
	FROM public.session_turns t
	JOIN public.request_logs l ON l.request_id = t.request_id
	WHERE t.id > $1
	  AND t.request_status IS NULL
	  AND l.request_status IS NOT NULL
	ORDER BY t.id
	LIMIT $2`

// processBatch claims up to batchSize joinable NULL turns in one transaction and
// copies their request_status from request_logs in a single guarded UPDATE.
// The guard makes it idempotent under concurrent replicas and across restarts.
func (b *sessionRequestStatusBackfill) processBatch(ctx context.Context) (int, error) {
	tx, err := b.db.Begin(ctx)
	if err != nil {
		requestStatusBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		requestStatusBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("set super-admin role GUC: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		requestStatusBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("set RLS bypass GUC: %w", err)
	}

	rows, err := tx.Query(ctx, sessionRequestStatusCandidateSQL, b.cursor, b.batchSize)
	if err != nil {
		// Relation vanished between probe and query: re-probe, and if the source
		// is genuinely gone treat it as the terminal state, not an error.
		if b.retiredMidRun(ctx) {
			return 0, errRequestStatusSourceRetired
		}
		requestStatusBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("select candidates: %w", err)
	}
	// panic 路径下 rows 无人关闭（下方显式 Close 只覆盖 return 路径）；defer 与
	// 显式 Close 幂等共存。
	defer rows.Close()
	batch := make([]requestStatusBackfillRow, 0, b.batchSize)
	for rows.Next() {
		var r requestStatusBackfillRow
		if err := rows.Scan(&r.id, &r.partitionDate, &r.tenantID, &r.requestID); err != nil {
			rows.Close()
			// A relation dropped mid-iteration surfaces here (pgx defers planning
			// for some plans), so this path needs the same retirement check.
			if b.retiredMidRun(ctx) {
				return 0, errRequestStatusSourceRetired
			}
			requestStatusBackfillTotal.WithLabelValues("error").Inc()
			return 0, fmt.Errorf("scan candidate: %w", err)
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		if b.retiredMidRun(ctx) {
			return 0, errRequestStatusSourceRetired
		}
		requestStatusBackfillTotal.WithLabelValues("error").Inc()
		return 0, fmt.Errorf("iterate candidates: %w", err)
	}
	rows.Close()
	if len(batch) == 0 {
		return 0, nil
	}
	// Advance the keyset watermark to the last id claimed so the next batch
	// resumes here. Guarded UPDATEs may still leave a row NULL (concurrent
	// replica), which is fine — it is not a candidate for anybody anymore.
	b.cursor = batch[len(batch)-1].id

	start := time.Now()
	stopped := false
	for range batch {
		// Throttle before the batch write (the limiter's buffered tick lets the
		// first batch go through immediately). One tick per ROW keeps ratePerSec
		// meaning rows/s, identical to session_digest_backfill, while collapsing
		// N writes into 1 statement.
		if !b.wait(ctx) {
			stopped = true
			break
		}
	}
	if stopped {
		// Shutdown mid-throttle: nothing was written yet, leave the rows NULL.
		return len(batch), nil
	}

	affected, err := execRequestStatusUpdate(ctx, tx, batch)
	if err != nil {
		if b.retiredMidRun(ctx) {
			return len(batch), errRequestStatusSourceRetired
		}
		requestStatusBackfillTotal.WithLabelValues("error").Inc()
		return len(batch), fmt.Errorf("update request_status: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		requestStatusBackfillTotal.WithLabelValues("error").Inc()
		return len(batch), fmt.Errorf("commit: %w", err)
	}
	if affected > 0 {
		requestStatusBackfillTotal.WithLabelValues("filled").Add(float64(affected))
	}
	if skipped := len(batch) - int(affected); skipped > 0 {
		// Rows the guard rejected: a concurrent replica filled them (or the row
		// moved between our SELECT and UPDATE). Either way this replica has no
		// further work on them.
		requestStatusBackfillTotal.WithLabelValues("skipped").Add(float64(skipped))
	}
	requestStatusBackfillDuration.Observe(time.Since(start).Seconds())
	return len(batch), nil
}

// retiredMidRun re-probes after a batch-level failure. Used to distinguish
// "the source was retired" (terminal, quiet) from "the DB hiccuped" (retry).
func (b *sessionRequestStatusBackfill) retiredMidRun(ctx context.Context) bool {
	available, err := b.sourceAvailable(ctx)
	return err == nil && !available
}

// execRequestStatusUpdate issues ONE guarded UPDATE for the whole batch.
func execRequestStatusUpdate(ctx context.Context, tx pgx.Tx, batch []requestStatusBackfillRow) (int64, error) {
	if len(batch) == 0 {
		return 0, nil
	}
	sql, args := buildRequestStatusUpdateSQL(batch)
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("batch update: %w", err)
	}
	return tag.RowsAffected(), nil
}

// buildRequestStatusUpdateSQL renders the guarded batch UPDATE and its bind
// args. Split out from the Exec so the SQL contract (idempotence guard, join
// key, partition pruning) is directly assertable in tests — the regex matchers
// pgxmock uses would only prove *some* UPDATE was issued.
//
// The FROM-VALUES list carries each row's (id, partition_date) so the
// partitioned parent prunes to the owning partition's pkey
// (session_turns_pkey = (id, partition_date)) instead of touching all six.
// The label itself comes from request_logs (l.request_status), never from the
// VALUES tuple, so the copy has exactly one source of truth.
func buildRequestStatusUpdateSQL(batch []requestStatusBackfillRow) (string, []any) {
	var sb strings.Builder
	args := make([]any, 0, len(batch)*3)
	sb.WriteString("UPDATE public.session_turns t SET request_status = l.request_status FROM (VALUES ")
	for i, r := range batch {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 3
		fmt.Fprintf(&sb, "($%d::bigint, $%d::date, $%d::text)", base+1, base+2, base+3)
		args = append(args, r.id, r.partitionDate, r.requestID)
	}
	sb.WriteString(") AS v(id, partition_date, request_id) ")
	sb.WriteString("JOIN public.request_logs l ON l.request_id = v.request_id ")
	sb.WriteString("WHERE t.id = v.id AND t.partition_date = v.partition_date ")
	sb.WriteString("AND t.request_status IS NULL AND l.request_status IS NOT NULL")
	return sb.String(), args
}

// wait consumes one limiter tick. Returns false when the job must stop.
func (b *sessionRequestStatusBackfill) wait(ctx context.Context) bool {
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

// compile-time assertion that the production pool satisfies the seam.
var _ requestStatusBackfillDB = (*pgxpool.Pool)(nil)
