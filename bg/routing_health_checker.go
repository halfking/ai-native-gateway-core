package bg

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RoutingHealthChecker struct {
	db       *pgxpool.Pool
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once

	// skipped 记录**本轮**因前提消失而没有执行的检查的 reason 键。
	//
	// 2026-10-20（R51 续轮）：停写期间 recorded_cost_is_negative 读的是
	// 已冻结的 `_request_logs` ⇒ 「有没有新出现的负成本」不可判定。
	// 跳过本身**必须**可观测：安静地 return 0 会让 findings 计数把
	// 「本轮没跑」读成「扫了没发现问题」。见 bg/s4_scan_skip_metrics.go。
	//
	// 形态与 bg/ledger_reconciliation.go 的同名字段一致（skippedMu /
	// skipped / markSkipped / resetSkipped / SkippedChecks），
	// 刻意保持同形：这两个 worker 是同一族机制，形状分叉会让下一个人
	// 以为「跳过」有两种语义。
	skippedMu sync.Mutex
	skipped   []string
}

func NewRoutingHealthChecker(db *pgxpool.Pool) *RoutingHealthChecker {
	interval := 15 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_HEALTH_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	return &RoutingHealthChecker{db: db, interval: interval, stopCh: make(chan struct{})}
}

func (w *RoutingHealthChecker) Start(ctx context.Context) {
	slog.Info("routing_health_checker started", "interval", w.interval)
	SpawnLoop(ctx, "routing_health_checker.loop", func(context.Context) {
		w.runOnce(ctx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("routing_health_checker stopping")
				return
			case <-w.stopCh:
				slog.Info("routing_health_checker stopped")
				return
			case <-ticker.C:
				w.runOnce(ctx)
			}
		}
	})
}

func (w *RoutingHealthChecker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// SkippedChecks returns the machine-readable reason keys of checks that were
// skipped in the most recent run, in execution order. Empty means every
// check actually ran.
func (w *RoutingHealthChecker) SkippedChecks() []string {
	if w == nil {
		return nil
	}
	w.skippedMu.Lock()
	defer w.skippedMu.Unlock()
	return append([]string(nil), w.skipped...)
}

// markSkipped records one skip reason for the current round.
func (w *RoutingHealthChecker) markSkipped(reason string) {
	w.skippedMu.Lock()
	defer w.skippedMu.Unlock()
	w.skipped = append(w.skipped, reason)
}

// resetSkipped clears the skip list at the start of a round.
//
// 归零是显式的：不归零的话，「本轮跳过、下一轮真跑了」会永远显示为跳过
// （与 bg/s4_scan_skip_metrics.go 里 §9.35 M3 那个 bug 同一形状）。
func (w *RoutingHealthChecker) resetSkipped() {
	w.skippedMu.Lock()
	defer w.skippedMu.Unlock()
	w.skipped = nil
}

func (w *RoutingHealthChecker) runOnce(ctx context.Context) {
	// 本轮先清空跳过列表，再把本轮的跳过发布出去。
	w.resetSkipped()
	// Publish "this round did not execute" to /metrics. Deferred so it covers
	// every return path (including ones added later) — the skipped list is read
	// at exit, after the checks have had their chance to markSkipped.
	// See bg/s4_scan_skip_metrics.go.
	defer recordS4ScanSkipState(s4ScanWorkerRoutingHealthChecker, w.SkippedChecks(), time.Now())

	crit, warn, err := runChecksWithRecorder(ctx, w.db, AllHealthChecks(), w)
	if err != nil {
		slog.Error("routing_health_checker: run failed", "error", err)
		return
	}
	slog.Info("routing_health_checker: run completed", "new_critical", crit, "new_warning", warn)
}
