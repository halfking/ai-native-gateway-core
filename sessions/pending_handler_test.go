package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// stubPendingStore is a hand-rolled PendingStore used by the handler
// tests below. We avoid constructing a real *pending.Store (which
// needs a live Redis) so these tests run in the sessions package's
// own `go test` invocation without external dependencies.
type stubPendingStore struct {
	getFn     func(ctx context.Context, sessionID, requestID string) (*PendingEntry, bool, error)
	latestFn  func(ctx context.Context, sessionID string) (*PendingEntry, string, bool, error)
}

func (s *stubPendingStore) Get(ctx context.Context, sessionID, requestID string) (*PendingEntry, bool, error) {
	return s.getFn(ctx, sessionID, requestID)
}

func (s *stubPendingStore) GetLatest(ctx context.Context, sessionID string) (*PendingEntry, string, bool, error) {
	return s.latestFn(ctx, sessionID)
}

// pendingTestEntry is a small helper that returns a "completed"
// entry for a session with a streaming SSE body. Used by the
// 200-replay tests below.
func pendingTestEntry(sid, rid, body string, status string) *PendingEntry {
	return &PendingEntry{
		SessionID:   sid,
		RequestID:   rid,
		Status:      status,
		Body:        body,
		ContentType: "text/event-stream",
		IsStream:    true,
	}
}

func TestGetPendingResponse_NilStoreReturns503(t *testing.T) {
	h := NewHandler(nil) // no session manager, no auth, no store
	r := httptest.NewRequest("GET", "/v1/sessions/sess-x/pending-response", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store: got %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PENDING_STORE_UNAVAILABLE") {
		t.Fatalf("body: got %q, want PENDING_STORE_UNAVAILABLE", w.Body.String())
	}
}

func TestGetPendingResponse_200CompletedSSE(t *testing.T) {
	const body = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	store := &stubPendingStore{
		getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
			return pendingTestEntry(sid, rid, body, "completed"), true, nil
		},
	}
	h := NewHandler(nil)
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET",
		"/v1/sessions/sess-1/pending-response?request_id=req-1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", got)
	}
	if got := w.Header().Get("X-Gw-Pending-Replay"); got != "true" {
		t.Errorf("X-Gw-Pending-Replay: got %q, want true", got)
	}
	if got := w.Body.String(); got != body {
		t.Errorf("body: got %q, want %q", got, body)
	}
}

func TestGetPendingResponse_202InProgress(t *testing.T) {
	store := &stubPendingStore{
		getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
			return pendingTestEntry(sid, rid, "", "in_progress"), true, nil
		},
	}
	h := NewHandler(nil)
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET",
		"/v1/sessions/sess-2/pending-response?request_id=req-2", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After: got %q, want 5", got)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "in_progress" {
		t.Errorf("status: got %v, want in_progress", body["status"])
	}
	if body["retry_after"] != float64(5) {
		t.Errorf("retry_after: got %v, want 5", body["retry_after"])
	}
}

func TestGetPendingResponse_200Failed(t *testing.T) {
	store := &stubPendingStore{
		getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
			e := pendingTestEntry(sid, rid, "", "failed")
			e.ErrorMessage = "all credentials exhausted"
			return e, true, nil
		},
	}
	h := NewHandler(nil)
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET",
		"/v1/sessions/sess-3/pending-response?request_id=req-3", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (failed body is still a valid response)", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "failed" {
		t.Errorf("status: got %v", body["status"])
	}
	if body["error_message"] != "all credentials exhausted" {
		t.Errorf("error_message: got %v", body["error_message"])
	}
}

func TestGetPendingResponse_404NotFound(t *testing.T) {
	store := &stubPendingStore{
		getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
			return nil, false, nil
		},
	}
	h := NewHandler(nil)
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET",
		"/v1/sessions/sess-4/pending-response?request_id=req-4", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PENDING_NOT_FOUND") {
		t.Errorf("body: got %q", w.Body.String())
	}
}

func TestGetPendingResponse_404HidesMissingSessionDetail(t *testing.T) {
	// When the session row is not in the session manager AND the
	// pending lookup returns not-found, we still return 404 with
	// the generic "no pending response" message. This avoids
	// leaking whether a session id exists in the system.
	store := &stubPendingStore{
		getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
			return nil, false, nil
		},
	}
	h := NewHandler(nil) // nil session manager → all session lookups return ErrSessionNotFound
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET",
		"/v1/sessions/sess-orphan/pending-response?request_id=req-x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("orphan session: got %d, want 404 (not 403)", w.Code)
	}
}

func TestGetPendingResponse_StoreErrorReturns503(t *testing.T) {
	store := &stubPendingStore{
		getFn: func(_ context.Context, _, _ string) (*PendingEntry, bool, error) {
			return nil, false, errors.New("redis dial: connection refused")
		},
	}
	h := NewHandler(nil)
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET",
		"/v1/sessions/sess-5/pending-response?request_id=req-5", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PENDING_STORE_ERROR") {
		t.Errorf("body: got %q", w.Body.String())
	}
}

func TestGetPendingResponse_GetLatestFallback(t *testing.T) {
	// When no request_id is supplied, the handler should fall back
	// to GetLatest. We pin this with a stub that returns from
	// latestFn only.
	store := &stubPendingStore{
		getFn: func(_ context.Context, _, _ string) (*PendingEntry, bool, error) {
			t.Fatal("Get should NOT be called when no request_id is supplied")
			return nil, false, nil
		},
		latestFn: func(_ context.Context, sid string) (*PendingEntry, string, bool, error) {
			return pendingTestEntry(sid, "req-latest", "data: [DONE]\n\n", "completed"),
				"req-latest", true, nil
		},
	}
	h := NewHandler(nil)
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET", "/v1/sessions/sess-latest/pending-response", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Gw-Pending-Request"); got != "req-latest" {
		t.Errorf("X-Gw-Pending-Request: got %q, want req-latest", got)
	}
}

func TestGetPendingResponse_UnknownStatusIs503(t *testing.T) {
	store := &stubPendingStore{
		getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
			return pendingTestEntry(sid, rid, "garbage", "wat"), true, nil
		},
	}
	h := NewHandler(nil)
	h.SetPendingStore(store)
	r := httptest.NewRequest("GET",
		"/v1/sessions/sess-6/pending-response?request_id=req-6", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown status: got %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PENDING_BAD_STATUS") {
		t.Errorf("body: got %q", w.Body.String())
	}
}

func TestGetPendingResponse_OnlyGETAllowed(t *testing.T) {
	// The sub-route /v1/sessions/{id}/pending-response is read-only.
	// POST/PUT/DELETE on it must 405. This is the regression guard
	// for the routing change in C3 — the new sub-route must not
	// accidentally accept write methods.
	h := NewHandler(nil)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		r := httptest.NewRequest(method, "/v1/sessions/sess-x/pending-response", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s pending-response: got %d, want 405", method, w.Code)
		}
	}
}

func TestGetPendingResponse_PlainSessionGetStillWorks(t *testing.T) {
	// Regression guard: the sub-route check must not steal plain
	// GET /v1/sessions/{id} requests. The manager is constructed
	// with a redis client that points to a closed port; the
	// GetSessionByID call will return an internal error, but
	// what matters here is that we did NOT route to
	// getPendingResponse (which would 503 PENDING_STORE_UNAVAILABLE).
	rc := NewRedisClient("127.0.0.1:1", "", 0) // closed port → fast error
	sm := NewManager(rc, 0)
	h := NewHandler(sm)
	r := httptest.NewRequest("GET", "/v1/sessions/sess-y", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusServiceUnavailable &&
		strings.Contains(w.Body.String(), "PENDING_STORE_UNAVAILABLE") {
		t.Fatalf("plain session GET was misrouted to pending-response handler: %s", w.Body.String())
	}
	_ = redis.Nil // keep the redis import in case future tests need it
}
