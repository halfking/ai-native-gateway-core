// Package workers — IntentFlusher (PR-V4-10)。
//
// 周期性把 IntentWorker 的 in-memory 计数 flush 到 assets.IntentAggregateStore。
// 与 Loop 不同的地方在于：Loop 处理外部事件，Flusher 处理 worker 的内部状态。
package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/assets" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// IntentFlusher 周期 flush IntentWorker 计数到 store。
//
// 字段：
//   - worker：被 flush 的 worker（必填）
//   - store：flush 目标（nil 时只清零，便于测试）
//   - interval：flush 间隔；<= 0 时默认 60s
type IntentFlusher struct {
	worker   *IntentWorker
	store    assets.IntentAggregateStore
	interval time.Duration
	logger   *slog.Logger
}

// NewIntentFlusher 构造 flusher。
func NewIntentFlusher(worker *IntentWorker, store assets.IntentAggregateStore, interval time.Duration, logger *slog.Logger) *IntentFlusher {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &IntentFlusher{
		worker:   worker,
		store:    store,
		interval: interval,
		logger:   logger,
	}
}

// Run 阻塞运行直到 ctx done。每 interval 调一次 FlushAndReset。
//
// 失败不退出：单次 flush 失败仅记录日志，等下一周期。worker 内部计数
// 在 FlushAndReset 期间已被清零（即使 store 调用失败），所以失败一次后
// 这一窗的 delta 会丢失；这是可接受的弱一致设计（统计量）。
func (f *IntentFlusher) Run(ctx context.Context) {
	if f == nil || f.worker == nil {
		return
	}
	tick := time.NewTicker(f.interval)
	defer tick.Stop()
	f.logger.Info("intent_flusher: started",
		"interval", f.interval.String())
	for {
		select {
		case <-ctx.Done():
			// 退出前做最后一次 flush，避免窗内计数丢失。
			if err := f.worker.FlushAndReset(context.Background(), f.store); err != nil {
				f.logger.Warn("intent_flusher: final flush failed", "error", err)
			}
			f.logger.Info("intent_flusher: stopped")
			return
		case <-tick.C:
			if err := f.worker.FlushAndReset(ctx, f.store); err != nil {
				f.logger.Warn("intent_flusher: flush failed", "error", err)
			}
		}
	}
}
