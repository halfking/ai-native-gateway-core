package bg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

func TestProbeServiceDirectFailureKeepsNodeUnavailableAndBacksOff(t *testing.T) {
	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{errCode: "network_error"},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true},
		recorder.apply,
	)

	before := time.Now()
	result, err := service.Run(context.Background(), probeServiceTask(1))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ProbeQueueFailed || result.ReasonCode != "network_error" {
		t.Fatalf("result = %+v", result)
	}
	assertRetryDelay(t, result.NextRunAt, before, 5*time.Second)
	outcome := recorder.single(t)
	if outcome.success || outcome.direct.ok {
		t.Fatalf("outcome = %+v, want unavailable", outcome)
	}
}

func TestProbeServiceDirectSuccessGatewayFailureDoesNotRecover(t *testing.T) {
	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true, providerID: 7},
		gatewayProbeResult{round: nodeProbeRoundResult{errCode: "http_503", httpStatus: 503}, pinned: true},
		recorder.apply,
	)
	service.gatewayRoundFn = func(context.Context, int, string) gatewayProbeResult {
		if recorder.count() != 0 {
			t.Fatal("direct success applied recovery before gateway round")
		}
		return gatewayProbeResult{round: nodeProbeRoundResult{errCode: "http_503", httpStatus: 503}, pinned: true}
	}

	result, err := service.Run(context.Background(), probeServiceTask(2))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ProbeQueueFailed || result.ReasonCode != "http_503" || result.NextRunAt == nil {
		t.Fatalf("result = %+v", result)
	}
	if recorder.single(t).success {
		t.Fatal("gateway failure recovered node")
	}
}

func TestProbeServiceBothPinnedRoundsSuccessRecoversOnce(t *testing.T) {
	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true, providerID: 7, httpStatus: 200},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true},
		recorder.apply,
	)

	result, err := service.Run(context.Background(), probeServiceTask(3))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ProbeQueueSuccess || result.NextRunAt != nil {
		t.Fatalf("result = %+v", result)
	}
	if !recorder.single(t).success {
		t.Fatal("both successful pinned rounds did not recover node")
	}
}

func TestProbeServiceFallbackGatewayPinsCredential(t *testing.T) {
	var pin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pin = r.Header.Get("X-LLM-Pin-Credential")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	service := &ProbeService{worker: &NodeProbeWorker{
		baseURL: server.URL,
		apiKey:  "system-key",
		client:  server.Client(),
	}}
	gateway := service.gatewayRound(context.Background(), 42, "model-a")
	if !gateway.pinned || !gateway.round.ok {
		t.Fatalf("gateway = %+v, want pinned success", gateway)
	}
	if pin != "42" {
		t.Fatalf("pin header = %q, want 42", pin)
	}
}

func TestProbeServiceLegacyUnpinnedGatewayCannotRecover(t *testing.T) {
	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: false},
		recorder.apply,
	)

	result, err := service.Run(context.Background(), probeServiceTask(1))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ProbeQueueFailed || result.ReasonCode != "gateway_pin_unsupported" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.ReasonDetail, "cannot prove credential attribution") {
		t.Fatalf("reason detail = %q", result.ReasonDetail)
	}
	if recorder.single(t).success {
		t.Fatal("legacy unpinned gateway recovered node")
	}
}

func TestProbeServiceDefaultOutcomeHooksRecoverOnDirectSuccess(t *testing.T) {
	observer := &probeStateObserver{}
	var invalidations, circuitRecoveries int
	worker := &NodeProbeWorker{
		stateObserver:            observer,
		invalidateCandidateCache: func(int) { invalidations++ },
		recordCircuitSuccess:     func(int, int) { circuitRecoveries++ },
	}
	service := &ProbeService{worker: worker}

	service.applyOutcome(context.Background(), probeOutcome{
		credentialID: 42, model: "model-a", direct: nodeProbeRoundResult{ok: true, providerID: 7},
		gateway: nodeProbeRoundResult{errCode: "http_503"}, recoverAt: time.Now().Add(5 * time.Minute),
	})
	recoveredDirect := observer.last(t)
	if !recoveredDirect.Available || recoveredDirect.HealthStatus != "healthy" {
		t.Fatalf("direct-ok observed state = %+v", recoveredDirect)
	}
	if invalidations != 1 || circuitRecoveries != 1 {
		t.Fatalf("direct-ok hooks: invalidations=%d circuit_recoveries=%d", invalidations, circuitRecoveries)
	}

	service.applyOutcome(context.Background(), probeOutcome{
		credentialID: 42, model: "model-a", direct: nodeProbeRoundResult{ok: true, providerID: 7},
		gateway: nodeProbeRoundResult{ok: true}, success: true,
	})
	recovered := observer.last(t)
	if !recovered.Available || recovered.HealthStatus != "healthy" {
		t.Fatalf("recovered observed state = %+v", recovered)
	}
	if invalidations != 2 || circuitRecoveries != 2 {
		t.Fatalf("success hooks: invalidations=%d circuit_recoveries=%d", invalidations, circuitRecoveries)
	}
}

func TestProbeServiceFailureBackoffChain(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{{1, 5 * time.Second}, {3, 30 * time.Second}, {8, time.Minute}} {
		t.Run(tc.want.String(), func(t *testing.T) {
			service := newTestProbeService(
				nodeProbeRoundResult{errCode: "network_error"},
				gatewayProbeResult{round: nodeProbeRoundResult{errCode: "http_503"}, pinned: true},
				func(context.Context, probeOutcome) {},
			)
			before := time.Now()
			result, err := service.Run(context.Background(), probeServiceTask(tc.attempt))
			if err != nil {
				t.Fatal(err)
			}
			assertRetryDelay(t, result.NextRunAt, before, tc.want)
		})
	}
}

func newTestProbeService(direct nodeProbeRoundResult, gateway gatewayProbeResult, apply func(context.Context, probeOutcome)) *ProbeService {
	return &ProbeService{
		worker:         &NodeProbeWorker{},
		directRoundFn:  func(context.Context, int, string) nodeProbeRoundResult { return direct },
		gatewayRoundFn: func(context.Context, int, string) gatewayProbeResult { return gateway },
		applyOutcomeFn: apply,
	}
}

func probeServiceTask(attempt int) ProbeQueueTask {
	return ProbeQueueTask{CredentialID: 42, RawModel: "nodehealth-test-model", Attempt: attempt}
}

type probeStateObserver struct {
	credentialstate.StateObserver
	mu     sync.Mutex
	states []*credentialstate.State
}

func (o *probeStateObserver) UpdateFromProbe(_ context.Context, state *credentialstate.State) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.states = append(o.states, state)
}

func (o *probeStateObserver) last(t *testing.T) *credentialstate.State {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.states) == 0 {
		t.Fatal("no observed state")
	}
	return o.states[len(o.states)-1]
}

type probeOutcomeRecorder struct {
	mu       sync.Mutex
	outcomes []probeOutcome
}

func (r *probeOutcomeRecorder) apply(_ context.Context, outcome probeOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
}

func (r *probeOutcomeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.outcomes)
}

func (r *probeOutcomeRecorder) single(t *testing.T) probeOutcome {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(r.outcomes))
	}
	return r.outcomes[0]
}

func assertRetryDelay(t *testing.T, next *time.Time, before time.Time, want time.Duration) {
	t.Helper()
	if next == nil {
		t.Fatal("next retry is nil")
	}
	got := next.Sub(before)
	if got < want || got > want+time.Second {
		t.Fatalf("retry delay = %v, want %v (+ scheduling tolerance)", got, want)
	}
}
