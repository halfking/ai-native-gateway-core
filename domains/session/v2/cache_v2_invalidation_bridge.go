package v2

// cache_v2_invalidation_bridge.go —— session 缓存的**跨实例**失效通道
// （2026-11-13 全面审计v3 53 增量移交轮，F-47 收口）。
//
// ── 为什么需要这个文件（F-47 缺口陈述）────────────────────────────────────
//
// SessionCacheV2 的 L1（进程内 LRU）/ L1.5（本地磁盘文件快照）是**进程内**
// 缓存，而它承载的判定是**跨进程共享事实**的快照：压缩链路在本实例 Set
// 新状态（L1+L1.5 本地写 + L2 Redis 共享写）后，**别的实例的 L1/L1.5 仍持有
// 旧状态，最多 30min 脏读**（LRU TTL 上界）。写后即读的敏感面 = 压缩元数据：
// 另一个实例基于旧压缩元数据再次压缩 ⇒ 重复压缩 / 截断口径漂移。
//
// ★ 与候选缓存的跨实例失效桥（provider/candidate_cache_invalidation.go，
//   2a27e0e22）**同构**：候选缓存那条桥只接了 provider 候选缓存一条链
//   ⇒ 本文件把同一形状接到 session 缓存上 —— **照抄形状，不发明第二套语义**
//   （一个通道名 + 一个可解析的载荷 + 一个带重连的订阅者）。
//
// ── 载荷与粒度 ────────────────────────────────────────────────────────────
//
// 本地失效的粒度本来就是「tenant+session」：Invalidate / Set 都按
// (tenantID, sessionID) 走 ⇒ 跨进程传同一对坐标就够了，与本地语义逐字一致。
// 载荷 = "all"（整库，留作将来整库失效用）或 "<tenant>\x1f<session>"
// （\x1f = ASCII unit separator；tenant/session 是 PG text，实际不含控制符；
// 解出非一体的载荷按脏数据跳过）。
//
// ── 失败语义（★ 照抄 candidate 桥，重要）──────────────────────────────────
//
//	· **发布失败** ⇒ 只记日志，不影响本地写/失效。远端实例最多多活 30min
//	  —— 这与「今天根本没有跨实例失效」是同一条上界 ⇒ 不构成新失效模式。
//	· **队列满** ⇒ 丢弃该条并记 Debug ⇒ 不阻塞压缩写路径。
//	· **订阅端断线** ⇒ 指数退避重连（照抄 candidate/URSM 订阅者）。
//	· **载荷不可解析** ⇒ 跳过该条，不阻断通道。
//	· **收到自己实例的广播** ⇒ 本地照常失效（L1/L1.5 删除后下一次 Get 从
//	  L2/L3 回填**刚写入的新状态**，语义自洽）——candidate 桥同款，不做
//	  self-tagging（少一份簿记；代价只是本实例一次多余的本地删除）。
//
// ★ 订阅端**只清本地两层（L1 + L1.5），不清 L2**：L2 是 Redis 共享源，
//   写方 Set 已经刷新了它 ⇒ 再删一遍是冗余 IO 且可能与写方的 Set 竞态
//   （删晚于写会丢掉刚写的新治理状态，等 TTL 或下次写才能补回）。

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// sessionCacheInvalidationChannel 是「session 缓存失效」的跨进程广播通道名。
	// 命名与候选缓存桥（llmgw:meta:candidate_cache_invalidation）同一族形状。
	sessionCacheInvalidationChannel = "llmgw:meta:session_cache_invalidation"

	// sessionCacheInvalidationAll 是「整库失效」的载荷（留作将来整库失效用）。
	sessionCacheInvalidationAll = "all"

	// sessionCacheInvalidationSep 分隔 tenant 与 session（ASCII unit separator）。
	sessionCacheInvalidationSep = "\x1f"

	// sessionCacheInvalidationPublishTimeout 单次发布的超时上限（后台发布器）。
	sessionCacheInvalidationPublishTimeout = 200 * time.Millisecond

	// sessionCacheInvalidationQueueSize 待发布队列容量；满 ⇒ 丢弃（见失败语义）。
	sessionCacheInvalidationQueueSize = 1024

	// sessionCacheInvalidationReconnectBase 订阅者重连的初始退避。
	sessionCacheInvalidationReconnectBase = 100 * time.Millisecond
)

// encodeSessionCacheInvalidation 把失效坐标编码成线上载荷。
//
// tenantID 与 sessionID 皆空 ⇒ 整库失效（"all"）。
func encodeSessionCacheInvalidation(tenantID, sessionID string) string {
	if tenantID == "" && sessionID == "" {
		return sessionCacheInvalidationAll
	}
	return tenantID + sessionCacheInvalidationSep + sessionID
}

// decodeSessionCacheInvalidation 解析线上载荷。
//
// 返回 (tenantID, sessionID, ok)：
//   - "all" 且 ok=true ⇒ 整库失效
//   - ok=false ⇒ 载荷不可解析，调用方**跳过**该条（不阻断通道）。
//     空段坐标（"\x1f" / "a\x1f"）按脏数据拒：空 tenant/session 不是合法
//     缓存键，且 "\x1f" 会与 "all" 语义歧义。
func decodeSessionCacheInvalidation(payload string) (string, string, bool) {
	p := strings.TrimSpace(payload)
	if p == "" {
		return "", "", false
	}
	if p == sessionCacheInvalidationAll {
		return "", "", true
	}
	tenant, session, found := strings.Cut(p, sessionCacheInvalidationSep)
	if !found || tenant == "" || session == "" {
		return "", "", false
	}
	return tenant, session, true
}

// sessionInvalidationEvent 是待广播的失效事件。
type sessionInvalidationEvent struct {
	tenantID  string
	sessionID string
	all       bool
}

// sessionInvalidationBridge 持有「发布器 + 订阅者」两侧的运行时状态。
type sessionInvalidationBridge struct {
	cache   *SessionCacheV2
	rdb     *redis.Client
	queue   chan sessionInvalidationEvent
	stop    context.CancelFunc
	wg      sync.WaitGroup
	ownsRdb bool // rdb 由本桥创建（wiring 用 addr 构造）⇒ stop 时一并 Close
}

// sessionInvalidationActive 是当前进程的桥（未启动时为 nil）。
//
// ★ atomic.Pointer 而非 mutex：发布点在 Set / Invalidate 路径上（压缩完成 /
// 会话失效），锁会成为串行点 ⇒ 与 candidate 桥同款原子读。
var sessionInvalidationActive atomic.Pointer[sessionInvalidationBridge]

// StartSessionCacheInvalidationBridge 启动跨实例失效桥：
//
//	① 订阅者：收别的进程广播的失效事件 ⇒ 本实例只清 L1+L1.5（**不再广播**，无回环）
//	② 发布者：把本实例产生的失效事件广播出去（Set / Invalidate 触发）
//
// ★ 没有 Redis（rdb == nil / cache == nil）时**整条桥不启动** ⇒ 行为退回到
//
//	「进程内失效 + LRU TTL 上界」，不构成新失效模式。
func StartSessionCacheInvalidationBridge(ctx context.Context, cache *SessionCacheV2, rdb *redis.Client) {
	if rdb == nil || cache == nil {
		return
	}
	startSessionCacheInvalidationBridge(ctx, cache, rdb, false)
}

// StartSessionCacheInvalidationBridgeWithAddr 是装配侧的便捷形态：由 addr/db
// 构造桥专用 Redis 客户端（与 L2 的连接池分开，订阅连接独立），停桥时一并关闭。
func StartSessionCacheInvalidationBridgeWithAddr(ctx context.Context, cache *SessionCacheV2, redisAddr string, redisDB int) {
	if cache == nil || redisAddr == "" {
		return
	}
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr, DB: redisDB})
	startSessionCacheInvalidationBridge(ctx, cache, rdb, true)
}

func startSessionCacheInvalidationBridge(ctx context.Context, cache *SessionCacheV2, rdb *redis.Client, ownsRdb bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	bridgeCtx, cancel := context.WithCancel(ctx)

	b := &sessionInvalidationBridge{
		cache:   cache,
		rdb:     rdb,
		queue:   make(chan sessionInvalidationEvent, sessionCacheInvalidationQueueSize),
		stop:    cancel,
		ownsRdb: ownsRdb,
	}

	if !sessionInvalidationActive.CompareAndSwap(nil, b) {
		// 已有活桥 ⇒ 不重复启动（double-start 守卫照抄 candidate/URSM）；
		// 刚建的客户端立即关闭，不留孤儿连接。
		cancel()
		if ownsRdb {
			_ = rdb.Close()
		}
		return
	}

	b.wg.Add(2)
	go b.runPublisher(bridgeCtx)
	go b.runSubscriber(bridgeCtx)

	slog.Info("session cache cross-instance invalidation bridge started",
		"channel", sessionCacheInvalidationChannel,
		"queue_size", sessionCacheInvalidationQueueSize)
}

// StopSessionCacheInvalidationBridge 停掉桥（进程停机路径用）。
func StopSessionCacheInvalidationBridge() {
	b := sessionInvalidationActive.Swap(nil)
	if b == nil {
		return
	}
	b.stop()
	b.wg.Wait()
	if b.ownsRdb {
		_ = b.rdb.Close()
	}
}

// publishSessionCacheInvalidation 把一次本地失效广播给别的进程。
//
// ★ 非阻塞：队列满 ⇒ 丢弃（记 Debug）⇒ 绝不把 Redis 延迟带进压缩写路径。
// ★ 桥未启动（无 Redis / 未装配）⇒ 直接返回 ⇒ 本地写/失效照旧生效。
func publishSessionCacheInvalidation(tenantID, sessionID string) {
	b := sessionInvalidationActive.Load()
	if b == nil {
		return
	}
	ev := sessionInvalidationEvent{tenantID: tenantID, sessionID: sessionID, all: tenantID == "" && sessionID == ""}
	select {
	case b.queue <- ev:
	default:
		slog.Debug("session cache invalidation queue full; dropping broadcast",
			"tenant_id", tenantID, "session_id", sessionID)
	}
}

func (b *sessionInvalidationBridge) runPublisher(ctx context.Context) {
	defer b.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-b.queue:
			b.publishOne(ctx, ev)
		}
	}
}

func (b *sessionInvalidationBridge) publishOne(ctx context.Context, ev sessionInvalidationEvent) {
	pubCtx, cancel := context.WithTimeout(ctx, sessionCacheInvalidationPublishTimeout)
	defer cancel()
	payload := encodeSessionCacheInvalidation(ev.tenantID, ev.sessionID)
	if err := b.rdb.Publish(pubCtx, sessionCacheInvalidationChannel, payload).Err(); err != nil {
		// 发布失败不阻断本地写：本地已经落了，远端最多多活一个 LRU TTL。
		slog.Warn("session cache invalidation broadcast failed",
			"tenant_id", ev.tenantID, "session_id", ev.sessionID, "error", err)
	}
}

func (b *sessionInvalidationBridge) runSubscriber(ctx context.Context) {
	defer b.wg.Done()
	backoff := sessionCacheInvalidationReconnectBase
	for ctx.Err() == nil {
		pubsub := b.rdb.Subscribe(context.Background(), sessionCacheInvalidationChannel)
		closed := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = pubsub.Close()
			case <-closed:
			}
		}()

		for {
			msg, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				break
			}
			tenant, session, ok := decodeSessionCacheInvalidation(msg.Payload)
			if !ok {
				// 脏载荷：跳过该条，不阻断通道。
				continue
			}
			// ★ 只清本地两层（L1+L1.5），不清 L2，也**不再广播** ⇒ 无回环。
			if tenant == "" && session == "" {
				b.cache.invalidateAllStaleLocal(ctx)
				continue
			}
			b.cache.invalidateStaleLocal(ctx, tenant, session)
		}

		_ = pubsub.Close()
		close(closed)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("session cache invalidation subscriber reconnecting",
			"channel", sessionCacheInvalidationChannel, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}
