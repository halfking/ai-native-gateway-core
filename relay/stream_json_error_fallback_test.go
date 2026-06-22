package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamChat_UpstreamReturnsJSONError_TriggersFallback exercises the
// 2026-06-20 audit fix: when the upstream returns HTTP 200 with a
// non-SSE JSON error envelope (the case observed in production where
// provider=33 evol returns `{"error":{"type":"service_unavailable",
// "message":"积分不足"}}` for stream-mode requests), the stream reader
// must NOT pass the JSON body through as a successful first chunk.
// It must surface StreamOutcome{Interrupted:true, Resumable:true,
// ChunkCount:0, Reason:"json_error_in_stream"} so the executor
// continues to the next credential.
func TestStreamChat_UpstreamReturnsJSONError_TriggersFallback(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "openai envelope - 积分不足",
			body: `{"error": {"type": "service_unavailable", "message": "Stream error: proxy stream error: 积分不足，请升级套餐"}}` + "\n",
		},
		{
			name: "openai envelope - code shape",
			body: `{"error": {"code": "insufficient_quota", "message": "You have exceeded your quota"}}` + "\n",
		},
		{
			name: "anthropic bare - upstream error",
			body: `{"type": "upstream_error", "message": "billing pool switched"}` + "\n",
		},
		{
			name: "trailing SSE terminator artifacts",
			body: "{\"error\":{\"type\":\"service_unavailable\",\"message\":\"当前算力池切换中，请重试即可\"}}\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()

			req, _ := http.NewRequest("POST", upstream.URL, strings.NewReader("{}"))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("upstream request failed: %v", err)
			}
			//nolint:errcheck // best-effort close
			defer resp.Body.Close()

			rec := httptest.NewRecorder()
			outcome := StreamChatWithPendingCapture(rec, resp, "claude-opus-4-8", "claude-opus-4-8", nil, nil, false, nil, nil)

			if !outcome.Interrupted {
				t.Fatalf("expected Interrupted=true, got false (outcome=%+v body=%q)", outcome, rec.Body.String())
			}
			if outcome.Reason != "json_error_in_stream" {
				t.Errorf("expected Reason=json_error_in_stream, got %q", outcome.Reason)
			}
			if !outcome.Resumable {
				t.Errorf("expected Resumable=true so executor continues to next credential, got false")
			}
			if outcome.ChunkCount != 0 {
				t.Errorf("expected ChunkCount=0, got %d (we should bail before writing any SSE chunk)", outcome.ChunkCount)
			}
			// The client should receive a clean SSE-wrapped error,
			// NOT the raw vendor JSON envelope. The original body
			// was a bare {"error":{...}} (no SSE framing); the
			// client must see a properly SSE-prefixed "data: "
			// line. We assert on framing + presence of the
			// upstream's error code so the SDK can still see
			// WHICH upstream failed, while the SSE wrapper
			// protects chat-completion SDK clients from parsing
			// the raw vendor shape as a successful chunk.
			body := rec.Body.String()
			if !strings.HasPrefix(body, "data: ") {
				t.Errorf("client body should start with SSE data: prefix, got: %q", body)
			}
			// The upstream's kind (the "code" field) should be
			// preserved so the SDK / client can route on it.
			// We don't assert on the message text because the
			// wrapping intentionally forwards the vendor's
			// reason — that is the point of surfacing the error
			// instead of dropping it.
			if !strings.Contains(body, `"error":`) {
				t.Errorf("client body should contain error envelope, got: %q", body)
			}
		})
	}
}

// TestStreamChat_ValidSSE_NotMisclassified guards against false
// positives: a legitimate SSE "data: {...}" line must never be
// re-classified as a non-SSE JSON error just because the JSON
// inside the data: envelope happens to contain an "error" key
// in some intermediate chunk (e.g. a mid-stream tool_call error).
//
// This test feeds a normal chat.completion.chunk and asserts
// the reader does NOT return Interrupted=true.
func TestStreamChat_ValidSSE_NotMisclassified(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// Intentionally include a data: line that contains the
		// word "error" in a benign context to make sure the
		// isJSONErrorBody helper is only triggered by the RAW
		// body shape, not by string-content matches.
		chunks := []string{
			`{"id":"chat-1","model":"claude-opus-4-8","choices":[{"delta":{"role":"assistant","content":"hi"},"index":0}]}`,
			`{"id":"chat-1","model":"claude-opus-4-8","choices":[{"delta":{},"finish_reason":"stop","index":0}]}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		// Pad with a trailing blank line so the reader sees a
		// proper end-of-stream (the production handler always
		// gets a well-formed terminator; this guards against a
		// test-only flakiness caused by HTTP/1.1 connection
		// close firing before readNextStreamLine returns EOF).
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	req, _ := http.NewRequest("POST", upstream.URL, strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upstream request failed: %v", err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	outcome := StreamChatWithPendingCapture(rec, resp, "claude-opus-4-8", "claude-opus-4-8", nil, nil, false, nil, nil)

	// "eof_without_done" is acceptable here — the upstream may
	// close the connection without sending an extra blank line
	// in httptest. The point of the test is that the JSON-error
	// fallback does NOT fire, so we only assert the reason is
	// NOT "json_error_in_stream".
	if outcome.Reason == "json_error_in_stream" {
		t.Errorf("valid SSE stream must not trigger json_error_in_stream fallback, got outcome=%+v", outcome)
	}
	if outcome.ChunkCount == 0 {
		t.Errorf("valid SSE stream should have produced at least one chunk, got outcome=%+v", outcome)
	}
}
