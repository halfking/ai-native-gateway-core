package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/stretchr/testify/assert"
)

// TestDetectEmptyNonStreamResponse verifies the detectEmptyNonStreamResponse
// function correctly identifies non-streaming successful requests with missing
// or empty response bodies.
//
// Per §4.4 design (2026-08-29-success-empty-response-design.md), this detection
// is for observability only and does not modify the success flag.
//
// Note: The function itself does not check isStream — the caller in emitTelemetry
// guards the call with `if !isStream`. These tests verify the function's logic
// assuming it's already called in a non-streaming context.
func TestDetectEmptyNonStreamResponse(t *testing.T) {
	tests := []struct {
		name     string
		reqLog   *telemetry.RequestLogEntry
		wantTrue bool
	}{
		{
			name: "success_with_nil_body",
			reqLog: &telemetry.RequestLogEntry{
				Success:      true,
				ResponseBody: nil,
			},
			wantTrue: true,
		},
		{
			name: "success_with_empty_string_body",
			reqLog: &telemetry.RequestLogEntry{
				Success:      true,
				ResponseBody: strPtr(""),
			},
			wantTrue: true,
		},
		{
			name: "success_with_empty_json_body",
			reqLog: &telemetry.RequestLogEntry{
				Success:      true,
				ResponseBody: strPtr("{}"),
			},
			wantTrue: true,
		},
		{
			name: "success_with_whitespace_only_body",
			reqLog: &telemetry.RequestLogEntry{
				Success:      true,
				ResponseBody: strPtr("   \n\t  "),
			},
			wantTrue: true,
		},
		{
			name: "success_with_valid_body",
			reqLog: &telemetry.RequestLogEntry{
				Success:      true,
				ResponseBody: strPtr(`{"choices":[{"message":{"content":"hello"}}]}`),
			},
			wantTrue: false,
		},
		{
			name: "failure_with_nil_body",
			reqLog: &telemetry.RequestLogEntry{
				Success:      false,
				ResponseBody: nil,
			},
			wantTrue: false, // Not detected because success=false
		},
		{
			name: "nil_reqLog",
			reqLog: nil,
			wantTrue: false,
		},
		{
			name: "success_with_minimal_json",
			reqLog: &telemetry.RequestLogEntry{
				Success:      true,
				ResponseBody: strPtr(`{"id":"test"}`),
			},
			wantTrue: false, // Has content, not empty
		},
		{
			name: "success_with_empty_array",
			reqLog: &telemetry.RequestLogEntry{
				Success:      true,
				ResponseBody: strPtr(`[]`),
			},
			wantTrue: false, // Not detected (only {} is considered empty JSON)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectEmptyNonStreamResponse(tt.reqLog)
			assert.Equal(t, tt.wantTrue, got, "detectEmptyNonStreamResponse() result mismatch")
		})
	}
}
