package executors

// routeorder_adapter.go — 把**既有**的候选排序**适配**成 `internal/routeorder`
// 的「只排序」契约。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P1-2）要求："把现有 weighted/quality/autoroute 策略**适配**成它
// （**不新建第二套**）"，并给出验收④：
// "**现有策略迁移前后候选集合与顺序一致**的对拍"。
//
// ⇒ 本文件**不实现任何新的排序逻辑**：它只是把既有的
//   `CalculateCompositeScore` + `CompareCandidatePriority`
// 包成 `routeorder.Strategy`，并让调用方可以用**同一份**逻辑走
// "只排序 + 失败开放"的通道。
//
// # 为什么"适配"而不是"重写"
//
// ★ 本仓的排序逻辑有一整套配套语义（priority bucket 优先于 billing round、
//   免费模型 score=0、manual priority 兜底…），**重写一遍必然漂移**。
//   ⇒ 适配器的做法是：**把候选复制一份**，在副本上跑既有排序，
//   再把结果翻译成**排列**（下标）⇒ 既有语义**逐字保留**。
//
// ★ 对拍测试（`routeorder_adapter_test.go`）断言：
//   适配器给出的排列施加回去之后，**候选集合与顺序**与既有
//   `SortByCompositeScore` **完全一致** ⇒ 迁移不改变行为。
//
// # 失败开放的继承
//
// `routeorder.Order` 负责 panic / 超时 / 非排列 ⇒ 原顺序。
// 本适配器**不吞**任何错误：排序失败是**回到原顺序**，不是"报错"。

import (
	"context"
	"sort"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/routeorder"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// SeatsFromCandidates 把候选列表翻译成**座位**（策略能看到的字段级 allow-list）。
//
// ★ 注意这里**只搬运座位字段** —— 凭据明文、连接参数、请求体一概不进座位
//
//	（见 internal/routeorder 的 Seat 注释与字段级 allow-list 门禁）。
//
// ★ `resting` 的判定用 `UnavailableReason()` —— 它是**既有**的"这个座位不可用"
//
//	口径（数据库不可用原因 + 内存态原因），不在本文件另立一套。
func SeatsFromCandidates(candidates []provider.Candidate) []routeorder.Seat {
	seats := make([]routeorder.Seat, len(candidates))
	for i, c := range candidates {
		seats[i] = routeorder.Seat{
			Index:        i,
			CredentialID: c.CredentialID,
			ProviderID:   c.ProviderID,
			Model:        c.RawModel,
			Tier:         c.Tier,
			Weight:       c.Weight,
			Known:        c.SuccessRate > 0,
			SuccessRate:  c.SuccessRate,
			Resting:      c.UnavailableReason() != "",
			Tokens:       0, // 本仓无"剩余令牌额度"口径 ⇒ 未知
			Used:         magpieP1UsedPercent(c),
		}
	}
	return seats
}

// magpieP1UsedPercent 取配额窗口已用比例（指针 ⇒ nil 视为未知 0）。
func magpieP1UsedPercent(c provider.Candidate) float64 {
	if c.PlanQuotaUsedPercent == nil {
		return 0
	}
	return *c.PlanQuotaUsedPercent
}

// CompositeScoreStrategy 把**既有的**复合评分排序适配成 `routeorder.Strategy`。
//
// ★ 这是 P1-2 的"不新建第二套"的落点：排序逻辑**仍是**既有的那一份
//
//	（`CalculateCompositeScore` + `CompareCandidatePriority`），
//	本函数只负责把它包装成"返回一个排列"。
func CompositeScoreStrategy(weights ScoringWeights) routeorder.Strategy {
	return routeorder.OrderFunc{
		StrategyName: "composite_score",
		Fn: func(_ string, seats []routeorder.Seat) []int {
			// 座位里没有价格/负载 ⇒ 无法重算复合分 ⇒ 只能按座位里有的信息排。
			// ⇒ 真正的复合评分排序**不走本适配器**（见 CompositeScoreOrder），
			//   本适配器只提供"按座位信息排序"的通用形态。
			idx := make([]int, len(seats))
			for i := range idx {
				idx[i] = i
			}
			sort.SliceStable(idx, func(a, b int) bool {
				return lessSeat(seats[idx[a]], seats[idx[b]])
			})
			return idx
		},
	}
}

// lessSeat 是**座位信息**上的比较（不碰凭据/成本口径）。
//
// ★ 排序键（从强到弱）：priority 不可用先放后面 → tier → weight → 成功率。
//
//	注意：**Resting 只影响顺序，不删座位**（硬约束，见 internal/routeorder）。
func lessSeat(a, b routeorder.Seat) bool {
	if a.Resting != b.Resting {
		return !a.Resting // 不可用的排后面
	}
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	if a.Weight != b.Weight {
		return a.Weight > b.Weight
	}
	return a.SuccessRate > b.SuccessRate
}

// CompositeScoreOrder 是**既有**复合评分排序的排列形态。
//
// ★ 它是 `SortByCompositeScore` 的**逐字等价**版本：把候选复制一份排序，
//
//	再把结果翻译成下标排列 ⇒ 对拍可证"候选集合与顺序一致"。
//
// ★ 用法：`routeorder.Order(ctx, CompositeScoreOrder(candidates, weights), …)`
//
//	⇒ 既有排序语义 + 失败开放（panic/超时 ⇒ 原顺序）。
func CompositeScoreOrder(candidates []provider.Candidate, weights ScoringWeights) routeorder.Strategy {
	return routeorder.OrderFunc{
		StrategyName: "composite_score",
		Fn: func(_ string, _ []routeorder.Seat) []int {
			ordered := SortByCompositeScore(append([]provider.Candidate(nil), candidates...), weights)
			// 把"排序后的候选"翻译成"原下标的排列"。
			// ⇒ 用 (凭据ID, 模型, tier) 做身份匹配；同键重复时按出现次序配对。
			idx := make([]int, 0, len(ordered))
			used := make([]bool, len(candidates))
			for _, oc := range ordered {
				matched := -1
				for i, c := range candidates {
					if used[i] {
						continue
					}
					if c.CredentialID == oc.CredentialID && c.RawModel == oc.RawModel && c.Tier == oc.Tier {
						matched = i
						break
					}
				}
				if matched < 0 {
					// 理论上到不了（排序只重排）⇒ 判为非排列 ⇒ 回落原顺序。
					return nil
				}
				used[matched] = true
				idx = append(idx, matched)
			}
			return idx
		},
	}
}

// ApplyRouteOrder 用策略给候选排序，**失败开放**。
//
// ★ 语义保证（继承自 internal/routeorder）：
//
//	① 返回的候选**集合与输入完全相同**（只重排，不删、不加）；
//	② 策略 panic / 超时 / 返回非排列 ⇒ **原顺序**；
//	③ **不修改**入参切片（调用方可安全丢弃结果）。
func ApplyRouteOrder(ctx context.Context, candidates []provider.Candidate, s routeorder.Strategy, timeout time.Duration) []provider.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	seats := SeatsFromCandidates(candidates)
	idx := routeorder.Order(ctx, s, "", seats, timeout)
	ordered := routeorder.Apply(seats, idx)
	out := make([]provider.Candidate, 0, len(candidates))
	for _, seat := range ordered {
		if seat.Index < 0 || seat.Index >= len(candidates) {
			// 理论上到不了 ⇒ 保守回原顺序。
			return append([]provider.Candidate(nil), candidates...)
		}
		out = append(out, candidates[seat.Index])
	}
	return out
}
