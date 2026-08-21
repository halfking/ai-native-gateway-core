package executors_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

const anthropicPreCommitError = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"provider failed before output\"}}\n\n"

func newProtocolFailoverExecutor(t *testing.T) *executors.Executor {
	t.Helper()
	limiter := credential.NewLimiter()
	t.Cleanup(limiter.Stop)
	exec := executors.NewExecutor(
		executors.NewRouter(executors.NewStickyCache(), limiter),
		credential.NewManager(),
		limiter,
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, _ bool) []byte { return chunk },
		nil,
		nil,
	)
	pipeline := exec.NewDispatchPipeline()
	pipeline.Start()
	t.Cleanup(pipeline.Stop)
	exec.SetDispatchPipeline(pipeline)
	return exec
}

func protocolCandidate(baseURL, protocol, catalog, model, apiKey string, providerID, credentialID int) provider.Candidate {
	tier := 1
	if providerID%100 == 2 {
		tier = 2
	}
	return provider.Candidate{
		ProviderID:        providerID,
		CredentialID:      credentialID,
		BaseURL:           baseURL,
		Protocol:          protocol,
		CatalogCode:       catalog,
		Tier:              tier,
		Weight:            100,
		RawModel:          model,
		OfferRawModel:     model,
		APIKey:            apiKey,
		BillingMode:       "token_plan",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
	}
}

func streamResult(out executors.StreamOutcome) executors.StreamOutcome { return out }

func TestExecuteAnthropicMessages_PreCommitFailoverUsesSecondProvider(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("x-api-key"))
		paths = append(paths, r.URL.Path)
		attempt := len(auth)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if attempt <= 3 {
			_, _ = fmt.Fprint(w, anthropicPreCommitError)
			return
		}
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-ok\",\"usage\":{\"input_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"anthropic-ok\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	exec := newProtocolFailoverExecutor(t)
	exec.AnthropicPassthroughStream = func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, _ any) executors.StreamOutcome {
		return streamResult(streaming.StreamAnthropicPassthrough(w, resp, clientModel, outboundModel, requestID, capture, nil))
	}

	rec := httptest.NewRecorder()
	result, err := exec.Execute(&executors.ExecParams{
		W:              rec,
		R:              httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)),
		BodyBytes:      []byte(`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
		Model:          "claude-test",
		ClientModel:    "claude-test",
		ClientProtocol: "anthropic-messages",
		IsStream:       true,
		ClientID:       identity.ClientIdentity{IdentityHash: "anthropic-precommit-failover"},
		Candidates: []provider.Candidate{
			protocolCandidate(server.URL, "anthropic-messages", "anthropic", "claude-test", "anthropic-a", 101, 1001),
			protocolCandidate(server.URL, "anthropic-messages", "anthropic", "claude-test", "anthropic-b", 102, 1002),
		},
		Policy:                      &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		DispatchAllowProviderChange: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Candidate.CredentialID != 1002 {
		t.Fatalf("result candidate = %+v, want second provider candidate", result)
	}
	if !strings.Contains(rec.Body.String(), "anthropic-ok") || strings.Contains(rec.Body.String(), "provider failed") {
		t.Fatalf("client body = %q, want only successful Anthropic stream", rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(auth) != 4 || auth[0] != "anthropic-a" || auth[1] != "anthropic-a" || auth[2] != "anthropic-a" || auth[3] != "anthropic-b" {
		t.Fatalf("x-api-key sequence = %v, want A,A,A,B", auth)
	}
	for _, path := range paths {
		if path != "/v1/messages" {
			t.Fatalf("upstream path = %q, want /v1/messages", path)
		}
	}
}

func TestExecuteResponses_OpenAIPreCommitFailoverUsesResponsesBridge(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		attempt := len(auth)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if attempt <= 3 {
			_, _ = fmt.Fprint(w, "data: {\"id\":\"failed\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"ok\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"responses-ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	exec := newProtocolFailoverExecutor(t)
	exec.OpenAIToResponsesStream = func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, _ any) executors.StreamOutcome {
		return streamResult(streaming.StreamOpenAIToResponsesSSE(w, resp, clientModel, outboundModel, requestID, capture, nil))
	}

	rec := httptest.NewRecorder()
	result, err := exec.Execute(&executors.ExecParams{
		W:              rec,
		R:              httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)),
		BodyBytes:      []byte(`{"model":"gpt-test","input":[],"stream":true}`),
		Model:          "gpt-test",
		ClientModel:    "gpt-test",
		ClientProtocol: "openai-responses",
		IsStream:       true,
		ClientID:       identity.ClientIdentity{IdentityHash: "responses-precommit-failover"},
		Candidates: []provider.Candidate{
			protocolCandidate(server.URL, "openai-completions", "openai", "gpt-test", "responses-a", 201, 2001),
			protocolCandidate(server.URL, "openai-completions", "openai", "gpt-test", "responses-b", 202, 2002),
		},
		Policy:                      &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		DispatchAllowProviderChange: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Candidate.CredentialID != 2002 {
		t.Fatalf("result candidate = %+v, want second provider candidate", result)
	}
	wire := rec.Body.String()
	if !strings.Contains(wire, "responses-ok") || !strings.Contains(wire, "response.completed") {
		t.Fatalf("Responses body = %q, want successful bridge output", wire)
	}
	if strings.Contains(wire, "chat.completion.chunk") || strings.Contains(wire, "failed") {
		t.Fatalf("raw/failed upstream output leaked: %q", wire)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(auth) != 4 || auth[0] != "Bearer responses-a" || auth[1] != "Bearer responses-a" || auth[2] != "Bearer responses-a" || auth[3] != "Bearer responses-b" {
		t.Fatalf("Authorization sequence = %v, want A,A,A,B", auth)
	}
}

func TestExecuteResponses_AnthropicPreCommitFailoverUsesResponsesBridge(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("x-api-key"))
		attempt := len(keys)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if attempt <= 3 {
			_, _ = fmt.Fprint(w, anthropicPreCommitError)
			return
		}
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-resp\",\"usage\":{\"input_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"anthropic-responses-ok\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	exec := newProtocolFailoverExecutor(t)
	exec.AnthropicToResponsesStream = func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, _ any) executors.StreamOutcome {
		return streamResult(streaming.StreamAnthropicSSEToResponses(w, resp, clientModel, outboundModel, requestID, capture, nil))
	}

	rec := httptest.NewRecorder()
	result, err := exec.Execute(&executors.ExecParams{
		W:              rec,
		R:              httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)),
		BodyBytes:      []byte(`{"model":"claude-test","input":[],"stream":true}`),
		Model:          "claude-test",
		ClientModel:    "claude-test",
		ClientProtocol: "openai-responses",
		IsStream:       true,
		ClientID:       identity.ClientIdentity{IdentityHash: "responses-anthropic-precommit-failover"},
		Candidates: []provider.Candidate{
			protocolCandidate(server.URL, "anthropic-messages", "anthropic", "claude-test", "anthropic-responses-a", 301, 3001),
			protocolCandidate(server.URL, "anthropic-messages", "anthropic", "claude-test", "anthropic-responses-b", 302, 3002),
		},
		Policy:                      &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		DispatchAllowProviderChange: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Candidate.CredentialID != 3002 {
		t.Fatalf("result candidate = %+v, want second provider candidate", result)
	}
	if !strings.Contains(rec.Body.String(), "anthropic-responses-ok") || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("Responses body = %q, want successful Anthropic bridge output", rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 4 || keys[0] != "anthropic-responses-a" || keys[1] != "anthropic-responses-a" || keys[2] != "anthropic-responses-a" || keys[3] != "anthropic-responses-b" {
		t.Fatalf("x-api-key sequence = %v, want A,A,A,B", keys)
	}
}

func TestExecuteAnthropicMessages_PostCommitFailureDoesNotCallSecondProvider(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("x-api-key"))
		attempt := len(keys)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"committed\"}}\n\n")
			_, _ = fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"after output\"}}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	exec := newProtocolFailoverExecutor(t)
	exec.AnthropicPassthroughStream = func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, _ any) executors.StreamOutcome {
		return streamResult(streaming.StreamAnthropicPassthrough(w, resp, clientModel, outboundModel, requestID, capture, nil))
	}
	capture := &audit.StreamCapture{}
	rec := httptest.NewRecorder()
	_, err := exec.Execute(&executors.ExecParams{
		W:              rec,
		R:              httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)),
		BodyBytes:      []byte(`{"model":"claude-test","max_tokens":16,"messages":[],"stream":true}`),
		Model:          "claude-test",
		ClientModel:    "claude-test",
		ClientProtocol: "anthropic-messages",
		IsStream:       true,
		ClientID:       identity.ClientIdentity{IdentityHash: "anthropic-postcommit-no-switch"},
		Capture:        capture,
		Candidates: []provider.Candidate{
			protocolCandidate(server.URL, "anthropic-messages", "anthropic", "claude-test", "post-a", 401, 4001),
			protocolCandidate(server.URL, "anthropic-messages", "anthropic", "claude-test", "post-b", 402, 4002),
		},
		Policy: &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0}, DispatchAllowProviderChange: true,
	})
	if err == nil {
		t.Fatal("expected post-commit stream interruption")
	}
	if !strings.Contains(rec.Body.String(), "committed") || !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("client body = %q, want committed content and terminal error", rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 1 || keys[0] != "post-a" {
		t.Fatalf("provider requests = %v, want only first provider", keys)
	}
}

func TestExecuteResponses_PostCommitFailureDoesNotCallSecondProvider(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"committed\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE-BROKEN]\n\n")
	}))
	defer server.Close()

	exec := newProtocolFailoverExecutor(t)
	exec.OpenAIToResponsesStream = func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, _ any) executors.StreamOutcome {
		return streamResult(streaming.StreamOpenAIToResponsesSSE(w, resp, clientModel, outboundModel, requestID, capture, nil))
	}
	capture := &audit.StreamCapture{}
	rec := httptest.NewRecorder()
	_, err := exec.Execute(&executors.ExecParams{
		W:              rec,
		R:              httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)),
		BodyBytes:      []byte(`{"model":"gpt-test","input":[],"stream":true}`),
		Model:          "gpt-test",
		ClientModel:    "gpt-test",
		ClientProtocol: "openai-responses",
		IsStream:       true,
		ClientID:       identity.ClientIdentity{IdentityHash: "responses-postcommit-no-switch"},
		Capture:        capture,
		Candidates: []provider.Candidate{
			protocolCandidate(server.URL, "openai-completions", "openai", "gpt-test", "responses-post-a", 501, 5001),
			protocolCandidate(server.URL, "openai-completions", "openai", "gpt-test", "responses-post-b", 502, 5002),
		},
		Policy: &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0}, DispatchAllowProviderChange: true,
	})
	if err == nil {
		t.Fatal("expected post-commit Responses interruption")
	}
	wire := rec.Body.String()
	if !strings.Contains(wire, "committed") || !strings.Contains(wire, `"status":"incomplete"`) {
		t.Fatalf("Responses body = %q, want committed content and incomplete terminal envelope", wire)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auth) != 1 || auth[0] != "Bearer responses-post-a" {
		t.Fatalf("provider requests = %v, want only first provider", auth)
	}
}
