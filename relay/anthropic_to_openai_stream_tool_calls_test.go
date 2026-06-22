package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_InconsistentToolCalls tests the case where
// Anthropic returns stop_reason:"tool_use" but sends no content_block_start(tool_use)
// event. This was observed with claude-sonnet-4-6 in production (request_id:
// 5732c872-0df0-4b30-b9d6-528c8521975d). The gateway should detect this
// inconsistency and correct finish_reason to "stop" to prevent clients from
// waiting forever for tool_calls data that never arrives.
func TestAnthropicToOpenAIStream_InconsistentToolCalls(t *testing.T) {
	// Simulate Anthropic SSE stream with inconsistent tool_calls:
	// - Has stop_reason:"tool_use" in message_delta
	// - But NO content_block_start(tool_use) event
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I will generate a code graph."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(anthropicSSE))
	}))
	defer upstream.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	resp, _ := http.DefaultClient.Do(req)

	outcome := StreamAnthropicSSEToOpenAI(
		w,
		resp,
		"claude-sonnet-4-6",
		"claude-sonnet-4-6",
		"test-inconsistent-tool-calls",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Should NOT contain finish_reason:"tool_calls" (inconsistent case)
	if strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("Found inconsistent finish_reason:tool_calls in output (should be corrected to stop):\n%s", body)
	}

	// Should contain finish_reason:"stop" (corrected)
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("Expected corrected finish_reason:stop, not found in response:\n%s", body)
	}

	// Should NOT contain any tool_calls data
	if strings.Contains(body, `"tool_calls"`) {
		t.Errorf("Found tool_calls in response despite no tool_use block from upstream:\n%s", body)
	}

	// Should contain the text content
	if !strings.Contains(body, "I will generate a code graph") {
		t.Errorf("Expected text content not found in response:\n%s", body)
	}
}

// TestAnthropicToOpenAIStream_ConsistentToolCalls tests the normal case where
// Anthropic returns stop_reason:"tool_use" AND sends content_block_start(tool_use).
// This should result in finish_reason:"tool_calls" with tool_calls data.
func TestAnthropicToOpenAIStream_ConsistentToolCalls(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(anthropicSSE))
	}))
	defer upstream.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	resp, _ := http.DefaultClient.Do(req)

	outcome := StreamAnthropicSSEToOpenAI(
		w,
		resp,
		"claude-sonnet-4-6",
		"claude-sonnet-4-6",
		"test-consistent-tool-calls",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Should contain finish_reason:"tool_calls" (correct case)
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("Expected finish_reason:tool_calls, not found in response:\n%s", body)
	}

	// Should contain tool_calls data
	if !strings.Contains(body, `"tool_calls"`) {
		t.Errorf("Expected tool_calls in response, not found:\n%s", body)
	}

	// Should contain the tool name
	if !strings.Contains(body, `"name":"bash"`) {
		t.Errorf("Expected tool name 'bash' in response, not found:\n%s", body)
	}
}

// TestAnthropicToOpenAIStream_NoToolCalls tests the normal case with no tools.
// Should result in finish_reason:"stop" and no tool_calls.
func TestAnthropicToOpenAIStream_NoToolCalls(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(anthropicSSE))
	}))
	defer upstream.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	resp, _ := http.DefaultClient.Do(req)

	outcome := StreamAnthropicSSEToOpenAI(
		w,
		resp,
		"claude-sonnet-4-6",
		"claude-sonnet-4-6",
		"test-no-tool-calls",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Should contain finish_reason:"stop"
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("Expected finish_reason:stop, not found in response:\n%s", body)
	}

	// Should NOT contain tool_calls
	if strings.Contains(body, `"tool_calls"`) {
		t.Errorf("Found unexpected tool_calls in response:\n%s", body)
	}

	// Should contain the text
	if !strings.Contains(body, "Hello") {
		t.Errorf("Expected text 'Hello' not found in response:\n%s", body)
	}
}
