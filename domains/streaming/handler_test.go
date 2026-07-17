package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestDetectEmptyStreamResponse_ToolOnlyStructuredCallIsNotEmpty(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	if detectEmptyStreamResponse(map[string]any{
		"stream_chunk_count": 2,
		"tool_calls":         []map[string]any{{"id": "call_1"}},
	}, entry) {
		t.Fatal("tool-only structured response must not be classified as empty")
	}
}

func TestDetectEmptyStreamResponse_EmptyStreamIsEmpty(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	if !detectEmptyStreamResponse(map[string]any{"stream_chunk_count": 2}, entry) {
		t.Fatal("contentless stream should be classified as empty")
	}
}
