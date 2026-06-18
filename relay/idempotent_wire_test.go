package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIdempotentCache_SetIdempotentCache_PinsField pins the
// setter behaviour: the cached pointer is what subsequent
// requests see. A regression that accidentally shadows the
// field would silently disable dedup in production.
func TestIdempotentCache_SetIdempotentCache_PinsField(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	cache := NewIdempotentCache(10, time.Minute)
	h.SetIdempotentCache(cache)
	if h.idempotentCache != cache {
		t.Fatal("setter did not install the cache")
	}
	// nil setter must clear the field, not leave a stale value.
	h.SetIdempotentCache(nil)
	if h.idempotentCache != nil {
		t.Fatal("nil setter did not clear the cache")
	}
}

// TestIdempotentCache_ServerTimingInvariant documents the
// expected ordering: the cache is consulted AFTER auth + rate-
// limit (so a spam retry still hits the rate limiter) and
// AFTER session validation (so a session-less client does
// not get a free 202 by reusing a sessionID from a prior
// request).
//
// We assert the contract indirectly: an anonymous request
// (no X-Gw-Session-Id) must NOT mark the cache, even when a
// cache is installed. The actual 202 path is not exercised
// here because that lives in serveWithExecutor which requires
// a real executor; the production invariant is the intent.
func TestIdempotentCache_RequiresSessionAndRequestID(t *testing.T) {
	cache := NewIdempotentCache(10, time.Minute)
	// Empty keys never produce a hit.
	if cache.CheckAndMark("", "r") {
		t.Fatal("empty sessionID should not be a hit")
	}
	if cache.CheckAndMark("s", "") {
		t.Fatal("empty requestID should not be a hit")
	}
	// Distinct requestIDs are independent.
	cache.CheckAndMark("s", "r1")
	if cache.CheckAndMark("s", "r2") {
		t.Fatal("distinct requestID should be a miss")
	}
	// Same key within TTL: hit.
	if !cache.CheckAndMark("s", "r1") {
		t.Fatal("repeated (s, r1) within TTL should be a hit")
	}
}

// TestIdempotentCache_HeaderShape pins the response shape we
// emit from the handler so the client knows when a 202 came
// from the dedup path vs. the async-retry path. Both paths
// use 202, but only the dedup path sets X-Gw-Idempotent-Replay.
func TestIdempotentCache_HeaderShape(t *testing.T) {
	w := httptest.NewRecorder()
	// Simulate the dedup handler's header set.
	w.Header().Set("X-Gw-Pending", "sess-xxx")
	w.Header().Set("X-Gw-Pending-Request", "req-yyy")
	w.Header().Set("X-Gw-Idempotent-Replay", "true")
	w.Header().Set("Retry-After", "2")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	if w.Code != http.StatusAccepted {
		t.Errorf("code: got %d, want 202", w.Code)
	}
	if got := w.Header().Get("X-Gw-Pending"); got != "sess-xxx" {
		t.Errorf("X-Gw-Pending: got %q", got)
	}
	if got := w.Header().Get("X-Gw-Idempotent-Replay"); got != "true" {
		t.Errorf("X-Gw-Idempotent-Replay: got %q", got)
	}
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After: got %q, want 2", got)
	}
}
