package relay

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

// TestStreamCapture_E2E_LargeContext verifies that 35K+ token streams
// do not lose content. This is a regression test for bug e0f10f81 where
// response_body was truncated to 307 bytes (only the first chunk).
//
// Bug context: Request e0f10f81 had 35,356 prompt tokens and 805 completion
// tokens across 7 messages, but response_body only captured the first chunk.
// The root cause was the string-based ObservePayload API and format confusion.
//
// This test validates:
// 1. Large text content (84K+ chars, ~21K tokens) is fully captured
// 2. completion_tokens is correctly recorded
// 3. stream_chunk_count reflects all chunks
func TestStreamCapture_E2E_LargeContext(t *testing.T) {
	// Construct a large text response (~84K characters, simulating 35K+ tokens)
	largeText := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 1500)
	// 57 chars * 1500 = 85,500 chars ≈ 21,375 tokens (assuming 4 chars/token)

	var sseLines []string
	sseLines = append(sseLines, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")

	// Split into 100 chunks to simulate realistic streaming
	chunkSize := len(largeText) / 100
	for i := 0; i < len(largeText); i += chunkSize {
		end := i + chunkSize
		if end > len(largeText) {
			end = len(largeText)
		}
		chunk := largeText[i:end]
		sseLines = append(sseLines, fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk))
	}

	// Final chunk with usage and finish_reason
	sseLines = append(sseLines, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8839,\"completion_tokens\":2137}}\n\n")
	sseLines = append(sseLines, "data: [DONE]\n\n")

	// Simulate upstream response
	upstreamBody := strings.Join(sseLines, "")
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	// Convert through StreamOpenAIToAnthropicSSE (this is the path that was broken)
	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamOpenAIToAnthropicSSE(rec, resp, "claude-opus-4", "claude-opus-4", "test-large-e2e", capture, nil)

	// Validate outcome
	if outcome.Interrupted {
		t.Fatalf("stream interrupted: %s", outcome.Reason)
	}

	summary := capture.SummaryAsMap()
	textContent := summary["stream_text_content"].(string)

	// CRITICAL: Verify content is NOT truncated (bug e0f10f81 had only 307 bytes)
	if len(textContent) < len(largeText) {
		t.Errorf("REGRESSION: text content truncated like bug e0f10f81!\nGot %d bytes, expected %d bytes", len(textContent), len(largeText))
	}

	// Verify completion_tokens is recorded (bug e0f10f81 had completion_tokens=0)
	completionTokens, ok := summary["completion_tokens"].(int)
	if !ok || completionTokens != 2137 {
		t.Errorf("completion_tokens incorrect: got %v, expected 2137", summary["completion_tokens"])
	}

	// Verify chunk_count
	chunkCount, ok := summary["stream_chunk_count"].(int)
	if !ok || chunkCount < 100 {
		t.Errorf("chunk_count too low: got %v, expected ~102", summary["stream_chunk_count"])
	}

	// Verify prompt_tokens
	promptTokens, ok := summary["prompt_tokens"].(int)
	if !ok || promptTokens != 8839 {
		t.Errorf("prompt_tokens incorrect: got %v, expected 8839", summary["prompt_tokens"])
	}

	t.Logf("✅ E2E Large Context Test Passed:")
	t.Logf("   - Text content: %d bytes (complete)", len(textContent))
	t.Logf("   - Completion tokens: %d", completionTokens)
	t.Logf("   - Chunk count: %d", chunkCount)
	t.Logf("   - Prompt tokens: %d", promptTokens)
}

// TestStreamCapture_E2E_AnthropicToOpenAI verifies that Anthropic SSE
// (with thinking blocks) is correctly converted to OpenAI format and
// all content types (text, reasoning, tool_calls) are captured.
//
// This validates the Q3 conversion path (Anthropic upstream → OpenAI client).
func TestStreamCapture_E2E_AnthropicToOpenAI(t *testing.T) {
	// Simulate realistic Anthropic SSE with thinking blocks
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_e2e_test","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4","usage":{"input_tokens":256,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me analyze this problem step by step. "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"First, I need to understand the core requirements..."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Based on my analysis, the answer is 42."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":67}}

event: message_stop
data: {"type":"message_stop"}
`

	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(anthropicSSE)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(rec, resp, "minimax-m3", "MiniMax-M3", "test-think-e2e", capture, nil)

	if outcome.Interrupted {
		t.Fatalf("stream interrupted: %s", outcome.Reason)
	}

	summary := capture.SummaryAsMap()
	textContent := summary["stream_text_content"].(string)

	// Verify all content types are captured
	expectedPhrases := []string{
		"Let me analyze this problem step by step.",
		"First, I need to understand the core requirements...",
		"Based on my analysis, the answer is 42.",
	}

	for _, phrase := range expectedPhrases {
		if !strings.Contains(textContent, phrase) {
			t.Errorf("Missing expected phrase in captured content: %q", phrase)
		}
	}

	// Verify tokens
	promptTokens, ok := summary["prompt_tokens"].(int)
	if !ok || promptTokens != 256 {
		t.Errorf("prompt_tokens incorrect: got %v, expected 256", summary["prompt_tokens"])
	}

	completionTokens, ok := summary["completion_tokens"].(int)
	if !ok || completionTokens != 67 {
		t.Errorf("completion_tokens incorrect: got %v, expected 67", summary["completion_tokens"])
	}

	t.Logf("✅ E2E Anthropic→OpenAI Test Passed:")
	t.Logf("   - Content captured: %d bytes", len(textContent))
	t.Logf("   - All phrases found: ✅")
	t.Logf("   - Prompt tokens: %d", promptTokens)
	t.Logf("   - Completion tokens: %d", completionTokens)
}

// TestStreamCapture_E2E_ToolCalls verifies that tool_calls streaming
// is correctly captured. This tests the function.arguments field requirement
// (OpenAI spec: function MUST have arguments even if empty string).
func TestStreamCapture_E2E_ToolCalls(t *testing.T) {
	// Anthropic tool_use event sequence
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4","usage":{"input_tokens":100,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc123","name":"execute_command","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"ls -la"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}

event: message_stop
data: {"type":"message_stop"}
`

	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(anthropicSSE)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(rec, resp, "claude-sonnet-4", "claude-sonnet-4", "test-tool-e2e", capture, nil)

	if outcome.Interrupted {
		t.Fatalf("stream interrupted: %s", outcome.Reason)
	}

	// Parse the actual OpenAI chunks emitted
	output := rec.Body.String()
	lines := strings.Split(output, "\n")

	// Find the first tool_calls chunk and verify it has function.arguments
	foundToolCall := false
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}

		// Check if this chunk has tool_calls
		if strings.Contains(data, "tool_calls") && strings.Contains(data, "function") {
			foundToolCall = true
			// CRITICAL: function.arguments MUST be present
			if !strings.Contains(data, "\"arguments\"") {
				t.Errorf("REGRESSION: function.arguments field missing!\nChunk: %s", data)
			}
			break
		}
	}

	if !foundToolCall {
		t.Error("No tool_calls chunk found in output")
	}

	summary := capture.SummaryAsMap()
	completionTokens, ok := summary["completion_tokens"].(int)
	if !ok || completionTokens != 15 {
		t.Errorf("completion_tokens incorrect: got %v, expected 15", summary["completion_tokens"])
	}

	t.Logf("✅ E2E Tool Calls Test Passed:")
	t.Logf("   - Tool call found with function.arguments: ✅")
	t.Logf("   - Completion tokens: %d", completionTokens)
}
