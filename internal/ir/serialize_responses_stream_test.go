package ir

import (
	"strings"
	"testing"
)

// TestStreamChunk_Responses_ExtensionLoss pins which Responses SSE event
// types round-trip through StreamChunk.SerializeResponses. The matrix below
// mirrors the table in docs/handoff/2026-08-30-main-integration-data-closure-audit.md
// (item D) — update both at the same time.
//
//	Pinned event types (emitted today):
//	- response.output_text.delta
//	- response.reasoning_text.delta
//	- response.output_item.added          (tool call first chunk)
//	- response.function_call_arguments.delta
//	- response.audio.delta                (when Delta.AudioDelta.Data != "")
//	- response.audio_transcript.delta     (when Delta.AudioDelta.Transcript != "")
//	- error                               (ChunkTypeError)
//
//	Silently dropped event types (intentional today):
//	- response.created                    (orchestrator emits scaffolding)
//	- response.in_progress                (orchestrator emits scaffolding)
//	- response.output_item.done           (orchestrator emits scaffolding)
//	- response.content_part.added/done    (orchestrator emits scaffolding)
//	- response.completed                  (orchestrator emits with aggregated usage)
//	- response.incomplete                 (orchestrator emits when stop reason truncates)
//
//	If a new event type is added to the StreamChunk IR, this test must be
//	updated to assert the new event shows up. Contributors MUST NOT silently
//	add events to the SSE output without pinning them here first.
//
// Future / unknown event types (e.g. response.mcp_call.*, response.file_search.*,
// response.code_interpreter.*) are NOT yet wired. They will surface as
// unsupported chunk types and SerializeResponses returns "" for them — that
// is the documented loss. See the assertion in
// TestStreamChunk_Responses_FutureEvent_IsNotEmitted for the pinned behavior.
func TestStreamChunk_Responses_ExtensionLoss(t *testing.T) {
	cases := []struct {
		name        string
		chunk       *StreamChunk
		wantEvents  []string
		notEmitted  []string
	}{
		{
			name: "text delta",
			chunk: &StreamChunk{
				Type:  ChunkTypeDelta,
				Delta: &StreamDelta{Content: "hello"},
			},
			wantEvents: []string{"response.output_text.delta"},
		},
		{
			name: "reasoning delta",
			chunk: &StreamChunk{
				Type:  ChunkTypeDelta,
				Delta: &StreamDelta{ReasoningContent: "thinking..."},
			},
			wantEvents: []string{"response.reasoning_text.delta"},
		},
		{
			name: "tool call delta with arguments",
			chunk: &StreamChunk{
				Type: ChunkTypeDelta,
				Delta: &StreamDelta{
					ToolCalls: []StreamToolCallDelta{{
						Index:     0,
						ID:        "call_1",
						Type:      "function",
						Name:      "search",
						Arguments: `{"q":"x"}`,
					}},
				},
			},
			wantEvents: []string{
				"response.output_item.added",
				"response.function_call_arguments.delta",
			},
		},
		{
			name: "error chunk",
			chunk: &StreamChunk{
				Type:  ChunkTypeError,
				Error: &StreamError{Type: "rate_limit", Message: "slow down", Code: "429"},
			},
			wantEvents: []string{"error"},
		},
		{
			name: "done chunk emits nothing (orchestrator handles)",
			chunk: &StreamChunk{
				Type: ChunkTypeDone,
			},
			wantEvents: nil,
			notEmitted: []string{
				"response.completed", "response.output_item.done", "response.content_part.done",
			},
		},
		{
			name: "usage chunk emits nothing (orchestrator aggregates)",
			chunk: &StreamChunk{
				Type:  ChunkTypeUsage,
				Usage: &StreamUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
			},
			wantEvents: nil,
			notEmitted: []string{"response.completed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.chunk.SerializeResponses("msg_test")
			for _, want := range tc.wantEvents {
				if !strings.Contains(out, "event: "+want) {
					t.Errorf("expected event %q in output, got:\n%s", want, out)
				}
			}
			for _, notWant := range tc.notEmitted {
				if strings.Contains(out, "event: "+notWant) {
					t.Errorf("event %q should NOT be emitted by SerializeResponses, got:\n%s", notWant, out)
				}
			}
		})
	}
}

// TestStreamChunk_Responses_FutureEvent_IsNotEmitted pins the documented
// loss: a future Responses event type (response.mcp_call.*, response.file_search.*,
// etc.) that has no equivalent in the StreamChunk IR today is silently
// dropped — SerializeResponses returns "" — rather than panicking. If a
// contributor wants to add support for a new event, they must (a) extend
// StreamChunk/StreamDelta with the new field, (b) implement the SSE emission,
// and (c) update TestStreamChunk_Responses_ExtensionLoss above.
func TestStreamChunk_Responses_FutureEvent_IsNotEmitted(t *testing.T) {
	// A chunk with only a "future" delta marker must not produce any
	// SSE event because there is no IR field for it.
	chunk := &StreamChunk{
		Type:  ChunkTypeDelta,
		Delta: &StreamDelta{
			// Empty Delta — no text/reasoning/tool/audio content. This
			// represents an upstream future event the IR cannot represent.
		},
	}
	out := chunk.SerializeResponses("msg_future")
	if out != "" {
		t.Errorf("future event should produce empty SSE output, got:\n%s", out)
	}
}

// TestStreamChunk_Responses_ErrorChunk verifies that an error chunk emits
// the Responses SSE error event with the documented field shape (type, code,
// message) so consumers can route on event.type.
func TestStreamChunk_Responses_ErrorChunk(t *testing.T) {
	chunk := &StreamChunk{
		Type: ChunkTypeError,
		Error: &StreamError{
			Type:    "rate_limit_error",
			Message: "Rate limit exceeded",
			Code:    "rate_limit",
		},
	}
	out := chunk.SerializeResponses("msg_err")
	if !strings.Contains(out, "event: error") {
		t.Fatalf("expected error event, got:\n%s", out)
	}
	if !strings.Contains(out, `"type":"rate_limit_error"`) {
		t.Errorf("error.type missing or wrong, got:\n%s", out)
	}
	if !strings.Contains(out, `"message":"Rate limit exceeded"`) {
		t.Errorf("error.message missing or wrong, got:\n%s", out)
	}
	if !strings.Contains(out, `"code":"rate_limit"`) {
		t.Errorf("error.code missing or wrong, got:\n%s", out)
	}
}
