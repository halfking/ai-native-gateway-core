package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// F7 (2026-11-10 · 属主裁决「恢复 Anthropic 腿的 reasoning trace 转发」).
//
// 这组用例钉住 Anthropic 腿的 reasoning trace 转发契约：
//
//	① 非流式：OpenAI 系上游的 `message.reasoning_content` 必须以 Anthropic
//	   的 `thinking` 内容块转发给客户端（此前整条 trace 被丢弃）。
//	② 非流式：IR 里已经有 "thinking" 块时不得重复发第二块（那块带签名）。
//	③ 非流式：Anthropic→Anthropic 原样透传必须逐字不变（不得多出第二块）。
//	④ 流式：上游 OpenAI 流的 `delta.reasoning_content` 必须以 thinking_delta
//	   到达 Anthropic 客户端。
//	⑤ OpenAI 腿**不得**被顺带改动（它的 `reasoning_content` 逐字不变）。
//
// 线形态（非流式，thinking 块排在内容数组最前）：
//
//	{"type":"thinking","thinking":"<trace>"}
//
// 无签名时**不写** signature 字段（IR 把签名挂在内容块上，不在
// ReasoningContent 上；写空签名会被 Anthropic API 拒）。

// anthropicTraceBlocks is the decoded Anthropic Messages content array.
type anthropicTraceBlocks []struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	Text      string `json:"text"`
}

func decodeAnthropicTraceBlocks(t *testing.T, body []byte) anthropicTraceBlocks {
	t.Helper()
	var payload struct {
		Content anthropicTraceBlocks `json:"content"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal anthropic body: %v\nbody: %s", err, body)
	}
	return payload.Content
}

// ① 非流式：上游 `reasoning_content` → Anthropic 的 thinking 块。
func TestAnthropicReasoningTrace_NonStreamingForwardsReasoning(t *testing.T) {
	upstream := []byte(`{
		"id":"chatcmpl-f7",
		"model":"deepseek-reasoner",
		"choices":[{
			"message":{"role":"assistant","content":"42","reasoning_content":"step one\nstep two"},
			"finish_reason":"stop"
		}]
	}`)

	ir, err := ParseOpenAIResponse(upstream)
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}
	if ir.ReasoningContent == "" {
		t.Fatal("ParseOpenAIResponse dropped reasoning_content — the IR is the precondition of this whole contract")
	}

	body, err := SerializeAnthropicResponse(ir, "claude-test")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}

	blocks := decodeAnthropicTraceBlocks(t, body)
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2 (thinking + text): %s", len(blocks), body)
	}
	if blocks[0].Type != "thinking" {
		t.Fatalf("block[0].type = %q, want thinking (thinking must precede text): %s", blocks[0].Type, body)
	}
	if blocks[0].Thinking != "step one\nstep two" {
		t.Errorf("block[0].thinking = %q, want the upstream trace verbatim", blocks[0].Thinking)
	}
	if blocks[0].Signature != "" {
		t.Errorf("block[0].signature = %q, want empty (IR carries none ⇒ field must be omitted)", blocks[0].Signature)
	}
	if strings.Contains(string(body), `"signature"`) {
		t.Errorf("body carries a signature field although the IR has none: %s", body)
	}
	if blocks[1].Type != "text" || blocks[1].Text != "42" {
		t.Errorf("block[1] = %+v, want the text block with the answer", blocks[1])
	}
}

// ①b 非流式：只有 reasoning、没有正文时也要有 thinking 块。
func TestAnthropicReasoningTrace_NonStreamingReasoningOnly(t *testing.T) {
	ir := &InternalResponse{
		ID:               "msg-f7",
		Model:            "deepseek-reasoner",
		Role:             "assistant",
		ReasoningContent: "thinking out loud",
		FinishReason:     "stop",
		SourceProtocol:   ProtocolOpenAIChat,
	}

	body, err := SerializeAnthropicResponse(ir, "claude-test")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}

	blocks := decodeAnthropicTraceBlocks(t, body)
	if len(blocks) != 1 || blocks[0].Type != "thinking" {
		t.Fatalf("content = %+v, want exactly one thinking block: %s", blocks, body)
	}
	if blocks[0].Thinking != "thinking out loud" {
		t.Errorf("block[0].thinking = %q, want the trace", blocks[0].Thinking)
	}
}

// ② 非流式：IR 里已有 thinking 块 ⇒ 不得重复发第二块（那块带签名）。
func TestAnthropicReasoningTrace_NonStreamingDoesNotDuplicateThinkingBlock(t *testing.T) {
	ir := &InternalResponse{
		ID:    "msg-f7-dup",
		Model: "claude-test",
		Role:  "assistant",
		// ParseAnthropicResponse aggregates the block text into
		// ReasoningContent AND keeps the block (with its signature) in
		// Content ⇒ both surfaces carry the same trace.
		ReasoningContent: "verified thinking",
		Content: []ResponseContentBlock{
			{Type: "thinking", Thinking: "verified thinking", Signature: "sig_abc"},
			{Type: "text", Text: "answer"},
		},
	}

	body, err := SerializeAnthropicResponse(ir, "claude-test")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}

	blocks := decodeAnthropicTraceBlocks(t, body)
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2 (one thinking + one text): %s", len(blocks), body)
	}
	if blocks[0].Type != "thinking" {
		t.Fatalf("block[0].type = %q, want thinking", blocks[0].Type)
	}
	if blocks[0].Signature != "sig_abc" {
		t.Errorf("block[0].signature = %q, want the block's own signature", blocks[0].Signature)
	}
	if blocks[0].Thinking != "verified thinking" {
		t.Errorf("block[0].thinking = %q, want the block's text", blocks[0].Thinking)
	}
	if strings.Count(string(body), `"type":"thinking"`) != 1 {
		t.Errorf("thinking block emitted %d times, want exactly 1: %s",
			strings.Count(string(body), `"type":"thinking"`), body)
	}
}

// ③ 非流式：Anthropic→Anthropic 原样透传必须逐字不变。
func TestAnthropicReasoningTrace_NonStreamingAnthropicPassthroughUnchanged(t *testing.T) {
	upstream := []byte(`{
		"id":"msg_f7_native",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-4-5",
		"content":[
			{"type":"thinking","thinking":"native trace","signature":"sig_native"},
			{"type":"text","text":"native answer"}
		],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":3,"output_tokens":4}
	}`)

	ir, err := ParseAnthropicResponse(upstream)
	if err != nil {
		t.Fatalf("ParseAnthropicResponse: %v", err)
	}
	if ir.ReasoningContent == "" {
		t.Fatal("ParseAnthropicResponse dropped the thinking text")
	}

	body, err := SerializeAnthropicResponse(ir, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}

	blocks := decodeAnthropicTraceBlocks(t, body)
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2 (passthrough must not duplicate): %s", len(blocks), body)
	}
	if blocks[0].Type != "thinking" || blocks[0].Signature != "sig_native" {
		t.Errorf("block[0] = %+v, want the signed thinking block", blocks[0])
	}
	if blocks[1].Type != "text" || blocks[1].Text != "native answer" {
		t.Errorf("block[1] = %+v, want the text block", blocks[1])
	}
}

// ④ 流式：上游 OpenAI 流的 `delta.reasoning_content` 必须到达 Anthropic 客户端。
func TestAnthropicReasoningTrace_StreamingForwardsReasoning(t *testing.T) {
	chunk, err := ParseOpenAIStreamChunk(`data: {"id":"s1","choices":[{"delta":{"reasoning_content":"step one "}}]}`)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk: %v", err)
	}
	if chunk.Delta == nil || chunk.Delta.ReasoningContent != "step one " {
		t.Fatalf("Delta.ReasoningContent = %+v, want the upstream trace", chunk.Delta)
	}

	out := chunk.SerializeAnthropic("msg_f7", "claude-test")
	if !strings.Contains(out, `"thinking_delta"`) {
		t.Errorf("anthropic stream lost the reasoning delta: %s", out)
	}
	if !strings.Contains(out, `"thinking":"step one "`) {
		t.Errorf("anthropic stream lost the reasoning text: %s", out)
	}
}

// ④b 流式：Ollama /v1 方言的 `reasoning` 别名同样要到达 Anthropic 客户端
// （IR 的 alias 归一化覆盖它 ⇒ 流式桥不得把它丢掉）。
func TestAnthropicReasoningTrace_StreamingForwardsReasoningAlias(t *testing.T) {
	chunk, err := ParseOpenAIStreamChunk(`data: {"id":"s2","choices":[{"delta":{"reasoning":"alias trace"}}]}`)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk: %v", err)
	}
	if chunk.Delta == nil || chunk.Delta.ReasoningContent != "alias trace" {
		t.Fatalf("Delta.ReasoningContent = %+v, want the aliased trace", chunk.Delta)
	}

	out := chunk.SerializeAnthropic("msg_f7", "claude-test")
	if !strings.Contains(out, `"thinking_delta"`) || !strings.Contains(out, `"thinking":"alias trace"`) {
		t.Errorf("anthropic stream lost the aliased reasoning trace: %s", out)
	}
}

// ⑤ OpenAI 腿**不得**被这次改动顺带碰过：
//   · 非流式：`choices[].message.reasoning_content` 逐字不变；
//   · 流式：`choices[].delta.reasoning_content` 逐字不变。
func TestAnthropicReasoningTrace_OpenAILegUnchanged(t *testing.T) {
	ir, err := ParseOpenAIResponse([]byte(`{
		"id":"chatcmpl-openai-leg",
		"model":"deepseek-reasoner",
		"choices":[{
			"message":{"role":"assistant","content":"42","reasoning_content":"openai trace"},
			"finish_reason":"stop"
		}]
	}`))
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}

	body, err := SerializeOpenAIResponse(ir, "deepseek-reasoner")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning_content":"openai trace"`) {
		t.Errorf("openai leg lost its reasoning_content: %s", body)
	}
	if !strings.Contains(string(body), `"content":"42"`) {
		t.Errorf("openai leg lost its content: %s", body)
	}

	chunk, err := ParseOpenAIStreamChunk(`data: {"id":"s3","choices":[{"delta":{"content":"42","reasoning_content":"openai stream trace"}}]}`)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk: %v", err)
	}
	out := chunk.SerializeOpenAI("chatcmpl-openai-leg", "deepseek-reasoner", 1700000000)
	if !strings.Contains(out, `"reasoning_content":"openai stream trace"`) {
		t.Errorf("openai stream leg lost its reasoning_content: %s", out)
	}
	if !strings.Contains(out, `"content":"42"`) {
		t.Errorf("openai stream leg lost its content: %s", out)
	}
}
