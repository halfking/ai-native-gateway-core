// Package relay — anthropic_to_openai stream: opus-4-8 thinking+tool_use
// fixture tests (PR-2, 2026-06-24).
//
// Why this file exists:
//
// The reported production symptom is "claude-opus-4-8 finishes its turn,
// the OpenAI-shaped tool_use is delivered to the client, the client
// executes the tool, but on the next turn the tool_use is gone and the
// model does not continue". The root cause is that the IR-layer parser
// for Anthropic's `signature_delta` streaming event used to fall
// through to an unknown default branch and the chunk was either
// dropped or — worse — corrupted the chunk index for the *following*
// tool_use block.
//
// These tests are the regression guard: a realistic opus-4-8 sequence
// (thinking + signature_delta + tool_use) must produce a tool_call
// chunk on the wire AND set HasThinking on the audit capture, without
// the relay crashing on the signature_delta delta type.
package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

// TestStreamAnthropicSSEToOpenAI_ThinkingPlusToolUse_Opus4_8 walks the
// full opus-4-8 streaming sequence:
//
//	thinking block   (index 0)
//	├─ start         {type: thinking}
//	├─ delta         {type: thinking_delta}
//	├─ delta         {type: signature_delta}  ← was the regression
//	└─ stop
//	tool_use block   (index 1)
//	├─ start         {type: tool_use, id, name, input:{}}
//	├─ delta         {type: input_json_delta, partial_json}
//	└─ stop
//	message_delta    {stop_reason: tool_use}
//	message_stop
//
// and verifies that:
//   - the stream finishes without interruption
//   - the audit capture records HasThinking=true
//   - the tool_use was emitted as an OpenAI tool_call delta with the
//     correct id, name, and accumulated arguments
//   - the final finish_reason is "tool_calls" (mapped from "tool_use")
func TestStreamAnthropicSSEToOpenAI_ThinkingPlusToolUse_Opus4_8(t *testing.T) {
	events := strings.Join([]string{
		// envelope
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_opus4_8_001\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":80,\"output_tokens\":0}}}\n\n",

		// thinking block (index 0)
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"I need to look up the weather for Tokyo.\"}}\n\n",
		// signature_delta is the event that used to break chunk index tracking.
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_opus_4_8_abc123def456\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",

		// tool_use block (index 1)
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_opus_tokyo\",\"name\":\"get_weather\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Tokyo\\\"}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",

		// trailer
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":42}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	resp := &http.Response{
		Body:       io.NopCloser(strings.NewReader(events)),
		Request:    httptest.NewRequest("POST", "/", nil),
		Header:     http.Header{},
		StatusCode: 200,
	}
	rec := httptest.NewRecorder()

	capture := audit.NewStreamCapture()
	outcome := StreamAnthropicSSEToOpenAI(
		rec, resp,
		"claude-opus-4-8",
		"claude-opus-4-8",
		"req-opus4-8-fixture",
		capture,
		nil,
	)

	if outcome.Interrupted {
		t.Fatalf("stream interrupted: reason=%s", outcome.Reason)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("missing [DONE] sentinel; body:\n%s", rec.Body.String())
	}

	// 1. Audit capture must observe the thinking block
	if !capture.HasThinking {
		t.Errorf("capture.HasThinking = false, want true (thinking block was streamed)")
	}

	// 2. Tool call must be present in the audit summary
	summary := capture.SummaryAsMap()
	toolCalls, ok := summary["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("audit summary missing tool_calls. keys: %v", getKeys(summary))
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call in summary, got %d: %+v", len(toolCalls), toolCalls)
	}
	tc := toolCalls[0]
	if tc["id"] != "toolu_opus_tokyo" {
		t.Errorf("tool_call.id = %v, want toolu_opus_tokyo", tc["id"])
	}
	if fn, ok := tc["function"].(map[string]any); !ok || fn["name"] != "get_weather" {
		t.Errorf("tool_call.function.name = %v, want get_weather", tc["function"])
	} else if args, _ := fn["arguments"].(string); !strings.Contains(args, "Tokyo") {
		t.Errorf("tool_call.function.arguments = %q, want to contain Tokyo", args)
	}

	// 3. The OpenAI-format stream must contain a tool_calls delta for
	//    the tool_use and the final finish_reason must be tool_calls.
	chunks := readOpenAIChunks(t, rec.Body.String())

	var sawToolCallDelta bool
	var lastFinishReason string
	for _, c := range chunks {
		choices, ok := c["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		ch, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		if fr, ok := ch["finish_reason"].(string); ok && fr != "" && fr != "null" {
			lastFinishReason = fr
		}
		delta, ok := ch["delta"].(map[string]any)
		if !ok {
			continue
		}
		if tcs, ok := delta["tool_calls"].([]any); ok && len(tcs) > 0 {
			sawToolCallDelta = true
			tc0 := tcs[0].(map[string]any)
			if tc0["id"] != "toolu_opus_tokyo" {
				t.Errorf("tool_call delta id = %v, want toolu_opus_tokyo", tc0["id"])
			}
		}
	}

	if !sawToolCallDelta {
		t.Errorf("no tool_calls delta in OpenAI output — tool_use lost.\nbody:\n%s", rec.Body.String())
	}
	if lastFinishReason != "tool_calls" {
		t.Errorf("final finish_reason = %q, want tool_calls", lastFinishReason)
	}
}

// TestStreamAnthropicSSEToOpenAI_SignatureDelta_Alone_DoesNotCrash is
// the focused regression test: a stream that is *only* a thinking block
// closed by a signature_delta must not be dropped, must not crash, and
// must mark HasThinking on the capture. Before PR-2 the default branch
// in content_block_delta swallowed signature_delta and the stream
// reported Interrupted=true with reason=read_error on some paths.
func TestStreamAnthropicSSEToOpenAI_SignatureDelta_Alone_DoesNotCrash(t *testing.T) {
	events := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_sig\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_only\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	resp := &http.Response{
		Body:       io.NopCloser(strings.NewReader(events)),
		Request:    httptest.NewRequest("POST", "/", nil),
		Header:     http.Header{},
		StatusCode: 200,
	}
	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(
		rec, resp,
		"claude-opus-4-8", "claude-opus-4-8",
		"req-sig-only",
		capture, nil,
	)

	if outcome.Interrupted {
		t.Fatalf("signature_delta crashed the stream: reason=%s", outcome.Reason)
	}
	if !capture.HasThinking {
		t.Errorf("capture.HasThinking = false, want true (signature_delta is part of a thinking block)")
	}
}

// getKeys is defined in anthropic_to_openai_stream_tool_calls_test.go;
// reusing it here to keep failure messages informative.

// Ensure encoding/json stays referenced in case future cases need it.
var _ = json.Marshal
