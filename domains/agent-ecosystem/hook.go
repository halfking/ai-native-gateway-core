package agentecosystem

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// Metadata keys used by AgentDiscoveryHook.
const (
	// MetaKeyRequiredCapability required_capability 元数据键。
	// PipelineRequest.Metadata[MetaKeyRequiredCapability] = string
	MetaKeyRequiredCapability = "required_capability"

	// MetaKeyDiscoveredAgents discovered_agents 元数据键。
	// PipelineRequest.Metadata[MetaKeyDiscoveredAgents] = []*Agent
	MetaKeyDiscoveredAgents = "discovered_agents"
)

// AgentDiscoveryHook 智能体发现 Hook。
//
// 行为：
//   - Enabled: env != nil
//   - Execute: 从 env.Metadata[MetaKeyRequiredCapability] 读取所需能力名，
//     用 Registry.FindByCapability 查找候选 agent，写入
//     env.Metadata[MetaKeyDiscoveredAgents]。
//   - OnError: 吞掉错误（发现失败可降级，由后续 Hook 继续）。
//
// 适用阶段：PhasePreRouting（在 routing 之前发现候选）。
type AgentDiscoveryHook struct {
	registry *Registry
}

// NewAgentDiscoveryHook 构造 Hook。
func NewAgentDiscoveryHook(registry *Registry) *AgentDiscoveryHook {
	return &AgentDiscoveryHook{registry: registry}
}

// Name 返回 Hook 名。
func (h *AgentDiscoveryHook) Name() string { return "agent.discover" }

// Priority 返回优先级（在 PreRouting 阶段晚于认证/会话解析）。
func (h *AgentDiscoveryHook) Priority() int { return 200 }

// Enabled 仅当 env 非 nil 时启用。
func (h *AgentDiscoveryHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 执行 agent 发现。
func (h *AgentDiscoveryHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if h.registry == nil || env == nil {
		return nil
	}
	raw, _ := env.Metadata[MetaKeyRequiredCapability].(string)
	if raw == "" {
		// 未声明需求 capability -> 跳过发现
		return nil
	}
	agents := h.registry.FindByCapability(raw)
	if len(agents) == 0 {
		// 无候选 -> 不写入 metadata（避免下游误读空列表）
		return nil
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata[MetaKeyDiscoveredAgents] = agents
	return nil
}

// OnError 吞掉错误（发现失败可降级）。
func (h *AgentDiscoveryHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// 编译期断言。
var _ pipeline.Hook = (*AgentDiscoveryHook)(nil)
