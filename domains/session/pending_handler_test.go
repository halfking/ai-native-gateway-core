package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubPendingStore struct {
	getFn    func(ctx context.Context, sessionID, requestID string) (*PendingEntry, bool, error)
	latestFn func(ctx context.Context, sessionID string) (*PendingEntry, string, bool, error)
}

func (s *stubPendingStore) Get(ctx context.Context, sessionID, requestID string) (*PendingEntry, bool, error) {
	return s.getFn(ctx, sessionID, requestID)
}

func (s *stubPendingStore) GetLatest(ctx context.Context, sessionID string) (*PendingEntry, string, bool, error) {
	return s.latestFn(ctx, sessionID)
}

func pendingTestEntry(sid, rid, body, status string) *PendingEntry {
	return &PendingEntry{
		SessionID:   sid,
		TenantID:    "default",
		RequestID:   rid,
		Status:      status,
		Body:        body,
		ContentType: "text/event-stream",
		IsStream:    true,
	}
}

func pendingHandlerWithOwnedSession(t *testing.T, store PendingStore) (*Handler, *Session) {
	t.Helper()
	mgr, _ := newTestManager(t)
	sess, err := mgr.Create(context.Background(), 1, "default", "device")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	h := NewHandler(mgr)
	h.SetPendingStore(store)
	return h, sess
}

func pendingHandlerRequest(sessionID, requestID string) *http.Request {
	path := "/v1/sessions/" + sessionID + "/pending-response"
	if requestID != "" {
		path += "?request_id=" + requestID
	}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	return r.WithContext(SetTenantID(SetAPIKeyID(r.Context(), 1), "default"))
}

func TestGetPendingResponse_NilStoreReturns503(t *testing.T) {
	h := NewHandler(nil)
	r := httptest.NewRequest(http.MethodGet, "/v1/sessions/sess-x/pending-response", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

func TestGetPendingResponse_CompletedSSE(t *testing.T) {
	const body = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	store := &stubPendingStore{getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
		return pendingTestEntry(sid, rid, body, "completed"), true, nil
	}}
	h, sess := pendingHandlerWithOwnedSession(t, store)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, pendingHandlerRequest(sess.SessionID, "req-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Gw-Pending-Replay"); got != "true" {
		t.Fatalf("replay header = %q", got)
	}
	if got := w.Body.String(); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestGetPendingResponse_InProgressAndFailed(t *testing.T) {
	for _, status := range []string{"in_progress", "failed"} {
		t.Run(status, func(t *testing.T) {
			store := &stubPendingStore{getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
				entry := pendingTestEntry(sid, rid, "", status)
				entry.ErrorMessage = "upstream failed"
				return entry, true, nil
			}}
			h, sess := pendingHandlerWithOwnedSession(t, store)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, pendingHandlerRequest(sess.SessionID, "req-1"))
			want := http.StatusAccepted
			if status == "failed" {
				want = http.StatusOK
			}
			if w.Code != want {
				t.Fatalf("got %d, want %d: %s", w.Code, want, w.Body.String())
			}
		})
	}
}

func TestGetPendingResponse_GetLatestFallback(t *testing.T) {
	store := &stubPendingStore{
		getFn: func(_ context.Context, _, _ string) (*PendingEntry, bool, error) {
			t.Fatal("Get must not be called without request_id")
			return nil, false, nil
		},
		latestFn: func(_ context.Context, sid string) (*PendingEntry, string, bool, error) {
			return pendingTestEntry(sid, "req-latest", "data: [DONE]\n\n", "completed"), "req-latest", true, nil
		},
	}
	h, sess := pendingHandlerWithOwnedSession(t, store)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, pendingHandlerRequest(sess.SessionID, ""))
	if w.Code != http.StatusOK || w.Header().Get("X-Gw-Pending-Request") != "req-latest" {
		t.Fatalf("status=%d request=%q", w.Code, w.Header().Get("X-Gw-Pending-Request"))
	}
}

func TestGetPendingResponse_FailsClosed(t *testing.T) {
	t.Run("missing session", func(t *testing.T) {
		h := NewHandler(nil)
		h.SetPendingStore(&stubPendingStore{getFn: func(context.Context, string, string) (*PendingEntry, bool, error) {
			return pendingTestEntry("s", "r", "secret", "completed"), true, nil
		}})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, pendingHandlerRequest("s", "r"))
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("tenantless entry", func(t *testing.T) {
		store := &stubPendingStore{getFn: func(_ context.Context, sid, rid string) (*PendingEntry, bool, error) {
			return &PendingEntry{SessionID: sid, RequestID: rid, Status: "completed", Body: "secret"}, true, nil
		}}
		h, sess := pendingHandlerWithOwnedSession(t, store)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, pendingHandlerRequest(sess.SessionID, "r"))
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})
}

func TestGetPendingResponse_StoreErrorReturns503(t *testing.T) {
	store := &stubPendingStore{getFn: func(context.Context, string, string) (*PendingEntry, bool, error) {
		return nil, false, errors.New("redis unavailable")
	}}
	h, sess := pendingHandlerWithOwnedSession(t, store)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, pendingHandlerRequest(sess.SessionID, "req-1"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

func TestGetPendingResponse_OnlyGETAllowed(t *testing.T) {
	h := NewHandler(nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/v1/sessions/sess-x/pending-response", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s got %d, want 405", method, w.Code)
		}
	}
}

func TestGetPendingResponse_ErrorShape(t *testing.T) {
	w := httptest.NewRecorder()
	writeErrorJSON(w, http.StatusNotFound, "", "not found", "session_error", "PENDING_NOT_FOUND")
	var response map[string]any
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["error"].(map[string]any)["code"] != "PENDING_NOT_FOUND" {
		t.Fatalf("unexpected response: %#v", response)
	}
}
