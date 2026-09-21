package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddleware_BypassesAPIAdminPaths(t *testing.T) {
	// PR-3 (2026-06-30): /api/* must bypass global API-key auth so
	// cookie-authenticated browser sessions reach admin.AdminMiddleware
	// (which independently handles Bearer JWT / cookie / API key).
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{
		"/api/auth/token",
		"/api/auth/logout",
		"/api/auth/me",
		"/api/auth/change-password",
		"/api/users",
		"/api/users/42",
		"/api/agents/health",
		"/api/admin/probe/cache-state",
		"/api/routing/overview",
		"/api/telemetry/quality/heatmap",
	}

	for _, path := range cases {
		called = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		// NO Authorization header (cookie-only auth)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !called {
			t.Errorf("%s: handler should be called (bypass expected), got status=%d", path, rr.Code)
			continue
		}
		if rr.Code == http.StatusUnauthorized {
			t.Errorf("%s: should NOT return 401, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddleware_StillRequiresBearerForDataPaths(t *testing.T) {
	// /v1/* data paths (chat/messages/responses) must STILL require
	// the global API key.
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/responses",
		"/v1/models",
	}

	for _, path := range cases {
		called = false
		req := httptest.NewRequest(http.MethodPost, path, nil)
		// NO Authorization header
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if called {
			t.Errorf("%s: handler should NOT be called without API key, got status=%d", path, rr.Code)
		}
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddleware_AcceptsValidBearerForDataPaths(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("handler should be called with valid Bearer, got status=%d", rr.Code)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_RejectsInvalidBearer(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if called {
		t.Fatalf("handler should NOT be called with wrong API key")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddleware_BypassesHealthAndMetrics(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/healthz", "/healthz/full", "/readyz", "/version", "/metrics", "/"} {
		called = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !called {
			t.Errorf("%s: handler should be called (bypass expected), got status=%d", path, rr.Code)
		}
	}
}

func TestAuthMiddlewareRejectionPreservesRequestID(t *testing.T) {
	h := NewRequestIDMiddleware().Wrap(NewAuthMiddleware("secret-key").Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if rr.Code != http.StatusUnauthorized || rr.Header().Get("X-Request-Id") == "" {
		t.Fatalf("status=%d request_id=%q", rr.Code, rr.Header().Get("X-Request-Id"))
	}
}

func TestAuthMiddleware_AcceptsXApiKeyStaticKey(t *testing.T) {
	// Anthropic Messages clients (Claude Code with ANTHROPIC_API_KEY) send
	// x-api-key instead of Authorization: Bearer. The static deployed key
	// delivered via x-api-key must pass the gate exactly like a Bearer.
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("handler should be called with valid x-api-key, got status=%d", rr.Code)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_PassesSkXApiKeyToVerifier(t *testing.T) {
	// sk-* data-plane keys via x-api-key are not validated here; they pass
	// through to the per-request DB verifier (same contract as the Bearer
	// sk-* branch).
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "sk-user-data-plane-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("handler should be called for sk-* x-api-key passthrough, got status=%d", rr.Code)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_RejectsNonSkXApiKey(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if called {
		t.Fatalf("handler should NOT be called with wrong x-api-key")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAuthMiddleware_BearerTakesPrecedenceOverXApiKey(t *testing.T) {
	// When both headers are present, Authorization wins — matching the
	// precedence of extractBearerToken on the handler side. A malformed
	// Bearer must NOT be rescued by a valid x-api-key static key.
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	req.Header.Set("x-api-key", "secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if called {
		t.Fatalf("handler should NOT be called when Bearer is invalid even if x-api-key is valid")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}
