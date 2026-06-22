package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_InputJsonDelta tests that input_json_delta events
// are properly accumulated and emitted as tool_calls arguments.
// This addresses the issue where all tool_calls had empty arguments ("{}").
func TestAnthropicToOpenAIStream_InputJsonDelta(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls -la\"}"}}

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
		"test-input-json-delta",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Should contain finish_reason:"tool_calls"
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("Expected finish_reason:tool_calls, not found in response:\n%s", body)
	}

	// Should contain tool name
	if !strings.Contains(body, `"name":"bash"`) {
		t.Errorf("Expected tool name 'bash', not found in response:\n%s", body)
	}

	// Should contain the actual command arguments (not empty {})
	if !strings.Contains(body, `"command":"ls -la"`) && !strings.Contains(body, `"command\":\"ls -la\"`) {
		t.Errorf("Expected arguments with command 'ls -la', not found in response:\n%s", body)
	}

	// Should NOT have empty arguments
	if strings.Count(body, `"arguments":"{}"`) > 1 {
		t.Errorf("Found multiple empty arguments (should have actual command), response:\n%s", body)
	}
}
