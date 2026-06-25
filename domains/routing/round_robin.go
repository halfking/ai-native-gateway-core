package routing

import (
	"sync/atomic"
	"time"
)

// RoundRobinRouter 轮询路由（备选实现）。
//
// 工作原理：每次调用 Route() 时把内部计数器 +1，取
// (counter-1) % len(Candidates) 作为下标。
//
// 用途：作为 StickyRouter 的回退，或作为多个等价候选的负载均衡。
//
// 线程安全：使用 sync/atomic.Uint64 计数器，允许多 goroutine 并发 Route。
type RoundRobinRouter struct {
	counter atomic.Uint64
}

// NewRoundRobinRouter 构造一个轮询路由器。
func NewRoundRobinRouter() *RoundRobinRouter {
	return &RoundRobinRouter{}
}

// Route 实现 Router 接口。
//
// 行为：
//   - 候选为空：返回 (nil, nil) 表示"路由未决"。
//   - 单个候选：直接返回它。
//   - 多个候选：循环选取（每次调用切到下一个）。
func (r *RoundRobinRouter) Route(ctx Context) (*Decision, error) {
	if len(ctx.Candidates) == 0 {
		return nil, nil
	}
	idx := r.counter.Add(1) - 1
	selected := ctx.Candidates[int(idx%uint64(len(ctx.Candidates)))]
	return &Decision{
		Selected:  selected,
		Strategy:  "round_robin",
		DecidedAt: time.Now(),
	}, nil
}
