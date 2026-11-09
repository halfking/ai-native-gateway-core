// passive_baseline_worker.go — 被动基线的后台聚合 worker：
// collector 的周期 Flush + handler 两端（factory/sink）的一体化实现。
//
// passive_baseline_worker.go — the background baseline worker: periodic
// Flush of the passive collector, doubling as the handler-side factory
// and sink so wiring is a single SetPassiveTiming(w, w).
package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// PassiveBaselineFlushInterval 是基线刷库周期。
// 窗口越短合并越频繁、基线越平滑；5min 与校准缓存同档（慢变量）。
const PassiveBaselineFlushInterval = 5 * time.Minute

// PassiveBaselineHeartbeatTicks 是心跳间隔（以 Flush 次数计）。
//
// ★ 为什么需要心跳：基线 worker 在**没有流量时是静默的**（Flush 无节点
// ⇒ 不打日志）⇒ 运维侧看到「零日志」无法区分「worker 活着但没流量」与
// 「worker 没起来 / 已死」⇒ 观测面必须有「活着」的正面证据。
// 6 ticks × 5min = 30min。
const PassiveBaselineHeartbeatTicks = 6

// PassiveBaselineWorker 周期把累积的被动特征合并进 955 基线表。
//
// 同时实现 streaming.PassiveFeaturesSink（Submit）与
// streaming.PassiveTimingFactory（NewPassiveTimingTracker）——
// main 装配处一个 SetPassiveTiming(w, w) 接完两端。
type PassiveBaselineWorker struct {
	collector *modelcredibility.PassiveCollector
	sink      modelcredibility.PassiveBaselineSink
	interval  time.Duration

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}

	// ticks统计自 Start 以来的 Flush 次数（心跳节拍）；flushes 是
	// 自上次心跳以来累计落库节点数。
	ticks   int
	flushes int
}

// NewPassiveBaselineWorker 创建 worker。sink 为 *modelcredibility.Store
// 或测试替身；interval<=0 用默认 5min。
func NewPassiveBaselineWorker(sink modelcredibility.PassiveBaselineSink, interval time.Duration) *PassiveBaselineWorker {
	if interval <= 0 {
		interval = PassiveBaselineFlushInterval
	}
	return &PassiveBaselineWorker{
		collector: modelcredibility.NewPassiveCollector(),
		sink:      sink,
		interval:  interval,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Submit 实现 streaming.PassiveFeaturesSink（完成点出口）。
func (w *PassiveBaselineWorker) Submit(features *modelcredibility.PassiveFeatures) {
	if w == nil || w.collector == nil {
		return
	}
	w.collector.Add(features)
}

// NewPassiveTimingTracker 实现 streaming.PassiveTimingFactory（每请求
// 一个独立采样器）。
func (w *PassiveBaselineWorker) NewPassiveTimingTracker() *streaming.PassiveTimingTracker {
	if w == nil {
		return nil
	}
	return streaming.NewPassiveTimingTracker(time.Time{})
}

// Start 启动周期 Flush。重复 Start 幂等（no-op）。
func (w *PassiveBaselineWorker) Start(ctx context.Context) {
	if w == nil || w.sink == nil {
		return
	}
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()

	// 启动正面证据：运维要能区分「没起来」与「起来了但没流量」。
	slog.Info("passive baseline worker started",
		"flush_interval", w.interval.String(),
		"heartbeat_every_ticks", PassiveBaselineHeartbeatTicks)

	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				w.flushOnce(context.Background(), "shutdown")
				return
			case <-w.stop:
				w.flushOnce(context.Background(), "stop")
				return
			case <-ticker.C:
				w.flushOnce(ctx, "tick")
			}
		}
	}()
}

// Stop 停止并把残余窗口刷净（幂等；未启动时 no-op）。
func (w *PassiveBaselineWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.done
}

// flushOnce 执行一次 Flush 并记日志（失败不 panic——基线是观测面，
// 不是关键路径；下一窗口重试合并）。
func (w *PassiveBaselineWorker) flushOnce(ctx context.Context, why string) {
	flushed, err := w.collector.Flush(ctx, w.sink)
	if err != nil {
		slog.Warn("passive baseline flush had failures",
			"why", why, "flushed", flushed, "err", err)
		return
	}
	if flushed > 0 {
		slog.Info("passive baseline flushed", "why", why, "nodes", flushed)
	}
	w.beat(why, flushed)
}

// beat 记心跳：每 PassiveBaselineHeartbeatTicks 次 Flush 打一条 Info，
// 即使一个节点都没落库也打（这正是「活着但空闲」的正面证据）。
func (w *PassiveBaselineWorker) beat(why string, flushed int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.ticks++
	w.flushes += flushed
	ticks, flushes := w.ticks, w.flushes
	w.mu.Unlock()

	if ticks%PassiveBaselineHeartbeatTicks != 0 {
		return
	}
	slog.Info("passive baseline worker alive",
		"why", why,
		"ticks", ticks,
		"nodes_written_since_last_beat", flushes,
		"pending", w.collector.Pending())
}

// 编译期锁：worker 必须同时满足 handler 两端接口。
var (
	_ streaming.PassiveFeaturesSink  = (*PassiveBaselineWorker)(nil)
	_ streaming.PassiveTimingFactory = (*PassiveBaselineWorker)(nil)
)
