// Package outputcompliance — interceptor.go
//
// OutputComplianceInterceptor 把 output_compliance.Checker 接入 V1 ChatHandler
// 的响应侧 ResponseInterceptor 链。这是让"输出脱敏"在生产路径生效的正确接入点
// （见 docs/2026-07-09-session-tagging-redaction-architecture.md §2.3）。
//
// 设计：
//   - 实现 response.ResponseInterceptor（与 goal/audit hook 同一链式插件机制）。
//   - InterceptNonStream：解析 OpenAI choices[].message.content，调 Checker.Check，
//     按 redaction_mode + owner==caller 规则决定脱敏，返回 ModifiedBody。
//   - InterceptStreamEnd：流结束时 body 已被上游重组为非流式形态，复用同一逻辑。
//   - InterceptStreamChunk：透传（chunk 级脱敏为未来增强，本轮不做）。
//
// owner 上下文由调用方（ChatHandler）通过 OwnerContextFunc 提供，从 KeyInfo.OwnerUser
// 取 callerOwner，从 session_dim/请求行取 dataOwner。未提供时按"调用方无身份"保守脱敏。
package outputcompliance

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// OwnerContextFunc 返回一次请求的 (callerOwner, dataOwner)。
// callerOwner 来自 KeyInfo.OwnerUser；dataOwner 来自会话/请求行的 owner_user。
// 返回空串表示无该侧身份（按保守规则脱敏）。
type OwnerContextFunc func(sessionID, tenantID string) (callerOwner, dataOwner string)

// OutputComplianceInterceptor 实现 response.ResponseInterceptor。
type OutputComplianceInterceptor struct {
	checker     *outputcompliance.Checker
	ownerFn     OwnerContextFunc // 可空：为 nil 时 caller/data owner 均视为空（保守脱敏）
}

// NewOutputComplianceInterceptor 构造拦截器。checker 必须非 nil；ownerFn 可为 nil。
func NewOutputComplianceInterceptor(checker *outputcompliance.Checker, ownerFn OwnerContextFunc) *OutputComplianceInterceptor {
	return &OutputComplianceInterceptor{checker: checker, ownerFn: ownerFn}
}

// redactionMode 读取 output_compliance.redaction_mode 设置（热加载）。
func redactionMode() outputcompliance.RedactionMode {
	if settings.Global == nil {
		return outputcompliance.RedactOwnerMismatch
	}
	sp := settings.Global.Spec("output_compliance.redaction_mode")
	if sp == nil {
		return outputcompliance.RedactOwnerMismatch
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
	if err != nil || len(raw) == 0 {
		return outputcompliance.RedactOwnerMismatch
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return outputcompliance.RedactOwnerMismatch
	}
	return outputcompliance.RedactionMode(s)
}

// enabled 读取 output_compliance.enabled。
func enabled() bool {
	if settings.Global == nil {
		return false
	}
	sp := settings.Global.Spec("output_compliance.enabled")
	if sp == nil {
		return false
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
	if err != nil || len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false
	}
	return b
}

// InterceptNonStream 处理非流式响应：检测 + owner 规则脱敏。
func (it *OutputComplianceInterceptor) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if it == nil || it.checker == nil || req == nil || len(req.ResponseBody) == 0 {
		return nil, nil
	}
	if !enabled() {
		return nil, nil
	}
	return it.processBody(ctx, req)
}

// InterceptStreamChunk 透传（chunk 级脱敏为未来增强）。
func (it *OutputComplianceInterceptor) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

// InterceptStreamEnd 流结束：body 已重组为非流式形态，复用非流式逻辑。
func (it *OutputComplianceInterceptor) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	if it == nil || it.checker == nil || meta == nil || len(meta.ResponseBody) == 0 {
		return nil, nil
	}
	if !enabled() {
		return nil, nil
	}
	req := &response.InterceptRequest{
		SessionID:    meta.SessionID,
		RequestID:    meta.RequestID,
		TenantID:     meta.TenantID,
		ClientModel:  meta.ClientModel,
		ResponseBody: meta.ResponseBody,
		TokensUsed:   meta.TokensUsed,
		ContextWindow: meta.ContextWindow,
		MessageCount: meta.MessageCount,
		FinishReason: meta.FinishReason,
	}
	res, err := it.processBody(ctx, req)
	if err != nil || res == nil {
		return nil, err
	}
	// EndResult 不带 ModifiedBody（stream 已发送），仅用 Metadata 传递脱敏标记。
	end := &response.EndResult{}
	if res.Metadata != nil {
		end.Metadata = res.Metadata
	}
	return end, nil
}

// processBody 是非流式/流结束共用的核心逻辑。
func (it *OutputComplianceInterceptor) processBody(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	output := string(req.ResponseBody)

	// 1. 检测
	result, err := it.checker.Check(ctx, req.TenantID, output)
	if err != nil {
		// 故障降级为放行（避免误杀），与既有 hook.go 语义一致
		slog.Warn("output_compliance_interceptor: check failed, degrading to allow",
			"error", err, "session_id", req.SessionID)
		return nil, nil
	}

	mode := redactionMode()
	callerOwner, dataOwner := "", ""
	if it.ownerFn != nil {
		callerOwner, dataOwner = it.ownerFn(req.SessionID, req.TenantID)
	}
	shouldRedact := outputcompliance.ShouldRedact(mode, callerOwner, dataOwner)

	// 2. 阻断（仅在 enforce + 严重时，由 checker 决定）
	if result.Blocked {
		return &response.InterceptResult{ShouldBlock: true, Action: "output_compliance_block"}, nil
	}

	// 3. 脱敏判定
	redacted := false
	modified := req.ResponseBody
	if shouldRedact && result.RedactedOutput != "" && result.RedactedOutput != output {
		// checker.RedactedOutput 是对"整个 output 字符串"脱敏后的结果。
		// 但 ResponseBody 是完整 JSON（含 choices/usage），需要把脱敏后的
		// assistant content 回填进 JSON 结构，而非整体替换。
		if rebuilt, ok := rewriteAssistantContent(req.ResponseBody, result.RedactedOutput); ok {
			modified = rebuilt
			redacted = true
		} else {
			// 回填失败时降级为不脱敏（保守：不破坏响应结构）
			slog.Warn("output_compliance_interceptor: rewriteAssistantContent failed, skipping redaction",
				"session_id", req.SessionID)
		}
	}

	out := &response.InterceptResult{}
	if redacted {
		out.ModifiedBody = modified
		out.Action = "output_compliance_redact"
		out.Metadata = map[string]interface{}{
			"output_compliance_redacted": true,
			"pii_stripped":               true, // 点亮 cache_update_hook 的悬空契约
			"issue_count":                len(result.Issues),
			"redaction_mode":             string(mode),
		}
	}
	return out, nil
}

// rewriteAssistantContent 把"完整响应 JSON 中 choices[].message.content"替换为
// 脱敏后的纯文本。redactedContent 是 Checker 对提取出的 assistant 文本脱敏后的结果。
//
// 实现策略：解析 JSON → 取第一个 assistant choice 的 content → 用 redactedContent
// 替换 → 重新序列化。保留其它字段（usage/finish_reason 等）不变。
func rewriteAssistantContent(body []byte, redactedContent string) ([]byte, bool) {
	// 尝试 OpenAI 格式
	var openai struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &openai); err != nil {
		return nil, false
	}
	if len(openai.Choices) == 0 {
		return nil, false
	}
	// 用 map 重新序列化以保留未知字段
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}
	choices, ok := raw["choices"].([]any)
	if !ok {
		return nil, false
	}
	changed := false
	for _, c := range choices {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := cm["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" && role != "" {
			// 只改 assistant 消息；空 role 的也改（兼容）
		}
		if _, exists := msg["content"]; exists {
			msg["content"] = redactedContent
			changed = true
			break // 只改第一个 assistant
		}
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	return out, true
}
