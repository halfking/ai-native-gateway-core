// Package requestjourney — retention.go
//
// request_state_transitions 保留清理（B3-PR1, 2026-08-17）。
// 该表同时承载 journey 行（event_type 非空，migration 530）与 legacy 行
// （transition_type 非空：admin 节点操作审计 + 历史数据）。V3.2
// StateTransitionLogger 退役后，其内置 7 天清理 goroutine 是该表唯一的
// 保留机制；本 worker 以相同语义（1h tick、7 天保留、启动先清一次、
// 事务内 app.bypass_rls 跨租户删除）承接该职责，归属 530 契约属主。
package requestjourney

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RetentionTx / RetentionDB 把清理逻辑与 pgx 连接池解耦，测试可注入假实现。
type RetentionTx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type RetentionDB interface {
	Begin(ctx context.Context) (RetentionTx, error)
}

type retentionPool struct {
	pool *pgxpool.Pool
}

func (p retentionPool) Begin(ctx context.Context) (RetentionTx, error) {
	return p.pool.Begin(ctx)
}

// RetentionWorker 周期删除超过保留期的 request_state_transitions 行
// （journey 行与 legacy 行一并覆盖，按 created_at 判定）。db 为 nil 时
// Start 是 no-op。生命周期跟随进程；Stop 幂等，优雅退出时尽力清一次。
// 未 Start 过的 worker 调 Stop 是安全 no-op（不会等待不存在的 goroutine）。
type RetentionWorker struct {
	db RetentionDB

	interval  time.Duration
	retention time.Duration

	started  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
}

// NewRetentionWorker 构造清理 worker。db 为 nil 时 Start 变为 no-op
// （测试模式 / DB 禁用不影响主链路）。
func NewRetentionWorker(db *pgxpool.Pool) *RetentionWorker {
	w := &RetentionWorker{
		interval:  time.Hour,
		retention: 7 * 24 * time.Hour,
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	if db != nil {
		w.db = retentionPool{pool: db}
	}
	return w
}

// Start 启动后台清理 goroutine（db 为 nil 时立即返回）。
func (w *RetentionWorker) Start() {
	if w == nil || w.db == nil || !w.started.CompareAndSwap(false, true) {
		return
	}
	go w.run()
}

// Stop 停止清理 goroutine 并在退出前尽力执行一次清理。幂等，可安全多次调用。
func (w *RetentionWorker) Stop() {
	if w == nil || w.db == nil || !w.started.Load() {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.done
}

func (w *RetentionWorker) run() {
	defer close(w.done)
	w.CleanupOnce(context.Background())
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			w.CleanupOnce(context.Background())
			return
		case <-ticker.C:
			w.CleanupOnce(context.Background())
		}
	}
}

// CleanupOnce 删除超过保留期的历史，旁路执行：失败只记 warn，不返回中断。
func (w *RetentionWorker) CleanupOnce(ctx context.Context) {
	deleted, err := w.CleanupExpired(ctx)
	switch {
	case err != nil:
		slog.Warn("request journey retention: cleanup failed (non-fatal)", "error", err)
	case deleted > 0:
		slog.Info("request journey retention: removed expired transitions",
			"deleted", deleted, "retention", w.retention.String())
	}
}

// CleanupExpired 在单事务内设置事务级 bypass_rls（跨租户删除需要）并删除
// created_at 早于保留期的 journey rows，以及同一窗口外的 snapshot receipts。
// 会话级 GUC 会污染连接池中的共享连接，禁止使用。
func (w *RetentionWorker) CleanupExpired(ctx context.Context) (int64, error) {
	if w == nil || w.db == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // safe no-op if Commit succeeds
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM request_state_transitions
		WHERE created_at < NOW() - $1::interval`, w.retention.String())
	if err != nil {
		return 0, err
	}
	// Receipt cleanup is deliberately best-effort for mixed-version databases:
	// older installations may not have migration 618 yet. The runtime migration
	// creates the table before this worker starts; this guard preserves startup
	// compatibility for operators running retention during an upgrade window.
	receiptTag, receiptErr := tx.Exec(ctx, `
		DELETE FROM journal_snapshot_receipts
		WHERE updated_at < NOW() - $1::interval
		  AND (status = 'completed' OR claim_until < NOW())`, w.retention.String())
	if receiptErr != nil {
		if !strings.Contains(receiptErr.Error(), "journal_snapshot_receipts") {
			return 0, receiptErr
		}
		receiptTag = pgconn.NewCommandTag("DELETE 0")
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected() + receiptTag.RowsAffected(), nil
}
