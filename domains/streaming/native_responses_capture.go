package streaming

import (
	"encoding/json"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard
)

// observeNativeResponsesEvent updates request-scoped telemetry without changing
// the native event sent to the client. The audit capture's legacy text funnel is
// fed synthetic internal payloads because it intentionally knows Chat-shaped
// deltas only; those payloads never leave this process.
func observeNativeResponsesEvent(capture *audit.StreamCapture, event NativeResponsesEvent) {
	if capture == nil || len(event.Payload) == 0 {
		return
	}

	if model := nativeString(event.Payload, "model"); model != "" {
		capture.SetRespModelIfEmpty(model)
	}
	if response, ok := nativeObject(event.Payload, "response"); ok {
		if model := nativeString(response, "model"); model != "" {
			capture.SetRespModelIfEmpty(model)
		}
		if usage := nativeObjectValue(response, "usage"); usage != nil {
			observeNativeUsage(capture, usage)
		}
	}
	if usage := nativeObjectValue(event.Payload, "usage"); usage != nil {
		observeNativeUsage(capture, usage)
	}

	switch event.Name {
	case "response.output_text.delta", "response.refusal.delta", "response.audio_transcript.delta":
		if delta := nativeString(event.Payload, "delta"); delta != "" {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": delta}}}})
			capture.ObservePayload(string(b), "", false)
		}
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		if delta := nativeString(event.Payload, "delta"); delta != "" {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"reasoning_content": delta}}}})
			capture.ObservePayload(string(b), "", false)
		}
	case "response.function_call_arguments.delta":
		if delta := nativeString(event.Payload, "delta"); delta != "" {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]string{"arguments": delta}}}}}}})
			capture.ObservePayload(string(b), "", false)
		}
	}
}

func observeNativeUsage(capture *audit.StreamCapture, usage map[string]json.RawMessage) {
	input := nativeInt(usage, "input_tokens")
	output := nativeInt(usage, "output_tokens")
	// 2026-09-09 audit round 3: native Responses usage carries cache hits in
	// input_tokens_details.cached_tokens — feed it through to the capture.
	// 2026-10-27 缓存标准对齐轮：GPT-5.6 世代起同一对象还带
	// cache_write_tokens（显式缓存写入）⇒ 一并进捕获；未配置缓存写价时
	// 计费公式把写 token 留在 prompt 原价内，台账只多信息、不改变金额。
	var cacheRead, cacheWrite *int
	if details, ok := nativeObject(usage, "input_tokens_details"); ok {
		cacheRead = nativeInt(details, "cached_tokens")
		cacheWrite = nativeInt(details, "cache_write_tokens")
	}
	// 2026-10-27 缓存监测轮：GPT-5.6 显式缓存的 TTL 写入分档
	// （usage.cache_write_tokens_details.{ttl_30m,ttl_12h}）进监测面。
	if split, ok := nativeObject(usage, "cache_write_tokens_details"); ok {
		short := nativeInt(split, "ttl_30m_tokens")
		long := nativeInt(split, "ttl_12h_tokens")
		if short != nil || long != nil {
			capture.ObserveCacheWriteSplit(short, long)
		}
	}
	if input != nil || output != nil {
		capture.ObserveUsage(input, output, cacheRead, cacheWrite)
	}
}

func nativeString(obj map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := obj[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return strings.TrimSpace(value)
}

func nativeObject(obj map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := obj[key]
	if !ok {
		return nil, false
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return value, true
}

func nativeObjectValue(obj map[string]json.RawMessage, key string) map[string]json.RawMessage {
	value, _ := nativeObject(obj, key)
	return value
}

func nativeInt(obj map[string]json.RawMessage, key string) *int {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}
