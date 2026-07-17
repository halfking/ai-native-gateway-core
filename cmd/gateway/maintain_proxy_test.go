package main

import (
	"net/http"
	"net/http/httptest"
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
		if r.URL.Query().Get("token") != "" { t.Error("token query parameter was forwarded") }
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog?token=secret", nil)
	request.Header.Set("X-Request-ID", "req-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent { t.Fatalf("status = %d", response.Code) }
	if response.Header().Get("Deprecation") != "true" { t.Fatal("missing Deprecation header") }
	if response.Header().Get("Link") == "" { t.Fatal("missing successor Link header") }
}

func TestMaintainCompatProxyFallsBackToLegacyWhenUnset(t *testing.T) {
	t.Setenv("MAINTAIN_SERVICE_URL", "")
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTeapot { t.Fatalf("status = %d", response.Code) }
}
