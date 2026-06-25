package sessioninspector

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// Metadata keys used by InspectorHook.
const (
	// MetaKeySessionFindings session_findings 元数据键。
	// PipelineRequest.Metadata[MetaKeySessionFindings] = []*Finding
	MetaKeySessionFindings = "session_findings"

	// MetaKeyRequestCount request_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyRequestCount] = int
	MetaKeyRequestCount = "request_count"

	// MetaKeyTokenCount token_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyTokenCount] = int
	MetaKeyTokenCount = "token_count"

	// MetaKeyLastActiveAt last_active_at 元数据键。
	// PipelineRequest.Metadata[MetaKeyLastActiveAt] = time.Time
	MetaKeyLastActiveAt = "last_active_at"
)

// InspectorHook 检查器编排 Hook。
//
// 行为：
//   - Enabled: env != nil 且 env.SessionID != ""（没有 session 就没有快照）
//   - Execute: 从 env 构造 SessionSnapshot，依次调用所有 Inspector，
//     把全部 findings 写入 env.Metadata[MetaKeySessionFindings]。
//   - OnError: 吞掉错误（检查器失败可降级，不影响主流程）。
//
// 适用阶段：PreRouting / PostResponse。
type InspectorHook struct {
	inspectors []Inspector
}

// NewInspectorHook 构造 Hook。
func NewInspectorHook(inspectors ...Inspector) *InspectorHook {
	return &InspectorHook{inspectors: inspectors}
}

// Add 追加一个 Inspector（允许运行时注册）。
func (h *InspectorHook) Add(i Inspector) {
	if i == nil {
		return
	}
	h.inspectors = append(h.inspectors, i)
}

// Inspectors 返回已注册的 inspector 列表（只读副本）。
func (h *InspectorHook) Inspectors() []Inspector {
	out := make([]Inspector, len(h.inspectors))
	copy(out, h.inspectors)
	return out
}

// Name 返回 Hook 名。
func (h *InspectorHook) Name() string { return "session.inspect" }

// Priority 返回优先级（晚于认证/会话解析，早于路由决策）。
func (h *InspectorHook) Priority() int { return 100 }

// Enabled 仅当 env 非 nil 且存在 SessionID 时启用。
func (h *InspectorHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.SessionID != ""
}

// Execute 顺序执行所有 Inspector 并收集 findings。
func (h *InspectorHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil || env.SessionID == "" {
		// 没有 session_id -> 与 Enabled 保持一致，跳过检查
		return nil
	}
	snap := h.buildSnapshot(env)

	var all []*Finding
	for _, ins := range h.inspectors {
		if ins == nil {
			continue
		}
		findings, err := ins.Inspect(snap)
		if err != nil {
			return err
		}
		all = append(all, findings...)
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata[MetaKeySessionFindings] = all
	return nil
}

// buildSnapshot 从 PipelineRequest 构造 SessionSnapshot。
func (h *InspectorHook) buildSnapshot(env *domain.PipelineRequest) *SessionSnapshot {
	snap := &SessionSnapshot{
		SessionID: env.SessionID,
		TenantID:  env.TenantID,
		Metadata:  env.Metadata,
	}
	if env.Metadata == nil {
		return snap
	}
	if rc, ok := env.Metadata[MetaKeyRequestCount].(int); ok {
		snap.RequestCount = rc
	}
	if tc, ok := env.Metadata[MetaKeyTokenCount].(int); ok {
		snap.TokenCount = tc
	}
	if la, ok := env.Metadata[MetaKeyLastActiveAt].(time.Time); ok {
		snap.LastActiveAt = la
	}
	return snap
}

// OnError 吞掉错误（检查器失败可降级）。
func (h *InspectorHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// 编译期断言。
var _ pipeline.Hook = (*InspectorHook)(nil)
