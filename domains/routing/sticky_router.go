package routing

import "time"

// StickyRouter 粘性路由：优先使用 ctx.Metadata["preferred_credential"]。
//
// 工作原理：
//  1. 检查 ctx.Metadata 中是否有 "preferred_credential"（字符串）。
//  2. 在 ctx.Candidates 中查找匹配该 ID 的候选；命中则返回。
//  3. 未命中或没有偏好时，调用 fallback.Route(ctx) 回退。
//
// 设计动机：会话粘性可显著降低对话中断（同一会话总是命中同一上游），
// 但若首选凭据不可用，必须能降级到其他候选——所以需要 fallback 链。
type StickyRouter struct {
	// fallback 回退路由器（通常为 RoundRobinRouter 或 ScoreRouter）。
	// 不允许为 nil：构造时校验。
	fallback Router
}

// NewStickyRouter 构造一个粘性路由器。
//
// 参数 fallback 不允许为 nil；若传入 nil，函数会 panic。
// 这与"粘性必须有降级路径"的不变量绑定。
func NewStickyRouter(fallback Router) *StickyRouter {
	if fallback == nil {
		panic("routing.NewStickyRouter: fallback router must not be nil")
	}
	return &StickyRouter{fallback: fallback}
}

// Route 实现 Router 接口。
func (r *StickyRouter) Route(ctx Context) (*Decision, error) {
	// 提取粘性偏好
	pref, _ := ctx.Metadata["preferred_credential"].(string)
	if pref != "" {
		for _, c := range ctx.Candidates {
			if c != nil && c.CredentialID == pref {
				return &Decision{
					Selected:  c,
					Strategy:  "sticky",
					DecidedAt: time.Now(),
				}, nil
			}
		}
	}
	// 未命中或无偏好：回退
	return r.fallback.Route(ctx)
}
