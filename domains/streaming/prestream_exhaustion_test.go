package streaming

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPreStreamExhaustion_ClientReceivesErrorFrame answers a question the
// journald evidence alone cannot: when a pre-streamed request exhausts its
// retries, does the client get anything at all?
//
// Live shape on 154 (both before and after the 2026-08-08 overload work, so
// this is NOT specific to KindUpstreamOverloaded): a request that hits the
// same 5xx on every attempt logs success=false with stream_chunks=0, yet the
// HTTP status recorded is 200. That looks like "client got an empty 200".
//
// It is not empty. startPreStreamKeepalive commits 200 +
// text/event-stream BEFORE the first upstream attempt (handler.go:200), which
// is the entire point of pre-stream keepalive — it holds the connection open
// so agent clients do not time out while the executor works. Once those
// headers are on the wire the status can no longer change, so the failure is
// necessarily delivered in-band as an SSE error frame. stream_chunks counts
// CONTENT deltas, and an error frame is not content, hence 0.
//
// This test pins the guarantee that actually matters to a client: the bytes
// contain a parseable error envelope, not silence.
func TestPreStreamExhaustion_ClientReceivesErrorFrame(t *testing.T) {
	rec := httptest.NewRecorder()

	// Commit the prewarmed 200 exactly as the handler does before executing.
	psk, ok := startPreStreamKeepalive(rec, time.Hour, "req-overload-1")
	if !ok {
		t.Fatal("expected a flusher-backed recorder")
	}
	if rec.Code != 200 {
		t.Fatalf("prewarmed status = %d, want 200 committed up front", rec.Code)
	}
	psk.stop()

	// The exhaustion branch the failing production requests take.
	writePrewarmedStreamError(rec,
		"No available provider for model 'gpt-5.6-luna'. All 1 candidates failed.",
		"server_error", "model_not_found")

	body := rec.Body.String()
	if !strings.Contains(body, `data: {"error":{`) {
		t.Fatalf("client body = %q; an exhausted pre-streamed request must still carry an SSE error envelope", body)
	}
	if !strings.Contains(body, "All 1 candidates failed") {
		t.Errorf("client body = %q, want the candidate-exhaustion message", body)
	}
	if !strings.Contains(body, `"code":"model_not_found"`) {
		t.Errorf("client body = %q, want a machine-readable code", body)
	}
}

// TestPreStreamExhaustion_RetryAfterHeaderIsUnreachable documents a real
// limitation of the overload work rather than asserting a feature.
//
// serveWithExecutor sets a Retry-After header on the overload-exhaustion path.
// On the pre-streamed path that header is dead code: WriteHeader(200) has
// already flushed the header block, so any later Header().Set is silently
// dropped. This test encodes that fact so nobody "fixes" a header that cannot
// work here, and so the limitation is discoverable from the test suite instead
// of only from reading the handler.
//
// Clients on the pre-streamed path must therefore take retry guidance from the
// SSE error frame, not from Retry-After. Non-streaming and non-prewarmed
// requests still get the header, because those write their status later.
func TestPreStreamExhaustion_RetryAfterHeaderIsUnreachable(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(rec, time.Hour, "req-overload-2")
	if !ok {
		t.Fatal("expected a flusher-backed recorder")
	}
	psk.stop()

	// Attempted after the 200 is already committed — exactly the ordering in
	// serveWithExecutor's exhaustion branch.
	rec.Header().Set("Retry-After", "5")
	writePrewarmedStreamError(rec, "exhausted", "server_error", "model_not_found")

	// httptest.ResponseRecorder snapshots headers at WriteHeader into
	// Result().Header, which is what a real client would have parsed.
	if got := rec.Result().Header.Get("Retry-After"); got != "" { //nolint:bodyclose // recorder result has no body to close
		t.Fatalf("Retry-After = %q; if this ever becomes non-empty the ordering changed "+
			"and the overload-exhaustion header is now actually delivered — update the comment above", got)
	}
	// The frame remains the client's only usable signal, so it must be present.
	if !strings.Contains(rec.Body.String(), `data: {"error":{`) {
		t.Fatal("expected the SSE error frame to still be delivered")
	}
}

// TestNonPrewarmedExhaustion_RetryAfterHeaderIsDelivered is the other half of
// the pair above: it proves the overload Retry-After header is NOT dead code
// in general. Non-streaming and non-prewarmed requests write their status in
// writeErrorJSONWithKind (handler.go:6272), i.e. after serveWithExecutor has
// set the header, so those clients do receive it.
func TestNonPrewarmedExhaustion_RetryAfterHeaderIsDelivered(t *testing.T) {
	rec := httptest.NewRecorder()

	// Ordering as in the non-prewarmed exhaustion branch: header first,
	// status written by the JSON error writer afterwards.
	rec.Header().Set("Retry-After", "5")
	writeErrorJSONWithKind(rec, 503, "req-overload-3",
		"No available provider for model 'gpt-5.6-luna'. All 1 candidates failed.",
		"server_error", "model_not_found", "upstream_overloaded", nil)

	res := rec.Result() //nolint:bodyclose // recorder result, no network body
	if got := res.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want \"5\" on the non-prewarmed path", got)
	}
	if res.StatusCode != 503 {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if !strings.Contains(rec.Body.String(), `"kind":"upstream_overloaded"`) {
		t.Errorf("body = %q, want the precise kind for client-side alerting", rec.Body.String())
	}
}
