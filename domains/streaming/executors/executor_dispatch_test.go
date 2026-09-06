package executors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// TestDispatchErrMapping locks the contract that dispatch pipeline errors are
// wrapped into *ExecuteError so the handler's Exhausted branch emits 503 (not
// 502) and goal-retry recognises the kind.
func TestDispatchErrMapping(t *testing.T) {
	cases := []struct {
		name    string
		in      error
		wantExh bool
		wantKnd errorsx.ErrorKind
	}{
		{"no-route", dispatch.ErrNoRoute, true, errorsx.KindConcurrent},
		{"overflow", dispatch.ErrOverflow, true, errorsx.KindConcurrent},
		{"deadline", context.DeadlineExceeded, true, errorsx.KindTimeout},
		{"forward-sentinel", errDispatchCircuitOpen, true, errorsx.KindTransient},
		{"generic", errors.New("boom"), true, errorsx.KindTransient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ee := dispatchErrToExecuteError(c.in)
			if !ee.Exhausted {
				t.Fatalf("Exhausted must be true for %q", c.name)
			}
			if ee.LastKind != c.wantKnd {
				t.Fatalf("kind = %q, want %q", ee.LastKind, c.wantKnd)
			}
			if !errors.Is(ee.LastErr, c.in) {
				t.Fatalf("LastErr does not wrap input: got %v", ee.LastErr)
			}
		})
	}
}

func TestExecuteDispatchStopsAtExactly100UpstreamAttempts(t *testing.T) {
	var (
		upstreamCalls atomic.Int64
		callsByNode   = map[string]int{}
		callsMu       sync.Mutex
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		call := upstreamCalls.Add(1)
		if call > int64(DefaultUpstreamAttemptLimit) {
			t.Errorf("upstream call %d exceeded attempt limit", call)
		}
		callsMu.Lock()
		callsByNode[body.Model+"/"+r.Header.Get("Authorization")]++
		callsMu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"persistent upstream failure"}}`))
	}))
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	exec := NewExecutor(
		NewRouter(NewStickyCache(), limiter), newCircuitManagerForTest(), limiter,
		pool.NewPoolManager(nil), nil, nil, nil, nil,
	)
	wireDispatchPipelineForTest(t, exec)

	const initialModel = "attempt-cap-model"
	exec.Provider = &attemptCapProvider{baseURL: upstream.URL}
	exec.SetDispatchModelRecommender(attemptCapRecommender{})
	dispatch.SetModelChangeEnabled(true)
	defer dispatch.SetModelChangeEnabled(false)
	candidates := attemptCapCandidates(upstream.URL, initialModel, 0)

	budget := NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit)
	_, err := exec.Execute(&ExecParams{
		W:                           httptest.NewRecorder(),
		R:                           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:                   []byte(`{"model":"attempt-cap-model","messages":[{"role":"user","content":"fail"} ]}`),
		ClientProtocol:              "openai-completions",
		ClientModel:                 initialModel,
		Model:                       initialModel,
		RequestID:                   "dispatch-attempt-cap",
		Candidates:                  candidates,
		Policy:                      &provider.Policy{TierFallbackMax: DefaultUpstreamAttemptLimit, RetryPerCredential: dispatch.MaxNodeFailures - 1},
		DispatchAllowProviderChange: true,
		DispatchAllowModelChange:    true,
		DispatchModelAlternatives: []string{
			"attempt-cap-model-b", "attempt-cap-model-c", "attempt-cap-model-d",
			"attempt-cap-model-e", "attempt-cap-model-f", "attempt-cap-model-g",
			"attempt-cap-model-h", "attempt-cap-model-i", "attempt-cap-model-j",
		},
		UpstreamAttempts: budget,
	})
	if err == nil {
		t.Fatal("expected dispatch to terminate after upstream failures")
	}
	if got := upstreamCalls.Load(); got != int64(DefaultUpstreamAttemptLimit) {
		t.Fatalf("upstream calls = %d, want exactly %d", got, DefaultUpstreamAttemptLimit)
	}
	if got := budget.Used(); got != DefaultUpstreamAttemptLimit {
		t.Fatalf("consumed upstream attempts = %d, want exactly %d", got, DefaultUpstreamAttemptLimit)
	}

	callsMu.Lock()
	defer callsMu.Unlock()
	for credentialID := 1; credentialID <= 12; credentialID++ {
		key := initialModel + "/Bearer key-" + strconv.Itoa(credentialID)
		if got := callsByNode[key]; got != dispatch.MaxNodeFailures {
			t.Fatalf("upstream calls for %s = %d, want %d", key, got, dispatch.MaxNodeFailures)
		}
	}
	for credentialID := 1001; credentialID <= 1012; credentialID++ {
		key := "attempt-cap-model-b/Bearer key-" + strconv.Itoa(credentialID)
		if got := callsByNode[key]; got != dispatch.MaxNodeFailures {
			t.Fatalf("upstream calls for %s = %d, want %d", key, got, dispatch.MaxNodeFailures)
		}
	}
}

type attemptCapProvider struct {
	baseURL string
}

func (p *attemptCapProvider) Enabled() bool { return true }

func (p *attemptCapProvider) ModelKnown(context.Context, string) bool { return true }

func (p *attemptCapProvider) GetCandidates(_ context.Context, model, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	return attemptCapCandidates(p.baseURL, model, 1000), &provider.Policy{TierFallbackMax: DefaultUpstreamAttemptLimit, RetryPerCredential: dispatch.MaxNodeFailures - 1}, nil
}

func attemptCapCandidates(baseURL, model string, offset int) []provider.Candidate {
	candidates := make([]provider.Candidate, 0, 12)
	for i := 0; i < 12; i++ {
		id := offset + i + 1
		candidate := overloadTestCandidate(baseURL)
		candidate.ProviderID = id
		candidate.CredentialID = id
		candidate.RawModel = model
		candidate.OfferRawModel = model
		candidate.StandardizedName = model
		candidate.APIKey = "key-" + strconv.Itoa(id)
		candidates = append(candidates, candidate)
	}
	return candidates
}

type attemptCapRecommender struct{}

func (attemptCapRecommender) RecommendModelAlternatives(_ context.Context, req autoroute.ModelAlternativeRequest) ([]string, error) {
	return append([]string(nil), req.PreferredModels...), nil
}

func TestDispatchFailureIsCredentialHealthy(t *testing.T) {
	cases := []struct {
		kind          errorsx.ErrorKind
		modelNotFound bool
		want          bool
	}{
		{kind: errorsx.KindClientBug, want: true},
		{kind: errorsx.KindToolCallIdMismatch, want: true},
		{kind: errorsx.KindContentFilter, want: true},
		{kind: errorsx.KindContextLength, want: true},
		{kind: errorsx.KindModelNotFound, modelNotFound: true, want: true},
		{kind: errorsx.KindAuth, want: false},
		{kind: errorsx.KindQuotaPeriodic, want: false},
		{kind: errorsx.KindUpstreamDown, want: false},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := dispatchFailureIsCredentialHealthy(tc.kind, tc.modelNotFound); got != tc.want {
				t.Fatalf("dispatchFailureIsCredentialHealthy(%q, %v) = %v, want %v", tc.kind, tc.modelNotFound, got, tc.want)
			}
		})
	}
}

type dispatchHealthCapture struct {
	mu        sync.Mutex
	decisions []nodehealth.Decision
}

func (c *dispatchHealthCapture) ApplyNodeHealthDecision(_ context.Context, decision nodehealth.Decision) error {
	c.mu.Lock()
	c.decisions = append(c.decisions, decision)
	c.mu.Unlock()
	return nil
}

func (c *dispatchHealthCapture) snapshot() []nodehealth.Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]nodehealth.Decision(nil), c.decisions...)
}

func TestDispatchUsesJourneyAttemptIDForNodeHealthReduction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	capture := &dispatchHealthCapture{}
	exec := &Executor{
		Circuit:            newCircuitManagerForTest(),
		Limiter:            limiter,
		UpstreamTimeout:    5 * time.Second,
		StreamTimeout:      10 * time.Second,
		NodeOutcomeReducer: nodehealth.NewOutcomeReducer(),
		NodeHealthAdapter:  capture,
	}
	candidate := provider.Candidate{
		ProviderID: 7, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-completions", RawModel: "vendor-model-a", StandardizedName: "model-a",
		APIKey: "test-key", BillingMode: "token_plan", CatalogCode: "vendor-a",
	}
	params := &ExecParams{
		W:           httptest.NewRecorder(),
		R:           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:   []byte(`{"model":"model-a","messages":[{"role":"user","content":"hello"}]}`),
		ClientModel: "model-a",
		Model:       "model-a",
		RequestID:   "dispatch-health-attempt",
		TenantID:    "tenant-a",
	}
	dctx := &dispatchCtx{
		params: params, candidates: []provider.Candidate{candidate}, byModel: mapCandidatesByModel([]provider.Candidate{candidate}),
		initialModel: "model-a", retryPerCred: 0, tTotal: time.Now(),
	}

	journeyAttemptIDCh := make(chan string, 1)
	pipeline := dispatch.NewPipeline(dispatch.Deps{
		RouteFunc: func(context.Context, *dispatch.QueuedRequest) ([]dispatch.CredentialRef, error) {
			return []dispatch.CredentialRef{candidateToRef(candidate)}, nil
		},
		ModelResolveFunc: func(_ context.Context, requested string, _ []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: exec.dispatchForward,
		ObservationSink: dispatch.ObservationSinkFunc(func(_ context.Context, observation dispatch.Observation) {
			if observation.Type == dispatch.ObservationAttemptStarted && observation.Attempt != nil {
				select {
				case journeyAttemptIDCh <- observation.Attempt.AttemptID:
				default:
				}
			}
		}),
	})
	pipeline.Start()
	defer pipeline.Stop()

	qr := dispatch.NewQueuedRequest(params.RequestID, params.TenantID, params.Model, params.R.Context(), dctx)
	qr.GatewayInstanceID = "gateway-test"
	result, err := pipeline.Submit(params.R.Context(), qr)
	if err != nil || result == nil {
		t.Fatalf("Submit() = (%v, %v), want success", result, err)
	}
	var journeyAttemptID string
	select {
	case journeyAttemptID = <-journeyAttemptIDCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for journey attempt ID")
	}
	if _, err := uuid.Parse(journeyAttemptID); err != nil {
		t.Fatalf("journey attempt ID %q is not a UUID: %v", journeyAttemptID, err)
	}
	decisions := capture.snapshot()
	if len(decisions) != 1 {
		t.Fatalf("node-health decisions = %+v, want exactly one", decisions)
	}
	decision := decisions[0]
	if decision.AttemptID != journeyAttemptID {
		t.Fatalf("reducer attempt ID = %q, journey UUID = %q", decision.AttemptID, journeyAttemptID)
	}
	// Node health keys by the binding-scoped raw model (OfferRawModel →
	// RawModel fallback), NOT the standardized client name: a client-facing
	// name can map to multiple provider bindings and keying by it would
	// merge their empty-response / health windows. The fixture sets
	// RawModel="vendor-model-a" with no OfferRawModel, so the reducer's
	// BindingRawModel() fallback must yield "vendor-model-a".
	if decision.Node != (nodehealth.NodeKey{TenantID: "tenant-a", ProviderID: 7, CredentialID: 22, Model: "vendor-model-a"}) {
		t.Fatalf("node = %+v", decision.Node)
	}
	if decision.RequestID != params.RequestID || decision.BillingMode != candidate.BillingMode || string(decision.Outcome) != "success" {
		t.Fatalf("observation metadata lost: %+v", decision)
	}
}

func TestForwardForDispatchReducesFailureAndCancellationOnce(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable"}}`))
		}))
		defer upstream.Close()

		capture := &dispatchHealthCapture{}
		limiter := newLimiterForTest()
		defer limiter.Stop()
		exec := &Executor{
			Circuit: newCircuitManagerForTest(), Limiter: limiter,
			UpstreamTimeout: 5 * time.Second, StreamTimeout: 10 * time.Second,
			NodeOutcomeReducer: nodehealth.NewOutcomeReducer(), NodeHealthAdapter: capture,
		}
		params := &ExecParams{
			W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			BodyBytes:   []byte(`{"model":"model-a","messages":[{"role":"user","content":"hello"}]}`),
			ClientModel: "model-a", Model: "model-a", RequestID: "dispatch-failure", TenantID: "tenant-a",
		}
		candidate := provider.Candidate{
			ProviderID: 7, CredentialID: 22, BaseURL: upstream.URL, Protocol: "openai-completions",
			RawModel: "vendor-model-a", StandardizedName: "model-a", APIKey: "test-key", BillingMode: "token_plan",
		}
		dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate}, retryPerCred: 0, tTotal: time.Now()}

		out := exec.forwardForDispatch(dctx, candidate, "failure-attempt", func() {})
		if out.Err == nil || out.ErrorKind == "" || out.HTTPStatus != http.StatusInternalServerError {
			t.Fatalf("forward outcome = %+v", out)
		}
		decisions := capture.snapshot()
		if len(decisions) != 1 {
			t.Fatalf("decisions = %+v, want exactly one", decisions)
		}
		decision := decisions[0]
		if decision.AttemptID != "failure-attempt" || decision.Outcome != requestjourney.OutcomeFailure || decision.HTTPStatus != http.StatusInternalServerError || decision.ErrorDetail == "" {
			t.Fatalf("failure decision = %+v", decision)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		capture := &dispatchHealthCapture{}
		limiter := newLimiterForTest()
		defer limiter.Stop()
		exec := &Executor{
			Circuit: newCircuitManagerForTest(), Limiter: limiter,
			NodeOutcomeReducer: nodehealth.NewOutcomeReducer(), NodeHealthAdapter: capture,
		}
		params := &ExecParams{
			W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx),
			BodyBytes: []byte(`{"model":"model-a"}`), ClientModel: "model-a", Model: "model-a",
			RequestID: "dispatch-canceled", TenantID: "tenant-a",
		}
		candidate := provider.Candidate{
			ProviderID: 7, CredentialID: 22, Protocol: "openai-completions",
			RawModel: "vendor-model-a", StandardizedName: "model-a", BillingMode: "token_plan",
		}
		dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate}, retryPerCred: 0, tTotal: time.Now()}

		out := exec.forwardForDispatch(dctx, candidate, "canceled-attempt", func() {})
		if !errors.Is(out.Err, context.Canceled) {
			t.Fatalf("forward error = %v, want context canceled", out.Err)
		}
		decisions := capture.snapshot()
		if len(decisions) != 1 {
			t.Fatalf("decisions = %+v, want exactly one", decisions)
		}
		decision := decisions[0]
		if decision.AttemptID != "canceled-attempt" || decision.Outcome != requestjourney.OutcomeCanceled || len(decision.Effects) != 0 {
			t.Fatalf("canceled decision = %+v", decision)
		}
	})
}

func TestForwardForDispatchAcceptsStreamOnlyNativeCapability(t *testing.T) {
	// Audit-2026-08-29: regression guard for the dispatch↔executeOpenAI
	// capability-gate asymmetry. native_responses_stream is verified
	// independently of native_responses_nonstream (migration 612), so a
	// credential that opts in to SSE only must NOT be rejected by the
	// dispatch gate before executeOpenAI has a chance to forward it.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"event: response.created\ndata: {\"type\":\"response.created\"}\n\n"+
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"+
				"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	exec := newOverloadTestExecutor()
	exec.Circuit = newCircuitManagerForTest()
	exec.Limiter = limiter
	exec.NativeResponsesStream = func(_ context.Context, w http.ResponseWriter, resp *http.Response, _ string, _ *audit.StreamCapture, _ *atomic.Bool) StreamOutcome {
		defer resp.Body.Close()
		_, _ = io.Copy(w, resp.Body)
		return StreamOutcome{ChunkCount: 1}
	}

	candidate := provider.Candidate{
		CredentialID:                  33,
		ProviderID:                    44,
		BaseURL:                       upstream.URL,
		Protocol:                      "openai-responses",
		CatalogCode:                   "openai",
		RawModel:                      "gpt-responses-stream-only",
		APIKey:                        "sk-stream-only",
		SupportsNativeResponsesStream: true,
		// SupportsNativeResponses intentionally false: stream-only credential.
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
	}
	params := &ExecParams{
		W:                  httptest.NewRecorder(),
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		IsStream:           true,
		BodyBytes:          []byte(`{"model":"gpt-responses","messages":[]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-responses","input":"hello"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-responses",
		OutboundModel:      "gpt-responses",
		RequestID:          "dispatch-stream-only-cap",
	}
	dctx := &dispatchCtx{
		params:       params,
		candidates:   []provider.Candidate{candidate},
		retryPerCred: 0,
		tTotal:       time.Now(),
	}

	out := exec.forwardForDispatch(dctx, candidate, "stream-only-attempt", func() {})
	if out.Err != nil {
		t.Fatalf("forward outcome err = %v, want stream-only capability to be honoured", out.Err)
	}
	result, ok := out.Result.(*ExecuteResult)
	if !ok || result == nil || result.Response == nil {
		t.Fatalf("forward outcome result = %+v, want upstream call to succeed", out.Result)
	}
}

func TestForwardForDispatchRejectsNoNativeCapability(t *testing.T) {
	// Counter-case to TestForwardForDispatchAcceptsStreamOnlyNativeCapability:
	// a credential without either stream OR non-stream native Responses
	// capability must still be rejected at the dispatch gate (the executeOpenAI
	// gate would reject it too, but failing fast at dispatch keeps the audit
	// signal clean and avoids burning an upstream circuit probe).
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	limiter := newLimiterForTest()
	defer limiter.Stop()
	exec := newOverloadTestExecutor()
	exec.Circuit = newCircuitManagerForTest()
	exec.Limiter = limiter

	candidate := provider.Candidate{
		CredentialID: 33,
		ProviderID:   44,
		BaseURL:      upstream.URL,
		Protocol:     "openai-responses",
		CatalogCode:  "openai",
		RawModel:     "gpt-responses-no-cap",
		APIKey:       "sk-no-cap",
		// Neither SupportsNativeResponses nor SupportsNativeResponsesStream.
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
	}
	params := &ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		IsStream:           true,
		BodyBytes:          []byte(`{"model":"gpt-responses","messages":[]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-responses","input":"hello"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-responses",
		RequestID:          "dispatch-no-native-cap",
	}
	dctx := &dispatchCtx{
		params:       params,
		candidates:   []provider.Candidate{candidate},
		retryPerCred: 0,
		tTotal:       time.Now(),
	}

	out := exec.forwardForDispatch(dctx, candidate, "no-cap-attempt", func() {})
	if out.Err == nil {
		t.Fatalf("forward outcome err = nil, want capability rejection")
	}
	if called {
		t.Fatal("upstream was contacted; dispatch gate must reject before executeOpenAI")
	}
}

func TestDispatchReducerDuplicateAppliesOnce(t *testing.T) {
	capture := &dispatchHealthCapture{}
	exec := &Executor{NodeOutcomeReducer: nodehealth.NewOutcomeReducer(), NodeHealthAdapter: capture}
	observation := nodehealth.Observation{
		Node:      nodehealth.NodeKey{TenantID: "tenant-a", ProviderID: 7, CredentialID: 22, Model: "model-a"},
		AttemptID: "attempt-duplicate", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
	}
	first, applied := exec.reduceDispatchOutcome(context.Background(), observation)
	if !applied || !first.Accepted {
		t.Fatalf("first reduction = %+v, applied=%v", first, applied)
	}
	duplicate, applied := exec.reduceDispatchOutcome(context.Background(), observation)
	if applied || duplicate.Accepted || !duplicate.Duplicate {
		t.Fatalf("duplicate reduction = %+v, applied=%v", duplicate, applied)
	}
	if got := len(capture.snapshot()); got != 1 {
		t.Fatalf("adapter calls = %d, want 1", got)
	}
}

func TestCopyQueueTimestampsToError(t *testing.T) {
	qr := dispatch.NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	t1 := time.Now().Add(-100 * time.Millisecond)
	t6 := time.Now().Add(-10 * time.Millisecond)
	t9 := time.Now()
	qr.SetReqStageTime(dispatch.ReqStageTotalEnqueued, t1)
	qr.SetReqStageTime(dispatch.ReqStageCredDequeued, t6)
	qr.SetReqStageTime(dispatch.ReqStageResponseEnd, t9)

	ee := dispatchErrToExecuteError(dispatch.ErrNoRoute)
	copyQueueTimestampsToError(ee, qr)
	if ee.T0ArrivedAt == nil {
		t.Fatal("T0ArrivedAt missing")
	}
	if ee.T1TotalEnqueuedAt == nil || !ee.T1TotalEnqueuedAt.Equal(t1) {
		t.Fatalf("T1=%v", ee.T1TotalEnqueuedAt)
	}
	if ee.T6CredDequeuedAt == nil || !ee.T6CredDequeuedAt.Equal(t6) {
		t.Fatalf("T6=%v", ee.T6CredDequeuedAt)
	}
	if ee.T9ResponseEndAt == nil || !ee.T9ResponseEndAt.Equal(t9) {
		t.Fatalf("T9=%v", ee.T9ResponseEndAt)
	}
}

// TestDispatchErrToExecuteErrorPreservesUpstreamRateLimit pins the
// 2026-09-07 mock-system-test §5.3 fix: when the dispatch exhaustion chain
// terminates in an upstream 429, the ExecuteError must carry KindRateLimit
// so the handler returns HTTP 429 + Retry-After instead of 503
// model_not_found. Other upstream kinds keep the legacy transient mapping.
func TestDispatchErrToExecuteErrorPreservesUpstreamRateLimit(t *testing.T) {
	upstreamErr := &upstreampkg.Error{
		Kind:       errorsx.KindRateLimit,
		Message:    "Rate limit",
		StatusCode: 429,
		RetryAfter: 7 * time.Second,
	}

	ee := dispatchErrToExecuteError(&dispatch.ExhaustedError{Cause: upstreamErr})
	if !ee.Exhausted || ee.LastKind != errorsx.KindRateLimit {
		t.Fatalf("exhausted rate-limit mapping = (Exhausted=%v, LastKind=%q), want exhausted rate_limit", ee.Exhausted, ee.LastKind)
	}

	// Other upstream kinds deliberately keep the blanket transient mapping
	// (handler-side classification unchanged).
	ee = dispatchErrToExecuteError(&dispatch.ExhaustedError{
		Cause: &upstreampkg.Error{Kind: errorsx.KindUpstreamDown, StatusCode: 502},
	})
	if !ee.Exhausted || ee.LastKind != errorsx.KindTransient {
		t.Fatalf("non-rate-limit mapping = (Exhausted=%v, LastKind=%q), want exhausted transient", ee.Exhausted, ee.LastKind)
	}

	// Deeply wrapped (fmt.Errorf %w) still unwraps.
	ee = dispatchErrToExecuteError(fmt.Errorf("dispatch failed: %w", upstreamErr))
	if ee.LastKind != errorsx.KindRateLimit {
		t.Fatalf("wrapped rate-limit kind = %q, want rate_limit", ee.LastKind)
	}

	// Legacy sentinels keep their mappings.
	ee = dispatchErrToExecuteError(dispatch.ErrNoRoute)
	if !ee.Exhausted || ee.LastKind != errorsx.KindConcurrent {
		t.Fatalf("ErrNoRoute mapping = (Exhausted=%v, LastKind=%q), want exhausted concurrent", ee.Exhausted, ee.LastKind)
	}
}
