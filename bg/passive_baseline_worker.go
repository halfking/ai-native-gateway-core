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

// passiveBaselineFlushTimeout 是**退出路径**上最后一次刷库的时间上界。
//
// ★ 2026-11-11 审计 P2-M：★ 原实现退出时用 `context.Background()`（★ 无 deadline）
// ⇒ ★★ DB 挂起 / 网络不可达 ⇒ ★★ flushOnce 无限阻塞 ⇒ ★★ 进程**永不退出**
// ⇒ ★★ 且这发生在**退出路径**上 ⇒ ★★ 越是在故障时越容易卡死（★ 故障与退出叠加）。
// ⇒ ★★ 修法：★ 退出路径改用**有界** ctx（★ 30s）⇒ ★★ 刷库要么在 30s 内完成、
// ★★ 要么放弃这一轮（★ 下一轮 worker 起来会重新累积 ⇒ ★ 不丢数据，只延后落库）。
const passiveBaselineFlushTimeout = 30 * time.Second

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
	// stopOnce 保证 w.stop 只被 close 一次。
	// ★ 2026-11-11 审计 P2-L：★ 原实现用 `select { case <-w.stop: default: close(w.stop) }`
	// ⇒ ★★ select+default **不是原子** ⇒ ★★ 两个 goroutine 并发 Stop() 时可双双走到 default
	// ⇒ ★★ 第二个 close(w.stop) ⇒ ★★ panic: close of closed channel（★ 且 panic 发生在
	// ★★ shutdown 路径上 ⇒ ★★ 进程退出被 panic 打断 ⇒ ★★ 残余窗口没刷净）。
	// ⇒ ★★ 修法：★ 用 sync.Once 把「关通道」变成**恰好一次**（★ Stop 本身仍幂等）。
	stopOnce sync.Once

	// ticks统计自 Start 以来的 Flush 次数（心跳节拍）；flushes 是
	// 自上次心跳以来累计落库节点数（每次心跳发出后归零，见 beat）。
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
				fctx, cancel := boundedFlushCtx()
				w.flushOnce(fctx, "shutdown")
				cancel()
				return
			case <-w.stop:
				fctx, cancel := boundedFlushCtx()
				w.flushOnce(fctx, "stop")
				cancel()
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
	// ★ 2026-11-11 审计 P2-L：★ close 只做**恰好一次**（★ sync.Once）⇒ ★★ 并发 Stop() 不再 panic
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

// boundedFlushCtx 造一个**有界**的退出期刷库 ctx（★ 见 passiveBaselineFlushTimeout）。
//
// ★ 注意：★ 不能复用 Start 的那个 ctx（★ 它在 ctx.Done() 分支里**已经取消**了
// ★ ⇒ ★ 用它刷库会**立即失败** ⇒ ★ 等于「退出时不刷」）。
func boundedFlushCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), passiveBaselineFlushTimeout)
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
//
// ★ 口径（2026-11-10 修正）：日志键 `nodes_written_since_last_beat` 承诺的
// 口径是「**自上次心跳以来**」⇒ 发出心跳后必须把窗口计数归零。此前实现只累加
// 不归零 ⇒ 该键实际报的是「自 Start 以来累计」⇒ **字段名与实际值不符** ⇒
// 运维会把「历史累计很大」误读成「本窗口有写入」（而心跳的用途恰恰是
// 判断**本窗口**是否活跃）⇒ 归零，使名与值一致。
func (w *PassiveBaselineWorker) beat(why string, flushed int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.ticks++
	w.flushes += flushed
	ticks, flushes := w.ticks, w.flushes
	emit := ticks%PassiveBaselineHeartbeatTicks == 0
	if emit {
		// 窗口计数归零：下一个心跳只报「自本次心跳以来」。
		w.flushes = 0
	}
	w.mu.Unlock()

	if !emit {
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
