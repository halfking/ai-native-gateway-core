package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		BaseURL:          upstreamServer.URL,
		Protocol:         "openai-completions",
		RawModel:         "vendor-embed",
		APIKey:           "upstream-key",
		Routable:         true,
		LifecycleStatus:  "active",
		AvailabilityState: "ready",
		QuotaState:       "ok",
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
