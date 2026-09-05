package executors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

type dispatchAuditLogger struct {
	requestEnvelope RawCorrelationEnvelope
}

func (l *dispatchAuditLogger) LogRequest(string, string, []byte) error        { return nil }
func (l *dispatchAuditLogger) LogResponse(string, string, []byte, bool) error { return nil }
func (l *dispatchAuditLogger) LogUpstreamRequestWithEnvelope(_ string, _ string, _ []byte, _ string, env RawCorrelationEnvelope) {
	l.requestEnvelope = env
}

func TestForwardForDispatchPopulatesAttemptAuditEnvelope(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	logger := &dispatchAuditLogger{}
	exec := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		RawDataLogger:   logger,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
	baseAudit := &AuditContext{RequestID: "req-1", TraceID: "hydrate-trace"}
	params := &ExecParams{
		W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:   []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}]}`),
		ClientModel: "glm-5.2", Model: "glm-5.2", RequestID: "req-1", TenantID: "default", Audit: baseAudit,
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
	params.UpstreamAttempts.TryConsume()
	params.UpstreamAttempts.TryConsume()
	candidate := provider.Candidate{
		ProviderID: 32, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-completions", RawModel: "glm-5.2", APIKey: "test-key", BillingMode: "token_plan",
	}
	dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate}, tTotal: time.Now()}

	outcome := exec.forwardForDispatch(dctx, candidate, "dispatch-audit-attempt", func() {}, t.Context())
	if outcome.Err != nil {
		t.Fatalf("forward failed: %v", outcome.Err)
	}
	env := logger.requestEnvelope
	if env.ProviderID != 32 || env.CredentialID != 22 || env.AttemptNo != 3 {
		t.Fatalf("attempt envelope = provider:%d credential:%d attempt:%d", env.ProviderID, env.CredentialID, env.AttemptNo)
	}
	if env.UpstreamEndpoint != upstream.URL || env.TraceID != "hydrate-trace" {
		t.Fatalf("attempt envelope endpoint/trace = %q/%q", env.UpstreamEndpoint, env.TraceID)
	}
	if baseAudit.ProviderID != 0 || baseAudit.CredentialID != 0 || baseAudit.AttemptNo != 0 {
		t.Fatalf("base audit context was mutated: %+v", baseAudit)
	}
}

func TestDispatchForwardUsesPipelineContextAndAttemptNumber(t *testing.T) {
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	logger := &dispatchAuditLogger{}
	exec := &Executor{
		Circuit: newCircuitManagerForTest(), Limiter: limiter, RawDataLogger: logger,
		UpstreamTimeout: 5 * time.Second, StreamTimeout: 10 * time.Second,
	}
	clientCtx, cancelClient := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(clientCtx)
	params := &ExecParams{
		W: httptest.NewRecorder(), R: req,
		BodyBytes:   []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}]}`),
		ClientModel: "glm-5.2", Model: "glm-5.2", RequestID: "req-dispatch", TenantID: "default",
		IsStream: true, SurvivalAttempt: true, StreamSurvivesClientCancel: true, Audit: &AuditContext{RequestID: "req-dispatch"},
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
	candidate := provider.Candidate{
		ProviderID: 32, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-completions", RawModel: "glm-5.2", APIKey: "test-key", BillingMode: "token_plan",
	}
	dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate}, initialModel: "glm-5.2", tTotal: time.Now()}
	dispatchCtx, cancelDispatch := dispatchExecutionContext(params)
	defer cancelDispatch()
	cancelClient()

	outcome := exec.forwardForDispatch(dctx, candidate, "dispatch-context-attempt", func() {}, dispatchCtx)
	if outcome.Err != nil {
		t.Fatalf("detached dispatch forward failed: %v", outcome.Err)
	}
	if !upstreamHit {
		t.Fatal("detached dispatch context did not reach upstream")
	}
	if logger.requestEnvelope.AttemptNo != 1 {
		t.Fatalf("first dispatch attempt = %d, want 1", logger.requestEnvelope.AttemptNo)
	}
}

func TestForwardForDispatchDegradesWhenFpSlotSaturatesAfterPrefilter(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	fpMgr := credentialfpslot.New(credentialfpslot.Config{DefaultLimit: 1, Enabled: true}, client)

	fpLimit := 1
	lease, ok := fpMgr.Acquire(t.Context(), 22, &fpLimit, "other-session", "default")
	if !ok || lease == nil {
		t.Fatal("failed to saturate fingerprint slot")
	}

	var upstreamHit bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	exec := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		FpSlots:         fpMgr,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
	params := &ExecParams{
		W:                httptest.NewRecorder(),
		R:                httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:        []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}]}`),
		ClientModel:      "glm-5.2",
		Model:            "glm-5.2",
		RequestID:        "dispatch-fp-race",
		TenantID:         "default",
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
	candidate := provider.Candidate{
		ProviderID: 32, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-completions", RawModel: "glm-5.2", APIKey: "test-key",
		FpSlotLimit: &fpLimit, BillingMode: "token_plan",
	}
	dctx := &dispatchCtx{
		params: params, candidates: []provider.Candidate{candidate},
		holder: "new-session", retryPerCred: 0, tTotal: time.Now(),
		fpSlotDegraded: false,
	}

	outcome := exec.forwardForDispatch(dctx, candidate, "dispatch-fp-attempt", func() {}, t.Context())
	if outcome.Err != nil {
		t.Fatalf("forward failed after fp-slot race degradation: %v", outcome.Err)
	}
	if outcome.Result == nil {
		t.Fatal("forward returned nil result after fp-slot race degradation")
	}
	if !upstreamHit {
		t.Fatal("upstream was not attempted after fp-slot saturation")
	}
	if !dctx.fpSlotDegraded {
		t.Fatal("dispatch context did not record fp-slot degradation")
	}
}
