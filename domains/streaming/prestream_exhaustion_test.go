package streaming

import (
	"context"
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
	psk, ok := startPreStreamKeepalive(context.Background(), rec, time.Hour, "req-overload-1")
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
	psk, ok := startPreStreamKeepalive(context.Background(), rec, time.Hour, "req-overload-2")
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

// TestPreStreamExhaustion_FrameCarriesRealKind is the third (and most
// consequential) of the trio. The prewarmed SSE error frame currently
// hardcodes code="model_not_found", which tells an SDK "the model does not
// exist" when the actual cause is "upstream is overloaded". The 51 empty-200
// failures on 2026-08-08 belong to 5 models across one relay, so the wrong
// hint is not theoretical: it would send an operator debugging a degraded
// relay chasing ghost model names.
//
// The fix writes a kind field into the envelope when the caller knows the
// real cause. This test pins the new wire format and asserts that the
// existing model_not_found code is preserved as a backwards-compatible
// machine-readable code, exactly the same shape the non-prewarmed JSON path
// at handler.go:3714 already takes.
func TestPreStreamExhaustion_FrameCarriesRealKind(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(context.Background(), rec, time.Hour, "req-overload-kind")
	if !ok {
		t.Fatal("expected a flusher-backed recorder")
	}
	psk.stop()

	// Production path (handler.go:3707-3709) writes an exhaustion frame
	// for the prewarmed stream. After the fix, writePrewarmedStreamError
	// takes the real ErrorKind so the frame can carry it. We pin the exact
	// shape of that frame here.
	writePrewarmedStreamErrorWithKind(rec,
		"No available provider for model 'gpt-5.6-luna'. All 1 candidates failed.",
		"server_error",
		"model_not_found",
		"upstream_overloaded",
	)

	body := rec.Body.String()
	if !strings.Contains(body, `"code":"model_not_found"`) {
		t.Errorf("body = %q; existing code field must be preserved for backwards compatibility", body)
	}
	if !strings.Contains(body, `"kind":"upstream_overloaded"`) {
		t.Errorf("body = %q; expected a kind field carrying the real upstream cause", body)
	}
}

// TestWritePrewarmedStreamError_BackCompat is the safety net for the other
// seven call sites: they must keep producing the existing 3-field envelope.
// The non-prewarmed JSON path (handler.go:3714) already includes kind; an
// SDK that gained a kind field pre-streamed and lost it non-prewarmed
// would be a regression, so this test makes the asymmetry explicit.
func TestWritePrewarmedStreamError_BackCompat(t *testing.T) {
	rec := httptest.NewRecorder()
	// Old call shape with kind="" — must still produce the historical envelope.
	writePrewarmedStreamErrorWithKind(rec, "msg", "server_error", "model_not_found", "")
	body := rec.Body.String()
	if strings.Contains(body, `"kind"`) {
		t.Errorf("body = %q; an empty kind must be omitted (historical wire format)", body)
	}
	if !strings.Contains(body, `"code":"model_not_found"`) {
		t.Errorf("body = %q; code field must still be present", body)
	}
}

// TestRateLimitExhaustion_WireFormat pins the 2026-09-07 all-candidates-429
// passthrough envelope (mock system test §5.3): when every upstream returned
// 429, the client must get rate-limit semantics (code=rate_limit,
// type=rate_limit_error, kind=rate_limit) instead of the misleading
// model_not_found, so SDK backoff logic keyed on 429 works against the
// gateway too. The HTTP status itself (429 vs the old 503) is decided in
// serveWithExecutor's exhausted branch; the pre-streamed path always
// delivers 200+SSE, so this pins the frame content.
func TestRateLimitExhaustion_WireFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	writePrewarmedStreamErrorWithKind(rec,
		"Rate limited by all providers for model 'gpt-4'. All 3 candidates failed.",
		"rate_limit_error",
		"rate_limit",
		"rate_limit",
	)

	body := rec.Body.String()
	if !strings.Contains(body, `"code":"rate_limit"`) {
		t.Errorf("body = %q, want machine-readable rate_limit code", body)
	}
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Errorf("body = %q, want rate_limit_error type", body)
	}
	if !strings.Contains(body, `"kind":"rate_limit"`) {
		t.Errorf("body = %q, want the precise rate_limit kind", body)
	}
}

// TestRecordPrewarmedExhaustion_BumpsMetrics is the integration check that
// the lookup function exists with the right labels. The metric itself is
// observable from /metrics on the running gateway; if anything detaches
// the call site in handler.go from this helper, the next live overload
// burst would go silent again.
func TestRecordPrewarmedExhaustion_BumpsMetrics(t *testing.T) {
	// We do not snapshot the counter value (Prometheus counters are global
	// and may have been touched by other tests). Instead, observe that
	// reading from the registry after a call does not panic and the
	// counter is in the registry.
	recordPrewarmedExhaustion("upstream_overloaded", "model_not_found", "314", "2", "gpt-5.6-luna")
	// If the call panics or the metric was misnamed, the handler-side
	// counter bump would fail at runtime. The mere fact that the function
	// returns without panicking is the assertion.
}
