package transformation

import (
	"strings"
	"testing"
)

// TestValidateAnthropicToolCallIntegrity_Orphaned tests that orphaned tool_results
// are detected in JSON body.
func TestValidateAnthropicToolCallIntegrity_Orphaned(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "Hello"},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_orphaned_123",
						"content": "Result"
					}
				]
			},
			{"role": "user", "content": "Continue"}
		]
	}`)

	err := ValidateAnthropicToolCallIntegrity(body)
	if err == nil {
		t.Fatalf("expected validation error for orphaned tool_result, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "orphaned tool_result") {
		t.Errorf("error message should mention 'orphaned tool_result', got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "toolu_orphaned_123") {
		t.Errorf("error message should include the orphaned ID, got: %v", errMsg)
	}
}

// TestValidateAnthropicToolCallIntegrity_Valid tests that valid tool chains pass.
func TestValidateAnthropicToolCallIntegrity_Valid(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "What's the weather?"},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_valid_123",
						"name": "get_weather",
						"input": {"city": "Beijing"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_valid_123",
						"content": "Sunny, 25°C"
					}
				]
			},
			{"role": "assistant", "content": "It's sunny and 25°C"}
		]
	}`)

	err := ValidateAnthropicToolCallIntegrity(body)
	if err != nil {
		t.Fatalf("ValidateAnthropicToolCallIntegrity failed on valid tool chain: %v", err)
	}
}

// TestValidateAnthropicToolCallIntegrity_MiniMaxToolCallID tests MiniMax-specific
// tool_call_id field instead of tool_use_id.
func TestValidateAnthropicToolCallIntegrity_MiniMaxToolCallID(t *testing.T) {
	body := []byte(`{
		"model": "abab6.5s-chat",
		"messages": [
			{"role": "user", "content": "Test"},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_minimax_123",
						"name": "test_tool",
						"input": {}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_call_id": "call_minimax_123",
						"content": "OK"
					}
				]
			}
		]
	}`)

	err := ValidateAnthropicToolCallIntegrity(body)
	if err != nil {
		t.Fatalf("ValidateAnthropicToolCallIntegrity failed for MiniMax tool_call_id: %v", err)
	}
}

// TestValidateAnthropicToolCallIntegrity_PartialOrphans tests mixed valid and orphaned.
func TestValidateAnthropicToolCallIntegrity_PartialOrphans(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "Test"},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_valid",
						"name": "tool1",
						"input": {}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_valid",
						"content": "OK"
					},
					{
						"type": "tool_result",
						"tool_use_id": "toolu_orphaned",
						"content": "Lost"
					}
				]
			}
		]
	}`)

	err := ValidateAnthropicToolCallIntegrity(body)
	if err == nil {
		t.Fatalf("expected validation error for partial orphans, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "toolu_orphaned") {
		t.Errorf("error should mention orphaned ID, got: %v", errMsg)
	}
}

// TestValidateAnthropicToolCallIntegrity_ShortList tests that short lists skip validation.
func TestValidateAnthropicToolCallIntegrity_ShortList(t *testing.T) {
	// Single message
	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`)

	err := ValidateAnthropicToolCallIntegrity(body)
	if err != nil {
		t.Fatalf("ValidateAnthropicToolCallIntegrity failed on short list: %v", err)
	}

	// Two messages
	body = []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi"}
		]
	}`)

	err = ValidateAnthropicToolCallIntegrity(body)
	if err != nil {
		t.Fatalf("ValidateAnthropicToolCallIntegrity failed on 2-message list: %v", err)
	}
}

// TestValidateAnthropicToolCallIntegrity_MultipleOrphans tests error message truncation.
func TestValidateAnthropicToolCallIntegrity_MultipleOrphans(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "Test"},
			{"role": "user", "content": "Message 2"},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_orphan_1", "content": "R1"},
					{"type": "tool_result", "tool_use_id": "toolu_orphan_2", "content": "R2"},
					{"type": "tool_result", "tool_use_id": "toolu_orphan_3", "content": "R3"},
					{"type": "tool_result", "tool_use_id": "toolu_orphan_4", "content": "R4"},
					{"type": "tool_result", "tool_use_id": "toolu_orphan_5", "content": "R5"}
				]
			}
		]
	}`)

	err := ValidateAnthropicToolCallIntegrity(body)
	if err == nil {
		t.Fatalf("expected validation error for multiple orphans, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "found 5 orphaned") {
		t.Errorf("error should mention total count of 5, got: %v", errMsg)
	}
	// Should limit displayed IDs to first 3
	if !strings.Contains(errMsg, "toolu_orphan_1") {
		t.Errorf("error should show first orphan, got: %v", errMsg)
	}
}

// TestValidateAnthropicToolCallIntegrity_InvalidJSON tests that malformed JSON is skipped.
func TestValidateAnthropicToolCallIntegrity_InvalidJSON(t *testing.T) {
	body := []byte(`not valid json`)

	err := ValidateAnthropicToolCallIntegrity(body)
	if err != nil {
		t.Fatalf("ValidateAnthropicToolCallIntegrity should skip invalid JSON, got: %v", err)
	}
}
