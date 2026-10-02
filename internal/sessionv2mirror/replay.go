// Package sessionv2mirror — replay.go
//
// session_mirror_outbox 重放器（迁移 712，spec §12 GAP 2 闭环的消化侧）。
// 骨架对齐 domains/session/v2 的 session_aggregate_outbox_reaper（630）：
// FOR UPDATE SKIP LOCKED 多副本协作、claim/replay 分离事务（claim lease 兜
// 崩溃孤儿）、指数退避、超限 dead 落 slog.Error + Prometheus counter。
//
// 与 630 的差异：成功即 DELETE 行（无 'done' 态）——done 行没有重放价值，
// 只会拖累 request_id 唯一约束的登记幂等；退避失败行回到 'pending'。
//
// 重放语义与实时 hook 逐 gate 一致（entryToProcessedRequest 同一桥接）：
// 非终态行、内部回环行按 hook 行为跳过并删行（它们本就不该进 V2）；合成
// 会话（D4）按同款规则标 client_type='system'。重放不受 shadow_write 实时
// 开关控制（欠账行本就是应该写的历史），由独立开关
// sessions_v2.mirror_outbox_replay 控制（默认开）。
package sessionv2mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	mirrorReplayDefaultInterval = 30 * time.Second
	mirrorReplayDefaultBatch    = 250
	mirrorReplayDefaultMaxAtts  = 10
	// mirrorReplayMaxBackoff caps the exponential schedule (30s base).
	mirrorReplayMaxBackoff = 1 * time.Hour
	// mirrorReplayClaimLease bounds how long a 'claimed' row may stay in
	// that state before another tick re-claims it — the crash-orphan safety
	// net (same rationale as 630's sessionOutboxClaimLease).
	mirrorReplayClaimLease = 5 * time.Minute
	// mirrorReplayWorkersCap caps the parallel-claim fan-out so a single
	// mirror stall cannot exhaust the DB pool. NumCPU() on 32-core hosts is
	// far more than the 4 workers the reaper's batch actually needs — each
	// worker holds a pool conn for the duration of claimBatch + replayOne.
	mirrorReplayWorkersCap = 4
	// mirrorReplayMaxAttsFloor / Ceiling clamp the hot-reload setting so an
	// operator cannot dial the dead-letter budget past the documented safe
	// range (5-30). The ceiling is the audit-handoff hard guardrail; the
	// floor prevents a typo from dead-lettering every row on first failure.
	mirrorReplayMaxAttsFloor   = 5
	mirrorReplayMaxAttsCeiling = 30
)

// mirrorReplayWriteBudgetMs matches the live shadow-write budget so a
// replayed turn and a live turn have identical DB-side expectations.
const mirrorReplayWriteBudgetMs = defaultShadowWriteTimeoutMs

// currentMaxAtts reads the hot-reload setting sessions_v2.mirror_outbox_max_attempts,
// falling back to def when settings.Global is nil (unit-test binary) or the key is
// unregistered. Clamps to [mirrorReplayMaxAttsFloor, mirrorReplayMaxAttsCeiling] so an
// operator typo cannot dead-letter every row on first failure or never dead-letter at
// all. The clamp bounds match the spec's hard guardrails (audit handoff §7).
func currentMaxAtts(def int) int {
	v := settings.GetPlatformInt("sessions_v2.mirror_outbox_max_attempts", def)
	if v < mirrorReplayMaxAttsFloor {
		return mirrorReplayMaxAttsFloor
	}
	if v > mirrorReplayMaxAttsCeiling {
		return mirrorReplayMaxAttsCeiling
	}
	return v
}

// mirrorReplayWorkers returns the parallel-claim fan-out: min(NumCPU, cap).
// Floor of 1 keeps the reaper functional on machines reporting 0 cores
// (cgroup isolation); cap of 4 keeps DB connection pressure bounded — the
// reaper's claimBatch holds one pool conn per worker for the duration of
// the batch.
func mirrorReplayWorkers() int {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	if n > mirrorReplayWorkersCap {
		n = mirrorReplayWorkersCap
	}
	return n
}

var (
	mirrorOutboxPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "session_v2_mirror_outbox_pending",
		Help: "session_mirror_outbox rows awaiting replay (status='pending'). Persistent growth means the mirror is losing rows faster than the reaper drains them (spec §12 GAP 2).",
	})
	mirrorReplayTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "session_mirror_outbox_replays_total",
			Help: "session_mirror_outbox replay outcomes (ok|retry|dead|skipped). The dead label is the per-attempt dead-letter rate; session_mirror_outbox_dead_total is the aggregate dead-letter event count and the alerting-friendly top-level signal.",
		},
		[]string{"result"}, // ok | retry | dead | skipped
	)
	mirrorReplayDeadTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "session_mirror_outbox_dead_total",
		Help: "session_mirror_outbox rows permanently dead-lettered after exhausting mirror_outbox_max_attempts retries. Sustained growth means manual reconciliation is required (spec §12 GAP 2 dead contract).",
	})
)

// replayDB is the minimal pool surface the reaper needs.
type replayDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// setBypassGUCs lifts tenant isolation for the current transaction (R29
// audit 2026-09-15): session_mirror_outbox is ENABLE+FORCE RLS (712), so
// even the table owner is subject to the tenant policy — without these GUCs
// every non-'default'-tenant row is invisible to the reaper and re-registration
// INSERTs are rejected by WITH CHECK. Same contract as the 630 reaper
// (domains/session/v2/session_aggregate_outbox_reaper.go). is_local=true keeps
// the pooled connection clean after commit.
func setBypassGUCs(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('app.current_role', 'super_admin', true), set_config('app.bypass_rls', 'true', true)`)
	if err != nil {
		return fmt.Errorf("set RLS bypass GUCs: %w", err)
	}
	return nil
}

// execBypass runs one statement against the FORCE-RLS outbox inside a
// transaction-scoped bypass (single-statement pool Exec cannot carry the
// GUCs; a session-scoped set_config would leak across pool borrowers).
func execBypass(ctx context.Context, db replayDB, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	//nolint:errcheck // rollback is the safe default after commit too
	defer tx.Rollback(ctx)
	if err := setBypassGUCs(ctx, tx); err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return tag, tx.Commit(ctx)
}

// MirrorOutboxReaper drains session_mirror_outbox rows.
type MirrorOutboxReaper struct {
	db        replayDB
	writer    V2Writer
	interval  time.Duration
	batchSize int
	maxAtts   int
	workers   int
	stopCh    chan struct{}
	doneCh    chan struct{}
	mu        sync.Mutex
	started   bool
	stopped   bool
}

// StartMirrorOutboxReaper starts the reaper in the background. Returns nil
// when there is no pool or no writer (nothing to replay into). Idempotent.
func StartMirrorOutboxReaper(ctx context.Context, pool *pgxpool.Pool, writer V2Writer) *MirrorOutboxReaper {
	if pool == nil || writer == nil {
		return nil
	}
	r := &MirrorOutboxReaper{
		db:        pool,
		writer:    writer,
		interval:  mirrorReplayDefaultInterval,
		batchSize: mirrorReplayDefaultBatch,
		maxAtts:   currentMaxAtts(mirrorReplayDefaultMaxAtts),
		workers:   mirrorReplayWorkers(),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	r.Start(ctx)
	return r
}

// Start launches the reaper goroutine.
func (r *MirrorOutboxReaper) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go r.run(ctx)
}

// Stop halts the reaper and waits for the in-flight tick to return.
func (r *MirrorOutboxReaper) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.stopped || !r.started {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	close(r.stopCh)
	r.mu.Unlock()
	<-r.doneCh
}

func (r *MirrorOutboxReaper) run(ctx context.Context) {
	defer close(r.doneCh)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	// time.NewTicker 首跳在 interval（30s）后而非立即；重启后重启前排出的
	// 积压至多延迟一个 interval 才开始排空。若要立即消化，可在此处先手动
	// tick 一次（M-6③ 裁决：30s 延迟可接受，只修注释不改行为）。
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			if !settings.GetPlatformBool("sessions_v2.mirror_outbox_replay", true) {
				continue
			}
			if err := r.tick(ctx); err != nil {
				slog.Warn("sessionv2mirror: outbox replay tick failed", "error", err)
			}
		}
	}
}

// claimRow is one row claimed for replay.
type claimRow struct {
	id        int64
	requestID string
	sessionID string
	payload   []byte
	attempts  int
}

func (r *MirrorOutboxReaper) tick(ctx context.Context) error {
	// 1. Recover crash orphans: rows stuck in 'claimed' beyond the lease go
	// back to 'pending' (single statement, no claim contention).
	if _, err := execBypass(ctx, r.db, `
		UPDATE public.session_mirror_outbox
		SET status = 'pending', claimed_at = NULL, updated_at = NOW()
		WHERE status = 'claimed' AND claimed_at < NOW() - $1::interval
	`, mirrorReplayClaimLease); err != nil {
		return err
	}

	// 2. Claim and replay in parallel. Each worker independently calls
	// claimBatch (its own tx with FOR UPDATE SKIP LOCKED) so workers never
	// double-claim the same row. A worker stops when its claim returns
	// fewer than batchSize rows — the table is drained.
	workers := r.workers
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// M-6①：Go 里任意 goroutine 的未恢复 panic 会击穿整个进程；
			// replay 链路（claimBatch→replayOne→writer.Write→桥接转换）
			// 任一环 panic 只记日志不坠机，行保持 claimed 由 lease 孤儿
			// 回收兜底回 pending。
			defer func() {
				if p := recover(); p != nil {
					slog.Error("sessionv2mirror: outbox replay worker panic",
						"panic", p, "stack", string(debug.Stack()))
				}
			}()
			r.drainWorker(ctx)
		}()
	}
	wg.Wait()

	// 3. Refresh the depth gauge (also covers hook-side registrations).
	return r.refreshGauge(ctx)
}

// drainWorker loops claimBatch → replayOne until no rows remain. FOR UPDATE
// SKIP LOCKED makes the loop safe under N workers; a short batch signals
// the table is drained.
// drainEmptyConfirmations 是「本 worker 连续几次拿到空批次后才认定排空」的
// 次数。
//
// 为什么不能只看「短批次」：claimBatch 用的是 FOR UPDATE SKIP LOCKED，短批次
// 只说明「本次快照里未被别的事务锁住、且够 LIMIT 的行不足」，不等于表已排空。
// 并行 drain 时兄弟 worker 的 claim 事务尚未提交、hook 侧 INSERT 与本轮并发，
// 都会让某个 worker 读到短批次。若据此退出，全部 worker 可能集体提前收工，
// 剩余行要等下一个 tick（本实现 30s）才被捡起——高写入量下出箱堆积速度可能
// 反超排空速度，与本 worker 的初衷相反。
//
// 改成「连续 N 次空批次」后，单次因锁竞争/并发写入造成的短读不会让 worker
// 提前退出；真正的排空（表确实空了）只需要多跑 N-1 次空查询，代价可忽略
// （每 tick 每 worker 多 1~2 次极轻量的 SELECT ... FOR UPDATE SKIP LOCKED）。
const drainEmptyConfirmations = 2

func (r *MirrorOutboxReaper) drainWorker(ctx context.Context) {
	emptyStreak := 0
	for {
		if ctx.Err() != nil {
			return
		}
		// M-6②：ctx 是 Background（永不过期），停机信号只走 stopCh；
		// 不查它则 Stop() 后 run 的 wg.Wait 会被 drain-until-empty 拖住
		// （大积压 + 后台 ctx 时优雅停机无界阻塞）。中途退出时已 claim
		// 的行由 lease 孤儿回收兜底。
		select {
		case <-r.stopCh:
			return
		default:
		}
		rows, err := r.claimBatch(ctx)
		if err != nil {
			slog.Warn("sessionv2mirror: outbox claimBatch failed", "error", err)
			return
		}
		if len(rows) == 0 {
			// 空批次连续出现 drainEmptyConfirmations 次才收工，把「锁竞争 /
			// 并发写入导致的短读」与「真的排空了」区分开。
			emptyStreak++
			if emptyStreak >= drainEmptyConfirmations {
				return
			}
			continue
		}
		emptyStreak = 0
		for _, row := range rows {
			r.replayOne(ctx, row)
		}
	}
}

func (r *MirrorOutboxReaper) claimBatch(ctx context.Context) ([]claimRow, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	//nolint:errcheck // readonly after claim update; rollback is the safe default
	defer tx.Rollback(ctx)
	if err := setBypassGUCs(ctx, tx); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		UPDATE public.session_mirror_outbox
		SET status = 'claimed', claimed_at = NOW(), updated_at = NOW()
		WHERE id IN (
			SELECT id FROM public.session_mirror_outbox
			WHERE status = 'pending' AND next_retry_at <= NOW()
			ORDER BY id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, request_id, session_id, payload, attempts
	`, r.batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claimed []claimRow
	for rows.Next() {
		var row claimRow
		if err := rows.Scan(&row.id, &row.requestID, &row.sessionID, &row.payload, &row.attempts); err != nil {
			return nil, err
		}
		claimed = append(claimed, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return claimed, tx.Commit(ctx)
}

// replayOne processes one claimed row: success deletes it, transient
// failure re-queues with backoff, terminal failure marks it dead.
func (r *MirrorOutboxReaper) replayOne(ctx context.Context, row claimRow) {
	var entry telemetry.RequestLogEntry
	if err := json.Unmarshal(row.payload, &entry); err != nil {
		// Corrupt payload can never succeed — no point burning retries.
		r.markDead(ctx, row, "decode: "+err.Error())
		return
	}

	// Gate parity with the live hook (hook.go): only the rows the hook
	// would have written belong in V2. Skipped rows are deleted — they are
	// not failures, and keeping them would loop forever.
	if !entry.Success && !isTerminalFailure(&entry) {
		r.deleteRow(ctx, row.id, "skipped: non-terminal entry")
		mirrorReplayTotal.WithLabelValues("skipped").Inc()
		return
	}
	// R60 S2-F4 gate parity：探针合成会话自 hook 起即不入 mirror（R51 教训），
	// 已登记的存量失败行（多为 advisory lock 超时噪声）在重放时直接删除，
	// 不再无限重试刷噪声。
	if IsProbeSyntheticSession(&entry) {
		r.deleteRow(ctx, row.id, "skipped: probe synthetic session (R51 mirror-skip class)")
		mirrorReplayTotal.WithLabelValues("skipped").Inc()
		return
	}
	synthetic := entry.GwSessionID == nil || *entry.GwSessionID == ""
	if !synthetic && telemetry.IsInternalAutoEntry(&entry) {
		r.deleteRow(ctx, row.id, "skipped: internal loopback (hook exclusion class)")
		mirrorReplayTotal.WithLabelValues("skipped").Inc()
		return
	}

	sessionID := row.sessionID
	if synthetic {
		sessionID = SyntheticSessionID(&entry)
		if sessionID == "" {
			r.markDead(ctx, row, "decode: empty session id and not synthesizable")
			return
		}
	}
	req := entryToProcessedRequest(&entry, sessionID)
	if req == nil {
		r.markDead(ctx, row, "decode: entry→ProcessedRequest bridge returned nil")
		return
	}
	if synthetic {
		req.ClientType = "system"
	}

	writeCtx, cancel := context.WithTimeout(ctx, mirrorReplayWriteBudgetMs*time.Millisecond)
	defer cancel()
	if err := r.writer.Write(writeCtx, req); err != nil {
		r.requeue(ctx, row, err)
		return
	}
	if _, err := execBypass(ctx, r.db, `DELETE FROM public.session_mirror_outbox WHERE id = $1`, row.id); err != nil {
		// The turn is written (idempotent on request_id); a leftover row
		// self-heals: the next replay attempt re-writes nothing (ON
		// CONFLICT DO NOTHING) and deletes the row.
		slog.Warn("sessionv2mirror: replayed turn delete failed",
			"request_id", row.requestID, "error", err)
	}
	mirrorReplayTotal.WithLabelValues("ok").Inc()
}

// requeue schedules another attempt with exponential backoff, or marks the
// row dead once attempts are exhausted. maxAtts is read per-call so the
// hot-reload setting sessions_v2.mirror_outbox_max_attempts is honored
// without restarting the gateway (audit handoff §7).
func (r *MirrorOutboxReaper) requeue(ctx context.Context, row claimRow, writeErr error) {
	maxAtts := currentMaxAtts(r.maxAtts)
	attempts := row.attempts + 1
	if attempts >= maxAtts {
		r.markDead(ctx, row, "write: "+writeErr.Error())
		return
	}
	backoff := time.Duration(1<<uint(attempts-1)) * 30 * time.Second
	if backoff > mirrorReplayMaxBackoff {
		backoff = mirrorReplayMaxBackoff
	}
	tag, err := execBypass(ctx, r.db, `
		UPDATE public.session_mirror_outbox
		SET status = 'pending', attempts = $2, last_error = $3,
		    next_retry_at = NOW() + $4::interval, claimed_at = NULL, updated_at = NOW()
		WHERE id = $1 AND status = 'claimed'
	`, row.id, attempts, writeErr.Error(), backoff)
	if err != nil {
		// The row stays 'claimed' past the lease and the orphan recovery
		// re-queues it — no loss, just a slower retry.
		slog.Warn("sessionv2mirror: outbox requeue update failed",
			"request_id", row.requestID, "error", err)
	} else if tag.RowsAffected() == 0 {
		// 十六轮审计 E6a：UPDATE 带 status='claimed' 守卫，lease 已被
		// 孤儿回收并可能被兄弟 worker 重 claim——本次 retry 实际未生效，
		// 不计 retry 指标，行的下一次重试由新 claim 路径驱动。
		slog.Warn("sessionv2mirror: outbox requeue superseded (row no longer claimed)",
			"request_id", row.requestID)
	} else {
		mirrorReplayTotal.WithLabelValues("retry").Inc()
	}
}

func (r *MirrorOutboxReaper) markDead(ctx context.Context, row claimRow, reason string) {
	// R34 (2026-09-17 audit): guard on 'claimed'. Without it a slow worker
	// whose lease the orphan recovery already returned to 'pending' (and that
	// a second worker re-claimed and re-dead-lettered) could resurrect the row
	// back to 'pending' here, breaking the dead-letter contract.
	tag, err := execBypass(ctx, r.db, `
		UPDATE public.session_mirror_outbox
		SET status = 'dead', last_error = $2, claimed_at = NULL, updated_at = NOW()
		WHERE id = $1 AND status = 'claimed'
	`, row.id, reason)
	if err != nil {
		slog.Error("sessionv2mirror: outbox dead-mark update failed",
			"request_id", row.requestID, "reason", reason, "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		// 十六轮审计 E6a：0 行命中 = 行并未落死信（lease 交接给了新的
		// claim）——不计 dead、不报数据丢失，与 630 reaper 的
		// RowsAffected 先查口径一致；否则死信计数虚高且误报
		// "mirror data lost"。
		slog.Warn("sessionv2mirror: outbox dead-mark superseded (row no longer claimed)",
			"request_id", row.requestID, "reason", reason)
		return
	}
	// A dead row is permanently lost mirror data — same observability
	// contract as the 630 reaper's dead-letter counters.
	slog.Error("sessionv2mirror: outbox row dead, mirror data lost pending manual reconciliation",
		"request_id", row.requestID, "session_id", row.sessionID,
		"attempts", row.attempts, "reason", reason)
	mirrorReplayTotal.WithLabelValues("dead").Inc()
	mirrorReplayDeadTotal.Inc()
}

func (r *MirrorOutboxReaper) deleteRow(ctx context.Context, id int64, why string) {
	if _, err := execBypass(ctx, r.db, `DELETE FROM public.session_mirror_outbox WHERE id = $1`, id); err != nil {
		slog.Warn("sessionv2mirror: outbox skip-delete failed", "id", id, "why", why, "error", err)
	}
}

func (r *MirrorOutboxReaper) refreshGauge(ctx context.Context) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	//nolint:errcheck // gauge scrape only
	defer tx.Rollback(ctx)
	if err := setBypassGUCs(ctx, tx); err != nil {
		return err
	}
	var pending float64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM public.session_mirror_outbox WHERE status = 'pending'
	`).Scan(&pending); err != nil {
		return err
	}
	mirrorOutboxPending.Set(pending)
	return tx.Commit(ctx)
}
