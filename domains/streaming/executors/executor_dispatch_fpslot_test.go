package executors

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

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
		W:           httptest.NewRecorder(),
		R:           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:   []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}]}`),
		ClientModel: "glm-5.2",
		Model:       "glm-5.2",
		RequestID:   "dispatch-fp-race",
		TenantID:    "default",
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

	outcome := exec.forwardForDispatch(dctx, candidate, "dispatch-fp-attempt", func() {})
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
