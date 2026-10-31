package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func writeTimeBlockingRedactor() func([]byte, string, string) []byte {
	checker := outputcompliance.NewBuiltinChecker()
	checker.SetManagedRuleSource(func() outputcompliance.ManagedRuleCheck {
		return func(context.Context, string, string) (*outputcompliance.ComplianceResult, error) {
			return &outputcompliance.ComplianceResult{Blocked: true}, nil
		}
	})
	return BuildRedactBodyFn(checker, nil, nil)
}

func assertWriteTimePolicyFailure(t *testing.T, status int, body string, entries []*telemetry.RequestLogEntry, original string) {
	t.Helper()
	if status != http.StatusForbidden || !strings.Contains(body, "output_policy_blocked") || strings.Contains(body, original) {
		t.Errorf("write-time block must return safe 403: status=%d body=%q", status, body)
	}
	failures := 0
	for _, entry := range entries {
		if entry.Success {
			t.Errorf("blocked output emitted success audit: %+v", entry)
		}
		if entry.ResponseBody != nil && strings.Contains(*entry.ResponseBody, original) {
			t.Errorf("blocked output persisted original response")
		}
		if entry.ErrorKind != nil && *entry.ErrorKind == "output_policy_blocked" {
			failures++
			if entry.ProviderID == nil || *entry.ProviderID != 11 || entry.CredentialID == nil || *entry.CredentialID != 22 {
				t.Errorf("policy audit lost provider attribution: %+v", entry)
			}
		}
	}
	if failures != 1 {
		t.Errorf("want exactly one policy failure audit, got failures=%d rows=%d", failures, len(entries))
	}
}

func TestChatWriteTimePolicyBlockReturns403AndOneFailureAudit(t *testing.T) {
	r1013RequireComplianceOn(t)
	rec, entries := runChatOutputCommit(t, nil, writeTimeBlockingRedactor())
	assertWriteTimePolicyFailure(t, rec.Code, rec.Body.String(), entries, "13800138000")
}

type writePolicyResolver struct{ candidates []provider.Candidate }

func (writePolicyResolver) Enabled() bool                           { return true }
func (writePolicyResolver) ModelKnown(context.Context, string) bool { return true }

func (writePolicyResolver) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	// 默认：不是模态不匹配。既有测试的语义是「零候选」，不应被新的
	// 模态诊断分支改变行为；需要覆盖模态分支的测试单独提供自己的替身。
	return nil, nil
}
func (r writePolicyResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return r.candidates, provider.DefaultPolicy(), nil
}
func (r writePolicyResolver) GetCandidatesByModality(ctx context.Context, model, profile, tenant, _ string) ([]provider.Candidate, *provider.Policy, error) {
	return r.GetCandidates(ctx, model, profile, tenant)
}

type writePolicyHealthCapture struct{ calls atomic.Int64 }

func (c *writePolicyHealthCapture) ApplyNodeHealthDecision(context.Context, nodehealth.Decision) error {
	c.calls.Add(1)
	return nil
}

type writePolicyGoal struct{ reads, retries atomic.Int64 }

func (g *writePolicyGoal) GetSession(context.Context, string, string) (*goal.Session, error) {
	g.reads.Add(1)
	return &goal.Session{}, nil
}
func (g *writePolicyGoal) AddRetryCount(_ context.Context, _, _ string, delta int) error {
	g.retries.Add(int64(delta))
	return nil
}
func (*writePolicyGoal) ResolveGoalRetryPolicy(string) GoalRetryPolicy {
	return GoalRetryPolicy{Enabled: true, MaxRetries: 2, TotalTimeout: 5 * time.Second,
		BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
}

func TestWriteTimePolicyBlockIsTerminalOnEveryHTTPFace(t *testing.T) {
	r1013RequireComplianceOn(t)
	for _, tc := range []struct {
		name, path, request, protocol, providerBody string
		serve                                       func(*ChatHandler) http.Handler
	}{
		{"chat", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			"openai-completions", chatOutputProviderBody, func(h *ChatHandler) http.Handler { return h }},
		{"messages", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			"openai-completions", chatOutputProviderBody, func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"m","input":"hi"}`,
			"openai-responses", `{"id":"resp_1","object":"response","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"13800138000"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`,
			func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls, redactionCalls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.providerBody))
			}))
			defer upstream.Close()
			limiter := credential.NewLimiter()
			defer limiter.Stop()
			executor := executors.NewExecutor(executors.NewRouter(executors.NewStickyCache(), limiter),
				credential.NewManager(), limiter, pool.NewPoolManager(nil), nil, nil, nil, nil)
			blockedBody := writeTimeBlockingRedactor()([]byte(chatOutputProviderBody), "", "")
			executor.RedactBodyFn = func([]byte, string, string) []byte {
				redactionCalls.Add(1)
				return blockedBody
			}
			clientLogger := &nativeClientResponseLogger{}
			executor.RawDataLogger = clientLogger
			health := &writePolicyHealthCapture{}
			executor.NodeHealthAdapter = health
			pipeline := executor.NewDispatchPipeline(nil)
			pipeline.Start()
			defer pipeline.Stop()
			executor.SetDispatchPipeline(pipeline)
			candidate := provider.Candidate{
				ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL, Protocol: tc.protocol,
				CatalogCode: "openai", RawModel: "m", OfferRawModel: "m", APIKey: "upstream-key",
				Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true,
				LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
				SupportsNativeResponses: tc.protocol == "openai-responses",
			}
			sibling := candidate
			sibling.ProviderID, sibling.CredentialID, sibling.Tier = 33, 44, 2
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.executor = executor
			h.provider = writePolicyResolver{[]provider.Candidate{candidate, sibling}}
			goalRetry := &writePolicyGoal{}
			if tc.name == "chat" {
				h.SetGoalRetryPolicyResolver(goalRetry)
				h.SetGoalRetryRecorder(goalRetry)
			}
			owner := "alice"
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", OwnerUser: &owner}})
			client := telemetry.NewClient()
			client.SetRequestLogSink(nativeOutputAuditSink{})
			defer client.Stop()
			h.SetTelemetry(client)
			var entries []*telemetry.RequestLogEntry
			h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) {
				copy := *entry
				entries = append(entries, &copy)
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.request))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", "gw_write-time-policy")
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, req)
			assertWriteTimePolicyFailure(t, rec.Code, rec.Body.String(), entries, "13800138000")
			if got := upstreamCalls.Load(); got != 1 {
				t.Errorf("policy block retried or failed over: upstream calls=%d", got)
			}
			if got := redactionCalls.Load(); got != 1 {
				t.Errorf("write-time redaction calls=%d, want one", got)
			}
			if got := health.calls.Load(); got != 0 {
				t.Errorf("local policy changed provider health: calls=%d", got)
			}
			if got := clientLogger.clientBodies(); len(got) != 0 {
				t.Errorf("policy block logged approved client bytes: %q", got)
			}
			if tc.name == "chat" && (goalRetry.reads.Load() == 0 || goalRetry.retries.Load() != 0) {
				t.Errorf("active goal must terminate without retry: session checks=%d retries=%d", goalRetry.reads.Load(), goalRetry.retries.Load())
			}
		})
	}
}
