package streaming

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

// 2026-10-27 缓存标准对齐轮：native Responses 事件里的 usage 自带缓存命中，
// GPT-5.6 世代起同一对象还带 cache_write_tokens（显式缓存写入）⇒ 两个槽
// 都必须进捕获，账面才完整（未配置缓存写价时计费公式仍按 prompt 原价）。
func TestObserveNativeUsageCapturesCacheReadAndWrite(t *testing.T) {
	capture := audit.NewStreamCapture()
	usage := map[string]json.RawMessage{
		"input_tokens":         json.RawMessage(`1200`),
		"output_tokens":        json.RawMessage(`64`),
		"input_tokens_details": json.RawMessage(`{"cached_tokens":768,"cache_write_tokens":128}`),
	}
	observeNativeUsage(capture, usage)

	m := capture.SummaryAsMap()
	if m["prompt_tokens"].(int) != 1200 || m["completion_tokens"].(int) != 64 {
		t.Fatalf("token slots = %v", m)
	}
	if m["cache_read_tokens"].(int) != 768 {
		t.Fatalf("cache_read_tokens = %v, want 768", m["cache_read_tokens"])
	}
	if m["cache_write_tokens"].(int) != 128 {
		t.Fatalf("cache_write_tokens = %v, want 128", m["cache_write_tokens"])
	}

	// response.completed 包裹形态（usage 在 response 对象内）同样要进。
	capture2 := audit.NewStreamCapture()
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"response":{"model":"gpt-5.6","usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cached_tokens":64,"cache_write_tokens":8}}}}`), &wrapped); err != nil {
		t.Fatalf("unmarshal wrapped payload: %v", err)
	}
	observeNativeResponsesEvent(capture2, NativeResponsesEvent{
		Name:    "response.completed",
		Payload: wrapped,
	})
	m2 := capture2.SummaryAsMap()
	if m2["cache_read_tokens"].(int) != 64 || m2["cache_write_tokens"].(int) != 8 {
		t.Fatalf("wrapped usage slots = %v", m2)
	}

	// 2026-10-27 缓存监测轮：GPT-5.6 显式缓存的 TTL 写入分档进监测面。
	capture3 := audit.NewStreamCapture()
	var withSplit map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"response":{"model":"gpt-5.6","usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cache_write_tokens":192},"cache_write_tokens_details":{"ttl_30m_tokens":64,"ttl_12h_tokens":128}}}}`), &withSplit); err != nil {
		t.Fatalf("unmarshal split payload: %v", err)
	}
	observeNativeResponsesEvent(capture3, NativeResponsesEvent{
		Name:    "response.completed",
		Payload: withSplit,
	})
	m3 := capture3.SummaryAsMap()
	if m3["cache_write_ttl_short_tokens"].(int) != 64 || m3["cache_write_ttl_long_tokens"].(int) != 128 {
		t.Fatalf("ttl split slots = %v", m3)
	}
}
