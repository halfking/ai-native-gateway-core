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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// VacuumWorker 定期对 request_logs_bodies 执行 VACUUM FULL。
type VacuumWorker struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// 配置
	interval     time.Duration // 执行间隔（默认 7 天）
	executeHour  int           // 执行时间（小时，0-23，默认 2）
	lastExecuted time.Time     // 上次执行时间
}

// NewVacuumWorker 构造 worker。
func NewVacuumWorker(db *pgxpool.Pool) *VacuumWorker {
	return &VacuumWorker{
		db:          db,
		done:        make(chan struct{}),
		interval:    7 * 24 * time.Hour, // 默认每周一次
		executeHour: 2,                  // 默认凌晨 2 点
	}
}

// SetInterval 设置执行间隔（用于测试）。
func (w *VacuumWorker) SetInterval(d time.Duration) {
	w.interval = d
}

// SetExecuteHour 设置执行时间（0-23 小时）。
func (w *VacuumWorker) SetExecuteHour(hour int) {
	if hour >= 0 && hour <= 23 {
		w.executeHour = hour
	}
}

// Start 启动后台 goroutine。Stop 之前不能重复 Start。
func (w *VacuumWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("vacuum worker started",
		"interval", w.interval,
		"execute_hour", w.executeHour)
}

// Stop 取消并等待 goroutine 退出。
func (w *VacuumWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *VacuumWorker) run(ctx context.Context) {
	defer close(w.done)

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
	if now.Hour() != w.executeHour {
		return
	}

	if !w.lastExecuted.IsZero() && now.Sub(w.lastExecuted) < w.interval {
		return
	}

	// 执行 VACUUM
	w.vacuum(ctx)
	w.lastExecuted = now
}

func (w *VacuumWorker) vacuum(ctx context.Context) {
	start := time.Now()
	slog.Info("vacuum worker: starting VACUUM FULL on request_logs_bodies")

	// VACUUM FULL 可能需要较长时间，设置 30 分钟超时
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// VACUUM FULL 会锁表并重写整个表，回收所有空闲空间
	// 对于 TOAST 表密集型的表特别有效
	_, err := w.db.Exec(timeoutCtx, "VACUUM FULL request_logs_bodies")
	
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
