// builder.go —— identity 领域的"客户端身份构造器"。
//
// 封装 BuildIdentityFromRequest，提供给 Pipeline Hook 与外部调用方使用。
// IdentityHook 只需要 builder.Build(ctx, tenantID) 一个入口，避免 Hook 持有
// *http.Request 这种底层 transport 抽象。
package identity

import (
	"context"
	"net/http"
)

// IdentityBuilder 根据 HTTP 请求与租户 ID 构造稳定的 ClientIdentity。
type IdentityBuilder struct {
	// defaultProfile（可选）：当请求头未指定 client profile 时使用
	defaultProfile string
}

// NewIdentityBuilder 创建一个新的 IdentityBuilder。
func NewIdentityBuilder() *IdentityBuilder {
	return &IdentityBuilder{}
}

// WithDefaultProfile 设置默认 client profile。
func (b *IdentityBuilder) WithDefaultProfile(profile string) *IdentityBuilder {
	b.defaultProfile = profile
	return b
}

// Build 根据 ctx 中的 *http.Request 与 tenantID 构造 ClientIdentity。
// 这是从 transport 层调用 BuildIdentityFromRequest 的"门面方法"。
func (b *IdentityBuilder) Build(ctx context.Context, tenantID string) (ClientIdentity, error) {
	if r, ok := ctx.Value(identityCtxKey{}).(*http.Request); ok && r != nil {
		return BuildIdentityFromRequest(r, tenantID, nil, nil, b.defaultProfile), nil
	}
	// 没有 request 时返回带 PrimarySeed 兜底的最小 identity
	fp := ClientFingerprint{
		ClientProfile: b.defaultProfile,
	}
	_ = fp.PrimarySeed()
	return BuildIdentity(tenantID, nil, nil, fp), nil
}

type identityCtxKey struct{}

// WithRequest 把 *http.Request 放入 ctx 以便 IdentityBuilder.Build 提取。
func WithRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, r)
}

// computedIdentityKey 是用于传递"已计算的 ClientIdentity"的 context key。
// Pipeline 在 preflight 阶段计算出 identity 后，通过 WithComputedIdentity
// 注入 ctx，下游的 v1 ChatHandler 用 ComputedIdentityFromContext 读取，
// 避免重复计算。
type computedIdentityKey struct{}

// WithComputedIdentity 将一个已计算的 *ClientIdentity 放入 ctx。
// 允许 nil（下游会走 fallback 重新计算）。
func WithComputedIdentity(ctx context.Context, c *ClientIdentity) context.Context {
	return context.WithValue(ctx, computedIdentityKey{}, c)
}

// ComputedIdentityFromContext 返回 ctx 中预先计算的 *ClientIdentity。
// 若不存在则返回 nil, false。
func ComputedIdentityFromContext(ctx context.Context) (*ClientIdentity, bool) {
	c, ok := ctx.Value(computedIdentityKey{}).(*ClientIdentity)
	return c, ok && c != nil
}
