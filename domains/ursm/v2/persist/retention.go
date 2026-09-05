// Package persist — retention.go
//
// ursm_node_snapshot_min 保留清理（URSM v2 容量门禁整改，2026-09-05）。
// 该表是 Writer.Flush 的唯一落盘出口，此前只有 INSERT 没有任何清理：
// 容量基线（docs/perf/capacity-retention-baseline-2026-09-05.md §5）实测
// 45 天 13.7M 行 / 6.6 GB（约 148 MB/天），是 URSM v2 进入 canary/authoritative
// （persist writer 常开）前的硬阻塞项。
//
// 与 requestjourney.RetentionWorker 的两点差异：
//   - 表无 RLS（pg_class.relrowsecurity = f），无需 bypass_rls GUC；
//   - 存量达千万行级，单事务全删会造成长事务/WAL 尖峰，故按
//     SnapshotRetentionConfig.BatchSize 分批、每批独立事务，单轮清理
//     受 MaxCleanupWindow 墙钟上限约束，剩余量留给下一个 tick。
package persist

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

// SnapshotRetentionConfig 控制快照保留行为。Retention <= 0 表示禁用
// （Start 变为 no-op），用于运维显式关闭清理。
type SnapshotRetentionConfig struct {
	Retention        time.Duration // 保留窗口，默认 30 天（URSM_SNAPSHOT_RETENTION_DAYS）
	BatchSize        int           // 单批删除行数，默认 5000
	MaxCleanupWindow time.Duration // 单轮清理的墙钟上限，默认 10 分钟
}

// DefaultSnapshotRetentionConfig 返回默认配置。30 天审计窗口与容量基线
// 报告的建议一致（快照仅用于 URSM v2 cutover 前后的状态审计/比对）。
func DefaultSnapshotRetentionConfig() SnapshotRetentionConfig {
	return SnapshotRetentionConfig{
		Retention:        30 * 24 * time.Hour,
		BatchSize:        5000,
		MaxCleanupWindow: 10 * time.Minute,
	}
}

// SnapshotRetentionConfigFromEnv 读取 URSM_SNAPSHOT_RETENTION_DAYS。
// 未设置时用默认 30 天；显式设置 0 或负数禁用清理；非法值忽略（保持默认）。
func SnapshotRetentionConfigFromEnv() SnapshotRetentionConfig {
	cfg := DefaultSnapshotRetentionConfig()
	if v := os.Getenv("URSM_SNAPSHOT_RETENTION_DAYS"); v != "" {
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

// SnapshotRetentionTx / SnapshotRetentionDB 把清理逻辑与 pgx 连接池解耦，
// 测试可注入假实现（每批一个事务，Begin 会被多次调用）。
type SnapshotRetentionTx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type SnapshotRetentionDB interface {
	Begin(ctx context.Context) (SnapshotRetentionTx, error)
}

type snapshotRetentionPool struct {
	pool *pgxpool.Pool
}

func (p snapshotRetentionPool) Begin(ctx context.Context) (SnapshotRetentionTx, error) {
	return p.pool.Begin(ctx)
}

// SnapshotRetentionWorker 周期分批删除超过保留期的 ursm_node_snapshot_min 行
// （按 snapshot_ts 判定，走 ursm_node_snapshot_min_ts_idx）。
// db 为 nil 或 Retention <= 0 时 Start 是 no-op。生命周期跟随进程；
// Stop 幂等，优雅退出时尽力清一次。
type SnapshotRetentionWorker struct {
	db  SnapshotRetentionDB
	cfg SnapshotRetentionConfig

	started  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
}

// NewSnapshotRetentionWorker 构造清理 worker。db 为 nil 时 Start 变为 no-op
// （测试模式 / DB 禁用不影响主链路）。
func NewSnapshotRetentionWorker(db *pgxpool.Pool, cfg SnapshotRetentionConfig) *SnapshotRetentionWorker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultSnapshotRetentionConfig().BatchSize
	}
	if cfg.MaxCleanupWindow <= 0 {
		cfg.MaxCleanupWindow = DefaultSnapshotRetentionConfig().MaxCleanupWindow
	}
	w := &SnapshotRetentionWorker{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	if db != nil {
		w.db = snapshotRetentionPool{pool: db}
	}
	return w
}

// Disabled 报告清理是否被配置关闭（Retention <= 0）。
func (w *SnapshotRetentionWorker) Disabled() bool {
	return w == nil || w.cfg.Retention <= 0
}

// Start 启动后台清理 goroutine（db 为 nil 或已禁用时立即返回）。
func (w *SnapshotRetentionWorker) Start() {
	if w.Disabled() || w.db == nil || !w.started.CompareAndSwap(false, true) {
		return
	}
	go w.run()
}

// Stop 停止清理 goroutine 并在退出前尽力执行一次清理。幂等，可安全多次调用。
func (w *SnapshotRetentionWorker) Stop() {
	if w.Disabled() || w.db == nil || !w.started.Load() {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.done
}

func (w *SnapshotRetentionWorker) run() {
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

// CleanupOnce 删除超过保留期的快照行，失败只记 warn，不返回中断。
func (w *SnapshotRetentionWorker) CleanupOnce(ctx context.Context) {
	if w.Disabled() || w.db == nil {
		return
	}
	deleted, err := w.CleanupExpired(ctx)
	switch {
	case err != nil:
		slog.Warn("ursm.v2: snapshot retention cleanup failed (non-fatal)", "error", err)
	case deleted > 0:
		slog.Info("ursm.v2: snapshot retention removed expired snapshots",
			"deleted", deleted, "retention", w.cfg.Retention.String())
	}
}

// CleanupExpired 分批删除 snapshot_ts 早于保留期的行。每批独立事务：
// 单批失败即停止并返回已删数量与错误；批次循环直到删空或超过
// MaxCleanupWindow 墙钟上限（剩余量由下一个 tick 续删）。
func (w *SnapshotRetentionWorker) CleanupExpired(ctx context.Context) (int64, error) {
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

// deleteBatch 执行单批删除。行构造器 IN 命中主键
// (snapshot_ts, tenant_id, credential_id, raw_model_name)，
// 子查询走 ursm_node_snapshot_min_ts_idx 定位候选，避免全表扫描。
func (w *SnapshotRetentionWorker) deleteBatch(ctx context.Context) (int64, error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // safe no-op if Commit succeeds
	tag, err := tx.Exec(ctx, `
		DELETE FROM ursm_node_snapshot_min
		WHERE (snapshot_ts, tenant_id, credential_id, raw_model_name) IN (
			SELECT snapshot_ts, tenant_id, credential_id, raw_model_name
			FROM ursm_node_snapshot_min
			WHERE snapshot_ts < NOW() - $1::interval
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
