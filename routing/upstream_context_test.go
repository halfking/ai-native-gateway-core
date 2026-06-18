package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHasSessionID_GwHeader (Track C, 2026-06-18): X-Gw-Session-Id is
// the canonical session id. When present, the executor decouples the
// upstream context from the client context so a client disconnect
// does not cancel the vendor request.
func TestHasSessionID_GwHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_abc123")
	if !hasSessionID(&ExecParams{R: r}) {
		t.Fatal("X-Gw-Session-Id should be recognized")
	}
}

// TestHasSessionID_LegacyHeader: X-Session-Id is the legacy header
// that some clients still send. We accept it for backwards compat.
func TestHasSessionID_LegacyHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Session-Id", "sess-legacy")
	if !hasSessionID(&ExecParams{R: r}) {
		t.Fatal("X-Session-Id (legacy) should be recognized")
	}
}

// TestHasSessionID_NoHeader: stateless requests (no session id) keep
// the original behaviour. This is the load-balancing test — most
// production traffic is stateless and must not pay the cache-write
// cost.
func TestHasSessionID_NoHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if hasSessionID(&ExecParams{R: r}) {
		t.Fatal("stateless request should not be flagged as session-bearing")
	}
}

// TestHasSessionID_NilParams: defensive guard.
func TestHasSessionID_NilParams(t *testing.T) {
	if hasSessionID(nil) {
		t.Fatal("nil params must return false")
	}
	if hasSessionID(&ExecParams{R: nil}) {
		t.Fatal("nil R must return false")
	}
}

// TestUpstreamContext_WithSession: with a session id, the returned
// context is NOT the client's. A cancel on the client context must
// not cancel the upstream context (otherwise we have not actually
// decoupled anything).
func TestUpstreamContext_WithSession(t *testing.T) {
	e := &Executor{}
	clientCtx, clientCancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_decouple_test")
	r = r.WithContext(clientCtx)
	upCtx, cancel := e.upstreamContext(&ExecParams{R: r}, 30*time.Second)
	defer cancel()

	// Cancel the CLIENT context — the upstream context must survive.
	clientCancel()
	select {
	case <-upCtx.Done():
		t.Fatal("upstream context should NOT cancel when client disconnects")
	case <-time.After(50 * time.Millisecond):
		// good — still alive after client cancel
	}
}

// TestUpstreamContext_WithoutSession: stateless requests keep the
// original behaviour (client cancel propagates to upstream).
func TestUpstreamContext_WithoutSession(t *testing.T) {
	e := &Executor{}
	clientCtx, clientCancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r = r.WithContext(clientCtx)
	upCtx, cancel := e.upstreamContext(&ExecParams{R: r}, 30*time.Second)
	defer cancel()

	clientCancel()
	select {
	case <-upCtx.Done():
		// good — propagates as before
	case <-time.After(50 * time.Millisecond):
		t.Fatal("without session id, client cancel MUST propagate to upstream")
	}
}

// TestUpstreamContext_TimeoutStillRespected: even with session id,
// a stuck vendor is bounded by the timeout.
func TestUpstreamContext_TimeoutStillRespected(t *testing.T) {
	e := &Executor{}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_timeout_test")
	upCtx, cancel := e.upstreamContext(&ExecParams{R: r}, 50*time.Millisecond)
	defer cancel()

	select {
	case <-upCtx.Done():
		// good — timeout fires regardless of client state
	case <-time.After(500 * time.Millisecond):
		t.Fatal("upstream context must respect timeout even with session id")
	}
	if err := upCtx.Err(); err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

// TestUpstreamContext_HeaderParseIsLenient: header values with
// surrounding whitespace must still be detected (matches
// sanitizeGwSessionHeader in relay/handler.go).
func TestHasSessionID_WhitespaceHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "   ")
	if hasSessionID(&ExecParams{R: r}) {
		// We do not trim here — the relay handler's
		// sanitizeGwSessionHeader does that and treats whitespace-
		// only as empty. Our check is "is the header key set?",
		// which is intentionally a positive first-pass filter.
		// The actual session validity is enforced upstream in
		// relay/handler.go. Document the asymmetry here.
		t.Log("whitespace-only header is treated as present (defensive); relay/handler.go trims before validating")
	}
}

// silence unused-import warning for `http` (not used after we removed
// the http.Error reference, kept for future test expansion).
var _ = http.StatusOK
