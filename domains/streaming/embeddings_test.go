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

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/upstream"
	"github.com/stretchr/testify/require"
)

type embeddingResolverStub struct {
	candidates []provider.Candidate
	model      string
	profile    string
	tenantID   string
	modality   string
}

func (h *EmbeddingsHandler) setKeyVerifierForTest(verifier embeddingKeyVerifier) {
	h.keyVerifier = verifier
}

func TestEmbeddingsHandlerMarksThrottledAPIKeyAsGateway429(t *testing.T) {
	handler := NewEmbeddingsHandler(&embeddingResolverStub{}, upstream.NewWithRetries(0))
	handler.setKeyVerifierForTest(&stubKeyVerifier{info: &authentication.KeyInfo{ID: 7, Status: "throttled"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"m","input":"x"}`))
	req.Header.Set("Authorization", "Bearer user-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(ratelimit.GatewayRateLimitScopeHeader); got != ratelimit.GatewayRateLimitScopeSharedKey {
		t.Fatalf("rate limit scope = %q, want %q", got, ratelimit.GatewayRateLimitScopeSharedKey)
	}
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
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))

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

type embeddingUsageSink struct {
	entries []telemetry.RequestLogEntry
}

func (s *embeddingUsageSink) PersistRequestLog(_ context.Context, entry *telemetry.RequestLogEntry) error {
	s.entries = append(s.entries, *entry)
	return nil
}

func TestEmbeddingsHandlerProjectsOnlyMeasuredServingUsage(t *testing.T) {
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Model == "primary" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"unavailable"},"usage":{"prompt_tokens":19}}`))
			return
		}
		usage, data := `{"prompt_tokens":3,"total_tokens":3}`, `[{"embedding":[0.1]}]`
		switch request.Input {
		case "zero":
			usage = `{"input_tokens":0}`
		case "unknown":
			usage = `{"prompt_tokens":null,"total_tokens":3}`
		case "invalid":
			usage = `{"prompt_tokens":2147483648}`
		case "empty":
			data = `[]`
		case "error envelope":
			_, _ = w.Write([]byte(`{"data":[{}],"usage":{"prompt_tokens":3},"error":{"message":"failure"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":` + data + `,"usage":` + usage + `}`))
	}))
	defer supplier.Close()

	primary := embeddingCandidate(supplier.URL, "primary")
	primary.ProviderID, primary.CredentialID = 10, 11
	backup := embeddingCandidate(supplier.URL, "backup")
	backup.ProviderID, backup.CredentialID, backup.CanonicalID = 20, 21, intPtr(22)
	handler := NewEmbeddingsHandler(&embeddingResolverStub{candidates: []provider.Candidate{primary, backup}}, upstream.NewWithRetries(0))
	handler.setKeyVerifierForTest(&stubKeyVerifier{info: &authentication.KeyInfo{ID: 7, TenantID: "tenant-a", ApplicationID: 8}})
	client := telemetry.NewClient()
	t.Cleanup(client.Stop)
	sink := &embeddingUsageSink{}
	client.SetRequestLogSink(sink)
	handler.SetTelemetry(client)
	route := middleware.NewRequestIDMiddleware().Wrap(handler)

	var ids []string
	for _, input := range []string{"known", "zero", "unknown", "invalid", "empty", "error envelope"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embed","input":"`+input+`"}`))
		req.Header.Set("Authorization", "Bearer user-key")
		req.Header.Set("X-Request-Id", "reused-client-id")
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, input)
		ids = append(ids, rec.Header().Get("X-Request-Id"))
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embed","input":"known"}`))
	rec := httptest.NewRecorder()
	route.ServeHTTP(rec, request)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	client.Stop()

	require.Len(t, sink.entries, 2)
	require.NotEqual(t, ids[0], ids[1])
	wantTokens := []int{3, 0}
	for i, entry := range sink.entries {
		require.Equal(t, telemetry.RequestLogStandaloneUsage, entry.Op)
		require.Equal(t, ids[i], entry.RequestID)
		require.NotEqual(t, "reused-client-id", entry.RequestID)
		require.Equal(t, "tenant-a", entry.TenantID)
		require.Equal(t, intPtr(7), entry.APIKeyID)
		require.Equal(t, intPtr(8), entry.ApplicationID)
		require.Equal(t, intPtr(20), entry.ProviderID)
		require.Equal(t, intPtr(21), entry.CredentialID)
		require.Equal(t, intPtr(22), entry.CanonicalID)
		require.Equal(t, strPtr("embed"), entry.ClientModel)
		require.Equal(t, strPtr("backup"), entry.OutboundModel)
		require.Equal(t, intPtr(wantTokens[i]), entry.PromptTokens)
		require.Equal(t, intPtr(0), entry.CompletionTokens)
		require.Equal(t, strPtr("llm"), entry.UsageSource)
		require.True(t, entry.Success)
		require.Nil(t, entry.CostUSD)
		require.Nil(t, entry.CreditsCharged)
		require.Nil(t, entry.ResponseBody)
		require.Nil(t, entry.GwSessionID)
	}
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
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
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
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestEmbeddingsHandlerValidatesRequest(t *testing.T) {
	handler := NewEmbeddingsHandler(&embeddingResolverStub{}, upstream.NewWithRetries(0))
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

// ── 2026-09-14 audit #8: non-2xx pass-through tightening ─────────────────
// Previously every status outside the classified set (401/403/404/429/5xx)
// was relayed verbatim — 402/408/422 vendor bodies leaked straight to the
// client. Now only 2xx relays; everything else is classified and either
// fails over (retryable / credential-fatal / ambiguous) or renders a
// terminal client-determined family as a gateway-shaped error.

// healthyBackupServer returns a server + call counter answering a valid
// embedding response, so tests can assert whether the sibling candidate was
// attempted.
func healthyBackupServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[],"model":"backup","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestEmbeddingsHandlerFailoverOn402PaymentRequired: 402 (quota/payment
// family, non-terminal) must fail over to the sibling candidate instead of
// relaying the vendor's payment error body.
func TestEmbeddingsHandlerFailoverOn402PaymentRequired(t *testing.T) {
	paymentRequired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"billing hard limit reached"}}`, http.StatusPaymentRequired)
	}))
	defer paymentRequired.Close()
	backup, backupCalls := healthyBackupServer(t)

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(paymentRequired.URL, "primary"),
		embeddingCandidate(backup.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if *backupCalls == 0 {
		t.Fatal("sibling candidate was not attempted after 402")
	}
}

// TestEmbeddingsHandlerFailoverOn408Timeout: 408 classifies as KindTimeout
// (retryable family) — the sibling must be attempted.
func TestEmbeddingsHandlerFailoverOn408Timeout(t *testing.T) {
	slowUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"request timed out"}}`, http.StatusRequestTimeout)
	}))
	defer slowUpstream.Close()
	backup, backupCalls := healthyBackupServer(t)

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(slowUpstream.URL, "primary"),
		embeddingCandidate(backup.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if *backupCalls == 0 {
		t.Fatal("sibling candidate was not attempted after 408")
	}
}

// TestEmbeddingsHandlerTerminalContentFilterRendersGatewayShape: 422 with a
// content-moderation body is a client-determined terminal family — no
// failover, and the client sees the gateway's 400 invalid_request_error
// shape, never the vendor body verbatim.
func TestEmbeddingsHandlerTerminalContentFilterRendersGatewayShape(t *testing.T) {
	contentFiltered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"message":"Sensitive words detected (1026) in your input","code":"new_sensitive"}}`))
	}))
	defer contentFiltered.Close()
	backup, backupCalls := healthyBackupServer(t)

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(contentFiltered.URL, "primary"),
		embeddingCandidate(backup.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if *backupCalls != 0 {
		t.Fatal("terminal content_filter family must not fail over")
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Type != "invalid_request_error" || payload.Error.Code != "content_filter" {
		t.Fatalf("type/code = %q/%q, want invalid_request_error/content_filter", payload.Error.Type, payload.Error.Code)
	}
	// The vendor body must never be relayed verbatim: neither its raw JSON
	// shape nor the vendor-specific error code may leak into the gateway
	// response (the sanitized reason prose is allowed, the vendor envelope
	// is not).
	raw := recorder.Body.String()
	if strings.Contains(raw, `"code":"new_sensitive"`) || strings.Contains(raw, `{"error":{"message":"Sensitive`) {
		t.Fatalf("vendor body leaked verbatim: %s", raw)
	}
}

// TestEmbeddingsHandlerTerminalContextLengthRendersGatewayShape: 400 with a
// context-length body → terminal 400 invalid_request_error in the gateway
// shape (client-side input problem; failover cannot fix it).
func TestEmbeddingsHandlerTerminalContextLengthRendersGatewayShape(t *testing.T) {
	tooLong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens"}}`))
	}))
	defer tooLong.Close()
	backup, backupCalls := healthyBackupServer(t)

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(tooLong.URL, "primary"),
		embeddingCandidate(backup.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if *backupCalls != 0 {
		t.Fatal("terminal context_length family must not fail over")
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
	if payload.Error.Type != "invalid_request_error" || payload.Error.Code != "context_length_exceeded" {
		t.Fatalf("type/code = %q/%q, want invalid_request_error/context_length_exceeded", payload.Error.Type, payload.Error.Code)
	}
}

// TestEmbeddingsHandlerRetryAfterStaysWithinFamily (R27 #11-3 regression):
// a 429 candidate quoted Retry-After: 120, then a 503 candidate terminates
// the loop. The 503 (overloaded) exhaustion response must NOT carry the
// 120s window quoted by the earlier rate-limit family.
func TestEmbeddingsHandlerRetryAfterStaysWithinFamily(t *testing.T) {
	throttled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer throttled.Close()
	overloaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "overloaded", http.StatusServiceUnavailable)
	}))
	defer overloaded.Close()

	resolver := &embeddingResolverStub{candidates: []provider.Candidate{
		embeddingCandidate(throttled.URL, "primary"),
		embeddingCandidate(overloaded.URL, "backup"),
	}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/embeddings", stringsReader(`{"model":"embedding-fast","input":"hello"}`)))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want empty (120s belongs to the rate-limit family, not overloaded)", got)
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

func TestEmbeddingsHandlerCorrelatesRequestsWithoutChangingModelBinding(t *testing.T) {
	const correlation = "memora-vector-operation"
	const rawModel = "qwen3-embedding:0.6b"
	requestIDs := make(chan string, 2)
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestIDs <- r.Header.Get("X-Request-ID")
		if r.Header.Get("X-Correlation-ID") != "" || r.Header.Get("Authorization") != "Bearer supplier-fixture" {
			t.Error("unexpected supplier trace or credential")
		}
		var body struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != rawModel {
			t.Errorf("wrong model binding: %q %v", body.Model, err)
		}
		w.Write([]byte(`{"model":"qwen3-embedding:0.6b","data":[{"index":0,"embedding":[1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer supplier.Close()
	resolver := &embeddingResolverStub{candidates: []provider.Candidate{{
		BaseURL: supplier.URL, Protocol: "openai-completions", RawModel: rawModel, APIKey: "supplier-fixture",
		Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok",
	}}}
	handler := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	for _, id := range []string{"memora-inference-1", "memora-inference-2"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"qwen3-embedding:0.6b","input":"memory"}`))
		req.Header.Set("X-Request-ID", id)
		req.Header.Set("X-Correlation-ID", correlation)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Header().Get("X-Request-ID") != id || w.Header().Get("X-Correlation-ID") != correlation {
			t.Fatalf("Gateway response lost trace: status=%d headers=%v", w.Code, w.Header())
		}
		if forwarded := <-requestIDs; forwarded != id {
			t.Fatalf("supplier received request ID %q, want %q", forwarded, id)
		}
	}
	if resolver.model != rawModel || resolver.modality != "embedding" {
		t.Fatalf("resolver model=%q modality=%q", resolver.model, resolver.modality)
	}
}
