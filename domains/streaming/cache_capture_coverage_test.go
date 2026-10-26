package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

// 2026-10-27 缓存监测轮：透传与两条桥路径的缓存捕获覆盖钉桩。
// 判据：不管上游/客户端是什么协议组合，capture（SummaryAsMap ⇒ reqLog ⇒
// _request_logs 缓存列）都必须拿到 缓存命中/写入 总量与 TTL 写入分档。

func anthropicSSEServer(t *testing.T, sse string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	return resp
}

// TestStreamAnthropicPassthrough_CacheUsageReachesCapture 钉死透传路径
// （Anthropic→Anthropic，Claude Code 主流量）的缓存捕获：此前
// observeAnthropicPayload 只喂 InputTokens/OutputTokens（预览注入用），
// 标准捕获槽的 prompt/cache 全空 ⇒ prompt_tokens 落估算值、缓存命中与
// TTL 分档整段不进台账。
func TestStreamAnthropicPassthrough_CacheUsageReachesCapture(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":50,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	resp := anthropicSSEServer(t, sse)
	defer func() { _ = resp.Body.Close() }()

	capture := audit.NewStreamCapture()
	rec := httptest.NewRecorder()
	out := StreamAnthropicPassthrough(context.Background(), rec, resp,
		"claude-sonnet-5", "claude-sonnet-5", "req-cache-split", capture, nil)
	require.False(t, out.Interrupted, "outcome = %+v", out)

	m := capture.SummaryAsMap()
	assert.Equal(t, 100, m["prompt_tokens"], "prompt_tokens must come from message_start, not the estimator")
	assert.Equal(t, 7, m["completion_tokens"], "completion_tokens must come from the cumulative message_delta")
	assert.Equal(t, 50, m["cache_read_tokens"])
	assert.Equal(t, 300, m["cache_write_tokens"])
	assert.Equal(t, 100, m["cache_write_ttl_short_tokens"])
	assert.Equal(t, 200, m["cache_write_ttl_long_tokens"])
}

// TestStreamOpenAIToAnthropicSSE_CaptureUsesSharedUsageTable 钉死
// chat 上游 → Anthropic 客户端桥的捕获走共享 usage 变量表：DeepSeek 顶层
// 命中字段此前被窄解析器丢掉、cache_write 恒为 nil。
func TestStreamOpenAIToAnthropicSSE_CaptureUsesSharedUsageTable(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":36,"cache_creation_input_tokens":9}}` + "\n\n" +
				"data: [DONE]\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp,
		"claude-sonnet-5", "deepseek-chat", "req-shared-table", capture, nil)
	require.False(t, out.Interrupted, "outcome = %+v", out)

	m := capture.SummaryAsMap()
	assert.Equal(t, 100, m["prompt_tokens"])
	assert.Equal(t, 20, m["completion_tokens"])
	assert.Equal(t, 64, m["cache_read_tokens"], "DeepSeek top-level hit tokens must be captured via the shared variant table")
	assert.Equal(t, 9, m["cache_write_tokens"], "cache_write must flow into the capture (was hardcoded nil)")
}

// TestStreamResponsesSSE_CaptureUsesSharedUsageTable 同上，Responses 客户端
// ← chat 上游桥：capture 必须拿到写入量（此前恒 nil）。
func TestStreamResponsesSSE_CaptureUsesSharedUsageTable(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":64},"cache_creation_input_tokens":12}}` + "\n\n" +
				"data: [DONE]\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
	}
	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	out := StreamResponsesSSE(context.Background(), rec, resp,
		"gpt-5.6", "gpt-5.6", "req-responses-capture", capture)
	require.False(t, out.Interrupted, "outcome = %+v", out)

	m := capture.SummaryAsMap()
	assert.Equal(t, 64, m["cache_read_tokens"])
	assert.Equal(t, 12, m["cache_write_tokens"], "cache_write must flow into the capture (was hardcoded nil)")
}
