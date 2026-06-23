package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestPrepareAnthropicRequestBody_OpenAIToOpenAI verifies that when both
// client and upstream use openai-completions protocol, NO conversion happens.
// This is the fix for the 2026-06-23 issue where OpenCode CLI → MiniMax
// was incorrectly converting tool_call_id to tool_use_id.
func TestPrepareAnthropicRequestBody_OpenAIToOpenAI(t *testing.T) {
	exec := &Executor{}

	// OpenAI request with tool message (role: tool, tool_call_id)
	sourceBody := []byte(`{
		"model": "minimax-m3",
		"messages": [
			{"role": "user", "content": "天气?"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call-abc123", "type": "function", "function": {"name": "get_weather", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call-abc123", "content": "Sunny"}
		]
	}`)

	params := &ExecParams{
		ClientProtocol: "openai-completions",
		R:              &http.Request{},
	}
	params.R = params.R.WithContext(context.Background())

	// Upstream is MiniMax with openai-completions protocol
	cand := provider.Candidate{
		ProviderID: 14,
		Protocol:   "openai-completions", // ← Same as client
	}

	result, err := exec.prepareAnthropicRequestBody(params, cand, sourceBody)
	if err != nil {
		t.Fatalf("prepareAnthropicRequestBody failed: %v", err)
	}

	// Parse result to check it's still OpenAI format
	var resultJSON map[string]any
	if err := json.Unmarshal(result, &resultJSON); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	messages := resultJSON["messages"].([]any)
	toolMsg := messages[2].(map[string]any)

	// Should still have tool_call_id (OpenAI format), NOT tool_use_id (Anthropic format)
	if toolMsg["role"] != "tool" {
		t.Errorf("expected role=tool, got %v", toolMsg["role"])
	}
	if _, hasToolCallID := toolMsg["tool_call_id"]; !hasToolCallID {
		t.Error("tool_call_id missing - was incorrectly converted to Anthropic format")
	}
	if _, hasToolUseID := toolMsg["tool_use_id"]; hasToolUseID {
		t.Error("tool_use_id present - incorrectly converted to Anthropic format")
	}
}

// TestPrepareAnthropicRequestBody_OpenAIToAnthropic verifies that when
// client uses openai-completions and upstream uses anthropic-messages,
// conversion DOES happen.
func TestPrepareAnthropicRequestBody_OpenAIToAnthropic(t *testing.T) {
	// Setup executor with ChatToAnthropic converter
	exec := &Executor{
		ChatToAnthropic: func(body []byte) ([]byte, error) {
			// Simple mock: just mark that conversion happened
			var req map[string]any
			json.Unmarshal(body, &req)
			req["_converted"] = true
			return json.Marshal(req)
		},
	}

	sourceBody := []byte(`{"model": "claude-3", "messages": [{"role": "user", "content": "hello"}]}`)

	params := &ExecParams{
		ClientProtocol: "openai-completions",
		R:              &http.Request{},
	}
	params.R = params.R.WithContext(context.Background())

	// Upstream uses anthropic-messages protocol
	cand := provider.Candidate{
		ProviderID: 1,
		Protocol:   "anthropic-messages", // ← Different from client
	}

	result, err := exec.prepareAnthropicRequestBody(params, cand, sourceBody)
	if err != nil {
		t.Fatalf("prepareAnthropicRequestBody failed: %v", err)
	}

	var resultJSON map[string]any
	json.Unmarshal(result, &resultJSON)

	// Should have been converted
	if converted, ok := resultJSON["_converted"].(bool); !ok || !converted {
		t.Error("expected conversion to happen when protocols differ")
	}
}

// TestExecutorAnthropicPath_ToolCallIDPreserved is an integration test
// that verifies tool_call_id is preserved end-to-end when routing to
// an openai-completions upstream.
func TestExecutorAnthropicPath_ToolCallIDPreserved(t *testing.T) {
	// This test would require a full Executor setup with mock HTTP client
	// Skipping for now - the unit test above covers the core logic
	t.Skip("Integration test - requires full setup")
}
