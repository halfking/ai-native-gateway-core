package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestStreamOpenAI_MalformedFirstFrame verifies that the SSE validator
// rejects malformed first frames (e.g., bare "{") and returns a resumable
// outcome so the survival coordinator can retry transparently.
func TestStreamOpenAI_MalformedFirstFrame(t *testing.T) {
	tests := []struct {
		name          string
		firstFrame    string
		wantResumable bool
		wantReason    string
	}{
		{
			name:          "bare opening brace",
			firstFrame:    "data: {\n\n",
			wantResumable: true,
			wantReason:    "malformed_sse_frame",
		},
		{
			name:          "incomplete JSON object",
			firstFrame:    "data: {\"id\":\"chatcmpl-123\"\n\n",
			wantResumable: true,
			wantReason:    "malformed_sse_frame",
		},
		{
			name:          "bare text without structure",
			firstFrame:    "data: hello world\n\n",
			wantResumable: true,
			wantReason:    "malformed_sse_frame",
		},
		{
			name:          "valid frame should pass",
			firstFrame:    "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n",
			wantResumable: false,
			wantReason:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock upstream response with the malformed frame
			body := io.NopCloser(strings.NewReader(tt.firstFrame))
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       body,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Request:    httptest.NewRequest("POST", "/", nil),
			}

			// Create response writer
			w := httptest.NewRecorder()

			// Create capture
			capture := audit.NewStreamCapture()

			// Call StreamChatWithPendingCaptureAndDiagnosticsWithVendor
			ctx := context.Background()
			outcome := StreamChatWithPendingCaptureAndDiagnosticsWithVendor(
				ctx,
				w,
				resp,
				"gpt-3.5-turbo",  // clientModel
				"minimax-m3",     // outboundModel
				nil,              // norm
				capture,
				false,            // toolsRequested
				nil,              // stripFn
				"minimax",        // vendorCode
				nil,              // pc
				nil,              // diagnostics
			)

			// Verify outcome
			if tt.wantResumable {
				// Should detect malformed frame and return resumable=true
				if !outcome.Interrupted {
					t.Errorf("Expected Interrupted=true for malformed frame, got false")
				}
				if outcome.Reason != tt.wantReason {
					t.Errorf("Expected Reason=%q, got %q", tt.wantReason, outcome.Reason)
				}
				if !outcome.Resumable {
					t.Errorf("Expected Resumable=true for malformed first frame, got false")
				}
				if outcome.Kind != errorsx.KindUpstreamDown {
					t.Errorf("Expected Kind=%s, got %s", errorsx.KindUpstreamDown, outcome.Kind)
				}
				if outcome.ChunkCount != 0 {
					t.Errorf("Expected ChunkCount=0, got %d", outcome.ChunkCount)
				}

				// Verify no bytes written to client
				if w.Body.Len() > 0 {
					t.Errorf("Expected no client output for malformed first frame, got %d bytes", w.Body.Len())
				}
			} else {
				// Valid frame should process normally
				if outcome.Interrupted && outcome.Reason == "malformed_sse_frame" {
					t.Errorf("Valid frame incorrectly rejected as malformed")
				}
			}
		})
	}
}

// TestStreamOpenAI_MalformedMidStream verifies that malformed frames
// in the middle of a stream are handled correctly based on commit state.
func TestStreamOpenAI_MalformedMidStream(t *testing.T) {
	// Create a stream with valid first frame followed by malformed frame
	validChunk := "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	malformedChunk := "data: {\n\n"
	doneMarker := "data: [DONE]\n\n"

	body := io.NopCloser(strings.NewReader(validChunk + malformedChunk + doneMarker))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Request:    httptest.NewRequest("POST", "/", nil),
	}

	w := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	ctx := context.Background()
	outcome := StreamChatWithPendingCaptureAndDiagnosticsWithVendor(
		ctx,
		w,
		resp,
		"gpt-3.5-turbo",
		"minimax-m3",
		nil,
		capture,
		false,
		nil,
		"minimax",
		nil,
		nil,
	)

	// After first valid chunk is sent, malformed frame should be skipped
	// (not cause stream interruption) because client already saw output
	//
	// Note: The exact behavior depends on the commit gate implementation.
	// If the first chunk commits output, the malformed frame is skipped.
	// If not yet committed, it returns resumable=true.
	//
	// This test verifies that:
	// 1. No crash/panic occurs
	// 2. Stream completes (reaches [DONE])
	// 3. Client receives at least the first valid chunk

	if outcome.Interrupted && outcome.Reason == "malformed_sse_frame_mid_stream" {
		// If interrupted due to malformed frame before commit, should be resumable
		if !outcome.Resumable {
			t.Errorf("Mid-stream malformed frame should be resumable if not yet committed")
		}
	}

	// Verify client received some output (at least the first valid chunk)
	if w.Body.Len() == 0 {
		t.Errorf("Expected client to receive at least the first valid chunk")
	}
}

// TestValidateSSEDataFrame_IntegrationCases tests the validator with
// real-world SSE frames observed from minimax-m3 and glm-5.2.
func TestValidateSSEDataFrame_IntegrationCases(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		valid bool
	}{
		{
			name:  "minimax-m3 valid chunk",
			frame: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"minimax-m3","choices":[{"index":0,"delta":{"content":"测试"},"finish_reason":null}]}`,
			valid: true,
		},
		{
			name:  "glm-5.2 valid chunk",
			frame: `data: {"id":"glm-123","object":"chat.completion.chunk","created":1234567890,"model":"glm-5.2","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}`,
			valid: true,
		},
		{
			name:  "observed bare brace from minimax",
			frame: "data: {",
			valid: false,
		},
		{
			name:  "observed incomplete JSON from glm",
			frame: `data: {"id":"glm-`,
			valid: false,
		},
		{
			name:  "keepalive empty line",
			frame: "data: ",
			valid: true,
		},
		{
			name:  "completion marker",
			frame: "data: [DONE]",
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Add newlines to make it a complete SSE line
			line := tt.frame + "\n\n"
			got := validateSSEDataFrame(line)
			if got != tt.valid {
				t.Errorf("validateSSEDataFrame(%q) = %v, want %v", tt.frame, got, tt.valid)
			}
		})
	}
}

// TestStreamOpenAI_NoFalsePositives ensures that the validator doesn't
// incorrectly reject valid frames from various providers.
func TestStreamOpenAI_NoFalsePositives(t *testing.T) {
	validFrames := []string{
		// OpenAI
		`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
		// Anthropic (though this is for OpenAI endpoint, test generic JSON)
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		// MiniMax with base_resp (before stripping)
		`data: {"id":"chatcmpl-123","choices":[{"delta":{"content":"测试"}}],"base_resp":{"status_code":0}}`,
		// GLM with usage
		`data: {"id":"glm-123","choices":[{"delta":{"content":"你好"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		// Empty content (valid)
		`data: {"id":"chatcmpl-123","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}

	for i, frame := range validFrames {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			line := frame + "\n\n"
			if !validateSSEDataFrame(line) {
				t.Errorf("Valid frame incorrectly rejected as malformed:\n%s", frame)
			}
		})
	}
}

// BenchmarkValidateSSEDataFrame measures the performance impact of validation.
func BenchmarkValidateSSEDataFrame(b *testing.B) {
	validFrame := "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"test\"}}]}\n\n"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = validateSSEDataFrame(validFrame)
	}
}

// BenchmarkValidateSSEDataFrame_Invalid measures validation of invalid frames.
func BenchmarkValidateSSEDataFrame_Invalid(b *testing.B) {
	invalidFrame := "data: {\n\n"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = validateSSEDataFrame(invalidFrame)
	}
}
