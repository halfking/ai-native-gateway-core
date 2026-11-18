package securityevent

// 出网 sink：把安全事件从网关内部送到外部（Memora）。
//
// # 为什么不直接在检测点写
//
// 安全检测大多跑在**请求热路径**上（异常检测是 HTTP 中间件，注入检测在
// 解析链上）。让这些地方同步发 HTTP，会把外部系统的抖动直接传导成用户请求
// 的延迟 —— 一个不可用的 Memora 不该让网关变慢。
//
// ⇒ sink 的形状与本仓既有的异步写面一致（domains/memory/client 的 Sink）：
//
//   - 单个有界 channel + N 个 worker；
//   - **队列满 ⇒ 丢事件**，绝不阻塞调用方（安全事件是观测数据，不是权威
//     状态；丢了要能被计数看见，不能把请求拖住）；
//   - 连续错误进入退避，避免把死掉的外部系统反复打满；
//   - 计数器（入队/丢弃/成功/失败）暴露给 /healthz 与指标。
//
// # 失败语义
//
// 写不进去**不影响请求本身**：检测结论已经生效（拦的拦了），事件只是它的
// 投影。失败只计数 + 日志。

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Writer 是安全事件的落地面。
//
// 抽成接口是为了让传输层可替换：当前实现写 Memora（见 memora.go），
// 将来换成 v2 typed ingest 或别的目的地时，sink 与契约都不用动。
type Writer interface {
	// WriteSecurityEvent 把一条事件写到目的地。返回错误即失败，sink 会
	// 计数并按退避重试。
	WriteSecurityEvent(ctx context.Context, event Event) error
}

// Stats 是 sink 的运行计数。
type Stats struct {
	// Enqueued 是成功进入队列的事件数。
	Enqueued uint64
	// Dropped 是因为队列满被丢掉的事件数。
	Dropped uint64
	// Processed 是成功写出的事件数。
	Processed uint64
	// Errored 是写出失败的事件数。
	Errored uint64
	// Rejected 是因为不满足最小条件（缺租户/项目等）而拒绝的事件数。
	Rejected uint64
}

// SinkConfig 配置出网 sink。
type SinkConfig struct {
	// Writer 是落地面。nil ⇒ sink 处于禁用态，Publish 直接拒绝。
	Writer Writer
	// Workers 是并发写出的 goroutine 数。<=0 ⇒ 2。
	Workers int
	// QueueSize 是队列容量。<=0 ⇒ 256。
	QueueSize int
	// WriteTimeout 是单次写出的超时。<=0 ⇒ 5s。
	WriteTimeout time.Duration
	// ErrorBackoff 是连续失败后的退避间隔。<=0 ⇒ 5s。
	ErrorBackoff time.Duration
	// Logger 是日志器。nil ⇒ slog.Default()。
	Logger *slog.Logger
}

// Sink 是安全事件的出网 sink。
type Sink struct {
	writer       Writer
	queue        chan Event
	workers      int
	writeTimeout time.Duration
	errorBackoff time.Duration
	logger       *slog.Logger

	wg      sync.WaitGroup
	mu      sync.RWMutex
	started bool
	// closed 标记队列已被关闭（Stop 之后）。
	//
	// ★ 2026-11-18 审计轮 P2：Stop 此前**既不关队列也不排空** ——
	//	worker 在 ctx 取消时立刻退出 ⇒ 队列里已入队的事件**全部丢弃**
	//	（安全事件是攻击现场，丢了就再也补不回来）；而 Publish 仍会
	//	往一个没人消费的队列里塞（计数还涨）⇒ 静默丢失。
	//	⇒ Stop 关闭队列（此后 Publish 明确拒绝并计数），
	//	  worker 把队列里剩下的事件写完再退出。
	closed bool

	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	processed atomic.Uint64
	errored   atomic.Uint64
	rejected  atomic.Uint64

	// consecutiveErrors 记录连续失败次数，用于退避。
	consecutiveErrors atomic.Int64
}

// NewSink 构造出网 sink。
func NewSink(cfg SinkConfig) *Sink {
	workers := cfg.Workers
	if workers <= 0 {
		workers = 2
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 256
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 5 * time.Second
	}
	backoff := cfg.ErrorBackoff
	if backoff <= 0 {
		backoff = 5 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Sink{
		writer:       cfg.Writer,
		queue:        make(chan Event, queueSize),
		workers:      workers,
		writeTimeout: writeTimeout,
		errorBackoff: backoff,
		logger:       logger,
	}
}

// Enabled 报告 sink 是否可用（落地面已配置）。
func (s *Sink) Enabled() bool {
	return s != nil && s.writer != nil
}

// Start 启动 worker。幂等。
func (s *Sink) Start(ctx context.Context) {
	if s == nil || s.writer == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
}

// Stop 停止 worker 并等待在途写入结束。
//
// ★ 2026-11-18 审计轮 P2：Stop 现在**排空队列**。
//
//	原实现只等 wg，而 worker 在 Start 的 ctx 被取消时立刻 return
//	⇒ 队列里已入队的事件全部丢失（攻击现场补不回来），且 Publish
//	仍往无人消费的队列里塞（静默丢失 + 计数虚高）。
//	⇒ Stop 关闭队列：worker 读到「队列已空且已关闭」才退出，
//	  在途与排队的事件都被写出（每次写有独立超时）。
//	调用方必须**先 Stop 再取消 Start 的 ctx**（否则 ctx 取消会先于
//	排空生效，队列仍会被丢）—— cmd/gateway 的停机顺序已按此调整。
//
// ctx 取消后不再接新事件；ctx 只用于**等待**的时限，不用于丢弃队列。
func (s *Sink) Stop(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	started := s.started
	s.started = false
	if !s.closed {
		s.closed = true
		// 关闭队列：worker 排空后退出。此后 Publish 会明确拒绝。
		close(s.queue)
	}
	s.mu.Unlock()

	if !started {
		// 没启动过 ⇒ 没有 worker 会消费队列；队列已关闭，事件不会被消费。
		return
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		s.logger.Warn("security event sink stop timed out (queue may not be fully drained)",
			"pending", len(s.queue))
	}
}

// Publish 把一条事件送进队列。**永不阻塞**。
//
// 返回值表示事件是否被接受入队：false 意味着 sink 不可用、事件不满足最小
// 条件，或队列已满（此时事件被丢弃并计数）。
//
// 调用方**不应**因为返回 false 而改变请求的行为 —— 检测结论已经生效，
// 事件只是它的投影。
//
// ★ 刻意不接受 context：写出发生在 sink 自己的生命周期里（worker 用
// context.WithoutCancel 派生），调用方的请求 ctx 早已可能取消 —— 用它做
// 判据会**丢掉**恰恰是最该记录的那批事件（被取消的请求里的攻击尝试）。
func (s *Sink) Publish(event Event) bool {
	if s == nil || s.writer == nil {
		return false
	}
	if err := event.Validate(); err != nil {
		s.rejected.Add(1)
		s.logger.Warn("security event rejected", "error", err, "event_type", event.EventType, "code", event.Code)
		return false
	}
	// ★ 2026-11-18 审计轮 P2：Stop 之后队列已关闭 ⇒ 再往里发会 panic
	//	（send on closed channel）。⇒ 用读锁把「查 closed + 入队」与
	//	Stop 的「关队列」互斥（入队是非阻塞的，读锁持有时间极短）。
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		s.dropped.Add(1)
		s.logger.Warn("security event dropped (sink stopped)",
			"event_type", event.EventType, "code", event.Code)
		return false
	}
	select {
	case s.queue <- event:
		s.enqueued.Add(1)
		s.mu.RUnlock()
		return true
	default:
		// 队列满：丢事件并计数。**不阻塞调用方** —— 请求热路径上等一个
		// 外部系统是不可接受的。
		s.dropped.Add(1)
		s.mu.RUnlock()
		s.logger.Warn("security event dropped (queue full)",
			"event_type", event.EventType, "code", event.Code, "queue_size", cap(s.queue))
		return false
	}
}

// Stats 返回当前计数。
func (s *Sink) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{
		Enqueued:  s.enqueued.Load(),
		Dropped:   s.dropped.Load(),
		Processed: s.processed.Load(),
		Errored:   s.errored.Load(),
		Rejected:  s.rejected.Load(),
	}
}

// worker 从队列取事件并写出。
//
// ★ 2026-11-18 审计轮 P2：worker 现在**排空队列**才退出。
//
//	原实现 `case <-ctx.Done(): return` ⇒ ctx 一取消，队列里已入队的
//	事件全部丢失（攻击现场不可恢复）。⇒ 关闭信号（队列关闭 ⇒ ok=false）
//	优先于 ctx：先把剩下的写完（每次写有独立超时），再退出。
//	仍保留 ctx 取消的快速出口（未走 Stop 的兜底路径），避免 goroutine 泄漏。
func (s *Sink) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		// 优先排空（队列已关闭时 ok=false ⇒ 退出）。
		select {
		case event, ok := <-s.queue:
			if !ok {
				return
			}
			s.write(ctx, event)
			continue
		default:
		}
		select {
		case event, ok := <-s.queue:
			if !ok {
				return
			}
			s.write(ctx, event)
		case <-ctx.Done():
			return
		}
	}
}

// write 写出一条事件，并对连续失败做退避。
func (s *Sink) write(ctx context.Context, event Event) {
	// 退避：外部系统连续失败时，不要把它反复打满。
	if failures := s.consecutiveErrors.Load(); failures > 0 {
		wait := time.Duration(failures) * s.errorBackoff
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.writeTimeout)
	defer cancel()
	if err := s.writer.WriteSecurityEvent(writeCtx, event); err != nil {
		s.errored.Add(1)
		s.consecutiveErrors.Add(1)
		s.logger.Warn("security event write failed",
			"error", err, "event_type", event.EventType, "code", event.Code, "event_id", event.EventID)
		return
	}
	s.consecutiveErrors.Store(0)
	s.processed.Add(1)
}
