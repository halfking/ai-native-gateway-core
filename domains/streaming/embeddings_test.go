package streaming

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

type embeddingResolverStub struct {
	candidates []provider.Candidate
	model      string
	profile    string
	tenantID   string
	modality   string
}

func (s *embeddingResolverStub) GetCandidatesByModality(_ context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error) {
	s.model = model
	s.profile = profile
	s.tenantID = tenantID
	s.modality = modality
	return s.candidates, provider.DefaultPolicy(), nil
}

func TestEmbeddingsHandlerProxiesBatchRequest(t *testing.T) {
	var received map[string]json.RawMessage
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"vendor-embed","usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer upstreamServer.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{{
		BaseURL:           upstreamServer.URL,
		Protocol:          "openai-completions",
		RawModel:          "vendor-embed",
		APIKey:            "upstream-key",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
	}}}
	handler := NewEmbeddingsHandler(resolver, upstream.New())

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":["你好","世界"],"dimensions":1024,"encoding_format":"float","user":"tenant-user"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if resolver.model != "embedding-fast" || resolver.modality != "embedding" {
		t.Fatalf("resolver model=%q modality=%q", resolver.model, resolver.modality)
	}
	assertRawString(t, received["model"], "vendor-embed")
	assertRawNumber(t, received["dimensions"], 1024)
	assertRawString(t, received["encoding_format"], "float")
	assertRawString(t, received["user"], "tenant-user")
}

func TestEmbeddingsHandlerFailsOverOnServerError(t *testing.T) {
	var mu sync.Mutex
	failedCalls := 0
	failedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		failedCalls++
		mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer failedServer.Close()

	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[],"model":"backup","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer healthyServer.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(failedServer.URL, "primary"),
		embeddingCandidate(healthyServer.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.New())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	mu.Lock()
	calls := failedCalls
	mu.Unlock()
	if calls == 0 {
		t.Fatal("primary candidate was not attempted")
	}
}

func TestEmbeddingsHandlerFailsOverOnCredentialError(t *testing.T) {
	unauthorizedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "credential rejected", http.StatusUnauthorized)
	}))
	defer unauthorizedServer.Close()

	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[],"model":"backup","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer healthyServer.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(unauthorizedServer.URL, "primary"),
		embeddingCandidate(healthyServer.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.New())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestEmbeddingsHandlerValidatesRequest(t *testing.T) {
	handler := NewEmbeddingsHandler(&embeddingResolverStub{}, upstream.New())
	tests := []struct {
		name   string
		method string
		body   string
		status int
	}{
		{name: "method", method: http.MethodGet, status: http.StatusMethodNotAllowed},
		{name: "invalid json", method: http.MethodPost, body: `{`, status: http.StatusBadRequest},
		{name: "missing model", method: http.MethodPost, body: `{"input":"hello"}`, status: http.StatusBadRequest},
		{name: "missing input", method: http.MethodPost, body: `{"model":"embed"}`, status: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, "/v1/embeddings", stringsReader(tc.body)))
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}
}

func embeddingCandidate(baseURL, model string) provider.Candidate {
	return provider.Candidate{
		BaseURL:           baseURL,
		Protocol:          "openai-completions",
		RawModel:          model,
		APIKey:            "upstream-key",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
	}
}

func stringsReader(value string) *strings.Reader {
	return strings.NewReader(value)
}

func assertRawString(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func assertRawNumber(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()
	var got int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("value = %d, want %d", got, want)
	}
}

// ── Exhaustion contract tests (2026-09-13 audit fix) ─────────────────────
// Prior behavior: every exhausted failover returned 502/server_error with
// the last vendor status erased. The contract now harvests the typed Kind
// per candidate and maps exhaustion to 429 (rate_limit_error, Retry-After
// relayed) when the fleet was throttled, 502 otherwise. Bodies are still
// never relayed — only the Kind decides the client-facing status.

// TestEmbeddingsHandlerExhaustionRateLimit429: all candidates answer 429 →
// client sees 429 rate_limit_error and the vendor's Retry-After is relayed.
func TestEmbeddingsHandlerExhaustionRateLimit429(t *testing.T) {
	throttled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer throttled.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(throttled.URL, "primary"),
		embeddingCandidate(throttled.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want 7", got)
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Type != "rate_limit_error" || payload.Error.Code != "rate_limit_exhausted" {
		t.Fatalf("error type/code = %q/%q, want rate_limit_error/rate_limit_exhausted", payload.Error.Type, payload.Error.Code)
	}
}

// TestEmbeddingsHandlerExhaustionOverloaded503: all candidates answer 503 —
// this project classifies 503 as concurrent-load (KindConcurrent), so the
// exhaustion response is 503 overloaded_error, distinct from a dead fleet.
func TestEmbeddingsHandlerExhaustionOverloaded503(t *testing.T) {
	overloaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "12")
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer overloaded.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(overloaded.URL, "primary"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "12" {
		t.Fatalf("Retry-After = %q, want 12", got)
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Type != "overloaded_error" {
		t.Fatalf("error type = %q, want overloaded_error", payload.Error.Type)
	}
}

// TestEmbeddingsHandlerExhaustionDeadUpstream502: a truly dead fleet
// (connection refused — no server at the address) must keep the legacy 502
// mapping. KindNetwork/KindUpstreamDown are bad-gateway semantics.
func TestEmbeddingsHandlerExhaustionDeadUpstream502(t *testing.T) {
	// Reserve a port then close the listener: connections are refused.
	deadAddr, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadURL := "http://" + deadAddr.Addr().String()
	_ = deadAddr.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(deadURL, "primary"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want empty on 502", got)
	}
}

// TestEmbeddingsHandlerExhaustionMixedTakesWorstRetryAfter: one candidate
// throttled with Retry-After: 30, another with 3 → client gets 429 and the
// longest window (30s), because the client must back off at least as long
// as the worst vendor constraint.
func TestEmbeddingsHandlerExhaustionMixedTakesWorstRetryAfter(t *testing.T) {
	shortWindow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer shortWindow.Close()
	longWindow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer longWindow.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(shortWindow.URL, "primary"),
		embeddingCandidate(longWindow.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want 30 (max across candidates)", got)
	}
}
