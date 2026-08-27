// Package admin — 2026-08-25: LiveStreamSSEHub 异步化 (fix-154).
//
// 背景: Run() 的主事件循环此前同步执行两类慢操作, 会阻塞循环里的所有
// case (注册/注销、普通 request 广播、心跳、queue/node 推送):
//
//   (a) actionTicker case 里同步跑 pollLiveActions → deliverNewActions →
//       resolveActionTenants + fanOutLifecycleActions → CredentialLabelsFor。
//       CredentialLabelsFor 冷缓存时要做一次 500ms 超时的 DB 批量查询
//       (credentialLabelForSQL, WHERE id = ANY($1)); DB 抖动时 250ms 一次
//       的 tick 会把主循环卡住最长 ~2.5s (actionReadTimeout 2s + 500ms),
//       期间所有普通 request 广播全部排队。
//   (b) Publish 先同步 store.Record (200ms 超时的 Redis 读改写 + per-request
//       SETNX 锁重试) 再 enqueueBroadcast, Redis 慢时把 telemetry 侧的
//       Publish 调用方也一起拖慢, 本地 SSE 可见性被 Redis 延迟。
//
// 本文件的两个 worker 把这两条慢链移出主循环:
//
//   - runActionWorker: 单 goroutine 串行消费 action 触发, 执行整条
//     poll→deliver→resolve→fanOut 链。绝不并发多 worker —— actionCursor /
//     actionTenantIndex 的顺序语义依赖串行推进 (OBS-BE2 的 at-least-once
//     语义按 tick 串行才成立)。
//   - runRecordDrainer: 单 goroutine FIFO 消费 recordQueue, 逐条
//     store.Record。同一 request_id 的 in_progress → terminal 两个阶段
//     因此保持 Publish 调用顺序 (旧版两个调用方 goroutine 并发抢跑
//     Record 反而没有这个保证); 跨 request_id 本就无顺序依赖 (ZSET score
//     来自 req.Ts 而非墙钟), store.Record 内部的 per-request_id SETNX 锁
//     (live_stream_redis_store.go) 继续兜底跨实例并发。
//
// 两个 worker 都跟随 stopCh 退出; Stop() 用有界 join 等待 (joinAsyncWorkers),
// Run 从未被调用时 worker 未启动、done chan 为 nil, join 直接跳过 —— Stop
// 对"Run 未调用"安全, 绝不挂起。
package admin

import (
	"context"
	"log/slog"
	"time"
)

const (
	// recordQueueCapacity bounds the async Record backlog (2026-08-25).
	// 正常一条 Record 是几十 ms 的 pipeline; 256 的容量在 8k req/min 的
	// 154 网关上约等于 2s 的突发缓冲。满了就丢弃 + 计数 —— Redis 持续
	// 慢时背压必须体现在丢计数 (recordDrops) 而不是把慢传导回调用方。
	recordQueueCapacity = 256
	// recordDropsLog limits the drop-log volume: 每 N 次丢弃打一条 Warn,
	// 与 incidentUpdateDropsLog 的限频模式一致。
	recordDropsLog = 50
	// asyncWorkerJoinTimeout bounds Stop() 的 worker join。action worker
	// 最坏一次 poll ≈ actionReadTimeout(2s) + CredentialLabelsFor(500ms);
	// record drainer 最坏要排空 cap 条 × 200ms ≈ 51s。
	// P1-9 fix (2026-08-28): Increased from 3s to 60s to allow drainer to
	// complete queue flush before process exit, reducing data loss during
	// graceful shutdown (k8s SIGTERM window is typically 30-60s).
	asyncWorkerJoinTimeout = 60 * time.Second
	// liveStreamRecordWriteTimeout 是单条 store.Record 的写超时,
	// 2026-08-25 从 Publish 的两处内联 200ms 常量化 (drainer 与同步兜底
	// 共用同一预算, 行为与旧同步路径一致)。
	liveStreamRecordWriteTimeout = 200 * time.Millisecond
)

// ── action worker ───────────────────────────────────────────────────────────

// startActionWorker 启动唯一的 action 轮询 worker (幂等)。由 Run() 调用;
// stopCh 已关闭时直接不启动 —— 这是 Stop 先于 Run 的边界 (契约上调用方
// 应先 Run 再 Stop, main.go 即此顺序, 但防御性处理没有代价)。
func (h *LiveStreamSSEHub) startActionWorker() {
	h.asyncMu.Lock()
	defer h.asyncMu.Unlock()
	select {
	case <-h.stopCh:
		return
	default:
	}
	if h.actionWorkerStarted {
		return
	}
	h.actionWorkerStarted = true
	h.actionWorkerDone = make(chan struct{})
	go h.runActionWorker()
}

// runActionWorker 串行消费 action 触发。pollLiveActions 内部自带
// actionReadTimeout(2s) 预算, 因此 worker 停止延迟最坏 ≈ 2.5s (含
// CredentialLabelsFor 的 500ms), 在 asyncWorkerJoinTimeout 的 3s 之内。
// 手工构造的 hub 若 actionTrigger 为 nil, 该 case 永不就绪, worker 只是
// 空转到 stopCh —— 零值安全, 不会 panic 也不会忙轮询。
func (h *LiveStreamSSEHub) runActionWorker() {
	defer close(h.actionWorkerDone)
	for {
		select {
		case <-h.stopCh:
			return
		case <-h.actionTrigger:
			// 串行执行整条链 (poll → deliverNewActions → resolveActionTenants
			// → fanOutLifecycleActions)。绝不并发多 worker: actionCursor 的
			// 推进与 actionTenantIndex 的写入顺序依赖串行。
			h.pollLiveActions()
		}
	}
}

// triggerActionPoll 非阻塞投递一次 action 轮询。chan cap 为 1, 发送永不
// 阻塞: worker 忙时本次 tick 合并 —— pollLiveActions 每次 LRANGE 最新 500
// 条并按 cursor 推进, worker 空闲后的下一次触发自然追赶, 不会漏事件
// (最多把两次 tick 的动作并成一个 batch, 端到端预算退化一个 tick, 仍远
// 优于旧版把主循环卡死 2.5s)。
func (h *LiveStreamSSEHub) triggerActionPoll() {
	select {
	case h.actionTrigger <- struct{}{}:
	default:
	}
}

// ── record drainer ──────────────────────────────────────────────────────────

// startRecordDrainer 启动唯一的 Record drainer (幂等)。未配置 Redis 时
// Record 本就是 no-op, 不启动空转 goroutine (与 runRedisSubscriber 的
// 启动条件一致)。由 Run() 调用。
func (h *LiveStreamSSEHub) startRecordDrainer() {
	if h.store == nil || h.cfg.RedisClient == nil {
		return
	}
	h.asyncMu.Lock()
	defer h.asyncMu.Unlock()
	select {
	case <-h.stopCh:
		return
	default:
	}
	if h.recordDrainerStarted || h.recordQueue == nil {
		// recordQueue == nil: 手工构造 (绕过 NewLiveStreamSSEHub) 的 hub ——
		// enqueueRecord 会对这种情况返回 false, Publish 走同步兜底。
		return
	}
	h.recordDrainerStarted = true
	h.recordDrainerDone = make(chan struct{})
	go h.runRecordDrainer()
}

// runRecordDrainer FIFO 逐条执行 store.Record。关停契约: stopCh 关闭后排空
// 已缓冲条目再退出 (上限 recordQueueCapacity×200ms ≈ 51s, 可能超过 Stop 的
// join 预算 —— joinAsyncWorkers 的 doc 详述该取舍), 避免突发流量后立刻 Stop
// 丢掉整个尾巴; Stop 的 asyncMu 串行化保证排空期间不会再有新条目入队
// (close(stopCh) 与 enqueueRecord 互斥, 见 Stop 的注释)。
func (h *LiveStreamSSEHub) runRecordDrainer() {
	defer close(h.recordDrainerDone)
	for {
		select {
		case <-h.stopCh:
			for {
				select {
				case req := <-h.recordQueue:
					h.recordToStore(req)
				default:
					return
				}
			}
		case req := <-h.recordQueue:
			h.recordToStore(req)
		}
	}
}

// enqueueRecord 把一条待写入 Redis 的请求放入异步队列。返回 false 表示
// drainer 不可用 (Run 未调用 —— 测试/嵌入式用法; Stop 之后; 手工构造的
// hub), 调用方应同步兜底以保持旧语义。返回 true 表示条目"已被接管"——
// 要么成功入队, 要么队列满已按背压策略丢弃计数 (recordDrops); 丢弃后
// 不再同步兜底, 否则会把 Redis 慢重新传导回 Publish 调用方, 违背本次
// 改动的目的。stopCh 检查与投递在同一 asyncMu 临界区、且 Stop 的
// close(stopCh) 持同一锁 —— 不存在"close 后投递成功但 drainer 已退出"
// 的滞留窗口 (见 Stop 注释)。
func (h *LiveStreamSSEHub) enqueueRecord(req LiveRequest) bool {
	h.asyncMu.Lock()
	defer h.asyncMu.Unlock()
	select {
	case <-h.stopCh:
		return false
	default:
	}
	if !h.recordDrainerStarted || h.recordQueue == nil {
		return false
	}
	select {
	case h.recordQueue <- req:
		return true
	default:
		h.recordDrops.Add(1)
		if n := h.recordDrops.Load(); n%recordDropsLog == 1 {
			slog.Warn("live stream record queue full, dropping redis record",
				"request_id", req.RequestID,
				"tenant_id", req.TenantID,
				"dropped", n)
		}
		return true
	}
}

// recordToStore 执行一次带 200ms 超时的 store.Record。Warn 日志字段与旧
// 同步路径逐字一致 ("live stream redis record failed" + request_id/
// tenant_id/model/provider/err), 运维既有告警关键字无需调整。
func (h *LiveStreamSSEHub) recordToStore(req LiveRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), liveStreamRecordWriteTimeout)
	defer cancel()
	if err := h.store.Record(ctx, req, h.instanceID); err != nil {
		slog.Warn("live stream redis record failed", "request_id", req.RequestID, "tenant_id", req.TenantID, "model", req.Model, "provider", req.ProviderCode, "err", err.Error())
	}
}

// ── shutdown join ───────────────────────────────────────────────────────────

// joinAsyncWorkers 有界等待异步 worker 退出, 由 Stop() 在 close(stopCh)
// 之后调用。共享一个总 deadline (3s): action worker 最坏一次 poll ≈
// actionReadTimeout(2s)+500ms, 在预算内; record drainer 的关停排空上限
// 是 recordQueueCapacity×200ms ≈ 51s, **可能超出本预算** —— 这是刻意取舍:
// Stop 不悬挂优先于排空完整性, 超时只 Warn, drainer 留在后台继续排空,
// 进程退出时随之消亡 (k8s SIGTERM 宽限期内通常已排完, 残余随进程丢失,
// 与 forwarder 关停不 drain 的取舍一致)。任何一个 worker 超时只 Warn。
// done chan 为 nil (worker 从未启动, 即 Run 未被调用) 时直接跳过, 保证
// Stop 永不挂起。
func (h *LiveStreamSSEHub) joinAsyncWorkers() {
	h.asyncMu.Lock()
	actionDone := h.actionWorkerDone
	recordDone := h.recordDrainerDone
	h.asyncMu.Unlock()
	deadline := time.NewTimer(asyncWorkerJoinTimeout)
	defer deadline.Stop()
	for _, w := range []struct {
		name string
		done chan struct{}
	}{
		{"action_worker", actionDone},
		{"record_drainer", recordDone},
	} {
		if w.done == nil {
			continue
		}
		select {
		case <-w.done:
		case <-deadline.C:
			slog.Warn("live stream: async worker join timed out, worker left running",
				"worker", w.name, "timeout", asyncWorkerJoinTimeout.String())
		}
	}
}

// recordDropsCount 暴露 record queue 的丢弃计数 (Stats() 汇总与测试断言用;
// recordDrops 本身是 atomic.Int64, 与 incidentDrops 的模式一致)。
func (h *LiveStreamSSEHub) recordDropsCount() int64 {
	return h.recordDrops.Load()
}
