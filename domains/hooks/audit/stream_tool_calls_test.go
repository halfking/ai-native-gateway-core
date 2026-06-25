package audit

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestObserveChunk_ToolCalls_SingleChunk tests tool call capture from a single chunk
// (first chunk with id, name, and complete arguments).
func TestObserveChunk_ToolCalls_SingleChunk(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     0,
					ID:        "call_abc123",
					Type:      "function",
					Name:      "get_weather",
					Arguments: `{"location":"San Francisco"}`,
				},
			},
		},
		SourceProtocol: ir.ProtocolAnthropicMessages,
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()
	toolCallsRaw, ok := summary["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls not found in summary")
	}
	if len(toolCallsRaw) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCallsRaw))
	}

	tc := toolCallsRaw[0]
	if tc["id"] != "call_abc123" {
		t.Errorf("id = %v, want call_abc123", tc["id"])
	}
	if tc["type"] != "function" {
		t.Errorf("type = %v, want function", tc["type"])
	}

	fn, ok := tc["function"].(map[string]any)
	if !ok {
		t.Fatalf("function not found")
	}
	if fn["name"] != "get_weather" {
		t.Errorf("function.name = %v, want get_weather", fn["name"])
	}
	if fn["arguments"] != `{"location":"San Francisco"}` {
		t.Errorf("function.arguments = %v, want complete JSON", fn["arguments"])
	}
}

// TestObserveChunk_ToolCalls_IncrementalArguments tests incremental argument accumulation
// across multiple chunks (OpenAI streaming format).
func TestObserveChunk_ToolCalls_IncrementalArguments(t *testing.T) {
	capture := NewStreamCapture()

	// First chunk: id, name, empty arguments
	chunk1 := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     0,
					ID:        "call_xyz789",
					Type:      "function",
					Name:      "calculate_sum",
					Arguments: "",
				},
			},
		},
		SourceProtocol: ir.ProtocolOpenAIChat,
	}
	capture.ObserveChunk(chunk1)

	// Second chunk: delta arguments (partial JSON)
	chunk2 := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     0,
					Arguments: `{"a":`,
				},
			},
		},
		SourceProtocol: ir.ProtocolOpenAIChat,
	}
	capture.ObserveChunk(chunk2)

	// Third chunk: more delta arguments
	chunk3 := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     0,
					Arguments: `10,"b":20}`,
				},
			},
		},
		SourceProtocol: ir.ProtocolOpenAIChat,
	}
	capture.ObserveChunk(chunk3)

	summary := capture.SummaryAsMap()
	toolCallsRaw, ok := summary["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls not found in summary")
	}
	if len(toolCallsRaw) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCallsRaw))
	}

	tc := toolCallsRaw[0]
	fn, ok := tc["function"].(map[string]any)
	if !ok {
		t.Fatalf("function not found")
	}
	if fn["name"] != "calculate_sum" {
		t.Errorf("function.name = %v, want calculate_sum", fn["name"])
	}

	// Arguments should be concatenated
	expectedArgs := `{"a":10,"b":20}`
	if fn["arguments"] != expectedArgs {
		t.Errorf("function.arguments = %v, want %v", fn["arguments"], expectedArgs)
	}

	// Verify it's valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &parsed); err != nil {
		t.Errorf("accumulated arguments are not valid JSON: %v", err)
	}
}

// TestObserveChunk_ToolCalls_MultipleTools tests multiple tool calls in a single response.
func TestObserveChunk_ToolCalls_MultipleTools(t *testing.T) {
	capture := NewStreamCapture()

	// First tool call (index 0)
	chunk1 := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     0,
					ID:        "call_001",
					Type:      "function",
					Name:      "tool_one",
					Arguments: `{"arg1":"value1"}`,
				},
			},
		},
		SourceProtocol: ir.ProtocolOpenAIChat,
	}
	capture.ObserveChunk(chunk1)

	// Second tool call (index 1)
	chunk2 := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     1,
					ID:        "call_002",
					Type:      "function",
					Name:      "tool_two",
					Arguments: `{"arg2":"value2"}`,
				},
			},
		},
		SourceProtocol: ir.ProtocolOpenAIChat,
	}
	capture.ObserveChunk(chunk2)

	summary := capture.SummaryAsMap()
	toolCallsRaw, ok := summary["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls not found in summary")
	}
	if len(toolCallsRaw) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCallsRaw))
	}

	// Verify first tool call
	tc1 := toolCallsRaw[0]
	if tc1["id"] != "call_001" {
		t.Errorf("tool[0].id = %v, want call_001", tc1["id"])
	}
	fn1, _ := tc1["function"].(map[string]any)
	if fn1["name"] != "tool_one" {
		t.Errorf("tool[0].function.name = %v, want tool_one", fn1["name"])
	}

	// Verify second tool call
	tc2 := toolCallsRaw[1]
	if tc2["id"] != "call_002" {
		t.Errorf("tool[1].id = %v, want call_002", tc2["id"])
	}
	fn2, _ := tc2["function"].(map[string]any)
	if fn2["name"] != "tool_two" {
		t.Errorf("tool[1].function.name = %v, want tool_two", fn2["name"])
	}
}

// TestObserveChunk_ToolCalls_EmptyResponse tests that empty responses don't create tool_calls.
func TestObserveChunk_ToolCalls_EmptyResponse(t *testing.T) {
	capture := NewStreamCapture()

	// Content-only chunk (no tool calls)
	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			Content: "Hello, world!",
		},
		SourceProtocol: ir.ProtocolOpenAIChat,
	}
	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()
	_, ok := summary["tool_calls"]
	if ok {
		t.Errorf("tool_calls should not be present when no tool calls were observed")
	}
}

// TestObserveChunk_ToolCalls_JSONMarshaling tests that tool_calls can be marshaled to JSON
// (for persistence in request_logs.tool_calls JSONB column).
func TestObserveChunk_ToolCalls_JSONMarshaling(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     0,
					ID:        "call_test",
					Type:      "function",
					Name:      "test_function",
					Arguments: `{"key":"value"}`,
				},
			},
		},
		SourceProtocol: ir.ProtocolOpenAIChat,
	}
	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()
	toolCallsRaw, ok := summary["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls not found")
	}

	// Marshal to JSON (simulates what relay/handler.go does)
	jsonBytes, err := json.Marshal(toolCallsRaw)
	if err != nil {
		t.Fatalf("failed to marshal tool_calls to JSON: %v", err)
	}

	// Unmarshal back to verify structure
	var unmarshaled []map[string]any
	if err := json.Unmarshal(jsonBytes, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal tool_calls JSON: %v", err)
	}

	if len(unmarshaled) != 1 {
		t.Fatalf("expected 1 tool call after unmarshal, got %d", len(unmarshaled))
	}

	tc := unmarshaled[0]
	if tc["id"] != "call_test" {
		t.Errorf("id = %v, want call_test", tc["id"])
	}
}
