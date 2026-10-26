// Package routeorder — 候选顺序的**契约**：策略**只排序**。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P1-2）指出：magpie 的路由策略点是一个**只排序**的契约
// （`routes.Strategy{Order func(pool string, seats []Seat) []int}`），
// 且它的**失败策略是"失败开放"**：
//
//	internal/routes/routes.go —— Order 只返一个**排列**；
//	panic / 超时 / 非排列 ⇒ 回到原顺序；
//	**每条路线仍会被试**（排序不是筛选）。
//
// 本仓的现状（§3.2 / §4-B10）：策略**散在多层**
// —— `domains/streaming/executors/router.go` 的 `SortByCompositeScore`、
//   `sortCandidatesByWeight`、`domains/autocombo/engine.go` 的 `sortCandidates`、
//   `routingopt/recommender.go` 的 `sortByScore`、`admin/routing.go` 的
//   `sortResolveCandidatesStable`…… ⇒ 没有一个统一契约，
//   也**没有一处**写明"失败时怎么办"。
//
// # 本包的形态：契约 + 失败开放（纯 stdlib）
//
// ★ **stdlib-only**（与 `internal/probekind` 同款做法）：它只定义**契约**，
//   不依赖本仓任何其它包 ⇒ 谁能用、什么时候用都不引入依赖方向问题。
//
//	① `Seat`：策略能看到的**全部**信息（字段级 allow-list，策略**看不到**凭据明文、
//	   看不到连接参数、看不到请求体）。
//	② `Strategy`：只有 `Order(pool string, seats []Seat) []int` —— **只排序**。
//	③ `Order(...)`：**失败开放**的运行器 —— panic / 超时 / 非排列 ⇒ **原顺序**。
//	④ `Apply(...)`：把排列施加到候选切片上（**只重排，不删**）。
//
// # 为什么"只排序"是硬约束
//
// magpie 的注释写得很直白："Order 只返排列"，且 "**每条路线仍会被试**"。
// ⇒ 一旦允许策略"少给几个座位"，策略就变成了**筛选器** ⇒
//   它就能悄悄决定"这个模型**根本不试**" ⇒ 那是 rest 的职责（见
//   `errorsx/restclasses.go` 的归责矩阵），不是排序的。
// ⇒ 本包用**类型**把这条约束钉死：`Order` 返回 `[]int`（排列），
//   而不是 `[]Seat`（子集）⇒ **返回子集在类型上就是"少给了座位"**。
//
// # 名字：为什么不叫 `Strategy`
//
// ★ 本仓**已经有** `autoroute.Strategy` —— 它是**分类器**的 A/B 策略
//   （baseline_heuristic / pattern_layered / llm_fallback，产出的是
//   "这次请求判成什么任务类型"），与"候选顺序"**不是一回事**。
// ⇒ 同一个名字指两件事，是本仓反复吃过的亏（「同一口径两处实现」的近亲）。
// ⇒ 本包用 `routeorder` 命名空间，策略类型叫 `routeorder.Strategy`。
//
// # 失败开放的语义（magpie 的原话）
//
//	"失败开放（panic/超时/非排列 ⇒ 回到原顺序）；**每条路线仍会被试**"
//
// ⇒ 三条都要有断言（见 `internal/routeorder/routeorder_test.go`）：
//	① 非排列（少给/多给/重复/越界）⇒ 原顺序；
//	② panic ⇒ 原顺序；
//	③ 超时 ⇒ 原顺序。
//
// # 引用
//
//	domains/streaming/executors/routeorder_adapter.go —— 把既有复合评分排序
//	  **适配**成本契约（不新建第二套排序逻辑）
//	docs/subsystems/request-surface.md —— 请求面设计页

package routeorder

import (
	"context"
	"time"
)

// DefaultTimeout 是策略排序的默认超时预算。
//
// ★ magpie 的路由策略点超时是 **20ms**（internal/routes/routes.go 的策略超时）。
const DefaultTimeout = 20 * time.Millisecond

// Seat 是策略能看到的**一个候选座位**。
//
// ★ **字段级 allow-list**：策略只能看到这里列出的字段。
//
//	它**看不到**凭据明文、看不到连接参数、看不到请求体、看不到租户信息。
//	⇒ 这是本仓比 magpie **更强**的地方（magpie 的 DTOProfile 有 none/summary/
//	redacted 三档，本仓直接给出"座位"这一层，天然没有敏感字段）。
type Seat struct {
	// Index 是该座位在**原顺序**里的下标（排列用它来指代）。
	Index int
	// CredentialID 是凭据 ID（本仓的"key"）。
	CredentialID int
	// ProviderID 是供应商 ID（本仓的"account"）。
	ProviderID int
	// Model 是候选的出站模型名。
	Model string
	// Tier 是供应商层级（越小越优先，既有语义）。
	Tier int
	// Weight 是人工权重（既有语义）。
	Weight int
	// Known 表示这个 (凭据,模型) 组合**有过成功记录**。
	Known bool
	// SuccessRate 是历史成功率（0..1）。
	SuccessRate float64
	// Resting 表示该座位当前**正在休息**（不可用）。
	//
	// ★ 策略**可以**据此降序，但**不得**因此把它从排列里去掉 ——
	//   「休息」是归责矩阵（errorsx）的职责，见文件头「为什么只排序是硬约束」。
	Resting bool
	// Tokens 是剩余令牌额度（0 = 未知）。
	Tokens int
	// Used 是配额窗口已用比例（0..1；未知时为 0）。
	Used float64
}

// Strategy 是**只排序**的路由策略。
//
// ★ 契约：
//
//	① 返回值必须是 `seats` 的一个**排列**（每个下标恰好出现一次）；
//	② **不得**增删座位（少给 ⇒ 会被判为非排列 ⇒ 回落原顺序）；
//	③ **不得**触碰 rest / 亲和 / 规则 / pin —— 那是别处的职责。
//
// ★ 实现方**不必**自己处理 panic 与超时：`Order(...)` 运行器会兜住。
type Strategy interface {
	// Name 返回策略名（用于日志与台账）。
	Name() string
	// Order 返回 seats 的一个排列（元素是 seats[i].Index）。
	Order(pool string, seats []Seat) []int
}

// OrderFunc 把一个函数适配成 Strategy。
type OrderFunc struct {
	// StrategyName 是策略名。
	StrategyName string
	// Fn 是排序函数。
	Fn func(pool string, seats []Seat) []int
}

// Name 实现 Strategy。
func (o OrderFunc) Name() string {
	if o.StrategyName == "" {
		return "anonymous"
	}
	return o.StrategyName
}

// Order 实现 Strategy。
func (o OrderFunc) Order(pool string, seats []Seat) []int {
	if o.Fn == nil {
		return nil
	}
	return o.Fn(pool, seats)
}

// identityPermutation 返回 0..n-1 的恒等排列（原顺序）。
func identityPermutation(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// IsPermutation 报告 idx 是否是 0..n-1 的一个排列。
//
// ★ 判定包含四件：长度恰好 n、每个元素在 [0,n)、**没有重复**。
//
//	少给（长度不足）/ 多给（长度超出）/ 重复 / 越界 ⇒ 一律不是排列。
func IsPermutation(idx []int, n int) bool {
	if len(idx) != n {
		return false
	}
	seen := make([]bool, n)
	for _, v := range idx {
		if v < 0 || v >= n {
			return false
		}
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// Order 用策略排序座位，**失败开放**。
//
// ★ 失败开放的三个出口（magpie 的原话："panic/超时/非排列 ⇒ 回到原顺序"）：
//
//	① 策略 panic ⇒ 原顺序；
//	② 超过 timeout ⇒ 原顺序；
//	③ 返回的不是排列 ⇒ 原顺序。
//
// ★ 另有一条本仓自加的：**上下文取消** ⇒ 原顺序（避免把一个已经放弃的请求
//
//	继续花在排序上）。
//
// ★ **绝不返回 nil**：调用方拿到的一定是可用的排列（长度 = len(seats)）。
func Order(ctx context.Context, s Strategy, pool string, seats []Seat, timeout time.Duration) []int {
	original := identityPermutation(len(seats))
	if s == nil || len(seats) == 0 {
		return original
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	type result struct{ idx []int }
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// ★ panic ⇒ 原顺序（失败开放）。刻意**不**把 panic 传出去：
				//   排序失败不该让整个请求失败。
				ch <- result{idx: nil}
			}
		}()
		ch <- result{idx: s.Order(pool, seats)}
	}()

	select {
	case res := <-ch:
		if !IsPermutation(res.idx, len(seats)) {
			return original
		}
		return res.idx
	case <-time.After(timeout):
		return original
	case <-ctx.Done():
		return original
	}
}

// Apply 把策略给出的排列施加到 seats 上，**只重排、不删**。
//
// ★ 返回**新切片**（不改原切片）⇒ 调用方可以安全地在失败时丢弃结果。
// ★ 长度恒等于 len(seats) ⇒ **每条路线仍会被试**（这是硬约束，见文件头）。
func Apply(seats []Seat, idx []int) []Seat {
	if len(seats) == 0 {
		return nil
	}
	if !IsPermutation(idx, len(seats)) {
		// 非排列 ⇒ 原样返回（调用方拿到的仍是**全部**座位）。
		out := make([]Seat, len(seats))
		copy(out, seats)
		return out
	}
	out := make([]Seat, 0, len(seats))
	for _, i := range idx {
		out = append(out, seats[i])
	}
	return out
}
