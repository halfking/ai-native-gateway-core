// Package promptinjection 把 domains/promptinjection 接入 V4 Pipeline。
//
// 设计：Hook 不直接依赖具体 Detector 类型，而是依赖最小接口 Detector，
// 由调用方注入具体实现（*promptinjection.Detector 或测试桩）。
// 这样本包可以独立编译、单元测试，无需 DB。
package promptinjection

import (
	"context"
	"errors"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// DetectionResult 是 promptinjection.Detector.Detect 的返回的精简镜像。
// 避免直接 import domains/promptinjection（DB 强依赖）。
type DetectionResult struct {
	Score       int
	RiskLevel   string // low/medium/high/critical
	ActionTaken string // pass/log/warn/sanitize/block
	Blocked     bool
	Evidence    string
}

// Detector 是 Hook 所需的最小接口；*promptinjection.Detector 天然实现。
type Detector interface {
	Detect(ctx context.Context, tenantID, input string) (*DetectionResult, error)
}

// Hook 把 prompt injection 检测接入 V4 Pipeline。
//
// 在 PhasePreRouting 执行，读 env.Metadata["user_content"] 做检测，
// 结果写入 env.EnsureGovernance()。检测命中阻断阈值时返回 error
// 让 Pipeline 中断（dispatch gate 写 403）。
type Hook struct {
	detector Detector
}

// NewHook 构造 Hook。detector 为 nil 时 Enabled() 返回 false（等价于未注册）。
func NewHook(detector Detector) *Hook {
	return &Hook{detector: detector}
}

// Name 实现 pipeline.Hook。
func (h *Hook) Name() string { return "prompt_injection.detect" }

// Priority 在 PreRouting 阶段中靠后（security 已先跑）。
func (h *Hook) Priority() int { return 120 }

// Enabled 仅当 detector 非 nil 且请求有 user_content 时启用。
func (h *Hook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	if h == nil || h.detector == nil || env == nil {
		return false
	}
	content, _ := env.Metadata["user_content"].(string)
	return content != ""
}

// Execute 执行检测并写入 governance verdict。
func (h *Hook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	content, _ := env.Metadata["user_content"].(string)
	if content == "" {
		return nil
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	tenant := env.TenantID
	result, err := h.detector.Detect(ctx, tenant, content)
	if err != nil {
		// 检测器故障不应阻断主流程，降级为 verdict=allow + 日志 metadata。
		env.Metadata["prompt_injection_error"] = err.Error()
		return nil
	}
	env.Metadata["prompt_injection_result"] = map[string]any{
		"score":        result.Score,
		"risk_level":   result.RiskLevel,
		"action_taken": result.ActionTaken,
		"blocked":      result.Blocked,
	}
	gv := toGovernanceVerdict(result)
	if gv != nil {
		env.EnsureGovernance().RecordVerdict(gv)
	}
	if result.Blocked {
		return errors.New("prompt_injection: request blocked (risk=" + result.RiskLevel + ")")
	}
	return nil
}

// OnError 阻断时设 403。
func (h *Hook) OnError(_ context.Context, env *domain.PipelineRequest, _ error) error {
	if env != nil {
		env.StatusCode = 403
	}
	return nil
}

// toGovernanceVerdict 把检测结果映射到 governance.Verdict。
//
// Severity 映射（domain/governance 约定 0=info 1=warn 2=block 3=critical）：
//   - low → 0
//   - medium → 1
//   - high → 2
//   - critical → 3
func toGovernanceVerdict(r *DetectionResult) *governance.Verdict {
	if r == nil {
		return nil
	}
	gv := &governance.Verdict{
		PluginName: "prompt_injection",
		Allow:      !r.Blocked,
		Code:       "prompt_injection." + r.RiskLevel,
		Reason:     "risk_level=" + r.RiskLevel + " action=" + r.ActionTaken,
		Evidence: map[string]any{
			"score":        r.Score,
			"action_taken": r.ActionTaken,
			"evidence":     r.Evidence,
		},
	}
	switch r.RiskLevel {
	case "medium":
		gv.Severity = 1
	case "high":
		gv.Severity = 2
	case "critical":
		gv.Severity = 3
	}
	if r.ActionTaken == "sanitize" {
		gv.FixAction = "sanitize_input"
	}
	return gv
}

var _ pipeline.Hook = (*Hook)(nil)
