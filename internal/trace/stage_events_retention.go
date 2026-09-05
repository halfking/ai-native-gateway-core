// Package trace — stage_events_retention.go
//
// request_stage_events 保留清理（容量门禁整改，2026-09-05）。
// RedisRecorder.writeStageEvents 是该表的唯一写入方，此前无任何清理：
// 容量基线（docs/perf/capacity-retention-baseline-2026-09-05.md §5）实测
// 47 天 6.96M 行 / 2.8 GB（约 60 MB/天），与 ursm_node_snapshot_min 同为
// 无界增长表。默认 7 天窗口与 request_state_transitions 的 journey 保留
// （requestjourney.RetentionWorker）对齐——两者同属请求链路观测数据。
//
// 实现差异说明：该表没有 created_at 单列索引（现有索引均以 tenant/stage
// 为前导列），子查询按 created_at 过滤会走顺序扫描；得益于"最老行物理
// 靠前"的堆布局与 LIMIT 批次，首清存量后每 tick 的增量成本很小。若生产
// 实测扫描成本高，再补 created_at 索引迁移，本 worker 的 SQL 无需变化。
// 表无 RLS（pg_class.relrowsecurity = f），无需 bypass_rls GUC。
package trace

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StageEventsRetentionConfig 控制清理行为。Retention <= 0 表示禁用
// （Start 变为 no-op），用于运维显式关闭清理。
type StageEventsRetentionConfig struct {
	Retention        time.Duration // 保留窗口，默认 7 天（STAGE_EVENTS_RETENTION_DAYS）
	BatchSize        int           // 单批删除行数，默认 5000
	MaxCleanupWindow time.Duration // 单轮清理的墙钟上限，默认 10 分钟
}

// DefaultStageEventsRetentionConfig 返回默认配置。
func DefaultStageEventsRetentionConfig() StageEventsRetentionConfig {
	return StageEventsRetentionConfig{
		Retention:        7 * 24 * time.Hour,
		BatchSize:        5000,
		MaxCleanupWindow: 10 * time.Minute,
	}
}

// StageEventsRetentionConfigFromEnv 读取 STAGE_EVENTS_RETENTION_DAYS。
// 未设置时用默认 7 天；显式设置 0 或负数禁用清理；非法值忽略（保持默认）。
func StageEventsRetentionConfigFromEnv() StageEventsRetentionConfig {
	cfg := DefaultStageEventsRetentionConfig()
	if v := os.Getenv("STAGE_EVENTS_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n <= 0 {
				cfg.Retention = 0
			} else {
				cfg.Retention = time.Duration(n) * 24 * time.Hour
			}
		}
	}
	return cfg
}

// StageEventsRetentionTx / StageEventsRetentionDB 把清理逻辑与 pgx 连接池
// 解耦，测试可注入假实现（每批一个事务，Begin 会被多次调用）。
type StageEventsRetentionTx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type StageEventsRetentionDB interface {
	Begin(ctx context.Context) (StageEventsRetentionTx, error)
}

type stageEventsRetentionPool struct {
	pool *pgxpool.Pool
}

func (p stageEventsRetentionPool) Begin(ctx context.Context) (StageEventsRetentionTx, error) {
	return p.pool.Begin(ctx)
}

// StageEventsRetentionWorker 周期分批删除超过保留期的 request_stage_events
// 行（按 created_at 判定）。db 为 nil 或 Retention <= 0 时 Start 是 no-op。
// 生命周期跟随进程；Stop 幂等，优雅退出时尽力清一次。
type StageEventsRetentionWorker struct {
	db  StageEventsRetentionDB
	cfg StageEventsRetentionConfig

	started  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
}

// NewStageEventsRetentionWorker 构造清理 worker。db 为 nil 时 Start 变为
// no-op（测试模式 / DB 禁用不影响主链路）。
func NewStageEventsRetentionWorker(db *pgxpool.Pool, cfg StageEventsRetentionConfig) *StageEventsRetentionWorker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultStageEventsRetentionConfig().BatchSize
	}
	if cfg.MaxCleanupWindow <= 0 {
		cfg.MaxCleanupWindow = DefaultStageEventsRetentionConfig().MaxCleanupWindow
	}
	w := &StageEventsRetentionWorker{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	if db != nil {
		w.db = stageEventsRetentionPool{pool: db}
	}
	return w
}

// Disabled 报告清理是否被配置关闭（Retention <= 0）。
func (w *StageEventsRetentionWorker) Disabled() bool {
	return w == nil || w.cfg.Retention <= 0
}

// Start 启动后台清理 goroutine（db 为 nil 或已禁用时立即返回）。
func (w *StageEventsRetentionWorker) Start() {
	if w.Disabled() || w.db == nil || !w.started.CompareAndSwap(false, true) {
		return
	}
	go w.run()
}

// Stop 停止清理 goroutine 并在退出前尽力执行一次清理。幂等，可安全多次调用。
func (w *StageEventsRetentionWorker) Stop() {
	if w.Disabled() || w.db == nil || !w.started.Load() {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.done
}

func (w *StageEventsRetentionWorker) run() {
	defer close(w.done)
	w.CleanupOnce(context.Background())
	ticker := time.NewTicker(time.Hour)
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

// CleanupOnce 删除超过保留期的舞台事件，失败只记 warn，不返回中断。
func (w *StageEventsRetentionWorker) CleanupOnce(ctx context.Context) {
	if w.Disabled() || w.db == nil {
		return
	}
	deleted, err := w.CleanupExpired(ctx)
	switch {
	case err != nil:
		slog.Warn("trace: stage events retention cleanup failed (non-fatal)", "error", err)
	case deleted > 0:
		slog.Info("trace: stage events retention removed expired rows",
			"deleted", deleted, "retention", w.cfg.Retention.String())
	}
}

// CleanupExpired 分批删除 created_at 早于保留期的行。每批独立事务：
// 单批失败即停止并返回已删数量与错误；批次循环直到删空或超过
// MaxCleanupWindow 墙钟上限（剩余量由下一个 tick 续删）。
func (w *StageEventsRetentionWorker) CleanupExpired(ctx context.Context) (int64, error) {
	if w.Disabled() || w.db == nil {
		return 0, nil
	}
	deadline := time.Now().Add(w.cfg.MaxCleanupWindow)
	var total int64
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		deleted, err := w.deleteBatch(batchCtx)
		cancel()
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < int64(w.cfg.BatchSize) {
			return total, nil
		}
		if time.Now().After(deadline) {
			return total, nil
		}
	}
}

// deleteBatch 执行单批删除。子查询按 PK (id) 回表删除。
func (w *StageEventsRetentionWorker) deleteBatch(ctx context.Context) (int64, error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // safe no-op if Commit succeeds
	tag, err := tx.Exec(ctx, `
		DELETE FROM request_stage_events
		WHERE id IN (
			SELECT id FROM request_stage_events
			WHERE created_at < NOW() - $1::interval
			LIMIT $2
		)`, w.cfg.Retention.String(), w.cfg.BatchSize)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
