//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/relay"
	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmptyResponseAudit_ToolCallNotMisclassified verifies that a streaming
// tool-call-only response (no delta.content) is NOT misclassified as
// empty_response after the 2026-06-26 fix.
//
// To run:
//   export LLM_GATEWAY_PG_URL="postgres://user:pass@host:5432/llm_gateway?sslmode=disable"
//   go test -tags=integration ./tests/integration -v -run TestEmptyResponseAudit
func TestEmptyResponseAudit_ToolCallNotMisclassified(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err, "connect to db")
	defer pool.Close()

	// Create telemetry client
	client := telemetry.NewClient()
	client.SetDB(pool)
	defer client.Stop()

	// Scenario 1: Tool-call response with 2 chunks, finish_reason="tool_calls"
	t.Run("ToolCall_WithFinishReason_NotEmpty", func(t *testing.T) {
		reqID := "audit-toolcall-" + time.Now().Format("20060102-150405.000")
		
		// Simulate capture of a tool-call stream
		capture := &audit.StreamCapture{}
		capture.ObserveChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{
				Role: "assistant",
			},
			SourceProtocol: ir.ProtocolOpenAIChat,
		})
		capture.ObserveChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{
				ToolCalls: []ir.StreamToolCallDelta{{
					Index:     0,
					ID:        "call_abc123",
					Type:      "function",
					Name:      "get_weather",
					Arguments: `{"location":"San Francisco"}`,
				}},
			},
			FinishReason:   "tool_calls",
			SourceProtocol: ir.ProtocolOpenAIChat,
		})
		capture.ObserveChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeUsage,
			Usage: &ir.StreamUsage{
				PromptTokens:     10,
				CompletionTokens: 25,
			},
			SourceProtocol: ir.ProtocolOpenAIChat,
		})

		// Build capture summary
		m := capture.SummaryAsMap()
		
		// Build request log entry as emitTelemetry does
		reqLog := &telemetry.RequestLogEntry{
			RequestID:        reqID,
			TenantID:         "test-tenant",
			Success:          true,
			RequestStatus:    strPtr("success"),
			CompletionTokens: intPtr(25),
			PromptTokens:     intPtr(10),
		}

		// Apply detectEmptyStreamResponse check (the function under audit)
		isEmpty := relay.DetectEmptyStreamResponse(m, reqLog)
		
		assert.False(t, isEmpty, "tool-call response with finish_reason should NOT be classified as empty")
		assert.True(t, reqLog.Success, "Success flag should remain true")
		assert.Nil(t, reqLog.ErrorKind, "ErrorKind should remain nil")
	})

	// Scenario 2: Tool-call response WITHOUT usage block (0 tokens), but has tool_calls
	t.Run("ToolCall_ZeroTokens_StillNotEmpty", func(t *testing.T) {
		reqID := "audit-toolcall-notoken-" + time.Now().Format("20060102-150405.000")
		
		capture := &audit.StreamCapture{}
		capture.ObserveChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{
				ToolCalls: []ir.StreamToolCallDelta{{
					Index:     0,
					ID:        "call_xyz",
					Type:      "function",
					Name:      "search",
					Arguments: `{"q":"test"}`,
				}},
			},
			FinishReason:   "tool_calls",
			SourceProtocol: ir.ProtocolOpenAIChat,
		})

		m := capture.SummaryAsMap()
		reqLog := &telemetry.RequestLogEntry{
			RequestID:        reqID,
			Success:          true,
			CompletionTokens: intPtr(0), // ← zero tokens, but tool_calls present
		}

		isEmpty := relay.DetectEmptyStreamResponse(m, reqLog)
		
		assert.False(t, isEmpty, "tool_calls present → NOT empty, even with 0 tokens")
	})

	// Scenario 3: stream_interrupted (eof_without_done) should NOT be re-classified
	t.Run("StreamInterrupted_NotReclassified", func(t *testing.T) {
		reqID := "audit-interrupted-" + time.Now().Format("20060102-150405.000")
		
		capture := &audit.StreamCapture{}
		capture.ObserveChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{
				Content: "Hello",
			},
			SourceProtocol: ir.ProtocolOpenAIChat,
		})
		capture.MarkInterruptedWithReason("eof_without_done")

		m := capture.SummaryAsMap()
		reqLog := &telemetry.RequestLogEntry{
			RequestID:        reqID,
			Success:          true, // will be set to false by stream_interrupted branch
			CompletionTokens: intPtr(0),
		}

		isEmpty := relay.DetectEmptyStreamResponse(m, reqLog)
		
		assert.False(t, isEmpty, "stream_interrupted=true → defer to stream_interrupted classifier")
	})

	// Scenario 4: Truly empty response (3 chunks, no content, no tokens, no finish_reason)
	t.Run("TrulyEmpty_StillDetected", func(t *testing.T) {
		reqID := "audit-empty-" + time.Now().Format("20060102-150405.000")
		
		// Simulate 3 empty delta chunks (Provider 18 NVIDIA NIM pattern)
		capture := &audit.StreamCapture{}
		for i := 0; i < 3; i++ {
			capture.ObserveChunk(&ir.StreamChunk{
				Type:           ir.ChunkTypeDelta,
				Delta:          &ir.StreamDelta{}, // empty
				SourceProtocol: ir.ProtocolOpenAIChat,
			})
		}

		m := capture.SummaryAsMap()
		reqLog := &telemetry.RequestLogEntry{
			RequestID:        reqID,
			Success:          true,
			CompletionTokens: intPtr(0),
		}

		isEmpty := relay.DetectEmptyStreamResponse(m, reqLog)
		
		assert.True(t, isEmpty, "truly empty response (no content, no tokens, no finish_reason) → still empty")
	})

	// Scenario 5: upstream_finish_reason present in map (core regression test)
	t.Run("UpstreamFinishReasonInMap_NotEmpty", func(t *testing.T) {
		reqID := "audit-finish-" + time.Now().Format("20060102-150405.000")
		
		capture := &audit.StreamCapture{}
		capture.ObserveChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{
				Content: "OK",
			},
			FinishReason:   "stop",
			SourceProtocol: ir.ProtocolOpenAIChat,
		})

		m := capture.SummaryAsMap()
		reqLog := &telemetry.RequestLogEntry{
			RequestID:           reqID,
			Success:             true,
			CompletionTokens:    intPtr(0),
			UpstreamFinishReason: nil, // ← still nil at call site (production state)
		}

		isEmpty := relay.DetectEmptyStreamResponse(m, reqLog)
		
		assert.False(t, isEmpty, "upstream_finish_reason in m['upstream_finish_reason'] → NOT empty (regression fix)")
	})
}

func strPtr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}
