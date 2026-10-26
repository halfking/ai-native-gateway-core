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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// OwnerContextFunc 返回一次请求的 data owner（来自会话/请求行的 owner_user）。
// callerOwner 由请求元数据携带（KeyInfo.OwnerUser → meta.CallerOwner），
// data owner 需要回查 session_dim 或 PG；返回空串表示无该侧身份（按保守
// 规则脱敏）。ctx 用于上游 owner 查询的 timeout / cancellation 传递（V3-A01）。
type OwnerContextFunc func(ctx context.Context, sessionID, tenantID string) (dataOwner string)

// OutputComplianceInterceptor 实现 response.ResponseInterceptor。
type OutputComplianceInterceptor struct {
	checker interface {
		Check(context.Context, string, string) (*outputcompliance.ComplianceResult, error)
	}
	ownerFn   OwnerContextFunc // 可空：为 nil 时 caller/data owner 均视为空（保守脱敏）
	mandatory bool
	stateKey  string

	// restoredValues 解析「本次响应已还原值」的来源，可空。为 nil 时豁免机制
	// 整体关闭（行为与旧版一致），因此未装配 sanitize 的部署不受影响。
	restoredValues func(ctx context.Context, sessionID, tenantID string) ([]string, error)
}

// SetRestoredValueSource 注入「本次响应已还原值」的来源。
//
// 存在的理由只有一个：输出合规排在占位符还原**之后**（链序见
// cmd/gateway/goal_control.go），因此它看到的文本里混着模型写的值和网关刚
// 还原回去的调用方原值，两者不可分辨。旧实现一视同仁地遮蔽，用户自己发来的
// IP/账号刚被还原出来就被压成星号且换不回去。
func (it *OutputComplianceInterceptor) SetRestoredValueSource(fn func(ctx context.Context, sessionID, tenantID string) ([]string, error)) {
	if it == nil || it.mandatory {
		// 强制守卫排在还原**之前**，它的职责正是筛查模型自己造出来的值：
		// 那时候还没有任何东西被还原，豁免机制对它没有意义，给它反而会
		// 让「与历史轮次某个调用方值重合」的模型输出溜过去。
		return
	}
	it.restoredValues = fn
}

// withRestoredValues 把已还原值集合挂到 ctx 上，供这一次响应内的每一次
// checker.Check 读取。解析失败**不阻断响应**：豁免是保护措施，不是闸门，
// 拿不到集合时退回到旧行为（遮蔽照做），而不是把流量打挂。
func (it *OutputComplianceInterceptor) withRestoredValues(ctx context.Context, sessionID, tenantID string) context.Context {
	if it == nil || it.restoredValues == nil || sessionID == "" {
		return ctx
	}
	values, err := it.restoredValues(ctx, sessionID, tenantID)
	if err != nil {
		slog.Warn("output_compliance_interceptor: restored-value lookup failed; masking as before",
			"error", err, "session_id", sessionID)
		return ctx
	}
	return outputcompliance.WithRestoredValues(ctx, outputcompliance.NewRestoredValues(values))
}

// NewMandatoryOutputComplianceInterceptor reuses the protocol walker and
// bounded stream staging for a required, pre-restoration sensitive-output gate.
// It has independent stream state and cannot inherit an owner exemption or
// the optional output_compliance.enabled setting.
func NewMandatoryOutputComplianceInterceptor(checker interface {
	Check(context.Context, string, string) (*outputcompliance.ComplianceResult, error)
}, stateKey string) *OutputComplianceInterceptor {
	if stateKey == "" {
		stateKey = "mandatory_output.pending"
	}
	return &OutputComplianceInterceptor{checker: checker, mandatory: true, stateKey: stateKey}
}

func (it *OutputComplianceInterceptor) isEnabled() bool {
	return settings.GetPlatformBool("sanitize.output_check_enabled", true) && (it.mandatory || enabled())
}

func (it *OutputComplianceInterceptor) FailClosed() bool { return true }

// NewOutputComplianceInterceptor 构造拦截器。checker 必须非 nil；ownerFn 可为 nil。
func NewOutputComplianceInterceptor(checker interface {
	Check(context.Context, string, string) (*outputcompliance.ComplianceResult, error)
}, ownerFn OwnerContextFunc) *OutputComplianceInterceptor {
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
//
// 默认关闭：与 ModuleSpecs / write-time redactor 一致。未初始化、未注册、
// 未写入具体值时都不跑可选输出合规；只有显式 true 才启用。强制外传守卫
// （mandatory OutputSensitive）不走本函数。
func enabled() bool {
	return settings.GetPlatformBool("output_compliance.enabled", false)
}

// InterceptNonStream 处理非流式响应：检测 + owner 规则脱敏。
func (it *OutputComplianceInterceptor) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if it == nil || it.checker == nil || req == nil || len(req.ResponseBody) == 0 {
		return nil, nil
	}
	if !it.isEnabled() {
		return nil, nil
	}
	// R25-E: request-scoped policy cache so a multi-field body loads the
	// tenant policy once (the stream path already wraps its check context).
	ctx = outputcompliance.WithRequestPolicyCache(ctx)
	// 2026-10-10: 本次响应里被还原器注入的调用方原值不得被遮蔽销毁。
	ctx = it.withRestoredValues(ctx, req.SessionID, req.TenantID)
	return it.processBody(ctx, req)
}

// InterceptStreamChunk 流式 chunk 级检测：跨帧合并直到终态/finish 由
// stream_compliance.go 的 processStreamChunk 实现；这里委托过去，启用与禁用
// 规则按 OutputComplianceInterceptor.shouldEnable()。
func (it *OutputComplianceInterceptor) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return it.processStreamChunk(ctx, chunk, meta)
}

// InterceptStreamEnd 流结束：body 已重组为非流式形态，复用非流式逻辑。
func (it *OutputComplianceInterceptor) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	if it == nil || it.mandatory || it.checker == nil || meta == nil || len(meta.ResponseBody) == 0 {
		return nil, nil
	}
	if !it.isEnabled() {
		return nil, nil
	}
	req := &response.InterceptRequest{
		SessionID:      meta.SessionID,
		RequestID:      meta.RequestID,
		TenantID:       meta.TenantID,
		CallerOwner:    meta.CallerOwner,
		ClientProtocol: meta.ClientProtocol,
		ClientModel:    meta.ClientModel,
		ResponseBody:   meta.ResponseBody,
		TokensUsed:     meta.TokensUsed,
		ContextWindow:  meta.ContextWindow,
		MessageCount:   meta.MessageCount,
		FinishReason:   meta.FinishReason,
	}
	// R25-E: request-scoped policy cache, same as InterceptNonStream.
	ctx = outputcompliance.WithRequestPolicyCache(ctx)
	ctx = it.withRestoredValues(ctx, meta.SessionID, meta.TenantID)
	res, err := it.processBody(ctx, req)
	if err != nil || res == nil {
		return nil, err
	}
	// EndResult 不带 ModifiedBody（stream 已发送），仅用 Metadata 传递脱敏
	//标记。R25-C 语义钉：owner 匹配跳过脱敏时，EndResult 必须不带任何
	// metadata——processBody 的 observe 分支（issue_count>0 但未改写）只是
	// 观测信号，不是脱敏事实；把 observe metadata 透传到 stream end 会让
	// 下游（cache_update_hook 等）把「已检查未脱敏」误读成「发生了脱敏」。
	if res.Metadata != nil {
		if res.Action == "output_compliance_redact" {
			end := &response.EndResult{}
			end.Metadata = res.Metadata
			return end, nil
		}
	}
	return &response.EndResult{}, nil
}

// policyIssueDetail 取第一条带原因码/位置的问题。
//
// r1039（D3）：拦截要让客户端知道「哪条规则、哪个参数」——只有规则名不够：
// 调用方（agent）要能据此提示用户「这次调用被拦了，要不要继续执行」。
//
// Location 语义随来源不同：tool 参数是 JSON 路径（$.command），纯文本是
// 字符区间（char:120-145）。两者都是「出问题的地方」，都能直接回给用户。
func policyIssueDetail(issues []outputcompliance.ComplianceIssue) (subtype, location string) {
	for _, issue := range issues {
		if issue.Subtype == "" && issue.Location == "" {
			continue
		}
		return issue.Subtype, issue.Location
	}
	return "", ""
}

// policyBlockReason 产出人类可读的拦截原因短语。
//
// owner 口径（2026-10-20）：「就算是被拦截，应该在中断时反馈给客户因为什么
// 规则被拦截，不要搞空响应」——客户端要能拿这句话去提示用户。
func policyBlockReason(ruleID, subtype, field string) string {
	phrase := ""
	switch subtype {
	case "credential":
		phrase = "a credential value was found in a protected field"
	case "egress_destination":
		phrase = "the tool call appears to send data to an external destination"
	default:
		phrase = "the response violated an output policy rule"
	}
	if ruleID != "" {
		phrase += " (rule " + ruleID + ")"
	}
	if field != "" {
		phrase += " at " + field
	}
	return phrase
}

// policyBlockMetadata 组装拦截元数据：规则名 + 人类可读原因 + 出问题的位置。
//
// 键名与流式侧一致（policy_rule，见 policyRuleFromChunkResult），写者据此
// 渲染协议终帧；非流式侧据此渲染 403 响应体。
func policyBlockMetadata(rule, subtype, field string) map[string]interface{} {
	meta := map[string]interface{}{}
	if rule != "" {
		meta["policy_rule"] = rule
	}
	if field != "" {
		meta["policy_field"] = field
	}
	if reason := policyBlockReason(rule, subtype, field); reason != "" {
		meta["policy_reason"] = reason
	}
	return meta
}

// processBody 是非流式/流结束共用的核心逻辑。
//
// F02 (V3): the body is decomposed into client-visible text lanes
// (transformVisibleJSON / collectVisibleText in protocol_text.go) and each
// lane is checked and redacted independently. Checking the raw JSON string
// could both miss escaped text and mistake ids/usage for model output.
func (it *OutputComplianceInterceptor) processBody(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	dataOwner := ""
	if it.ownerFn != nil {
		dataOwner = it.ownerFn(ctx, req.SessionID, req.TenantID)
	}
	shouldRedact := it.mandatory || outputcompliance.ShouldRedact(redactionMode(), req.CallerOwner, dataOwner)

	// r1039（D3）：拦截原因（规则名 / 原因码 / 出问题的位置）在**拦截那一刻**
	// 记下来。原来的非流式 403 只有一句 message，客户端既说不出因为什么规则
	// 被拦，也没法据此提示用户。
	var blockRule, blockSubtype, blockField string

	transform := func(label, original string) (string, int, bool, error) {
		result, err := it.checkText(ctx, req.TenantID, label, original)
		if err != nil {
			// Checker failure fails closed: the whole response is withheld
			// instead of degrading to an unchecked passthrough.
			return "", 0, false, err
		}
		if result == nil {
			return "", 0, false, errors.New("output compliance checker returned nil result")
		}
		if result.Blocked || shouldRedact && result.ToolShapeRedactionRequired {
			if rule := policyRuleFromIssues(result.Issues); rule != "" {
				blockRule = rule
			}
			if subtype, field := policyIssueDetail(result.Issues); subtype != "" || field != "" {
				blockSubtype, blockField = subtype, field
			}
			return "", 0, true, nil
		}
		if !shouldRedact || result.RedactedOutput == "" || result.RedactedOutput == original {
			return original, len(result.Issues), false, nil
		}
		return result.RedactedOutput, len(result.Issues), false, nil
	}

	redactedBody, issues, blocked, err := transformVisibleJSON(req.ResponseBody, false, it.mandatory, transform)
	if err != nil {
		// Malformed response JSON or an unavailable checker must never reach
		// the client unchecked.
		slog.Warn("output_compliance_interceptor: body transform failed, blocking",
			"error", err, "session_id", req.SessionID)
		metrics.OutputGateFailClosedErrorsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateSourceTransformError).Inc()
		metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionBlock, metrics.OutputGatePathBody).Inc()
		return &response.InterceptResult{ShouldBlock: true, Action: "output_compliance_block"}, nil
	}
	if blocked {
		metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionBlock, metrics.OutputGatePathBody).Inc()
		return &response.InterceptResult{
			ShouldBlock: true,
			Action:      "output_compliance_block",
			Metadata:    policyBlockMetadata(blockRule, blockSubtype, blockField),
		}, nil
	}

	if !bytes.Equal(redactedBody, req.ResponseBody) {
		metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionRedact, metrics.OutputGatePathBody).Inc()
		mode := redactionMode()
		out := &response.InterceptResult{
			ModifiedBody: redactedBody,
			Action:       "output_compliance_redact",
			Metadata: map[string]interface{}{
				"output_compliance_redacted": true,
				"pii_stripped":               true, // 点亮 cache_update_hook 的悬空契约
				"pii_stripped_turn":          true, // Per-turn 脱敏标记（增强 3, 2026-07-09）
				"issue_count":                issues,
				"redaction_mode":             string(mode),
			},
		}
		return out, nil
	}
	if issues > 0 {
		metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionObserve, metrics.OutputGatePathBody).Inc()
		mode := redactionMode()
		return &response.InterceptResult{
			Action: "output_compliance_observe",
			Metadata: map[string]interface{}{
				"issue_count":    issues,
				"redaction_mode": string(mode),
			},
		}, nil
	}
	metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionAllow, metrics.OutputGatePathBody).Inc()
	return &response.InterceptResult{}, nil
}

func (it *OutputComplianceInterceptor) checkText(ctx context.Context, tenant, label, text string) (*outputcompliance.ComplianceResult, error) {
	if checker, ok := it.checker.(interface {
		CheckField(context.Context, string, string, string) (*outputcompliance.ComplianceResult, error)
	}); ok {
		return checker.CheckField(ctx, tenant, label, text)
	}
	return it.checker.Check(ctx, tenant, text)
}

// rewriteAssistantContent 把"完整响应 JSON 中 assistant 文本字段"替换为脱敏后的
// 纯文本。redactedContent 是 Checker 对提取出的 assistant 文本脱敏后的结果。
//
// 支持三种协议形态：
//   - OpenAI Chat Completions：choices[].message.content（字符串）
//   - Anthropic Messages：       type="assistant" 时 content[].text（[]ContentBlock 第一个 text）
//   - OpenAI Responses：           output[].content[].output_text.text
//
// 实现策略：解析 JSON → 定位第一个 assistant 文本字段 → 用 redactedContent
// 替换 → 重新序列化。保留其它字段（usage/finish_reason 等）不变。
func rewriteAssistantContent(body []byte, redactedContent string) ([]byte, bool) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}

	replaceInBlock := func(block map[string]any) bool {
		// Anthropic Messages: {"type":"message","role":"assistant","content":[{"type":"text","text":...}]}
		// OpenAI Responses:        {"type":"message","content":[{"type":"output_text","text":...}]}
		if content, ok := block["content"].([]any); ok {
			for _, part := range content {
				pm, ok := part.(map[string]any)
				if !ok {
					continue
				}
				pt, _ := pm["type"].(string)
				if pt == "text" || pt == "output_text" {
					pm["text"] = redactedContent
					return true
				}
			}
		}
		// OpenAI Chat Completions: {"choices":[{"message":{"role":"assistant","content":...}}]}
		if msg, ok := block["message"].(map[string]any); ok {
			if role, _ := msg["role"].(string); role != "assistant" && role != "" {
				return false
			}
			if _, exists := msg["content"]; exists {
				msg["content"] = redactedContent
				return true
			}
		}
		return false
	}

	// OpenAI Chat Completions
	if choices, ok := raw["choices"].([]any); ok {
		for _, c := range choices {
			if cm, ok := c.(map[string]any); ok {
				if replaceInBlock(cm) {
					break
				}
			}
		}
	}
	// Anthropic Messages (top-level) or Responses output[*]
	if _, ok := raw["choices"]; !ok {
		// Anthropic: top-level content array
		if _, ok := raw["content"]; ok {
			if replaceInBlock(raw) {
				goto done
			}
		}
		// Responses: output[*].content[*].output_text
		if output, ok := raw["output"].([]any); ok {
			for _, item := range output {
				if im, ok := item.(map[string]any); ok {
					replaceInBlock(im)
				}
			}
		}
	}
done:
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	// Did anything change? Compare against original.
	return out, !bytes.Equal(out, body)
}
