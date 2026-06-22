package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_EmptyInputThenDelta tests the case where
// Anthropic sends input:{} at content_block_start, then sends the actual
// arguments via input_json_delta. This was causing clients to see
// "{}{"command":"ls"}" - concatenating empty object with real arguments.
//
// Expected behavior:
// 1. content_block_start with input:{} should emit tool call with ID+name only (no arguments)
// 2. input_json_delta should emit arguments
// 3. Client should NOT see "{}{...}" concatenation
func TestAnthropicToOpenAIStream_EmptyInputThenDelta(t *testing.T) {
	// Realistic Anthropic SSE stream:
	// 1. content_block_start with empty input:{}
	// 2. Multiple input_json_delta events with real arguments
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":50,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_xyz789","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls -la\""}}

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
		"test-empty-input-delta",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Parse and analyze tool_calls chunks
	lines := strings.Split(body, "\n")
	var allToolCallArgs []string

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
			funcRaw, hasFunc := tc["function"]
			if !hasFunc {
				continue
			}

			funcJSON, _ := json.Marshal(funcRaw)
			var funcObj map[string]interface{}
			if err := json.Unmarshal(funcJSON, &funcObj); err != nil {
				continue
			}

			if args, hasArgs := funcObj["arguments"].(string); hasArgs {
				allToolCallArgs = append(allToolCallArgs, args)
			}
		}
	}

	// Critical check: Should NOT find "{}" as an argument
	for i, args := range allToolCallArgs {
		if args == "{}" {
			t.Errorf("Tool call chunk #%d contains empty object '{}': this will cause concatenation issues", i)
		}
		// Also check for "{}{..." pattern (concatenated empty + real)
		if strings.HasPrefix(args, "{}") {
			t.Errorf("Tool call chunk #%d has concatenated empty object: %q", i, args)
		}
	}

	// Should have at least one arguments chunk with the real command
	if len(allToolCallArgs) == 0 {
		t.Errorf("Expected at least one tool call arguments chunk, got none. Body:\n%s", body)
	}

	// Concatenate all arguments to get the final result
	finalArgs := strings.Join(allToolCallArgs, "")
	t.Logf("Final concatenated arguments: %s", finalArgs)

	// Verify it's valid JSON
	var parsedArgs map[string]interface{}
	if err := json.Unmarshal([]byte(finalArgs), &parsedArgs); err != nil {
		t.Errorf("Final arguments are not valid JSON: %v. Args: %s", err, finalArgs)
	}

	// Verify the command is correct
	if cmd, ok := parsedArgs["command"].(string); !ok || cmd != "ls -la" {
		t.Errorf("Expected command 'ls -la', got: %v", parsedArgs["command"])
	}
}

// TestAnthropicToOpenAIStream_NonEmptyInput tests the case where
// content_block_start already contains complete arguments (not empty).
// This should work as before.
func TestAnthropicToOpenAIStream_NonEmptyInput(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"claude-3","usage":{"input_tokens":20,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{"location":"SF"}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

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
		"claude-3",
		"claude-3",
		"test-non-empty-input",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Should contain the complete arguments in the first chunk
	if !strings.Contains(body, `"arguments":"{\"location\":\"SF\"}"`) {
		t.Errorf("Expected complete arguments in response, not found. Body:\n%s", body)
	}

	// Should NOT contain empty {} arguments
	if strings.Contains(body, `"arguments":"{}"`) {
		t.Errorf("Found unexpected empty arguments. Body:\n%s", body)
	}
}
