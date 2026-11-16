package autoroute

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// cache_revalidate.go — 会话意图缓存的**可用性重校验**（2026-11-16 auto 模式可用性轮）。
//
// ★ 为什么要这道门（magpie 对照结论）：
//
// magpie 的粘性有一条明确规则——**「粘性目标正在休息 ⇒ 不用它，会话自然让给下一个」**
// （magpie internal/gateway/affinity.go:599-600：`case pl.order[at].Rest != nil: a.Why = "resting"`）。
// 也就是说 magpie 的会话粘性是**带逃生口的**：粘住的是「上次答话的那个」，而它一旦
// 休息/用尽/被风控，会话会**换一个**继续，而不是一直撞同一堵墙。
//
// 本仓的会话意图缓存（autoroute/decision.go V1 路径 + decision_v2.go V2 路径）
// 原本**只有分类一致性判据**（role/kind/workType/drift），**没有可用性判据**：
// 缓存命中即无条件复用 (ChosenModel, CredentialID)。
//
// 后果（211 真库实测，2026-11-16 取证轮）：
//   · 会话 `gw_455af85c-…` 连续 55 轮全部落在 glm-5.2，最后一轮 latency 203ms
//     直接失败（request_rejected）；`gw_10dafced-…` 15 轮同样全落在 glm-5.2。
//   · 30 天内 87 笔 auto 请求 13 笔失败，其中 **7 笔集中在 credential 62**
//     （zipu-max-account2，随后被置为 availability_state=suspended /
//     quota_state=periodic_exhausted）。
//   · `auto_route_selections.fallback_used` **87 笔全为 false** —— auto 模式
//     从来没有换过模型。
//
// 也就是说：模型一坏，**整个会话在缓存 TTL（10 分钟 / 50 次命中）内一直坏**，
// 用户看到的就是「中途断掉，表现为原来的模型不可用」。这就是本轮要修的核心。
//
// ★ 三条安全边界（缺一不可）：
//
//  1. **没有 pool ⇒ 放行**：测试/降级环境没有 DB，重校验不能把会话打散。
//  2. **重校验本身出错 ⇒ 放行**：一次 DB 抖动不应该变成全站 auto 失败；
//     数据库说"不知道"不等于"不可用"。
//  3. **明确不可路由 ⇒ 不放行**：这是唯一会作废缓存的分支。作废后由调用方
//     重判（重新走分类 + 打分），等价于 magpie 的「粘性目标休息 ⇒ 让给下一个」。

// indexPool 返回 index 的 PG 连接池（若可得）。
//
// 取池子必须在锁外做校验拷贝：SetPool 持写锁写池子，热路径持读锁取指针
// （与 decision_v2.go 的既有做法同款，避免拷贝后指针被并发改写）。
func (d *Decider) indexPool() *pgxpool.Pool {
	if d == nil {
		return nil
	}
	idx, ok := d.index.(*Index)
	if !ok || idx == nil {
		return nil
	}
	idx.mu.RLock()
	pool := idx.pool
	idx.mu.RUnlock()
	return pool
}

// cachedChoiceRoutable 报告会话意图缓存里的 (凭据, 模型) 此刻是否仍可路由。
//
// 返回 false 的唯一含义是：**这个选择已经明确不可用了**，调用方应当作废缓存
// 并重新决策。
func (d *Decider) cachedChoiceRoutable(ctx context.Context, cached CachedIntent) bool {
	if d != nil && d.CachedChoiceValidator != nil {
		return d.CachedChoiceValidator(ctx, cached.CredentialID, cached.ChosenModel)
	}
	pool := d.indexPool()
	if pool == nil {
		// 边界 1：无 pool ⇒ 放行（沿用既有行为）。
		return true
	}
	routable, err := validateCachedChoiceDetailed(ctx, pool, cached.CredentialID, cached.ChosenModel)
	if err != nil {
		// 边界 2：重校验自身出错 ⇒ 放行 + 留痕（DB 抖动不该让 auto 全灭）。
		slog.WarnContext(ctx, "autoroute: cached choice revalidation failed; keeping the cached choice",
			"err", err,
			"cached_model", cached.ChosenModel,
			"cached_credential_id", cached.CredentialID,
		)
		return true
	}
	return routable
}

// cacheChoiceStillRoutable 是 V1 决策路径用的同一道门。
//
// 与 V2 的差别只有一处：**V2 把整段会话缓存分支挂在 UseCacheRevalidation 开关下**
// （开关关 ⇒ V2 干脆不用缓存，每轮重判 ⇒ 天然无粘性、也就天然没有"钉死"问题）；
// **V1 没有这层结构**——V1 的缓存分支一直在跑，只是过去不查可用性。
//
// 因此 V1 这道门**不挂开关**：一个已经被证明不可路由的选择是**过期数据**，
// 不是"某个特性"；留着它就等于把用户报障的形态（会话被钉死在死模型上）
// 复制一份到 legacy 路径。开关关掉时 V2 走"每轮重判"，V2 与 V1 都安全。
//
// 无 pool / 查询出错 ⇒ 放行（见 cachedChoiceRoutable 的边界 1、2），
// 因此测试与降级环境的行为不变。
func (d *Decider) cacheChoiceStillRoutable(ctx context.Context, cached CachedIntent) bool {
	return d.cachedChoiceRoutable(ctx, cached)
}
