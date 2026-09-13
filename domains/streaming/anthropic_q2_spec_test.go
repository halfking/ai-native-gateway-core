package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 2026-08-28 spec-audit regression suite for the Q2 (OpenAI → Anthropic)
// bridge. Grounded in official vendor specs gathered during the review:
//
//   - OpenAI tool_calls[].index is the stable aggregation key — array
//     position within one chunk is NOT (OpenAI Chat API spec).
//   - Anthropic content_block_start/stop must come in matched pairs; every
//     tool_use block must close before message_delta (Anthropic Messages
//     streaming reference).
//   - OpenAI-compatible upstreams (Qwen DashScope, some Kimi deployments)
//     may deliver a mid-stream `data: {"error":{...}}` chunk — a concern
//     for vendors that don't document `event: error` frames.
//   - Zhipu GLM uses finish_reason as an out-of-band error channel
//     (network_error/sensitive/model_context_window_exceeded).

// TestQ2ToolCallStableIndexAggregatesFragments pins the tool_calls index
// aggregation: fragments for the same openaiIdx must merge into a single
// Anthropic tool_use block with exactly one start and one stop.
func TestQ2ToolCallStableIndexAggregatesFragments(t *testing.T) {
	body := strings.Join([]string{
		// Fragment 1: full id+name+leading args.
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"do_thing","arguments":"{\"root\":"}}]},"finish_reason":null}]}`,
		"",
		// Fragment 2: argument continuation, NO id/name — must not open a
		// second block.
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"steps\":[]}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"s1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "kimi-k3", "kimi-k3", "req-q2-tool-idx", nil, nil)

	assert.False(t, out.Interrupted)
	wire := rec.Body.String()
	// Exactly ONE tool_use content block plus a matching stop_reason field.
	// (Content-block bodies spell it `"type":"tool_use"` once.)
	assert.Equal(t, 1, strings.Count(wire, `"type":"tool_use"`), wire)
	assert.Contains(t, wire, `"id":"call_1"`)
	assert.Contains(t, wire, `"name":"do_thing"`)
	// Both argument fragments must appear in order on block index 1.
	assert.Contains(t, wire, `"index":1`)
	assert.Contains(t, wire, `"{\"root\":"`)
	assert.Contains(t, wire, `"\"steps\":[]}"`)
	// The tool block is closed exactly once before message_delta.
	stopCount := strings.Count(wire, `"content_block_stop"`)
	// One stop for the (pre-closed) text block 0 plus one for the tool block.
	assert.Equal(t, 2, stopCount, wire)
	// Tool call made and its block acknowledged: message_delta carries
	// stop_reason=tool_use exactly once.
	assert.Equal(t, 1, strings.Count(wire, `"stop_reason":"tool_use"`), wire)
	assert.Contains(t, wire, "event: message_stop", wire)
}

// TestQ2MidStreamJSONErrorChunkIntercepted pins: an OpenAI-compatible
// upstream that sends `data: {"error":{...}}` mid-stream (the non-standard
// DashScope/Azure pattern) must interrupt the stream rather than failing
// silently to completion.
func TestQ2MidStreamJSONErrorChunkIntercepted(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"s1","choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`,
		"",
		`data: {"error":{"type":"rate_limit_error","code":"Throttling","message":"burst rate hit"}}`,
		"",
	}, "\n")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "qwen-max", "qwen-max", "req-q2-json-error", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
	assert.Equal(t, errorsx.KindRateLimit, out.Kind,
		"rate_limit_error must classify as rate_limit, not generic upstream_down")
	// The raw upstream message must NOT leak to the client.
	assert.NotContains(t, rec.Body.String(), "burst rate hit")
}

// TestQ2GLMStyleFinishReasonError pins the Zhipu GLM spec behavior: the
// vendor encodes in-stream failures via abnormal finish_reason values
// (network_error/sensitive/model_context_window_exceeded). Those must halt
// the turn, not silently complete.
func TestQ2GLMStyleFinishReasonError(t *testing.T) {
	cases := []struct {
		finishReason  string
		wantReason    string
		wantKind      errorsx.ErrorKind
		wantResumable bool
	}{
		// Content was already committed to the client before these GLM
		// error signals — a retry would duplicate it, so resumable=false.
		{"network_error", "network_error", errorsx.KindNetwork, false},
		{"sensitive", "content_filter", errorsx.KindContentFilter, false},
		{"model_context_window_exceeded", "context_length_exceeded", errorsx.KindContextLength, false},
	}
	for _, tc := range cases {
		t.Run(tc.finishReason, func(t *testing.T) {
			body := strings.Join([]string{
				`data: {"id":"s1","choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`,
				"",
				`data: {"id":"s1","choices":[{"delta":{},"finish_reason":"` + tc.finishReason + `"}]}`,
				"",
				`data: [DONE]`,
				"",
			}, "\n")
			resp := &http.Response{
				Body:    io.NopCloser(strings.NewReader(body)),
				Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			}
			rec := httptest.NewRecorder()

			out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp,
				"glm-5.2", "glm-5.2", "req-q2-glm-"+tc.finishReason, nil, nil)

			assert.True(t, out.Interrupted, "finish_reason=%s must interrupt", tc.finishReason)
			assert.Equal(t, tc.wantReason, out.Reason)
			assert.Equal(t, tc.wantKind, out.Kind)
			assert.Equal(t, tc.wantResumable, out.Resumable)
			// Content was visible: the client must receive a sanitized error frame.
			wire := rec.Body.String()
			assert.Contains(t, wire, "partial", "content must be delivered")
			assert.Contains(t, wire, `"error"`, "client-visible error required")
		})
	}
}

// TestQ2GLMStyleFinishReasonErrorPreContent verifies that a GLM error signal
// arriving before ANY client-visible content keeps the attempt transparently
// retryable (no error frame on wire, Resumable=true).
func TestQ2GLMStyleFinishReasonErrorPreContent(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"s1","choices":[{"delta":{},"finish_reason":"network_error"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp,
		"glm-5.2", "glm-5.2", "req-q2-glm-precon", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "network_error", out.Reason)
	assert.True(t, out.Resumable, "pre-content: must stay transparent for failover")
	// No client-visible content, so no error frame should be emitted.
	wire := rec.Body.String()
	assert.NotContains(t, wire, `"error"`, "pre-content interruption must not emit error frame")
}

// TestQ2ToolCallMultiIndexParity verifies parallel tool calls each collapse
// to their own Anthropic block with correct start/stop pairing, independent
// of the order the fragments arrive in.
func TestQ2ToolCallMultiIndexParity(t *testing.T) {
	body := strings.Join([]string{
		// Both calls announced in one chunk (indices 0 and 1).
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[` +
			`{"index":0,"id":"call_a","type":"function","function":{"name":"a","arguments":"{}"}},` +
			`{"index":1,"id":"call_b","type":"function","function":{"name":"b","arguments":"{}"}}]},` +
			`"finish_reason":null}]}`,
		"",
		// Arguments extended for the second call only.
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"x"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"s1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "kimi-k3", "kimi-k3", "req-q2-multi-tool", nil, nil)

	assert.False(t, out.Interrupted)
	wire := rec.Body.String()
	assert.Equal(t, 2, strings.Count(wire, `"type":"tool_use"`), wire)
	assert.Contains(t, wire, `"name":"a"`)
	assert.Contains(t, wire, `"name":"b"`)
	// Anthropic block indices for the two tool calls must be 1 and 2 (block 0
	// is reserved for the implicit text declaration).
	assert.Contains(t, wire, `"index":1`)
	assert.Contains(t, wire, `"index":2`)
	// Extending call_b's arguments must land in its own block only.
	assert.Contains(t, wire, `"x"`, wire)
	// Both tool_use blocks must be closed before the message tail.
	assert.GreaterOrEqual(t, strings.Count(wire, `"content_block_stop"`), 3, wire)
	assert.Contains(t, wire, `"stop_reason":"tool_use"`)
}

// TestQ2NativeReasoningContentStreamsAsThinkingBlock pins the audit R8 P1
// fix: DeepSeek R1 / GLM-Z1 style upstreams stream reasoning in
// delta.reasoning_content. The bridge must surface it as a thinking block —
// not silently drop the chain-of-thought — and keep content blocks strictly
// sequential (thinking closes before the text block reopens on a fresh index).
func TestQ2NativeReasoningContentStreamsAsThinkingBlock(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"s1","choices":[{"delta":{"reasoning_content":"step one "}}]}`,
		`data: {"id":"s1","choices":[{"delta":{"reasoning_content":"step two"}}]}`,
		`data: {"id":"s1","choices":[{"delta":{"content":"final answer"}}]}`,
		`data: {"id":"s1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "deepseek-r1", "deepseek-r1", "req-q2-reasoning", nil, nil)

	assert.False(t, out.Interrupted)
	wire := rec.Body.String()
	assert.Contains(t, wire, `"type":"thinking"`, "thinking block must be opened")
	assert.Contains(t, wire, `"thinking_delta"`)
	assert.Contains(t, wire, "step one ")
	assert.Contains(t, wire, "step two")
	// Text must survive and land after the thinking block closed.
	assert.Contains(t, wire, `"text_delta"`)
	assert.Contains(t, wire, "final answer")
	thinkingIdx := strings.Index(wire, `"type":"thinking"`)
	textAfterThinking := strings.Index(wire, "final answer")
	assert.Greater(t, textAfterThinking, thinkingIdx)
	// Matched start/stop pairs: text block 0 + thinking block + replayed text block.
	starts := strings.Count(wire, `"content_block_start"`)
	stops := strings.Count(wire, `"content_block_stop"`)
	assert.Equal(t, starts, stops, wire)
}

// TestQ2ArgFirstToolCallDefersBlockOpenUntilName pins the lazy-open hold:
// an arg-first fragment (arguments before function.name) must NOT emit a
// content_block_start with an empty name. The buffered prefix replays as
// the first input_json_delta once the name arrives, in order.
func TestQ2ArgFirstToolCallDefersBlockOpenUntilName(t *testing.T) {
	body := strings.Join([]string{
		// Fragment 1: id + arguments, NO name — held, nothing emitted.
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_af","type":"function","function":{"arguments":"{\"a\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":null}]}`,
		// Fragment 3: name arrives — block opens NOW, buffered args replay.
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"deferred_tool"}}]},"finish_reason":null}]}`,
		`data: {"id":"s1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "arg-first-model", "arg-first-model", "req-q2-argfirst", nil, nil)

	assert.False(t, out.Interrupted)
	wire := rec.Body.String()
	// Exactly one tool_use block, and its start carries the real name.
	// (Map serialization is alphabetical: name precedes type in the JSON.)
	assert.Equal(t, 1, strings.Count(wire, `"type":"tool_use"`), wire)
	assert.Contains(t, wire, `"name":"deferred_tool"`, wire)
	assert.NotContains(t, wire, `"name":""`, "no empty-name tool_use may appear on the wire")
	// Held fragments coalesce and replay as input_json_delta(s) on the
	// opened block, in order. JSON key ordering proves the prefix survived.
	assert.Contains(t, wire, `"{\"a\":1}"`, wire)
	assert.Equal(t, 1, strings.Count(wire, `"type":"input_json_delta"`), wire)
	// Stop pairing still holds: text block 0 + tool block.
	assert.Equal(t, 2, strings.Count(wire, `"content_block_stop"`), wire)
}

// TestQ2NamelessToolCallDroppedAtStreamEnd pins the terminal rejection: if
// the upstream finishes without ever naming a call, no tool_use block may
// be emitted for it (empty name violates the Anthropic wire format), and
// the stream still terminates cleanly.
func TestQ2NamelessToolCallDroppedAtStreamEnd(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"s1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_nn","type":"function","function":{"arguments":"{\"x\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"s1","choices":[{"delta":{"content":"no tool for you"},"finish_reason":null}]}`,
		`data: {"id":"s1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "nameless-model", "nameless-model", "req-q2-nameless", nil, nil)

	assert.False(t, out.Interrupted)
	wire := rec.Body.String()
	assert.NotContains(t, wire, `"type":"tool_use"`, "nameless call must not open a block")
	assert.NotContains(t, wire, `"name":""`)
	assert.NotContains(t, wire, `"id":"call_nn"`, "held state must not leak to the wire")
	assert.Contains(t, wire, "no tool for you")
	assert.Contains(t, wire, "event: message_stop")
}
