// Package interception 是 V4 治理平台的同步拦截域（PR-V4-04 引入）。
//
// 核心职责：
//   - 读取 PipelineRequest.Governance.Verdicts（由 domains/security plugins 写入）
//   - 按 EngineConfig 规则产出 governance.Decision（continue / block / mutate / suspend / terminate）
//   - 把决策写回 PipelineRequest.Governance.Decision 供后续阶段消费
//
// 本包不直接修改 HTTP 响应；阻断/挂起的实际执行由更高层（PR-V4-05+ 的
// dispatch gate / response writer）根据 Decision 统一处理。
package interception

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// EngineConfig 拦截引擎配置。
type EngineConfig struct {
	// BlockThreshold severity >= 此值 → Block。0 = 默认 2。
	BlockThreshold int

	// SuspendOnCritical severity >= 3 时挂起审批而非直接阻断。默认 false。
	// 启用后，critical 风险改为 DecisionSuspend + WaitFor=Approval。
	SuspendOnCritical bool

	// CriticalRiskLevel 写入 ApprovalRequest.RiskLevel。默认 "high"。
	CriticalRiskLevel string

	// BlockSeverityBoost 让 deny-but-low-severity verdict 升级为更严重。
	// 例如设 1，则任何 Allow=false 都视为 severity=2。默认 0（不升级）。
	BlockSeverityBoost int
}

func (c *EngineConfig) applyDefaults() {
	if c.BlockThreshold <= 0 {
		c.BlockThreshold = 2
	}
	if c.CriticalRiskLevel == "" {
		c.CriticalRiskLevel = "high"
	}
}

// Engine 拦截引擎。
//
// 纯逻辑：不持有 DB / HTTP / LLM 依赖，便于测试与复用。
type Engine struct {
	cfg EngineConfig
}

// NewEngine 构造拦截引擎。
func NewEngine(cfg EngineConfig) *Engine {
	cfg.applyDefaults()
	return &Engine{cfg: cfg}
}

// Config 返回当前生效配置（用于 telemetry / admin UI 展示）。
func (e *Engine) Config() EngineConfig { return e.cfg }

// Decide 根据 env.Governance.Verdicts 产出 Decision 并写回。
//
// 决策规则（按优先级匹配）：
//  1. 无 verdict 或全部 Allow=true → Continue
//  2. 存在 Allow=false 且最高 severity >= 3 且 SuspendOnCritical=true → Suspend(WaitFor=Approval)
//  3. 存在 Allow=false 且最高 severity >= BlockThreshold → Block
//  4. 其他情况（含低 severity 的 deny） → Continue
//
// 返回 error 仅用于参数错误（nil envelope）；业务决策通过 *Decision 表达。
func (e *Engine) Decide(ctx context.Context, env *domain.PipelineRequest) (*governance.Decision, error) {
	if env == nil {
		return nil, fmt.Errorf("interception: nil envelope")
	}

	state := env.EnsureGovernance()
	highest := effectiveSeverity(state, e.cfg.BlockSeverityBoost)
	hasBlock := state.HasBlock()

	var d *governance.Decision
	switch {
	case len(state.Verdicts) == 0:
		d = newDecision(governance.DecisionContinue, "no verdicts collected", "")
	case hasBlock && highest >= 3 && e.cfg.SuspendOnCritical:
		d = newDecision(governance.DecisionSuspend, "critical severity; awaiting approval", blockReason(state.Verdicts))
		d.Suspension = &governance.SuspensionSpec{
			WaitFor: governance.WaitForApproval,
			Approval: &governance.ApprovalRequest{
				RiskLevel: e.cfg.CriticalRiskLevel,
				Reason:    "critical verdict from governance plugins",
				SessionID: env.SessionID,
				RequestID: envelopeRequestID(env),
				Snapshot:  snapshotVerdicts(state.Verdicts),
			},
		}
	case hasBlock && highest >= e.cfg.BlockThreshold:
		d = newDecision(governance.DecisionBlock, blockReason(state.Verdicts), "")
	default:
		d = newDecision(governance.DecisionContinue, "all verdicts allow or below threshold", blockReasonIfDeny(state.Verdicts))
	}

	state.RecordDecision(d)
	return d, nil
}

// effectiveSeverity 计算当前最高 severity，叠加可选 boost。
func effectiveSeverity(state *governance.GovernanceState, boost int) int {
	h := state.HighestSeverity()
	if boost > 0 && h >= 0 && h < boost {
		h = boost
	}
	return h
}

func newDecision(kind governance.DecisionKind, reason, detail string) *governance.Decision {
	if detail != "" && reason != "" && !strings.HasSuffix(reason, ": "+detail) {
		// 把首条 deny verdict 的 plugin+reason 拼到主 reason 后，便于运维定位
		reason = reason + " | first_deny: " + detail
	}
	return &governance.Decision{
		Kind:      kind,
		Reason:    reason,
		CreatedAt: time.Now(),
	}
}

func blockReason(vs []*governance.Verdict) string {
	for _, v := range vs {
		if v != nil && !v.Allow {
			return fmt.Sprintf("[%s] %s", v.PluginName, v.Reason)
		}
	}
	return "blocked by unknown plugin"
}

func blockReasonIfDeny(vs []*governance.Verdict) string {
	for _, v := range vs {
		if v != nil && !v.Allow {
			return fmt.Sprintf("low_severity_deny=[%s] %s", v.PluginName, v.Reason)
		}
	}
	return ""
}

func snapshotVerdicts(vs []*governance.Verdict) any {
	out := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		if v == nil {
			continue
		}
		out = append(out, map[string]any{
			"plugin":   v.PluginName,
			"allow":    v.Allow,
			"severity": v.Severity,
			"code":     v.Code,
			"reason":   v.Reason,
		})
	}
	return out
}

func envelopeRequestID(env *domain.PipelineRequest) string {
	if env.Envelope != nil {
		return env.Envelope.RequestID
	}
	return ""
}
