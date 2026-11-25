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
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// ErrorAnalysisWorker 周期性地处理错误分析队列。
type ErrorAnalysisWorker struct {
	analyzer *ErrorAnalyzer
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
	// ★ 2026-11-17 第57增量：Start 幂等守卫。注释此前声称「幂等」但
	//   实现没有守卫 ⇒ 每次 Start 都再起一条循环（重复 LLM 花费）。
	started atomic.Bool

	// preBatch 是每轮领取之前先跑的一步（2026-11-25：请求错误发现器挂在这里——
	// 先发现、再分析，同一节拍；nil ⇒ 不跑）。装配期设置一次，之后只读。
	preBatch func(ctx context.Context)
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
// ★ 幂等：重复调用只生效一次（第57增量：真正落上守卫——
//
//	原实现注释声称幂等但没有 started 位 ⇒ 每次 Start 多起一条循环）。
func (w *ErrorAnalysisWorker) Start(ctx context.Context) {
	if w == nil || w.analyzer == nil {
		return
	}
	if !w.started.CompareAndSwap(false, true) {
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

// SetPreBatch 设置每轮领取之前的前置步骤（装配期调用一次）。
func (w *ErrorAnalysisWorker) SetPreBatch(fn func(ctx context.Context)) {
	if w == nil {
		return
	}
	w.preBatch = fn
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
	if w.preBatch != nil {
		// 前置步骤自己兜住错误：发现失败不该让本轮的分析也停摆。
		w.preBatch(ctx)
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
