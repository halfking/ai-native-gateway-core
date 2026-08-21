// GoalRunContext (Wave 2-D, 2026-08-22): GoalRun 元数据在 ChatHandler 内的
// context 传递。当前仅承载 Resolved 投影，后续 Wave 3-A/B 可在此基础上
// 扩展为 lease_owner/version 之类的高频读字段。
package streaming

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/goalintegration"
)

type goalRunContextKey struct{}

// WithGoalRun 把已落库的 GoalRun 投影挂到 ctx。
func WithGoalRun(ctx context.Context, resolved *goalintegration.Resolved) context.Context {
	if resolved == nil {
		return ctx
	}
	return context.WithValue(ctx, goalRunContextKey{}, resolved)
}

// GoalRunFromContext 返回 ctx 上的 GoalRun 投影；nil 表示本次请求未创建
// GoalRun（兼容无 goal 字段的请求）。
func GoalRunFromContext(ctx context.Context) *goalintegration.Resolved {
	v, ok := ctx.Value(goalRunContextKey{}).(*goalintegration.Resolved)
	if !ok {
		return nil
	}
	return v
}
