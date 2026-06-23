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
