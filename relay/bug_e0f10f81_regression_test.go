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

// TestBugE0f10f81_Regression is a direct regression test for the original bug.
//
// Bug Report:
// - Request ID: e0f10f81-10f7-4c76-9d82-b58cc1cc7b4b
// - Scenario: 35,356 prompt tokens, 805 completion tokens, 7 messages (multi-turn)
// - Symptom: response_body was only 307 bytes (just the first chunk)
// - Symptom: completion_tokens = 0 in request_logs
// - Root Cause: StreamAnthropicSSE used format-confused ObservePayload calls
//
// This test validates the exact scenario and symptoms to ensure the bug is fixed.
func TestBugE0f10f81_Regression(t *testing.T) {
	t.Log("========================================")
	t.Log("Bug e0f10f81 Regression Test")
	t.Log("========================================")
	t.Log("")
	t.Log("Original Bug:")
	t.Log("- Request: e0f10f81-10f7-4c76-9d82-b58cc1cc7b4b")
	t.Log("- 35,356 prompt tokens + 805 completion tokens")
	t.Log("- Symptom 1: response_body only 307 bytes")
	t.Log("- Symptom 2: completion_tokens = 0")
	t.Log("- Root Cause: Format confusion in ObservePayload")
	t.Log("")

	// Simulate the exact scenario: large multi-turn conversation
	// 35K tokens ≈ 140K chars (assuming 4 chars/token)
	largeMessage := strings.Repeat("量子计算的基本原理是利用量子叠加态和量子纠缠来进行信息处理。", 2000)
	// 32 chars * 2000 = 64,000 chars ≈ 16K tokens per message
	// 7 messages * 16K = ~112K tokens (close to original 35K, scaled for testing)

	// Build 7-turn conversation (simulating the original request)
	var messages []map[string]string
	for i := 0; i < 7; i++ {
		messages = append(messages, map[string]string{
			"role":    "user",
			"content": fmt.Sprintf("问题 %d: %s", i+1, largeMessage),
		})
	}

	// Simulate the upstream OpenAI response (this is what was coming through)
	// The bug was in how we captured this in StreamAnthropicSSE (now StreamOpenAIToAnthropicSSE)
	var sseChunks []string
	sseChunks = append(sseChunks, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")

	// Generate a realistic 805-token response (≈3220 chars, split into chunks)
	responseText := strings.Repeat("量子比特可以同时处于0和1的叠加态，这是量子计算优势的基础。", 100)
	// 32 chars * 100 = 3200 chars ≈ 800 tokens

	// Split into 20 chunks (simulating streaming)
	chunkSize := len(responseText) / 20
	for i := 0; i < len(responseText); i += chunkSize {
		end := i + chunkSize
		if end > len(responseText) {
			end = len(responseText)
		}
		chunk := responseText[i:end]
		sseChunks = append(sseChunks, fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk))
	}

	// Final chunk with usage (the original bug lost this)
	sseChunks = append(sseChunks, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":35356,\"completion_tokens\":805}}\n\n")
	sseChunks = append(sseChunks, "data: [DONE]\n\n")

	upstreamBody := strings.Join(sseChunks, "")

	// This is the exact code path that was broken
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	// Run through the exact function that had the bug
	outcome := StreamOpenAIToAnthropicSSE(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "e0f10f81-regression", capture, nil)

	if outcome.Interrupted {
		t.Fatalf("Stream interrupted (original bug symptom): %s", outcome.Reason)
	}

	summary := capture.SummaryAsMap()

	// === CRITICAL CHECKS: Verify bug symptoms are GONE ===

	// Symptom 1: response_body was only 307 bytes (first chunk only)
	// Fix: Should capture ALL chunks
	textContent := summary["stream_text_content"].(string)
	if len(textContent) < 3000 {
		t.Errorf("❌ REGRESSION: response_body too small!\n"+
			"   Got: %d bytes\n"+
			"   Expected: >3000 bytes\n"+
			"   Original bug: 307 bytes (only first chunk)",
			len(textContent))
	} else {
		t.Logf("✅ PASS: response_body = %d bytes (> 3000, bug fixed)", len(textContent))
	}

	// Symptom 2: completion_tokens = 0
	// Fix: Should capture token count from usage chunk
	completionTokens, ok := summary["completion_tokens"].(int)
	if !ok || completionTokens == 0 {
		t.Errorf("❌ REGRESSION: completion_tokens = 0!\n" +
			"   This is the EXACT symptom of bug e0f10f81\n" +
			"   Expected: 805")
	} else if completionTokens != 805 {
		t.Errorf("❌ FAIL: completion_tokens = %d, expected 805", completionTokens)
	} else {
		t.Logf("✅ PASS: completion_tokens = %d (bug fixed)", completionTokens)
	}

	// Additional validation: prompt_tokens
	promptTokens, ok := summary["prompt_tokens"].(int)
	if !ok || promptTokens != 35356 {
		t.Errorf("❌ FAIL: prompt_tokens = %v, expected 35356", summary["prompt_tokens"])
	} else {
		t.Logf("✅ PASS: prompt_tokens = %d", promptTokens)
	}

	// Additional validation: chunk count should be > 20
	chunkCount, ok := summary["stream_chunk_count"].(int)
	if !ok || chunkCount < 20 {
		t.Errorf("❌ FAIL: chunk_count = %v, expected >20", summary["stream_chunk_count"])
	} else {
		t.Logf("✅ PASS: chunk_count = %d", chunkCount)
	}

	// Verify the actual Anthropic SSE output is valid
	output := rec.Body.String()
	if !strings.Contains(output, "message_start") {
		t.Error("❌ FAIL: Missing message_start event")
	}
	if !strings.Contains(output, "content_block_delta") {
		t.Error("❌ FAIL: Missing content_block_delta events")
	}
	if !strings.Contains(output, "message_stop") {
		t.Error("❌ FAIL: Missing message_stop event")
	}

	t.Log("")
	t.Log("========================================")
	t.Log("🎉 Bug e0f10f81 Regression Test PASSED")
	t.Log("========================================")
	t.Log("")
	t.Log("Summary:")
	t.Logf("  - Text content: %d bytes (not 307)", len(textContent))
	t.Logf("  - Completion tokens: %d (not 0)", completionTokens)
	t.Logf("  - Prompt tokens: %d", promptTokens)
	t.Logf("  - Chunk count: %d", chunkCount)
	t.Log("")
	t.Log("The original bug symptoms are GONE. ✅")
}

// TestBugE0f10f81_OriginalFailureMode documents what the bug looked like.
// This test is EXPECTED to fail when run against the OLD code (pre-IR refactor).
// It's kept here as documentation of the failure mode.
func TestBugE0f10f81_OriginalFailureMode(t *testing.T) {
	t.Skip("This test documents the original bug behavior. It should fail on old code.")

	// The old code had this bug:
	// In StreamAnthropicSSE (now StreamOpenAIToAnthropicSSE), line 293:
	//   openaiPayload := fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, textDelta)
	//   capture.ObservePayload(openaiPayload, "", false)
	//
	// Problem: It constructed an OpenAI-format JSON string to pass to ObservePayload,
	// but extractDeltaText() expected the actual SSE format or raw JSON.
	//
	// Result:
	// 1. Only the first chunk was captured (the role chunk)
	// 2. All content chunks were lost
	// 3. completion_tokens was never recorded (usage chunk lost)
	//
	// This caused:
	// - response_body = 307 bytes (just the first chunk)
	// - completion_tokens = 0
	//
	// The fix:
	// - Replaced ObservePayload(string) with ObserveChunk(*ir.StreamChunk)
	// - All content is now captured via structured IR
}
