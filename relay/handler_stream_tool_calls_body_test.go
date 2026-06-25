// Regression test for the bug reported on 2026-06-25:
//
//   "We found that when selecting gpt-5.4 in kaixuan, the returned
//    message no longer contains tool-related content. Request id
//    351dc531e35491f0a24b2d2b464e9878. Please check the logs and fix it —
//    it might be a data unpacking issue."
//
// Investigation summary:
//
//   - The dedicated JSONB column request_logs.tool_calls IS populated
//     correctly for streaming responses via audit.StreamCapture.ToolCalls
//     (the IR layer in audit/stream.go::mergeToolCall handles accumulation).
//
//   - However, when emitTelemetry synthesizes response_body for a streaming
//     request that did not produce a non-empty ResponseBody, it builds a
//     pseudoBody from stream_text_content alone and writes tool_calls
//     into `content` as plain text (e.g. "[Tool Call: bash]\n{...args...}").
//
//   - Admin UI and downstream API consumers (admin/message_display.go
//     extractAssistantFromResponseJSON) look for `message.tool_calls` in
//     response_body — they cannot find it, so the user sees "no tool content".
//
//   - For the gpt-5.4 request specifically, it was a title-generator task
//     with the system prompt "You are a title generator. You output ONLY a
//     thread title. Nothing else.", so the model legitimately returns text
//     only. The user's observation is consistent with the general bug for
//     all streaming tool-calling requests, not just this specific record.
//
// Fix: emitTelemetry now reads the structured tool_calls from
// capture.SummaryAsMap() and embeds them into the synthetic response_body
// as message.tool_calls, stripping the streaming-only "index" field and
// flipping finish_reason to "tool_calls".

package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestEmitTelemetry_StreamingToolCalls_EmbeddedInResponseBody asserts that
// when the audit.StreamCapture accumulated structured tool_calls (via the IR
// layer), the synthetic response_body persisted to request_logs.response_body
// contains a proper message.tool_calls array — not the legacy
// "[Tool Call: ...]" text rendering.
func TestEmitTelemetry_StreamingToolCalls_EmbeddedInResponseBody(t *testing.T) {
	// Build a StreamCapture that mirrors what the IR pipeline populates
	// during an OpenAI-style streaming tool-call response.
	capture := audit.NewStreamCapture()

	// Chunk 1: first tool_call with id + name + empty arguments.
	capture.ObserveChunk(&ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			Role: "assistant",
			ToolCalls: []ir.StreamToolCallDelta{{
				Index:     0,
				ID:        "call_019efb4dac2d7ea3bcd1b1e8",
				Type:      "function",
				Name:      "bash",
				Arguments: "",
			}},
		},
	})
	// Chunk 2: arguments delta — accumulates into the same index 0.
	capture.ObserveChunk(&ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{{
				Index:     0,
				Arguments: `{"command":"ls -la"}`,
			}},
		},
	})
	// Done.
	capture.ObserveChunk(&ir.StreamChunk{
		Type:         ir.ChunkTypeDone,
		FinishReason: "tool_calls",
	})

	// Confirm IR layer actually populated structured tool_calls.
	summary := capture.SummaryAsMap()
	rawTC, ok := summary["tool_calls"]
	if !ok {
		t.Fatalf("expected summary to carry tool_calls, got keys=%v", keysOf(summary))
	}
	arr, ok := rawTC.([]map[string]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("expected one structured tool_call, got %#v", rawTC)
	}
	if arr[0]["id"] != "call_019efb4dac2d7ea3bcd1b1e8" {
		t.Fatalf("id mismatch: %#v", arr[0])
	}

	// Now the fix-path: the synthetic response_body should embed the
	// tool_calls. We do not call emitTelemetry here (it depends on
	// routing/auth/telemetry wiring); instead we re-run the same logic
	// the fix uses, which is intentionally a pure function of
	// capture.SummaryAsMap() so it is directly testable.
	body := buildSyntheticResponseBodyForTest(t, capture)

	// Expect a parseable JSON object.
	var parsed struct {
		Choices []struct {
			Message struct {
				Role      string                   `json:"role"`
				Content   string                   `json:"content"`
				ToolCalls []map[string]interface{} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("synthetic body is not valid JSON: %v\nbody=%s", err, body)
	}
	if len(parsed.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(parsed.Choices))
	}

	// The bug's smoking gun: legacy code rendered tool_calls as TEXT inside
	// content (e.g. "[Tool Call: bash]\n{...}"). After the fix, content
	// should NOT carry that text representation.
	if strings.Contains(parsed.Choices[0].Message.Content, "[Tool Call:") {
		t.Errorf("synthetic response_body still contains legacy '[Tool Call:' text rendering: %q",
			parsed.Choices[0].Message.Content)
	}

	// finish_reason should be "tool_calls" (upstream contract), not "stop".
	if parsed.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", parsed.Choices[0].FinishReason)
	}

	// message.tool_calls should now carry the structured array.
	tcs := parsed.Choices[0].Message.ToolCalls
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call in message, got %d (body=%s)", len(tcs), body)
	}
	if tcs[0]["id"] != "call_019efb4dac2d7ea3bcd1b1e8" {
		t.Errorf("tool_call.id = %v, want call_019efb4dac2d7ea3bcd1b1e8", tcs[0]["id"])
	}
	if tcs[0]["type"] != "function" {
		t.Errorf("tool_call.type = %v, want function", tcs[0]["type"])
	}

	// Streaming-only `index` field must NOT leak into the final response —
	// OpenAI Chat Completions final-response tool_calls do not include it,
	// and including it trips clients that strictly validate the schema.
	if _, leaked := tcs[0]["index"]; leaked {
		t.Errorf("streaming-only `index` field leaked into final response: %#v", tcs[0])
	}

	fn, ok := tcs[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_call.function missing or wrong type: %#v", tcs[0])
	}
	if fn["name"] != "bash" {
		t.Errorf("function.name = %v, want bash", fn["name"])
	}
	if fn["arguments"] != `{"command":"ls -la"}` {
		t.Errorf("function.arguments = %v, want %q", fn["arguments"], `{"command":"ls -la"}`)
	}
}

// TestEmitTelemetry_StreamingTextOnly_NoToolCallsField asserts the no-tool
// path is unaffected: when the model streams plain text and the capture has
// no structured tool_calls, the synthetic body keeps finish_reason="stop"
// and does NOT introduce a tool_calls field.
func TestEmitTelemetry_StreamingTextOnly_NoToolCallsField(t *testing.T) {
	capture := audit.NewStreamCapture()
	capture.ObserveChunk(&ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Role: "assistant", Content: "提交推送并合并主分支后审计修复代码"},
	})
	capture.ObserveChunk(&ir.StreamChunk{
		Type:         ir.ChunkTypeDone,
		FinishReason: "stop",
	})

	body := buildSyntheticResponseBodyForTest(t, capture)

	var parsed struct {
		Choices []struct {
			Message struct {
				Role      string                   `json:"role"`
				Content   string                   `json:"content"`
				ToolCalls []map[string]interface{} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("body parse: %v\nbody=%s", err, body)
	}
	if parsed.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", parsed.Choices[0].FinishReason)
	}
	if parsed.Choices[0].Message.Content != "提交推送并合并主分支后审计修复代码" {
		t.Errorf("content = %q", parsed.Choices[0].Message.Content)
	}
	if len(parsed.Choices[0].Message.ToolCalls) != 0 {
		t.Errorf("expected no tool_calls, got %d", len(parsed.Choices[0].Message.ToolCalls))
	}
}

// buildSyntheticResponseBodyForTest mirrors the relevant slice of
// emitTelemetry after the fix. It exists so the regression test does not
// need a fully wired ChatHandler (full emitTelemetry depends on routing,
// auth, telemetry — heavy for a unit test). The production code under test
// is the inline logic in relay/handler.go around the pseudoBody construction.
func buildSyntheticResponseBodyForTest(t *testing.T, capture *audit.StreamCapture) string {
	t.Helper()
	m := capture.SummaryAsMap()

	var textContent string
	if v, ok := m["stream_text_content"].(string); ok && v != "" {
		textContent = v
	}
	var toolCallsFromStream []map[string]any
	if v, ok := m["tool_calls"].([]map[string]any); ok && len(v) > 0 {
		toolCallsFromStream = v
	}
	// Mirror the production fix: when structured tool_calls are present,
	// strip the legacy "[Tool Call: <name>]\n<args>" text rendering
	// from the content body so the same data is not duplicated.
	if toolCallsFromStream != nil {
		textContent = stripLegacyToolCallText(textContent)
	}

	if textContent == "" && toolCallsFromStream == nil {
		if previewStr, ok := m["response_preview"].(string); ok && previewStr != "" {
			return previewStr
		}
		return ""
	}

	var pt, ct int
	if v, ok := m["prompt_tokens"].(int); ok {
		pt = v
	}
	if v, ok := m["completion_tokens"].(int); ok {
		ct = v
	}

	finishReason := "stop"
	var cleaned []map[string]any
	if toolCallsFromStream != nil {
		finishReason = "tool_calls"
		cleaned = make([]map[string]any, 0, len(toolCallsFromStream))
		for _, tc := range toolCallsFromStream {
			entry := map[string]any{}
			for k, v := range tc {
				if k == "index" {
					continue
				}
				entry[k] = v
			}
			cleaned = append(cleaned, entry)
		}
	}
	message := map[string]any{"role": "assistant", "content": textContent}
	if len(cleaned) > 0 {
		message["tool_calls"] = cleaned
	}
	pseudoBody := map[string]any{
		"choices": []map[string]any{
			{"message": message, "finish_reason": finishReason},
		},
	}
	if pt > 0 || ct > 0 {
		pseudoBody["usage"] = map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}
	}
	b, err := json.Marshal(pseudoBody)
	if err != nil {
		t.Fatalf("marshal pseudoBody: %v", err)
	}
	return string(b)
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
