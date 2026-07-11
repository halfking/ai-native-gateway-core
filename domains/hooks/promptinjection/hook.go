// Package promptinjection 把 domains/promptinjection 接入 V4 Pipeline。
//
// 设计：Hook 不直接依赖具体 Detector 类型，而是依赖最小接口 Detector，
// 由调用方注入具体实现（*promptinjection.Detector 或测试桩）。
// 这样本包可以独立编译、单元测试，无需 DB。
package promptinjection

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// DetectionResult 是 promptinjection.Detector.Detect 的返回的精简镜像。
// 避免直接 import domains/promptinjection（DB 强依赖）。
type DetectionResult struct {
	Score              int
	RiskLevel          string   // low/medium/high/critical
	Categories         []string // 风险类别列表
	ActionTaken        string   // pass/log/warn/replace/redact/remove/reject/terminate/approve/block
	Blocked            bool
	RequireApproval    bool
	ApprovalTimeoutMin int
	Evidence           string
	ReplacedContent    string
	CanaryTokenLeaked  string
	LLMConfidence      float64
	LLMReason          string
	SessionHealthDelta int
	TerminateSession   bool
}

// Detector 是 Hook 所需的最小接口；*promptinjection.Detector 天然实现。
type Detector interface {
	Detect(ctx context.Context, tenantID, input string) (*DetectionResult, error)
}

// Hook 把 prompt injection 检测接入 V4 Pipeline。
//
// 在 PhaseGovernance 执行，读 env.Metadata["user_content"] 做检测，
// 结果写入 env.EnsureGovernance()。根据检测结果执行不同动作：
//   - pass/log: 继续流程
//   - warn: 继续流程，添加警告头
//   - replace/redact/remove: 替换内容后继续
//   - reject/block: 中断流程，返回 403
//   - approve: 暂停流程，等待审批
//   - terminate: 终止会话
type Hook struct {
	detector Detector
}

// NewHook 构造 Hook。detector 为 nil 时 Enabled() 返回 false（等价于未注册）。
func NewHook(detector Detector) *Hook {
	return &Hook{detector: detector}
}

// Name 实现 pipeline.Hook。
func (h *Hook) Name() string { return "prompt_injection.detect" }

// Priority 在 Governance 阶段中靠后（security 已先跑）。
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

	// 保存检测结果到 metadata
	env.Metadata["prompt_injection_result"] = map[string]any{
		"score":                result.Score,
		"risk_level":           result.RiskLevel,
		"categories":           result.Categories,
		"action_taken":         result.ActionTaken,
		"blocked":              result.Blocked,
		"require_approval":     result.RequireApproval,
		"llm_confidence":       result.LLMConfidence,
		"session_health_delta": result.SessionHealthDelta,
	}

	// 写入 governance verdict
	gv := toGovernanceVerdict(result)
	if gv != nil {
		env.EnsureGovernance().RecordVerdict(gv)
	}

	// 根据动作类型处理
	switch result.ActionTaken {
	case "pass", "log":
		// 无影响，继续流程
		return nil

	case "warn":
		// 添加警告头，继续流程
		env.Metadata["security_warning"] = fmt.Sprintf(
			"Prompt injection detected (risk=%s, score=%d)", result.RiskLevel, result.Score)
		return nil

	case "replace", "redact", "remove":
		// 替换内容后继续
		if result.ReplacedContent != "" {
			env.Metadata["user_content"] = result.ReplacedContent
			env.Metadata["content_replaced"] = true
			env.Metadata["replacement_action"] = result.ActionTaken
		}
		return nil

	case "reject", "block":
		// 中断流程
		return fmt.Errorf("prompt_injection: request %s (risk=%s, score=%d)",
			result.ActionTaken, result.RiskLevel, result.Score)

	case "approve":
		// 需要审批，设置 suspension
		env.Metadata["approval_required"] = true
		env.Metadata["approval_timeout_minutes"] = result.ApprovalTimeoutMin
		return fmt.Errorf("prompt_injection: approval required (risk=%s, score=%d)",
			result.RiskLevel, result.Score)

	case "terminate":
		// 终止会话
		env.Metadata["session_terminated"] = true
		env.Metadata["terminate_reason"] = "prompt_injection_critical"
		return fmt.Errorf("prompt_injection: session terminated (risk=%s, score=%d)",
			result.RiskLevel, result.Score)

	default:
		return nil
	}
}

// OnError 根据动作类型设置不同的 HTTP 状态码。
func (h *Hook) OnError(_ context.Context, env *domain.PipelineRequest, err error) error {
	if env == nil {
		return nil
	}

	errMsg := err.Error()

	switch {
	case contains(errMsg, "approval required"):
		// 审批请求返回 202 Accepted
		env.StatusCode = 202
	case contains(errMsg, "session terminated"):
		// 会话终止返回 410 Gone
		env.StatusCode = 410
	default:
		// 默认返回 403 Forbidden
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
			"score":          r.Score,
			"action_taken":   r.ActionTaken,
			"evidence":       r.Evidence,
			"categories":     r.Categories,
			"llm_confidence": r.LLMConfidence,
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

	// 设置 FixAction
	switch r.ActionTaken {
	case "sanitize", "replace", "redact", "remove":
		gv.FixAction = "sanitize_input"
	case "reject", "block":
		gv.FixAction = "abort_request"
	case "approve":
		gv.FixAction = "require_approval"
	case "terminate":
		gv.FixAction = "terminate_session"
	}

	return gv
}

// contains 检查字符串是否包含子串
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

var _ pipeline.Hook = (*Hook)(nil)
