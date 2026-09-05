// bg/vacuum_worker.go — 2026-08-29
//
// 审计修复 (2026-08-29)：P1-7 - request_logs_bodies VACUUM自动化
//
// 定期对 request_logs_bodies 表执行 VACUUM FULL，回收 TOAST 表空间。
//
// 背景：
//   - request_logs_bodies 存储大量 JSONB 数据（请求/响应体）
//   - TOAST 表膨胀可能导致空间浪费
//   - 已有 TTL 清理（24小时保留），但未自动 VACUUM
//
// 设计：
//   - 每周执行一次 VACUUM FULL（周日凌晨 2:00）
//   - 仅在低峰期执行，避免影响业务
//   - VACUUM FULL 会锁表，需要在业务低峰期执行
//   - 支持优雅停止
//
// 接入点：cmd/gateway/main.go 在 init bg services 时构造 + Start。

package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/dbx"
)

// VacuumWorker 定期对 request_logs_bodies 执行 VACUUM FULL。
type VacuumWorker struct {
	db *pgxpool.Pool

	// lifecycle 由 BaseWorker 统一管理（审计报告 2026-08-31 模式 C）。
	*BaseWorker

	// 配置（业务字段，仍由本结构 mu 保护）
	mu          sync.Mutex
	interval    time.Duration // 执行间隔（默认 7 天）
	executeHour int           // 执行时间（小时，0-23，默认 2）
	lastExecuted time.Time    // 上次执行时间
}

// NewVacuumWorker 构造 worker。
func NewVacuumWorker(db *pgxpool.Pool) *VacuumWorker {
	return &VacuumWorker{
		db:          db,
		BaseWorker:  NewBaseWorker("vacuum-worker"),
		interval:    7 * 24 * time.Hour, // 默认每周一次
		executeHour: 2,                  // 默认凌晨 2 点
	}
}

// SetInterval 设置执行间隔（用于测试）。
func (w *VacuumWorker) SetInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.interval = d
}

// SetExecuteHour 设置执行时间（0-23 小时）。
func (w *VacuumWorker) SetExecuteHour(hour int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if hour >= 0 && hour <= 23 {
		w.executeHour = hour
	}
}

// Start 启动后台 goroutine。重复调用幂等；调用 Stop 之前不会重复启动。
func (w *VacuumWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	if !w.BaseWorker.Start(ctx, w.run) {
		return
	}
	w.mu.Lock()
	interval, hour := w.interval, w.executeHour
	w.mu.Unlock()
	slog.Info("vacuum worker started",
		"interval", interval,
		"execute_hour", hour)
}

// Stop 取消并等待 goroutine 退出。
func (w *VacuumWorker) Stop() {
	if w == nil {
		return
	}
	w.BaseWorker.Stop()
}

func (w *VacuumWorker) run(ctx context.Context) {
	defer w.BaseWorker.NotifyStopped()

	// 每小时检查一次是否到了执行时间
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// 启动后检查一次是否需要执行
	w.checkAndExecute(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkAndExecute(ctx)
		}
	}
}

func (w *VacuumWorker) checkAndExecute(ctx context.Context) {
	now := time.Now()

	// 检查是否到了执行时间
	// 1. 必须是指定的小时
	// 2. 距离上次执行至少超过了 interval
	// 配置字段的读写都在 mu 内完成；vacuum 在锁外执行，避免 30 分钟级的
	// VACUUM 长时间持锁阻塞 Stop/Start。
	w.mu.Lock()
	if now.Hour() != w.executeHour {
		w.mu.Unlock()
		return
	}

	if !w.lastExecuted.IsZero() && now.Sub(w.lastExecuted) < w.interval {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	// 执行 VACUUM；完成后才更新 lastExecuted（保持既有语义：执行失败时
	// 同一小时内仍可重试）
	w.vacuum(ctx)

	w.mu.Lock()
	w.lastExecuted = now
	w.mu.Unlock()
}

func (w *VacuumWorker) vacuum(ctx context.Context) {
	// nil-db 守卫：生产装配总是传入 pool，但测试与降级装配可能为 nil。
	// 无守卫时这里会 nil-deref panic 并带崩整个进程。
	if w.db == nil {
		slog.Warn("vacuum worker: no database pool configured, skipping VACUUM")
		return
	}

	start := time.Now()
	slog.Info("vacuum worker: starting VACUUM FULL on request_logs_bodies")

	// VACUUM FULL 可能需要较长时间，设置 30 分钟超时
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// 2026-09-01: cluster-wide VACUUM FULL mutex. With multiple
	// gateway replicas, both would fire at Sunday 02:00 and race
	// for ACCESS EXCLUSIVE — the loser fails with 55P03 and a
	// generic error. Acquire a separate connection for the
	// advisory lock so it can sit in pg_advisory_xact_lock for
	// the full VACUUM FULL duration without blocking the actual
	// vacuum connection.
	vacuumConn, err := w.db.Acquire(timeoutCtx)
	if err != nil {
		slog.Error("vacuum worker: acquire vacuumConn failed", "error", err)
		return
	}
	defer vacuumConn.Release()

	err = dbx.VacuumFullMutex(timeoutCtx, w.db, vacuumConn, 30*time.Second,
		func(ctx context.Context, c *pgxpool.Conn) error {
			_, ierr := c.Exec(ctx, "VACUUM FULL request_logs_bodies")
			return ierr
		})
	elapsed := time.Since(start)

	if err != nil {
		slog.Error("vacuum worker: VACUUM FULL failed",
			"error", err,
			"elapsed_seconds", elapsed.Seconds())
		return
	}

	slog.Info("vacuum worker: VACUUM FULL completed",
		"elapsed_seconds", elapsed.Seconds())

	// 同时对 hot 表也执行 VACUUM（不加 FULL，避免长时间锁表）
	// Hot 表数据量小，普通 VACUUM 即可
	hotCtx, hotCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer hotCancel()

	_, hotErr := w.db.Exec(hotCtx, "VACUUM request_logs_bodies_hot")
	if hotErr != nil {
		slog.Warn("vacuum worker: VACUUM on hot table failed",
			"error", hotErr)
	} else {
		slog.Info("vacuum worker: VACUUM on hot table completed")
	}
}
