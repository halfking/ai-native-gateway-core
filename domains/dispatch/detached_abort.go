package dispatch

import "context"

// detached_abort.go — R32（2026-10-02，采纳 12h 审计 P2-A）：surviving-stream
// 的租约中止信号载体。
//
// forwarder 为每个 attempt 派生 fwdCtx（WithCancel(qrCtx)），租约续约器在
// 确定性丢失/降级越 TTL 时 cancel 它实现 fail-closed 中止。但 executor 侧
// 对 StreamSurvivesClientCancel 的流用 context.WithoutCancel(params.R.Context())
// 构造 upstream 上下文——WithoutCancel 剥离的是**整条取消链**，把客户端断开
// 与租约中止一并剥离：流照跑至自然结束，"永不跑在过期租约上"的承诺对最长
// 的流（恰是租约最可能过期的流）不成立。
//
// 修法：forwarder 另铸一条**只随租约中止**触发的取消源（WithoutCancel(fwdCtx)
// 的孩子，因此对客户端断开免疫），以 context value 钉在 fwdCtx 上。WithoutCancel
// 保留 value，所以 executor 在剥离取消链之后仍能取到这条 abort 源并把它并回
// detached 流的 Done 集——客户端断开照旧免疫，租约中止照旧可达。
type detachedAbortCtxKey struct{}

// WithDetachedStreamAbort 把 abort 源钉进 ctx。仅 forwarder 在构造 fwdCtx 时
// 调用；非 dispatch 路径（无租约语义）不钉，executor 侧取到 nil 即维持原行为。
func WithDetachedStreamAbort(ctx context.Context, abort context.Context) context.Context {
	return context.WithValue(ctx, detachedAbortCtxKey{}, abort)
}

// DetachedStreamAbort 返回 forwarder 钉入的租约中止源；未钉（非 dispatch
// 路径）返回 nil。
func DetachedStreamAbort(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	abort, _ := ctx.Value(detachedAbortCtxKey{}).(context.Context)
	return abort
}
