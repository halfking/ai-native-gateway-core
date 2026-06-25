package tools

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// ToolInterceptionHook 工具拦截 Hook
type ToolInterceptionHook struct {
	interceptors []Interceptor
}

// NewToolInterceptionHook 创建工具拦截 Hook
func NewToolInterceptionHook(interceptors ...Interceptor) *ToolInterceptionHook {
	return &ToolInterceptionHook{interceptors: interceptors}
}

// Name 返回 Hook 名称
func (h *ToolInterceptionHook) Name() string { return "tools.intercept" }

// Priority 返回优先级
func (h *ToolInterceptionHook) Priority() int { return 100 }

// Enabled 是否启用
func (h *ToolInterceptionHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if env == nil {
		return false
	}
	calls, _ := env.Metadata["tool_calls"].([]*ToolCall)
	return len(calls) > 0
}

// Execute 依次执行所有拦截器
func (h *ToolInterceptionHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	calls, _ := env.Metadata["tool_calls"].([]*ToolCall)
	ictx := Context{
		Calls:    calls,
		TenantID: env.TenantID,
		Metadata: env.Metadata,
	}
	for _, it := range h.interceptors {
		modified, err := it.Intercept(ictx)
		if err != nil {
			return fmt.Errorf("interceptor %q failed: %w", it.Name(), err)
		}
		ictx.Calls = modified
	}
	env.Metadata["tool_calls"] = ictx.Calls
	return nil
}

// OnError 错误处理（工具拦截失败必须上报）
func (h *ToolInterceptionHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err
}

var _ pipeline.Hook = (*ToolInterceptionHook)(nil)
