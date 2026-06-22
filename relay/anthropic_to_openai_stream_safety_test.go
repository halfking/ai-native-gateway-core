package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_DualArgsSafety tests the safety mechanism that
// prevents sending arguments twice. This should NOT happen in real Anthropic
// streams, but we defend against it anyway.
//
// Scenario: What if Anthropic sends BOTH non-empty input AND input_json_delta?
// Expected: We send initial args, then IGNORE input_json_delta (no duplication).
func TestAnthropicToOpenAIStream_DualArgsSafety(t *testing.T) {
	// Edge case: both non-empty input and input_json_delta
	// (shouldn't happen, but test our defense)
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"test","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"bash","input":{"command":"ls"}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"extra\":\"ignored\"}"}}

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
		"test",
		"test",
		"test-dual-args",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Parse and collect all arguments
	lines := strings.Split(body, "\n")
	var allArgs []string

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

			if args, hasArgs := funcObj["arguments"].(string); hasArgs && args != "" {
				allArgs = append(allArgs, args)
			}
		}
	}

	// Should have exactly ONE arguments chunk (from content_block_start)
	if len(allArgs) != 1 {
		t.Errorf("Expected exactly 1 arguments chunk, got %d: %v", len(allArgs), allArgs)
	}

	// That one chunk should be the initial args, NOT the delta
	if len(allArgs) > 0 {
		finalArgs := allArgs[0]
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(finalArgs), &parsed); err != nil {
			t.Errorf("Arguments not valid JSON: %v. Args: %s", err, finalArgs)
		}

		// Should have "command":"ls" (from initial input)
		if cmd, ok := parsed["command"].(string); !ok || cmd != "ls" {
			t.Errorf("Expected command 'ls' from initial input, got: %v", parsed["command"])
		}

		// Should NOT have "extra":"ignored" (from input_json_delta)
		if _, hasExtra := parsed["extra"]; hasExtra {
			t.Errorf("Found 'extra' field from input_json_delta - safety mechanism failed! Args: %s", finalArgs)
		}
	}

	t.Logf("Safety test passed: input_json_delta was correctly ignored when initial args present")
}

// TestAnthropicToOpenAIStream_MultipleToolCalls tests multiple tool calls
// in one response to ensure our state (currentToolCallID, initialArgsSent)
// resets correctly between tool_use blocks.
func TestAnthropicToOpenAIStream_MultipleToolCalls(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"test","usage":{"input_tokens":20,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_001","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_002","name":"bash","input":{"command":"pwd"}}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

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
		"test",
		"test",
		"test-multiple-tools",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()

	// Should have two tool calls with different IDs and correct arguments
	toolCallCount := 0
	foundToolu001 := false
	foundToolu002 := false

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
			if id, hasID := tc["id"].(string); hasID {
				toolCallCount++
				if id == "toolu_001" {
					foundToolu001 = true
				}
				if id == "toolu_002" {
					foundToolu002 = true
				}
			}
		}
	}

	if !foundToolu001 {
		t.Errorf("Did not find tool call with ID toolu_001")
	}

	if !foundToolu002 {
		t.Errorf("Did not find tool call with ID toolu_002")
	}

	t.Logf("Multiple tool calls test passed: found %d tool call chunks, both IDs present", toolCallCount)
}
