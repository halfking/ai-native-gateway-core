package streaming

import (
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

func TestSuccessUpstreamStatusCode(t *testing.T) {
	if got := successUpstreamStatusCode(&executors.ExecuteResult{Response: &http.Response{StatusCode: http.StatusCreated}}); got != http.StatusCreated {
		t.Fatalf("successUpstreamStatusCode() = %d, want %d", got, http.StatusCreated)
	}
	if got := successUpstreamStatusCode(&executors.ExecuteResult{}); got != http.StatusOK {
		t.Fatalf("successUpstreamStatusCode() without response = %d, want %d", got, http.StatusOK)
	}
	if got := successUpstreamStatusCode(nil); got != http.StatusOK {
		t.Fatalf("successUpstreamStatusCode(nil) = %d, want %d", got, http.StatusOK)
	}
}

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

// TestApplyParentCorrelationFields (2026-08-06) — guards the flow from
// logCtx.ParentRequestID / OriginActor into the persisted RequestLogEntry.
// Without this, request_logs_hot.parent_request_id is always NULL on
// auto-title loopback rows, so operators cannot SQL JOIN
// "08aa2a8a → 3a03f7db".
func TestApplyParentCorrelationFields(t *testing.T) {
	tests := []struct {
		name           string
		logCtx         *RequestLogContext
		wantParentID   *string
		wantOriginActor *string
	}{
		{
			name:    "nil logCtx is no-op",
			logCtx:  nil,
			wantParentID: nil,
			wantOriginActor: nil,
		},
		{
			name:           "empty fields → nil entry pointers",
			logCtx:         &RequestLogContext{},
			wantParentID:   nil,
			wantOriginActor: nil,
		},
		{
			name: "both fields populated → both entry pointers filled",
			logCtx: &RequestLogContext{
				ParentRequestID: "08aa2a8af42ef05eb87c97973f467519",
				OriginActor:     "auto-title-generator",
			},
			wantParentID:   strPtrLocal("08aa2a8af42ef05eb87c97973f467519"),
			wantOriginActor: strPtrLocal("auto-title-generator"),
		},
		{
			name: "only parent → origin stays nil",
			logCtx: &RequestLogContext{
				ParentRequestID: "p",
			},
			wantParentID:   strPtrLocal("p"),
			wantOriginActor: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := &telemetry.RequestLogEntry{}
			applyParentCorrelationFields(entry, tc.logCtx)
			if !ptrEqualString(entry.ParentRequestID, tc.wantParentID) {
				t.Errorf("ParentRequestID = %v, want %v", entry.ParentRequestID, tc.wantParentID)
			}
			if !ptrEqualString(entry.OriginActor, tc.wantOriginActor) {
				t.Errorf("OriginActor = %v, want %v", entry.OriginActor, tc.wantOriginActor)
			}
		})
	}
}

func strPtrLocal(s string) *string { return &s }

// ptrEqualString reports whether two *string are both nil or both point to
// the same string value (Go pointer comparison is fine for *string built by
// strPtrLocal in this test scope).
func ptrEqualString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TestSanitizeGwSessionHeader (2026-08-06) — guards the three gateway
// session-id namespaces:
//   gw_<uuid>  — user main session (legacy)
//   gt_<...>   — auto-title branch (admin/auto_title_generator.go)
//   gs_<...>   — auto-summary branch (admin/auto_summary_generator.go)
//
// Strip the prefix to recover the parent user session id:
//
//   strings.TrimPrefix("gt_gw_abc", "gt_") == "gw_abc"
//   strings.TrimPrefix("gs_gw_abc", "gs_") == "gw_abc"
func TestSanitizeGwSessionHeader(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty stays empty", input: "", want: ""},
		{name: "whitespace stays empty", input: "   ", want: ""},
		{name: "plain uuid is rejected", input: "abc-123-def", want: ""},
		{name: "random session-tag-like is rejected", input: "sess_abc", want: ""},
		{name: "uppercase GW is rejected (case-sensitive)", input: "GW_abc", want: ""},
		{name: "main gw_ accepted", input: "gw_abc-123", want: "gw_abc-123"},
		{name: "title branch gt_ accepted", input: "gt_gw_abc-123", want: "gt_gw_abc-123"},
		{name: "summary branch gs_ accepted", input: "gs_gw_abc-123", want: "gs_gw_abc-123"},
		{name: "gt_ without trailing chars accepted", input: "gt_x", want: "gt_x"},
		{name: "gs_ without trailing chars accepted", input: "gs_x", want: "gs_x"},
		{name: "trailing whitespace trimmed", input: "  gw_abc  ", want: "gw_abc"},
		{name: "gt_ with whitespace preserved on content", input: "  gt_gw_abc  ", want: "gt_gw_abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeGwSessionHeader(tc.input); got != tc.want {
				t.Fatalf("sanitizeGwSessionHeader(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestShouldSkipAutoSummaryGeneration (2026-08-06) — symmetric to
// TestShouldSkipAutoTitleGeneration. The summary loopback sets
// X-Gw-Is-Auto: true so on its own emitTelemetry pass IsAutoRequest is
// true and the generator skips itself.
func TestShouldSkipAutoSummaryGeneration(t *testing.T) {
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
			if got := shouldSkipAutoSummaryGeneration(tc.logCtx); got != tc.expected {
				t.Fatalf("shouldSkipAutoSummaryGeneration() = %v, want %v", got, tc.expected)
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
