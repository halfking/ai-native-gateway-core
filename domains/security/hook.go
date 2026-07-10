package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domain"               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance"    // Verdict 类型
	"github.com/kaixuan/llm-gateway-go/domains/moduleexec"    // 模块执行记录器
	"github.com/kaixuan/llm-gateway-go/domains/moduleregistry" // 模块标识注册表
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// SecurityHook 把 Registry 接入 Pipeline。
//
// 行为：
//   - Enabled: env != nil
//   - Execute: 调 Registry.RunAll 并把所有 verdict 写入 env.EnsureGovernance()
//   - OnError: 吞掉 error（verdict 已写入，interception 引擎接管决策；
//     若某 plugin 直接 return error，Registry.RunAll 已把错误转 verdict）
//
// 适用阶段：PhaseGovernance。
//
// 2026-07-10: 集成模块执行器，支持 Check-Execute-Record 模式。
type SecurityHook struct {
	registry *Registry
	scope    Scope
	executor *moduleexec.Executor // 模块执行记录器（可选）
}

// NewSecurityHook 构造 hook。
func NewSecurityHook(registry *Registry, scope Scope) *SecurityHook {
	if registry == nil {
		registry = NewRegistry()
	}
	return &SecurityHook{registry: registry, scope: scope}
}

// SetExecutor 注入模块执行器。
// 启用 Check-Execute-Record 模式，相同请求的安全扫描结果会被缓存。
func (h *SecurityHook) SetExecutor(exec *moduleexec.Executor) {
	if h == nil {
		return
	}
	h.executor = exec
}

// Name 返回 hook 名（用于日志/调试）。
func (h *SecurityHook) Name() string { return "security.plugins" }

// Priority 优先级（与 v3 同名 hook 一致；同 phase 内排序使用）。
func (h *SecurityHook) Priority() int { return 100 }

// Enabled 报告 hook 是否启用。
func (h *SecurityHook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 调 Registry.RunAll 并写入 env.Governance。
func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 2026-07-10: 通过执行器执行安全扫描，结果会被缓存
	if h.executor != nil && env.SessionID != "" {
		return h.executeWithCache(ctx, env)
	}
	return h.executeDirectly(ctx, env)
}

// executeWithCache 通过执行器执行安全扫描（带缓存）
func (h *SecurityHook) executeWithCache(ctx context.Context, env *domain.PipelineRequest) error {
	params := securityParamsFromRequest(env)

	execResult, err := h.executor.CheckAndExecute(
		ctx, env.SessionID, env.TenantID,
		moduleregistry.ModuleSecurityScan,
		params, 0, // 使用模块默认 TTL（30分钟）
		func(ctx context.Context) (*moduleexec.ExecuteResult, error) {
			verdicts, err := h.registry.RunAll(ctx, env, h.scope)
			if err != nil {
				return nil, err
			}
			// 写入 Governance 状态
			state := env.EnsureGovernance()
			for _, v := range verdicts {
				state.RecordVerdict(v)
			}
			return &moduleexec.ExecuteResult{
				ResultSummary: verdictsToSummaryMap(verdicts),
				ResultDetail:  verdictsToDetailMap(verdicts),
			}, nil
		},
	)
	if err != nil {
		return err
	}

	// 如果是从缓存获取的，仍然需要写入 Governance 状态
	if execResult.FromCache {
		return h.applyCachedVerdicts(ctx, env, execResult)
	}
	return nil
}

// executeDirectly 直接执行安全扫描（不经过执行器）
func (h *SecurityHook) executeDirectly(ctx context.Context, env *domain.PipelineRequest) error {
	verdicts, err := h.registry.RunAll(ctx, env, h.scope)
	if err != nil {
		return err
	}
	state := env.EnsureGovernance()
	for _, v := range verdicts {
		state.RecordVerdict(v)
	}
	return nil
}

// applyCachedVerdicts 从缓存结果还原 verdicts 并写入 Governance
func (h *SecurityHook) applyCachedVerdicts(ctx context.Context, env *domain.PipelineRequest, result *moduleexec.ExecuteResult) error {
	// 从缓存结果中还原 verdicts
	verdicts, err := mapToVerdicts(result.ResultDetail)
	if err != nil {
		// 降级：如果缓存数据损坏，重新执行扫描
		return h.executeDirectly(ctx, env)
	}
	
	// 写入 Governance 状态
	state := env.EnsureGovernance()
	for _, v := range verdicts {
		state.RecordVerdict(v)
	}
	
	return nil
}

// mapToVerdicts 从 detail map 还原 verdicts
func mapToVerdicts(detail map[string]interface{}) ([]*governance.Verdict, error) {
	if detail == nil {
		return []*governance.Verdict{}, nil
	}
	
	verdictsRaw, ok := detail["verdicts"]
	if !ok {
		return []*governance.Verdict{}, nil
	}
	
	verdictsArray, ok := verdictsRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("verdicts is not an array, got %T", verdictsRaw)
	}
	
	verdicts := make([]*governance.Verdict, 0, len(verdictsArray))
	for i, vr := range verdictsArray {
		vm, ok := vr.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("verdict[%d] is not a map, got %T", i, vr)
		}
		
		verdict := &governance.Verdict{}
		
		// 安全的类型转换
		if v, ok := vm["plugin_name"].(string); ok {
			verdict.PluginName = v
		}
		if v, ok := vm["allow"].(bool); ok {
			verdict.Allow = v
		}
		if v, ok := vm["severity"].(float64); ok {
			verdict.Severity = int(v)
		}
		if v, ok := vm["code"].(string); ok {
			verdict.Code = v
		}
		if v, ok := vm["reason"].(string); ok {
			verdict.Reason = v
		}
		if v, ok := vm["fix_action"].(string); ok {
			verdict.FixAction = v
		}
		
		verdicts = append(verdicts, verdict)
	}
	
	return verdicts, nil
}

// securityParamsFromRequest 从请求构造缓存参数
func securityParamsFromRequest(env *domain.PipelineRequest) map[string]interface{} {
	// 使用请求内容的哈希作为缓存键
	var contentHash string
	if env.Envelope != nil && len(env.Envelope.Transport.BodyBytes) > 0 {
		h := sha256.Sum256(env.Envelope.Transport.BodyBytes)
		contentHash = hex.EncodeToString(h[:])[:16]
	}
	return map[string]interface{}{
		"content_hash": contentHash,
	}
}

// verdictsToSummaryMap 将 verdicts 转换为 summary map
func verdictsToSummaryMap(verdicts []*governance.Verdict) map[string]interface{} {
	allowCount, blockCount := 0, 0
	for _, v := range verdicts {
		if v.Allow {
			allowCount++
		} else {
			blockCount++
		}
	}
	return map[string]interface{}{
		"total":       len(verdicts),
		"allow_count": allowCount,
		"block_count": blockCount,
	}
}

// verdictsToDetailMap 将 verdicts 转换为 detail map
func verdictsToDetailMap(verdicts []*governance.Verdict) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(verdicts))
	for _, v := range verdicts {
		items = append(items, map[string]interface{}{
			"plugin_name": v.PluginName,
			"allow":       v.Allow,
			"severity":    v.Severity,
			"code":        v.Code,
			"reason":      v.Reason,
			"fix_action":  v.FixAction,
		})
	}
	return map[string]interface{}{"verdicts": items}
}

// OnError 吞掉错误（verdict 已写入；interception 引擎在后续阶段做决策）。
func (h *SecurityHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return nil
}

// Registry 返回内部 registry（用于测试与 telemetry）。
func (h *SecurityHook) Registry() *Registry { return h.registry }

// 编译期断言。
var _ pipeline.Hook = (*SecurityHook)(nil)
