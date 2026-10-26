package ir

import (
	"encoding/json"
	"testing"
)

// 2026-10-27 缓存标准对齐轮的回归钉桩。判据全部来自原厂当日文档：
//   - Anthropic cache_control = {"type":"ephemeral","ttl":"5m"|"1h"}（ttl 省略
//     即 5m），IR 路径此前只透传 type，1h 检查点静默退化成 5m；
//   - OpenAI Responses usage 的缓存命中在 input_tokens_details.cached_tokens
//     （GPT-5.6 世代起同对象还带 cache_write_tokens），非流式解析与
//     SerializeResponsesResponse 此前都把它整段丢弃；
//   - Gemini usageMetadata.cachedContentTokenCount：流式解析已捕获，
//     非流式 ParseGeminiResponse 此前丢弃。

func TestAnthropicCacheControlTTLRoundTrip(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-5",
		"max_tokens": 128,
		"cache_control": {"type": "ephemeral", "ttl": "1h"},
		"system": [
			{"type": "text", "text": "sys", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "no ttl", "cache_control": {"type": "ephemeral"}},
				{"type": "text", "text": "one hour", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
			]}
		]
	}`)

	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}
	if len(req.CacheControl) != 1 || req.CacheControl[0].TTL != "1h" {
		t.Fatalf("request-level cache_control ttl not kept: %+v", req.CacheControl)
	}
	if len(req.System.Parts) != 1 || req.System.Parts[0].CacheControl == nil || req.System.Parts[0].CacheControl.TTL != "1h" {
		t.Fatalf("system part cache_control ttl not kept: %+v", req.System.Parts)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 2 {
		t.Fatalf("unexpected messages shape: %+v", req.Messages)
	}
	if cc := req.Messages[0].Content[0].CacheControl; cc == nil || cc.TTL != "" {
		t.Fatalf("block without ttl must keep empty TTL, got %+v", cc)
	}
	if cc := req.Messages[0].Content[1].CacheControl; cc == nil || cc.TTL != "1h" {
		t.Fatalf("block ttl not kept: %+v", cc)
	}

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal serialized body: %v", err)
	}

	top, ok := wire["cache_control"].(map[string]any)
	if !ok || top["ttl"] != "1h" {
		t.Fatalf("serialized top-level cache_control = %v, want ttl=1h", wire["cache_control"])
	}
	sys, ok := wire["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("serialized system = %v", wire["system"])
	}
	sysCC, ok := sys[0].(map[string]any)["cache_control"].(map[string]any)
	if !ok || sysCC["ttl"] != "1h" {
		t.Fatalf("serialized system cache_control = %v, want ttl=1h", sys[0].(map[string]any)["cache_control"])
	}
	msgs, ok := wire["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("serialized messages = %v", wire["messages"])
	}
	blocks, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("serialized content = %v", msgs[0].(map[string]any)["content"])
	}
	noTTL, ok := blocks[0].(map[string]any)["cache_control"].(map[string]any)
	if !ok || noTTL["ttl"] != nil {
		t.Fatalf("block without ttl must serialize without the ttl key, got %v", blocks[0].(map[string]any)["cache_control"])
	}
	withTTL, ok := blocks[1].(map[string]any)["cache_control"].(map[string]any)
	if !ok || withTTL["ttl"] != "1h" {
		t.Fatalf("serialized block cache_control = %v, want ttl=1h", blocks[1].(map[string]any)["cache_control"])
	}
}

func TestParseResponsesResponseReadsInputTokensDetails(t *testing.T) {
	body := []byte(`{
		"id": "resp_1",
		"object": "response",
		"created_at": 1,
		"model": "gpt-5.6",
		"status": "completed",
		"output": [
			{"type": "message", "role": "assistant",
			 "content": [{"type": "output_text", "text": "ok"}]}
		],
		"usage": {"input_tokens": 1000, "output_tokens": 50, "total_tokens": 1050,
			"input_tokens_details": {"cached_tokens": 768, "cache_write_tokens": 128}}
	}`)

	parsed, err := ParseResponsesResponse(body)
	if err != nil {
		t.Fatalf("ParseResponsesResponse: %v", err)
	}
	if parsed.Usage.CacheReadTokens == nil || *parsed.Usage.CacheReadTokens != 768 {
		t.Fatalf("CacheReadTokens = %v, want 768", parsed.Usage.CacheReadTokens)
	}
	if parsed.Usage.CacheWriteTokens == nil || *parsed.Usage.CacheWriteTokens != 128 {
		t.Fatalf("CacheWriteTokens = %v, want 128", parsed.Usage.CacheWriteTokens)
	}

	out, err := SerializeResponsesResponse(parsed, "gpt-5.6")
	if err != nil {
		t.Fatalf("SerializeResponsesResponse: %v", err)
	}
	var wire struct {
		Usage struct {
			InputTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal responses body: %v", err)
	}
	if wire.Usage.InputTokensDetails.CachedTokens != 768 || wire.Usage.InputTokensDetails.CacheWriteTokens != 128 {
		t.Fatalf("responses usage details = %+v, want cached=768 write=128", wire.Usage.InputTokensDetails)
	}

	// 同一份 IR 落回 chat 形状时，命中走 prompt_tokens_details。
	chatOut, err := SerializeOpenAIResponse(parsed, "gpt-5.6")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	var chatWire struct {
		Usage struct {
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(chatOut, &chatWire); err != nil {
		t.Fatalf("unmarshal chat body: %v", err)
	}
	if chatWire.Usage.PromptTokensDetails.CachedTokens != 768 {
		t.Fatalf("chat usage cached_tokens = %d, want 768", chatWire.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestSerializeResponsesResponseOmitsEmptyCacheDetails(t *testing.T) {
	// 无值不发键：没有缓存命中时不得出现 input_tokens_details。
	irResp := &InternalResponse{
		ID:    "resp_2",
		Model: "gpt-5.6",
		Role:  "assistant",
		Usage: ResponseUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		Content: []ResponseContentBlock{
			{Type: "text", Text: "ok"},
		},
	}
	out, err := SerializeResponsesResponse(irResp, "gpt-5.6")
	if err != nil {
		t.Fatalf("SerializeResponsesResponse: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	usage, ok := wire["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing: %v", wire["usage"])
	}
	if _, exists := usage["input_tokens_details"]; exists {
		t.Fatalf("input_tokens_details must be absent when no cache tokens, got %v", usage["input_tokens_details"])
	}
}

func TestParseGeminiResponseReadsCachedContentTokenCount(t *testing.T) {
	body := []byte(`{
		"candidates": [
			{"content": {"role": "model", "parts": [{"text": "hello"}]}, "finishReason": "STOP"}
		],
		"usageMetadata": {"promptTokenCount": 900, "candidatesTokenCount": 10,
			"totalTokenCount": 910, "cachedContentTokenCount": 640},
		"modelVersion": "gemini-2.5-flash"
	}`)

	parsed, err := ParseGeminiResponse(body)
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	if parsed.Usage.CacheReadTokens == nil || *parsed.Usage.CacheReadTokens != 640 {
		t.Fatalf("CacheReadTokens = %v, want 640", parsed.Usage.CacheReadTokens)
	}

	out, err := SerializeGeminiResponse(parsed, "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	var wire struct {
		UsageMetadata struct {
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal gemini body: %v", err)
	}
	if wire.UsageMetadata.CachedContentTokenCount != 640 {
		t.Fatalf("cachedContentTokenCount = %d, want 640", wire.UsageMetadata.CachedContentTokenCount)
	}

	// 字段缺席 ⇒ 不进 IR（nil），序列化侧也不发键。
	noCache := []byte(`{
		"candidates": [{"content": {"role": "model", "parts": [{"text": "hi"}]}, "finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 1, "totalTokenCount": 6},
		"modelVersion": "gemini-2.5-flash"
	}`)
	parsed2, err := ParseGeminiResponse(noCache)
	if err != nil {
		t.Fatalf("ParseGeminiResponse(no cache): %v", err)
	}
	if parsed2.Usage.CacheReadTokens != nil {
		t.Fatalf("CacheReadTokens must be nil when field absent, got %v", *parsed2.Usage.CacheReadTokens)
	}
}

// 2026-10-27 缓存标准对齐轮第二批：块级 cache_control（system 块/消息块/
// tool_result 子块）在跨协议串行器上都必须上报丢失，不得静默蒸发。
func TestBlockLevelCacheControlLossIsReported(t *testing.T) {
	t.Run("openai chat", func(t *testing.T) {
		cap := resetDedupAndInstall(t)
		req := &InternalRequest{
			Model:          "claude-sonnet-5",
			SourceProtocol: ProtocolAnthropicMessages,
			Messages: []Message{{Role: "user", Content: []ContentBlock{
				{Type: "text", Text: "hi", CacheControl: &CacheControl{Type: "ephemeral"}},
			}}},
		}
		if _, err := SerializeOpenAI(req); err != nil {
			t.Fatalf("SerializeOpenAI: %v", err)
		}
		if !cap.hasEvent(AnomalyEvent{
			AnomalyType:    AnomalyProtocolLoss,
			FieldPath:      "cache_control",
			TargetProtocol: ProtocolOpenAIChat,
		}) {
			t.Fatalf("block-level cache_control lost silently on chat bridge; events: %+v", cap.snapshot())
		}
	})
	t.Run("ollama", func(t *testing.T) {
		cap := resetDedupAndInstall(t)
		req := &InternalRequest{
			Model:          "claude-sonnet-5",
			SourceProtocol: ProtocolAnthropicMessages,
			System:         &SystemPrompt{Parts: []ContentBlock{{Type: "text", Text: "sys", CacheControl: &CacheControl{Type: "ephemeral"}}}},
			Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		}
		if _, err := SerializeOllama(req); err != nil {
			t.Fatalf("SerializeOllama: %v", err)
		}
		if !cap.hasEvent(AnomalyEvent{
			AnomalyType:    AnomalyProtocolLoss,
			FieldPath:      "cache_control",
			TargetProtocol: ProtocolOllamaChat,
		}) {
			t.Fatalf("system-level cache_control lost silently on ollama bridge; events: %+v", cap.snapshot())
		}
	})
}

// 非流式 Gemini 解析的 thoughtsTokenCount 与流式对称（第二批 F8）。
func TestParseGeminiResponseReadsThoughtsTokenCount(t *testing.T) {
	body := []byte(`{
		"candidates": [{"content": {"role": "model", "parts": [{"text": "hi"}]}, "finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 100, "candidatesTokenCount": 50,
			"totalTokenCount": 170, "thoughtsTokenCount": 20},
		"modelVersion": "gemini-2.5-flash"
	}`)
	parsed, err := ParseGeminiResponse(body)
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	if parsed.Usage.ReasoningTokens == nil || *parsed.Usage.ReasoningTokens != 20 {
		t.Fatalf("ReasoningTokens = %v, want 20", parsed.Usage.ReasoningTokens)
	}
	out, err := SerializeGeminiResponse(parsed, "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	var wire struct {
		UsageMetadata struct {
			ThoughtsTokenCount int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire.UsageMetadata.ThoughtsTokenCount != 20 {
		t.Fatalf("thoughtsTokenCount = %d, want 20", wire.UsageMetadata.ThoughtsTokenCount)
	}
}

// 2026-10-27 缓存监测轮：Anthropic 嵌套 cache_creation 对象（5m/1h 写入
// 分档）进 IR usage——流式 message_start 与非流式两路，只服务监测面。
func TestAnthropicUsageCacheCreationSplitParsing(t *testing.T) {
	t.Run("streaming message_start", func(t *testing.T) {
		chunk, err := ParseAnthropicStreamEvent("message_start", []byte(
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":100,"cache_creation_input_tokens":300,"cache_read_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}`))
		if err != nil {
			t.Fatalf("ParseAnthropicStreamEvent: %v", err)
		}
		u := chunk.Usage
		if u == nil || u.CacheWriteTokens == nil || *u.CacheWriteTokens != 300 {
			t.Fatalf("CacheWriteTokens = %v, want 300", u)
		}
		if u.CacheReadTokens == nil || *u.CacheReadTokens != 50 {
			t.Fatalf("CacheReadTokens = %v, want 50", u)
		}
		if u.CacheWriteTTLShort == nil || *u.CacheWriteTTLShort != 100 {
			t.Fatalf("CacheWriteTTLShort = %v, want 100", u)
		}
		if u.CacheWriteTTLLong == nil || *u.CacheWriteTTLLong != 200 {
			t.Fatalf("CacheWriteTTLLong = %v, want 200", u)
		}
	})
	t.Run("non-streaming", func(t *testing.T) {
		parsed, err := ParseAnthropicResponse([]byte(
			`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":7,"cache_creation_input_tokens":300,"cache_read_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}`))
		if err != nil {
			t.Fatalf("ParseAnthropicResponse: %v", err)
		}
		if parsed.Usage.CacheWriteTTLShort == nil || *parsed.Usage.CacheWriteTTLShort != 100 {
			t.Fatalf("CacheWriteTTLShort = %v, want 100", parsed.Usage.CacheWriteTTLShort)
		}
		if parsed.Usage.CacheWriteTTLLong == nil || *parsed.Usage.CacheWriteTTLLong != 200 {
			t.Fatalf("CacheWriteTTLLong = %v, want 200", parsed.Usage.CacheWriteTTLLong)
		}
	})
}
