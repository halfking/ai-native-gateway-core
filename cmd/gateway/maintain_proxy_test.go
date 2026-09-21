package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMaintainServiceURLRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"", "maintain.example", "ftp://maintain.example", "https://user:pass@maintain.example", "https://maintain.example/?token=x"} {
		if _, err := parseMaintainServiceURL(raw); err == nil {
			t.Fatalf("parseMaintainServiceURL(%q) accepted unsafe value", raw)
		}
	}
}

func TestMaintainCompatProxyStripsTokenAndAddsDeprecation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "" {
			t.Error("token query parameter was forwarded")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog?token=secret", nil)
	request.Header.Set("X-Request-ID", "req-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("Deprecation") != "true" {
		t.Fatal("missing Deprecation header")
	}
	if response.Header().Get("Link") == "" {
		t.Fatal("missing successor Link header")
	}
}

func TestMaintainCompatProxyFallsBackToLegacyWhenUnset(t *testing.T) {
	t.Setenv("MAINTAIN_SERVICE_URL", "")
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTeapot {
		t.Fatalf("status = %d", response.Code)
	}
}

// Canonical /maintain-api/* must NOT carry a Deprecation header — it is the
// successor surface, not the shim.
func TestMaintainCanonicalProxyHasNoDeprecation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	handler := newMaintainCompatHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodGet, "/maintain-api/downloads/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("Deprecation") != "" {
		t.Fatalf("canonical path must not be deprecated, got %q", response.Header().Get("Deprecation"))
	}
}

// Client-supplied X-Tenant-ID must be stripped so tenant scope can only come
// from the authenticated session on the backend.
func TestMaintainProxyStripsClientTenantID(t *testing.T) {
	var seenHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Tenant-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	handler := newMaintainCompatHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodGet, "/maintain-api/downloads/catalog", nil)
	request.Header.Set("X-Tenant-ID", "forged")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if seenHeader != "" {
		t.Fatalf("X-Tenant-ID was forwarded as %q", seenHeader)
	}
}

// Gateway's own routes (/, /v1/*, /api/auth/*) must not be swallowed by the
// maintain proxy.
func TestMaintainProxyPreservesGatewayRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("maintain upstream should not receive Gateway routes")
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, path := range []string{"/", "/v1/chat/completions", "/api/auth/me", "/providers"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusTeapot {
			t.Fatalf("path %s should hit legacy handler, got %d", path, response.Code)
		}
	}
}

func TestMaintainProxyNormalizesUpstream5xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream html error"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, tc := range []struct {
		path       string
		deprecated bool
	}{
		{path: "/maintain-api/healthz"},
		{path: "/api/admin/licenses", deprecated: true},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		request.Header.Set("X-Request-ID", "audit-5xx")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("path %s status = %d, want 503", tc.path, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("path %s content-type = %q, want application/json", tc.path, got)
		}
		if got := response.Header().Get("Retry-After"); got != "10" {
			t.Fatalf("path %s Retry-After = %q, want 10", tc.path, got)
		}
		if tc.deprecated && response.Header().Get("Deprecation") != "true" {
			t.Fatalf("path %s lost Deprecation header", tc.path)
		}
		body := response.Body.String()
		if !strings.Contains(body, "maintain.upstream_unavailable") {
			t.Fatalf("path %s body does not contain normalized error: %s", tc.path, body)
		}
		if strings.Contains(body, "upstream html error") || strings.Contains(body, "upstream unavailable") {
			t.Fatalf("path %s leaked upstream error body: %s", tc.path, body)
		}
	}
}

func TestMaintainRoutes503WhenNotConfigured(t *testing.T) {
	t.Setenv("MAINTAIN_SERVICE_URL", "")
	handler := newMaintainGatewayHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }), nil)
	for _, path := range []string{"/maintain", "/maintain/home", "/maintain-assets/app.js", "/maintain-api/healthz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("path %s should answer 503, got %d", path, response.Code)
		}
	}
}
