// hook.go —— identity 领域的 Pipeline Hook 适配层。
//
// IdentityHook 把 IdentityBuilder 适配到 pipeline.Hook 接口，
// 把构造好的 ClientIdentity 注入到 PipelineRequest。
package identity

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// IdentityHook 注入客户端识别信息到 PipelineRequest。
type IdentityHook struct {
	builder *IdentityBuilder
}

// NewIdentityHook 创建一个新的 IdentityHook。
func NewIdentityHook(builder *IdentityBuilder) *IdentityHook {
	return &IdentityHook{builder: builder}
}

// Name 返回 Hook 名称。
func (h *IdentityHook) Name() string { return "identity.inject" }

// Priority 返回 Hook 优先级（小值先执行）。
func (h *IdentityHook) Priority() int { return 100 }

// Enabled 在 envelope 非 nil 且尚未注入 identity 时启用。
func (h *IdentityHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.ClientIdentity == nil
}

// Execute 调用 builder.Build 构造 ClientIdentity 并注入 envelope。
func (h *IdentityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil {
		return nil
	}
	ident, err := h.builder.Build(ctx, env.TenantID)
	if err != nil {
		return err
	}
	env.ClientIdentity = &domain.PipelineClientIdentity{
		Hash:       ident.IdentityHash,
		VirtualIP:  ident.VirtualIP,
		VirtualMAC: ident.VirtualMAC,
	}
	return nil
}

// OnError 身份识别失败必须上报（不吞错）。
func (h *IdentityHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err
}

// 编译期检查: IdentityHook 实现了 pipeline.Hook
var _ pipeline.Hook = (*IdentityHook)(nil)
