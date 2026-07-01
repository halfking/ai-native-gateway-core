package transformation

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// TransformHook 转换 Hook（串联所有 transformer）。
//
// 行为：
//   - Enabled: env != nil && env.TransformedRequest == nil
//     （如果已转换过，跳过——支持 pipeline 内多次 Transform）
//   - Execute: 构造 transformation.Context，依次调用每个 Transformer.Transform。
//     任意一个失败立即返回。
//   - OnError: 转换失败透传 error（转换失败必须可见）。
//
// 数据流：
//
//	env.TransformedRequest -> ctx.Request
//	Transformers 修改 ctx.Request / ctx.Metadata
//	回写: ctx.Request -> env.TransformedRequest
//	      ctx.Metadata -> env.Metadata (merge)
type TransformHook struct {
	transformers []Transformer
}

// NewTransformHook 构造一个 Transform Hook，串联给定的 transformers。
//
// 允许传入 0 个 transformer（Hook 会成为 no-op）。
func NewTransformHook(transformers ...Transformer) *TransformHook {
	return &TransformHook{transformers: transformers}
}

// Name 返回 Hook 名称。
func (h *TransformHook) Name() string { return "transformation.apply" }

// Priority 返回 Hook 优先级。
func (h *TransformHook) Priority() int { return 100 }

// Enabled 报告 Hook 是否启用。
func (h *TransformHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.TransformedRequest == nil
}

// Execute 执行转换链。
func (h *TransformHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil {
		return nil
	}
	if env.Metadata == nil {
		env.Metadata = map[string]any{}
	}

	// 构造 transformation.Context
	tctx := Context{
		Request:  env.TransformedRequest,
		Metadata: env.Metadata,
	}

	// 顺序执行所有 transformer
	for _, t := range h.transformers {
		if t == nil {
			continue
		}
		if err := t.Transform(tctx); err != nil {
			return err
		}
	}

	// 回写 Request
	if tctx.Request != nil {
		env.TransformedRequest = tctx.Request
	}
	return nil
}

// OnError 转换失败时透传 error。
func (h *TransformHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err
}

// 编译期接口断言。
var _ pipeline.Hook = (*TransformHook)(nil)
