package provider

// candidate_cache_invalidation.go —— 候选缓存的**跨实例**失效通道
// （2026-11-11 缓存与路由状态即时同步轮）。
//
// ── 为什么需要这个文件（缺口陈述）────────────────────────────────────────
//
// 候选缓存（Client.candCache）是**进程内**缓存，而它承载的判定是
// **跨进程共享事实**的快照：
//
//	① 计划的「可用 / 不可用 / 计划序」来自候选 SQL 的那一批列：
//	   routing_blocked / lifecycle_status / availability_state / quota_state /
//	   balance。而 **availability_state 是探测写出来的** —— 探测跑在哪个进程里
//	   都一样成立（蓝绿两槽同时在跑、副本部署时多进程同时探）。
//
//	② 候选 SQL 本身就直接按探测判定裁剪计划：
//	   brokenPairExcludeSQL（provider/client.go）用 NOT EXISTS 排除
//	   state='broken_confirmed' 的 (credential, raw_model) 对
//	   ⇒ **探测判定一变，计划的候选集就变**。
//
// ⇒ 所以「计划」这件事的**事实源是跨进程的**，而它的缓存是**进程内的**
// ⇒ ⇒ 失效必须能跨进程传播，否则：
//
//	**别的进程翻的判定，本进程看不见** ⇒ 本进程缓存里那条
//	「已被别处判定 broken / quota_exhausted 的桶」**最多再活 candidateCacheTTL
//	（30s）** ⇒ ⇒ 这正是「路由总是命中不可用或失败的供应商桶」的机制。
//
// ★ 与 URSM v2 的节点失效通道**同构**：domains/ursm/v2/store/keys.go 的
//   NodeInvalidationChannel + domains/ursm/v2/manager.go 的订阅者
//   —— 同一族问题，仓里已有一套被审计过的成熟写法 ⇒ 这里**照抄形状**，
//   不发明第二套语义（一个通道名 + 一个可解析的载荷 + 一个带重连的订阅者）。
//
// ── 为什么载荷只带 credential_id（不带模型）────────────────────────────────
//
// 本地失效的粒度本来就是「凭据」：InvalidateCandidateCacheForCredential 扫
// candCache，把 **PlanOrder 里含该凭据**的条目整条删掉（候选集是按凭据
// 组织的，一个凭据的判定变化会影响它参与的每一条计划）。
// ⇒ 跨进程传「哪个凭据变了」就够了，且与本地语义**逐字一致**，
// 不引入第二套「按模型失效」的语义。
//
// ── 失败语义（重要）────────────────────────────────────────────────────────
//
//	· **发布失败**（Redis 不可达 / 通道不存在）⇒ 只记日志，**不影响本地失效**，
//	  也不影响请求。远端实例最多多活 30s —— 这与「今天根本没有跨实例失效」
//	  是同一条上界 ⇒ **不构成新的失效模式**。
//	· **队列满**（本进程瞬时积压）⇒ **丢弃该条**并记 Debug。同上：
//	  丢弃只是退回到今天的行为，不会让状态更坏。
//	· **订阅端断线** ⇒ 指数退避重连（照抄 URSM 订阅者的写法）：
//	  永久退出会让「跨实例失效」静默失效直到进程重启 ⇒ 必须重连。
//	· **载荷不可解析** ⇒ 跳过该条，**不阻断**（脏数据不该打挂整条通道）。

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// candidateCacheInvalidationChannel 是「候选缓存失效」的跨进程广播通道名。
	//
	// ★ 命名与 URSM v2 的节点失效通道（NodeInvalidationChannel → "<prefix>meta:node_invalidation"）
	//   保持同一族形状：llmgw: 前缀 + meta: 中段 + 语义后缀。
	candidateCacheInvalidationChannel = "llmgw:meta:candidate_cache_invalidation"

	// candidateCacheInvalidationAll 是「整库失效」的载荷（对应 InvalidateAllCandidateCache）。
	candidateCacheInvalidationAll = "all"

	// candidateCacheInvalidationPublishTimeout 是单次发布的超时上限。
	// 走后台发布器，**不在请求热路径上**，所以给一个不短也不长的额度。
	candidateCacheInvalidationPublishTimeout = 200 * time.Millisecond

	// candidateCacheInvalidationQueueSize 是待发布队列的容量。
	// 队列满 ⇒ 丢弃（见文件头「失败语义」）⇒ 不阻塞调用方。
	candidateCacheInvalidationQueueSize = 1024

	// candidateCacheInvalidationReconnectBase 是订阅者重连的初始退避。
	candidateCacheInvalidationReconnectBase = 100 * time.Millisecond
)

// encodeCandidateCacheInvalidation 把「哪个凭据的判定变了」编码成线上载荷。
//
// credentialID <= 0 ⇒ 整库失效（与 InvalidateAllCandidateCache 语义一致）。
func encodeCandidateCacheInvalidation(credentialID int) string {
	if credentialID <= 0 {
		return candidateCacheInvalidationAll
	}
	return strconv.Itoa(credentialID)
}

// decodeCandidateCacheInvalidation 解析线上载荷。
//
// 返回 (credentialID, ok)：
//   - credentialID == 0 且 ok=true ⇒ 整库失效
//   - credentialID > 0 且 ok=true ⇒ 该凭据的判定变了
//   - ok=false ⇒ 载荷不可解析，调用方**跳过**该条（不阻断通道）
func decodeCandidateCacheInvalidation(payload string) (int, bool) {
	p := strings.TrimSpace(payload)
	if p == "" {
		return 0, false
	}
	if p == candidateCacheInvalidationAll {
		return 0, true
	}
	id, err := strconv.Atoi(p)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// candidateInvalidationBridge 持有「发布器 + 订阅者」两侧的运行时状态。
//
//	client —— 本桥服务的缓存实例（订阅端清的是**它自己那一份**）
//	queue  —— 本进程待广播的失效事件（凭据 ID；0 = 整库）
//	rdb    —— 通道用的 Redis 连接
type candidateInvalidationBridge struct {
	client *Client
	rdb    *redis.Client
	queue  chan int
	stop   context.CancelFunc
	wg     sync.WaitGroup
}

var (
	candidateInvalidationMu sync.Mutex
	// candidateInvalidationActive 是**当前进程**的桥（未启动时为 nil）。
	candidateInvalidationActive *candidateInvalidationBridge
)

// StartCandidateCacheInvalidationBridge 启动跨实例失效桥：
//
//	① 订阅者：收别的进程广播的失效事件 ⇒ 本实例本地清缓存（**只本地，不再广播**，避免回环）
//	② 发布者：把本实例产生的失效事件广播出去
//
// ★ 没有 Redis 时**整条桥不启动**（调用方传 nil）⇒ 行为退回到今天的
//   「进程内失效 + 30s TTL 上界」，**不构成新失效模式**。
//
// ★ ctx 取消 ⇒ 两个 goroutine 都退出（订阅者关连接、发布者停止广播）。
func StartCandidateCacheInvalidationBridge(ctx context.Context, c *Client, rdb *redis.Client) {
	if rdb == nil || c == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bridgeCtx, cancel := context.WithCancel(ctx)

	b := &candidateInvalidationBridge{
		client: c,
		rdb:    rdb,
		queue:  make(chan int, candidateCacheInvalidationQueueSize),
		stop:   cancel,
	}

	candidateInvalidationMu.Lock()
	if candidateInvalidationActive != nil {
		// 已有活桥 ⇒ 不重复启动（照抄 URSM 订阅者的 double-start 守卫：
		// 重复启动会孤儿前一个 cancel func 并让 WaitGroup 计数失衡）。
		candidateInvalidationMu.Unlock()
		cancel()
		return
	}
	candidateInvalidationActive = b
	candidateInvalidationMu.Unlock()

	b.wg.Add(2)
	go b.runPublisher(bridgeCtx)
	go b.runSubscriber(bridgeCtx)

	slog.Info("candidate cache cross-instance invalidation bridge started",
		"channel", candidateCacheInvalidationChannel,
		"queue_size", candidateCacheInvalidationQueueSize)
}

// StopCandidateCacheInvalidationBridge 停掉桥（进程停机路径用）。
func StopCandidateCacheInvalidationBridge() {
	candidateInvalidationMu.Lock()
	b := candidateInvalidationActive
	candidateInvalidationActive = nil
	candidateInvalidationMu.Unlock()
	if b == nil {
		return
	}
	b.stop()
	b.wg.Wait()
}

// publishCandidateCacheInvalidation 把一次本地失效广播给别的进程。
//
// ★ **非阻塞**：队列满 ⇒ 丢弃（记 Debug）⇒ 绝不把 Redis 延迟带进请求热路径。
// ★ 桥未启动（无 Redis / 未装配）⇒ 直接返回 ⇒ 本地失效照旧生效。
//
// ★ 不带 client 参数：广播的内容是**全局事实**（「凭据 X 的判定变了」），
//   与「谁发布的」无关 ⇒ ⇒ 任何实例都可以发布同一条事件。
func publishCandidateCacheInvalidation(credentialID int) {
	candidateInvalidationMu.Lock()
	b := candidateInvalidationActive
	candidateInvalidationMu.Unlock()
	if b == nil {
		return
	}
	select {
	case b.queue <- credentialID:
	default:
		slog.Debug("candidate cache invalidation queue full; dropping broadcast",
			"credential_id", credentialID)
	}
}

// runPublisher 把队列里的失效事件广播到 Redis 通道。
func (b *candidateInvalidationBridge) runPublisher(ctx context.Context) {
	defer b.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case credentialID := <-b.queue:
			b.publishOne(ctx, credentialID)
		}
	}
}

func (b *candidateInvalidationBridge) publishOne(ctx context.Context, credentialID int) {
	pubCtx, cancel := context.WithTimeout(ctx, candidateCacheInvalidationPublishTimeout)
	defer cancel()
	if err := b.rdb.Publish(pubCtx, candidateCacheInvalidationChannel,
		encodeCandidateCacheInvalidation(credentialID)).Err(); err != nil {
		// ★ 发布失败**不阻断**本地失效：本地已经清了，远端最多多活 30s。
		slog.Warn("candidate cache invalidation broadcast failed",
			"credential_id", credentialID, "error", err)
	}
}

// runSubscriber 订阅失效通道，把别处广播来的事件落到本地缓存上。
//
// ★ 断线 ⇒ 指数退避重连（照抄 URSM 订阅者 domains/ursm/v2/manager.go
//   startInvalidationSubscriber 的形状）：永久退出会让跨实例失效静默失效
//   直到进程重启 ⇒ 那比"没有这条通道"更坏（看起来有、实际没有）。
func (b *candidateInvalidationBridge) runSubscriber(ctx context.Context) {
	defer b.wg.Done()
	backoff := candidateCacheInvalidationReconnectBase
	for ctx.Err() == nil {
		pubsub := b.rdb.Subscribe(context.Background(), candidateCacheInvalidationChannel)
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
			credentialID, ok := decodeCandidateCacheInvalidation(msg.Payload)
			if !ok {
				// 脏载荷：跳过该条，**不阻断**通道。
				continue
			}
			if credentialID == 0 {
				b.client.invalidateAllCandidateCacheLocal()
				continue
			}
			// ★ 只做本地失效，**不再广播** ⇒ 不会回环。
			b.client.invalidateCandidateCacheLocal(credentialID)
		}

		_ = pubsub.Close()
		close(closed)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("candidate cache invalidation subscriber reconnecting",
			"channel", candidateCacheInvalidationChannel, "backoff", backoff)
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
