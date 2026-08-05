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

func TestShouldSkipAutoTitleGeneration(t *testing.T) {
	tests := []struct {
		name     string
		logCtx   *RequestLogContext
		expected bool
	}{
		{name: "nil logCtx treated as normal request", logCtx: nil, expected: false},
		{name: "normal request not skipped", logCtx: &RequestLogContext{}, expected: false},
		{name: "auto request skipped", logCtx: &RequestLogContext{IsAutoRequest: true}, expected: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSkipAutoTitleGeneration(tc.logCtx); got != tc.expected {
				t.Fatalf("shouldSkipAutoTitleGeneration() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestDetectUpstreamContextLoss(t *testing.T) {
	tests := []struct {
		name            string
		bodyBytes       int
		promptTokens    int
		promptSet       bool // false → leave PromptTokens nil
		completion      int
		completionSet   bool // false → leave CompletionTokens nil
		finishReason    string
		includeFinish   bool // false → omit upstream_finish_reason from m
		wantContextLoss bool
	}{
		{
			name:            "incident parameters are detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: true,
		},
		{
			name:            "healthy sibling with full prompt is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    256000,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "small request is not detected",
			bodyBytes:       40 * 1024,
			promptTokens:    100,
			promptSet:       true,
			completion:      10,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "substantial completion is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      50,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "completion just below threshold still flagged",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      49,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: true,
		},
		{
			name:            "interrupted stream is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "eof_without_done",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "missing upstream_finish_reason is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "",
			includeFinish:   false,
			wantContextLoss: false,
		},
		{
			name:            "missing prompt_tokens is not detected",
			bodyBytes:       919 * 1024,
			promptSet:       false,
			completion:      21,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "stop terminator with full body still flags when prompt drops",
			bodyBytes:       919 * 1024,
			promptTokens:    500,
			promptSet:       true,
			completion:      30,
			completionSet:   true,
			finishReason:    "stop",
			includeFinish:   true,
			wantContextLoss: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &telemetry.RequestLogEntry{
				RequestBytes: intPtr(tt.bodyBytes),
			}
			if tt.promptSet {
				entry.PromptTokens = intPtr(tt.promptTokens)
			}
			if tt.completionSet {
				entry.CompletionTokens = intPtr(tt.completion)
			}

			m := map[string]any{}
			if tt.includeFinish {
				m["upstream_finish_reason"] = tt.finishReason
			}

			got := detectUpstreamContextLoss(m, entry)
			if got != tt.wantContextLoss {
				t.Fatalf("detectUpstreamContextLoss() = %v, want %v", got, tt.wantContextLoss)
			}
		})
	}
}

func TestEstimatePromptTokensFromBytes(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero body", 0, 0},
		{"negative body clamps to zero", -1024, 0},
		{"1 byte is zero", 1, 0},
		{"4 bytes is one token", 4, 1},
		{"919 KB mirrors incident body", 919 * 1024, 919 * 1024 / 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimatePromptTokensFromBytes(tc.in); got != tc.want {
				t.Fatalf("estimatePromptTokensFromBytes(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
