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
//
// 返回：
//   - func(body []byte, sessionID, tenantID string) []byte
//     输入完整 OpenAI response JSON，返回脱敏后 JSON。失败时返回原 body（降级保守）。
func BuildRedactBodyFn(checker *outputcompliance.Checker, ownerFn RedactOwnerContextFunc) func([]byte, string, string) []byte {
	if checker == nil {
		return nil
	}
	return func(body []byte, sessionID, tenantID string) []byte {
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

		// 3. 检测 PII/敏感（context.Background 因 write-time 无请求上下文）
		ctx := context.Background()
		result, err := checker.Check(ctx, tenantID, output)
		if err != nil || result == nil || len(result.Issues) == 0 {
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

// outputComplianceEnabled 读取 output_compliance.enabled。SC-2
// (docs/修订0811/19)：此前是硬编码 true 的占位实现，绕过了管理台的
// 模块开关；现在从 settings 读取（默认 true 保持旧行为）。
func outputComplianceEnabled() bool {
	return settings.GetPlatformBool("output_compliance.enabled", true)
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
