package streaming

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// RedactOwnerContextFunc 返回一次请求的 (callerOwner, dataOwner)。
// 定义在 streaming 包，避免与 outputcompliance/interceptor 循环依赖。
type RedactOwnerContextFunc func(sessionID, tenantID string) (callerOwner, dataOwner string)

// BuildRedactBodyFn 构造 write-time 脱敏函数，在 w.Write 前对客户端可见字节脱敏。
//
// 设计：
//   - 复用 outputcompliance.Checker 的检测 + redactOutput 逻辑。
//   - 与 OutputComplianceInterceptor 互补：前者改客户端字节（write-time），
//     后者改 telemetry/日志/缓存（post-response）。两者都会运行（幂等）。
//   - 非流式：在 executor.WriteNonStreamResponse 的 w.Write 前调用。
//   - 流式：在 stripChunkFields 内对每个完整 JSON chunk 调用（跨边界 PII 不处理）。
//
// 参数：
//   - checker: 必须非 nil，用于检测和脱敏。
//   - ownerFn: 可为 nil，为 nil 时 caller/data owner 均视为空（保守脱敏）。
//   - restoredValues: 可为 nil。提供「本次会话已还原值」的来源（2026-10-10）。
//     为 nil 时行为与旧版一致。
//
// 返回：
//   - func(body []byte, sessionID, tenantID string) []byte
//     输入完整 OpenAI response JSON，返回脱敏后 JSON。失败时返回原 body（降级保守）。
//
// 为什么 write-time 路径也需要豁免（2026-10-10）
//
// 这条路径是**最后一米**：它直接改 w.Write 之前的客户端可见字节，跑在占位符
// 还原**之后**。因此它看到的文本里同样混着模型写的值和网关刚换回来的调用方
// 原值，而这里用的是 context.Background()（注释里写明「write-time 无请求
// 上下文」），豁免集合根本到不了检查器。
//
// 结果是：拦截器已经放行的调用方原值，在这里仍会被压成 `[REDACTED:...]` 或
// 星号串。两条路径幂等互补不等于其中一条可以破坏另一条的判断——这条离客户端
// 最近，破坏发生在这里就无从挽回。
func BuildRedactBodyFn(
	checker *outputcompliance.Checker,
	ownerFn RedactOwnerContextFunc,
	restoredValues func(context.Context, string, string) ([]string, error),
) func([]byte, string, string) []byte {
	fn := BuildRedactBodyWithContextFn(checker, ownerFn, restoredValues)
	if fn == nil {
		return nil
	}
	return func(body []byte, sessionID, tenantID string) []byte {
		return fn(context.Background(), body, sessionID, tenantID)
	}
}

// BuildRedactBodyWithContextFn keeps the authenticated tenant and pinned policy
// snapshot from the HTTP request through the final non-streaming write.
func BuildRedactBodyWithContextFn(
	checker *outputcompliance.Checker,
	ownerFn RedactOwnerContextFunc,
	restoredValues func(context.Context, string, string) ([]string, error),
) func(context.Context, []byte, string, string) []byte {
	if checker == nil {
		return nil
	}
	return func(ctx context.Context, body []byte, sessionID, tenantID string) []byte {
		if len(body) == 0 {
			return body
		}

		// 1. 检查 output_compliance.enabled（与 interceptor 同逻辑）
		if !outputComplianceEnabled() {
			return body
		}

		// 2. 提取 choices[].message.content
		output, err := extractAssistantContent(body)
		if err != nil || output == "" {
			// 解析失败或无 content，降级不脱敏
			return body
		}

		// Preserve request values; legacy callers explicitly supply Background.
		if ctx == nil {
			ctx = context.Background()
		}
		if restoredValues != nil && sessionID != "" {
			// 解析失败退回旧行为（照遮），不阻断响应：豁免是保护措施不是闸门。
			if values, err := restoredValues(ctx, sessionID, tenantID); err == nil {
				ctx = outputcompliance.WithRestoredValues(ctx, outputcompliance.NewRestoredValues(values))
			} else {
				slog.Warn("redact_body: restored-value lookup failed; masking as before",
					"error", err, "session_id", sessionID)
			}
		}
		result, err := checker.Check(ctx, tenantID, output)
		if err != nil || result == nil {
			return body
		}
		// Preserve a block decision at the final non-streaming writer. The executor
		// recognizes this envelope and terminates with a policy failure audit.
		if result.Blocked {
			return []byte(`{"error":{"message":"Response blocked by output policy","type":"policy_violation","code":"output_policy_blocked"}}`)
		}
		if len(result.Issues) == 0 {
			return body
		}

		// 4. owner 规则判定
		mode := getRedactionMode()
		var callerOwner, dataOwner string
		if ownerFn != nil {
			callerOwner, dataOwner = ownerFn(sessionID, tenantID)
		}
		shouldRedact := outputcompliance.ShouldRedact(mode, callerOwner, dataOwner)

		if !shouldRedact {
			return body
		}

		// 5. 脱敏
		if result.RedactedOutput == "" || result.RedactedOutput == output {
			// 无需脱敏或脱敏结果与原文相同
			return body
		}

		// 6. 回填到 JSON
		redacted, ok := rewriteAssistantContentInJSON(body, result.RedactedOutput)
		if !ok {
			slog.Warn("redact_body: rewriteAssistantContent failed, returning original",
				"session_id", sessionID)
			return body
		}

		return redacted
	}
}

// extractAssistantContent 从 OpenAI response JSON 提取 choices[0].message.content。
func extractAssistantContent(body []byte) (string, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", nil
	}
	return resp.Choices[0].Message.Content, nil
}

// rewriteAssistantContentInJSON 把 redactedContent 回填到 OpenAI response JSON。
func rewriteAssistantContentInJSON(body []byte, redactedContent string) ([]byte, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}

	choicesRaw, ok := raw["choices"]
	if !ok {
		return nil, false
	}

	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(choicesRaw, &choices); err != nil || len(choices) == 0 {
		return nil, false
	}

	msgRaw, ok := choices[0]["message"]
	if !ok {
		return nil, false
	}

	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return nil, false
	}

	// 替换 content
	contentJSON, _ := json.Marshal(redactedContent)
	msg["content"] = contentJSON

	// 重新组装
	msgJSON, _ := json.Marshal(msg)
	choices[0]["message"] = msgJSON

	choicesJSON, _ := json.Marshal(choices)
	raw["choices"] = choicesJSON

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	return out, true
}

// outputComplianceEnabled 读取 output_compliance.enabled。
// 默认 false：与 ModuleSpecs / optional interceptor 一致；显式 true 才启用
// write-time 脱敏。强制外传守卫不走本开关。
func outputComplianceEnabled() bool {
	return settings.GetPlatformBool("sanitize.output_check_enabled", true) && settings.GetPlatformBool("output_compliance.enabled", false)
}

// getRedactionMode 读取 output_compliance.redaction_mode。SC-2：此前硬
// 编码 RedactOwnerMismatch；现在解析 settings 枚举
// （off/always/owner_mismatch），未知值回落 owner_mismatch（旧行为）。
func getRedactionMode() outputcompliance.RedactionMode {
	switch settings.GetPlatformString("output_compliance.redaction_mode", "owner_mismatch") {
	case "off":
		return outputcompliance.RedactOff
	case "always":
		return outputcompliance.RedactAlways
	default:
		return outputcompliance.RedactOwnerMismatch
	}
}
