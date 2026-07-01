package streaming

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamAnthropicSSEToResponses_FullFlow exercises the complete
// Anthropic → Responses API pipeline: initial scaffolding, delta text
// events, accumulated usage flowing into response.completed, and the
// closing sequence.
func TestStreamAnthropicSSEToResponses_FullFlow(t *testing.T) {
	upstreamBody := strings.Join([]string{
		// 1) message_start (input tokens)
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_upstream","model":"claude-opus-4-8","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n",
		"\n",
		// 2) content_block_start + delta (text)
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}` + "\n",
		"\n",
		// 3) content_block_stop
		"event: content_block_stop\n",
		`data: {"type":"content_block_stop","index":0}` + "\n",
		"\n",
		// 4) message_delta (output tokens + stop reason)
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n",
		"\n",
		// 5) message_stop
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-123456789012345678", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()

	// Initial scaffolding events must be present, in order.
	assert.Contains(t, body, "event: response.created")
	assert.Contains(t, body, "event: response.output_item.added")
	assert.Contains(t, body, "event: response.content_part.added")

	// Delta events must appear with response.output_text.delta.
	assert.Contains(t, body, "event: response.output_text.delta")
	assert.Contains(t, body, `"delta":"Hello "`)
	assert.Contains(t, body, `"delta":"world"`)

	// Closing events must appear.
	assert.Contains(t, body, "event: response.output_text.done")
	assert.Contains(t, body, "event: response.output_item.done")
	assert.Contains(t, body, "event: response.completed")

	// Accumulated text surfaces in response.output_text.done and response.completed.
	assert.Contains(t, body, `"text":"Hello world"`)

	// Usage flows into response.completed (input_tokens + output_tokens).
	assert.Contains(t, body, `"input_tokens":10`)
	assert.Contains(t, body, `"output_tokens":5`)
	assert.Contains(t, body, `"total_tokens":15`)

	// Status is "completed" since end_turn → stop.
	assert.Contains(t, body, `"status":"completed"`)

	// No raw Anthropic events leak to the client.
	assert.NotContains(t, body, "event: message_start")
	assert.NotContains(t, body, "event: message_delta")
	assert.NotContains(t, body, "event: message_stop")
	assert.NotContains(t, body, "event: content_block_delta")
	assert.NotContains(t, body, "event: content_block_start")
	assert.NotContains(t, body, `"object":"chat.completion.chunk"`)
}

// TestStreamAnthropicSSEToResponses_MaxTokensMapsToIncomplete verifies
// the Anthropic "max_tokens" finish reason surfaces as Responses API
// "incomplete" status (not "completed").
func TestStreamAnthropicSSEToResponses_MaxTokensMapsToIncomplete(t *testing.T) {
	upstreamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_x","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":0}}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial..."}}` + "\n",
		"\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":100}}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-trunc", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()
	assert.Contains(t, body, `"status":"incomplete"`,
		"max_tokens should map to incomplete status")
	assert.Contains(t, body, `"text":"partial..."`)
	assert.Contains(t, body, `"output_tokens":100`)
}

// TestStreamAnthropicSSEToResponses_ToolUse verifies tool_use blocks
// surface as response.output_item.added + function_call_arguments.delta.
func TestStreamAnthropicSSEToResponses_ToolUse(t *testing.T) {
	upstreamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_t","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n",
		"\n",
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"get_weather","input":{}}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"SF\"}"}}` + "\n",
		"\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-tool", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()
	assert.Contains(t, body, "event: response.output_item.added")
	assert.Contains(t, body, `"name":"get_weather"`)
	assert.Contains(t, body, `"type":"function_call"`)
	assert.Contains(t, body, "event: response.function_call_arguments.delta")
	assert.Contains(t, body, `\"city\":\"SF\"`)
}

// TestStreamAnthropicSSEToResponses_ToolUse_MultipleArgChunks covers
// the realistic Anthropic tool-use case where the JSON arguments are
// streamed across multiple input_json_delta events. Each subsequent
// delta must carry the SAME item_id (the tool_use id from the
// preceding content_block_start) so the Responses API client can
// correlate the partial arguments to the correct function call.
func TestStreamAnthropicSSEToResponses_ToolUse_MultipleArgChunks(t *testing.T) {
	upstreamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_multi","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n",
		"\n",
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_multi_xyz","name":"get_weather","input":{}}}` + "\n",
		"\n",
		// First chunk of arguments
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}` + "\n",
		"\n",
		// Second chunk — this is where the IR parser leaves tc.ID empty,
		// so the bridge's toolCallIDs state must supply it.
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Tokyo\"}"}}` + "\n",
		"\n",
		// Third chunk
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}` + "\n",
		"\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-multi", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()

	// Find all function_call_arguments.delta data lines and extract
	// each one to verify they all carry the same item_id.
	lines := strings.Split(body, "\n")
	var argDeltas []string
	for i, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var parsed map[string]any
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			continue
		}
		if parsed["type"] == "response.function_call_arguments.delta" {
			argDeltas = append(argDeltas, payload)
			_ = i // silence unused
		}
	}
	require.GreaterOrEqual(t, len(argDeltas), 3,
		"expected at least 3 args delta events, got %d", len(argDeltas))

	// Every delta must carry the same item_id — that's the regression
	// test for the toolCallIDs bridge state.
	for i, payload := range argDeltas {
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &parsed))
		itemID, _ := parsed["item_id"].(string)
		assert.Equal(t, "toolu_multi_xyz", itemID,
			"arg delta #%d item_id = %q, want toolu_multi_xyz (regression: bridge state must track id across chunks)", i, itemID)
	}
}

// TestStreamAnthropicSSEToResponses_ToolUse_SecondToolCall verifies
// that a second tool_use block (different Index) gets its own id
// tracked independently, and that the bridge doesn't confuse the two.
func TestStreamAnthropicSSEToResponses_ToolUse_SecondToolCall(t *testing.T) {
	upstreamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_m2","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n",
		"\n",
		// First tool use — input is the partial JSON itself so the args
		// delta will carry the real arguments (not just "{}").
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_first","name":"get_weather","input":{}}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"SF\"}"}}` + "\n",
		"\n",
		// Second tool use — its content_block_start emits an empty
		// arguments delta, then the real args come through the delta.
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_second","name":"get_time","input":{}}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"UTC\"}"}}` + "\n",
		"\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-m2", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()

	// Collect every function_call_arguments.delta event keyed by item_id.
	// (The bridge emits TWO events per tool: an empty one from the
	// content_block_start with input:{}, and a real one from the
	// content_block_delta with partial_json. We're interested in the
	// one that carries the real arguments.)
	lines := strings.Split(body, "\n")
	allDeltas := map[string][]string{}
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &parsed); err != nil {
			continue
		}
		if parsed["type"] == "response.function_call_arguments.delta" {
			itemID, _ := parsed["item_id"].(string)
			delta, _ := parsed["delta"].(string)
			allDeltas[itemID] = append(allDeltas[itemID], delta)
		}
	}

	// The FIRST tool gets two delta events (empty + "{"city":"SF"}").
	// The SECOND tool also gets two delta events (empty + "{"tz":"UTC"}").
	// We assert the LAST (non-empty) delta for each item_id is correctly
	// attributed.
	require.NotEmpty(t, allDeltas["toolu_first"], "first tool should have deltas")
	require.NotEmpty(t, allDeltas["toolu_second"], "second tool should have deltas")

	// The empty {} delta from content_block_start precedes the real one,
	// so the LAST delta carries the actual arguments.
	lastFirst := allDeltas["toolu_first"][len(allDeltas["toolu_first"])-1]
	lastSecond := allDeltas["toolu_second"][len(allDeltas["toolu_second"])-1]
	assert.Equal(t, `{"city":"SF"}`, lastFirst,
		"first tool's last delta should be its real args")
	assert.Equal(t, `{"tz":"UTC"}`, lastSecond,
		"second tool's last delta should be its real args")

	// Every delta MUST be attributed to the correct item_id (no
	// cross-contamination between the two tools).
	assert.Equal(t, `{"city":"SF"}`, allDeltas["toolu_first"][len(allDeltas["toolu_first"])-1],
		"toolu_first's last delta should be its real args")
	assert.Equal(t, `{"tz":"UTC"}`, allDeltas["toolu_second"][len(allDeltas["toolu_second"])-1],
		"toolu_second's last delta should be its real args")
	assert.NotContains(t, allDeltas["toolu_second"][len(allDeltas["toolu_second"])-1], "SF",
		"toolu_second should not have toolu_first's city value")
	assert.NotContains(t, allDeltas["toolu_first"][len(allDeltas["toolu_first"])-1], "UTC",
		"toolu_first should not have toolu_second's tz value")
}

// TestStreamAnthropicSSEToResponses_DropsOpenAIFormatData ensures that
// mislabeled upstream chunks (e.g. a proxy accidentally emitting OpenAI
// chunks on an Anthropic SSE stream) are dropped, not forwarded.
func TestStreamAnthropicSSEToResponses_DropsOpenAIFormatData(t *testing.T) {
	upstreamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_y","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n",
		"\n",
		// Mislabeled OpenAI chunk on the Anthropic event stream:
		"event: message_delta\n",
		`data: {"id":"chatcmpl-leaked","object":"chat.completion.chunk","choices":[{"delta":{"content":"leaked"}}]}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-x", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()
	assert.NotContains(t, body, `"object":"chat.completion.chunk"`,
		"raw OpenAI-format data must never reach the Responses API client")
	assert.NotContains(t, body, `"leaked"`,
		"leaked content must not appear in the output stream")
}

// TestStreamOpenAIToResponsesSSE_FullFlow exercises the OpenAI upstream →
// Responses API path. Mirrors the Anthropic flow test for parity.
func TestStreamOpenAIToResponsesSSE_FullFlow(t *testing.T) {
	upstreamBody := strings.Join([]string{
		// OpenAI first chunk (role)
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n",
		// OpenAI text deltas
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hi "},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"there"},"finish_reason":null}]}` + "\n\n",
		// Finish
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		// Usage chunk
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n",
		// Sentinel
		`data: [DONE]` + "\n\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(rec, resp, "gpt-4o", "gpt-4o", "req-openai-123456789012345678", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()

	// Initial scaffolding.
	assert.Contains(t, body, "event: response.created")
	assert.Contains(t, body, "event: response.output_item.added")
	assert.Contains(t, body, "event: response.content_part.added")

	// Delta events.
	assert.Contains(t, body, "event: response.output_text.delta")
	assert.Contains(t, body, `"delta":"Hi "`)
	assert.Contains(t, body, `"delta":"there"`)

	// Closing events.
	assert.Contains(t, body, "event: response.output_text.done")
	assert.Contains(t, body, "event: response.output_item.done")
	assert.Contains(t, body, "event: response.completed")
	assert.Contains(t, body, `"text":"Hi there"`)
	assert.Contains(t, body, `"status":"completed"`)

	// Usage accumulated.
	assert.Contains(t, body, `"input_tokens":10`)
	assert.Contains(t, body, `"output_tokens":5`)
	assert.Contains(t, body, `"total_tokens":15`)

	// No raw OpenAI chunks leak (the converter must not forward data:).
	assert.NotContains(t, body, `"object":"chat.completion.chunk"`)
	assert.NotContains(t, body, `data: [DONE]`)
}

// TestStreamOpenAIToResponsesSSE_NoUsageStillFinishes ensures the bridge
// closes gracefully when upstream omits usage (some providers do this).
func TestStreamOpenAIToResponsesSSE_NoUsageStillFinishes(t *testing.T) {
	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"delta":{"content":"ok"}}]}` + "\n\n",
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(rec, resp, "gpt-4o", "gpt-4o", "req-nou", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()
	assert.Contains(t, body, "event: response.completed")
	assert.Contains(t, body, `"text":"ok"`)
	assert.Contains(t, body, `"total_tokens":0`,
		"usage defaults to zero when upstream omits it")
}

// TestStreamOpenAIToResponsesSSE_LengthMapsToIncomplete mirrors the
// Anthropic max_tokens → incomplete translation for the OpenAI path.
func TestStreamOpenAIToResponsesSSE_LengthMapsToIncomplete(t *testing.T) {
	upstreamBody := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(rec, resp, "gpt-4o", "gpt-4o", "req-trunc", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()
	assert.Contains(t, body, `"status":"incomplete"`)
	assert.Contains(t, body, `"text":"partial"`)
}

// TestResponsesScaffold_IDDerivation verifies the resp_/msg_ IDs follow
// the existing StreamResponsesSSE convention so client-side dedup tokens
// continue to work across the IR bridge rollout.
func TestResponsesScaffold_IDDerivation(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newResponsesScaffold(rec, rec, "f3b1c4686b19651a6d5e6846ab54b2e7", "claude-opus-4-8")
	assert.Equal(t, "resp_f3b1c4686b19651a6d5e6846", s.respID,
		"respID = resp_ + requestID[:24]")
	// requestID[8:24] is 16 chars: "6b19651a6d5e6846"
	assert.Equal(t, "msg_6b19651a6d5e6846", s.msgID,
		"msgID = msg_ + requestID[8:24] (16 chars)")

	// Short requestID: full passthrough.
	s2 := newResponsesScaffold(rec, rec, "short-id", "gpt-4o")
	assert.Equal(t, "resp_short-id", s2.respID)
	assert.Equal(t, "msg_short-id", s2.msgID)

	// Empty requestID: deterministic placeholder.
	s3 := newResponsesScaffold(rec, rec, "", "gpt-4o")
	assert.Equal(t, "resp_no_request_id", s3.respID)
	assert.Equal(t, "msg_no_request_id", s3.msgID)
}

// TestMapAnthropicFinishReasonIRMapping pins the IR's
// Anthropic→OpenAI stop_reason translation by round-tripping
// Anthropic message_delta payloads through the IR parser and asserting
// the resulting StreamChunk.FinishReason is in OpenAI form. The bridge
// no longer maps explicitly (the IR layer does it during parse), so this
// test catches any drift between the IR's mapping and what the bridge
// expects to receive.
func TestMapAnthropicFinishReasonIRMapping(t *testing.T) {
	// Inline the IR's documented mapping for documentation purposes.
	// Keep in sync with internal/ir/stream.go:mapAnthropicFinishReasonToOpenAI.
	cases := []struct {
		anthropic string
		want      string
	}{
		{"end_turn", "stop"},
		{"tool_use", "tool_calls"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"refusal", "content_filter"},
		{"unknown_future_reason", "stop"},
	}
	for _, c := range cases {
		// Build an Anthropic message_delta event matching the IR
		// parser's expected shape (type + delta + usage). The IR layer
		// rejects malformed message_delta events.
		payload := `{"type":"message_delta","delta":{"stop_reason":"` + c.anthropic + `"},"usage":{"output_tokens":0}}`
		chunk, err := ir.ParseAnthropicStreamEvent("message_delta", []byte(payload))
		if err != nil {
			t.Fatalf("parse failed for %q: %v", c.anthropic, err)
		}
		assert.Equal(t, c.want, chunk.FinishReason,
			"IR mapping %q (Anthropic message_delta → OpenAI finish_reason)", c.anthropic)
	}
}

// TestResponsesScaffold_RoundTrip ensures the initial and final events
// produced by the scaffold are valid JSON and contain the expected fields.
func TestResponsesScaffold_RoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newResponsesScaffold(rec, httptest.NewRecorder(), "f3b1c4686b19651a6d5e6846ab54b2e7", "claude-opus-4-8")
	s.writeInitialEvents()
	rec2 := httptest.NewRecorder()
	// Overwrite w to a fresh recorder so we capture only the final events.
	s2 := &responsesScaffold{
		w: rec2, flusher: rec2,
		requestID:   "f3b1c4686b19651a6d5e6846ab54b2e7",
		clientModel: "claude-opus-4-8",
		respID:      s.respID,
		msgID:       s.msgID,
		created:     s.created,
	}
	s2.writeFinalEvents("Hello world", "stop", 10, 5, 15)

	// Parse the closing response.completed event JSON.
	body := rec2.Body.String()
	require.Contains(t, body, "event: response.completed")

	// Find the data: line that follows response.completed.
	lines := strings.Split(body, "\n")
	var payload string
	for i, line := range lines {
		if strings.HasPrefix(line, "event: response.completed") {
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "data: ") {
					payload = strings.TrimPrefix(lines[j], "data: ")
					break
				}
			}
			break
		}
	}
	require.NotEmpty(t, payload, "response.completed data line not found")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &parsed))
	assert.Equal(t, "response.completed", parsed["type"])
	respObj := parsed["response"].(map[string]any)
	assert.Equal(t, s.respID, respObj["id"])
	assert.Equal(t, "response", respObj["object"])
	assert.Equal(t, "completed", respObj["status"])

	usage := respObj["usage"].(map[string]any)
	assert.Equal(t, float64(10), usage["input_tokens"])
	assert.Equal(t, float64(5), usage["output_tokens"])
	assert.Equal(t, float64(15), usage["total_tokens"])

	output := respObj["output"].([]any)
	require.Len(t, output, 1)
	item := output[0].(map[string]any)
	assert.Equal(t, "message", item["type"])
	content := item["content"].([]any)
	require.Len(t, content, 1)
	part := content[0].(map[string]any)
	assert.Equal(t, "output_text", part["type"])
	assert.Equal(t, "Hello world", part["text"])
}

// TestStreamOpenAIToResponsesSSE_ToolCalls_MultipleArgChunks covers
// the OpenAI tool_calls streaming protocol where the tool id is sent
// only in the FIRST chunk and subsequent chunks carry only the
// incremental arguments. The bridge must track the id across chunks
// so every function_call_arguments.delta event references the same
// item_id (matching the Responses API contract).
func TestStreamOpenAIToResponsesSSE_ToolCalls_MultipleArgChunks(t *testing.T) {
	upstreamBody := strings.Join([]string{
		// First chunk: has id + name + empty args
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_xyz","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
		// Subsequent chunks: incremental args only, no id
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(rec, resp, "gpt-4o", "gpt-4o", "req-oai-tool", nil, nil)
	require.False(t, out.Interrupted)

	body := rec.Body.String()

	// Find all function_call_arguments.delta events. The bridge emits
	// exactly 2 deltas for our test data: one with `{"city":` and one
	// with `"SF"}`. Together they form the full arguments `{"city":"SF"}`.
	lines := strings.Split(body, "\n")
	var argDeltas []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &parsed); err != nil {
			continue
		}
		if parsed["type"] == "response.function_call_arguments.delta" {
			argDeltas = append(argDeltas, strings.TrimPrefix(line, "data: "))
		}
	}
	require.GreaterOrEqual(t, len(argDeltas), 2,
		"expected at least 2 args delta events, got %d", len(argDeltas))

	// Every delta must carry the same item_id.
	for i, payload := range argDeltas {
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &parsed))
		itemID, _ := parsed["item_id"].(string)
		assert.Equal(t, "call_xyz", itemID,
			"arg delta #%d item_id = %q, want call_xyz (regression: bridge state must track id across chunks)",
			i, itemID)
	}

	// The first output_item.added event should also carry call_xyz as id.
	assert.Contains(t, body, `"id":"call_xyz"`,
		"first output_item.added should have tool call id call_xyz")

	// The concatenated delta text reconstructs the JSON args.
	var fullDelta strings.Builder
	for _, payload := range argDeltas {
		var parsed map[string]any
		_ = json.Unmarshal([]byte(payload), &parsed)
		if d, ok := parsed["delta"].(string); ok {
			fullDelta.WriteString(d)
		}
	}
	assert.Equal(t, `{"city":"SF"}`, fullDelta.String(),
		"concatenated deltas should form the full JSON arguments")
}
