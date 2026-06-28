package interception

import (
	"encoding/json"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

// DispatchOutcome dispatch gate 对请求的处置结果。
type DispatchOutcome int

const (
	// DispatchContinue 表示继续把请求交给 chatHandler 等真正的上游处理者。
	DispatchContinue DispatchOutcome = iota

	// DispatchShortCircuit 表示 dispatch gate 已经准备好阻断/挂起响应，
	// 调用方应写完响应后立即 return，不要再调用 fallback。
	DispatchShortCircuit
)

// InspectDecision 读取 env.Governance.Decision 并返回处置结果。
//
// forward-compatible：未识别 / 未设置 Decision 一律按 Continue 处理，
// 避免新增 DecisionKind 时影响既有 dispatch 行为。
func InspectDecision(env *domain.PipelineRequest) DispatchOutcome {
	if env == nil {
		return DispatchContinue
	}
	state := env.Governance
	if state == nil || state.Decision == nil {
		return DispatchContinue
	}
	switch state.Decision.Kind {
	case governance.DecisionBlock,
		governance.DecisionSuspend,
		governance.DecisionTerminate:
		return DispatchShortCircuit
	default:
		return DispatchContinue
	}
}

// WriteDecisionResponse 根据 env.Governance.Decision 生成 (statusCode, body)。
//
// 返回值约定：
//   - statusCode = http.StatusOK 时 body 为 nil，调用方应继续走 fallback
//   - 其他 statusCode 必须写响应
func WriteDecisionResponse(env *domain.PipelineRequest) (int, []byte) {
	if env == nil || env.Governance == nil || env.Governance.Decision == nil {
		return http.StatusOK, nil
	}
	d := env.Governance.Decision
	switch d.Kind {
	case governance.DecisionBlock:
		return http.StatusForbidden, blockBody(d)
	case governance.DecisionSuspend:
		return http.StatusAccepted, suspendBody(d)
	case governance.DecisionTerminate:
		return http.StatusGone, terminateBody(d)
	default:
		return http.StatusOK, nil
	}
}

// ErrorBody 公共错误响应结构（避免在 Block / Terminate 中重复）。
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 错误细节。
type ErrorDetail struct {
	Code         string `json:"code"`
	Reason       string `json:"reason"`
	TraceID      string `json:"trace_id,omitempty"`
	SourcePlugin string `json:"source_plugin,omitempty"`
}

func blockBody(d *governance.Decision) []byte {
	code := "governance_blocked"
	if d.Suspension != nil && d.Suspension.Approval != nil {
		// critical 但配了 SuspendOnCritical 时，Engine 不会到这里；
		// 保留分支以防御性兼容未来策略。
		code = "governance_blocked_after_suspend_eval"
	}
	body := ErrorBody{
		Error: ErrorDetail{
			Code:         code,
			Reason:       d.Reason,
			TraceID:      d.TraceID,
			SourcePlugin: d.SourcePlugin,
		},
	}
	return mustMarshal(body)
}

// SuspendBody 挂起响应（202 Accepted）。
//
// 当前 PR-V4-05 不接 sessionaudit，approval_id 留空并加 polling_url 待办；
// PR-V4-06 起接入 sessionaudit.ApprovalManager 填充真实 approval_id。
type SuspendBody struct {
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	TraceID    string `json:"trace_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	RiskLevel  string `json:"risk_level,omitempty"`
	PollingURL string `json:"polling_url,omitempty"`
}

func suspendBody(d *governance.Decision) []byte {
	body := SuspendBody{
		Status:  "pending",
		Reason:  d.Reason,
		TraceID: d.TraceID,
		// polling_url 留给 sessionaudit 集成阶段填充；当前为空字符串（JSON 省略）
	}
	if d.Suspension != nil && d.Suspension.Approval != nil {
		body.RiskLevel = d.Suspension.Approval.RiskLevel
		// ApprovalID 留给 PR-V4-06 通过 sessionaudit.ApprovalManager.Create 填充
	}
	return mustMarshal(body)
}

func terminateBody(d *governance.Decision) []byte {
	body := ErrorBody{
		Error: ErrorDetail{
			Code:   "session_terminated",
			Reason: d.Reason,
		},
	}
	return mustMarshal(body)
}

func mustMarshal(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		// 不应发生：所有字段都是 string / 内置类型
		return []byte(`{"error":{"code":"internal_marshal_failure"}}`)
	}
	return out
}
