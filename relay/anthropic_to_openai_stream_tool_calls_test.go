package relay

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

// TestStreamAnthropicSSEToOpenAI_ToolCalls_Complete tests end-to-end tool call capture
// from Anthropic SSE format to OpenAI format, verifying that tool_calls are captured
// in the audit.StreamCapture and can be persisted to the database.
//
// This test simulates the real-world scenario reported in the bug:
// - Anthropic upstream returns tool_use content blocks via SSE
// - StreamAnthropicSSEToOpenAI converts them to OpenAI tool_calls format
// - audit.StreamCapture.ObserveChunk accumulates the structured tool_calls
// - The tool_calls are available in SummaryAsMap for persistence
func TestStreamAnthropicSSEToOpenAI_ToolCalls_Complete(t *testing.T) {
	// Simulate Anthropic SSE response with a tool call
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test123","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":100,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc123","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\":\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"San Francisco\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":","}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"unit\":\"celsius\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":50}}

event: message_stop
data: {"type":"message_stop"}

`

	// Create mock response
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(anthropicSSE)),
	}

	// Create audit capture
	capture := audit.NewStreamCapture()

	// Create response writer (buffer to capture output)
	writer := &testResponseWriter{}

	// Execute the stream conversion
	outcome := StreamAnthropicSSEToOpenAI(
		writer,
		resp,
		"claude-3-5-sonnet-20241022", // clientModel
		"claude-3-5-sonnet-20241022", // outboundModel
		"test-request-id",
		capture,
		nil, // pendingCapturer
	)

	// Verify stream completed successfully
	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}
	if outcome.ChunkCount == 0 {
		t.Fatal("no chunks were emitted")
	}

	// Verify audit capture contains tool_calls
	summary := capture.SummaryAsMap()

	toolCallsRaw, ok := summary["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls not found in audit summary. Summary keys: %v", getKeys(summary))
	}

	if len(toolCallsRaw) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCallsRaw))
	}

	// Verify tool call structure
	tc := toolCallsRaw[0]

	if tc["id"] != "toolu_abc123" {
		t.Errorf("tool_call.id = %v, want toolu_abc123", tc["id"])
	}

	if tc["type"] != "function" {
		t.Errorf("tool_call.type = %v, want function", tc["type"])
	}

	fn, ok := tc["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool_call.function not found or wrong type")
	}

	if fn["name"] != "get_weather" {
		t.Errorf("tool_call.function.name = %v, want get_weather", fn["name"])
	}

	// Verify arguments were accumulated correctly
	expectedArgs := `{"location":"San Francisco","unit":"celsius"}`
	if fn["arguments"] != expectedArgs {
		t.Errorf("tool_call.function.arguments = %v, want %v", fn["arguments"], expectedArgs)
	}

	// Verify finish_reason is tool_calls
	finishReason, ok := summary["upstream_finish_reason"].(string)
	if !ok || finishReason != "tool_calls" {
		t.Errorf("upstream_finish_reason = %v, want tool_calls", finishReason)
	}

	// Verify OpenAI output contains tool_calls
	output := writer.buf.String()
	t.Logf("OpenAI output:\n%s", output) // Debug output
	if !strings.Contains(output, `"tool_calls"`) {
		t.Error("OpenAI output does not contain tool_calls field")
	}
	if !strings.Contains(output, `"get_weather"`) {
		t.Error("OpenAI output does not contain tool name")
	}
	if !strings.Contains(output, `"location"`) && !strings.Contains(output, `San Francisco`) {
		t.Error("OpenAI output does not contain tool arguments")
	}
}

// testResponseWriter implements http.ResponseWriter for testing
type testResponseWriter struct {
	buf         bytes.Buffer
	header      http.Header
	wroteHeader bool
	statusCode  int
}

func (w *testResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *testResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.buf.Write(b)
}

func (w *testResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.statusCode = statusCode
	w.wroteHeader = true
}

func (w *testResponseWriter) Flush() {
	// No-op for testing
}

// getKeys returns the keys of a map for debugging
func getKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
