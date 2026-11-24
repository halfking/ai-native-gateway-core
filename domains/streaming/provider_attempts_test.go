package streaming

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

type memoryAttemptStore struct {
	mu       sync.Mutex
	attempts map[string]reconciliation.Attempt
	statuses map[string]int
	outcomes map[string]string
	usage    map[string]reconciliation.UsageEvidence
}

func newMemoryAttemptStore() *memoryAttemptStore {
	return &memoryAttemptStore{attempts: map[string]reconciliation.Attempt{}, statuses: map[string]int{}, outcomes: map[string]string{}, usage: map[string]reconciliation.UsageEvidence{}}
}
func (s *memoryAttemptStore) Prepare(_ context.Context, a reconciliation.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[a.ID] = a
	return nil
}
func (s *memoryAttemptStore) MarkDispatched(context.Context, string) error      { return nil }
func (s *memoryAttemptStore) MarkNotSent(context.Context, string) error         { return nil }
func (s *memoryAttemptStore) MarkSent(context.Context, string, time.Time) error { return nil }
func (s *memoryAttemptStore) RecordHTTP(_ context.Context, id string, status int, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[id] = status
	return nil
}
func (s *memoryAttemptStore) RecordUsage(_ context.Context, id string, usage reconciliation.UsageEvidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage[id] = usage
	return nil
}
func (s *memoryAttemptStore) Finish(_ context.Context, id, outcome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[id] = outcome
	return nil
}

func TestEmbeddingsHandlerRecordsProviderAttemptFromFakeUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path=%q", r.URL.Path)
		}
		w.Header().Set("x-request-id", "upstream-request-7")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}, "usage": map[string]any{"prompt_tokens": 3, "total_tokens": 3}})
	}))
	defer server.Close()
	resolver := &embeddingResolverStub{candidates: []provider.Candidate{{
		CredentialID: 44, ProviderID: 12, BaseURL: server.URL, Protocol: "openai-completions", RawModel: "embed-x", APIKey: "upstream-key",
		Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", Currency: "USD",
	}}}
	store := newMemoryAttemptStore()
	h := NewEmbeddingsHandler(resolver, upstream.NewWithRetries(0))
	h.attemptRecorder = store
	h.keyVerifier = &stubKeyVerifier{info: &authentication.KeyInfo{ID: 1, TenantID: "tenant-5", Status: "active"}}
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embed-x","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer user-key")
	req.Header.Set("X-Request-Id", "request-9")
	response := httptest.NewRecorder()
	wal := &memoryModalityWAL{}
	middleware.NewRequestIDMiddleware().Wrap(ObserveModalityIngress(h, wal, nil)).ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	requestID := response.Header().Get("X-Request-Id")
	if requestID == "" || requestID == "request-9" || len(wal.updates) != 1 || wal.updates[0].RequestID != requestID || wal.updates[0].Status != telemetry.StatusSuccess {
		t.Fatalf("unexpected ingress result id=%q, updates=%+v", requestID, wal.updates)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.attempts) != 1 {
		t.Fatalf("recorded attempts=%d, want 1", len(store.attempts))
	}
	for id, a := range store.attempts {
		if a.ID != id || a.TenantID != "tenant-5" || a.RequestID != requestID || a.TrafficClass != "business" || a.Source != "embeddings" || a.CredentialID != 44 || a.ProviderID != 12 {
			t.Fatalf("unexpected attempt identity: %+v", a)
		}
		if store.statuses[id] != http.StatusOK || store.outcomes[id] != "success" {
			t.Fatalf("status/outcome=%d/%q", store.statuses[id], store.outcomes[id])
		}
	}
}

func TestModalityAttemptIdentityRequiresVerifiedKey(t *testing.T) {
	owner := "credential-selfcheck-worker"
	unknownOwner := "unregistered-service"
	for _, tt := range []struct {
		name, wantClass, wantClient string
		key                         *authentication.KeyInfo
	}{
		{"header only", "unknown", "", nil},
		{"ordinary key with system owner text", "business", "key:7", &authentication.KeyInfo{ID: 7, TenantID: "tenant-a", OwnerUser: &owner}},
		{"unregistered system key", "unknown", "", &authentication.KeyInfo{ID: 7, TenantID: "tenant-a", IsSystem: true, OwnerUser: &unknownOwner}},
		{"verified system key", "platform", "platform-automation", &authentication.KeyInfo{ID: 7, TenantID: "tenant-a", IsSystem: true, OwnerUser: &owner}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://provider.invalid/v1/embeddings", nil)
			request.Header.Set("X-LLM-Origin-Stage", "self_check")
			request = request.WithContext(withModalityAttemptKey(request.Context(), tt.key))
			store := newMemoryAttemptStore()
			observed, observer := observeModalityRequest(request, store, provider.Candidate{}, "tenant-a", "gateway-request", "embeddings")
			call := observer.BeforeAttempt(observed).(*modalityAttempt)
			if a := call.evidence; a.TrafficClass != tt.wantClass || a.ClientID != tt.wantClient || a.RequestID != "gateway-request" {
				t.Fatalf("unexpected attribution: %+v", a)
			}
		})
	}
}

func TestModalityAttemptKeepsUsageFromCapturedHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "modality-error")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable"},"usage":{"prompt_tokens":8,"completion_tokens":0}}`))
	}))
	defer server.Close()
	store := newMemoryAttemptStore()
	client := upstream.NewWithRetries(0)
	defer client.Stop()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/embeddings", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req, _ = observeModalityRequest(req, store, provider.Candidate{Protocol: "openai-completions"}, "tenant", "request", "embeddings")
	resp, upstreamErr := client.Do(req)
	if upstreamErr == nil || resp == nil {
		t.Fatalf("expected provider error: response=%v error=%v", resp, upstreamErr)
	}
	defer resp.Body.Close()
	if len(store.attempts) != 1 {
		t.Fatalf("attempts=%d", len(store.attempts))
	}
	for id := range store.attempts {
		usage := store.usage[id]
		if store.outcomes[id] != "failure" || usage.InputTokens == nil || *usage.InputTokens != 8 ||
			usage.OutputTokens == nil || *usage.OutputTokens != 0 || usage.ReportedAmount != nil {
			t.Fatalf("failed call evidence: outcome=%q usage=%+v", store.outcomes[id], usage)
		}
	}
}

func TestModalityCrossOriginRedirectHasIndependentEvidence(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":8}}`)
	}))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/embeddings", http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	store := newMemoryAttemptStore()
	client := upstream.NewWithRetries(0)
	defer client.Stop()
	ctx := withModalityAttemptKey(t.Context(), &authentication.KeyInfo{ID: 7, TenantID: "tenant-a"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, first.URL+"/embeddings", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	cand := provider.Candidate{ProviderID: 12, CredentialID: 44, RawModel: "embed-model", Protocol: "openai-completions"}
	req, observer := observeModalityRequest(req, store, cand, "tenant-a", "gateway-request", "embeddings")
	resp, upstreamErr := client.Do(req)
	if upstreamErr != nil {
		t.Fatal(upstreamErr)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	observer.RecordResponse(body)
	observer.Finish("success")

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.attempts) != 2 {
		t.Fatalf("attempt count=%d, want 2", len(store.attempts))
	}
	for id, attempt := range store.attempts {
		if attempt.TenantID != "tenant-a" || attempt.RequestID != "gateway-request" || attempt.ClientID != "key:7" {
			t.Fatalf("gateway identity changed: %+v", attempt)
		}
		status, outcome, input := store.statuses[id], store.outcomes[id], store.usage[id].InputTokens
		switch attempt.AttemptNo {
		case 1:
			if status != http.StatusTemporaryRedirect || outcome != "unknown" || input != nil || attempt.ProviderID != 12 || attempt.CredentialID != 44 || attempt.Model != "embed-model" {
				t.Fatalf("first hop %+v status=%d outcome=%s input=%v", attempt, status, outcome, input)
			}
		case 2:
			if status != http.StatusOK || outcome != "success" || input == nil || *input != 8 || attempt.ProviderID != 0 || attempt.CredentialID != 0 || attempt.Model != "" || attempt.Source != "http_redirect:embeddings" {
				t.Fatalf("redirect hop %+v status=%d outcome=%s input=%v", attempt, status, outcome, input)
			}
		default:
			t.Fatalf("unexpected attempt number: %+v", attempt)
		}
	}
}

func TestAudioAttemptOnlyFinishesOnRecognizedResponse(t *testing.T) {
	for _, tt := range []struct{ body, outcome string }{{`{"text":"recognized"}`, "success"}, {`{"error":"invalid"}`, ""}} {
		t.Run(tt.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			store := newMemoryAttemptStore()
			service := NewAudioService(nil, upstream.NewWithRetries(0))
			service.SetAttemptRecorder(store)
			ctx := withModalityAttemptKey(context.Background(), &authentication.KeyInfo{ID: 1, TenantID: "tenant-a"})
			_, err := service.transcribeViaMultipart(ctx, provider.Candidate{ProviderID: 17, CredentialID: 19, BaseURL: server.URL, RawModel: "voice"},
				TranscribeRequest{TenantID: "tenant-a", RequestID: "gw-audio", Filename: "audio.wav", File: []byte("fixture")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(store.attempts) != 1 {
				t.Fatalf("attempt count = %d", len(store.attempts))
			}
			for id, a := range store.attempts {
				if a.RequestID != "gw-audio" || a.TrafficClass != "business" || store.statuses[id] != http.StatusOK || store.outcomes[id] != tt.outcome {
					t.Fatalf("unexpected attempt outcome %q: %+v", store.outcomes[id], a)
				}
			}
		})
	}
}
