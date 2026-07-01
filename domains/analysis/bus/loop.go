package bus

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// PollFunc 从持久层拉取一批待处理事件。
//
// 返回的事件应满足：processed_at IS NULL AND type IN (worker.SubscribedTypes())。
// 实现应使用 FOR UPDATE SKIP LOCKED 以支持多 worker 并发。
type PollFunc func(ctx context.Context, batchSize int) ([]analysis.AnalysisEvent, error)

// MarkFunc 把单条事件标记为已处理 / 处理失败。
//
// err == nil  → processed_at = NOW(), attempts += 0
// err != nil  → attempts += 1, last_error = err.Error()
type MarkFunc func(ctx context.Context, eventID, workerName string, err error) error

// LoopConfig Loop 行为配置。
type LoopConfig struct {
	Interval  time.Duration // 轮询间隔；默认 5s
	BatchSize int           // 单次拉取条数；默认 10
	Logger    *slog.Logger
}

func (c *LoopConfig) applyDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 10
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// RunLoop 启动一个 worker 循环；ctx.Done() 时退出。
//
// 设计要点：
//   - poll 与 mark 由调用方注入，便于测试
//   - 单条事件失败不中断整个 loop
//   - 同一 worker 内串行处理；多 worker 可通过多次调用 RunLoop 并发
func RunLoop(ctx context.Context, w analysis.Worker, poll PollFunc, mark MarkFunc, cfg LoopConfig) {
	if w == nil || poll == nil || mark == nil {
		return
	}
	cfg.applyDefaults()
	cfg.Logger.Info("analysis.RunLoop: starting",
		"worker", w.Name(),
		"subscribed_types", w.SubscribedTypes(),
		"interval", cfg.Interval.String(),
		"batch_size", cfg.BatchSize,
	)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cfg.Logger.Info("analysis.RunLoop: stopping", "worker", w.Name())
			return
		case <-ticker.C:
			events, err := poll(ctx, cfg.BatchSize)
			if err != nil {
				cfg.Logger.Warn("analysis.RunLoop: poll failed",
					"worker", w.Name(), "error", err)
				continue
			}
			for _, evt := range events {
				if err := w.Handle(ctx, evt); err != nil {
					cfg.Logger.Warn("analysis.RunLoop: handle failed",
						"worker", w.Name(), "event_id", evt.EventID, "error", err)
					if mErr := mark(ctx, evt.EventID, w.Name(), err); mErr != nil {
						cfg.Logger.Warn("analysis.RunLoop: mark failed",
							"worker", w.Name(), "event_id", evt.EventID, "error", mErr)
					}
					continue
				}
				if mErr := mark(ctx, evt.EventID, w.Name(), nil); mErr != nil {
					cfg.Logger.Warn("analysis.RunLoop: mark-processed failed",
						"worker", w.Name(), "event_id", evt.EventID, "error", mErr)
				}
			}
		}
	}
}
