package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_FinishReasonMapping verifies that Anthropic
// stop_reason values are correctly mapped to OpenAI finish_reason values.
// Bug: previously used mapAnthropicStopReason (OpenAI→Anthropic) instead of
// mapAnthropicFinishReasonToChat (Anthropic→OpenAI), causing "end_turn" to
// pass through unchanged instead of being mapped to "stop".
func TestAnthropicToOpenAIStream_FinishReasonMapping(t *testing.T) {
	tests := []struct {
		name                 string
		anthropicStopReason  string
		expectedFinishReason string
	}{
		{
			name:                 "end_turn maps to stop",
			anthropicStopReason:  "end_turn",
			expectedFinishReason: "stop",
		},
		{
			name:                 "tool_use maps to tool_calls",
			anthropicStopReason:  "tool_use",
			expectedFinishReason: "tool_calls",
		},
		{
			name:                 "max_tokens maps to length",
			anthropicStopReason:  "max_tokens",
			expectedFinishReason: "length",
		},
		{
			name:                 "stop_sequence maps to stop",
			anthropicStopReason:  "stop_sequence",
			expectedFinishReason: "stop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate Anthropic SSE stream
			anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"` + tt.anthropicStopReason + `"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`
			// Create mock upstream response
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(anthropicSSE))
			}))
			defer upstream.Close()

			// Create mock downstream response recorder
			w := httptest.NewRecorder()

			// Create mock request to get upstream response
			req, _ := http.NewRequest("GET", upstream.URL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed to get upstream response: %v", err)
			}

			// Call StreamAnthropicSSEToOpenAI
			outcome := StreamAnthropicSSEToOpenAI(
				w,
				resp,
				"claude-opus-4-8",
				"claude-opus-4-8",
				"test-request-id",
				nil, // capture
				nil, // pendingCapturer
			)

			if outcome.Interrupted {
				t.Errorf("stream was interrupted: %s", outcome.Reason)
			}

			// Read response body
			body := w.Body.String()

			// Check that the finish_reason in the output is correctly mapped
			if !strings.Contains(body, `"finish_reason":"`+tt.expectedFinishReason+`"`) {
				t.Errorf("Expected finish_reason %q, but not found in response:\n%s",
					tt.expectedFinishReason, body)
			}

			// Ensure the wrong value is NOT present
			if tt.anthropicStopReason != tt.expectedFinishReason {
				if strings.Contains(body, `"finish_reason":"`+tt.anthropicStopReason+`"`) {
					t.Errorf("Found unmapped Anthropic stop_reason %q in OpenAI output:\n%s",
						tt.anthropicStopReason, body)
				}
			}
		})
	}
}

// TestAnthropicToOpenAIStream_ToolCallsFinishReason specifically tests that
// tool_use from Anthropic is correctly mapped to tool_calls in OpenAI format.
func TestAnthropicToOpenAIStream_ToolCallsFinishReason(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\":\"San Francisco\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}

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
		"claude-opus-4-8",
		"claude-opus-4-8",
		"test-request-id",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Should contain tool_calls finish_reason
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("Expected finish_reason:tool_calls, not found in response:\n%s", body)
	}

	// Should NOT contain tool_use (Anthropic format)
	if strings.Contains(body, `"finish_reason":"tool_use"`) {
		t.Errorf("Found unmapped Anthropic tool_use in OpenAI output:\n%s", body)
	}

	// Should contain tool_calls in delta
	if !strings.Contains(body, `"tool_calls"`) {
		t.Errorf("Expected tool_calls in response, not found:\n%s", body)
	}
}

// TestMapAnthropicFinishReasonToChat tests the mapping function directly
func TestMapAnthropicFinishReasonToChat(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"end_turn", "stop"},
		{"tool_use", "tool_calls"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"refusal", "content_filter"},
		{"unknown", "stop"}, // default case
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := mapAnthropicFinishReasonToChat(tt.input)
			if result != tt.expected {
				t.Errorf("mapAnthropicFinishReasonToChat(%q) = %q, want %q",
					tt.input, result, tt.expected)
			}
		})
	}
}
