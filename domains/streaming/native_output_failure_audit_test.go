package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

type nativeOutputAuditSink struct{}

func (nativeOutputAuditSink) PersistRequestLog(context.Context, *telemetry.RequestLogEntry) error {
	return nil
}

type nativeClientResponseLogger struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (*nativeClientResponseLogger) LogRequest(string, string, []byte) error { return nil }
func (*nativeClientResponseLogger) LogResponse(string, string, []byte, bool) error {
	return nil
}
func (l *nativeClientResponseLogger) LogClientResponse(_ string, _ string, body []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bodies = append(l.bodies, append([]byte(nil), body...))
	return nil
}
func (l *nativeClientResponseLogger) clientBodies() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]byte(nil), l.bodies...)
}

type nativeOutputAuditResolver struct{ candidate provider.Candidate }

func (r nativeOutputAuditResolver) Enabled() bool                           { return true }
func (r nativeOutputAuditResolver) ModelKnown(context.Context, string) bool { return true }
func (r nativeOutputAuditResolver) GetCandidates(ctx context.Context, model, profile, tenant string) ([]provider.Candidate, *provider.Policy, error) {
	return r.GetCandidatesByModality(ctx, model, profile, tenant, "text")
}
func (r nativeOutputAuditResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return []provider.Candidate{r.candidate}, provider.DefaultPolicy(), nil
}

func TestNativeNonStreamOutputBlockRecordsFailureWithoutProviderBody(t *testing.T) {
	for _, tc := range []struct {
		name, path, request, protocol, providerBody string
		serve                                       func(*ChatHandler) http.Handler
	}{
		{
			name: "messages", path: "/v1/messages", protocol: "anthropic-messages",
			request:      `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			providerBody: `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"private provider body"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`,
			serve:        func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) },
		},
		{
			name: "responses", path: "/v1/responses", protocol: "openai-responses",
			request:      `{"model":"m","input":"hi"}`,
			providerBody: `{"id":"resp_1","object":"response","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"private provider body"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`,
			serve:        func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.providerBody))
			}))
			defer upstream.Close()

			limiter := credential.NewLimiter()
			defer limiter.Stop()
			executor := executors.NewExecutor(
				executors.NewRouter(executors.NewStickyCache(), limiter),
				credential.NewManager(), limiter, pool.NewPoolManager(nil), nil, nil, nil, nil,
			)
			clientLogger := &nativeClientResponseLogger{}
			executor.RawDataLogger = clientLogger
			pipeline := executor.NewDispatchPipeline(nil)
			pipeline.Start()
			defer pipeline.Stop()
			executor.SetDispatchPipeline(pipeline)

			candidate := provider.Candidate{
				ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL, Protocol: tc.protocol,
				CatalogCode: "native-test", RawModel: "m", OfferRawModel: "m", APIKey: "upstream-key",
				Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true,
				LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
				SupportsNativeResponses: tc.protocol == "openai-responses",
			}
			telemetryClient := telemetry.NewClient()
			telemetryClient.SetRequestLogSink(nativeOutputAuditSink{})
			defer telemetryClient.Stop()
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.executor = executor
			h.provider = nativeOutputAuditResolver{candidate: candidate}
			owner := "alice"
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{
				ID: 42, TenantID: "tenant-1", ApplicationID: 7, OwnerUser: &owner,
			}})
			h.SetTelemetry(telemetryClient)
			interceptor := &nativeNonStreamInterceptor{blocked: true}
			h.SetResponseInterceptor(interceptor)
			var entries []*telemetry.RequestLogEntry
			h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) {
				copy := *entry
				entries = append(entries, &copy)
			})

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.request))
			req.Header.Set("Authorization", "Bearer sk-test")
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden || interceptor.seen == nil || interceptor.seen.CallerOwner != owner {
				t.Fatalf("output policy did not block native provider response: status=%d seen=%v body=%q", rec.Code, interceptor.seen != nil, rec.Body.String())
			}
			if len(entries) == 0 {
				t.Fatal("no request audit entries")
			}
			failures := 0
			for _, entry := range entries {
				if entry.Success {
					t.Fatalf("blocked output emitted success audit: %+v", entry)
				}
				if entry.ResponseBody != nil && strings.Contains(*entry.ResponseBody, "private provider body") {
					t.Fatalf("blocked provider body persisted: %+v", entry)
				}
				if entry.ErrorKind != nil && *entry.ErrorKind == "output_policy_blocked" {
					failures++
					if entry.ProviderID == nil || *entry.ProviderID != 11 || entry.CredentialID == nil || *entry.CredentialID != 22 {
						t.Fatalf("failure lost chosen provider identity: %+v", entry)
					}
				}
			}
			if failures != 1 {
				t.Fatalf("output_policy_blocked rows=%d, entries=%+v", failures, entries)
			}
			if got := clientLogger.clientBodies(); len(got) != 0 {
				t.Fatalf("blocked output was recorded as approved client response: %q", got)
			}
		})
	}
}

func TestChatNonStreamInterceptorUsesAuthenticatedOwner(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","object":"chat.completion","model":"m","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`))
	}))
	defer upstream.Close()
	limiter := credential.NewLimiter()
	defer limiter.Stop()
	executor := executors.NewExecutor(executors.NewRouter(executors.NewStickyCache(), limiter),
		credential.NewManager(), limiter, pool.NewPoolManager(nil), nil, nil, nil, nil)
	pipeline := executor.NewDispatchPipeline(nil)
	pipeline.Start()
	defer pipeline.Stop()
	executor.SetDispatchPipeline(pipeline)
	owner := "alice"
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = executor
	h.provider = nativeOutputAuditResolver{candidate: provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL, Protocol: "openai-completions",
		CatalogCode: "openai", RawModel: "m", OfferRawModel: "m", APIKey: "upstream-key",
		Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true,
		LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
	}}
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", OwnerUser: &owner}})
	interceptor := &nativeNonStreamInterceptor{}
	h.SetResponseInterceptor(interceptor)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || interceptor.seen == nil || interceptor.seen.CallerOwner != owner {
		t.Fatalf("chat interceptor owner missing: status=%d seen=%+v body=%q", rec.Code, interceptor.seen, rec.Body.String())
	}
}
