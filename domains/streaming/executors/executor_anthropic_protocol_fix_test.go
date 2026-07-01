package executors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
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

// TestAnthropicExecutor_StreamResponse_RoutesToResponsesTranslator
// verifies Phase E (2026-07-01): when ClientProtocol == "openai-responses",
// StreamResponse dispatches to ResponsesTranslator instead of
// OpenAITranslator. Mirrors the bug where ZCode UI /v1/responses clients
// were receiving chat.completion.chunk (the OpenAI Translator path) and
// failing Responses API schema validation.
func TestAnthropicExecutor_StreamResponse_RoutesToResponsesTranslator(t *testing.T) {
	var openAICalls, responsesCalls, passthroughCalls int

	ae := &AnthropicExecutor{
		ClientProtocol: "openai-responses",
		OpenAITranslator: func(w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			openAICalls++
			return StreamOutcome{}
		},
		ResponsesTranslator: func(w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			responsesCalls++
			return StreamOutcome{}
		},
		PassthroughStream: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			passthroughCalls++
			return StreamOutcome{}
		},
	}

	out := ae.StreamResponse(httptest.NewRecorder(), &http.Response{})
	_ = out

	if responsesCalls != 1 {
		t.Errorf("ResponsesTranslator should be called once for openai-responses client, got %d", responsesCalls)
	}
	if openAICalls != 0 {
		t.Errorf("OpenAITranslator should NOT be called for openai-responses client, got %d", openAICalls)
	}
	if passthroughCalls != 0 {
		t.Errorf("PassthroughStream should NOT be called when translator is wired, got %d", passthroughCalls)
	}
}

// TestAnthropicExecutor_StreamResponse_RoutesToOpenAITranslator
// verifies the Q3 path (openai-completions client) still routes to
// OpenAITranslator, not ResponsesTranslator. Regression guard for
// the Phase E refactor.
func TestAnthropicExecutor_StreamResponse_RoutesToOpenAITranslator(t *testing.T) {
	var openAICalls, responsesCalls int

	ae := &AnthropicExecutor{
		ClientProtocol: "openai-completions",
		OpenAITranslator: func(w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			openAICalls++
			return StreamOutcome{}
		},
		ResponsesTranslator: func(w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			responsesCalls++
			return StreamOutcome{}
		},
	}

	_ = ae.StreamResponse(httptest.NewRecorder(), &http.Response{})

	if openAICalls != 1 {
		t.Errorf("OpenAITranslator should be called for openai-completions client, got %d", openAICalls)
	}
	if responsesCalls != 0 {
		t.Errorf("ResponsesTranslator should NOT be called for openai-completions client, got %d", responsesCalls)
	}
}

// TestAnthropicExecutor_StreamResponse_FallsBackWhenResponsesTranslatorMissing
// verifies defensive fallback: if ResponsesTranslator is not wired (e.g.
// during a partial rollout or in a unit test), the executor falls back
// to PassthroughStream rather than crashing.
func TestAnthropicExecutor_StreamResponse_FallsBackWhenResponsesTranslatorMissing(t *testing.T) {
	var passthroughCalls int

	ae := &AnthropicExecutor{
		ClientProtocol: "openai-responses",
		// ResponsesTranslator intentionally nil
		PassthroughStream: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			passthroughCalls++
			return StreamOutcome{}
		},
	}

	out := ae.StreamResponse(httptest.NewRecorder(), &http.Response{})
	if passthroughCalls != 1 {
		t.Errorf("expected PassthroughStream fallback when ResponsesTranslator is nil, got %d", passthroughCalls)
	}
	// Passthrough fallback means the client receives raw Anthropic SSE —
	// which is malformed for a Responses API client. This branch should
	// only fire in misconfig scenarios; production must wire the
	// translator in main.go.
	_ = out
}

// TestAnthropicExecutor_StreamResponse_RoutesQ4Passthrough
// verifies the Q4 path (anthropic client → anthropic upstream) still
// routes to PassthroughStream. Regression guard.
func TestAnthropicExecutor_StreamResponse_RoutesQ4Passthrough(t *testing.T) {
	var passthroughCalls, openAICalls, responsesCalls int

	ae := &AnthropicExecutor{
		ClientProtocol: "anthropic-messages",
		OpenAITranslator: func(w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			openAICalls++
			return StreamOutcome{}
		},
		ResponsesTranslator: func(w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			responsesCalls++
			return StreamOutcome{}
		},
		PassthroughStream: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			passthroughCalls++
			return StreamOutcome{}
		},
	}

	_ = ae.StreamResponse(httptest.NewRecorder(), &http.Response{})

	if passthroughCalls != 1 {
		t.Errorf("PassthroughStream should be called for anthropic-messages client, got %d", passthroughCalls)
	}
	if openAICalls != 0 {
		t.Errorf("OpenAITranslator should NOT be called for anthropic-messages client, got %d", openAICalls)
	}
	if responsesCalls != 0 {
		t.Errorf("ResponsesTranslator should NOT be called for anthropic-messages client, got %d", responsesCalls)
	}
}
