package relay

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/stretchr/testify/assert"
)

// TestDetectEmptyStreamResponse_ThreeChunks_ZeroTokens tests the classic empty response pattern:
// 3 chunks, 0 tokens, no finish_reason. This is the pattern seen with Provider 18 (NVIDIA NIM)
// on large inputs (160k+ tokens).
func TestDetectEmptyStreamResponse_ThreeChunks_ZeroTokens(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count":   3,
		"stream_done_received": true,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: nil,
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.True(t, isEmpty, "3 chunks + 0 tokens + no finish_reason should be empty")
}

// TestDetectEmptyStreamResponse_ManyChunks tests that a normal response with many chunks
// is not considered empty, even if tokens happen to be 0 (which shouldn't happen in practice).
func TestDetectEmptyStreamResponse_ManyChunks(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count":   1992,
		"stream_done_received": true,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(2000),
		UpstreamFinishReason: strPtr("length"),
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "Normal response with many chunks should not be empty")
}

// TestDetectEmptyStreamResponse_FewChunks_HasTokens tests that a response with few chunks
// but non-zero tokens is not considered empty. This is the pattern for tool_calls responses
// from Provider 18.
func TestDetectEmptyStreamResponse_FewChunks_HasTokens(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 3,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(41), // Provider 18 tool_calls response
		UpstreamFinishReason: strPtr("tool_calls"),
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "Few chunks but has tokens should not be empty")
}

// TestDetectEmptyStreamResponse_HasPreview tests that a response with content preview
// is not considered empty, even with few chunks and zero tokens.
func TestDetectEmptyStreamResponse_HasPreview(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 4,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens: intPtr(0),
		ResponsePreview:  strPtr("Some content"),
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "Has preview content should not be empty")
}

// TestDetectEmptyStreamResponse_HasStreamTextContent tests that a response with
// stream_text_content in the capture summary is not considered empty.
func TestDetectEmptyStreamResponse_HasStreamTextContent(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count":  4,
		"stream_text_content": "Some streaming text",
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: nil,
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "Has stream_text_content should not be empty")
}

// TestDetectEmptyStreamResponse_HasFinishReason tests that a response with
// upstream_finish_reason is not considered empty (normal completion).
func TestDetectEmptyStreamResponse_HasFinishReason(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 5,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: strPtr("stop"),
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "Has finish_reason should not be empty")
}

// TestDetectEmptyStreamResponse_EdgeCase_ExactlyThreeChunks tests the boundary at 3 chunks.
func TestDetectEmptyStreamResponse_EdgeCase_ExactlyThreeChunks(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 3,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: nil,
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.True(t, isEmpty, "3 chunks + 0 tokens + no finish_reason should be empty")
}

// TestDetectEmptyStreamResponse_EdgeCase_FourChunks tests that 4 chunks is not empty.
func TestDetectEmptyStreamResponse_EdgeCase_FourChunks(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 4,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: nil,
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "4 chunks should not be considered empty (threshold is > 3)")
}

// 2026-06-26 regression tests.
//
// Symptom: server-184 request_logs accumulated many rows with
// error_kind='empty_response' on successful requests. Root cause was that
// detectEmptyStreamResponse's "upstream finish_reason" check read
// reqLog.UpstreamFinishReason, which is still nil at the call site (it's
// filled a few lines later in emitTelemetry). The check was effectively
// dead, so any successful response with ≤3 chunks, 0 tokens, and no preview
// was wrongly flagged. These tests cover the four short-circuits that
// restore correct classification.

// TestDetectEmptyStreamResponse_ToolCalls_NotEmpty verifies that a stream
// which the capture populated with structured tool_calls is NOT considered
// empty — even if delta.content was never non-empty (tool-call-only
// responses are a perfectly normal OpenAI Chat Completions pattern).
func TestDetectEmptyStreamResponse_ToolCalls_NotEmpty(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 2,
		"tool_calls": []map[string]any{
			{"index": 0, "id": "call_abc", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"location":"SF"}`}},
		},
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: nil,
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "tool_calls populated → response is NOT empty")
}

// TestDetectEmptyStreamResponse_ToolCallsAnyType_NotEmpty verifies the
// short-circuit also fires when tool_calls arrives as []any (the IR layer's
// internal representation in some code paths).
func TestDetectEmptyStreamResponse_ToolCallsAnyType_NotEmpty(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 1,
		"tool_calls": []any{
			map[string]any{"id": "call_abc"},
		},
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens: intPtr(0),
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "tool_calls []any populated → response is NOT empty")
}

// TestDetectEmptyStreamResponse_StreamInterrupted_NotEmpty verifies that
// genuine stream interruptions (timeout / client cancel / eof_without_done)
// are NOT classified as empty_response here. The stream_interrupted branch
// in emitTelemetry (relay/handler.go ~1722-1736) already classifies them
// correctly as stream_error with the right failure_detail_code.
func TestDetectEmptyStreamResponse_StreamInterrupted_NotEmpty(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count":   2,
		"stream_interrupted":   true,
		"upstream_finish_reason": "eof_without_done",
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens: intPtr(0),
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "stream_interrupted=true → defer to stream_interrupted classifier, NOT empty")
}

// TestDetectEmptyStreamResponse_UpstreamFinishReasonFromMap_NotEmpty is the
// core regression test: upstream_finish_reason is read from m (the capture
// summary) NOT from reqLog.UpstreamFinishReason. Previously the read was
// from reqLog, which is still nil at the call site, so this case was
// wrongly classified as empty.
func TestDetectEmptyStreamResponse_UpstreamFinishReasonFromMap_NotEmpty(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count":     1,
		"upstream_finish_reason": "tool_calls",
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: nil, // ← this is the production state at the call site
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "upstream_finish_reason set in m → response is NOT empty (regression fix)")
}

// TestDetectEmptyStreamResponse_AllConditionsMet_StillEmpty verifies that
// the original detection logic still flags truly empty responses — none of
// the new short-circuits regress this behaviour.
func TestDetectEmptyStreamResponse_AllConditionsMet_StillEmpty(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 3,
		// No tool_calls
		// No stream_interrupted
		// No upstream_finish_reason
		// No stream_text_content
		// No response_preview
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: nil,
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.True(t, isEmpty, "all four classic conditions met → still empty (true positive preserved)")
}

// TestDetectEmptyStreamResponse_PrePopulatedReqLogFinishReason_NotEmpty
// verifies that callers (other than emitTelemetry) who pre-populate
// reqLog.UpstreamFinishReason still see the defensive fallback check 4
// fire. This protects against future callers reverting the behaviour.
func TestDetectEmptyStreamResponse_PrePopulatedReqLogFinishReason_NotEmpty(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 1,
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens:     intPtr(0),
		ResponsePreview:      nil,
		UpstreamFinishReason: strPtr("stop"), // pre-populated
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.False(t, isEmpty, "UpstreamFinishReason pre-populated → NOT empty")
}

// TestDetectEmptyStreamResponse_EmptyToolCallsArray_StillPossiblyEmpty
// verifies that a nil or empty tool_calls array does NOT short-circuit the
// detection — only a populated array does.
func TestDetectEmptyStreamResponse_EmptyToolCallsArray_StillPossiblyEmpty(t *testing.T) {
	m := map[string]any{
		"stream_chunk_count": 2,
		"tool_calls":         []map[string]any{}, // empty, NOT populated
	}
	reqLog := &telemetry.RequestLogEntry{
		CompletionTokens: intPtr(0),
	}

	isEmpty := detectEmptyStreamResponse(m, reqLog)
	assert.True(t, isEmpty, "empty tool_calls array → fall through to classic checks (true empty)")
}
