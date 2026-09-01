// system_prompt_prefix.go 提供「会话系统提示词前缀」的提取能力。
//
// 背景（2026-08-26）：会话总结 LLM 的 prompt 此前只包含对话消息，无法
// 识别请求来自哪类智能体（Cursor / ZCode / opencode / ...）与专家类型
// （software_engineering / security / ...）——这两类信息几乎都写在客户端
// 注入的 system prompt 开头。本文件把「会话首条请求体中的系统提示词前
// SystemPromptPrefixBytes 字节」提取出来，作为总结 LLM 的额外输入，
// 同时也作为规则兜底（telemetry.DetectAgentFromSystemPrompt /
// sessionmeta.DetectExpertFromSystemPrompt）的输入。
//
// 协议覆盖（与 domains/analysis/sessionmeta.ParseMessages 同构，但只关注
// system 部分，且对单条超大 system 做字节截断而不是整段读取）：
//   - OpenAI Chat:      messages[..].role == "system"（取首个）
//   - Anthropic:        顶层 system（string 或 text block 数组）
//   - OpenAI Responses: instructions（string）
//
// 安全：返回前统一走 secretmask.MaskSecrets，防止粘贴进 prompt 的 key 被
// 二次转发给总结 LLM。
package sessionsummary

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/secretmask"
)

// SystemPromptPrefixBytes 是系统提示词节选的字节上限。4KB 足够覆盖主流
// coding agent 的身份自述段（Cursor/ZCode/opencode 的身份行都在最前面），
// 同时不会显著放大总结 LLM 的 token 消耗。
const SystemPromptPrefixBytes = 4096

// SystemPromptSource 由 MessageSource 实现（可选能力）。summarizer 通过
// 类型断言启用：实现了就取，取不到就静默降级（与本次改动前的行为一致）。
//
// 返回空字符串 + nil error 表示「会话没有系统提示词」；error 仅用于
// 真实查询失败。实现必须 nil-safe（nil pool 返回 error 而非 panic）。
type SystemPromptSource interface {
	GetSystemPromptPrefix(ctx context.Context, tenantID, sessionKey string) (string, error)
}

// systemPromptPrefix 是 Summarizer 的内部 helper：从 messageSource 提取
// 系统提示词前缀。任何失败 / 未实现都降级为 ""，不阻断总结流程。
func (s *Summarizer) systemPromptPrefix(ctx context.Context, tenantID, sessionKey string) string {
	src, ok := s.messageSource.(SystemPromptSource)
	if !ok || src == nil {
		return ""
	}
	prefix, err := src.GetSystemPromptPrefix(ctx, tenantID, sessionKey)
	if err != nil {
		return ""
	}
	return prefix
}

// systemPromptFromRequestBody 从原始请求体 JSON 中提取系统提示词前缀。
// 兼容 OpenAI Chat / Anthropic Messages / OpenAI Responses 三种协议。
// raw 无效或不含系统提示词时返回 ""。
func systemPromptFromRequestBody(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}

	// Anthropic: 顶层 system（string 或 text blocks）
	if sys, ok := body["system"]; ok {
		if text := anthropicSystemText(sys); text != "" {
			return prefixBytesSecretMasked(text)
		}
	}

	// OpenAI Responses: instructions
	if inst, ok := body["instructions"]; ok {
		var text string
		if err := json.Unmarshal(inst, &text); err == nil && strings.TrimSpace(text) != "" {
			return prefixBytesSecretMasked(text)
		}
	}

	// OpenAI Chat: messages 数组中首条 role=system
	if msgs, ok := body["messages"]; ok {
		return systemFromChatMessages(msgs)
	}
	return ""
}

func anthropicSystemText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			sb.WriteString(b.Text)
			sb.WriteString(" ")
		}
	}
	return sb.String()
}

func systemFromChatMessages(raw json.RawMessage) string {
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return ""
	}
	for _, m := range msgs {
		if strings.ToLower(strings.TrimSpace(m.Role)) != "system" {
			continue
		}
		var text string
		if err := json.Unmarshal(m.Content, &text); err == nil {
			return prefixBytesSecretMasked(text)
		}
		// content 也可能为 text block 数组
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &blocks); err == nil {
			var sb strings.Builder
			for _, b := range blocks {
				if b.Type == "text" || b.Type == "" {
					sb.WriteString(b.Text)
					sb.WriteString(" ")
				}
			}
			return prefixBytesSecretMasked(sb.String())
		}
		return ""
	}
	return ""
}

// prefixBytesSecretMasked 在 rune 边界安全截断 + 脱敏后返回。
func prefixBytesSecretMasked(text string) string {
	if len(text) > SystemPromptPrefixBytes {
		cut := SystemPromptPrefixBytes
		// 向前回退到 rune 边界
		for cut > 0 && (text[cut]&0xC0) == 0x80 {
			cut--
		}
		text = text[:cut]
	}
	return secretmask.MaskSecrets(text)
}

// --- V1 实现：request_logs + request_logs_bodies ---

// GetSystemPromptPrefix 从该会话首个请求的请求体中提取系统提示词前缀。
func (m *pgRequestLogsSource) GetSystemPromptPrefix(ctx context.Context, tenantID, sessionKey string) (string, error) {
	if m.pool == nil {
		return "", fmt.Errorf("sessionsummary: store pool is nil")
	}
	var raw []byte
	// 2026-08-26: 读 _with_current_month 视图（hot ∪ parent）—— fresh 请求体
	// 先落 request_logs_bodies_hot，直接读父表会错过（详见 summarizer.go
	// getSessionMessagesQuery 的同期注释）。
	err := m.pool.QueryRow(ctx, `
		SELECT rb.request_body::text
		FROM request_logs_with_current_month rl
		JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.tenant_id = $1 AND rl.gw_session_id = $2
		ORDER BY rl.ts ASC
		LIMIT 1
	`, tenantID, sessionKey).Scan(&raw)
	if err != nil {
		return "", err
	}
	return systemPromptFromRequestBody(raw), nil
}

// --- V2 实现：session_bodies_unified（首个 turn 的 request_delta） ---

// GetSystemPromptPrefix 从该会话首个 turn 的 request_delta 中提取系统提示
// 词前缀（V2 增量存储里，系统提示词只在首个 turn 的 delta 中出现一次）。
func (m *v2SessionBodiesSource) GetSystemPromptPrefix(ctx context.Context, tenantID, sessionKey string) (string, error) {
	if m.pool == nil {
		return "", fmt.Errorf("sessionsummary: v2 message source pool is nil")
	}
	var raw []byte
	// 2026-09-01: 读 session_bodies_unified 视图（hot ∪ partition）——直接读
	// public.session_bodies 父表会漏最近 8 小时仍在 hot 表的行，以及 promote
	// 当天落进当日分区的行（迁移 625/637 的视图注释明确要求 admin 读方用该
	// 视图）。与 message_source_v2.go 的同期修正保持一致。
	err := m.pool.QueryRow(ctx, `
		SELECT b.request_delta
		FROM public.session_bodies_unified b
		WHERE b.session_id = $1 AND b.tenant_id = $2
		ORDER BY b.ts ASC
		LIMIT 1
	`, sessionKey, tenantID).Scan(&raw)
	if err != nil {
		return "", err
	}
	// request_delta 是消息数组
	var msgs []sessionMessageV2
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return "", fmt.Errorf("sessionsummary: v2 request_delta decode failed: %w", err)
	}
	for _, m2 := range msgs {
		if strings.ToLower(strings.TrimSpace(m2.Role)) == "system" && strings.TrimSpace(m2.Content) != "" {
			return prefixBytesSecretMasked(m2.Content), nil
		}
	}
	return "", nil
}
