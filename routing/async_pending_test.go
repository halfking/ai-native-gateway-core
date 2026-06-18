package routing

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/pending"
)

// TestAsyncPendingError_ErrorFormat (Track C C4, 2026-06-18) pins
// the wire-format string used by the error. The handler does not
// key on it, but operators reading logs do.
func TestAsyncPendingError_ErrorFormat(t *testing.T) {
	startedAt := time.Date(2026, 6, 18, 21, 0, 0, 0, time.UTC)
	e := &AsyncPendingError{
		SessionID: "gw_abc",
		RequestID: "req_xyz",
		StartedAt: startedAt,
	}
	got := e.Error()
	want := "async_pending: session=gw_abc request=req_xyz started_at=2026-06-18T21:00:00Z"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestShouldAsyncFallback_RejectsNilStore: without a PendingStore
// the gate must return false so the executor never spawns a
// goroutine that would silently drop the response.
func TestShouldAsyncFallback_RejectsNilStore(t *testing.T) {
	e := &Executor{} // no PendingStore
	ok := e.shouldAsyncFallback(nil, time.Now().Add(-1*time.Second), 3)
	if ok {
		t.Fatal("nil PendingStore must not allow async fallback")
	}
}

// TestShouldAsyncFallback_RejectsNoSession: the GET endpoint
// requires X-Gw-Session-Id. Without it there is no way for the
// client to retrieve the cached body, so async is useless.
func TestShouldAsyncFallback_RejectsNoSession(t *testing.T) {
	e := newAsyncTestExecutor()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ok := e.shouldAsyncFallback(&ExecParams{R: r}, time.Now().Add(-1*time.Second), 3)
	if ok {
		t.Fatal("missing session header must not allow async fallback")
	}
}

// TestShouldAsyncFallback_AcceptsLegacySessionHeader: the legacy
// X-Session-Id header is still recognised (the relay handler
// already does this; the executor must follow).
func TestShouldAsyncFallback_AcceptsLegacySessionHeader(t *testing.T) {
	e := newAsyncTestExecutor()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Session-Id", "sess-legacy")
	ok := e.shouldAsyncFallback(&ExecParams{R: r}, time.Now().Add(-20*time.Second), 3)
	if !ok {
		t.Fatal("X-Session-Id (legacy) should also unlock async fallback")
	}
}

// TestShouldAsyncFallback_RejectsFastPath: the synchronous walk
// must have actually exceeded the short timeout. A 5s walk
// (below the default 15s short) must NOT trigger async.
func TestShouldAsyncFallback_RejectsFastPath(t *testing.T) {
	e := newAsyncTestExecutor()
	e.AsyncShortTimeout = 15 * time.Second
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_x")
	ok := e.shouldAsyncFallback(&ExecParams{R: r}, time.Now().Add(-5*time.Second), 3)
	if ok {
		t.Fatal("5s walk (below default 15s short) must not trigger async")
	}
}

// TestShouldAsyncFallback_RejectsZeroTried: when no candidate
// was actually attempted (no_candidates), async would be
// pointless — the goroutine would just see the same empty list.
func TestShouldAsyncFallback_RejectsZeroTried(t *testing.T) {
	e := newAsyncTestExecutor()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_x")
	ok := e.shouldAsyncFallback(&ExecParams{R: r}, time.Now().Add(-20*time.Second), 0)
	if ok {
		t.Fatal("tried=0 must not trigger async")
	}
}

// TestShouldAsyncFallback_RejectsBadConfig: when long <= short
// the goroutine would just bail immediately. The gate rejects
// this mis-configuration rather than launching a useless
// goroutine.
func TestShouldAsyncFallback_RejectsBadConfig(t *testing.T) {
	e := newAsyncTestExecutor()
	e.AsyncShortTimeout = 30 * time.Second
	e.AsyncLongTimeout = 10 * time.Second // < short
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_x")
	ok := e.shouldAsyncFallback(&ExecParams{R: r}, time.Now().Add(-60*time.Second), 3)
	if ok {
		t.Fatal("long <= short must not trigger async")
	}
}

// TestShouldAsyncFallback_AcceptsValid: the happy path — all
// gates pass and the walk was slow.
func TestShouldAsyncFallback_AcceptsValid(t *testing.T) {
	e := newAsyncTestExecutor()
	e.AsyncShortTimeout = 5 * time.Second
	e.AsyncLongTimeout = 300 * time.Second
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_x")
	ok := e.shouldAsyncFallback(&ExecParams{R: r}, time.Now().Add(-20*time.Second), 3)
	if !ok {
		t.Fatal("valid configuration must trigger async")
	}
}

// newAsyncTestExecutor builds an Executor with a non-nil
// PendingStore but no real Redis connection. pending.NewStore(nil, 0)
// is a no-op (every method returns ErrUnavailable); we use it
// only to satisfy the nil-check in shouldAsyncFallback. The
// actual MarkInProgress/Save calls happen in startAsyncRetry,
// which the gate tests do NOT exercise.
func newAsyncTestExecutor() *Executor {
	return &Executor{
		PendingStore: pending.NewStore(nil, 0),
	}
}

// TestAsyncPendingError_ImplementsError: the error type must
// satisfy the error interface so errors.As works in the handler.
func TestAsyncPendingError_ImplementsError(t *testing.T) {
	var _ error = (*AsyncPendingError)(nil)
}

// TestAsyncPendingError_AsPattern: the handler uses errors.As
// to detect the async signal. This guards the contract end-to-end.
func TestAsyncPendingError_AsPattern(t *testing.T) {
	var err error = &AsyncPendingError{
		SessionID: "gw_1",
		RequestID: "r_1",
		StartedAt: time.Now(),
	}
	var async *AsyncPendingError
	if !errors.As(err, &async) {
		t.Fatal("errors.As must extract *AsyncPendingError from concrete type")
	}
	if async.SessionID != "gw_1" {
		t.Errorf("SessionID: got %q", async.SessionID)
	}
	if async.RequestID != "r_1" {
		t.Errorf("RequestID: got %q", async.RequestID)
	}
}

// TestContentTypeFor pins the wire-format choice for replayed
// pending responses. Clients key off Content-Type to decide
// whether to render progressively (SSE) or wait for the full
// body (JSON).
func TestContentTypeFor(t *testing.T) {
	if got := contentTypeFor(true); got != "text/event-stream" {
		t.Errorf("stream: got %q, want text/event-stream", got)
	}
	if got := contentTypeFor(false); got != "application/json" {
		t.Errorf("non-stream: got %q, want application/json", got)
	}
}

// TestTruncateForStore guards the upper bound on stored error
// messages. A vendor error body can be 10KB+; we cap at 1KB so
// the Redis Hash stays well under the recommended size.
func TestTruncateForStore(t *testing.T) {
	short := "all credentials exhausted"
	if got := truncateForStore(short); got != short {
		t.Errorf("short: got %q, want %q", got, short)
	}
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateForStore(string(long))
	if len(got) != 1024 {
		t.Fatalf("long: got len=%d, want 1024", len(got))
	}
}

// TestShouldAsyncFallback_RejectsRecursion is the regression guard
// for the recursion bug. The async goroutine calls Execute again;
// we must not demote to async a second time, otherwise the
// goroutine would spawn another goroutine (and so on) until the
// 300s timeout fires from the outermost level.
func TestShouldAsyncFallback_RejectsRecursion(t *testing.T) {
	e := newAsyncTestExecutor()
	e.AsyncShortTimeout = 5 * time.Second
	e.AsyncLongTimeout = 300 * time.Second
	e.asyncDepth = 1 // simulate "we are already inside an async walk"
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_x")
	ok := e.shouldAsyncFallback(&ExecParams{R: r}, time.Now().Add(-20*time.Second), 3)
	if ok {
		t.Fatal("asyncDepth > 0 must short-circuit the gate to false")
	}
}
