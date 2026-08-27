package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// passthroughNopCloser wraps an SSE body for the bridge tests.
func passthroughNopCloser(body string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(body))
}

// 2026-08-28 Q3 regression (245 incident): relay-style Anthropic upstreams
// answer a failing turn with an `event: error` frame whose message packs a
// multi-line internal diagnostic blob ("Turn execution failed / provider=… /
// request=… / reason=unknown retryable=false"). The Q4 passthrough path had
// an interception guard; the Q3 Anthropic→OpenAI translator forwarded the
// blob to the client verbatim and hardcoded KindUpstreamDown regardless of
// the upstream error type. These tests pin the fixed behavior:
//
//  1. the diagnostic blob never reaches the OpenAI client,
//  2. the StreamOutcome kind is classified from the upstream error type,
//  3. pre-content interruptions stay transparently retryable, post-content
//     ones are terminal.

const q3RelayDiagnosticErrorBody = "event: error\n" +
	"data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Turn execution failed\\nprovider=3230afbd-6e95-422f-ba98-0190e2c4ccca model=gpt-5.6-terra request=7f8ca320-031f-4d6b-b378-878630a51fc0 reason=unknown retryable=false\"}}\n\n"

// q3TextDeltaFrame emits one complete text block: the bridge buffers text
// deltas and flushes them on content_block_stop, so a stream-interruption test
// that needs committed client-visible content must include the stop event.
const q3TextDeltaFrame = "event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"

// Error as the first frame: buffered gate holds everything uncommitted →
// client sees nothing, interruption stays transparently retryable, and the
// kind follows the upstream error type (api_error → upstream_down).
func TestStreamAnthropicSSEToOpenAI_RelayDiagnosticErrorIsSuppressed(t *testing.T) {
	rec := httptest.NewRecorder()
	resp := &http.Response{
		Body:    passthroughNopCloser(q3RelayDiagnosticErrorBody),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp,
		"claude-sonnet-5", "gpt-5.6-terra", "req-q3-relay-err", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
	assert.Equal(t, errorsx.KindUpstreamDown, out.Kind)
	assert.True(t, out.Resumable, "nothing client-visible: executor must fail over transparently")

	wire := rec.Body.String()
	assert.NotContains(t, wire, "Turn execution failed")
	assert.NotContains(t, wire, "3230afbd-6e95-422f-ba98-0190e2c4ccca")
	assert.NotContains(t, wire, "reason=unknown")
	assert.NotContains(t, wire, "retryable=false")
	assert.NotContains(t, wire, "gpt-5.6-terra")
}

// Content first (commits the attempt), then the relay's terminal error: the
// client keeps its content and receives the gateway's sanitized error chunk;
// internal diagnostics must not leak and the kind must be classified.
func TestStreamAnthropicSSEToOpenAI_ErrorAfterContentRendersSanitizedError(t *testing.T) {
	// Immediate gate mode: content commits on its first frame, so the later
	// upstream error must render the sanitized client-visible terminal chunk.
	restore := setAttemptGateForTest(true, GateModeImmediate)
	defer restore()
	rec := httptest.NewRecorder()
	resp := &http.Response{
		Body:    passthroughNopCloser(q3TextDeltaFrame + overloadQ3ErrorFrame()),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp,
		"claude-sonnet-5", "claude-sonnet-5", "req-q3-post-content-err", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
	assert.Equal(t, errorsx.KindUpstreamOverloaded, out.Kind,
		"overloaded_error must classify as overload, not hub upstream_down")
	assert.False(t, out.Resumable, "client saw content + terminal error; retry would duplicate")

	wire := rec.Body.String()
	assert.Contains(t, wire, "hello")
	assert.Contains(t, wire, `"error"`)
	assert.Contains(t, wire, "data: [DONE]")
	assert.NotContains(t, wire, "Turn execution failed")
}

// Classification matrix for the Q3 error branch: outcome kind follows the
// upstream error.type instead of the previous hardcoded KindUpstreamDown.
func TestStreamAnthropicSSEToOpenAI_ErrorKindClassification(t *testing.T) {
	cases := []struct {
		errType string
		want    errorsx.ErrorKind
	}{
		{"rate_limit_error", errorsx.KindRateLimit},
		{"overloaded_error", errorsx.KindUpstreamOverloaded},
		{"authentication_error", errorsx.KindAuth},
		{"permission_error", errorsx.KindAuth},
		{"timeout_error", errorsx.KindTimeout},
		{"api_error", errorsx.KindUpstreamDown},
		{"mystery_error", errorsx.KindUpstreamDown},
	}
	for _, tc := range cases {
		t.Run(tc.errType, func(t *testing.T) {
			body := "event: error\n" +
				"data: {\"type\":\"error\",\"error\":{\"type\":\"" + tc.errType + "\",\"message\":\"Turn execution failed\\nreason=unknown retryable=false\"}}\n\n"
			rec := httptest.NewRecorder()
			resp := &http.Response{
				Body:    passthroughNopCloser(body),
				Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			}
			out := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp,
				"claude-sonnet-5", "claude-sonnet-5", "req-q3-kind-"+tc.errType, nil, nil)
			assert.True(t, out.Interrupted, tc.errType)
			assert.Equal(t, tc.want, out.Kind, tc.errType)
			assert.NotContains(t, rec.Body.String(), "Turn execution failed", tc.errType)
			assert.NotContains(t, rec.Body.String(), "reason=unknown", tc.errType)
		})
	}
}

// An error payload with no explicit type still lands on upstream_down and
// never leaks the raw message.
func TestStreamAnthropicSSEToOpenAI_UntypedErrorDefaultsToUpstreamDown(t *testing.T) {
	rec := httptest.NewRecorder()
	resp := &http.Response{
		Body: passthroughNopCloser("event: error\n" +
			"data: {\"type\":\"error\",\"error\":{\"message\":\"Turn execution failed\\nreason=unknown retryable=false\"}}\n\n"),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp,
		"claude-sonnet-5", "claude-sonnet-5", "req-q3-untyped-err", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, errorsx.KindUpstreamDown, out.Kind)
	assert.NotContains(t, rec.Body.String(), "Turn execution failed")
}

// The caller-supplied ctx is authoritative: previously the function silently
// replaced it with resp.Request.Context(), which let a canceled dispatch
// context go unnoticed. A pre-canceled ctx must abort the stream read.
func TestStreamAnthropicSSEToOpenAI_RespectsCallerContext(t *testing.T) {
	// A canceled caller ctx must abort promptly; the 1s chunk timeout is only
	// a safety net in case the cancellation path regresses.
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	resp := &http.Response{
		Body:    &closeUnblocksBody{closed: make(chan struct{})},
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicSSEToOpenAI(ctx, rec, resp,
		"claude-sonnet-5", "claude-sonnet-5", "req-q3-canceled-ctx", nil, nil)

	assert.True(t, out.Interrupted)
	assert.NotEqual(t, "", string(out.Kind), "canceled-ctx outcome must carry a classifiable kind")
}

// malformed_tool_args must carry KindUpstreamDown; leaving it empty made the
// executor default the failure to stream_timeout, poisoning the health
// attribution.
func TestStreamAnthropicSSEToOpenAI_MalformedToolArgsKindIsUpstreamDown(t *testing.T) {
	body := strings.Join([]string{
		q3ToolUseStartFrame(),
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{not-json\\\"\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
	}, "")
	rec := httptest.NewRecorder()
	capture := &audit.StreamCapture{}
	resp := &http.Response{
		Body:    passthroughNopCloser(body),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp,
		"claude-sonnet-5", "claude-sonnet-5", "req-q3-malformed-args", capture, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "malformed_tool_args", out.Reason)
	assert.Equal(t, errorsx.KindUpstreamDown, out.Kind)
	assert.NotContains(t, rec.Body.String(), "input_json_delta")
}

func q3ToolUseStartFrame() string {
	return "event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"get_weather\",\"input\":{}}}\n\n"
}

func overloadQ3ErrorFrame() string {
	return "event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
}
