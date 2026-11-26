package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// BillingRetryWorker 计费重试worker（P0-3阶段2，2026-11-26）
//
// 定期扫描 billing_retry_queue 表，重试失败的积分扣减操作。
// 使用指数退避策略：1分钟 → 2分钟 → 4分钟 → ... 最多60分钟
// 最多重试10次，超过后标记为永久失败。
type BillingRetryWorker struct {
	maasSvc      *maas.Service
	interval     time.Duration
	batchSize    int
	stopped      chan struct{}
	statsLog     bool // 是否记录统计日志
}

// NewBillingRetryWorker 创建计费重试worker
//
// 参数：
//   - maasSvc: MaaS服务实例
//   - interval: 扫描间隔（建议5分钟）
//   - batchSize: 每批处理的任务数（建议100）
func NewBillingRetryWorker(maasSvc *maas.Service, interval time.Duration, batchSize int) *BillingRetryWorker {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if batchSize <= 0 {
		batchSize = 100
	}

	return &BillingRetryWorker{
		maasSvc:   maasSvc,
		interval:  interval,
		batchSize: batchSize,
		stopped:   make(chan struct{}),
		statsLog:  true,
	}
}

// Start 启动worker（阻塞运行）
func (w *BillingRetryWorker) Start(ctx context.Context) {
	slog.Info("billing retry worker started",
		"interval", w.interval,
		"batch_size", w.batchSize)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// 启动时立即运行一次
	w.processOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("billing retry worker stopped by context")
			close(w.stopped)
			return
		case <-w.stopped:
			slog.Info("billing retry worker stopped")
			return
		case <-ticker.C:
			w.processOnce(ctx)
		}
	}
}

// Stop 停止worker
func (w *BillingRetryWorker) Stop() {
	close(w.stopped)
}

// processOnce 处理一批重试任务
func (w *BillingRetryWorker) processOnce(ctx context.Context) {
	if w.maasSvc == nil || !w.maasSvc.Enabled() {
		return
	}

	startTime := time.Now()

	// 设置处理超时（最多5分钟）
	processCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 处理重试队列
	processed, succeeded, failed := w.maasSvc.ProcessRetryQueue(processCtx, w.batchSize)

	elapsed := time.Since(startTime)

	// 记录统计日志
	if processed > 0 {
		slog.Info("billing retry batch completed",
			"processed", processed,
			"succeeded", succeeded,
			"failed", failed,
			"elapsed_ms", elapsed.Milliseconds())
	} else if w.statsLog {
		// 只在启用统计日志时记录空批次
		slog.Debug("billing retry batch completed (empty)",
			"elapsed_ms", elapsed.Milliseconds())
	}
}

// SetStatsLog 设置是否记录统计日志（空批次）
func (w *BillingRetryWorker) SetStatsLog(enabled bool) {
	w.statsLog = enabled
}
