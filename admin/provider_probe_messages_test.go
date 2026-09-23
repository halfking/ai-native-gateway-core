package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// R60 S3-F5 (2026-09-23 audit): anthropic-messages credentials were health
// probed with the Bearer-only chat probe, which always 401'd and flipped
// healthy credentials to health_status='warning' (admin-facing misreport;
// availability_state / auto-cool intentionally untouched). These tests pin
// the three-way dispatch and the anthropic wire shape.

// TestDoMessagesProbe_SendsMessagesWireShape pins the anthropic probe request
// shape: POST <base>/v1/messages, x-api-key + anthropic-version headers (same
// header names/version pin as the real anthropic upstream construction in
// internal/providercap.ApplyAuthHeaders), messages body with max_tokens=1,
// and — critically — no Authorization: Bearer header.
func TestDoMessagesProbe_SendsMessagesWireShape(t *testing.T) {
	var gotPath string
	var gotAPIKey, gotAuthz, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotAuthz = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("anthropic-version")
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-fable-5","content":[{"type":"text","text":"ping"}]}`))
	}))
	t.Cleanup(srv.Close)

	result, err := doMessagesProbe(context.Background(), srv.URL+"/v1/messages", "sk-ant-test", "claude-fable-5")
	if err != nil {
		t.Fatalf("doMessagesProbe() error = %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("probe path = %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "sk-ant-test" {
		t.Fatalf("x-api-key = %q, want credential key", gotAPIKey)
	}
	if gotAuthz != "" {
		t.Fatalf("Authorization header = %q, anthropic probe must not send Bearer", gotAuthz)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01 (providercap.ApplyAuthHeaders pin)", gotVersion)
	}
	if _, ok := gotBody["messages"]; !ok {
		t.Fatalf("request body missing \"messages\" field: %v", gotBody)
	}
	if maxTokens, _ := gotBody["max_tokens"].(float64); maxTokens != 1 {
		t.Fatalf("max_tokens = %v, want 1", gotBody["max_tokens"])
	}
	if result.statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200", result.statusCode)
	}
	if result.modelInResponse != "claude-fable-5" {
		t.Fatalf("modelInResponse = %q, want claude-fable-5", result.modelInResponse)
	}
}

// TestDoMessagesProbe_AuthFailedPropagates: 401/403 must surface as a
// non-2xx statusCode carrying the upstream reason — the caller maps that to
// health_status='warning' (failure semantics unchanged: no auto-cool).
func TestDoMessagesProbe_AuthFailedPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	t.Cleanup(srv.Close)

	result, err := doMessagesProbe(context.Background(), srv.URL+"/v1/messages", "bad-key", "claude-fable-5")
	if err != nil {
		t.Fatalf("doMessagesProbe() error = %v", err)
	}
	if result.statusCode != http.StatusUnauthorized {
		t.Fatalf("statusCode = %d, want 401 (auth_failed evidence)", result.statusCode)
	}
	if result.errorMessage == "" {
		t.Fatalf("errorMessage empty, want upstream authentication_error body")
	}
}

// TestCredentialProbeDispatch_ByProtocol pins the three-way branch:
// anthropic-messages → anthropic probe (/v1/messages), openai → chat probe
// (/v1/chat/completions), responses → responses probe (/v1/responses). Dirty
// alias spellings (the vapeur "openai-response", the legacy "anthropic" /
// "openai") must normalize into the same branches; unknown protocols keep the
// chat fallback.
func TestCredentialProbeDispatch_ByProtocol(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"probe-model"}`))
	}))
	t.Cleanup(srv.Close)

	cases := []struct {
		protocol string
		wantPath string
	}{
		{"anthropic-messages", "/v1/messages"},
		{"anthropic", "/v1/messages"},      // legacy alias → anthropic probe
		{"claude", "/v1/messages"},         // legacy alias → anthropic probe
		{"openai", "/v1/chat/completions"}, // legacy alias → chat probe
		{"openai-completions", "/v1/chat/completions"},
		{"weird-proto", "/v1/chat/completions"}, // unknown → chat fallback (unchanged)
		{"openai-responses", "/v1/responses"},
		{"openai-response", "/v1/responses"}, // vapeur misspelling → responses probe
	}
	for _, tc := range cases {
		t.Run(tc.protocol+"/"+tc.wantPath, func(t *testing.T) {
			before := len(paths)
			probeURL, probeFn := credentialProbeDispatch(tc.protocol, srv.URL)
			if probeURL == "" {
				t.Fatalf("credentialProbeDispatch(%q) returned empty probeURL", tc.protocol)
			}
			result, err := probeFn(context.Background(), probeURL, "test-key", "probe-model")
			if err != nil {
				t.Fatalf("probeFn() error = %v", err)
			}
			if result.statusCode != http.StatusOK {
				t.Fatalf("statusCode = %d, want 200", result.statusCode)
			}
			if len(paths) != before+1 {
				t.Fatalf("expected exactly one probe request, got %d", len(paths)-before)
			}
			if got := paths[before]; got != tc.wantPath {
				t.Fatalf("protocol %q probed %q, want %q", tc.protocol, got, tc.wantPath)
			}
		})
	}
}

// TestCredentialProbeDispatch_NormalizesBeforeBranch is the read-face guard
// for the vapeur incident itself: a DB row still carrying the dirty singular
// spelling must dispatch to the Responses probe, not the chat probe.
func TestCredentialProbeDispatch_NormalizesBeforeBranch(t *testing.T) {
	var sawBearer atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("dirty openai-response row probed %q, want /v1/responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer test-key" {
			sawBearer.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-5.2","status":"completed"}`))
	}))
	t.Cleanup(srv.Close)

	probeURL, probeFn := credentialProbeDispatch("openai-response", srv.URL)
	result, err := probeFn(context.Background(), probeURL, "test-key", "gpt-5.2")
	if err != nil {
		t.Fatalf("probeFn() error = %v", err)
	}
	if result.statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200", result.statusCode)
	}
	if !sawBearer.Load() {
		t.Fatalf("responses probe must authenticate with Authorization: Bearer")
	}
	if result.modelInResponse != "gpt-5.2" {
		t.Fatalf("modelInResponse = %q, want gpt-5.2", result.modelInResponse)
	}
}
