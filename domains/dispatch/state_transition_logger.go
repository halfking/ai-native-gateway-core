// Package dispatch — state_transition_logger.go
//
// V3.2 (2026-08-13) BE-B1: 请求状态变更历史记录。
// V3.3 OBS-BE7 (2026-08-15): 可靠性补齐 —— 失败重放（内存队列 + 指数退避
// 3 次）、幂等 INSERT（(request_id, seq) 唯一 + ON CONFLICT DO NOTHING，
// migration 515）、周期清理 goroutine（7 天保留，生命周期跟随进程）。
//
// 把请求生命周期的关键状态变更（路由决策 / 节点切换 / 重试 / 终态错误）
// 旁路异步写入 request_state_transitions 表（migration 511 + 515），供
// GET /api/admin/requests/{id}/transitions 查询，并让首页实时请求流
// 能展示"到达→路由→选节点→切换节点→重试🔄→回复/错误"的完整状态流转。
//
// 设计约束（ADR-V3-001/104）：
//   - 旁路异步：写入不阻塞 relay 主链路；失败只记日志。
//   - 批量：内存缓冲 + 定时 flush，减少 DB 往返。
//   - 关联键：复用 request_id（ADR-V3-102），不造新 ID。
//
// 可靠性语义（OBS-BE7）：
//   - 失败重放：写失败的行进入内存重试队列，指数退避（1s/2s/4s）重试
//     3 次，仍失败则丢弃并 warn（旁路数据，不无限堆积）。
//   - 启动回放：重试队列为纯内存（无持久化积压概念），进程重启后队列
//     清空，无需回放；中断批次的唯一后果是丢失尚未落库的旁路记录，
//     不影响主链路正确性。幂等 (request_id, seq) 保证重试/重放不会
//     产生重复行。
//   - 定期清理：独立旁路 goroutine 按 cleanupInterval（默认 1h）删除
//     created_at 早于 retention（默认 7 天）的行；启动即先清一次。
//     生命周期跟随进程，Stop() 优雅退出。
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StateTransition 是一条状态变更记录（对应 request_state_transitions 一行）。
type StateTransition struct {
	RequestID      string
	TenantID       string // 租户 ID (T0: admin, T1-T4: 正常租户)
	TransitionType string // route | node_switch | retry | error | state
	FromState      string
	ToState        string
	Metadata       map[string]any // 决策原因/候选列表/retry_seq/reason_class 等
}

type stateTransitionTx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type stateTransitionDB interface {
	Begin(ctx context.Context) (stateTransitionTx, error)
}

type stateTransitionPool struct {
	pool *pgxpool.Pool
}

func (p stateTransitionPool) Begin(ctx context.Context) (stateTransitionTx, error) {
	return p.pool.Begin(ctx)
}

// pendingTransition 是缓冲/重试队列中的待写条目。
// seq 在入队时一次性分配（进程内全局单调计数器），保证同一行重试/重放
// 时 (request_id, seq) 不变 → INSERT 幂等。
type pendingTransition struct {
	t           StateTransition
	seq         int64
	attempts    int       // 已失败的重试次数（首次写入失败后为 1）
	nextRetryAt time.Time // 下次重试时间（指数退避）
}

// StateTransitionLogger 异步批量写状态变更历史。
// 零值不可用；用 NewStateTransitionLogger 构造。
type StateTransitionLogger struct {
	db stateTransitionDB // nil 时所有 Log* 变为 no-op

	mu  sync.Mutex
	buf []pendingTransition

	nextSeq atomic.Int64 // (request_id, seq) 幂等键的进程内全局单调计数器

	retryMu sync.Mutex
	retryQ  []pendingTransition // 失败重放队列（纯内存，进程重启即清空）

	// now 可在测试里替换为固定时钟。
	now func() time.Time

	flushInterval    time.Duration
	batchSize        int
	maxRetryAttempts int           // 写失败后的最大重试次数（默认 3）
	retryBaseDelay   time.Duration // 指数退避基数：delay = base * 2^(attempts-1)
	retryScanEvery   time.Duration // 重试队列扫描间隔
	cleanupInterval  time.Duration // 清理任务 tick
	retention        time.Duration // 历史保留时长（默认 7 天）

	maxRetryQueueLen int // 重试队列上限（防 DB 长时间不可用导致内存膨胀）

	stopCh      chan struct{}
	done        chan struct{} // run()（flush + retry）
	doneCleanup chan struct{} // runCleanup()
	stopOnce    sync.Once
}

// NewStateTransitionLogger 构造 logger。db 为 nil 时所有 Log* 变为 no-op
// （优雅降级：DB 不可用不影响 relay）。
func NewStateTransitionLogger(db *pgxpool.Pool) *StateTransitionLogger {
	l := &StateTransitionLogger{
		db:               nil,
		buf:              make([]pendingTransition, 0, 64),
		now:              time.Now,
		flushInterval:    2 * time.Second,
		batchSize:        100,
		maxRetryAttempts: 3,
		retryBaseDelay:   1 * time.Second,
		retryScanEvery:   500 * time.Millisecond,
		cleanupInterval:  time.Hour,
		retention:        7 * 24 * time.Hour,
		maxRetryQueueLen: 10000,
		stopCh:           make(chan struct{}),
		done:             make(chan struct{}),
		doneCleanup:      make(chan struct{}),
	}
	if db != nil {
		l.db = stateTransitionPool{pool: db}
		go l.run()
		go l.runCleanup()
	}
	return l
}

// LogRouteDecision 记录路由决策（route_resolve/route_credential 阶段）。
// metadata 建议含 candidates / block_reason / chosen_credential_id。
func (l *StateTransitionLogger) LogRouteDecision(requestID, tenantID, fromState, toState string, metadata map[string]any) {
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "route", FromState: fromState, ToState: toState, Metadata: metadata})
}

// LogNodeSwitch 记录节点切换（failover/sibling 切换）。
// metadata 建议含 from_node / to_node / reason。
func (l *StateTransitionLogger) LogNodeSwitch(requestID, tenantID, fromNode, toNode string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["from_node"] = fromNode
	metadata["to_node"] = toNode
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "node_switch", FromState: fromNode, ToState: toNode, Metadata: metadata})
}

// LogRetry 记录重试（前端特殊标 🔄）。
// metadata 必须含 retry_seq / reason_class（供前端标记）。
func (l *StateTransitionLogger) LogRetry(requestID, tenantID string, retrySeq int, reasonClass string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["retry_seq"] = retrySeq
	metadata["reason_class"] = reasonClass
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "retry", ToState: "retry", Metadata: metadata})
}

// LogError 记录终态错误。
func (l *StateTransitionLogger) LogError(requestID, tenantID, fromState string, metadata map[string]any) {
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "error", FromState: fromState, ToState: "error", Metadata: metadata})
}

// add 追加到缓冲；满 batchSize 触发同步 flush（仍旁路：调用方是 relay 的
// 异步钩子，不在请求关键路径上）。
func (l *StateTransitionLogger) add(t StateTransition) {
	if l == nil || l.db == nil || t.RequestID == "" || strings.TrimSpace(t.TenantID) == "" {
		return
	}
	p := pendingTransition{t: t, seq: l.nextSeq.Add(1)}
	l.mu.Lock()
	l.buf = append(l.buf, p)
	full := len(l.buf) >= l.batchSize
	l.mu.Unlock()
	if full {
		l.Flush()
	}
}

// Flush 把缓冲批量写入 DB；失败的行进入重试队列（旁路：最终仍失败只记日志）。
func (l *StateTransitionLogger) Flush() {
	if l == nil || l.db == nil {
		return
	}
	l.mu.Lock()
	if len(l.buf) == 0 {
		l.mu.Unlock()
		return
	}
	batch := make([]pendingTransition, len(l.buf))
	copy(batch, l.buf)
	l.buf = l.buf[:0]
	l.mu.Unlock()

	l.writeBatch(context.Background(), batch)
}

// writeBatch 逐行 INSERT（幂等：ON CONFLICT (request_id, seq) DO NOTHING）。
// 每行使用独立事务，把 tenant GUC 与 INSERT 固定在同一物理连接上；失败粒度
// 仍保持逐行，便于精准重试。
func (l *StateTransitionLogger) writeBatch(ctx context.Context, batch []pendingTransition) []pendingTransition {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var failed []pendingTransition
	for _, p := range batch {
		if err := l.insertTransition(ctx, p); err != nil {
			slog.Warn("state_transition: insert failed (queued for retry)",
				"request_id", p.t.RequestID, "tenant_id", p.t.TenantID,
				"type", p.t.TransitionType, "seq", p.seq, "error", err)
			failed = append(failed, p)
		}
	}
	if len(failed) > 0 {
		now := l.now()
		l.enqueueRetry(failed, now)
	}
	return failed
}

// enqueueRetry 把失败行放入重试队列，安排指数退避。
// 队列满时淘汰最老的条目（旁路数据可丢，防内存膨胀）。
func (l *StateTransitionLogger) enqueueRetry(failed []pendingTransition, now time.Time) {
	l.retryMu.Lock()
	defer l.retryMu.Unlock()
	for _, p := range failed {
		p.attempts++
		if p.attempts > l.maxRetryAttempts {
			l.logDropLocked(p, "max retry attempts exceeded")
			continue
		}
		p.nextRetryAt = now.Add(l.retryBackoff(p.attempts))
		l.retryQ = append(l.retryQ, p)
		if len(l.retryQ) > l.maxRetryQueueLen {
			oldest := l.retryQ[0]
			l.retryQ = l.retryQ[1:]
			l.logDropLocked(oldest, "retry queue overflow")
		}
	}
}

// retryBackoff 返回第 attempts 次失败后的退避时长（1x, 2x, 4x ...）。
func (l *StateTransitionLogger) retryBackoff(attempts int) time.Duration {
	d := l.retryBaseDelay
	for i := 1; i < attempts; i++ {
		d *= 2
	}
	return d
}

func (l *StateTransitionLogger) logDropLocked(p pendingTransition, reason string) {
	slog.Warn("state_transition: record dropped",
		"reason", reason, "request_id", p.t.RequestID,
		"type", p.t.TransitionType, "seq", p.seq, "attempts", p.attempts)
}

// processRetriesDue 扫描重试队列，重写所有到期的行。
// 成功 → 出队；失败 → attempts++，超过 maxRetryAttempts 丢弃，否则按指数退避重新入队。
func (l *StateTransitionLogger) processRetriesDue(now time.Time) {
	l.retryMu.Lock()
	if len(l.retryQ) == 0 {
		l.retryMu.Unlock()
		return
	}
	var due []pendingTransition
	kept := l.retryQ[:0]
	for _, p := range l.retryQ {
		if !p.nextRetryAt.After(now) {
			due = append(due, p)
		} else {
			kept = append(kept, p)
		}
	}
	l.retryQ = kept
	l.retryMu.Unlock()

	if len(due) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stillFailed []pendingTransition
	for _, p := range due {
		if err := l.insertTransition(ctx, p); err == nil {
			slog.Info("state_transition: retry replay succeeded",
				"request_id", p.t.RequestID, "type", p.t.TransitionType,
				"seq", p.seq, "attempts", p.attempts)
		} else {
			stillFailed = append(stillFailed, p)
		}
	}
	if len(stillFailed) > 0 {
		l.enqueueRetry(stillFailed, now)
	}
}

func (l *StateTransitionLogger) insertTransition(ctx context.Context, p pendingTransition) error {
	if strings.TrimSpace(p.t.TenantID) == "" {
		return fmt.Errorf("empty tenant_id")
	}
	var metaJSON any
	if p.t.Metadata != nil {
		encoded, err := json.Marshal(p.t.Metadata)
		if err != nil {
			return fmt.Errorf("marshal transition metadata: %w", err)
		}
		// The gateway uses pgx simple protocol globally. Under simple protocol,
		// []byte is encoded as bytea, which PostgreSQL cannot parse as JSONB.
		metaJSON = string(encoded)
	}
	tx, err := l.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transition tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", p.t.TenantID); err != nil {
		return fmt.Errorf("set transition tenant: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO request_state_transitions
		  (request_id, tenant_id, transition_type, from_state, to_state, metadata, seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (request_id, seq) DO NOTHING`,
		p.t.RequestID, p.t.TenantID, p.t.TransitionType,
		nullStr(p.t.FromState), nullStr(p.t.ToState), metaJSON, p.seq); err != nil {
		return fmt.Errorf("insert transition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transition: %w", err)
	}
	return nil
}

// retryQueueLen 返回重试队列当前长度（测试/诊断用）。
func (l *StateTransitionLogger) retryQueueLen() int {
	l.retryMu.Lock()
	defer l.retryMu.Unlock()
	return len(l.retryQ)
}

// run 定时 flush + 扫描重试队列。
func (l *StateTransitionLogger) run() {
	flushTicker := time.NewTicker(l.flushInterval)
	defer flushTicker.Stop()
	retryTicker := time.NewTicker(l.retryScanEvery)
	defer retryTicker.Stop()
	defer close(l.done)
	for {
		select {
		case <-l.stopCh:
			// 优雅退出：尽力落盘缓冲 + 立即重放一次到期/未到期的积压。
			l.Flush()
			l.processRetriesDue(l.now().Add(time.Hour))
			return
		case <-flushTicker.C:
			l.Flush()
		case <-retryTicker.C:
			l.processRetriesDue(l.now())
		}
	}
}

// runCleanup 定期删除超过保留期的历史（默认 7 天），旁路 goroutine，
// 生命周期跟随进程。启动先清一次，避免新部署等满一个 tick。
func (l *StateTransitionLogger) runCleanup() {
	defer close(l.doneCleanup)
	l.cleanupOnce(context.Background())
	ticker := time.NewTicker(l.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			l.cleanupOnce(context.Background())
		}
	}
}

func (l *StateTransitionLogger) cleanupOnce(ctx context.Context) {
	n, err := l.CleanupOldTransitions(ctx)
	switch {
	case err != nil:
		slog.Warn("state_transition: cleanup failed (non-fatal)", "error", err)
	case n > 0:
		slog.Info("state_transition: cleanup removed expired rows",
			"deleted", n, "retention", l.retention.String())
	}
}

// Stop 停止后台 flush/重试/清理 goroutine 并尽力落盘剩余缓冲。
// 幂等（sync.Once），可安全多次调用。
func (l *StateTransitionLogger) Stop() {
	if l == nil || l.db == nil {
		return
	}
	l.stopOnce.Do(func() { close(l.stopCh) })
	<-l.done
	<-l.doneCleanup
}

// CleanupOldTransitions 删除超过保留期（默认 7 天）的历史。
// 由内置清理 goroutine 周期调用，也可外部显式触发。
func (l *StateTransitionLogger) CleanupOldTransitions(ctx context.Context) (int64, error) {
	if l == nil || l.db == nil {
		return 0, nil
	}
	tx, err := l.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM request_state_transitions
		WHERE created_at < NOW() - $1::interval`, l.retention.String())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// nullStr 把空字符串转为 nil（写 NULL）。
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
