package autoroute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHTTPLlmCaller_EndpointEmpty ensures safe failure when not configured.
func TestHTTPLlmCaller_EndpointEmpty(t *testing.T) {
	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{})
	_, err := caller.Call(context.Background(), "any")
	if err == nil || !strings.Contains(err.Error(), "endpoint not configured") {
		t.Errorf("expected endpoint-not-configured error, got %v", err)
	}
}

// TestHTTPLlmCaller_SuccessfulResponse tests the happy path against
// an httptest mock that mimics an OpenAI-compatible response.
func TestHTTPLlmCaller_SuccessfulResponse(t *testing.T) {
	var capturedAuth atomic.Value
	var capturedBody atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth.Store(r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		capturedBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-abc",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "reasoning"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 50, "completion_tokens": 1, "total_tokens": 51}
		}`))
	}))
	defer srv.Close()

	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{
		Endpoint:   srv.URL,
		APIKey:     "sk-test-1234",
		Model:      "claude-haiku-4-5",
		Timeout:    2 * time.Second,
		HTTPClient: srv.Client(),
		ExtraHeaders: map[string]string{
			"X-Request-ID": "req-abc",
		},
	})

	resp, err := caller.Call(context.Background(), "classify this")
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if resp != "reasoning" {
		t.Errorf("resp = %q, want 'reasoning'", resp)
	}
	if got := capturedAuth.Load(); got != "Bearer sk-test-1234" {
		t.Errorf("auth header = %v, want 'Bearer sk-test-1234'", got)
	}
	if body, ok := capturedBody.Load().(string); ok {
		if !strings.Contains(body, "claude-haiku-4-5") {
			t.Errorf("body missing model: %s", body)
		}
		if !strings.Contains(body, "classify this") {
			t.Errorf("body missing prompt: %s", body)
		}
		if !strings.Contains(body, `"temperature":0`) {
			t.Errorf("body should request deterministic (temperature=0): %s", body)
		}
	}
}

func TestHTTPLlmCaller_5xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down"}}`))
	}))
	defer srv.Close()

	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{
		Endpoint:   srv.URL,
		APIKey:     "sk-x",
		HTTPClient: srv.Client(),
	})
	_, err := caller.Call(context.Background(), "p")
	if err == nil {
		t.Fatal("expected error on 5xx")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err should mention 503, got %v", err)
	}
}

func TestHTTPLlmCaller_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
	})
	_, err := caller.Call(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "empty choices") {
		t.Errorf("expected empty-choices error, got %v", err)
	}
}

func TestHTTPLlmCaller_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{
		Endpoint:   srv.URL,
		Timeout:    50 * time.Millisecond, // tight
		HTTPClient: srv.Client(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := caller.Call(ctx, "p")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err should wrap context.DeadlineExceeded, got %v", err)
	}
}

func TestHTTPLlmCaller_DefaultConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"chat"}}]}`))
	}))
	defer srv.Close()

	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{Endpoint: srv.URL, HTTPClient: srv.Client()})
	resp, err := caller.Call(context.Background(), "p")
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if resp != "chat" {
		t.Errorf("resp = %q, want 'chat'", resp)
	}
}

func TestHTTPLlmCaller_TrailingSlashHandled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %s, want suffix /chat/completions", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	// Trailing slash on endpoint should still produce /chat/completions
	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{
		Endpoint:   srv.URL + "/v1/",
		HTTPClient: srv.Client(),
	})
	resp, err := caller.Call(context.Background(), "p")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp != "ok" {
		t.Errorf("resp = %q, want 'ok'", resp)
	}
}

func TestHTTPLlmCaller_ExtraHeaders(t *testing.T) {
	var capturedHdr atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHdr.Store(r.Header.Get("X-Org"))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	caller := NewHTTPLlmCaller(HTTPLlmCallerConfig{
		Endpoint:     srv.URL,
		ExtraHeaders: map[string]string{"X-Org": "kaixuan"},
		HTTPClient:   srv.Client(),
	})
	_, _ = caller.Call(context.Background(), "p")
	if got := capturedHdr.Load(); got != "kaixuan" {
		t.Errorf("X-Org header = %v, want 'kaixuan'", got)
	}
}

// ── BuildHTTPLlmCallerFromEnv ───────────────────────────────────

func TestBuildHTTPLlmCallerFromEnv_Disabled(t *testing.T) {
	caller, enabled := BuildHTTPLlmCallerFromEnv(func(k string) string { return "" })
	if enabled {
		t.Error("expected disabled when no env var set")
	}
	if _, ok := caller.(DisabledCaller); !ok {
		t.Errorf("expected DisabledCaller, got %T", caller)
	}
}

func TestBuildHTTPLlmCallerFromEnv_Enabled(t *testing.T) {
	envs := map[string]string{
		"LLMGatewayAutoLLMEndpoint": "https://llm.example/v1",
		"LLMGatewayAutoLLMApiKey":   "sk-abc",
		"LLMGatewayAutoLLMModel":    "claude-3-5-sonnet",
		"LLMGatewayAutoLLMTimeout":  "5",
	}
	caller, enabled := BuildHTTPLlmCallerFromEnv(func(k string) string { return envs[k] })
	if !enabled {
		t.Error("expected enabled")
	}
	h, ok := caller.(*HTTPLlmCaller)
	if !ok {
		t.Fatalf("expected *HTTPLlmCaller, got %T", caller)
	}
	if h.cfg.Endpoint != "https://llm.example/v1" {
		t.Errorf("endpoint = %q", h.cfg.Endpoint)
	}
	if h.cfg.APIKey != "sk-abc" {
		t.Errorf("apikey = %q", h.cfg.APIKey)
	}
	if h.cfg.Model != "claude-3-5-sonnet" {
		t.Errorf("model = %q", h.cfg.Model)
	}
	if h.cfg.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", h.cfg.Timeout)
	}
}

func TestBuildHTTPLlmCallerFromEnv_DefaultModel(t *testing.T) {
	envs := map[string]string{
		"LLMGatewayAutoLLMEndpoint": "https://llm.example/v1",
	}
	caller, _ := BuildHTTPLlmCallerFromEnv(func(k string) string { return envs[k] })
	h := caller.(*HTTPLlmCaller)
	if h.cfg.Model != "gpt-4o-mini" {
		t.Errorf("default model = %q, want 'gpt-4o-mini'", h.cfg.Model)
	}
}

func TestBuildHTTPLlmCallerFromEnv_DefaultTimeout(t *testing.T) {
	envs := map[string]string{
		"LLMGatewayAutoLLMEndpoint": "https://llm.example/v1",
	}
	caller, _ := BuildHTTPLlmCallerFromEnv(func(k string) string { return envs[k] })
	h := caller.(*HTTPLlmCaller)
	if h.cfg.Timeout != 3*time.Second {
		t.Errorf("default timeout = %v, want 3s", h.cfg.Timeout)
	}
}

func TestBuildHTTPLlmCallerFromEnv_NilLookupSafe(t *testing.T) {
	caller, enabled := BuildHTTPLlmCallerFromEnv(nil)
	if enabled {
		t.Error("expected disabled when lookup is nil")
	}
	if _, ok := caller.(DisabledCaller); !ok {
		t.Errorf("expected DisabledCaller, got %T", caller)
	}
}

// ── parseFloatSeconds ───────────────────────────────────────────

func TestParseFloatSeconds(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		err  bool
	}{
		{"3", 3.0, false},
		{"3.5", 3.5, false},
		{"0.5", 0.5, false},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseFloatSeconds(tt.in)
		if (err != nil) != tt.err {
			t.Errorf("parseFloatSeconds(%q) err = %v", tt.in, err)
		}
		if !tt.err && got != tt.want {
			t.Errorf("parseFloatSeconds(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
