package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_FirstChunkHasArgumentsField tests that the
// first tool_calls chunk (the one with ID+name) MUST have the arguments field,
// even if it's an empty string. This is required by OpenAI streaming spec.
//
// User reported error: "Expected 'function.name' to be a string"
// Actual cause: arguments field was missing when name was present.
func TestAnthropicToOpenAIStream_FirstChunkHasArgumentsField(t *testing.T) {
	// Anthropic sends input:{} at start, then input_json_delta
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}

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
		"claude-sonnet-4-6",
		"claude-sonnet-4-6",
		"test-first-chunk-args",
		nil,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream was interrupted: %s", outcome.Reason)
	}

	body := w.Body.String()
	lines := strings.Split(body, "\n")

	// Find the first tool_calls chunk (should have id, type, name, and arguments)
	var firstChunk map[string]interface{}
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

		// Found first tool_calls chunk
		toolCallsJSON, _ := json.Marshal(toolCallsRaw)
		var toolCalls []map[string]interface{}
		if err := json.Unmarshal(toolCallsJSON, &toolCalls); err != nil {
			t.Fatalf("Failed to parse tool_calls: %v", err)
		}

		if len(toolCalls) > 0 {
			firstChunk = toolCalls[0]
			break
		}
	}

	if firstChunk == nil {
		t.Fatalf("No tool_calls chunk found in response")
	}

	// CRITICAL checks for the first chunk
	t.Logf("First chunk: %+v", firstChunk)

	// 1. Must have id
	id, hasID := firstChunk["id"].(string)
	if !hasID || id == "" {
		t.Errorf("First chunk missing 'id' field or id is empty")
	}

	// 2. Must have type
	typ, hasType := firstChunk["type"].(string)
	if !hasType || typ != "function" {
		t.Errorf("First chunk missing 'type' or type != 'function', got: %v", typ)
	}

	// 3. Must have function object
	funcRaw, hasFunc := firstChunk["function"]
	if !hasFunc {
		t.Fatalf("First chunk missing 'function' field")
	}

	funcJSON, _ := json.Marshal(funcRaw)
	var funcObj map[string]interface{}
	if err := json.Unmarshal(funcJSON, &funcObj); err != nil {
		t.Fatalf("Failed to parse function object: %v", err)
	}

	// 4. CRITICAL: function must have name
	name, hasName := funcObj["name"].(string)
	if !hasName || name == "" {
		t.Errorf("function object missing 'name' field or name is empty")
	}

	// 5. CRITICAL: function MUST have arguments (even if empty string)
	// This is what was causing "Expected 'function.name' to be a string" error
	args, hasArgs := funcObj["arguments"]
	if !hasArgs {
		t.Errorf("CRITICAL: function object missing 'arguments' field - this causes client validation error")
	} else {
		// arguments should be a string (even if empty "")
		argsStr, isString := args.(string)
		if !isString {
			t.Errorf("arguments is not a string, got type: %T, value: %v", args, args)
		} else {
			t.Logf("First chunk arguments field: %q (length: %d)", argsStr, len(argsStr))
			// Empty string is OK for first chunk
			if argsStr != "" && argsStr != "{\"command\":\"ls\"}" {
				t.Logf("Note: arguments is %q", argsStr)
			}
		}
	}
}
