// Package requestobserver — **统一的请求级观察者**。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §4-C3 / §6-P1-1）指出：magpie 有 `usage.Observer`
// （`Serve` / `Bodies` / `Begin` / `Ledger`，`Watch` 收 `Attempt` / `End`），
// 且它的**队列语义**是：
//
//	「`onUsage`（250ms，队列 1024，**满了丢弃并计数，请求永不等待**）」
//
// 本仓的现状（§3.4 / §4-C3）：有采集、有看板，但**没有统一的请求级观察者接口**
// ⇒ 「谁在看这次请求」「看到了什么」没有单一事实源，每个观测面各写各的。
//
// # 本包的三条硬约束
//
//	① **观察者永不影响请求结果** —— 发布的路径上不允许出现阻塞、错误返回、
//	   观察者 panic 的传播。观察者炸了 ⇒ 事件被吞，**请求照常完成**。
//	② **有界队列，满则丢弃并计数，请求永不等待** —— 发布是**非阻塞**的
//	   （`select` + `default`），队列满时立刻丢弃并累加计数。
//	③ **字段级 allow-list** —— 观察者拿到的是 `RequestInfo` / `AttemptInfo` /
//	   `ResultInfo`，**不含**请求体、响应体、凭据明文、连接参数。
//
// # 为什么"丢弃并计数"而不是"阻塞等"
// magpie 的原话是「满了丢弃并计数，**请求永不等待**」——
// 一个观测面**慢**或**挂**时，请求的时延与成功率**不得**被它拖垮。
// ⇒ 本包的丢弃计数是**可观测的**（`Dropped()`）⇒ 丢弃不是静默的。
//
// # 名字
// ★ 本仓的 `internal/collector`、`internal/telemetry`、`domains/collector` 各自
//   采集面各有其法；本包**不替代**它们，只提供**请求级**的统一入口。
//
// # 引用
//
//	docs/subsystems/request-surface.md —— 请求面设计页
//	docs/audit/2026-10-24-magpie-v0.1.1367-改造落地审计.md —— 落地审计

package requestobserver

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// EventKind 是观察事件的类型。
type EventKind string

const (
	// EventBegin：请求开始。
	EventBegin EventKind = "begin"
	// EventAttempt：一次上游尝试结束。
	EventAttempt EventKind = "attempt"
	// EventEnd：请求终结。
	EventEnd EventKind = "end"
)

// RequestInfo 是"请求开始"时可见的信息（**字段级 allow-list**）。
type RequestInfo struct {
	// RequestID 是请求标识（与网关的 X-Request-Id 一致）。
	RequestID string
	// TenantID 是租户标识。
	TenantID string
	// SessionKey 是会话键。
	SessionKey string
	// ClientModel 是客户端请求的模型名。
	ClientModel string
	// StartedAt 是请求开始时刻。
	StartedAt time.Time
}

// AttemptInfo 是一次上游尝试的信息（**字段级 allow-list**）。
type AttemptInfo struct {
	// RequestID 对应的事件请求。
	RequestID string
	// Attempt 是第几次尝试（从 1 开始）。
	Attempt int
	// ProviderID 是供应商 ID。
	ProviderID int
	// CredentialID 是凭据 ID（**标识**，不是凭据本身）。
	CredentialID int
	// OutboundModel 是出站模型名。
	OutboundModel string
	// Success 表示这次尝试是否成功。
	Success bool
	// StatusCode 是上游返回的状态码（0 = 没有响应）。
	StatusCode int
	// ErrorKind 是失败类别（见 errorsx.ErrorKind 的字符串形态）。
	ErrorKind string
	// LatencyMS 是这次尝试的耗时（毫秒）。
	LatencyMS int
	// TTFTMS 是首字节耗时（毫秒，0 = 没有首字节）。
	TTFTMS int
	// TokensIn / TokensOut 是本次尝试的令牌数。
	TokensIn  int
	TokensOut int
	// At 是尝试结束时刻。
	At time.Time
}

// ResultInfo 是请求终结时的信息（**字段级 allow-list**）。
type ResultInfo struct {
	// RequestID 对应的请求。
	RequestID string
	// Success 表示整体是否成功。
	Success bool
	// StatusCode 是返回给客户端的状态码。
	StatusCode int
	// ErrorKind 是最终失败类别。
	ErrorKind string
	// LatencyMS 是整体耗时（毫秒）。
	LatencyMS int
	// TokensIn / TokensOut 是整体令牌数。
	TokensIn  int
	TokensOut int
	// Attempts 是尝试次数。
	Attempts int
	// At 是终结时刻。
	At time.Time
}

// Observer 是请求级观察者。
//
// ★ **契约**：实现方**不得**假设自己被同步调用；Hub 在独立 goroutine 里
//
//	投递事件；实现方**不得**阻塞（Hub 不等它）。
type Observer interface {
	// Begin 收到"请求开始"。
	Begin(ctx context.Context, req RequestInfo)
	// Watch 收到"一次尝试结束"。
	Watch(ctx context.Context, attempt AttemptInfo)
	// End 收到"请求终结"。
	End(ctx context.Context, result ResultInfo)
}

// ObserverFuncs 便于只实现其中一部分的观察者。
type ObserverFuncs struct {
	// OnBegin 可选。
	OnBegin func(ctx context.Context, req RequestInfo)
	// OnWatch 可选。
	OnWatch func(ctx context.Context, attempt AttemptInfo)
	// OnEnd 可选。
	OnEnd func(ctx context.Context, result ResultInfo)
}

// Begin 实现 Observer。
func (o ObserverFuncs) Begin(ctx context.Context, req RequestInfo) {
	if o.OnBegin != nil {
		o.OnBegin(ctx, req)
	}
}

// Watch 实现 Observer。
func (o ObserverFuncs) Watch(ctx context.Context, attempt AttemptInfo) {
	if o.OnWatch != nil {
		o.OnWatch(ctx, attempt)
	}
}

// End 实现 Observer。
func (o ObserverFuncs) End(ctx context.Context, result ResultInfo) {
	if o.OnEnd != nil {
		o.OnEnd(ctx, result)
	}
}

// queuedEvent 是队列里的一项（带事件类别）。
type queuedEvent struct {
	// ctx 是**发布方**的上下文（请求的 ctx）。
	//
	// ★ 刻意**不**用分发 goroutine 的 ctx：观察者应当看到"这次请求"自己的
	//   上下文（例如请求已被取消 ⇒ ctx 已取消 ⇒ 观察者可以据此判断）。
	//   ⇒ 但观察者**不得**依赖 ctx 做事（它可能已经取消）。
	ctx     context.Context
	kind    EventKind
	request RequestInfo
	attempt AttemptInfo
	result  ResultInfo
}

// DefaultQueueSize 是队列默认长度。
//
// ★ magpie 的 `onUsage` 队列是 1024（internal/middleware 的 onUsage 语义）。
const DefaultQueueSize = 1024

// Hub 是观察者的分发中心：有界队列 + 丢弃计数 + 非阻塞发布。
type Hub struct {
	queue chan queuedEvent

	dropped   atomic.Uint64
	delivered atomic.Uint64

	mu        sync.RWMutex
	observers []Observer

	// enabled 为假 ⇒ **不观测、无开销**（发布路径上只有一次原子读）。
	enabled atomic.Bool

	stopOnce sync.Once
	stopped  chan struct{}
}

// NewHub 造一个观察者中心。
//
// queueSize ≤ 0 时用 DefaultQueueSize。
func NewHub(queueSize int) *Hub {
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	h := &Hub{
		queue:   make(chan queuedEvent, queueSize),
		stopped: make(chan struct{}),
	}
	h.enabled.Store(true)
	return h
}

// Enabled 报告观察面是否开启。
func (h *Hub) Enabled() bool {
	return h != nil && h.enabled.Load()
}

// SetEnabled 打开/关闭观察面。
//
// ★ 关闭 ⇒ **无观测、无开销**（发布路径上只有一次原子读就返回）。
func (h *Hub) SetEnabled(on bool) {
	if h == nil {
		return
	}
	h.enabled.Store(on)
}

// Register 登记一个观察者。
func (h *Hub) Register(o Observer) {
	if h == nil || o == nil {
		return
	}
	h.mu.Lock()
	h.observers = append(h.observers, o)
	h.mu.Unlock()
}

// Unregister 注销一个观察者。
func (h *Hub) Unregister(o Observer) {
	if h == nil || o == nil {
		return
	}
	h.mu.Lock()
	for i, cur := range h.observers {
		if cur == o {
			h.observers = append(h.observers[:i], h.observers[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
}

// ObserverCount 返回当前登记的观察者数量。
func (h *Hub) ObserverCount() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.observers)
}

// Dropped 返回**因队列满而被丢弃**的事件数。
//
// ★ 丢弃是**可观测的** ⇒ 队列满不是静默失效。
func (h *Hub) Dropped() uint64 {
	if h == nil {
		return 0
	}
	return h.dropped.Load()
}

// Delivered 返回**成功投递给观察者**的事件数。
func (h *Hub) Delivered() uint64 {
	if h == nil {
		return 0
	}
	return h.delivered.Load()
}

// Publish 发布一个事件 —— **永不阻塞**。
//
// ★ 队列满 ⇒ **立刻丢弃并计数**（请求**永不等待**）。
// ★ 关闭 ⇒ 直接返回（无观测、无开销）。
func (h *Hub) Publish(ev queuedEvent) {
	if h == nil || !h.enabled.Load() {
		return
	}
	select {
	case h.queue <- ev:
	default:
		h.dropped.Add(1)
	}
}

// Begin 发布"请求开始"。
func (h *Hub) Begin(ctx context.Context, req RequestInfo) {
	if h == nil {
		return
	}
	h.Publish(queuedEvent{ctx: ctx, kind: EventBegin, request: req})
}

// Watch 发布"一次尝试结束"。
func (h *Hub) Watch(ctx context.Context, attempt AttemptInfo) {
	if h == nil {
		return
	}
	h.Publish(queuedEvent{ctx: ctx, kind: EventAttempt, attempt: attempt})
}

// End 发布"请求终结"。
func (h *Hub) End(ctx context.Context, result ResultInfo) {
	if h == nil {
		return
	}
	h.Publish(queuedEvent{ctx: ctx, kind: EventEnd, result: result})
}

// Start 启动分发 goroutine（直到 ctx 取消或 Stop 被调用）。
//
// ★ 观察者的 panic 在这里被**兜住** ⇒ 观察者炸了**不影响请求**。
func (h *Hub) Start(ctx context.Context) {
	if h == nil {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.stopped:
				return
			case ev := <-h.queue:
				h.deliver(ctx, ev)
			}
		}
	}()
}

// Stop 停止分发 goroutine。
func (h *Hub) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stopped) })
}

// deliver 把一个事件投给所有观察者（**逐个隔离 panic**）。
func (h *Hub) deliver(ctx context.Context, ev queuedEvent) {
	h.mu.RLock()
	observers := make([]Observer, len(h.observers))
	copy(observers, h.observers)
	h.mu.RUnlock()

	for _, o := range observers {
		// ★ 每个观察者**独立**兜 panic ⇒ 一个观察者炸了不影响其它观察者，
		//   更不影响请求（请求根本不在这个 goroutine 里）。
		evCtx := ev.ctx
		if evCtx == nil {
			evCtx = ctx
		}
		func(o Observer) {
			defer func() { _ = recover() }()
			switch ev.kind {
			case EventBegin:
				o.Begin(evCtx, ev.request)
			case EventAttempt:
				o.Watch(evCtx, ev.attempt)
			case EventEnd:
				o.End(evCtx, ev.result)
			}
		}(o)
	}
	h.delivered.Add(1)
}

// ── 进程级默认观察中心 ────────────────────────────────────────────────────

var (
	defaultMu  sync.RWMutex
	defaultHub *Hub
)

// Default 返回进程级默认观察中心（**懒启动**，第一次调用时建并起分发 goroutine）。
//
// ★ 为什么要有"默认"：本仓的观察面是**进程内**的（不像 PG 那样跨实例），
//
//	而 v1 请求路径（domains/streaming、domains/dispatch）都是**深层调用**，
//	把 hub 一路传下去会污染很多签名 ⇒ 用进程级默认 + **显式注入点**
//	（SetDefault）兼顾。
//
// ★ 默认**不注册任何观察者** ⇒ 没有观察者时，观察面只多一次原子读与一次
//
//	非阻塞入队（队列 1024）⇒ 开销可忽略；`SetEnabled(false)` 可彻底关掉。
func Default() *Hub {
	defaultMu.RLock()
	h := defaultHub
	defaultMu.RUnlock()
	if h != nil {
		return h
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultHub == nil {
		defaultHub = NewHub(DefaultQueueSize)
		defaultHub.Start(context.Background())
	}
	return defaultHub
}

// SetDefault 替换进程级默认观察中心。
//
// ★ 传 nil ⇒ **关闭观察面**（Default() 之后会再建一个空的）。
func SetDefault(h *Hub) {
	defaultMu.Lock()
	defaultHub = h
	defaultMu.Unlock()
}
