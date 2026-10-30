package executors

// rate_sink.go — B2（429 沉底排序，magpie 四域对比刷新轮 §五 B2 / §三 B9）。
//
// # 契约（研究方案验收量具）
//
//	① 429 写键（MarkRateLimited —— 由 executor 的 429 失败路径调用）；
//	② 下一次 plan 沉底（planByTier 尾段把「近期被 429 且仍在 SinkTTL 窗内」
//	   的候选排到未被限流者之后；同层相对顺序保持）；
//	③ TTL 过期回原序（entry 过期后等价于无信号，零影响）；
//	④ quota 类不写键（那是 rest 不是 sink —— quota 走 KindQuota* 的
//	   恢复点路径，sink 只服务「配额未满、容量被短窗限流」的形态）。
//
// # 与 A2（ratebackoff.go 绑定冷却升级）的分工
//
// 冷却升级（A2）把反复 429 的绑定**摘出 v_routable 一段时间**；sink（B2）
// 管**冷却到期后的回归顺序**——没有它，冷却 3min 一过，凭据带着与从未失败者
// 相同的 P2C 分数回到队首，紧接着又撞 429（magpie 的原话：负载轮流打满各
// 账号 vs 单一凭据反复被首试）。两者键空间一致（(credential, model)），
// 但 A2 写 DB 绑定行、B2 写轻量信号存储，互不替代。
//
// # 载体
//
// 默认载体是**进程内 map**（含 TTL 语义 + 与 slowrest 同款的多实例已知局限：
// 多实例部署下各实例各记各的）。RateSinkStore 接口允许 main 注入 Redis 适配器
// （键 gw:sink:{cred}:{model}，TTL 同值）——接口与 StickyRedisStore 同形的最小
// 面（Put/Get），跨实例一致性按需升级，行为面（判据①~④）与载体无关。
//
// # 引用
//
//	domains/streaming/executors/router.go —— planByTier 尾段消费（MarkAndSink）
//	domains/credential/ratebackoff.go —— A2 冷却升级（分工见上）
//	magpie internal/gateway/sink.go —— 原契约（sinkOrder：先沉者在前）

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

const (
	// RateSinkTTL 是沉底信号的存活窗。5min（研究方案 §五 B2「TTL 5min」）：
	// 比一次绑定冷却（首罚 3min）稍长 ⇒ 凭据从冷却里出来时信号还在，
	// 排在健康同伴之后；再往后信号自然过期，回到纯 P2C。
	RateSinkTTL = 5 * time.Minute
)

// RateSinkStore 是沉底信号的存储面（进程内 map 的默认实现；
// Redis 适配器按需注入以获得跨实例一致性）。
type RateSinkStore interface {
	// Put 记一次沉底信号（at 用于多实例排序；TTL 由实现自己执行）。
	Put(ctx context.Context, credID int, model string, at time.Time, ttl time.Duration)
	// Get 返回该键最近一次沉底的时刻；miss ⇒ zero。
	Get(ctx context.Context, credID int, model string) time.Time
}

// rateSinkEntry 是进程内 map 的条目。
type rateSinkEntry struct {
	at        time.Time
	expiresAt time.Time
}

// MemoryRateSink 是 RateSinkStore 的进程内默认实现。
type MemoryRateSink struct {
	mu    sync.Mutex
	items map[string]rateSinkEntry
}

// NewMemoryRateSink 构造进程内沉底存储。
func NewMemoryRateSink() *MemoryRateSink {
	return &MemoryRateSink{items: make(map[string]rateSinkEntry)}
}

func rateSinkKey(credID int, model string) string {
	return strconv.Itoa(credID) + "/" + strings.TrimSpace(model)
}

// Put 记一次沉底信号。
func (s *MemoryRateSink) Put(_ context.Context, credID int, model string, at time.Time, ttl time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.items[rateSinkKey(credID, model)] = rateSinkEntry{at: at, expiresAt: at.Add(ttl)}
	s.mu.Unlock()
}

// Get 返回最近一次沉底时刻；过期/miss ⇒ zero。
func (s *MemoryRateSink) Get(_ context.Context, credID int, model string) time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[rateSinkKey(credID, model)]
	if !ok || time.Now().After(e.expiresAt) {
		return time.Time{}
	}
	return e.at
}

// RateSink 是 Router 侧的沉底信号面：写入在 429 失败路径，读取在 planByTier。
type RateSink struct {
	store RateSinkStore
}

// NewRateSink 构造沉底信号面。store 为 nil 时自动落进程内实现。
func NewRateSink(store RateSinkStore) *RateSink {
	if store == nil {
		store = NewMemoryRateSink()
	}
	return &RateSink{store: store}
}

// Mark 记一次 429 沉底信号（判据①）。
//
// 判据④（quota 类不写键）由调用方保证：只有 KindRateLimit（容量/短窗限流）
// 走本入口；KindQuota* 系列走 writer 的恢复点路径，不进 sink。
func (s *RateSink) Mark(ctx context.Context, credID int, model string) {
	if s == nil {
		return
	}
	s.store.Put(ctx, credID, model, time.Now(), RateSinkTTL)
}

// sinkAt 返回候选的沉底时刻（无信号 ⇒ zero）。
func (s *RateSink) sinkAt(ctx context.Context, credID int, model string) time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.store.Get(ctx, credID, model)
}

// MarkRateLimited 是 executor 429 失败路径的接线点（判据①）。
// Router 方法形态方便 executor 在 recordDispatchError 里一行调用。
func (r *Router) MarkRateLimited(ctx context.Context, credID int, model string) {
	if r == nil || r.RateSink == nil {
		return
	}
	r.RateSink.Mark(ctx, credID, model)
}

// applyRateSinkOrder 是 planByTier 尾段的读面（判据②③）：
// 逐候选查沉底时刻，再分段稳定排序（先沉者在前，未沉段保持原序）。
// 未接线/全部无信号 ⇒ 逐元素一致。
func (r *Router) applyRateSinkOrder(ctx context.Context, cands []provider.Candidate) []provider.Candidate {
	if r == nil || r.RateSink == nil || len(cands) <= 1 {
		return cands
	}
	times := make([]time.Time, len(cands))
	any := false
	for i, c := range cands {
		at := r.RateSink.sinkAt(ctx, c.CredentialID, c.RawModel)
		times[i] = at
		if !at.IsZero() {
			any = true
		}
	}
	if !any {
		return cands
	}
	return sinkOrderBy(cands, times)
}

// sinkOrderBy 把近期被 429 的候选沉到未被限流者之后（判据②③）。
//
// 分段稳定：sunk 段内按沉底时刻**升序**（先沉者在前——magpie sinkOrder
// 的语义：最早被限流的账号最先轮到「再试一次」，负载在时间维度摊开），
// 未沉段保持输入相对顺序。times[i] 是 cands[i] 的沉底时刻（zero = 无信号）。
// 无信号 ⇒ 逐元素一致（零变化）。
func sinkOrderBy(cands []provider.Candidate, times []time.Time) []provider.Candidate {
	if len(cands) <= 1 {
		return cands
	}
	open := make([]provider.Candidate, 0, len(cands))
	sunkIdx := make([]int, 0, len(cands))
	for i, c := range cands {
		if !times[i].IsZero() {
			sunkIdx = append(sunkIdx, i)
		} else {
			open = append(open, c)
		}
	}
	if len(sunkIdx) == 0 {
		return cands
	}
	// 先沉者在前：对 sunk 段按时刻升序做稳定插入排序（n 小，避免分配）。
	for i := 1; i < len(sunkIdx); i++ {
		for j := i; j > 0 && times[sunkIdx[j]].Before(times[sunkIdx[j-1]]); j-- {
			sunkIdx[j], sunkIdx[j-1] = sunkIdx[j-1], sunkIdx[j]
		}
	}
	out := open
	for _, idx := range sunkIdx {
		out = append(out, cands[idx])
	}
	return out
}
