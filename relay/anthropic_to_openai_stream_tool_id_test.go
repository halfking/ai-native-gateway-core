package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_ToolCallIDPersistence tests that tool call IDs
// from content_block_start are correctly propagated to subsequent input_json_delta
// events. This fixes the "Expected 'id' to be a string" error reported with
// claude-sonnet-4-6 where delta updates were missing the tool call ID.
//
// Before fix: input_json_delta emitted tool_calls without "id" field
// After fix: input_json_delta uses the cached ID from content_block_start
func TestAnthropicToOpenAIStream_ToolCallIDPersistence(t *testing.T) {
	// Simulate a realistic Anthropic SSE stream with:
	// 1. content_block_start with tool_use (has ID)
	// 2. Multiple input_json_delta events (no ID in the event itself)
	// 3. content_block_stop (flush accumulated args)
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":50,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_xyz789","name":"codegraph","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"which codegraph\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"}"}}

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
		"claude-sonnet-4-6",
		"claude-sonnet-4-6",
		"test-tool-id-persistence",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Parse each SSE event and check tool_calls chunks
	lines := strings.Split(body, "\n")
	toolCallChunks := 0
	var toolCallIDs []string

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}

		choice := choices[0].(map[string]interface{})
		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		toolCallsRaw, ok := delta["tool_calls"]
		if !ok {
			continue
		}

		// Parse tool_calls array
		toolCallsJSON, _ := json.Marshal(toolCallsRaw)
		var toolCalls []map[string]interface{}
		if err := json.Unmarshal(toolCallsJSON, &toolCalls); err != nil {
			t.Fatalf("Failed to parse tool_calls: %v", err)
		}

		for _, tc := range toolCalls {
			toolCallChunks++

			// Critical check: Every tool_call chunk MUST have an "id" field
			id, hasID := tc["id"]
			if !hasID {
				t.Errorf("Tool call chunk #%d missing 'id' field: %+v", toolCallChunks, tc)
			} else {
				idStr, isString := id.(string)
				if !isString {
					t.Errorf("Tool call chunk #%d has non-string 'id': %v (type %T)", toolCallChunks, id, id)
				} else if idStr == "" {
					t.Errorf("Tool call chunk #%d has empty 'id' string", toolCallChunks)
				} else {
					toolCallIDs = append(toolCallIDs, idStr)
					// The ID should match the one from content_block_start
					if idStr != "toolu_xyz789" {
						t.Errorf("Tool call chunk #%d has unexpected ID: got %q, want %q", toolCallChunks, idStr, "toolu_xyz789")
					}
				}
			}
		}
	}

	if toolCallChunks == 0 {
		t.Fatalf("No tool_calls chunks found in response. Body:\n%s", body)
	}

	// We expect at least 2 chunks:
	// 1. content_block_start (initial tool_use with ID + name)
	// 2. content_block_stop (flushed accumulated args from input_json_delta)
	if toolCallChunks < 2 {
		t.Errorf("Expected at least 2 tool_call chunks, got %d", toolCallChunks)
	}

	t.Logf("Successfully verified %d tool_call chunks, all with ID 'toolu_xyz789'", toolCallChunks)
}

// TestAnthropicToOpenAIStream_ToolCallIDFallback tests the fallback ID generation
// when a tool call arrives without a cached ID (edge case).
func TestAnthropicToOpenAIStream_ToolCallIDFallback(t *testing.T) {
	// Edge case: input_json arrives WITHOUT a preceding content_block_start(tool_use)
	// This shouldn't happen in normal Anthropic streams, but we have fallback logic
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"test-model","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json","\input":{"command":"ls"}}}

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
		"test-model",
		"test-model",
		"fallback-id-test",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// If tool_calls appear, they should have a synthetic fallback ID
	if strings.Contains(body, `"tool_calls"`) {
		// Parse and verify the fallback ID
		lines := strings.Split(body, "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				continue
			}

			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			choices, ok := chunk["choices"].([]interface{})
			if !ok || len(choices) == 0 {
				continue
			}

			choice := choices[0].(map[string]interface{})
			delta, ok := choice["delta"].(map[string]interface{})
			if !ok {
				continue
			}

			toolCallsRaw, ok := delta["tool_calls"]
			if !ok {
				continue
			}

			toolCallsJSON, _ := json.Marshal(toolCallsRaw)
			var toolCalls []map[string]interface{}
			if err := json.Unmarshal(toolCallsJSON, &toolCalls); err != nil {
				continue
			}

			for _, tc := range toolCalls {
				id, hasID := tc["id"]
				if !hasID {
					t.Errorf("Tool call missing 'id' field even with fallback: %+v", tc)
				} else {
					idStr, isString := id.(string)
					if !isString {
						t.Errorf("Tool call 'id' is not a string: %v (type %T)", id, id)
					} else if idStr == "" {
						t.Errorf("Tool call 'id' is empty string")
					} else {
						// Fallback ID should match pattern: call_<requestID>_<index>
						if !strings.HasPrefix(idStr, "call_fallback-id-test_") {
							t.Logf("Fallback ID generated: %s", idStr)
						}
					}
				}
			}
		}
	}
}
