package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateToolCallIntegrity_JSONUnmarshal tests that the type switch handles
// []interface{} from json.Unmarshal correctly (Bug C fix verification).
func TestValidateToolCallIntegrity_JSONUnmarshal(t *testing.T) {
	// Construct messages via JSON unmarshal to get []interface{} types
	jsonData := []byte(`[
		{"role": "user", "content": "Test"},
		{
			"role": "assistant",
			"content": "Using tool",
			"tool_calls": [
				{"id": "call_123", "type": "function", "function": {"name": "test"}}
			]
		},
		{"role": "tool", "tool_call_id": "call_123", "content": "OK"},
		{"role": "tool", "tool_call_id": "call_orphan", "content": "Lost"}
	]`)

	var messages []map[string]any
	if err := json.Unmarshal(jsonData, &messages); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Validate - should detect the orphaned tool_call_id
	err := validateToolCallIntegrity(messages)
	if err == nil {
		t.Fatalf("expected validation error for orphaned tool after JSON unmarshal, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "call_orphan") {
		t.Errorf("error should mention orphaned ID call_orphan, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "orphaned tool result") {
		t.Errorf("error should mention 'orphaned tool result', got: %v", errMsg)
	}
}

// TestValidateToolCallIntegrity_JSONUnmarshalValid tests valid tool chain via JSON.
func TestValidateToolCallIntegrity_JSONUnmarshalValid(t *testing.T) {
	jsonData := []byte(`[
		{"role": "user", "content": "Test"},
		{
			"role": "assistant",
			"content": "Using tool",
			"tool_calls": [
				{"id": "call_valid", "type": "function", "function": {"name": "test"}}
			]
		},
		{"role": "tool", "tool_call_id": "call_valid", "content": "OK"}
	]`)

	var messages []map[string]any
	if err := json.Unmarshal(jsonData, &messages); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	err := validateToolCallIntegrity(messages)
	if err != nil {
		t.Fatalf("validateToolCallIntegrity should pass for valid JSON chain, got: %v", err)
	}
}

// TestValidateToolCallIntegrity_MultipleToolCallsJSON tests multiple tool_calls via JSON.
func TestValidateToolCallIntegrity_MultipleToolCallsJSON(t *testing.T) {
	jsonData := []byte(`[
		{"role": "user", "content": "Test"},
		{
			"role": "assistant",
			"content": "Using tools",
			"tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "tool1"}},
				{"id": "call_2", "type": "function", "function": {"name": "tool2"}}
			]
		},
		{"role": "tool", "tool_call_id": "call_1", "content": "Result 1"},
		{"role": "tool", "tool_call_id": "call_2", "content": "Result 2"},
		{"role": "tool", "tool_call_id": "call_missing", "content": "Orphan"}
	]`)

	var messages []map[string]any
	if err := json.Unmarshal(jsonData, &messages); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	err := validateToolCallIntegrity(messages)
	if err == nil {
		t.Fatalf("expected validation error for orphaned tool_call_id, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "call_missing") {
		t.Errorf("error should mention call_missing, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "found 1 orphaned") {
		t.Errorf("error should mention count of 1, got: %v", errMsg)
	}
}

// TestValidateToolCallIntegrity_GoCodeConstructed tests that Go-constructed
// []map[string]any still works (backward compatibility).
func TestValidateToolCallIntegrity_GoCodeConstructed(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Test"},
		{
			"role": "assistant",
			"content": "Using tool",
			"tool_calls": []map[string]any{
				{"id": "call_go", "type": "function"},
			},
		},
		{"role": "tool", "tool_call_id": "call_go", "content": "OK"},
	}

	err := validateToolCallIntegrity(messages)
	if err != nil {
		t.Fatalf("validateToolCallIntegrity should pass for Go-constructed messages, got: %v", err)
	}
}

// TestValidateToolCallIntegrity_GoCodeOrphaned tests orphan detection with Go-constructed data.
func TestValidateToolCallIntegrity_GoCodeOrphaned(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Test"},
		{
			"role": "assistant",
			"content": "Using tool",
			"tool_calls": []map[string]any{
				{"id": "call_go", "type": "function"},
			},
		},
		{"role": "tool", "tool_call_id": "call_go", "content": "OK"},
		{"role": "tool", "tool_call_id": "call_go_orphan", "content": "Lost"},
	}

	err := validateToolCallIntegrity(messages)
	if err == nil {
		t.Fatalf("expected validation error for orphaned tool in Go-constructed data, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "call_go_orphan") {
		t.Errorf("error should mention call_go_orphan, got: %v", errMsg)
	}
}
