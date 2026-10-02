package ir

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeminiRawPartReplayDualUpstreamWireShape —— 2026-10-01 24h审计第十七轮
// 移交 ③ 的实证钉测：Gemini 入口的 executableCode（代码执行 live part）
// 走 R72 raw 保留进 IR 后，回注 OpenAI / Anthropic / Gemini 三向出站的
// wire 形状。此前「回注风险」只有纸面推断（raw 块无 type 字段回注 OpenAI
// content 数组，严格上游 400），本钉测把它钉成可执行契约：
//
//   - OpenAI 出站：raw 对象**原样**回注 content 数组、无 "type" 字段 ——
//     这是已登记的产品风险（严格上游 400，比丢弃更糟的形态是形状未定）；
//     本断言钉住的是当前事实。若有人改此行为，必须有意识地更新本钉测并
//     在审计留痕（放宽 or 合法化二选一，禁止静默变更）。
//   - Anthropic 出站：raw 对象被补 `type:"raw"`（serialize_anthropic 的
//     default 分支注入 block.Type）——"raw" 不是 Anthropic 合法 type，
//     同族 400 风险，一并钉住。
//   - Gemini 出站（native 上游）：内容块 switch 无 raw 分支 → **静默丢弃**
//     （serialize_gemini.go 的 P3 已登记项「接 native 上游前必须补」）。
//     钉住丢弃事实，补分支时本断言红 → 强制显式裁决。
//
// 三向共同前提：raw part 不得在 IR 侧丢失（R72 raw 保留语义）。
func TestGeminiRawPartReplayDualUpstreamWireShape(t *testing.T) {
	body := `{"contents":[
		{"role":"user","parts":[{"text":"run the code"}]},
		{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]},
		{"role":"user","parts":[{"text":"what did it print?"}]}
	]}`

	irReq, err := ParseGemini([]byte(body))
	require.NoError(t, err)

	// ---- IR 保真：raw 块在位（回注前提） ----
	var rawBlock *ContentBlock
	for i := range irReq.Messages {
		if irReq.Messages[i].Role != "assistant" {
			continue
		}
		for j := range irReq.Messages[i].Content {
			if irReq.Messages[i].Content[j].Type == "raw" {
				rawBlock = &irReq.Messages[i].Content[j]
			}
		}
	}
	require.NotNil(t, rawBlock, "executableCode part must survive into IR as a raw block (R72 raw preservation)")
	assert.Contains(t, rawBlock.RawContent, "executableCode")

	// ---- OpenAI 出站：原样回注、无 type 字段（已登记 400 风险形态） ----
	openaiBody, err := SerializeOpenAI(irReq)
	require.NoError(t, err)
	var openaiWire map[string]any
	require.NoError(t, json.Unmarshal(openaiBody, &openaiWire))
	openaiModelParts := extractOpenAIModelContentParts(t, openaiWire)
	require.NotEmpty(t, openaiModelParts, "model message content parts must reach the OpenAI wire")
	var execPart map[string]any
	for _, p := range openaiModelParts {
		if m, ok := p.(map[string]any); ok {
			if _, has := m["executableCode"]; has {
				execPart = m
			}
		}
	}
	require.NotNil(t, execPart, "executableCode must be replayed to the OpenAI upstream (not dropped)")
	_, hasType := execPart["type"]
	assert.False(t, hasType, "raw replay block carries no type field on the OpenAI wire — the registered strict-upstream 400 risk; changing this requires explicit audit sign-off")
	assert.Equal(t, map[string]any{"language": "PYTHON", "code": "print(1)"}, execPart["executableCode"], "raw payload must round-trip verbatim")

	// ---- Anthropic 出站：补 type:"raw"（同族 400 风险形态） ----
	anthropicBody, err := SerializeAnthropic(irReq)
	require.NoError(t, err)
	var anthropicWire map[string]any
	require.NoError(t, json.Unmarshal(anthropicBody, &anthropicWire))
	anthModelBlock := extractAnthropicModelRawBlock(t, anthropicWire)
	require.NotNil(t, anthModelBlock, "raw block must reach the Anthropic wire (not dropped)")
	assert.Equal(t, "raw", anthModelBlock["type"], "serialize_anthropic default branch injects block.Type as the type field — 'raw' is not a legal Anthropic type (registered risk)")
	assert.Equal(t, map[string]any{"language": "PYTHON", "code": "print(1)"}, anthModelBlock["executableCode"], "raw payload must round-trip verbatim")

	// ---- Gemini 出站（native 上游）：静默丢弃（P3 已登记） ----
	geminiBody, err := SerializeGemini(irReq)
	require.NoError(t, err)
	var geminiWire map[string]any
	require.NoError(t, json.Unmarshal(geminiBody, &geminiWire))
	assert.False(t, strings.Contains(string(geminiBody), "executableCode"),
		"current serialize_gemini has no raw outbound branch: raw parts are silently dropped on the native path (P3 registered — wiring a native upstream requires adding the branch and flipping this pin deliberately)")
}

// extractOpenAIModelContentParts 取 OpenAI wire 中 assistant 消息的 content
// 数组元素（数组形态；字符串形态返回空由调用方 require 暴露）。
func extractOpenAIModelContentParts(t *testing.T, wire map[string]any) []any {
	t.Helper()
	msgs, _ := wire["messages"].([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		if parts, ok := msg["content"].([]any); ok {
			return parts
		}
	}
	return nil
}

// extractAnthropicModelRawBlock 取 Anthropic wire 中 assistant 消息的 raw 块。
func extractAnthropicModelRawBlock(t *testing.T, wire map[string]any) map[string]any {
	t.Helper()
	msgs, _ := wire["messages"].([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "raw" {
				return bm
			}
		}
	}
	return nil
}
