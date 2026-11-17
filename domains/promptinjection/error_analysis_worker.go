// Package promptinjection - PI 智能体后台 worker
//
// ★ 2026-11-17 审计轮：PI 智能体此前**从未接线** ——
//
//	错误收集器、分析器、后台 worker、管理 API 全都「写好了、有测试、
//	从未被调用」⇒「错误信息的收集与分析」这条职责链产出为零。
//	本文件补上后台 worker（周期性地把队列里的错误送去 LLM 分析）。
//
// ★ 为什么用 bg.SpawnLoop（而不是裸 go func）：
//
//	本仓的 bg.Go / bg.SpawnLoop 自带 panic recover ⇒
//	worker 里的 panic 不会杀掉整个网关进程。
//	（裸 goroutine 的 panic 会**终止整个进程** —— 本仓已因此修过一次。）
package promptinjection

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// ErrorAnalysisWorker 周期性地处理错误分析队列。
type ErrorAnalysisWorker struct {
	analyzer *ErrorAnalyzer
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewErrorAnalysisWorker 创建后台 worker。
//
// interval <= 0 ⇒ 使用默认 5 分钟。
func NewErrorAnalysisWorker(analyzer *ErrorAnalyzer, interval time.Duration) *ErrorAnalysisWorker {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &ErrorAnalysisWorker{
		analyzer: analyzer,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动 worker 循环。
//
// ★ 幂等：重复调用只生效一次。
func (w *ErrorAnalysisWorker) Start(ctx context.Context) {
	if w == nil || w.analyzer == nil {
		return
	}
	slog.Info("pi_error_analysis_worker started", "interval", w.interval)
	bg.SpawnLoop(ctx, "pi_error_analysis.loop", func(ctx context.Context) {
		w.runOnce(ctx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("pi_error_analysis_worker stopping")
				return
			case <-w.stopCh:
				slog.Info("pi_error_analysis_worker stopped")
				return
			case <-ticker.C:
				w.runOnce(ctx)
			}
		}
	})
}

// Stop 停止 worker 循环。
func (w *ErrorAnalysisWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// runOnce 处理一批。
//
// ★ 每轮先做「孤儿回收」（ProcessBatch 内部已包含）与「批量分析」。
//
//	任何一轮的 panic 都被 bg.SpawnLoop 的 recover 收口 ⇒ 不杀进程。
func (w *ErrorAnalysisWorker) runOnce(ctx context.Context) {
	if w == nil || w.analyzer == nil {
		return
	}
	processed, err := w.analyzer.ProcessBatch(ctx)
	if err != nil {
		slog.Warn("pi_error_analysis_worker: batch failed", "error", err)
		return
	}
	if processed > 0 {
		slog.Info("pi_error_analysis_worker: batch completed", "processed", processed)
	}
}
