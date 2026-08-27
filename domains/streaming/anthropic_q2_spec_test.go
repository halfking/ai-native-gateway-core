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
		finishReason string
		wantReason   string
		wantKind     errorsx.ErrorKind
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
		})
	}
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
